package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

func (t *ToolSet) httpRequestTool() actool.CoreTool {
	return t.httpRequestToolWithVerification(nil)
}

func (t *ToolSet) httpRequestToolWithVerification(flow *verificationHTTPOptions) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "http_request",
		Description: "原生 HTTP/HTTPS 请求工具（优先替代 curl / Python requests 脚本）。支持任意 HTTP 方法、自定义 Header(含 Host 头覆盖)、Cookie、URL/表单参数、原始 Body 或 Base64 二进制 Body、multipart/form-data 文件上传、重定向跳转轨迹追踪及响应体正则提取。默认自动写入平台流量库并返回 exchange_id 供漏洞报告绑定证据，彻底避免 PowerShell/Bash 引号转义与 & 截断问题。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":              map[string]any{"type": "string", "description": "目标完整 URL（必须以 http:// 或 https:// 开头）"},
				"http_version":     map[string]any{"type": "string", "enum": []string{"auto", "1.1", "2"}, "description": "可选协议兼容策略；省略保持原行为，auto 协商 HTTP/1.1 或 HTTP/2，2 仅支持 HTTPS；不伪造 TLS 指纹"},
				"method":           map[string]any{"type": "string", "description": "HTTP 方法，默认 GET（如 GET/POST/PUT/DELETE/PATCH/OPTIONS/HEAD/PROPFIND）"},
				"headers":          map[string]any{"type": "object", "description": "自定义请求头键值对（支持设置 Host 覆盖虚拟主机）"},
				"cookies":          map[string]any{"type": "object", "description": "附加 Cookie 键值对"},
				"query":            map[string]any{"type": "object", "description": "附加到 URL 的查询参数键值对"},
				"body":             map[string]any{"type": "string", "description": "原始请求体字符串（JSON、XML、表单或任意载荷）"},
				"body_base64":      map[string]any{"type": "string", "description": "可选：Base64 编码的原始二进制请求体"},
				"form":             map[string]any{"type": "object", "description": "application/x-www-form-urlencoded 表单字段键值对"},
				"multipart":        map[string]any{"type": "array", "description": "multipart/form-data 上传字段列表，每项支持 {name, value, filename, content_type, file_path, base64_data}", "items": map[string]any{"type": "object"}},
				"follow_redirects": map[string]any{"type": "boolean", "description": "是否跟随 3xx 重定向，默认 false（便于观察 302 跳转、Location 与 Set-Cookie）"},
				"max_redirects":    map[string]any{"type": "integer", "description": "follow_redirects=true 时的最大跳转次数，默认 5"},
				"timeout_ms":       map[string]any{"type": "integer", "description": "超时时间（毫秒），默认 15000，最大 60000"},
				"timeout_sec":      map[string]any{"type": "integer", "description": "超时时间（秒，兼容字段），默认 15，最大 60"},
				"use_proxy":        map[string]any{"type": "boolean", "description": "是否使用平台代理，默认 true"},
				"proxy":            map[string]any{"type": "string", "description": "显式指定代理地址（可选，如 http://127.0.0.1:8080）"},
				"extract_regex":    map[string]any{"type": "string", "description": "可选：对响应头+响应体执行的正则表达式（如提取 CSRF Token、Session、内网地址）"},
				"max_body_bytes":   map[string]any{"type": "integer", "description": "返回响应体的最大字节数，默认 16384，最大 131072"},
			},
			"required": []any{"url"},
		},
		ReadOnly:    noStoreRo,
		Permissions: allowAllPerm,
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var a struct {
				URL             string              `json:"url"`
				Method          string              `json:"method"`
				HTTPVersion     string              `json:"http_version"`
				Headers         map[string]string   `json:"headers"`
				Cookies         map[string]string   `json:"cookies"`
				Query           map[string]string   `json:"query"`
				Body            string              `json:"body"`
				BodyBase64      string              `json:"body_base64"`
				Form            map[string]string   `json:"form"`
				Multipart       []multipartItemSpec `json:"multipart"`
				FollowRedirects bool                `json:"follow_redirects"`
				MaxRedirects    int                 `json:"max_redirects"`
				TimeoutMs       int                 `json:"timeout_ms"`
				TimeoutSec      int                 `json:"timeout_sec"`
				UseProxy        *bool               `json:"use_proxy"`
				Proxy           string              `json:"proxy"`
				ExtractRegex    string              `json:"extract_regex"`
				MaxBodyBytes    int                 `json:"max_body_bytes"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			rawURL := strings.TrimSpace(a.URL)
			if rawURL == "" {
				return actool.Errorf("url 为必填参数"), nil
			}
			parsedURL, err := url.Parse(rawURL)
			if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
				return actool.Errorf("url 必须是合法的 http:// 或 https:// 地址"), nil
			}
			if a.HTTPVersion != "" && a.HTTPVersion != "auto" && a.HTTPVersion != "1.1" && a.HTTPVersion != "2" {
				return actool.Errorf("http_version 必须是 auto、1.1 或 2"), nil
			}
			if a.HTTPVersion == "2" && parsedURL.Scheme != "https" {
				return actool.Errorf("HTTP/2 显式策略仅支持 HTTPS，不自动启用 h2c"), nil
			}
			if len(a.Query) > 0 {
				q := parsedURL.Query()
				for k, v := range a.Query {
					q.Set(k, v)
				}
				parsedURL.RawQuery = q.Encode()
			}
			method := strings.ToUpper(strings.TrimSpace(a.Method))
			if method == "" {
				method = "GET"
			}
			timeoutDur := 15 * time.Second
			if a.TimeoutMs > 0 {
				ms := defaultInt(a.TimeoutMs, 15000, 500, 60000)
				timeoutDur = time.Duration(ms) * time.Millisecond
			} else if a.TimeoutSec > 0 {
				sec := defaultInt(a.TimeoutSec, 15, 1, 60)
				timeoutDur = time.Duration(sec) * time.Second
			}
			maxBody := defaultInt(a.MaxBodyBytes, 16384, 512, 131072)

			var bodyBytes []byte
			var contentType string
			if len(a.Multipart) > 0 {
				b, ct, err := t.buildMultipartBody(a.Multipart)
				if err != nil {
					return actool.Errorf("构造 multipart 失败: " + err.Error()), nil
				}
				bodyBytes = b
				contentType = ct
			} else if len(a.Form) > 0 {
				vals := url.Values{}
				for k, v := range a.Form {
					vals.Set(k, v)
				}
				bodyBytes = []byte(vals.Encode())
				contentType = "application/x-www-form-urlencoded"
			} else if strings.TrimSpace(a.BodyBase64) != "" {
				raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(a.BodyBase64))
				if err != nil {
					return actool.Errorf("解码 body_base64 失败: " + err.Error()), nil
				}
				bodyBytes = raw
			} else if a.Body != "" {
				bodyBytes = []byte(a.Body)
			}

			runCtx, cancel := context.WithTimeout(ctx, timeoutDur)
			defer cancel()

			var bodyReader io.Reader
			if len(bodyBytes) > 0 {
				bodyReader = bytes.NewReader(bodyBytes)
			}
			req, err := http.NewRequestWithContext(runCtx, method, parsedURL.String(), bodyReader)
			if err != nil {
				return actool.Errorf("创建请求失败: " + err.Error()), nil
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
			for k, v := range a.Headers {
				if strings.EqualFold(strings.TrimSpace(k), "Host") {
					req.Host = v
				} else {
					req.Header.Set(k, v)
				}
			}
			if len(a.Cookies) > 0 {
				keys := make([]string, 0, len(a.Cookies))
				for k := range a.Cookies {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				parts := make([]string, 0, len(keys))
				for _, k := range keys {
					parts = append(parts, k+"="+a.Cookies[k])
				}
				existing := req.Header.Get("Cookie")
				if existing != "" {
					req.Header.Set("Cookie", existing+"; "+strings.Join(parts, "; "))
				} else {
					req.Header.Set("Cookie", strings.Join(parts, "; "))
				}
			}

			// 若已挂载旁路直写流量存储（t.trafficStore != nil）且未显式指定外部代理，
			// 则直连目标并在请求完成后通过 RecordDirect 异步写入 SQLite 流量库，省去本地 MITM 双重 TLS 开销。
			directRecord := t != nil && t.trafficStore != nil && strings.TrimSpace(a.Proxy) == "" && (a.UseProxy == nil || *a.UseProxy)
			var proxyURL, caCert string
			if !directRecord {
				proxyURL, caCert = t.resolveHTTPSettings(a.Proxy, a.UseProxy)
			}
			tr := compatibilityTransport(t.getPooledTransport(proxyURL, caCert, true), a.HTTPVersion)
			maxRedir := defaultInt(a.MaxRedirects, 5, 1, 20)
			var redirectChain []string
			var roundTripper http.RoundTripper = tr
			var jar http.CookieJar
			if flow != nil {
				var base http.RoundTripper
				if flow.profile == "" {
					if flow.transport == nil {
						flow.transport = verificationBoundedTransport(tr)
					}
					base = flow.transport
				} else {
					if flow.helper == nil {
						var err error
						flow.helper, err = startVerificationHelper(flow.ctx, flow.profile, a.HTTPVersion, proxyURL, caCert, flow.budget.origin)
						if err != nil {
							flow.failureCode = verificationIPCCode(err)
							return actool.Errorf("fingerprint provider unavailable or rejected configuration"), nil
						}
					}
					base = flow.helper
				}
				roundTripper = verificationRoundTripper{base: base, budget: flow.budget}
				jar = flow.jar
			}
			client := &http.Client{
				Transport: roundTripper,
				Jar:       jar,
				Timeout:   timeoutDur,
				CheckRedirect: func(r *http.Request, via []*http.Request) error {
					if flow != nil {
						if r == nil || r.URL == nil {
							return http.ErrUseLastResponse
						}
						origin, err := verificationOrigin(r.URL.String())
						if err != nil || origin != flow.budget.origin || (r.Method != "GET" && r.Method != "HEAD") {
							return http.ErrUseLastResponse
						}
					}
					if r != nil && r.URL != nil {
						redirectChain = append(redirectChain, r.URL.String())
					}
					if !a.FollowRedirects {
						return http.ErrUseLastResponse
					}
					if len(via) >= maxRedir {
						return http.ErrUseLastResponse
					}
					return nil
				},
			}

			reqHeaderSnap := req.Header.Clone()
			if req.Host != "" {
				reqHeaderSnap.Set("Host", req.Host)
			}

			start := time.Now()
			resp, err := client.Do(req)
			elapsedMs := time.Since(start).Milliseconds()
			if err != nil {
				return actool.Errorf("HTTP 请求执行失败: " + err.Error()), nil
			}
			defer resp.Body.Close()

			maxCaptureRead := 2 * 1024 * 1024
			if flow != nil {
				maxCaptureRead = verificationFlowCapture
			}
			rawResp, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(maxCaptureRead)+1))
			if flow != nil && readErr != nil {
				return actool.Errorf("verification response read failed; no business success is claimed"), nil
			}

			recordMethod, recordURL, recordBody := method, parsedURL.String(), bodyBytes
			if flow != nil && resp.Request != nil {
				recordMethod = resp.Request.Method
				recordURL = resp.Request.URL.String()
				reqHeaderSnap = resp.Request.Header.Clone()
				if resp.Request.Host != "" {
					reqHeaderSnap.Set("Host", resp.Request.Host)
				}
				if recordMethod != method {
					recordBody = nil
				}
			}
			var exchangeID string
			if directRecord {
				exchangeID = t.trafficStore.RecordDirect(
					recordMethod,
					recordURL,
					resp.Proto,
					reqHeaderSnap,
					recordBody,
					resp.StatusCode,
					resp.Header.Clone(),
					rawResp,
				)
			}

			truncated := len(rawResp) > maxBody
			bodySlice := rawResp
			if truncated {
				bodySlice = rawResp[:maxBody]
			}
			bodyStr := clipUTF8(string(bodySlice), maxBody)

			respHeaders := make(map[string]string, len(resp.Header))
			for k, vs := range resp.Header {
				respHeaders[k] = strings.Join(vs, "; ")
			}

			var extracted []string
			if rxStr := strings.TrimSpace(a.ExtractRegex); rxStr != "" {
				re, err := regexp.Compile(rxStr)
				if err != nil {
					return actool.Errorf("extract_regex 正则语法错误: " + err.Error()), nil
				}
				var headerBuf strings.Builder
				for k, vs := range resp.Header {
					for _, v := range vs {
						headerBuf.WriteString(k)
						headerBuf.WriteString(": ")
						headerBuf.WriteString(v)
						headerBuf.WriteByte('\n')
					}
				}
				searchTarget := headerBuf.String() + "\n" + string(rawResp)
				matches := re.FindAllStringSubmatch(searchTarget, 50)
				seen := make(map[string]struct{}, len(matches))
				for _, m := range matches {
					val := m[0]
					if len(m) > 1 {
						val = strings.Join(m[1:], " | ")
					}
					if _, ok := seen[val]; !ok {
						seen[val] = struct{}{}
						extracted = append(extracted, val)
					}
				}
			}

			out := map[string]any{
				"human_verification": inspectVerification(resp.StatusCode, respHeaders, string(rawResp)),
				"exchange_id":        exchangeID,
				"status":             resp.StatusCode,
				"status_text":        resp.Status,
				"proto":              resp.Proto,
				"url":                parsedURL.String(),
				"final_url":          resp.Request.URL.String(),
				"redirect_chain":     redirectChain,
				"elapsed_ms":         elapsedMs,
				"content_length":     resp.ContentLength,
				"headers":            respHeaders,
				"body_preview":       clipUTF8(bodyStr, 2048),
				"body_length":        len(rawResp),
				"body":               bodyStr,
				"body_truncated":     truncated,
			}
			if len(extracted) > 0 {
				out["extracted"] = extracted
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			if tc != nil {
				return actool.Text(actool.Capture(tc, string(b))), nil
			}
			return actool.Text(string(b)), nil
		},
	})
}

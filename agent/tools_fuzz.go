package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

func (t *ToolSet) httpFuzzTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "http_fuzz",
		Description: "受控并发 HTTP Fuzz/目录扫描/参数枚举/IDOR 遍历（严禁用 Python ThreadPoolExecutor/asyncio 写并发发包脚本，也不要用 http_request 循环代替）。在 url_template、headers 或 body_template 中使用 {{FUZZ}} 占位符，支持 payloads 数组、本地字典文件 wordlist_file 或数字序列 range_spec（如 '1-200' 或 '000-999'）。内置自动软 404 基线校准（auto_calibrate）及按需流量入库（404 噪声不写库，仅命中的异常响应自动写入流量库并返回 exchange_id）。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url_template":   map[string]any{"type": "string", "description": "含 {{FUZZ}} 占位符的目标 URL（如 https://example.com/api/user/{{FUZZ}}）"},
				"url":            map[string]any{"type": "string", "description": "url_template 的兼容别名"},
				"method":         map[string]any{"type": "string", "description": "HTTP 方法，默认 GET"},
				"headers":        map[string]any{"type": "object", "description": "请求头键值对（值中可包含 {{FUZZ}}）"},
				"cookies":        map[string]any{"type": "object", "description": "Cookie 键值对（值中可包含 {{FUZZ}}）"},
				"body_template":  map[string]any{"type": "string", "description": "请求体模板（可包含 {{FUZZ}}）"},
				"body":           map[string]any{"type": "string", "description": "body_template 的兼容别名"},
				"payloads":       map[string]any{"type": "array", "description": "内联测试载荷数组（单次最多 500 条）", "items": map[string]any{"type": "string"}},
				"wordlist_file":  map[string]any{"type": "string", "description": "可选：从本地字典文件逐行读取载荷（自动跳过空行与 # 注释）"},
				"range_spec":     map[string]any{"type": "string", "description": "可选：数字区间生成载荷，支持 '1-200'、'000-999'（自动按起止前导零补齐宽度）或 'start-end:step:width'（如 '0-999:1:4'）"},
				"concurrency":    map[string]any{"type": "integer", "description": "并发协程数，默认 8，最大 16（受平台全局信号量节流保护）"},
				"timeout_ms":     map[string]any{"type": "integer", "description": "单个请求超时时间（毫秒），默认 8000"},
				"timeout_sec":    map[string]any{"type": "integer", "description": "单个请求超时时间（秒，兼容字段），默认 8"},
				"match_status":   map[string]any{"type": "array", "description": "仅保留匹配这些状态码的响应", "items": map[string]any{"type": "integer"}},
				"filter_status":  map[string]any{"type": "array", "description": "过滤排除这些状态码（默认排除 [404]）", "items": map[string]any{"type": "integer"}},
				"filter_length":  map[string]any{"type": "array", "description": "过滤排除指定字节长度的响应", "items": map[string]any{"type": "integer"}},
				"filter_words":   map[string]any{"type": "array", "description": "过滤排除指定词数的响应", "items": map[string]any{"type": "integer"}},
				"filter_lines":   map[string]any{"type": "array", "description": "过滤排除指定行数的响应", "items": map[string]any{"type": "integer"}},
				"match_regex":    map[string]any{"type": "string", "description": "仅保留响应体匹配该正则的结果"},
				"filter_regex":   map[string]any{"type": "string", "description": "过滤排除响应体匹配该正则的结果"},
				"auto_calibrate": map[string]any{"type": "boolean", "description": "是否自动发送随机载荷探测软 404 基线并自动过滤同构噪声，默认 true"},
				"use_proxy":      map[string]any{"type": "boolean", "description": "是否强制所有探测请求走代理，默认 false（仅命中项按需直写流量库，避免 404 噪声阻塞代理与 FTS5）"},
			},
		},
		ReadOnly:    noStoreRo,
		Permissions: allowAllPerm,
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			t.steer.markTool("http_fuzz")
			var a struct {
				URLTemplate   string            `json:"url_template"`
				URL           string            `json:"url"`
				Method        string            `json:"method"`
				Headers       map[string]string `json:"headers"`
				Cookies       map[string]string `json:"cookies"`
				BodyTemplate  string            `json:"body_template"`
				Body          string            `json:"body"`
				Payloads      []string          `json:"payloads"`
				WordlistFile  string            `json:"wordlist_file"`
				RangeSpec     string            `json:"range_spec"`
				Concurrency   int               `json:"concurrency"`
				TimeoutMs     int               `json:"timeout_ms"`
				TimeoutSec    int               `json:"timeout_sec"`
				MatchStatus   []int             `json:"match_status"`
				FilterStatus  []int             `json:"filter_status"`
				FilterLength  []int             `json:"filter_length"`
				FilterWords   []int             `json:"filter_words"`
				FilterLines   []int             `json:"filter_lines"`
				MatchRegex    string            `json:"match_regex"`
				FilterRegex   string            `json:"filter_regex"`
				AutoCalibrate *bool             `json:"auto_calibrate"`
				UseProxy      *bool             `json:"use_proxy"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			rawURL := firstNonEmpty(a.URLTemplate, a.URL)
			if rawURL == "" {
				return actool.Errorf("url_template 为必填参数"), nil
			}
			if u, err := url.Parse(strings.ReplaceAll(rawURL, "{{FUZZ}}", "test")); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return actool.Errorf("url_template 必须是合法的 http:// 或 https:// 地址"), nil
			}
			bodyTmpl := firstNonEmpty(a.BodyTemplate, a.Body)

			var list []string
			list = append(list, a.Payloads...)
			if wf := strings.TrimSpace(a.WordlistFile); wf != "" {
				raw, err := os.ReadFile(t.resolveLocalPath(wf))
				if err != nil {
					return actool.Errorf("读取 wordlist_file 失败: " + err.Error()), nil
				}
				for _, line := range strings.Split(string(raw), "\n") {
					line = strings.TrimRight(line, "\r")
					trimmed := strings.TrimSpace(line)
					if trimmed == "" || strings.HasPrefix(trimmed, "#") {
						continue
					}
					list = append(list, line)
				}
			}
			if rs := strings.TrimSpace(a.RangeSpec); rs != "" {
				gen, err := expandRangeSpec(rs)
				if err != nil {
					return actool.Errorf("解析 range_spec 失败: " + err.Error()), nil
				}
				list = append(list, gen...)
			}
			if len(list) == 0 {
				return actool.Errorf("未提供任何载荷：请至少指定 payloads、wordlist_file 或 range_spec 之一"), nil
			}
			const maxPayloadsPerCall = 500
			truncatedPayloads := false
			if len(list) > maxPayloadsPerCall {
				list = list[:maxPayloadsPerCall]
				truncatedPayloads = true
			}

			var matchRe, filterRe *regexp.Regexp
			if s := strings.TrimSpace(a.MatchRegex); s != "" {
				r, err := regexp.Compile(s)
				if err != nil {
					return actool.Errorf("match_regex 正则语法错误: " + err.Error()), nil
				}
				matchRe = r
			}
			if s := strings.TrimSpace(a.FilterRegex); s != "" {
				r, err := regexp.Compile(s)
				if err != nil {
					return actool.Errorf("filter_regex 正则语法错误: " + err.Error()), nil
				}
				filterRe = r
			}

			method := strings.ToUpper(strings.TrimSpace(a.Method))
			if method == "" {
				method = "GET"
			}
			concurrency := defaultInt(a.Concurrency, 8, 1, 16)
			timeoutDur := 8 * time.Second
			if a.TimeoutMs > 0 {
				ms := defaultInt(a.TimeoutMs, 8000, 500, 30000)
				timeoutDur = time.Duration(ms) * time.Millisecond
			} else if a.TimeoutSec > 0 {
				sec := defaultInt(a.TimeoutSec, 8, 1, 30)
				timeoutDur = time.Duration(sec) * time.Second
			}

			// 默认在 Fuzz 期间直连目标（绕过本地 MITM 代理），仅将过滤后命中的异常响应通过 RecordDirect 写入流量库。
			wantProxy := false
			if a.UseProxy != nil {
				wantProxy = *a.UseProxy
			}
			var proxyURL, caCert string
			if wantProxy {
				proxyURL, caCert = t.resolveHTTPSettings("", a.UseProxy)
			}
			tr := t.getPooledTransport(proxyURL, caCert, true)
			client := &http.Client{
				Transport: tr,
				Timeout:   timeoutDur,
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}

			doProbe := func(pCtx context.Context, payload string) (status int, length, words, lines int, location, sample string, reqH, respH http.Header, reqB, respB []byte, targetURL, proto string, elapsedMs int64, err error) {
				targetURL = strings.ReplaceAll(rawURL, "{{FUZZ}}", payload)
				if !strings.Contains(rawURL, "{{FUZZ}}") && bodyTmpl == "" && !hasFuzzPlaceholder(a.Headers) && !hasFuzzPlaceholder(a.Cookies) {
					targetURL = strings.TrimRight(rawURL, "/") + "/" + strings.TrimLeft(payload, "/")
				}
				reqBodyStr := strings.ReplaceAll(bodyTmpl, "{{FUZZ}}", payload)
				var bodyReader io.Reader
				if reqBodyStr != "" {
					reqB = []byte(reqBodyStr)
					bodyReader = strings.NewReader(reqBodyStr)
				}
				rCtx, cancel := context.WithTimeout(pCtx, timeoutDur)
				defer cancel()
				req, err := http.NewRequestWithContext(rCtx, method, targetURL, bodyReader)
				if err != nil {
					return 0, 0, 0, 0, "", "", nil, nil, nil, nil, targetURL, "", 0, err
				}
				req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
				for k, v := range a.Headers {
					rv := strings.ReplaceAll(v, "{{FUZZ}}", payload)
					if strings.EqualFold(strings.TrimSpace(k), "Host") {
						req.Host = rv
					} else {
						req.Header.Set(k, rv)
					}
				}
				if len(a.Cookies) > 0 {
					var cparts []string
					for k, v := range a.Cookies {
						cparts = append(cparts, k+"="+strings.ReplaceAll(v, "{{FUZZ}}", payload))
					}
					req.Header.Set("Cookie", strings.Join(cparts, "; "))
				}
				reqH = req.Header.Clone()
				if req.Host != "" {
					reqH.Set("Host", req.Host)
				}
				t0 := time.Now()
				resp, err := client.Do(req)
				elapsedMs = time.Since(t0).Milliseconds()
				if err != nil {
					return 0, 0, 0, 0, "", "", reqH, nil, reqB, nil, targetURL, "", elapsedMs, err
				}
				defer resp.Body.Close()
				raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
				bodyText := string(raw)
				status = resp.StatusCode
				length = len(raw)
				words = len(strings.Fields(bodyText))
				lines = strings.Count(bodyText, "\n") + 1
				location = resp.Header.Get("Location")
				sample = clipUTF8(strings.TrimSpace(bodyText), 240)
				return status, length, words, lines, location, sample, reqH, resp.Header.Clone(), reqB, raw, targetURL, resp.Proto, elapsedMs, nil
			}

			autoCal := true
			if a.AutoCalibrate != nil {
				autoCal = *a.AutoCalibrate
			}
			var calStatus, calLen, calWords, calLines int
			hasCal := false
			if autoCal {
				globalHTTPFuzzSem <- struct{}{}
				st, ln, wd, li, _, _, _, _, _, _, _, _, _, err := doProbe(ctx, "artex_nonexistent_probe_9f8e7d6c")
				<-globalHTTPFuzzSem
				if err == nil && st > 0 {
					hasCal = true
					calStatus, calLen, calWords, calLines = st, ln, wd, li
				}
			}

			matchSt := toIntSet(a.MatchStatus)
			filterSt := toIntSet(a.FilterStatus)
			if len(matchSt) == 0 && len(filterSt) == 0 {
				filterSt[404] = struct{}{}
			}
			filterLen := toIntSet(a.FilterLength)
			filterWds := toIntSet(a.FilterWords)
			filterLns := toIntSet(a.FilterLines)

			type fuzzHit struct {
				Index      int    `json:"index"`
				Payload    string `json:"payload"`
				ExchangeID string `json:"exchange_id,omitempty"`
				Status     int    `json:"status"`
				Length     int    `json:"length"`
				Words      int    `json:"words"`
				Lines      int    `json:"lines"`
				ElapsedMs  int64  `json:"elapsed_ms"`
				Location   string `json:"location,omitempty"`
				Sample     string `json:"sample,omitempty"`
			}

			var (
				mu          sync.Mutex
				hits        []fuzzHit
				errCount    int
				filteredCnt int
				wg          sync.WaitGroup
				localSem    = make(chan struct{}, concurrency)
			)

			startAll := time.Now()
			for idx, payload := range list {
				if ctx.Err() != nil {
					break
				}
				wg.Add(1)
				localSem <- struct{}{}
				globalHTTPFuzzSem <- struct{}{}
				go func(i int, p string) {
					defer wg.Done()
					defer func() {
						<-globalHTTPFuzzSem
						<-localSem
					}()
					if ctx.Err() != nil {
						return
					}
					st, ln, wd, li, loc, samp, reqH, respH, reqB, respB, targetURL, proto, ms, err := doProbe(ctx, p)
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						errCount++
						return
					}
					if len(matchSt) > 0 {
						if _, ok := matchSt[st]; !ok {
							filteredCnt++
							return
						}
					}
					if _, ok := filterSt[st]; ok {
						filteredCnt++
						return
					}
					if _, ok := filterLen[ln]; ok {
						filteredCnt++
						return
					}
					if _, ok := filterWds[wd]; ok {
						filteredCnt++
						return
					}
					if _, ok := filterLns[li]; ok {
						filteredCnt++
						return
					}
					if hasCal && st == calStatus {
						if ln == calLen || (wd == calWords && li == calLines) {
							filteredCnt++
							return
						}
					}
					if matchRe != nil && !matchRe.Match(respB) {
						filteredCnt++
						return
					}
					if filterRe != nil && filterRe.Match(respB) {
						filteredCnt++
						return
					}
					var exID string
					if t != nil && t.trafficStore != nil && !wantProxy && len(hits) < 100 {
						exID = t.trafficStore.RecordDirect(method, targetURL, proto, reqH, reqB, st, respH, respB)
					}
					hits = append(hits, fuzzHit{
						Index:      i,
						Payload:    p,
						ExchangeID: exID,
						Status:     st,
						Length:     ln,
						Words:      wd,
						Lines:      li,
						ElapsedMs:  ms,
						Location:   loc,
						Sample:     samp,
					})
				}(idx, payload)
			}
			wg.Wait()

			sort.Slice(hits, func(i, j int) bool { return hits[i].Index < hits[j].Index })
			const maxReturnedHits = 100
			hitsTruncated := false
			if len(hits) > maxReturnedHits {
				hits = hits[:maxReturnedHits]
				hitsTruncated = true
			}

			out := map[string]any{
				"total_tested":       len(list),
				"payloads_truncated": truncatedPayloads,
				"hits_count":         len(hits),
				"hits_truncated":     hitsTruncated,
				"filtered_count":     filteredCnt,
				"error_count":        errCount,
				"elapsed_ms":         time.Since(startAll).Milliseconds(),
				"hits":               hits,
			}
			if hasCal {
				out["baseline_calibration"] = map[string]any{
					"status": calStatus,
					"length": calLen,
					"words":  calWords,
					"lines":  calLines,
				}
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return actool.Text(actool.Capture(tc, string(b))), nil
		},
	})
}

func hasFuzzPlaceholder(m map[string]string) bool {
	for _, v := range m {
		if strings.Contains(v, "{{FUZZ}}") {
			return true
		}
	}
	return false
}

func expandRangeSpec(spec string) ([]string, error) {
	parts := strings.Split(spec, ":")
	rangePart := strings.TrimSpace(parts[0])
	dashIdx := strings.IndexByte(rangePart[1:], '-')
	if dashIdx < 0 {
		return nil, fmt.Errorf("range_spec 格式应为 start-end 或 start-end:step:width（如 1-200、000-999 或 0-999:1:4）")
	}
	dashIdx++
	startStr := strings.TrimSpace(rangePart[:dashIdx])
	endStr := strings.TrimSpace(rangePart[dashIdx+1:])
	start, err1 := strconv.Atoi(startStr)
	end, err2 := strconv.Atoi(endStr)
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("range_spec 起止值必须为整数")
	}
	step := 1
	if start > end {
		step = -1
	}
	width := 0
	if len(startStr) > 1 && strings.HasPrefix(startStr, "0") {
		width = len(startStr)
	}
	if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
		s, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || s == 0 {
			return nil, fmt.Errorf("range_spec step 必须为非零整数")
		}
		step = s
	}
	if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
		w, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err != nil || w < 0 || w > 32 {
			return nil, fmt.Errorf("range_spec width 必须为 0..32 之间的整数")
		}
		width = w
	}
	var out []string
	for cur := start; (step > 0 && cur <= end) || (step < 0 && cur >= end); cur += step {
		if width > 0 {
			out = append(out, fmt.Sprintf("%0*d", width, cur))
		} else {
			out = append(out, strconv.Itoa(cur))
		}
		if len(out) >= 1000 {
			break
		}
	}
	return out, nil
}

func toIntSet(list []int) map[int]struct{} {
	m := make(map[int]struct{}, len(list))
	for _, v := range list {
		m[v] = struct{}{}
	}
	return m
}

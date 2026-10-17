package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
	"github.com/tidwall/gjson"
)

type sequenceStepSpec struct {
	Name            string            `json:"name"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
	FollowRedirects bool              `json:"follow_redirects"`
	ExtractVars     map[string]string `json:"extract_vars"`
	ExpectStatus    int               `json:"expect_status"`
}

type bolaIdentitySpec struct {
	Name         string            `json:"name"`
	Cookie       string            `json:"cookie"`
	Headers      map[string]string `json:"headers"`
	StripHeaders []string          `json:"strip_headers"`
}

func (t *ToolSet) httpSequenceTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "http_sequence",
		Description: "原生多步有状态会话链、流量重放改包(Repeater)、BOLA/IDOR 多身份越权矩阵、并发条件竞争(Race Condition)、时间盲注基线验证与双响应差分对比工具（彻底替代 Python requests.Session / threading.Barrier / time.sleep / difflib 及 Burp Repeater/Autorize 脚本）。支持 6 种模式：chain（自动维护 CookieJar 会话，按序执行多步请求，支持 regex:/json: 提取变量并通过 {{var}} 引用）、replay（直接读取已有 exchange_id 的原始请求并按需覆盖 method/url/path/query/headers/cookie/body 后重放并对比原始响应）、bola（对指定 exchange_id 或请求自动使用多组身份 Cookie/Token 及未授权态重放，检测水平/垂直越权）、race（使用内存同步屏障 Gate 在同一微秒齐射 N 个并发请求，测双花/重放）、timing（先测多组基准 RTT 再测延时载荷 RTT）、diff（对比两个 exchange_id 或两段响应的差分与相似度）。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mode": map[string]any{
					"type":        "string",
					"description": "执行模式：chain（多步会话链）| replay（基于 exchange_id 改包重放）| bola（多身份越权矩阵重放）| race（并发条件竞争齐射）| timing（时间盲注基线测速）| diff（双响应差分比对）",
				},
				"steps": map[string]any{
					"type":        "array",
					"description": "mode=chain 时的步骤数组，每项支持 {name, method, url, headers, body, follow_redirects, extract_vars: {'csrf': 'regex:name=\"csrf\" value=\"([^\"]+)\"', 'token': 'json:data.token'}, expect_status}",
					"items":       map[string]any{"type": "object"},
				},
				"initial_vars":     map[string]any{"type": "object", "description": "mode=chain 时的初始模板变量键值对"},
				"base_exchange_id": map[string]any{"type": "string", "description": "mode=replay 或 mode=bola 时引用的基准流量 exchange_id（自动从流量库加载原始请求与基准响应）"},
				"override_method":  map[string]any{"type": "string", "description": "mode=replay 时覆盖的 HTTP 方法"},
				"override_url":     map[string]any{"type": "string", "description": "mode=replay 时覆盖的完整 URL（若仅改路径可用 override_path）"},
				"override_path":    map[string]any{"type": "string", "description": "mode=replay 时覆盖的 URL Path（含或不含 query）"},
				"override_query":   map[string]any{"type": "object", "description": "mode=replay 时覆盖或追加的 URL Query 参数键值对"},
				"override_headers": map[string]any{"type": "object", "description": "mode=replay 时覆盖或追加的请求头"},
				"override_cookie":  map[string]any{"type": "string", "description": "mode=replay 时覆盖的 Cookie 字符串"},
				"override_body":    map[string]any{"type": "string", "description": "mode=replay 时覆盖的请求体"},
				"strip_headers":    map[string]any{"type": "array", "description": "mode=replay 时要删除的请求头名称列表（如 ['Authorization', 'Cookie']）", "items": map[string]any{"type": "string"}},
				"identities": map[string]any{
					"type":        "array",
					"description": "mode=bola 时的测试身份数组，每项支持 {name: 'userB/anon', cookie: '...', headers: {'Authorization': '...'}, strip_headers: ['Cookie', 'Authorization']}；不传时默认测试剥离认证头(unauthenticated)",
					"items":       map[string]any{"type": "object"},
				},
				"url":           map[string]any{"type": "string", "description": "mode=race / timing / bola(未传 base_exchange_id 时) 的目标 URL"},
				"method":        map[string]any{"type": "string", "description": "mode=race / timing / bola 的 HTTP 方法"},
				"headers":       map[string]any{"type": "object", "description": "mode=race / timing / bola 的请求头"},
				"body":          map[string]any{"type": "string", "description": "mode=race / timing / bola 的请求体"},
				"race_count":    map[string]any{"type": "integer", "description": "mode=race 时同步齐射的并发请求数，默认 15，最大 30"},
				"baseline_url":  map[string]any{"type": "string", "description": "mode=timing 时的基线对照 URL（不传则同 url）"},
				"baseline_body": map[string]any{"type": "string", "description": "mode=timing 时的基线对照请求体（如 sleep(0)）"},
				"baseline_runs": map[string]any{"type": "integer", "description": "mode=timing 时基线采样次数，默认 3，最大 5"},
				"exchange_id_a": map[string]any{"type": "string", "description": "mode=diff 时的基准流量 exchange_id"},
				"exchange_id_b": map[string]any{"type": "string", "description": "mode=diff 时的对照流量 exchange_id"},
				"text_a":        map[string]any{"type": "string", "description": "mode=diff 时的基准文本（与 exchange_id_a 二选一）"},
				"text_b":        map[string]any{"type": "string", "description": "mode=diff 时的对照文本（与 exchange_id_b 二选一）"},
				"timeout_ms":    map[string]any{"type": "integer", "description": "单次请求超时（毫秒），默认 15000"},
			},
			"required": []any{"mode"},
		},
		ReadOnly:    noStoreRo,
		Permissions: allowAllPerm,
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Mode            string             `json:"mode"`
				Steps           []sequenceStepSpec `json:"steps"`
				InitialVars     map[string]string  `json:"initial_vars"`
				BaseExchangeID  string             `json:"base_exchange_id"`
				OverrideMethod  string             `json:"override_method"`
				OverrideURL     string             `json:"override_url"`
				OverridePath    string             `json:"override_path"`
				OverrideQuery   map[string]string  `json:"override_query"`
				OverrideHeaders map[string]string  `json:"override_headers"`
				OverrideCookie  string             `json:"override_cookie"`
				OverrideBody    *string            `json:"override_body"`
				StripHeaders    []string           `json:"strip_headers"`
				Identities      []bolaIdentitySpec `json:"identities"`
				URL             string             `json:"url"`
				Method          string             `json:"method"`
				Headers         map[string]string  `json:"headers"`
				Body            string             `json:"body"`
				RaceCount       int                `json:"race_count"`
				BaselineURL     string             `json:"baseline_url"`
				BaselineBody    string             `json:"baseline_body"`
				BaselineRuns    int                `json:"baseline_runs"`
				ExchangeIDA     string             `json:"exchange_id_a"`
				ExchangeIDB     string             `json:"exchange_id_b"`
				TextA           string             `json:"text_a"`
				TextB           string             `json:"text_b"`
				TimeoutMs       int                `json:"timeout_ms"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			mode := strings.ToLower(strings.TrimSpace(a.Mode))
			if mode == "" {
				return actool.Errorf("mode 为必填参数（chain | replay | bola | race | timing | diff）"), nil
			}
			timeoutDur := time.Duration(defaultInt(a.TimeoutMs, 15000, 500, 60000)) * time.Millisecond

			switch mode {
			case "chain":
				if len(a.Steps) == 0 {
					return actool.Errorf("mode=chain 时 steps 不能为空"), nil
				}
				if len(a.Steps) > 20 {
					a.Steps = a.Steps[:20]
				}
				res, err := t.runHTTPChain(ctx, a.Steps, a.InitialVars, timeoutDur)
				if err != nil {
					return actool.Errorf("chain 执行失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "replay":
				exID := strings.TrimSpace(a.BaseExchangeID)
				if exID == "" {
					return actool.Errorf("mode=replay 时 base_exchange_id 为必填参数"), nil
				}
				res, err := t.runExchangeReplay(ctx, exID, a.OverrideMethod, a.OverrideURL, a.OverridePath, a.OverrideQuery, a.OverrideHeaders, a.OverrideCookie, a.OverrideBody, a.StripHeaders, timeoutDur)
				if err != nil {
					return actool.Errorf("replay 执行失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "bola":
				res, err := t.runBOLAMatrix(ctx, strings.TrimSpace(a.BaseExchangeID), a.Method, a.URL, a.Headers, a.Body, a.Identities, timeoutDur)
				if err != nil {
					return actool.Errorf("bola 执行失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "race":
				rawURL := strings.TrimSpace(a.URL)
				if rawURL == "" {
					return actool.Errorf("mode=race 时 url 为必填参数"), nil
				}
				if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					return actool.Errorf("url 必须是合法的 http:// 或 https:// 地址"), nil
				}
				method := strings.ToUpper(strings.TrimSpace(a.Method))
				if method == "" {
					method = "POST"
				}
				count := defaultInt(a.RaceCount, 15, 2, 30)
				res, err := t.runHTTPRace(ctx, method, rawURL, a.Headers, a.Body, count, timeoutDur)
				if err != nil {
					return actool.Errorf("race 执行失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "timing":
				rawURL := strings.TrimSpace(a.URL)
				if rawURL == "" {
					return actool.Errorf("mode=timing 时 url 为必填参数"), nil
				}
				if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					return actool.Errorf("url 必须是合法的 http:// 或 https:// 地址"), nil
				}
				method := strings.ToUpper(strings.TrimSpace(a.Method))
				if method == "" {
					method = "GET"
				}
				baseURL := firstNonEmpty(a.BaselineURL, rawURL)
				runs := defaultInt(a.BaselineRuns, 3, 1, 5)
				res, err := t.runHTTPTiming(ctx, method, baseURL, a.BaselineBody, rawURL, a.Body, a.Headers, runs, timeoutDur)
				if err != nil {
					return actool.Errorf("timing 执行失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "diff":
				textA := a.TextA
				textB := a.TextB
				if exA := strings.TrimSpace(a.ExchangeIDA); exA != "" {
					if t == nil || t.trafficStore == nil {
						return actool.Errorf("当前环境未挂载流量存储，无法按 exchange_id_a 读取"), nil
					}
					_, respA, err := t.trafficStore.Get(exA)
					if err != nil {
						return actool.Errorf("读取 exchange_id_a 失败: " + err.Error()), nil
					}
					textA = respA
				}
				if exB := strings.TrimSpace(a.ExchangeIDB); exB != "" {
					if t == nil || t.trafficStore == nil {
						return actool.Errorf("当前环境未挂载流量存储，无法按 exchange_id_b 读取"), nil
					}
					_, respB, err := t.trafficStore.Get(exB)
					if err != nil {
						return actool.Errorf("读取 exchange_id_b 失败: " + err.Error()), nil
					}
					textB = respB
				}
				if textA == "" && textB == "" {
					return actool.Errorf("mode=diff 需要提供 exchange_id_a/exchange_id_b 或 text_a/text_b"), nil
				}
				res := computeResponseDiff(textA, textB)
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			default:
				return actool.Errorf("未知的 http_sequence mode: " + mode), nil
			}
		},
	})
}

type parsedStoredExchange struct {
	Method   string
	URL      string
	Headers  http.Header
	Body     string
	RespBody string
}

func parseStoredHTTPExchange(reqRaw, respRaw string) (*parsedStoredExchange, error) {
	normReq := strings.ReplaceAll(reqRaw, "\r\n", "\n")
	headPart := normReq
	bodyPart := ""
	if idx := strings.Index(normReq, "\n\n"); idx >= 0 {
		headPart = normReq[:idx]
		bodyPart = normReq[idx+2:]
	}
	lines := strings.Split(headPart, "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("空的请求报文")
	}
	reqLine := strings.Fields(lines[0])
	if len(reqLine) < 2 {
		return nil, fmt.Errorf("无效的请求行: %s", lines[0])
	}
	method := strings.ToUpper(reqLine[0])
	rawTarget := reqLine[1]

	hdr := make(http.Header)
	hostVal := ""
	for _, l := range lines[1:] {
		if k, v, ok := strings.Cut(l, ":"); ok {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if strings.EqualFold(k, "Host") {
				hostVal = v
			} else if !strings.EqualFold(k, "Content-Length") {
				hdr.Add(k, v)
			}
		}
	}
	fullURL := rawTarget
	if !strings.HasPrefix(fullURL, "http://") && !strings.HasPrefix(fullURL, "https://") {
		scheme := "http"
		if strings.HasSuffix(hostVal, ":443") {
			scheme = "https"
		}
		if !strings.HasPrefix(rawTarget, "/") {
			rawTarget = "/" + rawTarget
		}
		fullURL = scheme + "://" + hostVal + rawTarget
	}
	return &parsedStoredExchange{
		Method:   method,
		URL:      fullURL,
		Headers:  hdr,
		Body:     bodyPart,
		RespBody: extractHTTPBodyText(respRaw),
	}, nil
}

func (t *ToolSet) runExchangeReplay(
	ctx context.Context,
	baseExID, overMethod, overURL, overPath string,
	overQuery, overHeaders map[string]string,
	overCookie string,
	overBody *string,
	stripHeaders []string,
	timeoutDur time.Duration,
) (map[string]any, error) {
	if t == nil || t.trafficStore == nil {
		return nil, fmt.Errorf("当前环境未挂载流量存储，无法按 base_exchange_id 重放")
	}
	reqRaw, respRaw, err := t.trafficStore.Get(baseExID)
	if err != nil {
		return nil, fmt.Errorf("读取 base_exchange_id(%s) 失败: %w", baseExID, err)
	}
	parsed, err := parseStoredHTTPExchange(reqRaw, respRaw)
	if err != nil {
		return nil, err
	}

	method := parsed.Method
	if strings.TrimSpace(overMethod) != "" {
		method = strings.ToUpper(strings.TrimSpace(overMethod))
	}
	targetURL := parsed.URL
	if strings.TrimSpace(overURL) != "" {
		targetURL = strings.TrimSpace(overURL)
	}
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("URL 解析失败: %w", err)
	}
	if strings.TrimSpace(overPath) != "" {
		if p, q, hasQ := strings.Cut(strings.TrimSpace(overPath), "?"); hasQ {
			u.Path = p
			u.RawQuery = q
		} else {
			u.Path = overPath
		}
	}
	if len(overQuery) > 0 {
		q := u.Query()
		for k, v := range overQuery {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	hdr := parsed.Headers.Clone()
	for _, sh := range stripHeaders {
		hdr.Del(strings.TrimSpace(sh))
	}
	for k, v := range overHeaders {
		hdr.Set(k, v)
	}
	if strings.TrimSpace(overCookie) != "" {
		hdr.Set("Cookie", strings.TrimSpace(overCookie))
	}

	bodyStr := parsed.Body
	if overBody != nil {
		bodyStr = *overBody
	}

	var br io.Reader
	if bodyStr != "" {
		br = strings.NewReader(bodyStr)
	}
	rCtx, cancel := context.WithTimeout(ctx, timeoutDur)
	defer cancel()
	req, err := http.NewRequestWithContext(rCtx, method, u.String(), br)
	if err != nil {
		return nil, err
	}
	req.Header = hdr
	if hostH := hdr.Get("Host"); hostH != "" {
		req.Host = hostH
	}

	tr := t.getPooledTransport("", "", true)
	client := &http.Client{
		Transport: tr,
		Timeout:   timeoutDur,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	reqSnap := req.Header.Clone()
	t0 := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(t0).Milliseconds()
	if err != nil {
		return nil, err
	}
	rawResp, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	resp.Body.Close()
	newRespBody := string(rawResp)

	newExID := t.trafficStore.RecordDirect(method, u.String(), resp.Proto, reqSnap, []byte(bodyStr), resp.StatusCode, resp.Header.Clone(), rawResp)
	diff := computeResponseDiff(parsed.RespBody, newRespBody)

	return map[string]any{
		"mode":               "replay",
		"base_exchange_id":   baseExID,
		"replay_exchange_id": newExID,
		"method":             method,
		"url":                u.String(),
		"status":             resp.StatusCode,
		"elapsed_ms":         elapsed,
		"body_length":        len(rawResp),
		"body_preview":       clipUTF8(newRespBody, 1500),
		"diff_vs_base":       diff,
	}, nil
}

func (t *ToolSet) runBOLAMatrix(
	ctx context.Context,
	baseExID, method, rawURL string,
	headers map[string]string,
	body string,
	identities []bolaIdentitySpec,
	timeoutDur time.Duration,
) (map[string]any, error) {
	var baseMethod, baseURL, baseBody, baseRespBody string
	baseHeaders := make(http.Header)

	if baseExID != "" {
		if t == nil || t.trafficStore == nil {
			return nil, fmt.Errorf("未挂载流量存储，无法读取 base_exchange_id")
		}
		reqRaw, respRaw, err := t.trafficStore.Get(baseExID)
		if err != nil {
			return nil, err
		}
		parsed, err := parseStoredHTTPExchange(reqRaw, respRaw)
		if err != nil {
			return nil, err
		}
		baseMethod = parsed.Method
		baseURL = parsed.URL
		baseHeaders = parsed.Headers
		baseBody = parsed.Body
		baseRespBody = parsed.RespBody
	} else {
		if strings.TrimSpace(rawURL) == "" {
			return nil, fmt.Errorf("mode=bola 时请提供 base_exchange_id 或 url")
		}
		baseMethod = strings.ToUpper(strings.TrimSpace(method))
		if baseMethod == "" {
			baseMethod = "GET"
		}
		baseURL = strings.TrimSpace(rawURL)
		for k, v := range headers {
			baseHeaders.Set(k, v)
		}
		baseBody = body
	}

	if len(identities) == 0 {
		identities = []bolaIdentitySpec{
			{
				Name:         "unauthenticated",
				StripHeaders: []string{"Authorization", "Cookie", "X-Auth-Token", "X-Access-Token", "Token"},
			},
		}
	}

	tr := t.getPooledTransport("", "", true)
	client := &http.Client{
		Transport: tr,
		Timeout:   timeoutDur,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// 若未提供 base_exchange_id，先发一次基线请求获取对照响应
	if baseExID == "" {
		var br io.Reader
		if baseBody != "" {
			br = strings.NewReader(baseBody)
		}
		rCtx, cancel := context.WithTimeout(ctx, timeoutDur)
		req, err := http.NewRequestWithContext(rCtx, baseMethod, baseURL, br)
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header = baseHeaders.Clone()
		resp, err := client.Do(req)
		if err == nil {
			rawB, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
			resp.Body.Close()
			baseRespBody = string(rawB)
			if t != nil && t.trafficStore != nil {
				baseExID = t.trafficStore.RecordDirect(baseMethod, baseURL, resp.Proto, req.Header, []byte(baseBody), resp.StatusCode, resp.Header, rawB)
			}
		}
		cancel()
	}

	type bolaMatrixRow struct {
		Identity        string  `json:"identity"`
		ExchangeID      string  `json:"exchange_id,omitempty"`
		Status          int     `json:"status"`
		BodyLength      int     `json:"body_length"`
		LengthDelta     int     `json:"length_delta"`
		SimilarityRatio float64 `json:"similarity_ratio"`
		SuspectedBOLA   bool    `json:"suspected_bola"`
		BodyPreview     string  `json:"body_preview"`
		Error           string  `json:"error,omitempty"`
	}

	var rows []bolaMatrixRow
	for _, idSpec := range identities {
		if ctx.Err() != nil {
			break
		}
		hdr := baseHeaders.Clone()
		for _, sh := range idSpec.StripHeaders {
			hdr.Del(strings.TrimSpace(sh))
		}
		if strings.TrimSpace(idSpec.Cookie) != "" {
			hdr.Set("Cookie", strings.TrimSpace(idSpec.Cookie))
		}
		for k, v := range idSpec.Headers {
			hdr.Set(k, v)
		}

		var br io.Reader
		if baseBody != "" {
			br = strings.NewReader(baseBody)
		}
		rCtx, cancel := context.WithTimeout(ctx, timeoutDur)
		req, err := http.NewRequestWithContext(rCtx, baseMethod, baseURL, br)
		if err != nil {
			cancel()
			rows = append(rows, bolaMatrixRow{Identity: idSpec.Name, Error: err.Error()})
			continue
		}
		req.Header = hdr
		reqSnap := req.Header.Clone()
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			rows = append(rows, bolaMatrixRow{Identity: idSpec.Name, Error: err.Error()})
			continue
		}
		rawResp, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		resp.Body.Close()
		cancel()
		respStr := string(rawResp)

		var exID string
		if t != nil && t.trafficStore != nil {
			exID = t.trafficStore.RecordDirect(baseMethod, baseURL, resp.Proto, reqSnap, []byte(baseBody), resp.StatusCode, resp.Header.Clone(), rawResp)
		}
		diff := computeResponseDiff(baseRespBody, respStr)
		sim, _ := diff["similarity_ratio"].(float64)
		suspected := resp.StatusCode >= 200 && resp.StatusCode < 300 && len(rawResp) > 20 && sim >= 0.75

		rows = append(rows, bolaMatrixRow{
			Identity:        firstNonEmpty(idSpec.Name, "identity"),
			ExchangeID:      exID,
			Status:          resp.StatusCode,
			BodyLength:      len(rawResp),
			LengthDelta:     len(rawResp) - len(baseRespBody),
			SimilarityRatio: sim,
			SuspectedBOLA:   suspected,
			BodyPreview:     clipUTF8(respStr, 600),
		})
	}

	return map[string]any{
		"mode":             "bola",
		"base_exchange_id": baseExID,
		"method":           baseMethod,
		"url":              baseURL,
		"base_body_length": len(baseRespBody),
		"matrix":           rows,
	}, nil
}

func substituteVars(tmpl string, vars map[string]string) string {
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

func (t *ToolSet) runHTTPChain(ctx context.Context, steps []sequenceStepSpec, initVars map[string]string, timeoutDur time.Duration) (map[string]any, error) {
	vars := make(map[string]string, len(initVars))
	for k, v := range initVars {
		vars[k] = v
	}
	jar, _ := cookiejar.New(nil)
	tr := t.getPooledTransport("", "", true)

	type stepOut struct {
		Step        int               `json:"step"`
		Name        string            `json:"name,omitempty"`
		Method      string            `json:"method"`
		URL         string            `json:"url"`
		ExchangeID  string            `json:"exchange_id,omitempty"`
		Status      int               `json:"status"`
		ElapsedMs   int64             `json:"elapsed_ms"`
		Extracted   map[string]string `json:"extracted,omitempty"`
		BodyPreview string            `json:"body_preview"`
		Error       string            `json:"error,omitempty"`
	}

	var results []stepOut
	for idx, st := range steps {
		if ctx.Err() != nil {
			break
		}
		method := strings.ToUpper(strings.TrimSpace(st.Method))
		if method == "" {
			method = "GET"
		}
		stepURL := substituteVars(strings.TrimSpace(st.URL), vars)
		stepBody := substituteVars(st.Body, vars)

		var bodyReader io.Reader
		if stepBody != "" {
			bodyReader = strings.NewReader(stepBody)
		}
		rCtx, cancel := context.WithTimeout(ctx, timeoutDur)
		req, err := http.NewRequestWithContext(rCtx, method, stepURL, bodyReader)
		if err != nil {
			cancel()
			results = append(results, stepOut{Step: idx + 1, Name: st.Name, Method: method, URL: stepURL, Error: err.Error()})
			break
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		for k, v := range st.Headers {
			rv := substituteVars(v, vars)
			if strings.EqualFold(strings.TrimSpace(k), "Host") {
				req.Host = rv
			} else {
				req.Header.Set(k, rv)
			}
		}

		client := &http.Client{
			Transport: tr,
			Jar:       jar,
			Timeout:   timeoutDur,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				if !st.FollowRedirects {
					return http.ErrUseLastResponse
				}
				return nil
			},
		}

		reqHeaderSnap := req.Header.Clone()
		t0 := time.Now()
		resp, err := client.Do(req)
		elapsed := time.Since(t0).Milliseconds()
		if err != nil {
			cancel()
			results = append(results, stepOut{Step: idx + 1, Name: st.Name, Method: method, URL: stepURL, ElapsedMs: elapsed, Error: err.Error()})
			break
		}
		rawResp, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		resp.Body.Close()
		cancel()
		respStr := string(rawResp)

		var exID string
		if t != nil && t.trafficStore != nil {
			exID = t.trafficStore.RecordDirect(method, stepURL, resp.Proto, reqHeaderSnap, []byte(stepBody), resp.StatusCode, resp.Header.Clone(), rawResp)
		}

		extracted := make(map[string]string)
		for varName, rule := range st.ExtractVars {
			rule = strings.TrimSpace(rule)
			if strings.HasPrefix(rule, "json:") {
				gpath := strings.TrimSpace(strings.TrimPrefix(rule, "json:"))
				val := gjson.Get(respStr, gpath)
				if val.Exists() {
					vars[varName] = val.String()
					extracted[varName] = val.String()
				}
			} else {
				rx := strings.TrimPrefix(rule, "regex:")
				if re, err := regexp.Compile(rx); err == nil {
					var hdrBuf strings.Builder
					for hk, hvs := range resp.Header {
						for _, hv := range hvs {
							hdrBuf.WriteString(hk + ": " + hv + "\n")
						}
					}
					m := re.FindStringSubmatch(hdrBuf.String() + "\n" + respStr)
					if len(m) > 1 {
						vars[varName] = m[1]
						extracted[varName] = m[1]
					} else if len(m) == 1 {
						vars[varName] = m[0]
						extracted[varName] = m[0]
					}
				}
			}
		}

		results = append(results, stepOut{
			Step:        idx + 1,
			Name:        st.Name,
			Method:      method,
			URL:         stepURL,
			ExchangeID:  exID,
			Status:      resp.StatusCode,
			ElapsedMs:   elapsed,
			Extracted:   extracted,
			BodyPreview: clipUTF8(respStr, 1024),
		})

		if st.ExpectStatus > 0 && resp.StatusCode != st.ExpectStatus {
			break
		}
	}

	return map[string]any{
		"mode":       "chain",
		"steps_run":  len(results),
		"final_vars": vars,
		"steps":      results,
	}, nil
}

func (t *ToolSet) runHTTPRace(ctx context.Context, method, rawURL string, headers map[string]string, body string, count int, timeoutDur time.Duration) (map[string]any, error) {
	tr := t.getPooledTransport("", "", true)
	client := &http.Client{
		Transport: tr,
		Timeout:   timeoutDur,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	type raceResult struct {
		Index      int    `json:"index"`
		ExchangeID string `json:"exchange_id,omitempty"`
		Status     int    `json:"status"`
		Length     int    `json:"length"`
		ElapsedMs  int64  `json:"elapsed_ms"`
		Sample     string `json:"sample,omitempty"`
		Error      string `json:"error,omitempty"`
	}

	var (
		readyWG    sync.WaitGroup
		doneWG     sync.WaitGroup
		startGate  = make(chan struct{})
		mu         sync.Mutex
		results    = make([]raceResult, 0, count)
		statusDist = make(map[int]int)
	)

	readyWG.Add(count)
	doneWG.Add(count)
	for i := 0; i < count; i++ {
		go func(idx int) {
			defer doneWG.Done()
			var bodyReader io.Reader
			if body != "" {
				bodyReader = strings.NewReader(body)
			}
			rCtx, cancel := context.WithTimeout(ctx, timeoutDur)
			defer cancel()
			req, err := http.NewRequestWithContext(rCtx, method, rawURL, bodyReader)
			if err != nil {
				readyWG.Done()
				return
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
			for k, v := range headers {
				if strings.EqualFold(strings.TrimSpace(k), "Host") {
					req.Host = v
				} else {
					req.Header.Set(k, v)
				}
			}
			reqH := req.Header.Clone()

			readyWG.Done()
			<-startGate

			t0 := time.Now()
			resp, err := client.Do(req)
			elapsed := time.Since(t0).Milliseconds()
			if err != nil {
				mu.Lock()
				results = append(results, raceResult{Index: idx, ElapsedMs: elapsed, Error: err.Error()})
				mu.Unlock()
				return
			}
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()

			var exID string
			if t != nil && t.trafficStore != nil {
				exID = t.trafficStore.RecordDirect(method, rawURL, resp.Proto, reqH, []byte(body), resp.StatusCode, resp.Header.Clone(), raw)
			}

			mu.Lock()
			statusDist[resp.StatusCode]++
			results = append(results, raceResult{
				Index:      idx,
				ExchangeID: exID,
				Status:     resp.StatusCode,
				Length:     len(raw),
				ElapsedMs:  elapsed,
				Sample:     clipUTF8(strings.TrimSpace(string(raw)), 200),
			})
			mu.Unlock()
		}(i)
	}

	readyWG.Wait()
	close(startGate)
	doneWG.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].Index < results[j].Index })
	return map[string]any{
		"mode":                "race",
		"total_requests":      count,
		"status_distribution": statusDist,
		"results":             results,
	}, nil
}

func (t *ToolSet) runHTTPTiming(ctx context.Context, method, baseURL, baseBody, probeURL, probeBody string, headers map[string]string, runs int, timeoutDur time.Duration) (map[string]any, error) {
	tr := t.getPooledTransport("", "", true)
	client := &http.Client{
		Transport: tr,
		Timeout:   timeoutDur,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	doOne := func(targetURL, reqBody string, record bool) (int, int64, string, error) {
		var br io.Reader
		if reqBody != "" {
			br = strings.NewReader(reqBody)
		}
		rCtx, cancel := context.WithTimeout(ctx, timeoutDur)
		defer cancel()
		req, err := http.NewRequestWithContext(rCtx, method, targetURL, br)
		if err != nil {
			return 0, 0, "", err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		for k, v := range headers {
			if strings.EqualFold(strings.TrimSpace(k), "Host") {
				req.Host = v
			} else {
				req.Header.Set(k, v)
			}
		}
		reqH := req.Header.Clone()
		t0 := time.Now()
		resp, err := client.Do(req)
		elapsed := time.Since(t0).Milliseconds()
		if err != nil {
			return 0, elapsed, "", err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		var exID string
		if record && t != nil && t.trafficStore != nil {
			exID = t.trafficStore.RecordDirect(method, targetURL, resp.Proto, reqH, []byte(reqBody), resp.StatusCode, resp.Header.Clone(), raw)
		}
		return resp.StatusCode, elapsed, exID, nil
	}

	var baseSamples []int64
	var sumBase, maxBase int64
	for i := 0; i < runs; i++ {
		_, ms, _, err := doOne(baseURL, baseBody, false)
		if err != nil {
			return nil, err
		}
		baseSamples = append(baseSamples, ms)
		sumBase += ms
		if ms > maxBase {
			maxBase = ms
		}
	}
	avgBase := sumBase / int64(len(baseSamples))

	probeStatus, probeMs, exID, err := doOne(probeURL, probeBody, true)
	if err != nil {
		return map[string]any{
			"mode":             "timing",
			"baseline_samples": baseSamples,
			"baseline_avg_ms":  avgBase,
			"baseline_max_ms":  maxBase,
			"probe_elapsed_ms": probeMs,
			"probe_error":      err.Error(),
			"delayed":          probeMs-maxBase >= 2500,
		}, nil
	}

	delta := probeMs - avgBase
	return map[string]any{
		"mode":              "timing",
		"baseline_samples":  baseSamples,
		"baseline_avg_ms":   avgBase,
		"baseline_max_ms":   maxBase,
		"probe_exchange_id": exID,
		"probe_status":      probeStatus,
		"probe_elapsed_ms":  probeMs,
		"delta_from_avg_ms": delta,
		"delta_from_max_ms": probeMs - maxBase,
		"delayed":           (probeMs - maxBase) >= 2500,
	}, nil
}

func computeResponseDiff(a, b string) map[string]any {
	linesA := strings.Split(strings.ReplaceAll(a, "\r\n", "\n"), "\n")
	linesB := strings.Split(strings.ReplaceAll(b, "\r\n", "\n"), "\n")

	setA := make(map[string]int, len(linesA))
	for _, l := range linesA {
		setA[l]++
	}
	setB := make(map[string]int, len(linesB))
	for _, l := range linesB {
		setB[l]++
	}

	var onlyInA, onlyInB []string
	commonLines := 0
	for l, cntA := range setA {
		if cntB, ok := setB[l]; ok {
			if cntA < cntB {
				commonLines += cntA
			} else {
				commonLines += cntB
			}
		} else if strings.TrimSpace(l) != "" && len(onlyInA) < 40 {
			onlyInA = append(onlyInA, clipUTF8(l, 200))
		}
	}
	for l := range setB {
		if _, ok := setA[l]; !ok && strings.TrimSpace(l) != "" && len(onlyInB) < 40 {
			onlyInB = append(onlyInB, clipUTF8(l, 200))
		}
	}

	totalLines := len(linesA) + len(linesB)
	similarity := 1.0
	if totalLines > 0 {
		similarity = float64(2*commonLines) / float64(totalLines)
	}

	return map[string]any{
		"mode":             "diff",
		"identical":        a == b,
		"length_a":         len(a),
		"length_b":         len(b),
		"length_delta":     len(b) - len(a),
		"lines_a":          len(linesA),
		"lines_b":          len(linesB),
		"similarity_ratio": similarity,
		"removed_from_a":   onlyInA,
		"added_in_b":       onlyInB,
	}
}

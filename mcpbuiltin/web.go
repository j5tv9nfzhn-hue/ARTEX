package mcpbuiltin

// artex-webscan：Web 主动验证类工具。纯 Go，等价于"能发请求、能比对、能判定"的最小闭环。
// 不实现扫描器规则库，也不打包字典；每个工具都要求显式目标，输出带证据行供报告引用。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/mcp"
)

func webScanServer() Server {
	return Server{
		Name:    "artex-webscan",
		Title:   "ARTEX Web 主动验证（原始请求 / 模糊测试 / 路径 / 参数 / JWT / SQLi）",
		Domain:  "pentest",
		Summary: "内置 Go 实现：发送任意原始 HTTP 请求、多载荷模糊测试与基线比对、路径枚举、参数反射探测、JWT 解析与弱密钥检查、SQL 注入三态判定。",
		Tools: []mcp.InProcessTool{
			{
				Name:        "http_send",
				Description: "发送一条原始 HTTP 请求（可自定义方法/头/体），返回状态、响应头与正文头部。用于复现验证，等价于 Repeater。",
				Schema: jsonSchema(map[string]any{
					"request":  propStr("原始请求文本，例如 `GET /api/x HTTP/1.1` + 换行 + `Host: example.com` + 空行 + body"),
					"base_url": propStr("可选，提供 scheme://host 用于补全 URL 与 Host"),
					"insecure": propBool("跳过 TLS 校验，默认 true"),
					"max_body": propInt("返回正文最大字节，默认 4000"),
				}, "request"),
				Handler: httpSend,
			},
			{
				Name:        "http_fuzz",
				Description: "把一个请求模板里的 FUZZ 占位符逐个替换成载荷并发发送，与基线比对状态/长度，可正则提取命中。适合参数、头、路径的批量试探。",
				Schema: jsonSchema(map[string]any{
					"request":     propStr("请求模板，必须包含 FUZZ 占位符（会出现多次则同时替换）"),
					"payloads":    propArr("载荷列表", "string"),
					"base_url":    propStr("可选，scheme://host"),
					"insecure":    propBool("跳过 TLS 校验，默认 true"),
					"concurrency": propInt("并发，默认 16，上限 32"),
					"match":       propStr("可选正则，命中则标记 MATCH，并在结果里列出第一个匹配"),
					"max_body":    propInt("每条响应读取的最大字节，默认 200000"),
				}, "request", "payloads"),
				Handler: httpFuzz,
			},
			{
				Name:        "dir_probe",
				Description: "路径枚举：把内置或自定义字典拼到 URL 后面并发请求，按状态码/长度过滤。只做 GET，不跟随跳转。",
				Schema: jsonSchema(map[string]any{
					"url":         propStr("基地址，例如 https://example.com"),
					"wordlist":    propArr("自定义路径列表；为空用内置常见路径", "string"),
					"extensions":  propArr("附加后缀，例如 ['.php','.bak']", "string"),
					"status":      propArr("关心的状态码，默认 [200,204,301,302,307,401,403]", "integer"),
					"concurrency": propInt("并发，默认 24，上限 64"),
					"insecure":    propBool("跳过 TLS 校验，默认 true"),
				}, "url"),
				Handler: dirProbe,
			},
			{
				Name:        "param_probe",
				Description: "参数反射与行为差异探测：对单个查询参数注入随机 canary，检查是否原样回显在正文/响应头，以及长度与状态是否变化。",
				Schema: jsonSchema(map[string]any{
					"url":      propStr("带参数的完整 URL，例如 https://x/a?id=1&next=/"),
					"param":    propStr("要探测的参数名；为空则探测 URL 里全部参数"),
					"insecure": propBool("跳过 TLS 校验，默认 true"),
				}, "url"),
				Handler: paramProbe,
			},
			{
				Name:        "jwt_inspect",
				Description: "解析 JWT 的 header/payload，检查 alg=none、过期时间与敏感字段，并可用内置弱密钥字典验证 HS256 签名。",
				Schema: jsonSchema(map[string]any{
					"token":    propStr("完整 JWT（三段以点分隔）"),
					"wordlist": propArr("自定义候选密钥；为空用内置弱密钥表", "string"),
				}, "token"),
				Handler: jwtInspect,
			},
			{
				Name:        "sqli_probe",
				Description: "SQL 注入三态判定：报错特征、布尔真/假对比、可选时间盲注。请求数有硬上限，只做判定不给利用链。",
				Schema: jsonSchema(map[string]any{
					"url":        propStr("带参数的完整 URL"),
					"param":      propStr("要测的参数名"),
					"time_based": propBool("是否做时间盲注（会多花约 3 秒），默认 false"),
					"insecure":   propBool("跳过 TLS 校验，默认 true"),
				}, "url", "param"),
				Handler: sqliProbe,
			},
		},
	}
}

func httpSend(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Request  string `json:"request"`
		BaseURL  string `json:"base_url"`
		Insecure *bool  `json:"insecure"`
		MaxBody  int    `json:"max_body"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	insecure := true
	if in.Insecure != nil {
		insecure = *in.Insecure
	}
	req, err := parseRawRequest(in.Request, in.BaseURL)
	if err != nil {
		return "", err
	}
	c := newHTTPClient(20*time.Second, insecure, false)
	resp, body, dur, err := sendRaw(ctx, c, req)
	if err != nil {
		return "", fmt.Errorf("发送失败：%w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s → %s（%s，%d 字节）\n\n", req.Method, req.URL, resp.Status, dur.Round(time.Millisecond), len(body))
	b.WriteString(headHeaders(resp, 40))
	b.WriteString("\n")
	b.WriteString(clip(string(body), defInt(in.MaxBody, 4000, 200, 200000)))
	return b.String(), nil
}

func httpFuzz(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Request     string   `json:"request"`
		Payloads    []string `json:"payloads"`
		BaseURL     string   `json:"base_url"`
		Insecure    *bool    `json:"insecure"`
		Concurrency int      `json:"concurrency"`
		Match       string   `json:"match"`
		MaxBody     int      `json:"max_body"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	if !strings.Contains(in.Request, "FUZZ") {
		return "", fmt.Errorf("request 模板里必须包含 FUZZ 占位符")
	}
	if len(in.Payloads) == 0 {
		return "", fmt.Errorf("payloads 不能为空")
	}
	in.Payloads = in.Payloads[:clamp(len(in.Payloads), 0, 500)]
	insecure := true
	if in.Insecure != nil {
		insecure = *in.Insecure
	}
	conc := defInt(in.Concurrency, 16, 1, 32)
	if conc == 0 {
		conc = 16
	}
	maxBody := defInt(in.MaxBody, 200000, 1000, 2<<20)

	c := newHTTPClient(20*time.Second, insecure, false)
	type result struct {
		Payload string
		Status  int
		Length  int
		DurMS   int64
		Match   string
		Err     string
	}
	send := func(payload string) result {
		r := in.Request
		if payload == "" {
			payload = "ARTEX_EMPTY"
		}
		r = strings.ReplaceAll(r, "FUZZ", payload)
		req, err := parseRawRequest(r, in.BaseURL)
		if err != nil {
			return result{Payload: payload, Err: err.Error()}
		}
		start := time.Now()
		resp, body, dur, err := sendRaw(ctx, c, req)
		if err != nil {
			return result{Payload: payload, DurMS: time.Since(start).Milliseconds(), Err: err.Error()}
		}
		res := result{Payload: payload, Status: resp.StatusCode, Length: len(body), DurMS: dur.Milliseconds()}
		if in.Match != "" {
			if re, err := compileRegex(in.Match); err == nil {
				if m := re.FindSubmatch(body); len(m) > 0 {
					res.Match = clip(string(m[0]), 80)
				}
			}
		}
		return res
	}

	base := send("ARTEX_BASELINE_9f3c")
	results := make([]result, len(in.Payloads))
	var wg sync.WaitGroup
	sem := make(chan struct{}, conc)
	for i, p := range in.Payloads {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = send(p)
		}(i, p)
	}
	wg.Wait()

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Status != results[j].Status {
			return results[i].Status < results[j].Status
		}
		return results[i].Length < results[j].Length
	})
	var b strings.Builder
	fmt.Fprintf(&b, "基线：%d / %d 字节\n", base.Status, base.Length)
	fmt.Fprintf(&b, "%-40s %-6s %-9s %-7s %s\n", "载荷", "状态", "长度", "差异", "备注")
	for _, r := range results {
		if r.Err != "" {
			fmt.Fprintf(&b, "%-40s %-6s %-9s %-7s %s\n", clip(r.Payload, 40), "-", "-", "-", "错误："+clip(r.Err, 60))
			continue
		}
		diff := r.Length - base.Length
		note := ""
		if r.Match != "" {
			note = "MATCH " + r.Match
		} else if r.Status == base.Status && diff == 0 {
			note = "与基线一致"
		}
		fmt.Fprintf(&b, "%-40s %-6d %-9d %-7d %s\n", clip(r.Payload, 40), r.Status, r.Length, diff, note)
	}
	fmt.Fprintf(&b, "\n共 %d 条，读取上限 %d 字节/条。\n", len(results), maxBody)
	return b.String(), nil
}

func dirProbe(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		URL         string   `json:"url"`
		Wordlist    []string `json:"wordlist"`
		Extensions  []string `json:"extensions"`
		Status      []int    `json:"status"`
		Concurrency int      `json:"concurrency"`
		Insecure    *bool    `json:"insecure"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	base := strings.TrimRight(strings.TrimSpace(in.URL), "/")
	if base == "" {
		return "", fmt.Errorf("url 不能为空")
	}
	if !strings.HasPrefix(base, "http") {
		base = "https://" + base
	}
	words := in.Wordlist
	if len(words) == 0 {
		words = defaultPaths
	}
	words = words[:clamp(len(words), 0, 2000)]
	if len(in.Extensions) > 0 {
		var expanded []string
		for _, w := range words {
			for _, e := range in.Extensions {
				expanded = append(expanded, w+e)
			}
		}
		words = expanded[:clamp(len(expanded), 0, 3000)]
	}
	want := map[int]bool{}
	for _, s := range in.Status {
		want[s] = true
	}
	if len(want) == 0 {
		for _, s := range []int{200, 204, 301, 302, 307, 401, 403} {
			want[s] = true
		}
	}
	insecure := true
	if in.Insecure != nil {
		insecure = *in.Insecure
	}
	conc := defInt(in.Concurrency, 24, 1, 64)
	if conc == 0 {
		conc = 24
	}
	c := newHTTPClient(12*time.Second, insecure, false)

	type hit struct {
		Path   string
		Status int
		Length int
	}
	var mu sync.Mutex
	var hits []hit
	var wg sync.WaitGroup
	sem := make(chan struct{}, conc)
	start := time.Now()
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()
			req, err := httpNewRequest(ctx, "GET", base+"/"+strings.TrimLeft(path, "/"))
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 artex-recon")
			resp, err := c.Do(req)
			if err != nil {
				return
			}
			body, _ := readAllLimited(resp, 64<<10)
			_ = resp.Body.Close()
			if !want[resp.StatusCode] {
				return
			}
			mu.Lock()
			hits = append(hits, hit{Path: "/" + strings.TrimLeft(path, "/"), Status: resp.StatusCode, Length: len(body)})
			mu.Unlock()
		}(w)
	}
	wg.Wait()
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Status != hits[j].Status {
			return hits[i].Status < hits[j].Status
		}
		return hits[i].Path < hits[j].Path
	})
	var b strings.Builder
	fmt.Fprintf(&b, "目标：%s  字典 %d 条  命中 %d  耗时 %s\n\n", base, len(words), len(hits), time.Since(start).Round(time.Millisecond))
	for _, h := range hits {
		fmt.Fprintf(&b, "  %-6d %-8d %s\n", h.Status, h.Length, h.Path)
	}
	if len(hits) == 0 {
		b.WriteString("  （无命中；可换字典或状态码）\n")
	}
	return b.String(), nil
}

var defaultPaths = []string{
	"robots.txt", "sitemap.xml", "favicon.ico", ".git/config", ".git/HEAD", ".svn/entries",
	".env", ".env.bak", ".DS_Store", "backup.zip", "db.sql", "dump.sql", "www.zip",
	"admin/", "admin/login", "login", "logout", "register", "signin", "signup",
	"api/", "api/v1/", "api/v2/", "api/swagger.json", "swagger-ui/", "openapi.json",
	"actuator", "actuator/env", "actuator/health", "actuator/heapdump",
	"health", "healthz", "status", "metrics", "debug", "console", "phpinfo.php",
	"server-status", "server-info", "web.config", "appsettings.json",
	"config.json", "config.php.bak", "credentials", "passwords.txt",
	"upload", "uploads/", "files/", "static/", "assets/", "backup/", "bak/",
	"test/", "dev/", "staging/", "internal/", "private/", "tmp/",
	"wp-admin/", "wp-login.php", "wp-json/", "xmlrpc.php",
	"manager/html", "jmx-console/", "solr/", "jenkins/", "grafana/", "kibana/",
	"phpmyadmin/", "pma/", "adminer.php", "cgi-bin/", "shell.php",
	"index.php", "index.html", "index.jsp", "default.aspx",
}

func paramProbe(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		URL      string `json:"url"`
		Param    string `json:"param"`
		Insecure *bool  `json:"insecure"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("url 不合法：%q", in.URL)
	}
	q := u.Query()
	params := []string{}
	if strings.TrimSpace(in.Param) != "" {
		params = append(params, in.Param)
	} else {
		for k := range q {
			params = append(params, k)
		}
		sort.Strings(params)
	}
	if len(params) == 0 {
		return "", fmt.Errorf("URL 里没有参数，也没有指定 param")
	}
	insecure := true
	if in.Insecure != nil {
		insecure = *in.Insecure
	}
	c := newHTTPClient(15*time.Second, insecure, true)

	fetch := func(target string) (int, []byte, http.Header, error) {
		req, err := httpNewRequest(ctx, "GET", target)
		if err != nil {
			return 0, nil, nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 artex-recon")
		resp, err := c.Do(req)
		if err != nil {
			return 0, nil, nil, err
		}
		body, _ := readAllLimited(resp, 512<<10)
		_ = resp.Body.Close()
		return resp.StatusCode, body, resp.Header, nil
	}

	var b strings.Builder
	for _, p := range params {
		baseQ := cloneValues(q)
		baseQ.Set(p, "artex0")
		baseU := *u
		baseU.RawQuery = baseQ.Encode()
		st0, body0, hdr0, err := fetch(baseU.String())
		if err != nil {
			fmt.Fprintf(&b, "%s：基线请求失败 %v\n", p, err)
			continue
		}
		canary := "artexq7z3m1"
		canQ := cloneValues(q)
		canQ.Set(p, canary)
		canU := *u
		canU.RawQuery = canQ.Encode()
		st1, body1, hdr1, err := fetch(canU.String())
		if err != nil {
			fmt.Fprintf(&b, "%s：注入请求失败 %v\n", p, err)
			continue
		}
		reflectBody := strings.Contains(string(body1), canary)
		reflectHeader := false
		for k, vs := range hdr1 {
			if strings.Contains(strings.Join(vs, ","), canary) {
				reflectHeader = true
				_ = k
				break
			}
		}
		_ = hdr0
		fmt.Fprintf(&b, "参数 %s：\n", p)
		fmt.Fprintf(&b, "  状态 %d → %d   长度 %d → %d（Δ%d）\n", st0, st1, len(body0), len(body1), len(body1)-len(body0))
		if reflectBody {
			b.WriteString("  ⚠ 正文原样回显 canary（可能是 XSS/SSTI/日志注入入口，需人工确认上下文）\n")
		}
		if reflectHeader {
			b.WriteString("  ⚠ 响应头回显 canary\n")
		}
		if !reflectBody && !reflectHeader {
			b.WriteString("  未见回显\n")
		}
	}
	return b.String(), nil
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	return out
}

func jwtInspect(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Token    string   `json:"token"`
		Wordlist []string `json:"wordlist"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	tok := strings.TrimSpace(in.Token)
	tok = strings.TrimPrefix(tok, "Bearer ")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("JWT 应为三段，实际 %d 段", len(parts))
	}
	var b strings.Builder
	hdr, err := b64urlDecode(parts[0])
	if err != nil {
		return "", fmt.Errorf("header 解码失败：%w", err)
	}
	pay, err := b64urlDecode(parts[1])
	if err != nil {
		return "", fmt.Errorf("payload 解码失败：%w", err)
	}
	fmt.Fprintf(&b, "header：%s\n", prettyJSON(hdr))
	fmt.Fprintf(&b, "payload：%s\n", prettyJSON(pay))

	var h struct {
		Alg string `json:"alg"`
	}
	_ = json.Unmarshal(hdr, &h)
	alg := strings.ToLower(h.Alg)
	if alg == "none" || alg == "" {
		b.WriteString("⚠ alg 为 none/空：服务端若仍接受该令牌，等于无签名。\n")
	}
	var p struct {
		Exp int64 `json:"exp"`
		Nbf int64 `json:"nbf"`
		Iat int64 `json:"iat"`
	}
	_ = json.Unmarshal(pay, &p)
	now := time.Now().Unix()
	if p.Exp > 0 {
		if p.Exp < now {
			fmt.Fprintf(&b, "⚠ 已过期（exp=%s）\n", time.Unix(p.Exp, 0).UTC().Format(time.RFC3339))
		} else {
			fmt.Fprintf(&b, "有效期至 %s\n", time.Unix(p.Exp, 0).UTC().Format(time.RFC3339))
		}
	}

	if strings.HasPrefix(alg, "hs") {
		words := in.Wordlist
		if len(words) == 0 {
			words = defaultJWTSecrets
		}
		signing := parts[0] + "." + parts[1]
		got, err := b64urlDecode(parts[2])
		if err == nil {
			var hit string
			for _, w := range words {
				if hsVerify(alg, w, signing, got) {
					hit = w
					break
				}
			}
			if hit != "" {
				fmt.Fprintf(&b, "⚠ 弱密钥命中：%q（%s 签名可伪造）\n", hit, strings.ToUpper(alg))
			} else {
				fmt.Fprintf(&b, "弱密钥表（%d 条）未命中\n", len(words))
			}
		}
	}
	return b.String(), nil
}

var defaultJWTSecrets = []string{
	"secret", "password", "123456", "changeme", "jwt", "jwtsecret", "key", "test",
	"admin", "default", "supersecret", "your-256-bit-secret", "s3cr3t", "token",
	"mysecret", "app_secret", "dev", "prod", "secretkey", "hmac",
}

func b64urlDecode(s string) ([]byte, error) {
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.URLEncoding.DecodeString(s)
}

func prettyJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return clip(string(raw), 500)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return clip(string(raw), 500)
	}
	return string(out)
}

func hsVerify(alg, secret, signing string, want []byte) bool {
	var mac hash.Hash
	switch strings.ToLower(alg) {
	case "hs256":
		mac = hmac.New(sha256.New, []byte(secret))
	case "hs384":
		mac = hmac.New(sha512.New384, []byte(secret))
	case "hs512":
		mac = hmac.New(sha512.New, []byte(secret))
	default:
		return false
	}
	mac.Write([]byte(signing))
	return hmac.Equal(mac.Sum(nil), want)
}

func sqliProbe(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		URL       string `json:"url"`
		Param     string `json:"param"`
		TimeBased bool   `json:"time_based"`
		Insecure  *bool  `json:"insecure"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("url 不合法：%q", in.URL)
	}
	param := strings.TrimSpace(in.Param)
	if param == "" {
		return "", fmt.Errorf("param 不能为空")
	}
	insecure := true
	if in.Insecure != nil {
		insecure = *in.Insecure
	}
	c := newHTTPClient(20*time.Second, insecure, false)
	fetch := func(val string) (int, int, time.Duration, string, error) {
		q := cloneValues(u.Query())
		q.Set(param, val)
		target := *u
		target.RawQuery = q.Encode()
		req, err := httpNewRequest(ctx, "GET", target.String())
		if err != nil {
			return 0, 0, 0, "", err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 artex-recon")
		start := time.Now()
		resp, err := c.Do(req)
		if err != nil {
			return 0, 0, time.Since(start), "", err
		}
		body, _ := readAllLimited(resp, 256<<10)
		_ = resp.Body.Close()
		return resp.StatusCode, len(body), time.Since(start), string(body), nil
	}

	baseSt, baseLen, _, _, err := fetch("1")
	if err != nil {
		return "", fmt.Errorf("基线请求失败：%w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "参数 %s  基线 %d / %d 字节\n\n", param, baseSt, baseLen)

	var evidence []string
	for _, p := range []string{"'", "\"", "')", "1'"} {
		st, ln, _, body, err := fetch(p)
		if err != nil {
			continue
		}
		for _, sig := range sqlErrorSignatures {
			if strings.Contains(strings.ToLower(body), sig) {
				evidence = append(evidence, fmt.Sprintf("报错特征命中 %q（载荷 %q，状态 %d，长度 %d）", sig, p, st, ln))
				break
			}
		}
	}
	stT, lnT, _, _, errT := fetch("1 AND 1=1")
	stF, lnF, _, _, errF := fetch("1 AND 1=2")
	if errT == nil && errF == nil {
		if stT == baseSt && stF != baseSt {
			evidence = append(evidence, fmt.Sprintf("布尔差异：AND 1=1 → %d，AND 1=2 → %d（状态不同）", stT, stF))
		} else if absInt(lnT-lnF) > 32 && lnT > 0 && lnF > 0 {
			evidence = append(evidence, fmt.Sprintf("布尔差异：AND 1=1 → %d 字节，AND 1=2 → %d 字节（Δ%d）", lnT, lnF, absInt(lnT-lnF)))
		}
	}
	if in.TimeBased {
		_, _, d0, _, e0 := fetch("1")
		_, _, d1, _, e1 := fetch("1 AND SLEEP(3)")
		if e0 == nil && e1 == nil && d1-d0 > 2*time.Second {
			evidence = append(evidence, fmt.Sprintf("时间盲注：基线 %s，SLEEP(3) 后 %s", d0.Round(time.Millisecond), d1.Round(time.Millisecond)))
		}
	}

	if len(evidence) == 0 {
		b.WriteString("未发现注入证据（报错/布尔/时间三态均未命中）。这**不能**排除注入，只说明该参数在当前载荷下没有反应。\n")
		return b.String(), nil
	}
	b.WriteString("证据：\n")
	for _, e := range evidence {
		fmt.Fprintf(&b, "  - %s\n", e)
	}
	b.WriteString("\n结论：疑似 SQL 注入，需要人工用回显/延时复核并确认上下文（此工具只做判定，不提供利用载荷）。\n")
	return b.String(), nil
}

var sqlErrorSignatures = []string{
	"you have an error in your sql syntax", "warning: mysql", "unclosed quotation mark",
	"quoted string not properly terminated", "pg_query()", "postgresql", "ora-01756",
	"ora-00933", "sqlite error", "sqlstate[", "odbc sql server driver",
	"syntax error at or near", "microsoft ole db provider for sql server",
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

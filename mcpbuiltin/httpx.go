package mcpbuiltin

// 共享的 HTTP 工具函数：构造客户端、解析原始请求、输出截断。
// 所有函数都不读全局状态，方便并发调用。

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// jsonSchema 小工具：拼 JSON Schema，省得每条工具手写一堆 map。
func jsonSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func propStr(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func propInt(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func propBool(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func propArr(desc string, item string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": desc,
		"items":       map[string]any{"type": item},
	}
}

// decodeArgs 把模型给的 JSON 解成结构体。
func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("参数不是合法 JSON：%w", err)
	}
	return nil
}

// newHTTPClient 返回带超时、可选跳过证书校验、可走环境代理的客户端。
func newHTTPClient(timeout time.Duration, insecure bool, followRedirects bool) *http.Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     30 * time.Second,
	}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 —— 渗透场景显式开启
	}
	c := &http.Client{Transport: tr, Timeout: timeout}
	if !followRedirects {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c
}

// rawRequest 是解析后的原始 HTTP 请求。
type rawRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    string
}

// parseRawRequest 解析 "POST /path HTTP/1.1" 形式的原始请求。
// baseURL 提供 scheme://host（原始请求里通常只有 Host 头）。
func parseRawRequest(raw, baseURL string) (*rawRequest, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, fmt.Errorf("请求为空：第一行应该是 `GET /path HTTP/1.1`")
	}
	parts := strings.Fields(lines[0])
	if len(parts) < 2 {
		return nil, fmt.Errorf("请求行不合法：%q", lines[0])
	}
	r := &rawRequest{Method: strings.ToUpper(parts[0]), Headers: map[string]string{}}
	i := 1
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			i++
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		r.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if i < len(lines) {
		r.Body = strings.Join(lines[i:], "\n")
	}
	host := r.Headers["Host"]
	if host == "" && baseURL != "" {
		if u, err := url.Parse(baseURL); err == nil {
			host = u.Host
			r.Headers["Host"] = host
		}
	}
	if baseURL != "" {
		if u, err := url.Parse(baseURL); err == nil {
			r.URL = u.Scheme + "://" + host + parts[1]
		}
	} else {
		r.URL = "https://" + host + parts[1]
	}
	return r, nil
}

// sendRaw 发送解析后的请求。
func sendRaw(ctx context.Context, c *http.Client, r *rawRequest) (*http.Response, []byte, time.Duration, error) {
	start := time.Now()
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
	if err != nil {
		return nil, nil, 0, err
	}
	for k, v := range r.Headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, time.Since(start), err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	_ = resp.Body.Close()
	return resp, data, time.Since(start), err
}

// clip 按字节截断，避免一条工具结果吃掉整个上下文。
func clip(s string, n int) string {
	if n <= 0 {
		n = 4000
	}
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n…（已截断，原长 %d 字节）", len(s))
}

// headHeaders 把响应头压成一行一条，按 key 排序，去掉噪声。
func headHeaders(resp *http.Response, max int) string {
	keys := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var b strings.Builder
	for i, k := range keys {
		if max > 0 && i >= max {
			b.WriteString("…（响应头已截断）\n")
			break
		}
		b.WriteString(k + ": " + strings.Join(resp.Header.Values(k), ", ") + "\n")
	}
	return b.String()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// httpNewRequest 是带 context 的最小封装，省得每处都写一遍。
func httpNewRequest(ctx context.Context, method, target string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, target, nil)
}

// readAllLimited 读取响应体，最多 n 字节。
func readAllLimited(resp *http.Response, n int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, n))
}

// compileRegex 编译用户给的正则；不合法时返回错误由调用方决定是否忽略。
func compileRegex(p string) (*regexp.Regexp, error) {
	if strings.TrimSpace(p) == "" {
		return nil, fmt.Errorf("空正则")
	}
	return regexp.Compile(p)
}

// defInt 取「可选整数」：0 或缺失用默认值，其余夹到区间里。
// 之前直接 clamp(v, lo, hi) 会把「没传」当成最小值（0 被夹成 lo），
// 结果默认并发变成 1、默认截断变成下限——工具没报错，但行为全错。
func defInt(v, def, lo, hi int) int {
	if v == 0 {
		v = def
	}
	return clamp(v, lo, hi)
}

// clamp 把越界的整数收进区间。
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

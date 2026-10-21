package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 探活与附着（P6-a）。
//
// 零常驻的前提是：任何一次接入都能在「不启动任何新进程」的前提下，先问清楚
// 「现在有没有活着的 ARTEX 可以连」。有就连上去（附着），没有才轮到 S3 去拉起。
//
// 这一层刻意不碰数据库、不碰 Manager、不开端口：它只是 HTTP 客户端。

// Endpoint 指一个已存在的 MCP Streamable HTTP 服务端。
type Endpoint struct {
	URL   string
	Token string
}

// EndpointInfo 是一次探活的结果。字段全部 omitempty，直接 JSON 输出给 `mcp status`。
type EndpointInfo struct {
	URL       string   `json:"url"`
	Alive     bool     `json:"alive"`
	Name      string   `json:"name,omitempty"`
	Title     string   `json:"title,omitempty"`
	Version   string   `json:"version,omitempty"`
	Protocols []string `json:"protocols,omitempty"`
	Tools     int      `json:"tools,omitempty"`
	LatencyMS int64    `json:"latency_ms"`
	Error     string   `json:"error,omitempty"`
}

// Normalized 补齐 scheme 与 /mcp 后缀。
//
// 用户填 127.0.0.1:8787 是常态，这里替他补全；已经写了 /mcp 的保持不动。
func (ep Endpoint) Normalized() Endpoint {
	u := strings.TrimSpace(ep.URL)
	if u == "" {
		u = "http://127.0.0.1:8787"
	}
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	u = strings.TrimRight(u, "/")
	if !strings.HasSuffix(u, "/mcp") {
		u += "/mcp"
	}
	return Endpoint{URL: u, Token: strings.TrimSpace(ep.Token)}
}

// probeTimeout 是单次探活的默认超时。
// 本地回环端口的 SYN 要么秒回要么秒拒，2 s 已经非常宽松；设长只会让 IDE 启动变慢。
const probeTimeout = 2 * time.Second

// Probe 探活一个端点。零副作用：只发 server/discover，失败的旧服务端退 tools/list。
//
// 之所以先 discover：2026-07-28 起它一次就能拿到 serverInfo + 协议版本，
// 比 initialize + tools/list 两次往返省一半时间。
func Probe(ctx context.Context, ep Endpoint, timeout time.Duration) EndpointInfo {
	ep = ep.Normalized()
	info := EndpointInfo{URL: ep.URL}
	if timeout <= 0 {
		timeout = probeTimeout
	}
	client := &http.Client{Timeout: timeout}
	start := time.Now()

	res, err := rpcPost(ctx, client, ep, "server/discover")
	if err != nil {
		// 旧服务端没有 server/discover（-32601），或返回了别的错。
		// 用 tools/list 再试一次：只要能回结果，就说明这个端点活着。
		res2, err2 := rpcPost(ctx, client, ep, "tools/list")
		info.LatencyMS = time.Since(start).Milliseconds()
		if err2 != nil {
			info.Error = err2.Error()
			return info
		}
		info.Alive = true
		info.Tools = countTools(res2)
		return info
	}

	info.LatencyMS = time.Since(start).Milliseconds()
	info.Alive = true
	if si, ok := res["serverInfo"].(map[string]any); ok {
		info.Name, _ = si["name"].(string)
		info.Title, _ = si["title"].(string)
		info.Version, _ = si["version"].(string)
	}
	if vs, ok := res["protocolVersions"].([]any); ok {
		for _, v := range vs {
			if s, ok := v.(string); ok {
				info.Protocols = append(info.Protocols, s)
			}
		}
	}
	// discover 不报工具数，需要就再问一次；status 里这个数字很实用，值得多一跳。
	if res2, err := rpcPost(ctx, client, ep, "tools/list"); err == nil {
		info.Tools = countTools(res2)
	}
	return info
}

// ProbeFirst 依次探活多个候选地址，返回第一个活的；全都不活则返回最后一个的结果。
//
// 顺序有意义：8799 是将来按需拉起的 headless 实例，8787 是控制台。
// 先看 headless 是为了「有专门的实例就优先用它」，避免把状态分叉到两个进程里。
func ProbeFirst(ctx context.Context, urls []string, token string, timeout time.Duration) EndpointInfo {
	var last EndpointInfo
	for _, u := range urls {
		if strings.TrimSpace(u) == "" {
			continue
		}
		info := Probe(ctx, Endpoint{URL: u, Token: token}, timeout)
		if info.Alive {
			return info
		}
		last = info
	}
	if last.URL == "" {
		last = EndpointInfo{Error: "没有可探测的地址"}
	}
	return last
}

// countTools 从 tools/list 的结果里数工具个数。
func countTools(res map[string]any) int {
	tools, _ := res["tools"].([]any)
	return len(tools)
}

// rpcPost 发一条 JSON-RPC 请求，返回 result（已解成 map）。
//
// 这里不复用 Response 结构：Response.Result 是 any，直接解成 map 更方便取字段，
// 而且探活只关心 result/error 两种形态。
func rpcPost(ctx context.Context, c *http.Client, ep Endpoint, method string) (map[string]any, error) {
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if method == "server/discover" {
		// 2026-07-28 无握手：协议版本与客户端能力随请求的 _meta 一起走。
		payload["params"] = map[string]any{"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion": ProtocolVersion,
			"clientCapabilities":                      map[string]any{},
			"clientInfo":                              map[string]any{"name": "artex-probe", "version": "1"},
		}}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	req.Header.Set("Mcp-Method", method)
	if ep.Token != "" {
		req.Header.Set("Authorization", "Bearer "+ep.Token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, clipText(string(raw), 160))
	}
	// 控制台是单页应用：未注册的路径会回 index.html（200）。
	// 这时候报「HTML」比报「JSON 解析失败」有用得多——用户一眼就知道是地址指错了
	// 或者对面是旧版本（本机就踩过：跑着的 exe 还没有 /mcp）。
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return nil, errors.New("该地址返回 HTML，不是 MCP 端点（可能是旧版控制台，或地址指向了静态站）")
	}
	txt := bytes.TrimSpace(raw)
	if len(txt) == 0 {
		return nil, errors.New("空响应")
	}
	var env struct {
		Result map[string]any `json:"result"`
		Error  *Error         `json:"error"`
	}
	if err := json.Unmarshal(txt, &env); err != nil {
		return nil, fmt.Errorf("响应不是 JSON-RPC: %v", err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("%s (code %d)", env.Error.Message, env.Error.Code)
	}
	if env.Result == nil {
		return nil, errors.New("响应缺少 result")
	}
	return env.Result, nil
}

// ProxyStdio 把 stdio 上的行式 JSON-RPC 原样转发到远端 HTTP 端点。
//
// 这是「附着」的实现：本进程不打开数据库、不创建 Manager、不监听端口，
// 只是一个透明桥。因此它能和已经跑着的实例共存，也不会触发任何任务调度。
//
// 串行转发：一个请求一个响应。MCP 客户端可能流水线发多个请求，
// 但我们的服务端是无状态的，串行只影响吞吐、不影响正确性。
func ProxyStdio(ctx context.Context, in io.Reader, out io.Writer, ep Endpoint) error {
	ep = ep.Normalized()
	br := bufio.NewReaderSize(in, 64<<10)
	enc := json.NewEncoder(out)
	// 工具调用可能跑很久，这里给足时间；真正的上限由服务端自己控。
	client := &http.Client{Timeout: 10 * time.Minute}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		frame, err := readFrame(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		frame = bytes.TrimSpace(frame)
		if len(frame) == 0 {
			continue
		}
		var req Request
		_ = json.Unmarshal(frame, &req)

		raw, err := forwardFrame(ctx, client, ep, frame)
		if err != nil {
			// 通知没有 id，失败就只能丢掉（无法回应）。
			if !hasID(req.ID) {
				continue
			}
			if e := enc.Encode(&Response{JSONRPC: "2.0", ID: req.ID,
				Error: &Error{Code: CodeInternal, Message: err.Error()}}); e != nil {
				return e
			}
			continue
		}
		if len(raw) == 0 {
			continue
		}
		if _, err := out.Write(raw); err != nil {
			return err
		}
	}
}

// forwardFrame 发一帧，返回要写回 stdout 的字节（含换行）；空切片表示无输出。
func forwardFrame(ctx context.Context, c *http.Client, ep Endpoint, frame []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	if ep.Token != "" {
		req.Header.Set("Authorization", "Bearer "+ep.Token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, MaxFrameBytes))
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, clipText(string(raw), 200))
	}
	txt := bytes.TrimSpace(raw)
	if len(txt) == 0 {
		return nil, nil
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return sseToFrame(txt), nil
	}
	return append(txt, '\n'), nil
}

// sseToFrame 把 SSE 响应里的 data 行拼成一条或多条 JSON-RPC 帧。
//
// 我们的服务端目前直接回 JSON，这个分支是为兼容会走 SSE 的第三方实现。
func sseToFrame(raw []byte) []byte {
	var out bytes.Buffer
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		out.Write(payload)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// clipText 截断长文本，避免把整个 HTML 错误页塞进 stderr。
func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

package mcpserver

import (
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

// CallTool 在远端的 /mcp 上执行一次 tools/call，把文本结果取回来。
//
// 只有「本进程没有引擎」的场景才用它（stdio 里的 L3）：它不维护会话、不重试、
// 不做任何降级。失败就把服务端给的原因原样带回去——上层必须能明确告诉用户
// 「引擎连不上」，而不是悄悄返回一个空结果让人以为任务建好了。
func CallTool(ctx context.Context, ep Endpoint, name string, args json.RawMessage, timeout time.Duration) (string, error) {
	ep = ep.Normalized()
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("缺少工具名")
	}
	if timeout <= 0 {
		// 建任务可能跑很久；真正的上限由服务端自己控，这里只防「永远不返回」。
		timeout = 10 * time.Minute
	}

	argObj := json.RawMessage("{}")
	if trimmed := bytes.TrimSpace(args); len(trimmed) > 0 && string(trimmed) != "null" {
		argObj = trimmed
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "bridge-1",
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": argObj},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	req.Header.Set("Mcp-Method", "tools/call")
	if ep.Token != "" {
		req.Header.Set("Authorization", "Bearer "+ep.Token)
	}

	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return "", errors.New("远端拒绝：token 不匹配（stdio 与目标实例用的不是同一个 token）")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, clipText(string(raw), 200))
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return "", errors.New("该地址返回 HTML，不是 MCP 端点")
	}

	var env struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *Error `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &env); err != nil {
		return "", fmt.Errorf("响应不是 JSON-RPC: %v", err)
	}
	if env.Error != nil {
		return "", fmt.Errorf("%s (code %d)", env.Error.Message, env.Error.Code)
	}
	if env.Result == nil {
		return "", errors.New("响应缺少 result")
	}

	parts := make([]string, 0, len(env.Result.Content))
	for _, c := range env.Result.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	out := strings.Join(parts, "\n")
	if env.Result.IsError {
		if out == "" {
			out = "工具执行失败（未返回原因）"
		}
		return "", errors.New(out)
	}
	return out, nil
}

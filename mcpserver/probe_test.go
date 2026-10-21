package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newRPCServer 起一个只有 /mcp 的假服务端，按 method 分发。
// id 原样回显——真实服务端也是这样，代理层不改写 id。
func newRPCServer(t *testing.T, h func(id json.RawMessage, method string, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		h(req.ID, req.Method, w)
	}))
}

func writeResult(w http.ResponseWriter, id json.RawMessage, v map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": v})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	})
}

// TestProbeDiscover：新服务端一次 discover 就能拿到 serverInfo 与协议版本。
func TestProbeDiscover(t *testing.T) {
	srv := newRPCServer(t, func(id json.RawMessage, method string, w http.ResponseWriter) {
		switch method {
		case "server/discover":
			writeResult(w, id, map[string]any{
				"resultType":       "complete",
				"serverInfo":       map[string]any{"name": "artex", "title": "ARTEX-REA", "version": "9.9.9"},
				"protocolVersions": []string{"2026-07-28", "2025-06-18"},
			})
		case "tools/list":
			writeResult(w, id, map[string]any{"tools": []any{
				map[string]any{"name": "a"}, map[string]any{"name": "b"}, map[string]any{"name": "c"},
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer srv.Close()

	info := Probe(context.Background(), Endpoint{URL: srv.URL}, 2*time.Second)
	if !info.Alive {
		t.Fatalf("应当探活成功: %+v", info)
	}
	if info.Name != "artex" || info.Title != "ARTEX-REA" || info.Version != "9.9.9" {
		t.Fatalf("serverInfo 解析错: %+v", info)
	}
	if info.Tools != 3 {
		t.Fatalf("工具数应为 3，实际 %d", info.Tools)
	}
	if len(info.Protocols) != 2 || info.Protocols[0] != "2026-07-28" {
		t.Fatalf("协议版本解析错: %v", info.Protocols)
	}
}

// TestProbeFallsBackToToolsList：老服务端没有 server/discover，退 tools/list 仍算活着。
func TestProbeFallsBackToToolsList(t *testing.T) {
	srv := newRPCServer(t, func(id json.RawMessage, method string, w http.ResponseWriter) {
		switch method {
		case "server/discover":
			writeRPCError(w, id, CodeMethodNotFound, "method not found")
		case "tools/list":
			writeResult(w, id, map[string]any{"tools": []any{
				map[string]any{"name": "x"}, map[string]any{"name": "y"},
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer srv.Close()

	info := Probe(context.Background(), Endpoint{URL: srv.URL}, 2*time.Second)
	if !info.Alive {
		t.Fatalf("回落 tools/list 后应判活: %+v", info)
	}
	if info.Tools != 2 {
		t.Fatalf("工具数应为 2，实际 %d", info.Tools)
	}
	if info.Name != "" {
		t.Fatalf("老服务端不该有 serverInfo: %+v", info)
	}
}

// TestProbeUnauthorized：token 不对时不能误判为「活着」。
func TestProbeUnauthorized(t *testing.T) {
	srv := newRPCServer(t, func(id json.RawMessage, method string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	})
	defer srv.Close()

	info := Probe(context.Background(), Endpoint{URL: srv.URL}, 2*time.Second)
	if info.Alive {
		t.Fatalf("401 不该判活: %+v", info)
	}
	if !strings.Contains(info.Error, "401") {
		t.Fatalf("错误里应包含状态码: %q", info.Error)
	}
}

// TestProbeHTMLResponse：控制台的 SPA 回退会给出 200 + HTML，必须明确说"不是 MCP 端点"。
// 本机实测踩过：跑着的旧版 exe 没有 /mcp，就走这条路。
func TestProbeHTMLResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html></html>"))
	}))
	defer srv.Close()

	info := Probe(context.Background(), Endpoint{URL: srv.URL}, 2*time.Second)
	if info.Alive {
		t.Fatalf("HTML 不该判活: %+v", info)
	}
	if !strings.Contains(info.Error, "HTML") {
		t.Fatalf("错误应点明是 HTML: %q", info.Error)
	}
}

// TestProbeDeadEndpoint：连不上时只报错、不 panic、不卡住。
func TestProbeDeadEndpoint(t *testing.T) {
	info := Probe(context.Background(), Endpoint{URL: "http://127.0.0.1:1"}, 300*time.Millisecond)
	if info.Alive {
		t.Fatalf("端口 1 不可能活着: %+v", info)
	}
	if info.Error == "" {
		t.Fatal("应有错误信息")
	}
}

// TestProxyStdio 验证 stdio 帧被逐条转发，并按原 id 回写。
func TestProxyStdio(t *testing.T) {
	srv := newRPCServer(t, func(id json.RawMessage, method string, w http.ResponseWriter) {
		writeResult(w, id, map[string]any{"tools": []any{}})
	})
	defer srv.Close()

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := ProxyStdio(context.Background(), in, &out, Endpoint{URL: srv.URL}); err != nil {
		t.Fatalf("代理失败: %v", err)
	}
	lines := nonEmptyLines(out.String())
	if len(lines) != 2 {
		t.Fatalf("应回 2 帧，实际 %d: %q", len(lines), out.String())
	}
	for i, line := range lines {
		var resp struct {
			ID     json.RawMessage `json:"id"`
			Result map[string]any  `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("第 %d 帧不是 JSON: %v", i+1, err)
		}
		if string(resp.ID) != strconv.Itoa(i+1) {
			t.Fatalf("第 %d 帧 id 错: %s", i+1, resp.ID)
		}
		if resp.Result == nil {
			t.Fatalf("第 %d 帧缺 result", i+1)
		}
	}
}

// TestProxyStdioTransportError：远端 500 时，客户端应收到带原 id 的 JSON-RPC 错误。
func TestProxyStdioTransportError(t *testing.T) {
	srv := newRPCServer(t, func(id json.RawMessage, method string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})
	defer srv.Close()

	in := strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := ProxyStdio(context.Background(), in, &out, Endpoint{URL: srv.URL}); err != nil {
		t.Fatalf("代理失败: %v", err)
	}
	lines := nonEmptyLines(out.String())
	if len(lines) != 1 {
		t.Fatalf("应回 1 帧: %q", out.String())
	}
	var resp struct {
		ID    json.RawMessage `json:"id"`
		Error *Error          `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("不是 JSON: %v", err)
	}
	if string(resp.ID) != "7" {
		t.Fatalf("id 应保持 7: %s", resp.ID)
	}
	if resp.Error == nil || resp.Error.Code != CodeInternal {
		t.Fatalf("应为 -32603: %+v", resp.Error)
	}
}

// TestProxyStdioNotification：通知（无 id）不回帧。
func TestProxyStdioNotification(t *testing.T) {
	srv := newRPCServer(t, func(id json.RawMessage, method string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusAccepted)
	})
	defer srv.Close()

	in := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	var out bytes.Buffer
	if err := ProxyStdio(context.Background(), in, &out, Endpoint{URL: srv.URL}); err != nil {
		t.Fatalf("代理失败: %v", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("通知不该有回帧: %q", out.String())
	}
}

// TestEndpointNormalized 覆盖用户手填地址的几种写法。
func TestEndpointNormalized(t *testing.T) {
	cases := []struct{ in, want string }{
		{"127.0.0.1:8787", "http://127.0.0.1:8787/mcp"},
		{"http://127.0.0.1:8787/", "http://127.0.0.1:8787/mcp"},
		{"http://127.0.0.1:8787/mcp", "http://127.0.0.1:8787/mcp"},
		{"", "http://127.0.0.1:8787/mcp"},
	}
	for _, c := range cases {
		if got := (Endpoint{URL: c.in}).Normalized().URL; got != c.want {
			t.Fatalf("Normalized(%q)=%q，期望 %q", c.in, got, c.want)
		}
	}
}

func nonEmptyLines(s string) []string {
	out := make([]string, 0, 4)
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

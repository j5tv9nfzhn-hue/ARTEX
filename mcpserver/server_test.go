package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixture() *Server {
	return New(Config{
		Name:         "artex",
		Title:        "ARTEX-REA",
		Version:      "test",
		Instructions: "用 artex 的工具做验证。",
		Tiers:        []Tier{L0, L1},
		Tools: []Tool{
			{
				Name: "echo", Description: "原样回显", Tier: L0, ReadOnly: true,
				Schema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
				Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
					return "echo:" + string(args), nil
				},
			},
			{
				Name: "probe", Description: "主动探测（L2）", Tier: L2,
				Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return "probed", nil },
			},
			{
				Name: "boom", Description: "总是失败", Tier: L0,
				Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
					return "", errors.New("kaboom")
				},
			},
		},
	})
}

func frame(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func decodeResult(t *testing.T, resp any) map[string]any {
	t.Helper()
	b := frame(t, resp)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return m
}

func TestInitializeNegotiatesProtocol(t *testing.T) {
	s := fixture()
	cases := []struct{ want, given string }{
		{"2025-06-18", "2025-06-18"},
		{ProtocolVersion, "1999-01-01"},
		{ProtocolVersion, ""},
	}
	for _, c := range cases {
		raw := frame(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"protocolVersion": c.given}})
		out := decodeResult(t, s.HandleFrame(context.Background(), raw))
		res, _ := out["result"].(map[string]any)
		if got, _ := res["protocolVersion"].(string); got != c.want {
			t.Fatalf("given %q: protocolVersion=%q want %q", c.given, got, c.want)
		}
	}
}

func TestToolsListFiltersByTier(t *testing.T) {
	s := fixture()
	raw := frame(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	out := decodeResult(t, s.HandleFrame(context.Background(), raw))
	res, _ := out["result"].(map[string]any)
	list, _ := res["tools"].([]any)
	names := map[string]bool{}
	for _, item := range list {
		m, _ := item.(map[string]any)
		names[m["name"].(string)] = true
	}
	if !names["echo"] || !names["boom"] {
		t.Fatalf("L0 工具缺失: %v", names)
	}
	if names["probe"] {
		t.Fatalf("L2 工具不应出现在 L0+L1 配置里: %v", names)
	}
	if len(list) != 2 {
		t.Fatalf("工具数=%d want 2", len(list))
	}
}

func TestCallToolSuccess(t *testing.T) {
	s := fixture()
	raw := frame(t, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "echo", "arguments": map[string]any{"text": "hi"}}})
	out := decodeResult(t, s.HandleFrame(context.Background(), raw))
	res, _ := out["result"].(map[string]any)
	if res["isError"] != false {
		t.Fatalf("isError 应为 false: %v", res)
	}
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content 长度=%d", len(content))
	}
	block, _ := content[0].(map[string]any)
	if text, _ := block["text"].(string); !strings.Contains(text, "echo:") || !strings.Contains(text, "hi") {
		t.Fatalf("text=%q", text)
	}
}

func TestCallToolErrorIsReportedInResult(t *testing.T) {
	s := fixture()
	raw := frame(t, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "boom"}})
	out := decodeResult(t, s.HandleFrame(context.Background(), raw))
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("工具失败必须走 result.isError 而不是协议错误: %v", out)
	}
	if res["isError"] != true {
		t.Fatalf("isError 应为 true: %v", res)
	}
	content, _ := res["content"].([]any)
	block, _ := content[0].(map[string]any)
	if text, _ := block["text"].(string); !strings.Contains(text, "kaboom") {
		t.Fatalf("错误原因未透出: %q", text)
	}
}

func TestUnknownToolAndMethod(t *testing.T) {
	s := fixture()
	out := decodeResult(t, s.HandleFrame(context.Background(), frame(t, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": map[string]any{"name": "nope"}})))
	errObj, _ := out["error"].(map[string]any)
	if code, _ := errObj["code"].(float64); int(code) != CodeInvalidParams {
		t.Fatalf("unknown tool code=%v", errObj)
	}
	out = decodeResult(t, s.HandleFrame(context.Background(), frame(t, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "bogus/method"})))
	errObj, _ = out["error"].(map[string]any)
	if code, _ := errObj["code"].(float64); int(code) != CodeMethodNotFound {
		t.Fatalf("unknown method code=%v", errObj)
	}
}

func TestNotificationGetsNoResponse(t *testing.T) {
	s := fixture()
	raw := frame(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if resp := s.HandleFrame(context.Background(), raw); resp != nil {
		t.Fatalf("通知不应产生响应: %v", resp)
	}
}

func TestStdioRoundTrip(t *testing.T) {
	s := fixture()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := s.ServeStdio(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("期望 2 条响应，得到 %d: %q", len(lines), out.String())
	}
	for _, ln := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("响应不是合法 JSON: %v (%s)", err, ln)
		}
		if m["jsonrpc"] != "2.0" {
			t.Fatalf("jsonrpc 字段错误: %v", m)
		}
	}
}

func TestHTTPRequiresBearer(t *testing.T) {
	s := fixture()
	h := s.HTTPHandler(HTTPConfig{Authorize: func(r *http.Request) error {
		if r.Header.Get("Authorization") != "Bearer good" {
			return errors.New("bad token")
		}
		return nil
	}})
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401，得到 %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer good")
	req.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post with token: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("带 token 应 200，得到 %d", resp2.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := m["result"]; !ok {
		t.Fatalf("缺少 result: %v", m)
	}
}

func TestHTTPRejectsForeignOriginAndGet(t *testing.T) {
	s := fixture()
	h := s.HTTPHandler(HTTPConfig{
		Authorize:   func(r *http.Request) error { return nil },
		AllowOrigin: func(o string) bool { return strings.HasPrefix(o, "http://127.0.0.1") },
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("外站 Origin 应 403，得到 %d", resp.StatusCode)
	}

	resp2, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405，得到 %d", resp2.StatusCode)
	}
}

func TestAllowAndDenyLists(t *testing.T) {
	s := New(Config{
		Tiers:      []Tier{L0},
		DenyTools:  []string{"boom"},
		AllowTools: []string{"echo", "boom"},
		Tools: []Tool{
			{Name: "echo", Tier: L0, Handler: func(context.Context, json.RawMessage) (string, error) { return "e", nil }},
			{Name: "boom", Tier: L0, Handler: func(context.Context, json.RawMessage) (string, error) { return "b", nil }},
			{Name: "other", Tier: L0, Handler: func(context.Context, json.RawMessage) (string, error) { return "o", nil }},
		},
	})
	got := s.Tools()
	if len(got) != 1 || got[0].Name != "echo" {
		t.Fatalf("白名单+黑名单交集应为 [echo]，得到 %v", got)
	}
}

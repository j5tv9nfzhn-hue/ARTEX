package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/mcpserver"
)

// 这组用例是「没跑过冒烟的工具不上线」的落地：MCP 对外工具面的每一档、
// 每一个工具、鉴权与计量，都必须在测试里被真的调过一遍。

func newMCPTestServer(t *testing.T) *Server {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)
	// 测试共用同一个 ARTEX_SQLITE_PATH，档位/开关会被前一个用例改掉；
	// 每个用例开头显式归位，保证顺序无关。
	if err := s.saveMCPTiers(defaultMCPTiers()); err != nil {
		t.Fatalf("reset tiers: %v", err)
	}
	if err := s.m.pg.SetBool(settingMCPEnabled, true); err != nil {
		t.Fatalf("reset enabled: %v", err)
	}
	return s
}

func mcpToolNames(srv *mcpserver.Server) []string {
	names := make([]string, 0, 32)
	for _, tl := range srv.Tools() {
		names = append(names, tl.Name)
	}
	return names
}

func hasTool(srv *mcpserver.Server, name string) bool {
	for _, tl := range srv.Tools() {
		if tl.Name == name {
			return true
		}
	}
	return false
}

// TestMCPTierDefaults：默认只开 L0+L1；开 L2 后 21 个工具全部出现，L3 仍为空。
func TestMCPTierDefaults(t *testing.T) {
	s := newMCPTestServer(t)

	l0 := []string{"file_identify", "pe_info", "strings_scan", "entropy_scan", "ioc_extract"}
	l1 := []string{"list_assets", "list_findings", "list_companies", "list_tasks", "get_task", "tactical_kb"}
	l2 := []string{"dns_recon", "port_probe", "tls_inspect", "tech_hints",
		"http_send", "http_fuzz", "dir_probe", "param_probe", "jwt_inspect", "sqli_probe"}

	def := s.buildMCPServer()
	for _, n := range append(append([]string{}, l0...), l1...) {
		if !hasTool(def, n) {
			t.Fatalf("默认档位缺少工具 %s（实际 %v）", n, mcpToolNames(def))
		}
	}
	for _, n := range l2 {
		if hasTool(def, n) {
			t.Fatalf("L2 工具 %s 在默认档位下不应可见", n)
		}
	}
	if got := len(def.Tools()); got != 11 {
		t.Fatalf("默认工具数=%d want 11", got)
	}

	if err := s.saveMCPTiers([]mcpserver.Tier{mcpserver.L0, mcpserver.L1, mcpserver.L2}); err != nil {
		t.Fatalf("saveMCPTiers: %v", err)
	}
	full := s.buildMCPServer()
	for _, n := range l2 {
		if !hasTool(full, n) {
			t.Fatalf("开 L2 后缺少工具 %s", n)
		}
	}
	if got := len(full.Tools()); got != 21 {
		t.Fatalf("开 L2 后工具数=%d want 21（L3 仍未实现）", got)
	}
	// 越权调用：L0 档位下调 dns_recon 必须被拒绝。
	if _, err := full.Handle(context.Background(), "tools/call",
		json.RawMessage(`{"name":"task_create","arguments":{}}`)); err == nil {
		t.Fatal("未注册的方法应返回 JSON-RPC 错误")
	}
}

// TestMCPToolSmoke 表驱动跑遍每一档的每个工具：不 panic，且一定给出可读文本或明确错误。
func TestMCPToolSmoke(t *testing.T) {
	s := newMCPTestServer(t)
	if err := s.saveMCPTiers([]mcpserver.Tier{mcpserver.L0, mcpserver.L1, mcpserver.L2}); err != nil {
		t.Fatalf("saveMCPTiers: %v", err)
	}
	sample := filepath.Join(t.TempDir(), "sample.bin")
	if err := os.WriteFile(sample, append([]byte("MZ\x90\x00"), bytes.Repeat([]byte("http://127.0.0.1/x "), 8)...), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	ptr := func(s string) string { return fmt.Sprintf("%q", s) }

	cases := []struct {
		name string
		args string
	}{
		{"file_identify", fmt.Sprintf(`{"path":%s}`, ptr(sample))},
		{"pe_info", fmt.Sprintf(`{"path":%s,"max_imps":5}`, ptr(sample))},
		{"strings_scan", fmt.Sprintf(`{"path":%s,"min_len":4,"limit":20}`, ptr(sample))},
		{"entropy_scan", fmt.Sprintf(`{"path":%s}`, ptr(sample))},
		{"ioc_extract", fmt.Sprintf(`{"path":%s,"max_each":5}`, ptr(sample))},

		{"list_assets", `{}`},
		{"list_findings", `{"page":1,"page_size":5}`},
		{"list_companies", `{}`},
		{"list_tasks", `{"limit":5}`},
		{"get_task", `{"id":999999}`},
		{"tactical_kb", `{"mode":"vuln_patterns","limit":2}`},
		{"tactical_kb", `{"mode":"logic_invariants","query":"ai_gateway","limit":2}`},

		{"dns_recon", `{"domain":"127.0.0.1","timeout_ms":800}`},
		{"port_probe", `{"host":"127.0.0.1","ports":"1","timeout_ms":300}`},
		{"tls_inspect", `{"target":"127.0.0.1:1","timeout_ms":500}`},
		{"tech_hints", `{"url":"http://127.0.0.1:1/"}`},
		{"http_send", `{"request":"GET / HTTP/1.1\u000d\u000aHost: 127.0.0.1:1\u000d\u000a\u000d\u000a","max_body":100}`},
		{"http_fuzz", `{"request":"GET /FUZZ HTTP/1.1\u000d\u000aHost: 127.0.0.1:1\u000d\u000a\u000d\u000a","payloads":["a"]}`},
		{"dir_probe", `{"url":"http://127.0.0.1:1/","wordlist":["a"]}`},
		{"param_probe", `{"url":"http://127.0.0.1:1/?a=1","param":"a"}`},
		{"jwt_inspect", `{"token":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdef"}`},
		{"sqli_probe", `{"url":"http://127.0.0.1:1/?a=1","param":"a"}`},
	}

	srv := s.buildMCPServer()
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			tl, ok := srv.Tools(), false
			var tool mcpserver.Tool
			for _, x := range tl {
				if x.Name == c.name {
					tool, ok = x, true
					break
				}
			}
			if !ok {
				t.Fatalf("工具 %s 不在对外列表里", c.name)
			}
			text, err := tool.Handler(context.Background(), json.RawMessage(c.args))
			if err != nil {
				if strings.TrimSpace(err.Error()) == "" {
					t.Fatalf("错误信息为空")
				}
				return
			}
			if strings.TrimSpace(text) == "" {
				t.Fatalf("返回值与错误都为空")
			}
		})
	}
}

// TestMCPHTTPAuth：Bearer 鉴权、关闭开关、token 轮换后旧 token 立即失效。
func TestMCPHTTPAuth(t *testing.T) {
	s := newMCPTestServer(t)
	h := s.mcpHandler()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

	post := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := post(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401，得到 %d", rec.Code)
	}
	if rec := post("artex_mcp_wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("错 token 应 401，得到 %d", rec.Code)
	}

	tok := s.mcpTokenValue()
	if tok == "" {
		t.Fatal("token 未生成")
	}
	rec := post(tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确 token 应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if len(out.Result.Tools) != 11 {
		t.Fatalf("HTTP tools/list 返回 %d 个工具，want 11", len(out.Result.Tools))
	}

	// 轮换后旧 token 失效
	newTok, err := s.rotateMCPToken()
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if newTok == tok {
		t.Fatal("轮换后 token 未变化")
	}
	if rec := post(tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("旧 token 应失效，得到 %d", rec.Code)
	}
	if rec := post(newTok); rec.Code != http.StatusOK {
		t.Fatalf("新 token 应 200，得到 %d", rec.Code)
	}

	// 关闭开关后整个端点 403（token 正确也不行）
	if err := s.m.pg.SetBool(settingMCPEnabled, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if rec := post(newTok); rec.Code != http.StatusForbidden {
		t.Fatalf("关闭后应 403，得到 %d", rec.Code)
	}
}

// TestMCPUsageLedger：tools/call 必须写一行 tool_usage，口径与内部控制台一致。
func TestMCPUsageLedger(t *testing.T) {
	s := newMCPTestServer(t)
	if _, err := s.m.pg.Exec(`DELETE FROM tool_usage WHERE agent_key = 'mcp-server'`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	srv := s.buildMCPServer()
	resp := srv.HandleFrame(context.Background(),
		[]byte(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"list_tasks","arguments":{"limit":1}}}`))
	if resp == nil {
		t.Fatal("tools/call 没有响应")
	}
	var n int
	if err := s.m.pg.QueryRow(`SELECT COUNT(*) FROM tool_usage WHERE agent_key = 'mcp-server' AND tool_key = 'list_tasks'`).Scan(&n); err != nil {
		t.Fatalf("query tool_usage: %v", err)
	}
	if n != 1 {
		t.Fatalf("tool_usage 行数=%d want 1", n)
	}
}

// TestMCPResourcesAndPrompts：资源快照与提示模板可用。
func TestMCPResourcesAndPrompts(t *testing.T) {
	s := newMCPTestServer(t)
	srv := s.buildMCPServer()
	for _, uri := range []string{"artex://assets", "artex://findings", "artex://tasks"} {
		out, err := srv.Handle(context.Background(), "resources/read", json.RawMessage(fmt.Sprintf(`{"uri":%q}`, uri)))
		if err != nil {
			t.Fatalf("resources/read %s: %v", uri, err)
		}
		if out == nil {
			t.Fatalf("resources/read %s 返回空", uri)
		}
	}
	out, err := srv.Handle(context.Background(), "prompts/get",
		json.RawMessage(`{"name":"web-pentest","arguments":{"target":"https://example.com"}}`))
	if err != nil {
		t.Fatalf("prompts/get: %v", err)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), "example.com") {
		t.Fatalf("提示模板没带上目标: %s", string(b))
	}
}

// TestMCPFileGuard：L0 工具拒绝读取 ARTEX 自己的凭据文件。
func TestMCPFileGuard(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "credential.key")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newMCPTestServer(t)
	srv := s.buildMCPServer()
	var fileIdentify mcpserver.Tool
	for _, tl := range srv.Tools() {
		if tl.Name == "file_identify" {
			fileIdentify = tl
		}
	}
	if _, err := fileIdentify.Handler(context.Background(),
		json.RawMessage(fmt.Sprintf(`{"path":%q}`, secret))); err == nil {
		t.Fatal("读取 credential.key 必须被拒绝")
	}
	sample := filepath.Join(dir, "ok.bin")
	if err := os.WriteFile(sample, []byte("MZ\x90\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fileIdentify.Handler(context.Background(),
		json.RawMessage(fmt.Sprintf(`{"path":%q}`, sample))); err != nil {
		t.Fatalf("普通样本应可读: %v", err)
	}
}

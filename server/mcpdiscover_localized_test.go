package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// connectMCP 中文错误回归测试：stdio 无命令、http 无 URL、sse 无 URL、未知传输。
// 四条路径都在 dial 之前返回，可无数据库、无网络直接调用。
// 复用 assertChineseError，并保留传输枚举与 URL。
func TestConnectMCPErrorsLocalized(t *testing.T) {
	// 固定错误常量。
	for _, c := range []struct{ label, msg string }{
		{"stdio_no_command", errMCPStdioNoCommand},
		{"http_no_url", errMCPHTTPNoURL},
		{"sse_no_url", errMCPSSENoURL},
		{"unknown_transport", errMCPUnknownTransportFmt},
	} {
		assertChineseError(t, c.label, c.msg)
	}

	// 实际执行四个拒绝路径，均在 dial 前返回。
	for _, c := range []struct {
		label string
		srv   *db.MCPServer
	}{
		{"stdio_missing_command", &db.MCPServer{Transport: "stdio", Command: ""}},
		{"http_missing_url", &db.MCPServer{Transport: "http", URL: ""}},
		{"sse_missing_url", &db.MCPServer{Transport: "sse", URL: ""}},
		{"unknown_transport_path", &db.MCPServer{Transport: "websocket"}},
	} {
		_, err := connectMCP(context.Background(), c.srv)
		if err == nil {
			t.Fatalf("%s：未返回错误（验证通过）", c.label)
		}
		assertChineseError(t, c.label, err.Error())
	}
}

func TestPrepareStdioMCPInjectsNPXYesAndEnv(t *testing.T) {
	_, args, env := prepareStdioMCP("npx", []string{"@modelcontextprotocol/server-filesystem", "--headless"}, map[string]string{"FOO": "bar"})
	if len(args) < 3 || args[0] != "-y" || args[1] != "@modelcontextprotocol/server-filesystem" {
		t.Fatalf("expected -y prepended to npx args, got %v", args)
	}
	if env["npm_config_yes"] != "true" || env["NPM_CONFIG_YES"] != "true" || env["FOO"] != "bar" {
		t.Fatalf("unexpected stdio env: %v", env)
	}

	_, args2, _ := prepareStdioMCP("npx", []string{"-y", "@modelcontextprotocol/server-filesystem", "--headless"}, nil)
	if len(args2) != 3 || args2[0] != "-y" {
		t.Fatalf("expected existing -y preserved without duplication, got %v", args2)
	}
}

func TestSeedBuiltinMCPToolsPopulatesCache(t *testing.T) {
	pg, err := db.Open("file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "mcp-seed.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })

	servers, err := pg.ListMCP()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*db.MCPServer{}
	for _, s := range servers {
		byName[s.Name] = s
	}

	ss := byName["ScopeSentry"]
	if ss == nil || len(ss.Tools) != 17 || !slices.Contains(ss.Tools, "mcp__ScopeSentry__list_assets") {
		t.Fatalf("expected ScopeSentry seeded with 17 tools, got %+v", ss)
	}

	br := byName["browser"]
	if br == nil || len(br.Tools) != 22 || !slices.Contains(br.Tools, "mcp__browser__browser_navigate") {
		t.Fatalf("expected browser seeded with 22 tools, got %+v", br)
	}
	var brArgs []string
	if err := json.Unmarshal(br.Args, &brArgs); err != nil || !slices.Contains(brArgs, "-y") {
		t.Fatalf("expected browser args to include -y, got %s (%v)", string(br.Args), err)
	}
}

package server

import (
	"context"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcpbuiltin"
)

// 证明「内置 Go MCP」不是只会在目录里出现：connectMCP 真的能拿到工具，
// 名字符合 mcp__<server>__<tool>，并且未知名字会明确失败（不静默返回空集）。
func TestBuiltinMCPConnect(t *testing.T) {
	ctx := context.Background()
	// 线上形态：transport=stdio + command=artex-builtin（表上有 CHECK 约束，不新增传输值）。
	cl, err := connectMCP(ctx, &db.MCPServer{Name: "artex-webscan", Transport: "stdio", Command: mcpbuiltin.Command})
	if err != nil {
		t.Fatalf("连接内置服务器失败：%v", err)
	}
	defer cl.Close()
	tools, err := cl.Tools(ctx)
	if err != nil {
		t.Fatalf("tools/list 失败：%v", err)
	}
	if len(tools) == 0 {
		t.Fatal("内置服务器不应返回空工具集")
	}
	for _, tool := range tools {
		if !strings.HasPrefix(tool.Name(), "mcp__artex-webscan__") {
			t.Fatalf("工具命名不符合 mcp__<server>__<tool>：%s", tool.Name())
		}
		if tool.Description() == "" || tool.InputSchema() == nil {
			t.Fatalf("%s 缺少描述或 schema", tool.Name())
		}
	}
	found := false
	for _, tool := range tools {
		if tool.Name() == "mcp__artex-webscan__http_send" {
			found = true
		}
	}
	if !found {
		t.Fatal("缺少 http_send（原始请求复现工具）")
	}
}

func TestBuiltinMCPUknownName(t *testing.T) {
	if _, err := connectMCP(context.Background(), &db.MCPServer{Name: "artex-nope", Transport: "stdio", Command: mcpbuiltin.Command}); err == nil {
		t.Fatal("未知内置服务器应报错，而不是返回空客户端")
	}
}

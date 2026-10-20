package server

import (
	"context"

	"github.com/Autumn-27/artex/mcpbuiltin"
	"github.com/Autumn-27/norma/mcp"
	actool "github.com/Autumn-27/norma/tool"
)

// builtinMCPClient 是「进程内 MCP 服务器」的客户端。
//
// 与 stdio/http/sse 不同，它不建立连接、不启子进程：Tools() 直接返回 Go 实现好的
// CoreTool 列表。工具命名仍是 mcp__<server>__<tool>，所以工具缓存、agent 绑定、
// deferred 解锁、计量与审计全部沿用既有链路，没有任何特殊分支。
type builtinMCPClient struct{ name string }

func (c *builtinMCPClient) Tools(ctx context.Context) ([]actool.CoreTool, error) {
	return mcp.InProcessServer(c.name, mcpbuiltin.Tools(c.name)), nil
}

func (c *builtinMCPClient) Close() error { return nil }

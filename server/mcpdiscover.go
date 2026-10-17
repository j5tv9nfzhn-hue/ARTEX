package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcphttp"
	"github.com/Autumn-27/norma/mcp"
	actool "github.com/Autumn-27/norma/tool"
)

// mcpClient is the shared surface of a connected MCP server (stdio, Streamable HTTP,
// or legacy SSE),
// so tools/list and cleanup are handled uniformly regardless of transport.
type mcpClient interface {
	Tools(context.Context) ([]actool.CoreTool, error)
	Close() error
}

// connectMCP 的四类传输配置错误通过 pgRefreshMCP 的 502 正文显示。
// Agent 组装、启动发现、ScopeSentry 同步和保存后发现只记录或丢弃错误。
// 因此这些验证消息中文化；stdio、http、sse 与 URL 为协议标识，保持原文。
const (
	errMCPStdioNoCommand      = "stdio 传输方式缺少命令"
	errMCPHTTPNoURL           = "http 传输方式缺少 URL"
	errMCPSSENoURL            = "sse 传输方式缺少 URL"
	errMCPUnknownTransportFmt = "未知的传输方式：%q"
)

// connectMCP dials one MCP server per its transport. Callers must Close the client.
func connectMCP(ctx context.Context, m *db.MCPServer) (mcpClient, error) {
	// env/header 值可能带 enc:v1: 密文（计划 #49）。拨号前在唯一出口解成明文；
	// 解不开（主密钥丢失/密文损坏）就明确失败，绝不把密文当明文塞进
	// Authorization 头——那只会得到一个原因离现场很远的 401。
	env, err := db.MCPEnvPlain(m.Env)
	if err != nil {
		return nil, fmt.Errorf("MCP %s：%w", m.Name, err)
	}
	switch m.Transport {
	case "stdio":
		if m.Command == "" {
			return nil, errors.New(errMCPStdioNoCommand)
		}
		return mcp.NewStdioClient(ctx, m.Name, m.Command, env, jsonStrSlice(m.Args)...)
	case "http":
		if m.URL == "" {
			return nil, errors.New(errMCPHTTPNoURL)
		}
		// env map doubles as HTTP headers (e.g. Authorization).
		return mcphttp.New(ctx, m.Name, m.URL, env, m.Insecure)
	case "sse":
		if m.URL == "" {
			return nil, errors.New(errMCPSSENoURL)
		}
		return mcphttp.NewSSE(ctx, m.Name, m.URL, env, m.Insecure)
	default:
		return nil, fmt.Errorf(trMsg(errMCPUnknownTransportFmt), m.Transport)
	}
}

// discoverAndCacheMCP connects to one MCP, lists its tools, and persists the tool
// names to mcp_tools_cache so the UI shows them without a live connection.
func (s *Server) discoverAndCacheMCP(ctx context.Context, m *db.MCPServer) error {
	managed, err := s.m.pg.REMCPManaged(m.ID)
	if err != nil {
		return err
	}
	if managed {
		return errors.New("逆向受管 MCP 请从软件分析工作台发现，避免抢占分析会话")
	}

	cl, err := connectMCP(ctx, m)
	if err != nil {
		return err
	}
	defer cl.Close()
	ts, err := cl.Tools(ctx)
	if err != nil {
		return err
	}
	tools := make([]db.MCPTool, 0, len(ts))
	for _, t := range ts {
		tools = append(tools, db.MCPTool{Name: t.Name(), Description: t.Description()})
	}
	if err := s.m.pg.SaveMCPTools(m.ID, tools); err != nil {
		return err
	}
	log.Printf(logT("[mcp] 从 %s 发现并缓存了 %d 个工具"), m.Name, len(tools))
	return nil
}

// discoverEmptyMCPsOnStartup fills the tool cache for any enabled MCP that has none
// yet (notably the seeded browser MCP on first run). Runs sequentially in one
// goroutine so we never spawn many stdio servers (npx) at once, and never blocks
// startup. Best-effort: a failure leaves the cache empty to retry next start.
func (s *Server) discoverEmptyMCPsOnStartup() {
	servers, err := s.m.pg.ListMCP()
	if err != nil {
		log.Printf(logT("[mcp] 启动时自动发现：读取列表失败：%v"), err)
		return
	}
	for _, m := range servers {
		managed, managedErr := s.m.pg.REMCPManaged(m.ID)
		if managedErr != nil || managed {
			continue
		}
		if !m.Enabled || len(m.Tools) > 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 90*time.Second)
		if err := s.discoverAndCacheMCP(ctx, m); err != nil {
			log.Printf(logT("[mcp] 启动时自动发现 %s 失败：%v"), m.Name, err)
		}
		cancel()
	}
}

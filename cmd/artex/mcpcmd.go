package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcpserver"
	"github.com/Autumn-27/artex/server"
)

// runMCPCommand 是 `artex mcp ...` 的入口。
//
// 三个子命令：
//   - serve   在 stdin/stdout 上说 MCP 协议（Codex / Claude Code / Cursor /
//     VS Code / Gemini CLI 把 ARTEX 当成一个本地工具服务器的标准方式）
//   - status  探活：现在有没有活着的 ARTEX 可以连（零进程、零端口、零数据库）
//   - help
//
// serve 自带 --attach：先探活，有活实例就附着上去（透明转发到它的 /mcp），
// 没有才打开本地数据库自己服务。这样「已经有实例在跑」时不会再多开一个。
func runMCPCommand(args []string) int {
	if len(args) == 0 {
		mcpUsage()
		return 2
	}
	switch args[0] {
	case "serve":
		return runMCPServe(args[1:])
	case "status":
		return runMCPStatus(args[1:])
	case "help", "-h", "--help":
		mcpUsage()
		return 0
	}
	fmt.Fprintf(os.Stderr, "未知子命令: mcp %s\n", args[0])
	mcpUsage()
	return 2
}

func mcpUsage() {
	fmt.Fprint(os.Stderr, `用法:
  artex mcp serve [--stdio] [--tiers L0,L1] [--file-root DIR] [--attach]
  artex mcp status [--addr HOST:PORT[,HOST:PORT]] [--token T] [--timeout 2s]

  --stdio     走 stdin/stdout 说 MCP（默认，也是目前唯一支持的传输）
  --tiers     启用的档位：L0 本地分析 / L1 知识库只读 / L2 主动探测 / L3 编排
  --file-root 限定 L0 工具可读的目录；不填=不限（仍有内置敏感路径黑名单）
  --attach    先探活，有活实例就附着（透明转发到它的 /mcp），没有才回落本地
  --addr      探活候选地址，逗号分隔；默认依次试 127.0.0.1:8799、127.0.0.1:8787
  --token     Bearer token；附着远程实例时必需，也可用 ARTEX_MCP_TOKEN

注意：--attach 只做「探活 + 转发」，不启动任何新进程、不打开数据库。
`)
}

// runMCPServe 启动 stdio 服务，或者在 --attach 时把自己降级成一个透明桥。
func runMCPServe(args []string) int {
	fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	stdio := fs.Bool("stdio", true, "走 stdin/stdout 说 MCP（默认）")
	tiersFlag := fs.String("tiers", "L0,L1", "启用的档位，逗号分隔")
	fileRoot := fs.String("file-root", os.Getenv("ARTEX_MCP_FILE_ROOT"), "L0 工具可读目录，空=不限")
	attach := fs.Bool("attach", false, "先探活，有活实例就附着")
	addrFlag := fs.String("addr", os.Getenv("ARTEX_MCP_ENDPOINT"), "探活候选地址，逗号分隔")
	tokenFlag := fs.String("token", os.Getenv("ARTEX_MCP_TOKEN"), "Bearer token")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*stdio {
		fmt.Fprintln(os.Stderr, "目前只支持 --stdio；HTTP 端点由控制台进程的 POST /mcp 提供")
		return 2
	}

	// 附着优先：能连上已有实例就不要自己再开一套（不开数据库、不建 Manager）。
	if *attach {
		info := mcpserver.ProbeFirst(context.Background(), candidateEndpoints(*addrFlag), *tokenFlag, 0)
		if info.Alive {
			fmt.Fprintf(os.Stderr, "artex mcp serve · 已附着 %s（%s %s，%d 个工具）\n",
				info.URL, emptyDash(info.Name), emptyDash(info.Version), info.Tools)
			ep := mcpserver.Endpoint{URL: info.URL, Token: *tokenFlag}
			if err := mcpserver.ProxyStdio(context.Background(), os.Stdin, os.Stdout, ep); err != nil {
				fmt.Fprintf(os.Stderr, "artex mcp serve · 附着会话结束: %v\n", err)
				return 1
			}
			return 0
		}
		fmt.Fprintf(os.Stderr, "artex mcp serve · 没有可附着的实例（%s），回落到本地 stdio\n", emptyDash(info.Error))
	}

	tiers := make([]mcpserver.Tier, 0, 4)
	for _, part := range splitComma(*tiersFlag) {
		t, ok := mcpserver.ParseTier(part)
		if !ok {
			fmt.Fprintf(os.Stderr, "未知档位: %s\n", part)
			return 2
		}
		tiers = append(tiers, t)
	}
	if len(tiers) == 0 {
		tiers = []mcpserver.Tier{mcpserver.L0, mcpserver.L1}
	}

	dsn, source, err := db.DSN()
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析数据库路径失败: %v\n", err)
		return 1
	}
	pg, err := db.Open(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败 (%s): %v\n", source, err)
		return 1
	}
	defer pg.Close()

	// stdout 只走协议帧，任何日志都去 stderr。
	fmt.Fprintf(os.Stderr, "artex mcp serve · stdio · 档位 %s · %s\n", joinTiers(tiers), source)
	srv := server.NewMCPStdioServer(pg, tiers, *fileRoot)
	if err := srv.ServeStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "stdio 会话结束: %v\n", err)
		return 1
	}
	return 0
}

// runMCPStatus 探活并打印 JSON。退出码：0=活，1=不活，2=参数错。
func runMCPStatus(args []string) int {
	fs := flag.NewFlagSet("mcp status", flag.ContinueOnError)
	addrFlag := fs.String("addr", os.Getenv("ARTEX_MCP_ENDPOINT"), "候选地址，逗号分隔")
	tokenFlag := fs.String("token", os.Getenv("ARTEX_MCP_TOKEN"), "Bearer token")
	timeout := fs.Duration("timeout", 2*time.Second, "单个地址的探活超时")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	info := mcpserver.ProbeFirst(context.Background(), candidateEndpoints(*addrFlag), *tokenFlag, *timeout)
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "序列化失败: %v\n", err)
		return 1
	}
	fmt.Println(string(b))
	if !info.Alive {
		return 1
	}
	return 0
}

// candidateEndpoints 决定默认探测顺序。
//
// 8799 在前：将来按需拉起的 headless 实例；8787 在后：控制台进程。
// 先看 headless 是为了有专门实例时优先用它，别把状态分叉成两份。
func candidateEndpoints(s string) []string {
	parts := splitComma(s)
	if len(parts) == 0 {
		return []string{"http://127.0.0.1:8799", "http://127.0.0.1:8787"}
	}
	return parts
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func splitComma(s string) []string {
	out := make([]string, 0, 4)
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		if r == ' ' || r == '\t' {
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func joinTiers(tiers []mcpserver.Tier) string {
	s := ""
	for i, t := range tiers {
		if i > 0 {
			s += ","
		}
		s += t.String()
	}
	return s
}

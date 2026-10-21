package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcpserver"
	"github.com/Autumn-27/artex/server"
)

// runMCPCommand 是 `artex mcp ...` 子命令入口。
//
// 目前只有一个动词：serve —— 在 stdin/stdout 上跑 MCP 协议，供 Codex / Claude
// Code / Cursor / VS Code / Gemini CLI 把 ARTEX 当成一个本地工具服务器拉起来。
// 它不监听端口、不读 JWT、不起 agent，只读同一份 art ex.db（SQLite WAL 下多进程
// 只读是安全的）。
func runMCPCommand(args []string) int {
	if len(args) == 0 {
		mcpUsage()
		return 2
	}
	switch args[0] {
	case "serve":
		return runMCPServe(args[1:])
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
  artex mcp serve [--stdio] [--tiers L0,L1] [--file-root DIR]

  --stdio      在 stdin/stdout 上跑 MCP（默认，也是唯一支持的传输）
  --tiers      放行档位：L0 离线分析 / L1 知识库只读 / L2 主动探测 / L3 编排
  --file-root  限定 L0 工具可读的目录；不给则放行本机任意可读路径
`)
}

func runMCPServe(args []string) int {
	fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	stdio := fs.Bool("stdio", true, "在 stdin/stdout 上跑 MCP（默认）")
	tiersFlag := fs.String("tiers", "L0,L1", "放行档位，逗号分隔")
	fileRoot := fs.String("file-root", os.Getenv("ARTEX_MCP_FILE_ROOT"), "L0 工具可读目录（空=不限）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*stdio {
		fmt.Fprintln(os.Stderr, "目前只支持 --stdio（HTTP 传输由控制台进程的 POST /mcp 提供）")
		return 2
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

	// stdout 只留协议帧：所有日志走 stderr。
	fmt.Fprintf(os.Stderr, "artex mcp serve · stdio · 档位 %s · %s\n", joinTiers(tiers), source)
	srv := server.NewMCPStdioServer(pg, tiers, *fileRoot)
	if err := srv.ServeStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "stdio 会话结束: %v\n", err)
		return 1
	}
	return 0
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

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Autumn-27/artex/mcpserver"
)

// 对外 MCP 接入：stdio 只是控制台的透明前置。
//
// 一条硬约束：**外部客户端接入时不创建任何东西**。
//
//	不打开数据库   —— 旧实现无条件调用 db.DSN()/db.Open()，在客户端进程的
//	                  当前目录（往往就是桌面）新建 artex.db + -wal + -shm。
//	不拉起后台进程 —— 旧的 --auto / ensure / headless 会在客户端旁边长出一个
//	                  detached 实例，用完自退；出问题最难查的就是它。
//	不降级         —— 控制台不在线就明确失败，不退回"本地 L0–L2 也能用"。
//	                  那正是上面两个副作用的前提。
//
// 形态因此定为：ARTEX 控制台与客户端**同时在线**，stdio 把整条会话原样转发
// 给 http://127.0.0.1:8787/mcp；任何一方不在，都给出能看懂的报错。
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
  artex mcp serve  [--addr http://127.0.0.1:8787] [--token T] [--timeout 2s]
  artex mcp status [--addr HOST:PORT] [--token T] [--timeout 2s]

  serve   在 stdin/stdout 上说 MCP，整条会话转发给已运行的 ARTEX 控制台。
          不建库、不起进程；控制台不在线直接失败。
  status  探活并打印 JSON。退出码 0=在线，1=不在线，2=参数错。

  --addr     控制台地址，默认 http://127.0.0.1:8787（也可用 ARTEX_MCP_ENDPOINT）
  --token    Bearer token，与控制台「外部接入」页显示的一致
             （也可用 ARTEX_MCP_TOKEN；不传会尝试无鉴权探活并提示）
`)
}

// serveTimeout 是启动探活的默认超时。回环端口要么秒连要么秒拒，2s 足够；
// 设长只会在 IDE 启动时白等。
const serveTimeout = 2 * time.Second

// resolveMCPToken 只认显式来源：--token 或 ARTEX_MCP_TOKEN。
//
// 刻意不回落到数据库：读库就要开库，开库就会在当前目录建库——那正是要根除的。
// 一键配置会把 token 写进客户端配置的 env 里，所以正常路径不需要手填。
func resolveMCPToken(explicit string) string {
	if t := strings.TrimSpace(explicit); t != "" {
		return t
	}
	return strings.TrimSpace(os.Getenv("ARTEX_MCP_TOKEN"))
}

// consoleEndpoint 把 --addr / 环境变量 / 默认值收敛成一个可探活的端点。
func consoleEndpoint(addr string) mcpserver.Endpoint {
	u := strings.TrimSpace(addr)
	if u == "" {
		u = strings.TrimSpace(os.Getenv("ARTEX_MCP_ENDPOINT"))
	}
	if u == "" {
		u = "http://127.0.0.1:8787"
	}
	return mcpserver.Endpoint{URL: u}
}

// runMCPServe 把 stdio 会话转发给控制台。
func runMCPServe(args []string) int {
	fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	stdio := fs.Bool("stdio", true, "走 stdin/stdout 说 MCP（唯一模式）")
	addrFlag := fs.String("addr", "", "ARTEX 控制台地址（默认 http://127.0.0.1:8787）")
	tokenFlag := fs.String("token", "", "Bearer token（默认读 ARTEX_MCP_TOKEN）")
	timeout := fs.Duration("timeout", serveTimeout, "启动探活超时")

	// 兼容旧配置：下面这些参数曾属于「附着 / 按需拉起 headless」那一套。
	// 机制已经删除，但已经写进客户端配置的命令行不会自己改，直接报
	// "flag provided but not defined" 会让 IDE 一侧整个起不来。
	// 所以照常解析、明确忽略，并在 stderr 留一行提示。
	var (
		legacyAuto, legacyAttach, legacyNoSpawn, legacyWatch bool
		legacyTiers, legacyRoot                              string
		legacyIdle                                           time.Duration
	)
	fs.BoolVar(&legacyAuto, "auto", false, "已废弃：忽略（现在总是转发给控制台）")
	fs.BoolVar(&legacyAttach, "attach", false, "已废弃：忽略")
	fs.BoolVar(&legacyNoSpawn, "no-spawn", false, "已废弃：忽略（本来就不再拉起进程）")
	fs.StringVar(&legacyTiers, "tiers", "", "已废弃：忽略（档位由控制台决定）")
	fs.StringVar(&legacyRoot, "file-root", "", "已废弃：忽略")
	fs.DurationVar(&legacyIdle, "idle", 0, "已废弃：忽略")
	fs.BoolVar(&legacyWatch, "watch-stdin", false, "已废弃：忽略")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if legacyAuto || legacyAttach || legacyNoSpawn || legacyWatch ||
		legacyTiers != "" || legacyRoot != "" || legacyIdle != 0 {
		fmt.Fprintln(os.Stderr, "artex mcp serve · 旧参数已忽略：现在只做 stdio→控制台 转发，不建库、不拉起后台进程。")
	}
	if !*stdio {
		fmt.Fprintln(os.Stderr, "artex mcp serve · 只支持 stdio；HTTP 直连请把客户端指向控制台的 /mcp")
		return 2
	}

	ep := consoleEndpoint(*addrFlag).Normalized()
	ep.Token = resolveMCPToken(*tokenFlag)

	ctx := context.Background()
	info := mcpserver.Probe(ctx, ep, *timeout)
	if !info.Alive {
		reason := strings.TrimSpace(info.Error)
		if reason == "" {
			reason = "未知原因"
		}
		fmt.Fprintf(os.Stderr, "artex mcp serve · 连不上 ARTEX 控制台：%s（%s）\n", ep.URL, reason)
		fmt.Fprintln(os.Stderr, "artex mcp serve · 外部客户端只做 stdio→控制台 的透明转发，不建库、不拉起后台进程。")
		fmt.Fprintln(os.Stderr, "artex mcp serve · 请先启动 ARTEX 控制台，确认两者同时在线后重试。")
		if ep.Token == "" {
			fmt.Fprintln(os.Stderr, "artex mcp serve · 未提供 token：请用 --token 或 ARTEX_MCP_TOKEN 传入控制台里显示的 token。")
		}
		return 1
	}

	fmt.Fprintf(os.Stderr, "artex mcp serve · 已连接 %s（%s %s，%d 个工具）\n",
		ep.URL, dash(info.Name), dash(info.Version), info.Tools)

	if err := mcpserver.ProxyStdio(ctx, os.Stdin, os.Stdout, ep); err != nil {
		fmt.Fprintf(os.Stderr, "artex mcp serve · 会话结束: %v\n", err)
		return 1
	}
	return 0
}

// runMCPStatus 探活并打印 JSON。退出码：0=活，1=不活，2=参数错。
func runMCPStatus(args []string) int {
	fs := flag.NewFlagSet("mcp status", flag.ContinueOnError)
	addrFlag := fs.String("addr", "", "候选地址（默认 http://127.0.0.1:8787）")
	tokenFlag := fs.String("token", "", "Bearer token（默认读 ARTEX_MCP_TOKEN）")
	timeout := fs.Duration("timeout", serveTimeout, "探活超时")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ep := consoleEndpoint(*addrFlag).Normalized()
	ep.Token = resolveMCPToken(*tokenFlag)

	info := mcpserver.Probe(context.Background(), ep, *timeout)
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

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

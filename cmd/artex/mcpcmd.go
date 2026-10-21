package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcpserver"
	"github.com/Autumn-27/artex/server"
)

// runMCPCommand 是 `artex mcp ...` 的入口。
//
// 四个子命令：
//   - serve     在 stdin/stdout 上说 MCP（本地工具服务器的标准方式）
//   - status    探活：现在有没有活着的 ARTEX 可以连（零进程、零端口、零数据库）
//   - ensure    没有活实例就 detached 拉起一个 headless（零常驻的「起」这一半）
//   - help
//
// 注意所有 mcp 子命令都在 flag.Parse 之前分流，不走控制台的启动路径，
// 因此绝不会触发 ensurePortFree 去结束同目录的 artex*.exe。
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
	case "ensure":
		return runMCPEnsureCommand(args[1:])
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
  artex mcp serve  [--stdio] [--tiers L0,L1] [--file-root DIR] [--attach|--auto] [--no-spawn]
  artex mcp serve  --headless [--addr 127.0.0.1:8799] [--idle 10m] [--watch-stdin]
  artex mcp status [--addr HOST:PORT[,HOST:PORT]] [--token T] [--timeout 2s]
  artex mcp ensure [--addr 127.0.0.1:8799] [--tiers L0,L1] [--idle 10m] [--no-spawn]

  --stdio       走 stdin/stdout 说 MCP（默认）
  --attach      全量透明转发：有活实例就把整条会话转过去，没有才回落本地
  --auto        保留 L3：本级仍跑 L0–L2，L3 转发给控制台（需要控制台在运行）
  --no-spawn    不再拉起任何进程（保留给 EDR 环境；--auto 本来就不拉进程）
  --headless    只开 /mcp 的 HTTP 服务，无 UI、不占 8787、不接管任何端口
  --idle        headless 空闲多久自退（默认 10m）
  --watch-stdin 父进程管道关闭时立即退出（宿主崩溃不留孤儿）

零常驻姿态：空闲时 0 个 ARTEX 进程；有活实例就附着，没有就按需拉起，用完自退。
`)
}

// runMCPServe 启动 stdio 服务、headless HTTP 服务，或者在 --attach/--auto 时当透明桥。
func runMCPServe(args []string) int {
	fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	stdio := fs.Bool("stdio", true, "走 stdin/stdout 说 MCP（默认）")
	headless := fs.Bool("headless", false, "只开 /mcp 的 HTTP 服务，无 UI")
	tiersFlag := fs.String("tiers", "L0,L1", "启用的档位，逗号分隔")
	fileRoot := fs.String("file-root", os.Getenv("ARTEX_MCP_FILE_ROOT"), "L0 工具可读目录，空=不限")
	attach := fs.Bool("attach", false, "先探活，有活实例就附着")
	auto := fs.Bool("auto", false, "attach 不上就拉起 headless 再附着")
	noSpawn := fs.Bool("no-spawn", false, "任何情况下都不拉起新进程")
	addrFlag := fs.String("addr", os.Getenv("ARTEX_MCP_ENDPOINT"), "探活/监听地址")
	tokenFlag := fs.String("token", os.Getenv("ARTEX_MCP_TOKEN"), "Bearer token")
	idle := fs.Duration("idle", defaultIdleExit, "headless 空闲退出时长")
	watchStdin := fs.Bool("watch-stdin", false, "父进程管道关闭即退出")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// 子命令模式：只开 /mcp，绝不碰控制台的 8787/8788。
	if *headless {
		return runMCPHeadless(headlessOptions{
			Addr:       firstNonEmpty(*addrFlag, defaultHeadlessAddr),
			Tiers:      *tiersFlag,
			Idle:       *idle,
			WatchStdin: *watchStdin,
		})
	}

	if !*stdio {
		fmt.Fprintln(os.Stderr, "stdio 模式只支持 --stdio；要用 HTTP 请加 --headless")
		return 2
	}

	// 附着优先：能连上已有实例就不要自己再开一套（不开数据库、不建 Manager）。
	//
	// 只有 --attach 才做「整条会话转发」。--auto 不再走这条路：一旦整条会话都
	// 转给控制台，控制台一重启，IDE 里的工具面就整体断了；而 L0–L2 本来就不需要
	// 引擎，留在本进程里更稳。
	if *attach {
		if code, ok := attachExisting(*addrFlag, resolveMCPToken(*tokenFlag), *tiersFlag, false, *noSpawn, *idle); ok {
			return code
		}
	}

	explicitTiers := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "tiers" {
			explicitTiers = true
		}
	})
	tiers := parseTiersOr(*tiersFlag, []mcpserver.Tier{mcpserver.L0, mcpserver.L1})

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

	// --auto 且没显式给档位时，跟随控制台里保存的档位：谁能被外部 AI 拿到，
	// 由界面上的开关决定，不由某台机器上的一次命令行参数决定。
	if *auto && !explicitTiers {
		tiers = server.MCPTiersFromSettings(pg)
	}

	// stdout 只走协议帧，任何日志都去 stderr。
	fmt.Fprintf(os.Stderr, "artex mcp serve · stdio · 档位 %s · %s\n", joinTiers(tiers), source)

	var srv *mcpserver.Server
	if *auto {
		srv = server.NewMCPStdioBridgeServer(pg, tiers, *fileRoot,
			consoleL3Bridge(resolveMCPToken(*tokenFlag), *addrFlag))
		if hasTier(tiers, mcpserver.L3) {
			fmt.Fprintln(os.Stderr, "artex mcp serve · L3 转发给控制台（控制台需在运行；headless 没有引擎）")
		}
	} else {
		srv = server.NewMCPStdioServer(pg, tiers, *fileRoot)
	}
	if err := srv.ServeStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "stdio 会话结束: %v\n", err)
		return 1
	}
	return 0
}

// attachExisting 负责「探活 → 附着 / 按需拉起 → 再附着」。
//
// 返回 (退出码, true) 表示已经处理完，调用方直接返回；
// (_, false) 表示没附着上，回落到本地 stdio。
func attachExisting(addr, token, tiers string, spawn, noSpawn bool, idle time.Duration) (int, bool) {
	info := mcpserver.ProbeFirst(context.Background(), candidateEndpoints(addr), token, 0)
	if !info.Alive && spawn && !noSpawn {
		// 明确给了单个地址就拉在那儿；否则统一用 8799，避免和控制台的 8787 撞车。
		target := defaultHeadlessAddr
		if a := strings.TrimSpace(addr); a != "" && !strings.Contains(a, ",") {
			target = strings.TrimPrefix(strings.TrimPrefix(a, "http://"), "https://")
			target = strings.TrimSuffix(target, "/mcp")
		}
		if err := spawnHeadless(target, tiers, idle, true); err == nil {
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) && !info.Alive {
				info = mcpserver.Probe(context.Background(),
					mcpserver.Endpoint{URL: "http://" + target, Token: token}, 800*time.Millisecond)
				if !info.Alive {
					time.Sleep(250 * time.Millisecond)
				}
			}
		} else {
			fmt.Fprintf(os.Stderr, "artex mcp serve · 拉起失败: %v\n", err)
		}
	}
	if !info.Alive {
		reason := firstNonEmpty(info.Error, "未知原因")
		if noSpawn && spawn {
			reason = "已指定 --no-spawn"
		}
		fmt.Fprintf(os.Stderr, "artex mcp serve · 没有可附着的实例（%s），回落到本地 stdio\n", reason)
		return 0, false
	}

	fmt.Fprintf(os.Stderr, "artex mcp serve · 已附着 %s（%s %s，%d 个工具）\n",
		info.URL, emptyDash(info.Name), emptyDash(info.Version), info.Tools)
	ep := mcpserver.Endpoint{URL: info.URL, Token: token}
	if err := mcpserver.ProxyStdio(context.Background(), os.Stdin, os.Stdout, ep); err != nil {
		fmt.Fprintf(os.Stderr, "artex mcp serve · 附着会话结束: %v\n", err)
		return 1, true
	}
	return 0, true
}

// runMCPEnsureCommand 是 `artex mcp ensure` 的入口。
func runMCPEnsureCommand(args []string) int {
	fs := flag.NewFlagSet("mcp ensure", flag.ContinueOnError)
	addrFlag := fs.String("addr", firstNonEmpty(os.Getenv("ARTEX_MCP_ENDPOINT"), defaultHeadlessAddr), "监听/探活地址")
	tiersFlag := fs.String("tiers", "L0,L1", "拉起的实例启用哪些档位")
	idle := fs.Duration("idle", defaultIdleExit, "空闲退出时长")
	noSpawn := fs.Bool("no-spawn", false, "只探活，不拉起")
	watchStdin := fs.Bool("watch-stdin", false, "父进程管道关闭即退出")
	tokenFlag := fs.String("token", os.Getenv("ARTEX_MCP_TOKEN"), "Bearer token")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return runMCPEnsure(ensureOptions{
		Addr:       *addrFlag,
		Tiers:      *tiersFlag,
		Idle:       *idle,
		WatchStdin: *watchStdin,
		NoSpawn:    *noSpawn,
		Token:      *tokenFlag,
	})
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
	// token 必须和实例用的是同一个：只认 --token / 环境变量的话，用户在界面上
	// 生成过 token 再来 status，会被自己拒成 401。这里回落到数据库。
	info := mcpserver.ProbeFirst(context.Background(), candidateEndpoints(*addrFlag), resolveMCPToken(*tokenFlag), *timeout)
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
// 8799 在前：按需拉起的 headless；8787 在后：控制台进程。
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

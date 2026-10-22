// Command artex runs the ARTEX-REA backend: the dual SQLite graph stores,
// the event-driven exploration engine, and the JSON HTTP API consumed by the
// shadcn/ui frontend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/selfupdate"
	"github.com/Autumn-27/artex/server"
)

// version is the build version, injected at release time via
// -ldflags "-X main.version=<tag>". Defaults to "dev" for local builds.
var version = "dev"

// restartGraceTimeout 是"更新/回滚重启"时留给在跑回合的收尾上限。
// 普通 Ctrl-C 仍然只等 5s：用户想立刻停下，不该被长任务拖住。
const restartGraceTimeout = 20 * time.Second

// 单实例锁的等待上限。正常退出与更新握手都在毫秒级；只有"想抢占一个正在服务的
// 实例"才会等到超时 —— 而那时的默认行为是报告并退出，不抢占（见 -takeover）。
const (
	instanceLockWait = 3 * time.Second
	// relaunchLockWait 给"自拉起来的新进程"用：旧进程可能还在 defer 里关库、
	// 释放锁，等它让出要比普通双击宽容得多。
	relaunchLockWait = 20 * time.Second
	takeoverLockWait = 30 * time.Second
)

const banner = `
    _    ____ _____ _______  __      ____  _____    _    
   / \  |  _ \_   _| ____\ \/ /     |  _ \| ____|  / \   
  / _ \ | |_) || | |  _|  \  / _____| |_) |  _|   / _ \  
 / ___ \|  _ < | | | |___ /  \_____|  _ <| |___ / ___ \ 
/_/   \_\_| \_\|_| |_____/_/\_\    |_| \_\_____/_/   \_\
`

// printBanner writes the startup banner + version/runtime info to stdout.
func printBanner(addr string) {
	fmt.Print(banner)
	fmt.Println("  ARTEX-REA  ·  AI 自主渗透测试系统与逆向工程指挥中枢")
	fmt.Printf("  版本 %s  ·  %s/%s  ·  %s  ·  监听地址 %s\n\n",
		version, runtime.GOOS, runtime.GOARCH, runtime.Version(), addr)
}

// main only maps run's result onto the process exit code. The exit code is part
// of the update protocol — the supervising start script reads it to decide
// whether to relaunch us (see selfupdate.ExitRestart) — so the body has to live
// in a function that can *return* rather than os.Exit past its own defers.
func main() {
	os.Exit(run())
}

func run() int {
	// 先把控制台切到 UTF-8（仅 Windows；可用 ARTEX_UTF8_CONSOLE=0 关闭），
	// 横幅、日志与子进程输出才不会在 GBK 控制台上乱码（批次 7 / #4–5）。
	enableUTF8Console()

	// 子命令分流必须在 flag.Parse 之前：`artex mcp serve` 的参数属于子命令，
	// 交给全局 flag 会直接报 "flag provided but not defined"。
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		return runMCPCommand(os.Args[2:])
	}

	var (
		addr      = flag.String("addr", "127.0.0.1:8787", "HTTP listen address (default accepts local connections only; use :8787 to serve other hosts)")
		dataDir   = flag.String("data", filepath.Join(config.BaseDir(), "data"), "data directory for SQLite stores (default: data/ next to the executable)")
		proxy     = flag.String("proxy", "127.0.0.1:8788", "traffic recording proxy address (empty to disable)")
		resetPass = flag.Bool("reset-password", false, "重置 ARTEX-REA 管理员密码后退出；新密码取自 ARTEX_RESET_NEWPASS，未设置则交互输入")
		takeover  = flag.Bool("takeover", false, "端口被另一个 ARTEX 实例占用时夺取它（默认不抢占，直接退出）")
		selfTest  = flag.Bool("self-test", false, "仅做启动自检并退出（升级流程的冒烟测试入口，不占端口/不开库/不出网）")
	)
	flag.Parse()

	// 冒烟测试专用出口：什么都不做、什么也不碰（不占端口、不开库、不出网），
	// 立刻退出。升级流程用它在真实系统上确认新二进制能执行。
	if *selfTest {
		fmt.Println("self-test ok")
		return 0
	}

	// 启动守卫：拒绝从升级临时文件（.new/.old/.failed/.staged/.part…）启动。
	// 否则一个被误启动的暂存件会按 os.Executable() 推出属于自己的一套台账与暂存路径，
	// 在同一台机器上形成"第二个安装"，还会和正式进程抢同一批端口。
	if err := selfupdate.RefuseUnsafeExecutable(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}

	// 版本化布局的入口转发：pointer.json 指向的核心程序不是自己时，交给它执行。
	// 升级因此不再替换任何正在运行的文件，只新增 versions/<ver>/ 并原子改指针。
	if code, handed := selfupdate.Handoff(); handed {
		return code
	}

	// exeDir 是发行包根目录：凭据主密钥、jwt.key 与用户数据都相对它定位。
	// 单独求值一次，供自举逻辑复用，避免与 config.BaseDir() 的解析时机耦合。
	exeDir := config.BaseDir()

	// 密码重置是一次性离线操作：不占端口、不跑自更新、不起后台 worker。
	// 账户被抢占后运维需要它独立于正常启动路径可用，所以排在所有启动步骤之前。
	if *resetPass {
		return runResetPassword()
	}

	// hand the build version to the server package so GET /api/health can report it
	// to the frontend top bar.
	server.BuildVersion = version

	printBanner(*addr)

	// capture backend logs into the in-memory sink (still to stderr) so the /logs
	// page can show a live log stream. Do this first, to catch startup logs too.
	server.StartLogCapture()

	// 进程树收口：把自己放进 kill-on-close Job Object。之后派生的子进程（Bash 工具的
	// powershell/python 及其多进程子进程、rg、npx…）全部与 ARTEX 同生共死——被强杀、
	// 崩溃或被新实例自举顶掉时，不再留下跑满 CPU 的孤儿脚本（详见 jobobject_windows.go）。
	// 失败只降级：退回「子进程可能残留」的旧行为，日志里会说明原因。
	_ = guardProcessTree()

	// 单实例仲裁：同一安装目录只允许一个"真正对外服务"的进程。
	//
	// 顺序是刻意的 —— 先拿锁，再谈端口：锁是唯一仲裁者，端口探测只是二次保险。
	// 顺序固定就不存在"两个进程都绑成功"的竞态，也不需要"谁占了端口就杀谁"。
	bootDone := false
	if rootDir, rootErr := selfupdate.InstallRoot(); rootErr != nil {
		log.Printf("[boot] 安装目录未知（%v），跳过单实例锁", rootErr)
	} else {
		lockWait := instanceLockWait
		if selfupdate.RelaunchedChild() {
			lockWait = relaunchLockWait
		}
		release, state, lockErr := selfupdate.AcquireInstance(rootDir, lockWait)
		switch {
		case lockErr != nil:
			// 锁机制本身不可用（目录只读、权限不足等）：退回旧行为，靠端口自举兜底。
			log.Printf("[boot] 单实例锁不可用（%v），退回端口自举", lockErr)
		case state == selfupdate.LockBusy && !*takeover:
			// 已经有一个 ARTEX 在服务：不再抢占、不再杀进程，直接退出。
			reportRunningElsewhere(*addr)
			return 0
		case state == selfupdate.LockBusy && *takeover:
			log.Printf("[boot] -takeover：先接管被占用的端口，再接手单实例锁")
			if err := bootBeforeStart(*addr, *proxy, exeDir, true); err != nil {
				log.Fatalf("接管失败：%v", err)
			}
			bootDone = true
			release, state, lockErr = selfupdate.AcquireInstance(rootDir, takeoverLockWait)
			if lockErr != nil || state == selfupdate.LockBusy {
				log.Fatalf("接管失败：旧实例仍持有单实例锁（%v）", lockErr)
			}
			defer release()
		default:
			defer release()
			if state == selfupdate.LockAbandoned {
				log.Printf("[boot] 上一个实例未正常退出（锁已被内核回收），本次接管")
			}
		}
	}

	// Self-update bootstrap: swap in a staged binary, or count a post-swap boot
	// attempt and roll back if the new build keeps dying. Must run before we open
	// the stores or bind a port — this may end with "exit and let the start script
	// relaunch me", and there is no point paying for either first.
	action, upState := selfupdate.Bootstrap(version)
	server.SetBootUpdateState(upState)
	if action == selfupdate.Restart {
		// 写重启意图 + 按托管关系决定：返 75 交给入口/脚本，还是自己拉起自己。
		return selfupdate.ExitForRestart("bootstrap")
	}

	// surface which config file the binary reads (absolute, so `go run`'s relative
	// "config.json" — resolved against the CWD — is unambiguous).
	cfgPath := config.Path()
	if abs, e := filepath.Abs(cfgPath); e == nil {
		cfgPath = abs
	}
	if _, e := os.Stat(cfgPath); e == nil {
		log.Printf(config.T("[config] 配置文件: %s"), cfgPath)
	} else {
		log.Printf(config.T("[config] 配置文件: %s (文件不存在 · 使用内置默认数据库路径)"), cfgPath)
	}

	// 自举：先解决端口占用，再打开任何存储。
	// 位置在 selfupdate 之后——自举会真正开始接管端口，
	// 不应与更新的换装流程交错。数据库是进程内打开的文件，无需自举。
	if !bootDone {
		if err := bootBeforeStart(*addr, *proxy, exeDir, *takeover); err != nil {
			log.Fatalf("自举启动失败：%v", err)
		}
	}

	// 凭据主密钥必须先于任何存储读写装好：llm_profiles.api_key 等加密字段
	// 没有它读不出来。密钥文件与 jwt.key 同放 exeDir，不进可浏览的 dataDir，
	// 也绝不与它保护的数据库文件同目录。
	if err := server.InstallCredentialKey(exeDir); err != nil {
		log.Fatalf("凭据主密钥初始化失败：%v", err)
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, shutdown := shutdownContext(sigCtx)
	defer shutdown(agent.AbortShutdown)

	mgr, err := server.NewManager(*dataDir, *proxy)
	if err != nil {
		log.Fatalf("open stores: %v", err)
	}
	defer mgr.Close()

	// workers 是「同时有几个 agent 在跑」的上限。超过物理/逻辑核数时，多个 worker
	// 同时执行 CPU 密集型命令（穷举、爆破、高并发 fuzz）会把 CPU 打满，平台服务与
	// SQLite 写入都会跟着变慢——只提示，不擅自改用户设置。
	if w := mgr.Workers(); w > runtime.NumCPU() {
		log.Printf("[config] workers=%d 超过本机逻辑 CPU 数 %d：并行 worker 同时跑 CPU 密集型脚本会打满 CPU，"+
			"平台与数据库会一起变慢；建议设为不超过 %d", w, runtime.NumCPU(), runtime.NumCPU())
	}

	// Surviving this long means a freshly swapped-in build actually works, so drop
	// the upgrade marker and stop counting attempts. Until it fires, every boot
	// increments the count and a build that keeps dying gets rolled back.
	// 新版本"活下来"的判定不只看进程还活着：还要核对运行版本号与 /api/health，
	// 不通过就把台账置为 failed，由下一次启动的 Bootstrap 累计失败次数并回滚。
	settle := time.AfterFunc(selfupdate.SettleDelay, func() {
		if err := selfupdate.Settle(version, *addr); err != nil {
			log.Printf("[update] %v", err)
		}
	})
	defer settle.Stop()

	skillDir := config.SkillDir()
	if abs, err := filepath.Abs(skillDir); err == nil {
		skillDir = abs
	}
	log.Printf(config.T("[config] skill 目录: %s"), skillDir)
	srv := server.New(ctx, mgr, skillDir, *dataDir, config.BaseDir())
	srv.WarnIfExposed(*addr)
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("ARTEX-REA %s backend listening on %s (data=%s, workers=%d)", version, *addr, *dataDir, mgr.Workers())
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	// Two ways out: a signal (normal stop → exit 0, the start script stops looping)
	// or a staged update / rollback (→ exit 75, the script relaunches us and the
	// bootstrap above installs the new build).
	code := 0
	restarting := false
	select {
	case <-ctx.Done():
	case <-server.RestartRequested():
		restarting = true
		shutdown(agent.AbortShutdown)
	}

	// 停机顺序是三条不变量，别调换：
	//   1. 先停止接受新请求（不再有新的 agent 回合）；
	//   2. 等在跑的回合收尾，超时强断；
	//   3. 退出时 defer 里的 mgr.Close() 关库，之后才轮到自重启拉起来的新进程开库。
	log.Println("shutting down...")
	grace := 5 * time.Second
	if restarting {
		// 更新/回滚重启给在跑的回合留更长的收尾时间：SSE 与工具调用被硬切会留下半截记录。
		grace = restartGraceTimeout
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	_ = httpSrv.Shutdown(shutdownCtx)
	cancel()
	if restarting {
		return selfupdate.ExitForRestart("apply")
	}
	return code
}

// reportRunningElsewhere 处理"已经有一个 ARTEX 在跑"：只报告地址，不抢占、不杀进程。
// 这是双击第二次、或更新后旧实例还在收尾时的正常路径，不是错误。
func reportRunningElsewhere(addr string) {
	fmt.Printf("\n  ARTEX-REA 已在运行：http://%s\n", displayAddr(addr))
	fmt.Println("  本次启动直接退出，没有动那个实例；要停止它请回到它的控制台按 Ctrl-C。")
}

// displayAddr 把监听地址转成浏览器能点的形式（0.0.0.0 / :: 换成 127.0.0.1）。
func displayAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	switch {
	case addr == "":
		return "127.0.0.1:8787"
	case strings.HasPrefix(addr, ":"):
		return "127.0.0.1" + addr
	case strings.HasPrefix(addr, "0.0.0.0:"):
		return "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	case strings.HasPrefix(addr, "[::]:"):
		return "127.0.0.1:" + strings.TrimPrefix(addr, "[::]:")
	default:
		return addr
	}
}

// shutdownContext deliberately does not derive from signalCtx. If it did, the
// parent's plain context.Canceled could win the race before AbortShutdown was
// attached to the child, losing the diagnostic cause in every running Agent.
func shutdownContext(signalCtx context.Context) (context.Context, context.CancelCauseFunc) {
	ctx, shutdown := context.WithCancelCause(context.Background())
	go func() {
		select {
		case <-signalCtx.Done():
			shutdown(agent.AbortShutdown)
		case <-ctx.Done():
		}
	}()
	return ctx, shutdown
}

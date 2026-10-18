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

	var (
		addr      = flag.String("addr", "127.0.0.1:8787", "HTTP listen address (default accepts local connections only; use :8787 to serve other hosts)")
		dataDir   = flag.String("data", filepath.Join(config.BaseDir(), "data"), "data directory for SQLite stores (default: data/ next to the executable)")
		proxy     = flag.String("proxy", "127.0.0.1:8788", "traffic recording proxy address (empty to disable)")
		resetPass = flag.Bool("reset-password", false, "重置 ARTEX-REA 管理员密码后退出；新密码取自 ARTEX_RESET_NEWPASS，未设置则交互输入")
	)
	flag.Parse()

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

	// Self-update bootstrap: swap in a staged binary, or count a post-swap boot
	// attempt and roll back if the new build keeps dying. Must run before we open
	// the stores or bind a port — this may end with "exit and let the start script
	// relaunch me", and there is no point paying for either first.
	action, upState := selfupdate.Bootstrap()
	server.SetBootUpdateState(upState)
	if action == selfupdate.Restart {
		return selfupdate.ExitRestart
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
	if err := bootBeforeStart(*addr, *proxy, exeDir); err != nil {
		log.Fatalf("自举启动失败：%v", err)
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
	settle := time.AfterFunc(selfupdate.SettleDelay, selfupdate.Settle)
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
	select {
	case <-ctx.Done():
	case <-server.RestartRequested():
		code = selfupdate.ExitRestart
		shutdown(agent.AbortShutdown)
	}

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	return code
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

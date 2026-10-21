package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcpserver"
	"github.com/Autumn-27/artex/server"
)

// 零常驻（S3）：需要时起、用完就退。
//
// 三条不变量，任何一条破了都会退回到「常驻 + 孤儿进程」的老路：
//  1. 绝不走控制台的启动路径。8787/8788 的 ensurePortFree 会结束同目录的
//     artex*.exe——那会把用户正在用的控制台杀掉。所以 headless 只监听 8799，
//     并且完全不碰 bootBeforeStart。
//  2. 三个退出条件全都要：空闲超时、stdin EOF（父进程死了）、显式 ctx 取消。
//     只做空闲超时的话，宿主崩了进程还会活满一个窗口。
//  3. 拉起必须能脱离宿主的 Job Object。Windows 下 DETACHED_PROCESS 不够，
//     只有 CREATE_BREAKAWAY_FROM_JOB 有效；父 job 没给 BREAKAWAY_OK 时
//     会 ERROR_ACCESS_DENIED，此时降级（不脱离）而不是静默失败。

const (
	defaultHeadlessAddr = "127.0.0.1:8799"
	defaultIdleExit     = 10 * time.Minute
	instanceFileName    = "mcpd.json"
	instanceLockName    = "mcpd.lock"
)

// instanceRecord 是无 UI 实例的自述文件。刻意不写 token。
type instanceRecord struct {
	Addr      string `json:"addr"`
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	StartedAt string `json:"started_at"`
	Tiers     string `json:"tiers"`
}

func instancePath() string { return filepath.Join(config.BaseDir(), instanceFileName) }
func lockPath() string     { return filepath.Join(config.BaseDir(), instanceLockName) }

// acquireInstanceLock 用 O_CREATE|O_EXCL 抢锁。
//
// 不用内核文件锁：跨平台行为差异大，而这里的竞争窗口只有「同时起两个」这一种，
// 排它创建足够，而且锁文件里带 pid，陈旧锁可以识别并清掉。
func acquireInstanceLock() (func(), error) {
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(lockPath()) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		// 已有的锁：看看持有者是不是还活着。活着就放弃，死了就清掉重抢。
		if pid := readLockPID(); pid > 0 && mcpProcessAlive(pid) {
			return nil, fmt.Errorf("已有实例持有锁（pid %d）", pid)
		}
		_ = os.Remove(lockPath())
	}
	return nil, errors.New("抢锁失败")
}

func readLockPID() int {
	raw, err := os.ReadFile(lockPath())
	if err != nil {
		return 0
	}
	var pid int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid)
	return pid
}

// ---- headless serve ----

type headlessOptions struct {
	Addr       string
	Tiers      string
	Idle       time.Duration
	WatchStdin bool
}

// runMCPHeadless 起一个只有 /mcp 的 HTTP 服务：没有 UI、没有 8787、没有开机自启。
func runMCPHeadless(opts headlessOptions) int {
	addr := strings.TrimSpace(opts.Addr)
	if addr == "" {
		addr = defaultHeadlessAddr
	}
	tiers := parseTiersOr(opts.Tiers, nil)
	if len(tiers) == 0 {
		tiers = []mcpserver.Tier{mcpserver.L0, mcpserver.L1}
	}
	idle := opts.Idle
	if idle <= 0 {
		idle = defaultIdleExit
	}

	unlock, err := acquireInstanceLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "artex mcp headless · %v；先探一下是不是已经有一个在跑\n", err)
		return 0
	}
	defer unlock()

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

	token, err := server.MCPTokenForCLI(pg)
	if err != nil || token == "" {
		fmt.Fprintf(os.Stderr, "取 MCP token 失败: %v\n", err)
		return 1
	}

	mcpSrv := server.NewMCPHeadlessServer(pg, tiers, os.Getenv("ARTEX_MCP_FILE_ROOT"))
	handler := mcpSrv.HTTPHandler(mcpserver.HTTPConfig{
		Authorize:   bearerOnly(token),
		AllowOrigin: loopbackOrigin,
		MaxBody:     8 << 20,
	})

	// 每来一次请求就刷新活跃时间。空闲退出靠它判断，不靠「有没有任务在跑」
	// ——任务在跑而没人问，恰恰是最该退的场景。
	var lastSeen atomic.Int64
	lastSeen.Store(time.Now().UnixNano())

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		lastSeen.Store(time.Now().UnixNano())
		handler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "监听 %s 失败（可能已有实例）: %v\n", addr, err)
		return 1
	}
	httpSrv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	rec := instanceRecord{
		Addr:      addr,
		PID:       os.Getpid(),
		Version:   server.BuildVersion,
		StartedAt: time.Now().Format(time.RFC3339),
		Tiers:     joinTiers(tiers),
	}
	if raw, err := json.MarshalIndent(rec, "", "  "); err == nil {
		_ = os.WriteFile(instancePath(), raw, 0o600)
	}
	defer func() { _ = os.Remove(instancePath()) }()

	fmt.Fprintf(os.Stderr, "artex mcp headless · %s · 档位 %s · 空闲 %s 退出 · pid %d\n",
		addr, joinTiers(tiers), idle, os.Getpid())

	done := make(chan struct{}, 3)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "headless 服务异常: %v\n", err)
			done <- struct{}{}
		}
	}()

	// 退出条件 1：空闲超时
	go func() {
		step := idle / 4
		if step < 5*time.Second {
			step = 5 * time.Second
		}
		t := time.NewTicker(step)
		defer t.Stop()
		for range t.C {
			if time.Since(time.Unix(0, lastSeen.Load())) >= idle {
				fmt.Fprintf(os.Stderr, "artex mcp headless · 空闲 %s，退出\n", idle)
				done <- struct{}{}
				return
			}
		}
	}()

	// 退出条件 2：父进程消失（stdin 管道 EOF）
	if opts.WatchStdin {
		go func() {
			buf := make([]byte, 256)
			for {
				if _, err := os.Stdin.Read(buf); err != nil {
					fmt.Fprintf(os.Stderr, "artex mcp headless · 父进程管道关闭，退出\n")
					done <- struct{}{}
					return
				}
			}
		}()
	}

	<-done
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	return 0
}

// ---- ensure：没有就拉起来 ----

type ensureOptions struct {
	Addr       string
	Tiers      string
	Idle       time.Duration
	WatchStdin bool
	NoSpawn    bool
	Token      string
}

// runMCPEnsure 探活；不活就 detached 拉起一个 headless，再等它就绪。
func runMCPEnsure(opts ensureOptions) int {
	addr := strings.TrimSpace(opts.Addr)
	if addr == "" {
		addr = defaultHeadlessAddr
	}
	token := resolveMCPToken(opts.Token)

	ep := mcpserver.Endpoint{URL: "http://" + addr, Token: token}
	if info := mcpserver.Probe(context.Background(), ep, 1500*time.Millisecond); info.Alive {
		printJSON(info)
		return 0
	}
	if opts.NoSpawn {
		fmt.Fprintln(os.Stderr, "artex mcp ensure · 没有活实例，且指定了 --no-spawn")
		return 3
	}

	if err := spawnHeadless(addr, opts.Tiers, opts.Idle, opts.WatchStdin); err != nil {
		fmt.Fprintf(os.Stderr, "artex mcp ensure · 拉起失败: %v\n", err)
		return 1
	}
	// 冷启动实测 0.2 s；给 8 s 是留给「机器刚醒、磁盘冷」的余量。
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if info := mcpserver.Probe(context.Background(), ep, 800*time.Millisecond); info.Alive {
			printJSON(info)
			return 0
		}
		time.Sleep(250 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "artex mcp ensure · 实例没起来（8 s 内没探活成功）")
	return 1
}

// spawnHeadless 以脱离宿主的姿态拉起 `artex mcp serve --headless`。
func spawnHeadless(addr, tiers string, idle time.Duration, watchStdin bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"mcp", "serve", "--headless", "--addr", addr}
	if strings.TrimSpace(tiers) != "" {
		args = append(args, "--tiers", tiers)
	}
	if idle > 0 {
		args = append(args, "--idle", idle.String())
	}
	if watchStdin {
		args = append(args, "--watch-stdin")
	}

	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = detachAttr(true)
	var stdinPipe *os.File
	if watchStdin {
		pr, pw, err := os.Pipe()
		if err != nil {
			return err
		}
		cmd.Stdin = pr
		stdinPipe = pw
		defer func() { _ = pr.Close() }()
	}
	if err := cmd.Start(); err != nil {
		// 父 job 没给 BREAKAWAY_OK 时会 ERROR_ACCESS_DENIED：降级为不脱离。
		if isAccessDenied(err) {
			cmd = exec.Command(exe, args...)
			cmd.SysProcAttr = detachAttr(false)
			if watchStdin {
				pr, pw, perr := os.Pipe()
				if perr == nil {
					cmd.Stdin = pr
					stdinPipe = pw
					defer func() { _ = pr.Close() }()
				}
			}
			if err2 := cmd.Start(); err2 != nil {
				return err2
			}
		} else {
			return err
		}
	}
	_ = cmd.Process.Release()
	if stdinPipe != nil {
		// 这个写端必须活到本进程结束：它一关，子进程就收到 EOF 自行退出。
		holdPipe(stdinPipe)
	}
	return nil
}

// ---- 鉴权与来源 ----

func bearerOnly(token string) func(*http.Request) error {
	return func(r *http.Request) error {
		hdr := strings.TrimSpace(r.Header.Get("Authorization"))
		const scheme = "bearer "
		if len(hdr) <= len(scheme) || !strings.EqualFold(hdr[:len(scheme)], scheme) {
			return errors.New("missing bearer token")
		}
		got := strings.TrimSpace(hdr[len(scheme):])
		if token == "" || got == "" {
			return errors.New("token not configured")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			return errors.New("invalid token")
		}
		return nil
	}
}

// loopbackOrigin 只放行本机来源，防 DNS rebinding。
func loopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func printJSON(v any) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "序列化失败: %v\n", err)
		return
	}
	fmt.Println(string(raw))
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// parseTiersOr 解析档位字符串；解析不出东西就返回 def。
func parseTiersOr(s string, def []mcpserver.Tier) []mcpserver.Tier {
	out := make([]mcpserver.Tier, 0, 4)
	for _, part := range splitComma(s) {
		if t, ok := mcpserver.ParseTier(part); ok {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

// resolveMCPToken 决定用哪个 token：显式给的优先，否则从库里取（必要时生成）。
//
// 这条很关键：token 是 console 和 CLI 共用的，附着时拿错一个就直接 401，
// 而 401 会让 --auto 误以为「没有实例」，白白多起一个进程。
func resolveMCPToken(explicit string) string {
	if t := strings.TrimSpace(explicit); t != "" {
		return t
	}
	dsn, _, err := db.DSN()
	if err != nil {
		return ""
	}
	pg, err := db.Open(dsn)
	if err != nil {
		return ""
	}
	defer pg.Close()
	tok, _ := server.MCPTokenForCLI(pg)
	return tok
}

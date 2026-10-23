package agent

// 工具执行层：把「解析到的 Shell」落实为真正跑命令的路径（任务 26 步骤 ②④）。
//
// 三层，按代价从低到高：
//  1. 进程内 Go 原语（已有，shell_builtins.go）；
//  2. 外部二进制直调（已有，Windows 上绕开 PowerShell 冷启）；
//  3. 本文件的执行器——PowerShell 走预热池（10–50ms），cmd / bash 走一次性 spawn
//     （cmd.exe 0.02–0.15s、bash 20–40ms，不值得为它们维持常驻进程）。
//
// 所有路径都遵守同一组不变量：
//   - 退出码来自命令本身（不是「PS 写出过错误记录」）；
//   - stdout/stderr 合并并按真实顺序返回；
//   - 超时即杀掉整个进程组，归类 timeout，绝不留下孤儿；
//   - 任何解析/配置失败都显式报错，不换执行器（用户明确否决了静默回落）。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// shellRequest 是一条待执行命令。
type shellRequest struct {
	Command string
	WorkDir string
	Env     []string // extra "KEY=VALUE"，叠加在父进程环境之上
	Timeout time.Duration
}

// shellOutcome 是一次执行的结果。
type shellOutcome struct {
	Output   string
	ExitCode int
	TimedOut bool
	// Via 记录实际走的路径：pool / oneshot / bg，用于指标与排障。
	Via string
	// Err 是执行器自身的失败（不是命令的退出码）。
	Err error
}

// shellRuntime 是全局的「当前执行器」。设置变更时整体替换。
type shellRuntime struct {
	mu   sync.RWMutex
	spec ShellSpec
	err  error // 解析失败原因；非 nil 时所有命令直接报 config 错
	pool *psPool
}

var shellRT = &shellRuntime{}

// 指标计数（原子）。只做诊断，不影响任何执行路径。
var (
	shellStatPoolHit     atomic.Int64
	shellStatPoolSpawn   atomic.Int64
	shellStatOneShot     atomic.Int64
	shellStatFallback    atomic.Int64 // 池 spawn 失败 → 同 exe 一次性 spawn（用户批准的降级）
	shellStatTimeout     atomic.Int64
	shellStatRestarts    atomic.Int64
	shellStatLastLatency atomic.Int64 // 最近一次耗时（毫秒）
)

// ShellRuntimeInfo 是暴露给设置页的运行态快照。
type ShellRuntimeInfo struct {
	Mode         string `json:"mode"`
	Bin          string `json:"bin"`
	Version      string `json:"version"`
	Flavor       string `json:"flavor"`
	Resolved     bool   `json:"resolved"`
	Error        string `json:"error,omitempty"`
	Note         string `json:"note,omitempty"`
	PoolTarget   int    `json:"pool_target"`
	PoolIdle     int    `json:"pool_idle"`
	PoolHits     int64  `json:"pool_hits"`
	PoolSpawns   int64  `json:"pool_spawns"`
	OneShots     int64  `json:"one_shots"`
	Fallbacks    int64  `json:"fallbacks"`
	Timeouts     int64  `json:"timeouts"`
	Restarts     int64  `json:"restarts"`
	LastLatencyM int64  `json:"last_latency_ms"`
	GOOS         string `json:"goos"`
}

// CurrentShellSpec 返回当前生效的 Shell（未解析时为零值）。
func CurrentShellSpec() ShellSpec {
	shellRT.mu.RLock()
	defer shellRT.mu.RUnlock()
	return shellRT.spec
}

// ShellRuntimeStatus 供设置页/诊断接口回显。
func ShellRuntimeStatus() ShellRuntimeInfo {
	shellRT.mu.RLock()
	spec, err, pool := shellRT.spec, shellRT.err, shellRT.pool
	shellRT.mu.RUnlock()
	info := ShellRuntimeInfo{
		Mode:         string(spec.Mode),
		Bin:          spec.Bin,
		Version:      spec.Version,
		Flavor:       spec.Flavor,
		Resolved:     !spec.Zero() && err == nil,
		Note:         spec.Note,
		PoolHits:     shellStatPoolHit.Load(),
		PoolSpawns:   shellStatPoolSpawn.Load(),
		OneShots:     shellStatOneShot.Load(),
		Fallbacks:    shellStatFallback.Load(),
		Timeouts:     shellStatTimeout.Load(),
		Restarts:     shellStatRestarts.Load(),
		LastLatencyM: shellStatLastLatency.Load(),
		GOOS:         runtime.GOOS,
	}
	if err != nil {
		info.Error = err.Error()
	}
	if pool != nil {
		info.PoolTarget = pool.target
		info.PoolIdle = pool.idleCount()
	}
	return info
}

// SetShellSpec 切换执行器并重建预热池。切换是原子的：新命令用新池，旧池异步关闭
// （不阻塞正在跑的命令）。
func SetShellSpec(spec ShellSpec) {
	shellRT.mu.Lock()
	old := shellRT.pool
	shellRT.spec = spec
	shellRT.err = nil
	shellRT.pool = nil
	if !spec.Zero() && shellNeedsPool(spec) {
		shellRT.pool = newPSPool(spec, shellPoolTarget())
	}
	shellRT.mu.Unlock()
	if old != nil {
		go old.Close()
	}
}

// SetShellResolveError 记录「解析失败」这一状态。此后所有 Bash 调用返回 config 类
// 错误，并带上原因与修复建议——绝不回落到别的 Shell。
func SetShellResolveError(err error) {
	shellRT.mu.Lock()
	old := shellRT.pool
	shellRT.err = err
	shellRT.spec = ShellSpec{}
	shellRT.pool = nil
	shellRT.mu.Unlock()
	if old != nil {
		go old.Close()
	}
}

// shellNeedsPool 只有 PowerShell 值得常驻（另外两个本身就便宜）。
func shellNeedsPool(spec ShellSpec) bool {
	return spec.Mode == ShellModePowerShell
}

// shellPoolTarget 是预热池规模。默认 2：每个 pwsh 常驻约 60–100MB，2 个足够覆盖
// 「一个任务里连续几条命令」的形态，又不会在小内存机器上失控。
// ARTEX_SHELL_POOL 可覆盖（0 = 关闭预热池，全部走一次性 spawn）。
func shellPoolTarget() int {
	if v := strings.TrimSpace(os.Getenv("ARTEX_SHELL_POOL")); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n >= 0 {
			if n > 8 {
				n = 8
			}
			return n
		}
	}
	n := runtime.NumCPU() / 2
	if n < 2 {
		n = 2
	}
	if n > 4 {
		n = 4
	}
	return n
}

// ErrShellNotConfigured 表示「当前没有可用的执行器」（未解析或解析失败）。
// 用哨兵而不是靠文本匹配：错误归类必须稳定，不能因为提示文案改写就漂移。
var ErrShellNotConfigured = errors.New("Shell 执行器未配置")

// runShell 是工具层的唯一入口。
func runShell(ctx context.Context, req shellRequest) shellOutcome {
	start := time.Now()
	defer func() { shellStatLastLatency.Store(time.Since(start).Milliseconds()) }()

	shellRT.mu.RLock()
	spec, rerr, pool := shellRT.spec, shellRT.err, shellRT.pool
	shellRT.mu.RUnlock()

	if rerr != nil {
		return shellOutcome{ExitCode: -1, Err: fmt.Errorf("%w：%v", ErrShellNotConfigured, rerr)}
	}
	if spec.Zero() {
		return shellOutcome{ExitCode: -1, Err: fmt.Errorf("%w：尚未解析出可用 Shell", ErrShellNotConfigured)}
	}
	if strings.TrimSpace(req.Command) == "" {
		return shellOutcome{ExitCode: -1, Err: errors.New("命令为空")}
	}
	if req.Timeout <= 0 {
		req.Timeout = 2 * time.Minute
	}
	if req.WorkDir != "" {
		req.WorkDir = filepath.Clean(req.WorkDir)
	}

	if spec.Mode == ShellModePowerShell && pool != nil {
		if out, ok := runViaPool(ctx, pool, req); ok {
			return out
		}
		// 池补不上（spawn 失败/会话全挂）：用户已批准降级为「同 exe 一次性 spawn」。
		shellStatFallback.Add(1)
	}
	shellStatOneShot.Add(1)
	out := runOneShot(ctx, spec, req)
	out.Via = "oneshot"
	return out
}

// runViaPool 从池里租一个会话执行；返回 ok=false 表示池不可用，调用方走一次性 spawn。
func runViaPool(ctx context.Context, pool *psPool, req shellRequest) (shellOutcome, bool) {
	sess, err := pool.acquire(ctx)
	if err != nil || sess == nil {
		return shellOutcome{}, false
	}
	script := buildPowerShellScript(req.Command, req.WorkDir, false)
	text, code, timedOut, rerr := sess.Run(ctx, script, req.Timeout)
	pool.release(sess)
	switch {
	case timedOut:
		shellStatTimeout.Add(1)
		return shellOutcome{Output: text, ExitCode: -1, TimedOut: true, Via: "pool",
			Err: fmt.Errorf("命令超时（%s）", req.Timeout)}, true
	case rerr != nil:
		shellStatRestarts.Add(1)
		return shellOutcome{Output: text, ExitCode: -1, Via: "pool", Err: rerr}, true
	default:
		return shellOutcome{Output: text, ExitCode: code, Via: "pool"}, true
	}
}

// runOneShot 为当前模式启动一个一次性进程。
func runOneShot(ctx context.Context, spec ShellSpec, req shellRequest) shellOutcome {
	var (
		cmd     *exec.Cmd
		cleanup func()
	)
	switch spec.Mode {
	case ShellModePowerShell:
		dir, err := os.MkdirTemp("", "artex-exec-")
		if err != nil {
			return shellOutcome{ExitCode: -1, Err: err}
		}
		path := filepath.Join(dir, "cmd.ps1")
		// 一次性执行需要真实退出码：脚本末尾显式 exit $LASTEXITCODE。
		if err := writePowerShellScript(path, buildPowerShellScript(req.Command, req.WorkDir, true)); err != nil {
			_ = os.RemoveAll(dir)
			return shellOutcome{ExitCode: -1, Err: err}
		}
		cmd = exec.Command(spec.Bin, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path)
		cleanup = func() { _ = os.RemoveAll(dir) }
	case ShellModeCmd:
		// chcp 65001 让子进程按 UTF-8 读写；>nul 丢掉它自己的提示行。
		line := "chcp 65001>nul & " + req.Command
		cmd = exec.Command(spec.Bin, "/d", "/s", "/c", line)
		cleanup = func() {}
	default: // bash
		cmd = exec.Command(spec.Bin, "-c", req.Command)
		cleanup = func() {}
	}
	defer cleanup()

	cmd.Dir = req.WorkDir
	if len(req.Env) > 0 {
		cmd.Env = append(os.Environ(), req.Env...)
	}
	var buf bytes.Buffer
	// 同一个 *bytes.Buffer 赋给两个字段：os/exec 识别为同一 writer，只用一个
	// goroutine 写，顺序天然正确且无数据竞争。
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	configureExternalProc(cmd)

	if err := cmd.Start(); err != nil {
		return shellOutcome{ExitCode: -1, Err: err}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(req.Timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		text := ensureUTF8Output(buf.String())
		if err == nil {
			return shellOutcome{Output: text, ExitCode: 0}
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return shellOutcome{Output: text, ExitCode: ee.ExitCode()}
		}
		return shellOutcome{Output: text, ExitCode: -1, Err: err}
	case <-timer.C:
		_ = terminateExternalProc(cmd)
		<-done
		shellStatTimeout.Add(1)
		return shellOutcome{Output: ensureUTF8Output(buf.String()), ExitCode: -1, TimedOut: true,
			Err: fmt.Errorf("命令超时（%s）", req.Timeout)}
	case <-ctx.Done():
		_ = terminateExternalProc(cmd)
		<-done
		return shellOutcome{Output: ensureUTF8Output(buf.String()), ExitCode: -1, Err: ctx.Err()}
	}
}

// buildPowerShellScript 组装交给 PowerShell 的脚本正文。
//
// oneShot=false（常驻会话）时不追加 exit：那会终结宿主进程。退出码由 loader 的
// $LASTEXITCODE / $? 推导。
func buildPowerShellScript(command, workDir string, oneShot bool) string {
	var b strings.Builder
	if windowsUTF8Enabled() {
		b.WriteString(powershellUTF8Preamble)
		b.WriteString("\n")
	}
	if workDir != "" {
		// 常驻会话的 cwd 会随用户命令变化，每条命令前显式回到工作目录，
		// 保证「命令在哪个目录跑」可预期。
		b.WriteString("Set-Location -LiteralPath '")
		b.WriteString(strings.ReplaceAll(workDir, "'", "''"))
		b.WriteString("'\n")
	}
	body := strings.TrimRight(command, " \t\r\n")
	b.WriteString(body)
	if oneShot && windowsExitWrapEnabled() && !strings.HasSuffix(body, "`") {
		b.WriteString("\nif ($null -eq $LASTEXITCODE) { exit 0 } else { exit $LASTEXITCODE }")
	}
	return b.String()
}

// --- 预热池 ---

// psPool 是 PowerShell 常驻会话池。租不到空闲会话就新建；空闲超过 10 分钟回收。
type psPool struct {
	spec   ShellSpec
	target int

	mu     sync.Mutex
	idle   []*psSession
	closed bool
}

func newPSPool(spec ShellSpec, target int) *psPool {
	p := &psPool{spec: spec, target: target}
	return p
}

func (p *psPool) idleCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.idle)
}

// acquire 取一个可用会话。池已满且无空闲时阻塞等待；等待超时则返回错误，
// 由调用方降级为一次性 spawn。
func (p *psPool) acquire(ctx context.Context) (*psSession, error) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errors.New("池已关闭")
		}
		// 从尾部取最新的（最可能还热着），同时丢弃过期的。
		for len(p.idle) > 0 {
			s := p.idle[len(p.idle)-1]
			p.idle = p.idle[:len(p.idle)-1]
			if s.dead.Load() || s.idleFor() > 10*time.Minute {
				shellStatRestarts.Add(1)
				go s.Close()
				continue
			}
			p.mu.Unlock()
			shellStatPoolHit.Add(1)
			return s, nil
		}
		p.mu.Unlock()

		s, err := newPSSession(p.spec)
		if err == nil {
			shellStatPoolSpawn.Add(1)
			return s, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// release 归还会话。池已满或会话已死则直接关掉。
func (p *psPool) release(s *psSession) {
	if s == nil {
		return
	}
	p.mu.Lock()
	if p.closed || s.dead.Load() || len(p.idle) >= p.target {
		p.mu.Unlock()
		s.Close()
		return
	}
	p.idle = append(p.idle, s)
	p.mu.Unlock()
}

// Close 关闭池内全部会话。
func (p *psPool) Close() {
	p.mu.Lock()
	sessions := p.idle
	p.idle = nil
	p.closed = true
	p.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
}

// WarmShellPool 在启动/切换执行器后调用一次：后台把池填满，让第一条命令就免冷启。
// 失败只是少一个预热进程，不影响正确性（下次 acquire 会再试）。
func WarmShellPool(ctx context.Context) {
	shellRT.mu.RLock()
	pool := shellRT.pool
	shellRT.mu.RUnlock()
	if pool == nil || pool.target <= 0 {
		return
	}
	go func() {
		for i := 0; i < pool.target; i++ {
			s, err := newPSSession(pool.spec)
			if err != nil {
				return
			}
			shellStatPoolSpawn.Add(1)
			pool.release(s)
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
}

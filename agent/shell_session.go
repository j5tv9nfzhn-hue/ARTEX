package agent

// 常驻 PowerShell 会话（任务 26 步骤 ④-A：预热进程池）。
//
// 为什么要常驻：`pwsh -NoProfile -Command "exit 0"` 冷启实测约 1.2s
// （Codex #55264），`powershell.exe` 5.1 在 0.82–6.88s 之间波动（#26998）。
// 而 cmd.exe 只要 0.02–0.15s。也就是说 Windows 上每条 Bash 调用都在为一个
// .NET 运行时付冷启动税，这也是「Bash 单次几百毫秒到数秒」的直接来源。
//
// 做法：启动一个 loader 脚本，自己 `[Console]::In.ReadLine()` 循环。父进程每执行一条
// 命令只做三件事：
//  1. 把命令写成 .ps1 文件（UTF-8 带 BOM + CRLF，见 shell_encoding.go）；
//  2. 往 stdin 写这个文件的绝对路径；
//  3. 读 stdout 直到出现本会话专属的结束标记。
//
// 用「文件 + 路径」而不是「把命令拼进一行」的两个理由：
//   - 不做引号转义，多行脚本 / here-string / 花括号嵌套原样保留；
//   - 每行输入都是一个绝对路径，不存在「一行命令被当成不完整语法块继续等输入」
//     的挂起风险（直接喂源码行的经典坑）。
//
// 已知边界（都有兜底，不是静默失败）：
//   - 脚本里写 `exit` 会直接终结宿主进程：检测到进程退出即重建会话，本次调用按
//     真实退出码返回，并记一次 session_restart。
//   - 脚本挂住（等 stdin / 死循环）超过超时：整进程 kill，按 timeout 归类，
//     下次调用重建。共享 runspace 里跑飞的脚本本来就杀不掉，只能连进程一起杀。

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// shellSessionLoader 是常驻 PowerShell 宿主的 loader 脚本。
//
// 全部用 ASCII 写（中文说明留在 Go 侧）：5.1 读无 BOM 的 .ps1 会按系统 ANSI 代码页
// 解码，loader 一旦含非 ASCII 就可能被解成乱码。让它保持纯 ASCII 可以直接绕开
// 这条链路，代价为零。
const shellSessionLoader = `$ErrorActionPreference = 'Continue'
try {
  [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
  [Console]::InputEncoding  = [Text.UTF8Encoding]::new($false)
  $OutputEncoding           = [Text.UTF8Encoding]::new($false)
  $ProgressPreference       = 'SilentlyContinue'
} catch { }
while ($true) {
  $p = [Console]::In.ReadLine()
  if ($null -eq $p) { break }
  if ($p -eq 'ARTEX_SHELL_EXIT') { break }
  $p = $p.Trim()
  if ($p -eq '') { continue }
  $global:LASTEXITCODE = $null
  $ok = $true
  try {
    . $p
  } catch {
    $ok = $false
    [Console]::Error.WriteLine($_.Exception.Message)
  }
  $rc = 0
  if ($null -ne $LASTEXITCODE) { $rc = $LASTEXITCODE } elseif (-not $ok) { $rc = 1 }
  [Console]::Out.WriteLine('__ARTEX_DONE_' + $env:ARTEX_SHELL_TOKEN + ':' + $rc)
  [Console]::Out.Flush()
}
`

// shellMaxCaptureBytes 是单条命令在内存里保留的输出上限。超过即判超限并重建会话
// （常驻会话没法只截断不断流，只能重来）。
const shellMaxCaptureBytes = 4 << 20

// psSession 是一个活着的 PowerShell 宿主进程。
type psSession struct {
	spec  ShellSpec
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *os.File
	rd    *bufio.Reader // 必须是常驻的：每命令新建 Reader 会丢掉上次预读的字节
	token string
	dir   string
	seq   int64
	mu    sync.Mutex // 同一会话串行执行
	dead  atomic.Bool
	done  chan struct{} // 进程退出时关闭
	// lastUse 是最近一次使用时间（unix nano），池据此回收空闲会话。
	lastUse atomic.Int64
}

var psSessionCounter atomic.Int64

// newPSSession 启动一个宿主进程并完成握手。握手失败即返回错误——调用方据此
// 决定「池补不上时降级为一次性 spawn」。
func newPSSession(spec ShellSpec) (*psSession, error) {
	id := psSessionCounter.Add(1)
	token := "s" + strconv.FormatInt(time.Now().UnixNano(), 36) + "_" + strconv.FormatInt(id, 10)

	dir, err := os.MkdirTemp("", "artex-shell-")
	if err != nil {
		return nil, err
	}
	loaderPath := filepath.Join(dir, "loader.ps1")
	// loader 必须带 BOM：5.1 见到 BOM 才按 UTF-8 解码。
	if err := writePowerShellScript(loaderPath, shellSessionLoader); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}

	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", loaderPath}
	cmd := exec.Command(spec.Bin, args...)
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.Stdin = stdinR
	cmd.Env = append(os.Environ(), "ARTEX_SHELL_TOKEN="+token)
	// CREATE_NO_WINDOW：不加会在每次 spawn 时闪黑窗（ZCode #363 的同类问题）。
	configureExternalProc(cmd)

	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		_ = stdinW.Close()
		_ = stdinR.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	// 父进程关掉自己的写端/读端副本，否则读端永远等不到 EOF。
	_ = pw.Close()
	_ = stdinR.Close()

	s := &psSession{
		spec:  spec,
		cmd:   cmd,
		stdin: stdinW,
		out:   pr,
		rd:    bufio.NewReaderSize(pr, 32<<10),
		token: token,
		dir:   dir,
		done:  make(chan struct{}),
	}
	// 唯一负责回收进程的 goroutine：exec.Cmd.Wait 只能调用一次。
	go func() {
		_ = cmd.Wait()
		close(s.done)
	}()
	s.touch()

	// 握手：跑一次空脚本，确认宿主真的在按协议工作（而不是启动失败后挂在半路）。
	readyPath := filepath.Join(dir, "ready.ps1")
	if err := writePowerShellScript(readyPath, "Write-Output 'artex-session-ready'"); err != nil {
		s.kill()
		return nil, err
	}
	out, code, timedOut, err := s.execFile(readyPath, 15*time.Second)
	if err != nil || timedOut || code != 0 || !strings.Contains(out, "artex-session-ready") {
		s.kill()
		switch {
		case err != nil:
			return nil, err
		case timedOut:
			return nil, fmt.Errorf("握手超时（15s）")
		default:
			return nil, fmt.Errorf("握手失败（exit=%d，输出 %q）", code, truncateForLog(out, 200))
		}
	}
	return s, nil
}

// Run 执行一条命令脚本正文。返回合并输出、退出码、是否超时。
func (s *psSession) Run(ctx context.Context, script string, timeout time.Duration) (string, int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead.Load() {
		return "", 0, false, errShellSessionDead
	}
	s.seq++
	path := filepath.Join(s.dir, "cmd"+strconv.FormatInt(s.seq, 10)+".ps1")
	if err := writePowerShellScript(path, script); err != nil {
		return "", 0, false, err
	}
	defer func() { _ = os.Remove(path) }()

	if deadline, ok := ctx.Deadline(); ok {
		if until := time.Until(deadline); timeout <= 0 || until < timeout {
			timeout = until
		}
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	s.touch()
	return s.execFile(path, timeout)
}

func (s *psSession) execFile(path string, timeout time.Duration) (string, int, bool, error) {
	// stdin 写路径。Windows 上反斜杠在 PowerShell 里是普通字符，直接用即可。
	if _, err := io.WriteString(s.stdin, path+"\n"); err != nil {
		s.dead.Store(true)
		return "", 0, false, fmt.Errorf("写入宿主 stdin 失败：%w", err)
	}
	marker := "__ARTEX_DONE_" + s.token + ":"

	type readResult struct {
		text string
		code int
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		var buf strings.Builder
		for {
			line, err := s.rd.ReadString('\n')
			if idx := strings.Index(line, marker); idx >= 0 {
				// 标记可能和没有换行结尾的输出拼在同一行：截断后返回。
				if idx > 0 {
					buf.WriteString(line[:idx])
				}
				codeStr := strings.TrimSpace(line[idx+len(marker):])
				code, cerr := strconv.Atoi(codeStr)
				if cerr != nil {
					code = -1
				}
				done <- readResult{text: buf.String(), code: code}
				return
			}
			buf.WriteString(line)
			if err != nil {
				done <- readResult{text: buf.String(), err: err}
				return
			}
			if buf.Len() > shellMaxCaptureBytes {
				done <- readResult{text: buf.String(), err: fmt.Errorf("单条命令输出超过 %d 字节", shellMaxCaptureBytes)}
				return
			}
		}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			s.dead.Store(true)
			s.kill()
			return r.text, -1, false, r.err
		}
		return r.text, r.code, false, nil
	case <-time.After(timeout):
		// 共享 runspace 里超时的脚本杀不掉，只能连进程一起杀；下次 acquire 重建。
		s.dead.Store(true)
		s.kill()
		return "", -1, true, nil
	}
}

func (s *psSession) touch() { s.lastUse.Store(time.Now().UnixNano()) }

func (s *psSession) idleFor() time.Duration {
	ns := s.lastUse.Load()
	if ns == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ns))
}

// Close 优雅关闭：先让它退出循环，再兜底 kill。
func (s *psSession) Close() {
	if s.dead.Swap(true) {
		s.kill()
		return
	}
	_, _ = io.WriteString(s.stdin, "ARTEX_SHELL_EXIT\n")
	_ = s.stdin.Close()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		_ = terminateExternalProc(s.cmd)
	}
	_ = s.out.Close()
	_ = os.RemoveAll(s.dir)
}

// kill 立即终止进程并清理临时目录（可重入）。
func (s *psSession) kill() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = terminateExternalProc(s.cmd)
	}
	_ = s.stdin.Close()
	_ = s.out.Close()
	_ = os.RemoveAll(s.dir)
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

var errShellSessionDead = fmt.Errorf("常驻会话已失效")

package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

var (
	extProcSemOnce sync.Once
	extProcSem     chan struct{}
)

// maxConcurrentExternalProcs 按报告规约将跨 Worker 全局并发外部子进程槽位数限制为
// max(2, runtime.NumCPU()/2)（4 核机器上最多同时运行 2 个重型外部子进程），
// 防止多个 Worker 同时拉起外部工具或 Shell 子进程把宿主机 CPU 打满。
func maxConcurrentExternalProcs() int {
	n := runtime.NumCPU() / 2
	if n < 2 {
		return 2
	}
	if n > 8 {
		return 8
	}
	return n
}

func getExternalProcSem() chan struct{} {
	extProcSemOnce.Do(func() {
		extProcSem = make(chan struct{}, maxConcurrentExternalProcs())
	})
	return extProcSem
}

// AcquireExternalProc 获取一个全局外部子进程执行令牌，返回释放函数。
func AcquireExternalProc(ctx context.Context) (func(), error) {
	sem := getExternalProcSem()
	select {
	case sem <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() { <-sem })
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runManagedExternalCommand 直接通过操作系统的进程创建原语（Windows 下直接 CreateProcess，
// 完全绕过 powershell.exe / cmd.exe）启动外部编译型工具（如 nmap、nuclei、ffuf、sqlmap、curl.exe），
// 受跨 Worker 全局外部进程信号量节流，并配置 WaitDelay 防止孙进程继承管道句柄导致挂死。
func runManagedExternalCommand(ctx context.Context, binPath string, args []string, workDir string, extraEnv []string, timeout time.Duration) (output string, exitCode int, err error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	release, err := AcquireExternalProc(runCtx)
	if err != nil {
		return "", -1, err
	}
	defer release()

	cmd := exec.CommandContext(runCtx, binPath, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	cmd.WaitDelay = 3 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()
	outStr := clipUTF8(buf.String(), 64*1024)
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		outStr += "\n... [命令执行超时已终止] ..."
		return outStr, 124, nil
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return outStr, exitErr.ExitCode(), nil
		}
		return outStr, 1, runErr
	}
	return outStr, 0, nil
}

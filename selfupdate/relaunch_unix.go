//go:build !windows

package selfupdate

// Unix 上"父进程退出、子进程继续跑"要靠 setsid：把子进程放进新会话，
// 父进程所在的终端关闭、父进程收到 SIGTERM 都不会连带把新进程带走。
// 这正是"不用 start.sh 也能更新"的关键一步。

import (
	"os"
	"os/exec"
	"syscall"
)

func spawnDetached(exe string, args []string, env []string, dir string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

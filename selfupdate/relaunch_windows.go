//go:build windows

package selfupdate

// Windows 没有 execve，也没有 fork：想让"父进程退出、子进程继续跑"，
// 只能 CreateProcess 一个新的自己。两条硬约束：
//
//  1. ARTEX 把自己放进了 kill-on-close 的 Job Object（jobobject_windows.go），
//     子进程默认继承 Job，父进程一退出内核就把子进程一起杀掉 →
//     必须用 CREATE_BREAKAWAY_FROM_JOB 挣脱（Job 已开 JOB_OBJECT_LIMIT_BREAKAWAY_OK）；
//  2. 不能 DETACHED_PROCESS：那会把子进程从控制台摘掉，双击启动时日志就看不到了。
//     CREATE_NEW_PROCESS_GROUP + 共用控制台，父进程退出后窗口仍在。

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func spawnDetached(exe string, args []string, env []string, dir string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB | windows.CREATE_NEW_PROCESS_GROUP,
	}
	return cmd.Start()
}

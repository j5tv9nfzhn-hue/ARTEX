//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
)

func configureExternalProc(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateExternalProc(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if pid := cmd.Process.Pid; pid > 0 {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
			return nil
		}
	}
	return cmd.Process.Kill()
}

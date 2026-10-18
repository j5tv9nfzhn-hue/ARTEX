//go:build windows

package agent

import (
	"os/exec"
	"syscall"
)

const createNoWindowFlag = 0x08000000

func configureExternalProc(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindowFlag}
}

func terminateExternalProc(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

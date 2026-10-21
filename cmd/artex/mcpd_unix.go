//go:build !windows

package main

import (
	"os"
	"syscall"
)

// detachAttr：Unix 上 Setsid 就足以脱离会话与终端。
func detachAttr(bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func isAccessDenied(error) bool { return false }

var heldPipes []*os.File

func holdPipe(f *os.File) { heldPipes = append(heldPipes, f) }

func mcpProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

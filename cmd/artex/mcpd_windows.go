//go:build windows

package main

import (
	"errors"
	"os"
	"syscall"
)

// Windows 的进程脱离只有一条真路。
//
//	DETACHED_PROCESS          不会脱离父 Job Object
//	CREATE_NEW_PROCESS_GROUP  也不会
//	CREATE_BREAKAWAY_FROM_JOB 才会 —— 但要求父 job 设了
//	                          JOB_OBJECT_LIMIT_BREAKAWAY_OK，否则 ERROR_ACCESS_DENIED
//
// 所以调用方必须准备「不带 breakaway 再试一次」的降级路径。
const (
	mpDetached     = 0x00000008
	mpNewGroup     = 0x00000200
	mpBreakaway    = 0x01000000
	mpNoWindow     = 0x08000000
	mpQueryLimited = 0x1000
	mpStillActive  = 259
	mpAccessDenied = syscall.Errno(5)
)

// detachAttr 返回脱离用的进程属性。
func detachAttr(breakaway bool) *syscall.SysProcAttr {
	flags := uint32(mpDetached | mpNewGroup | mpNoWindow)
	if breakaway {
		flags |= mpBreakaway
	}
	return &syscall.SysProcAttr{CreationFlags: flags}
}

// isAccessDenied 判断是不是「父 job 不给脱离」导致的拒绝。
func isAccessDenied(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == mpAccessDenied
}

// heldPipes 持有给子进程的 stdin 写端。
//
// 不能让它被 GC 掉：一旦 Close，子进程立刻收到 EOF 退出——
// 那正是我们要的行为，但时机必须由本进程的生死决定，而不是 GC。
var heldPipes []*os.File

func holdPipe(f *os.File) { heldPipes = append(heldPipes, f) }

// processAlive 用 OpenProcess + GetExitCodeProcess 判断进程是否还在。
func mcpProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(mpQueryLimited, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == mpStillActive
}

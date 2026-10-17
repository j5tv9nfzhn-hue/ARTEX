//go:build windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createNoWindow 让测试派生的控制台进程不弹窗（CREATE_NO_WINDOW）。
const createNoWindow = 0x08000000

// waitTimeout 是 WaitForSingleObject 的超时返回码（x/sys/windows 未导出该常量）。
const waitTimeout = 0x00000102

const (
	envJobHelper        = "ARTEX_TEST_JOB_HELPER"
	envJobHelperPIDFile = "ARTEX_TEST_JOB_HELPER_PIDFILE"
)

// procIsProcessInJob 是 x/sys/windows 未提供的 kernel32!IsProcessInJob。
var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// TestKillOnCloseJobTerminatesProcessTree 验证 Job 的两个关键契约：
//  1. 放进 Job 的进程之后派生的子进程（孙进程）自动继承 Job；
//  2. 最后一个句柄关闭时，Job 内全部进程被终止。
//
// CI（Linux）不跑本文件；Windows 上的 go test ./cmd/artex/ 会覆盖。
func TestKillOnCloseJobTerminatesProcessTree(t *testing.T) {
	job, err := newKillOnCloseJob()
	if err != nil {
		t.Fatalf("newKillOnCloseJob: %v", err)
	}
	jobHeld := true
	defer func() {
		if jobHeld {
			_ = windows.CloseHandle(job)
		}
	}()

	// 「先等 6 秒、再长跑 ping」的两段式包装：赋值 Job 发生在长跑孙进程创建之前，
	// 且 6 秒后 cmd 名下只会剩下那一个长跑 PING.EXE，枚举不歧义。
	start := time.Now()
	cmd := exec.Command("cmd.exe", "/c", "ping -n 6 127.0.0.1 >nul & ping -n 60 127.0.0.1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start cmd.exe: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	if err := assignPIDToJob(job, uint32(cmd.Process.Pid)); err != nil {
		if isJobAssignmentDenied(err) {
			t.Skipf("当前进程已在外层 Job 中（沙箱/CI），无法把子进程放进新 Job：%v", err)
		}
		t.Fatalf("assignPIDToJob(cmd): %v", err)
	}

	grandchild, err := waitSoleChildByName(uint32(cmd.Process.Pid), "PING.EXE", start.Add(7*time.Second), 15*time.Second)
	if err != nil {
		t.Fatalf("等待 cmd 的长跑子进程 PING.EXE：%v", err)
	}

	inJob, err := isProcessInJob(grandchild, job)
	if err != nil {
		t.Fatalf("isProcessInJob(%d): %v", grandchild, err)
	}
	if !inJob {
		t.Fatalf("孙进程 %d 不在 Job 内：继承关系未生效", grandchild)
	}

	// 关闭最后一个句柄 = 内核终止 Job 内全部进程。
	if err := windows.CloseHandle(job); err != nil {
		t.Fatalf("CloseHandle(job): %v", err)
	}
	jobHeld = false

	if err := waitProcessGone(grandchild, 10*time.Second); err != nil {
		t.Fatalf("Job 关闭后孙进程仍存活：%v", err)
	}
	if err := waitProcessGone(uint32(cmd.Process.Pid), 10*time.Second); err != nil {
		t.Fatalf("Job 关闭后 cmd 仍存活：%v", err)
	}
}

// TestGuardProcessTreeKillsDescendantsOnHardExit 是端到端验证：子进程调用
// guardProcessTree（与 artex 启动时同一入口）后派生长跑孙进程，父测试把子进程
// 强杀（TerminateProcess，等价任务管理器「结束任务」）；孙进程必须随 Job 一起
// 消失。这才证明了「ARTEX 被强杀后不再留下跑满 CPU 的 python 孤儿」。
func TestGuardProcessTreeKillsDescendantsOnHardExit(t *testing.T) {
	if os.Getenv(envJobHelper) == "1" {
		runJobHelperProcess()
		return
	}

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	var helperOut bytes.Buffer
	helper := exec.Command(exe, "-test.run=TestGuardProcessTreeKillsDescendantsOnHardExit", "-test.v")
	helper.Env = append(os.Environ(), envJobHelper+"=1", envJobHelperPIDFile+"="+pidFile)
	helper.Stdout = &helperOut
	helper.Stderr = &helperOut
	helper.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	helperKilled := false
	defer func() {
		if !helperKilled {
			_ = helper.Process.Kill()
		}
		_, _ = helper.Process.Wait()
	}()

	pid, err := waitHelperPID(pidFile, 30*time.Second)
	if err != nil {
		t.Fatalf("等待 helper 上报子进程 pid：%v（helper 输出：%s）", err, helperOut.String())
	}
	if alive, err := processAlive(pid); err != nil {
		t.Fatalf("processAlive(%d): %v", pid, err)
	} else if !alive {
		t.Fatalf("helper 上报的孙进程 %d 已经不在，测试无意义", pid)
	}

	// 硬杀 helper：没有 ctx 取消、没有清理钩子，只剩 Job 兜底。
	if err := helper.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	helperKilled = true

	if err := waitProcessGone(pid, 15*time.Second); err != nil {
		t.Fatalf("helper 被强杀后孙进程仍存活：%v", err)
	}
}

// runJobHelperProcess 是上面测试的子进程分支：与 artex 启动时一样收口，然后启动
// 一个长跑孙进程并把它的 pid 写进 PID 文件，最后挂起等父测试强杀。
func runJobHelperProcess() {
	report := func(s string) {
		if p := os.Getenv(envJobHelperPIDFile); p != "" {
			_ = os.WriteFile(p, []byte(s), 0o644)
		}
	}
	if err := guardProcessTree(); err != nil {
		report("ERR=" + err.Error())
		return
	}
	cmd := exec.Command("cmd.exe", "/c", "ping -n 60 127.0.0.1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		report("ERR=" + err.Error())
		return
	}
	pid, err := waitSoleChildByName(uint32(cmd.Process.Pid), "PING.EXE", time.Time{}, 15*time.Second)
	if err != nil {
		report("ERR=" + err.Error())
		return
	}
	report(fmt.Sprintf("PID=%d", pid))
	// 兜底：父测试 60 秒内没杀就自行退出（进程退出同样会收掉 Job）。
	time.Sleep(60 * time.Second)
}

// waitHelperPID 轮询 helper 写下的 PID 文件，返回它上报的长跑孙进程 pid。
func waitHelperPID(path string, timeout time.Duration) (uint32, error) {
	deadline := time.Now().Add(timeout)
	for {
		if b, err := os.ReadFile(path); err == nil {
			s := strings.TrimSpace(string(b))
			if s != "" {
				if msg, ok := strings.CutPrefix(s, "ERR="); ok {
					if isJobAssignmentDenied(errors.New(msg)) {
						return 0, fmt.Errorf("skip: %s", msg)
					}
					return 0, errors.New(msg)
				}
				if raw, ok := strings.CutPrefix(s, "PID="); ok {
					n, err := strconv.ParseUint(raw, 10, 32)
					if err != nil {
						return 0, fmt.Errorf("解析 pid %q: %w", raw, err)
					}
					return uint32(n), nil
				}
			}
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("等待 %s 超时", path)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitSoleChildByName 等待 parent 名下恰好出现一个名为 exe 的子进程并返回其 pid。
// notBefore 用于让包装命令的短命阶段先结束（零值表示立即接受）。
func waitSoleChildByName(parent uint32, exe string, notBefore time.Time, timeout time.Duration) (uint32, error) {
	deadline := time.Now().Add(timeout)
	var last int
	for {
		kids, err := childPIDsByName(parent, exe)
		if err != nil {
			return 0, err
		}
		last = len(kids)
		if last == 1 && (notBefore.IsZero() || time.Now().After(notBefore)) {
			return kids[0], nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("等了 %s，%s 名下匹配 %s 的子进程仍有 %d 个", timeout, strconv.FormatUint(uint64(parent), 10), exe, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// childPIDsByName 通过 Toolhelp32 快照枚举 parent 的子进程中可执行文件名为 exe 的项。
func childPIDsByName(parent uint32, exe string) ([]uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap) //nolint:errcheck // 只读快照

	var out []uint32
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil, nil
		}
		return nil, err
	}
	for {
		if entry.ParentProcessID == parent && strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), exe) {
			out = append(out, entry.ProcessID)
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, err
		}
	}
	return out, nil
}

// isProcessInJob 询问系统某 pid 是否在给定 Job 内。
func isProcessInJob(pid uint32, job windows.Handle) (bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(h) //nolint:errcheck // 只读查询
	var in int32
	ret, _, callErr := procIsProcessInJob.Call(uintptr(h), uintptr(job), uintptr(unsafe.Pointer(&in)))
	if ret == 0 {
		return false, callErr
	}
	return in != 0, nil
}

// processAlive 报告 pid 是否仍存活（不要求打开全部权限）。
func processAlive(pid uint32) (bool, error) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil // 进程已不存在
		}
		return false, err
	}
	defer windows.CloseHandle(h) //nolint:errcheck // 只读查询
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false, err
	}
	return ev == waitTimeout, nil
}

// waitProcessGone 轮询直到 pid 消失，或超时。
func waitProcessGone(pid uint32, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		alive, err := processAlive(pid)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("进程 %d 在 %s 内仍存活", pid, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// isJobAssignmentDenied 判断错误是否来自「进程已在别的 Job 中、系统不允许嵌套赋值」。
func isJobAssignmentDenied(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "Access is denied") || strings.Contains(msg, "拒绝访问") ||
		strings.Contains(msg, "skip:")
}

//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	unixBootPollInterval = 250 * time.Millisecond
	unixBootPortTimeout  = 15 * time.Second
)

// bootBeforeStart 在 Linux / macOS 等非 Windows 平台上执行启动前端口自举检查。
//
// 三端（Linux / macOS / Windows）均发布内嵌前端的 Go 单二进制。
//   - 当目标端口空闲时，直接放行；
//   - 当目标端口被占用时，在 Linux 下尝试通过 /proc 识别是否为同目录上一轮残留的
//     artex 进程并自动回收；若为无关进程或无法在当前系统/权限下确认归属，绝不误杀，
//     交由后续或直接返回明确诊断。
func bootBeforeStart(addr, proxy, exeDir string) error {
	if err := ensureUnixPortFree(addr, exeDir, "HTTP/API"); err != nil {
		return err
	}
	if proxy != "" {
		if err := ensureUnixPortFree(proxy, exeDir, "流量记录代理"); err != nil {
			return err
		}
	}
	return nil
}

func ensureUnixPortFree(address, exeDir, label string) error {
	if strings.TrimSpace(address) == "" {
		return nil
	}
	if l, err := net.Listen("tcp", address); err == nil {
		_ = l.Close()
		return nil
	}
	if runtime.GOOS != "linux" {
		return nil
	}
	pid := lookupLinuxListeningPID(address)
	if pid <= 0 {
		return nil
	}
	image, owned := isOwnedUnixProcess(pid, exeDir)
	if !owned {
		return fmt.Errorf(
			"%s 端口 %s 已被无关进程（PID %d）占用，为避免误杀已中止启动。\n"+
				"请先结束该进程，或用 -addr / -proxy 换用其他端口。",
			label, address, pid)
	}
	log.Printf("[boot] %s 端口 %s 被上一轮进程占用（PID %d，%s），正在结束",
		label, address, pid, filepath.Base(image))
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(unixBootPortTimeout)
	killed := false
	for time.Now().Before(deadline) {
		if l, err := net.Listen("tcp", address); err == nil {
			_ = l.Close()
			log.Printf("[boot] %s 端口 %s 已释放", label, address)
			return nil
		}
		if !killed && time.Until(deadline) < unixBootPortTimeout/2 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			killed = true
		}
		time.Sleep(unixBootPollInterval)
	}
	return fmt.Errorf("%s 端口 %s 在结束占用进程后仍未释放", label, address)
}

func isOwnedUnixProcess(pid int, exeDir string) (string, bool) {
	if pid <= 0 || pid == os.Getpid() || exeDir == "" {
		return "", false
	}
	exePath, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil || exePath == "" {
		return "", false
	}
	exePath = strings.TrimSuffix(exePath, " (deleted)")
	cleanTarget := filepath.Clean(exePath)
	cleanDir := filepath.Clean(exeDir)
	base := strings.ToLower(filepath.Base(cleanTarget))
	if filepath.Dir(cleanTarget) == cleanDir && strings.HasPrefix(base, "artex") {
		return cleanTarget, true
	}
	return "", false
}

func lookupLinuxListeningPID(address string) int {
	_, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	wantPortHex := fmt.Sprintf("%04X", port)
	inodes := map[string]bool{}
	for _, procFile := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(procFile)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		first := true
		for sc.Scan() {
			if first {
				first = false
				continue
			}
			fields := strings.Fields(sc.Text())
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			_, pHex, ok := strings.Cut(fields[1], ":")
			if !ok || !strings.EqualFold(pHex, wantPortHex) {
				continue
			}
			if fields[9] != "0" && fields[9] != "" {
				inodes[fields[9]] = true
			}
		}
		_ = f.Close()
	}
	if len(inodes) == 0 {
		return 0
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		fdDir := fmt.Sprintf("/proc/%d/fd", pid)
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
				inode := target[len("socket:[") : len(target)-1]
				if inodes[inode] {
					return pid
				}
			}
		}
	}
	return 0
}

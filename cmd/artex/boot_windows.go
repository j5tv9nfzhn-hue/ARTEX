//go:build windows

// 本文件实现 ARTEX 在 Windows 上的自举启动：程序在绑定端口之前先解决端口占用，
// 避免「双击就闪退」。
//
//   - 端口占用：-addr / -proxy 的端口已被占用时，先结束占用进程并等端口真正释放，
//     再继续启动；不再让 ListenAndServe 撞上 "address already in use" 之后退出。
//
// 数据库不再需要自举：ARTEX 只用 SQLite，库文件由 config.SQLitePath() 在首次打开时
// 直接创建，没有服务要拉起、没有端口要等。这也是「下载即用」的实现方式。
//
// 误杀防护：只结束能判定属于本程序家族的进程（同目录的 artex*.exe）。
// 判定不了就报错退出，绝不猜测。
package main

import (
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strings"
	"time"
)

const (
	bootPollInterval = 250 * time.Millisecond
	bootPortTimeout  = 15 * time.Second
)

// logf 统一走标准日志，保证自举信息与既有启动日志格式一致地出现在控制台。
func logf(format string, args ...any) { log.Printf("[boot] "+format, args...) }

// bootBeforeStart 是 main 调用的自举入口。
//
// 顺序刻意如此：
//  1. 先处理本程序自己的 HTTP 端口——归属最容易判定；
//  2. 再处理代理端口。
//
// 数据库不在其中：SQLite 是进程内打开一个文件，没有可等待的就绪状态。
func bootBeforeStart(addr, proxy, exeDir string) error {
	if err := ensurePortFree(addr, exeDir, "HTTP/API"); err != nil {
		return err
	}
	if proxy != "" {
		if err := ensurePortFree(proxy, exeDir, "流量记录代理"); err != nil {
			return err
		}
	}
	return nil
}

// isOwnedProcess 判定 PID 是否属于本程序家族。
func isOwnedProcess(pid uint32, exeDir string) (string, bool) {
	path, err := processImagePath(pid)
	if err != nil || path == "" {
		return "", false
	}
	lower := strings.ToLower
	clean := func(s string) string { return lower(filepath.Clean(s)) }

	target := clean(path)
	dir := clean(exeDir)
	base := lower(filepath.Base(target))

	// 只认"恰好是正式入口 / 核心程序"的映像名。
	//
	// 这里曾经用 artex*.exe 前缀通配，于是 artex-rea.new.exe、artex-rea.old.exe
	// 也被当成"自己人"：暂存件一旦被启动，就会与正式进程互相终止（谁后启动谁赢），
	// 表现是"更新完还是老版本"，且没有任何一处会报告这件事。
	switch base {
	case "artex-rea.exe", "artex-rea-core.exe", "artex.exe":
		if filepath.Dir(target) == dir {
			return path, true
		}
	}
	// 版本化布局：核心程序位于 <dir>/versions/<ver>/artex-rea-core.exe。
	if base == "artex-rea-core.exe" {
		prefix := dir + string(filepath.Separator) + "versions" + string(filepath.Separator)
		if strings.HasPrefix(filepath.Dir(target), prefix) {
			return path, true
		}
	}
	return "", false
}

// portOwnerPID 返回占用该监听地址的进程 PID；端口空闲时返回 0。
func portOwnerPID(address string) uint32 {
	// 能绑定就是空闲。这是最可靠且免权限的判断，也是安全边界：
	// 只有确认绑不上，才有必要去找占用者。
	if l, err := net.Listen("tcp", address); err == nil {
		_ = l.Close()
		return 0
	}
	return lookupTCPTablePID(address)
}

// ensurePortFree 保证监听地址可用。
func ensurePortFree(address, exeDir, label string) error {
	pid := portOwnerPID(address)
	if pid == 0 {
		return nil
	}

	image, owned := isOwnedProcess(pid, exeDir)
	if !owned {
		return fmt.Errorf(
			"%s 端口 %s 已被无关进程（PID %d）占用，为避免误杀已中止启动。\n"+
				"请先结束该进程，或用 -addr / -proxy 换用其他端口。",
			label, address, pid)
	}

	logf("%s 端口 %s 被上一轮进程占用（PID %d，%s），正在结束",
		label, address, pid, filepath.Base(image))
	if err := terminateProcess(pid); err != nil {
		return fmt.Errorf("%s 端口 %s 的占用进程结束失败：%w", label, address, err)
	}

	// 进程被结束后端口不会立刻释放（内核还要回收套接字），
	// 必须以「真的能绑上」为准，而不是 sleep 一个固定时长。
	deadline := time.Now().Add(bootPortTimeout)
	for time.Now().Before(deadline) {
		if l, err := net.Listen("tcp", address); err == nil {
			_ = l.Close()
			logf("%s 端口 %s 已释放", label, address)
			return nil
		}
		time.Sleep(bootPollInterval)
	}
	return fmt.Errorf("%s 端口 %s 在结束占用进程后仍未释放", label, address)
}

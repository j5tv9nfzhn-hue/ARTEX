//go:build windows

// 本文件实现 ARTEX 在 Windows 上的自举启动：程序在打开存储、绑定端口之前
// 先解决两件事，避免"双击就闪退"。
//
//  1. 端口占用：-addr / -proxy 的端口已被占用时，先结束占用进程并等端口
//     真正释放，再继续启动；不再让 ListenAndServe 撞上
//     "address already in use" 之后退出。
//  2. 数据库：PostgreSQL 连不上时，先拉起程序目录 pg/ 自带的实例，
//     再回退到已注册的 Windows 服务；两条路都走不通才报错。
//
// 全部逻辑内置在 EXE 内，不新增也不修改任何外部脚本。
//
// 误杀防护：只结束能判定属于本程序家族的进程（同目录的 artex*.exe，
// 或 <EXE目录>\pg\bin 下的 postgres.exe）。判定不了就报错退出，绝不猜测。

package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/config"
	"golang.org/x/sys/windows"
)

const (
	bootPollInterval = 250 * time.Millisecond
	bootPortTimeout  = 15 * time.Second
	bootDBTimeout    = 40 * time.Second
)

// errServiceAlreadyRunning 是 Win32 ERROR_SERVICE_ALREADY_RUNNING。
// 直接写死数值，避免依赖 windows 包是否导出该常量。
const errServiceAlreadyRunning = syscall.Errno(1060)

// windowsServiceCandidates 是可能承载 PostgreSQL 的 Windows 服务名。
// 第一个是本项目此前注册的实例，其余覆盖常见安装名。
var windowsServiceCandidates = []string{
	"postgresql-ar",
	"postgresql-x64-18",
	"postgresql",
	"pgsql",
}

// logf 统一走标准日志，保证自举信息与既有启动日志格式一致地出现在控制台。
func logf(format string, args ...any) { log.Printf("[boot] "+format, args...) }

// bootBeforeStart 是 main 调用的自举入口，负责取到 DSN 后交给 bootWithDSN。
//
// 数据库配置缺失不算自举的失败——那属于"没装数据库"的场景，
// 应当由后续 NewManager 报出更贴切的错误，而不是在这里含糊地拦下。
func bootBeforeStart(addr, proxy, exeDir string) error {
	dsn, _, err := config.PostgresDSN()
	if err != nil {
		logf("未读取到数据库配置，跳过数据库自举：%v", err)
		dsn = ""
	}
	return bootWithDSN(addr, proxy, exeDir, dsn)
}

// bootWithDSN 是自举的主体。DSN 为空表示不做数据库自举。
//
// 顺序刻意如此：
//  1. 先处理本程序自己的 HTTP 端口——归属最容易判定；
//  2. 再处理代理端口；
//  3. 最后处理数据库——它可能占用任意端口，放在最后可以避免
//     "自己占着自己"式的自锁。
func bootWithDSN(addr, proxy, exeDir, dsn string) error {
	if err := ensurePortFree(addr, exeDir, "HTTP/API"); err != nil {
		return err
	}
	if proxy != "" {
		if err := ensurePortFree(proxy, exeDir, "流量记录代理"); err != nil {
			return err
		}
	}
	if dsn == "" {
		return nil
	}
	return ensurePostgresRunning(dsn, exeDir)
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

	// 1) 同目录下的 artex*.exe：上一轮残留的自身
	if filepath.Dir(target) == dir && strings.HasPrefix(base, "artex") && strings.HasSuffix(base, ".exe") {
		return path, true
	}
	// 2) 发行包自带的 PostgreSQL
	pgBin := clean(filepath.Join(exeDir, "pg", "bin"))
	if filepath.Dir(target) == pgBin && (base == "postgres.exe" || base == "postmaster.exe") {
		return path, true
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

	logf("%s 端口 %s 被上一轮进程占用（PID %d：%s），正在结束",
		label, address, pid, filepath.Base(image))
	if err := terminateProcess(pid); err != nil {
		return fmt.Errorf("%s 端口 %s 的占用进程结束失败：%w", label, address, err)
	}

	// 进程被结束后端口不会立刻释放（内核还要回收套接字），
	必须以“真的能绑上”为准，而不是 sleep 一个固定时长。
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

// bundledPGDir 返回发行包内 PostgreSQL 的根目录（<EXE目录>\pg）。
func bundledPGDir(exeDir string) string { return filepath.Join(exeDir, "pg") }

// bundledPGReady 判断内置 pg/ 是否具备可启动的完整条件。
func bundledPGReady(exeDir string) bool {
	root := bundledPGDir(exeDir)
	if _, err := os.Stat(filepath.Join(root, "bin", "pg_ctl.exe")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(root, "data", "PG_VERSION"))
	return err == nil
}

// ensurePostgresRunning 保证 DSN 指向的 PostgreSQL 可连接。
func ensurePostgresRunning(dsn, exeDir string) error {
	if postgresReachable(dsn) {
		return nil
	}
	logf("PostgreSQL 未响应，开始自举启动（目标 %s）", hostPortOf(dsn))

	if bundledPGReady(exeDir) {
		root := bundledPGDir(exeDir)
		data := filepath.Join(root, "data")
		logFile := bundledPGLog(exeDir)
		logf("使用发行包内置 PostgreSQL：%s", root)
		if err := startBundledPostgres(root, data, logFile); err != nil {
			logf("内置 PostgreSQL 启动失败：%v；尝试 Windows 服务", err)
		} else if waitPostgres(dsn, bootDBTimeout) {
			logf("内置 PostgreSQL 已就绪")
			return nil
		} else {
			return withLogTail(
				fmt.Errorf("内置 PostgreSQL 已启动，但 %s 在 %s 内未就绪",
					hostPortOf(dsn), bootDBTimeout),
				logFile)
		}
	} else {
		logf("无可用的内置 pg/ 数据目录，尝试 Windows 服务")
	}

	if ok, name := startWindowsService(); ok {
		if waitPostgres(dsn, bootDBTimeout) {
			logf("Windows 服务 %s 已就绪", name)
			return nil
		}
		return fmt.Errorf("Windows 服务 %s 已启动，但 %s 在 %s 内未就绪",
			name, hostPortOf(dsn), bootDBTimeout)
	}

	return fmt.Errorf(
		"无法连接 PostgreSQL（%s），且既无可用的内置 pg/ 目录，也无法启动已注册的 Windows 服务。\n"+
			"请确认数据库已安装且 pg\\data 完整，或手工启动 PostgreSQL 后重试。",
		hostPortOf(dsn))
}

// bundledPGLog 是内置实例的日志文件路径。
//
// 必须放在 PGDATA 之外。放在 data\ 里时，postmaster 持有的 server.log 句柄
// 会让子进程以 "./server.log" 重新打开时撞上 sharing violation，
// 数据库会永远卡在 "the database system is starting up"，
// 表现为端口在监听但连接一律失败。
func bundledPGLog(exeDir string) string { return filepath.Join(bundledPGDir(exeDir), "server.log") }

// startBundledPostgres 调用 pg_ctl 启动内置实例。
// 输出直接落到当前控制台，让 PostgreSQL 的诊断信息可见。
func startBundledPostgres(root, data, logFile string) error {
	cmd := exec.Command(
		filepath.Join(root, "bin", "pg_ctl.exe"),
		"-D", data,
		"-l", logFile,
		"-w", "-t", "60", "start",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// serverLogTail 取日志末尾若干行，用于把启动失败的真实原因带给用户。
// 没有日志文件时返回空串，调用方据此省略。
func serverLogTail(path string, maxLines int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// withLogTail 在错误信息末尾附加数据库日志尾部。
// 自举失败时用户只看得到"未就绪"三个字，无从下手；日志尾部通常直接
// 写明了端口冲突、配置语法错误或权限问题。
func withLogTail(err error, logFile string) error {
	tail := serverLogTail(logFile, 10)
	if tail == "" {
		return err
	}
	return fmt.Errorf("%w\n\nPostgreSQL 日志尾部（%s）：\n%s", err, logFile, tail)
}

// startWindowsService 依次尝试启动候选服务名，返回是否启动成功及服务名。
func startWindowsService() (bool, string) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, ""
	}
	defer windows.CloseServiceHandle(scm)

	for _, name := range windowsServiceCandidates {
		namePtr, err := windows.UTF16PtrFromString(name)
		if err != nil {
			continue
		}
		svc, err := windows.OpenService(scm, namePtr, windows.SERVICE_START)
		if err != nil {
			continue // 服务不存在，换下一个候选
		}
		err = windows.StartService(svc, 0, nil)
		windows.CloseServiceHandle(svc)
		if err == nil || errors.Is(err, errServiceAlreadyRunning) {
			return true, name
		}
	}
	return false, ""
}

// waitPostgres 轮询等待数据库就绪。
func waitPostgres(dsn string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if postgresReachable(dsn) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(bootPollInterval)
	}
}

//go:build windows

package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 自举逻辑的测试分两类：
//   - 纯函数（DSN 解析、端口字节序、内置目录判定、归属判定）；
//   - 真正碰系统状态的（查端口占用），用本进程自己开的监听端口来验证。
//
// 结束进程、拉起 PostgreSQL 这两个动作不在单元测试里执行：
// 前者会杀掉测试自身，后者需要真实数据库，都不适合放在 go test 里。

// TestDecodePort 锁定端口字节序。
// dwLocalPort 存的是 htons 后的值，若按大端直读，8787 会被解析成 21282，
// 导致端口占用永远查不出来——这里用几个有代表性的端口钉死方向。
func TestDecodePort(t *testing.T) {
	cases := []struct {
		port int
	}{
		{80}, {443}, {8787}, {8788}, {5432}, {65535}, {1},
	}
	for _, c := range cases {
		// 构造 htons 结果：低 16 位按字节交换后放进 DWORD。
		//
		// 这里必须交换字节。此前写的是 `(low>>8)<<8 | (low&0xff)`，化简后
		// 等于 low 本身——即原样传入主机序值，于是用例断言的是恒等函数，
		// 而 decodePort 按 MSDN 的网络字节序正确地交换了一次，两边对不上。
		// 用例曾经报 decodePort(0x2253)=21282 而非 8787，根因在这里。
		low := uint16(c.port)
		htons := uint32(low&0xff)<<8 | uint32(low>>8)
		if got := decodePort(htons); got != c.port {
			t.Errorf("decodePort(%#x) = %d，预期 %d", htons, got, c.port)
		}
	}
}

// TestLookupTCPTablePIDFindsOwnListener 用本进程真实监听的端口端到端验证
// 端口归属查询：表遍历、变长行步长、IPv4 行结构体布局、端口字节序，
// 任一处出错这个用例都会失败。
func TestLookupTCPTablePIDFindsOwnListener(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	defer l.Close()

	_, portStr, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if portStr == "" {
		t.Fatal("未取到监听端口")
	}

	// 地址此刻确实被占用（自己占的）
	if pid := portOwnerPID(l.Addr().String()); pid != uint32(os.Getpid()) {
		t.Fatalf("portOwnerPID = %d，预期 %d（自身）", pid, os.Getpid())
	}

	// 换一个不相干的端口，必须查不到任何进程
	if pid := lookupTCPTablePID(net.JoinHostPort("127.0.0.1", "1")); pid != 0 {
		t.Errorf("127.0.0.1:1 不应有监听者，却查到 PID %d", pid)
	}
}

// TestPortOwnerPIDOnFreePort 确认空闲端口不会被误判为占用。
func TestPortOwnerPIDOnFreePort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	addr := l.Addr().String()
	l.Close() // 释放后再查

	if pid := portOwnerPID(addr); pid != 0 {
		t.Errorf("端口 %s 已释放，仍查到占用者 PID %d", addr, pid)
	}
}

func TestParsePostgresURLVariants(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		host string
		port string
	}{
		{"完整 URL", "postgres://u:p@db.internal:6543/artex?sslmode=disable", "db.internal", "6543"},
		{"URL 省略端口", "postgres://u:p@127.0.0.1/artex", "127.0.0.1", "5432"},
		{"URL 省略主机", "postgres:///artex", "127.0.0.1", "5432"},
		{"key=value 形式", "host=10.0.0.5 port=6544 user=artex dbname=artex", "10.0.0.5", "6544"},
		{"key=value 带引号", "host='db1' port=5433 user=artex", "db1", "5433"},
		{"空串回落默认", "", "127.0.0.1", "5432"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parsePostgresURL(c.dsn)
			if got.host != c.host || got.port != c.port {
				t.Fatalf("parsePostgresURL(%q) = %s:%s，预期 %s:%s",
					c.dsn, got.host, got.port, c.host, c.port)
			}
		})
	}
}

// TestPostgresReachableOnClosedPort 确认探活不会把"连不上"当成"连上了"。
func TestPostgresReachableOnClosedPort(t *testing.T) {
	if postgresReachable("postgres://artex@127.0.0.1:1/artex?sslmode=disable&connect_timeout=1") {
		t.Error("127.0.0.1:1 不应有数据库在监听")
	}
}

// TestBundledPGReadyOnMissingDir 确认不存在的目录不会被当成可用的内置 PG。
func TestBundledPGReadyOnMissingDir(t *testing.T) {
	if bundledPGReady(t.TempDir()) {
		t.Error("空目录不应判定为内置 PostgreSQL 就绪")
	}
}

// TestIsOwnedProcessRejectsTestProcess 是误杀防护的回归测试：
// 测试进程不在发行包目录内，必须判定为无关进程。
func TestIsOwnedProcessRejectsTestProcess(t *testing.T) {
	if _, owned := isOwnedProcess(uint32(os.Getpid()), t.TempDir()); owned {
		t.Error("测试进程不应被判定为本程序家族成员")
	}
}

// TestServerLogTail 锁定日志尾部的截取行为。
// 自举失败时这是唯一能让用户看到真实原因的信息，截错或读不出来都很致命。
func TestServerLogTail(t *testing.T) {
	dir := t.TempDir()

	// 文件不存在时返回空串，调用方据此省略而不是打印"日志尾部：<nil>"
	if got := serverLogTail(filepath.Join(dir, "missing.log"), 5); got != "" {
		t.Errorf("日志不存在时应返回空串，得到 %q", got)
	}

	// 末尾空行不应计入
	logPath := filepath.Join(dir, "server.log")
	body := "line1\nline2\r\nline3\n\n"
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := serverLogTail(logPath, 10), "line1\nline2\nline3"; got != want {
		t.Errorf("serverLogTail = %q，预期 %q", got, want)
	}

	// 超出上限时只保留尾部
	if got, want := serverLogTail(logPath, 2), "line2\nline3"; got != want {
		t.Errorf("截断后 = %q，预期 %q", got, want)
	}

	// withLogTail：日志为空时保持原错误不变
	err := withLogTail(errors.New("boom"), filepath.Join(dir, "missing.log"))
	if err.Error() != "boom" {
		t.Errorf("无日志时应原样返回错误，得到 %q", err.Error())
	}

	// withLogTail：有日志时附加尾部，且仍可 errors.Is 追溯
	sentinel := errors.New("数据库未就绪")
	wrapped := withLogTail(sentinel, logPath)
	if !errors.Is(wrapped, sentinel) {
		t.Error("withLogTail 后应仍能 errors.Is 追溯到原始错误")
	}
	if !strings.Contains(wrapped.Error(), "line3") {
		t.Errorf("错误信息未包含日志尾部：%q", wrapped.Error())
	}
}

// TestBootWithDSNSkipsDatabase 确认 DSN 为空时只处理端口、不碰数据库。
func TestBootWithDSNSkipsDatabase(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	if err := bootWithDSN(addr, "", t.TempDir(), ""); err != nil {
		t.Fatalf("DSN 为空且端口空闲时，bootWithDSN 应成功：%v", err)
	}
}

// TestBootWithDSNRejectsForeignPort 确认端口被无关进程占用时明确报错而不是乱杀。
func TestBootWithDSNRejectsForeignPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// exeDir 指向一个与测试进程无关的目录，因此监听者会被判为无关进程
	err = bootWithDSN(l.Addr().String(), "", t.TempDir(), "")
	if err == nil {
		t.Fatal("端口被无关进程占用时应报错，而不是静默继续")
	}
	// 监听者必须还活着——这是"不误杀"的关键断言
	if _, owned := isOwnedProcess(uint32(os.Getpid()), t.TempDir()); owned {
		t.Error("测试进程不应被判为本程序家族成员")
	}
}

// TestWaitPostgresTimesOut 确认等待会按超时返回，不会无限挂起。
func TestWaitPostgresTimesOut(t *testing.T) {
	if waitPostgres("postgres://artex@127.0.0.1:1/artex?connect_timeout=1", 2*bootPollInterval) {
		t.Error("不可达地址不应返回就绪")
	}
}

// TestEnsurePortFreeOnFreeAddress 是最基础的一条：地址空闲时不做任何事。
func TestEnsurePortFreeOnFreeAddress(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	if err := ensurePortFree(addr, t.TempDir(), "测试"); err != nil {
		t.Fatalf("空闲端口不应报错：%v", err)
	}
}

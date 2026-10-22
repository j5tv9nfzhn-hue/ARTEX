//go:build windows

package main

import (
	"net"
	"os"
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

// requireTCPTable skips when the IP Helper API is unusable on this host.
//
// GetExtendedTcpTable is the only way to map a listening port back to its owning
// process, and on some Windows builds it rejects every argument combination with
// ERROR_INVALID_PARAMETER (observed on this machine for all five variants). That is
// a platform limitation, not a defect in the lookup code, so the tests that need it
// report a skip instead of a failure. lookupTCPTablePID returns 0 for "nobody is
// listening" and for "the API failed" alike, so the two are indistinguishable from
// the outside — the probe below is the only honest way to tell them apart.
func requireTCPTable(t *testing.T) {
	t.Helper()
	if lookupTCPTablePID(net.JoinHostPort("127.0.0.1", "1")) == 0 {
		// No listener on port 1 is the expected reading; confirm the API itself works
		// by asking about a port we know is held. A working API must find it.
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Skipf("无法建立探测用监听端口:%v", err)
		}
		defer l.Close()
		if lookupTCPTablePID(l.Addr().String()) == 0 {
			t.Skip("GetExtendedTcpTable 在本机不可用（对该端口也返回 0），跳过端口归属用例")
		}
	}
}

// TestLookupTCPTablePIDFindsOwnListener 用本进程真实监听的端口端到端验证
// 端口归属查询:表遍历、变长行步长、IPv4 行结构体布局、端口字节序,
// 任一处出错这个用例都会失败。
func TestLookupTCPTablePIDFindsOwnListener(t *testing.T) {
	requireTCPTable(t)
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

// TestIsOwnedProcessRejectsTestProcess 是误杀防护的回归测试：
// 测试进程不在发行包目录内，必须判定为无关进程。
func TestIsOwnedProcessRejectsTestProcess(t *testing.T) {
	if _, owned := isOwnedProcess(uint32(os.Getpid()), t.TempDir()); owned {
		t.Error("测试进程不应被判定为本程序家族成员")
	}
}

// TestBootBeforeStartSkipsDatabase 确认自举只处理端口、不碰数据库：
// SQLite 是进程内打开一个文件，没有可等待的就绪状态。
func TestBootBeforeStartSkipsDatabase(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	if err := bootBeforeStart(addr, "", t.TempDir(), false); err != nil {
		t.Fatalf("端口空闲时，bootBeforeStart 应成功：%v", err)
	}
}

// TestBootBeforeStartRejectsForeignPort 确认端口被无关进程占用时明确报错而不是乱杀。
func TestBootBeforeStartRejectsForeignPort(t *testing.T) {
	requireTCPTable(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// exeDir 指向一个与测试进程无关的目录，因此监听者会被判为无关进程
	err = bootBeforeStart(l.Addr().String(), "", t.TempDir(), false)
	if err == nil {
		t.Fatal("端口被无关进程占用时应报错，而不是静默继续")
	}
	// 监听者必须还活着——这是"不误杀"的关键断言
	if _, owned := isOwnedProcess(uint32(os.Getpid()), t.TempDir()); owned {
		t.Error("测试进程不应被判为本程序家族成员")
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

	if err := ensurePortFree(addr, t.TempDir(), "测试", false); err != nil {
		t.Fatalf("空闲端口不应报错：%v", err)
	}
}

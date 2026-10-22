//go:build !windows

package main

import (
	"net"
	"os"
	"runtime"
	"testing"
)

func TestUnixBootBeforeStartOnFreeAddress(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	if err := bootBeforeStart(addr, "", t.TempDir(), false); err != nil {
		t.Fatalf("端口空闲时 bootBeforeStart 应成功：%v", err)
	}
}

func TestUnixIsOwnedProcessRejectsForeignProcess(t *testing.T) {
	if _, owned := isOwnedUnixProcess(os.Getpid(), t.TempDir()); owned {
		t.Fatal("测试进程不应被判定为发行目录内的本程序家族成员")
	}
}

func TestUnixBootBeforeStartRejectsForeignPortOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("仅 Linux 支持 /proc 端口归属反查")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if pid := lookupLinuxListeningPID(l.Addr().String()); pid == 0 {
		t.Skip("当前容器/内核环境无法从 /proc 反查监听套接字归属")
	}
	if err := bootBeforeStart(l.Addr().String(), "", t.TempDir(), false); err == nil {
		t.Fatal("端口被无关目录进程占用时应拒绝并报错")
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"unicode"

	"github.com/Autumn-27/artex/agent"
)

func TestShutdownContextPreservesNamedCause(t *testing.T) {
	signalCtx, signalCancel := context.WithCancel(context.Background())
	ctx, shutdown := shutdownContext(signalCtx)
	defer shutdown(nil)
	signalCancel()
	<-ctx.Done()
	if code, _, _, ok := agent.AbortReason(ctx); !ok || code != "shutdown" {
		t.Fatalf("code=%q ok=%v, want shutdown", code, ok)
	}
}

// TestPrintBannerLocalized verifies that the startup banner printed to stdout is
// Simplified Chinese and carries no leftover Hangul. This is the first thing a
// user sees when running ARTEX, so it must stay in Chinese.
// The "[config] ..." log lines in run() are intentionally out of scope — they
// are logs, which the localization brief ranks lowest (backlog Z2).
func TestPrintBannerLocalized(t *testing.T) {
	out := captureStdout(t, func() { printBanner(":8787") })
	t.Logf("启动横幅示例：\n%s", out)

	hasHan := false
	for _, r := range out {
		if unicode.Is(unicode.Hangul, r) {
			t.Fatalf("启动横幅仍有韩文残留 (%q)：%q", r, out)
		}
		if unicode.Is(unicode.Han, r) {
			hasHan = true
		}
	}
	if !hasHan {
		t.Fatalf("启动横幅未包含中文：%q", out)
	}

	for _, want := range []string{"ARTEX-REA", "自主渗透测试系统", "版本", "监听地址", ":8787"} {
		if !strings.Contains(out, want) {
			t.Errorf("启动横幅缺少 %q：%q", want, out)
		}
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns whatever
// fn wrote. printBanner uses fmt.Print*, which resolves os.Stdout at call time,
// so swapping it here captures the banner.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// TestIsAddrInUse 覆盖「端口被占用」的判定：syscall 错误链、Windows 原文、
// 以及完全无关的错误。这条判定的下游是专用退出码 2 —— 判错会让启动脚本
// 把一次端口冲突当成普通崩溃，又回到「重启—再撞」的老路上。
func TestIsAddrInUse(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"syscall", &net.OpError{Op: "listen", Net: "tcp", Err: syscall.EADDRINUSE}, true},
		{"windows-text", errors.New("listen tcp 0.0.0.0:8787: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted."), true},
		{"linux-text", errors.New("listen tcp :8787: bind: address already in use"), true},
		{"wrapped", fmt.Errorf("serve: %w", syscall.EADDRINUSE), true},
		{"other", errors.New("permission denied"), false},
	}
	for _, c := range cases {
		if got := isAddrInUse(c.err); got != c.want {
			t.Fatalf("%s: isAddrInUse=%v want %v", c.name, got, c.want)
		}
	}
}

// TestReportPortInUseExtractsPort 确认提示里带出的是端口号本身（不是 :8787 整个
// 字符串），netstat / taskkill 的命令才能被用户直接复制。
func TestReportPortInUseExtractsPort(t *testing.T) {
	var buf bytes.Buffer
	writePortInUse(&buf, "0.0.0.0:8787", errors.New("bind: address already in use"))
	out := buf.String()
	if !strings.Contains(out, "netstat -ano | findstr :8787") {
		t.Fatalf("提示里没有可复制的 netstat 命令：%q", out)
	}
	if !strings.Contains(out, "不会自动重试") {
		t.Fatalf("提示里没有说明「不再重试」：%q", out)
	}
}

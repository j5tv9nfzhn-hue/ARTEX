package agent

import (
	"strings"
	"testing"
)

// TestWrapWindowsExitCode 固定 Windows 退出码包装的口径：真退出码由最后一条原生命令
// 决定，PS 5.1 的「写出错误记录就返回 1」不再影响判定。
func TestWrapWindowsExitCode(t *testing.T) {
	got := wrapWindowsExitCode(`curl.exe -s -o out.txt https://a/b`)
	want := "curl.exe -s -o out.txt https://a/b\nif ($null -eq $LASTEXITCODE) { exit 0 } else { exit $LASTEXITCODE }"
	if got != want {
		t.Fatalf("包装结果不符：\n got=%q\nwant=%q", got, want)
	}

	// 空白结尾要归一，避免在 PowerShell 里多出一个空语句。
	if got := wrapWindowsExitCode("dir  \r\n"); strings.HasSuffix(got, " \nif") || !strings.Contains(got, "dir\nif") {
		t.Fatalf("尾部空白未归一：%q", got)
	}
	// 空命令、显式续行结尾不改（拼上去会破坏语法）。
	if got := wrapWindowsExitCode("   "); strings.Contains(got, "LASTEXITCODE") {
		t.Fatalf("空命令不该包装：%q", got)
	}
	if got := wrapWindowsExitCode("echo a `"); strings.Contains(got, "LASTEXITCODE") {
		t.Fatalf("续行结尾不该包装：%q", got)
	}

	// 开关：ARTEX_WINDOWS_EXIT_WRAP=0 时逐字节还原。
	t.Setenv("ARTEX_WINDOWS_EXIT_WRAP", "0")
	if got := wrapWindowsExitCode("curl.exe -s https://a"); got != "curl.exe -s https://a" {
		t.Fatalf("关掉开关后应原样返回，实际 %q", got)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// --- 解析 ---

func TestGitRootFromExecPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{`E:/Git/mingw64/libexec/git-core`, `E:\Git`},
		{`E:\Git\mingw64\libexec\git-core`, `E:\Git`},
		{`C:/Program Files/Git/mingw32/libexec/git-core/`, `C:\Program Files\Git`},
		{`/usr/lib/git-core`, ``}, // 非 Windows 布局：不反推
		{``, ``},
	}
	for _, c := range cases {
		if got := gitRootFromExecPath(c.in); got != c.want {
			t.Errorf("gitRootFromExecPath(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestResolveShellPrefersRealInstallOverStoreAlias(t *testing.T) {
	storeAlias := `C:\Users\u\AppData\Local\Microsoft\WindowsApps\pwsh.exe`
	realPath := `C:\Program Files\PowerShell\7\pwsh.exe`
	deps := ResolveDeps{
		GOOS: "windows",
		Env: func(k string) string {
			if k == "LOCALAPPDATA" {
				return `C:\Users\u\AppData\Local`
			}
			return ""
		},
		LookPath: func(string) (string, error) { return storeAlias, nil }, // PATH 里先命中的是别名
		Exists:   func(p string) bool { return p == realPath || p == storeAlias },
		Probe: func(exe string, _ ShellMode) (string, error) {
			if exe == storeAlias {
				return "", errors.New("exit status 1312") // MSIX 别名 spawn 失败
			}
			return "7.4.2", nil
		},
	}
	spec, err := ResolveShell(ShellModePowerShell, deps)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if spec.Bin != realPath {
		t.Fatalf("应优先真实安装路径，得到 %q", spec.Bin)
	}
	if spec.Flavor != "pwsh" || spec.Major() != 7 {
		t.Fatalf("flavor/version 判定错：%+v", spec)
	}
}

func TestResolveShellOverrideFailureIsErrorNotFallback(t *testing.T) {
	deps := ResolveDeps{
		GOOS:     "windows",
		Env:      func(string) string { return "" },
		LookPath: func(string) (string, error) { return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil },
		Exists:   func(string) bool { return true },
		Probe: func(exe string, _ ShellMode) (string, error) {
			if strings.Contains(exe, "broken") {
				return "", errors.New("拒绝访问")
			}
			return "5.1", nil
		},
		Override: `C:\broken\pwsh.exe`,
	}
	// 用户显式指定的路径跑不起来 → 必须报错。绝不能「悄悄改用 powershell.exe」。
	_, err := ResolveShell(ShellModePowerShell, deps)
	if err == nil || !errors.Is(err, ErrShellNotAvailable) {
		t.Fatalf("显式覆盖失败应返回 ErrShellNotAvailable，得到 %v", err)
	}
}

func TestResolveShellFindsBashViaGitExecPath(t *testing.T) {
	want := `E:\Git\bin\bash.exe`
	deps := ResolveDeps{
		GOOS:     "windows",
		Env:      func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", errors.New("not found") }, // bash 不在 PATH
		Exists:   func(p string) bool { return p == want },
		Probe:    func(string, ShellMode) (string, error) { return "5.3.15", nil },
		GitExecPath: func() (string, error) {
			return `E:/Git/mingw64/libexec/git-core`, nil
		},
	}
	spec, err := ResolveShell(ShellModeBash, deps)
	if err != nil {
		t.Fatalf("resolve bash: %v", err)
	}
	if spec.Bin != want {
		t.Fatalf("应通过 git --exec-path 反推到 %q，得到 %q", want, spec.Bin)
	}
}

func TestResolveShellRejectsCrossPlatformMode(t *testing.T) {
	_, err := ResolveShell(ShellModeCmd, ResolveDeps{GOOS: "linux"})
	if !errors.Is(err, ErrShellModeUnsupported) {
		t.Fatalf("Linux 上选 cmd 应报 ErrShellModeUnsupported，得到 %v", err)
	}
}

func TestParseShellModeHasNoAuto(t *testing.T) {
	if _, ok := ParseShellMode("auto"); ok {
		t.Fatal("auto 必须是非法值（用户明确否决）")
	}
	for _, v := range []string{"powershell", "CMD", " bash "} {
		if _, ok := ParseShellMode(v); !ok {
			t.Fatalf("%q 应合法", v)
		}
	}
}

// --- 编码 ---

func TestEncodePowerShellScriptHasBOM(t *testing.T) {
	got := encodePowerShellScript("Write-Output 'hi'")
	if len(got) < 3 || got[0] != 0xEF || got[1] != 0xBB || got[2] != 0xBF {
		t.Fatalf(".ps1 必须带 UTF-8 BOM，得到 %x", got[:3])
	}
	if strings.Contains(string(got[3:]), "\n") && !strings.Contains(string(got[3:]), "\r\n") {
		t.Fatal(".ps1 必须是 CRLF")
	}
}

func TestEncodeBatchScriptHasNoBOM(t *testing.T) {
	got := encodeBatchScript("@echo off\necho hi")
	if len(got) >= 3 && got[0] == 0xEF && got[1] == 0xBB && got[2] == 0xBF {
		// BOM 会被并入首行，`@echo off` 变成 `ï»¿@echo off`，第一条命令直接失败。
		t.Fatal(".bat 绝不能带 BOM")
	}
	s := string(got)
	if !strings.HasPrefix(s, "chcp 65001>nul\r\n") {
		t.Fatalf(".bat 首行应是 chcp 65001>nul，得到 %q", s)
	}
	if !strings.Contains(s, "@echo off\r\n") {
		t.Fatalf("应被归一成 CRLF，得到 %q", s)
	}
}

func TestWriteBatchScriptIdempotentChcp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bat")
	if err := writeBatchScript(p, "@echo off\nchcp 65001>nul\necho ok"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Count(string(raw), "chcp 65001") != 1 {
		t.Fatalf("不应重复插入 chcp：%q", string(raw))
	}
}

// --- 契约 ---

func withSpec(t *testing.T, spec ShellSpec, fn func()) {
	t.Helper()
	prev := CurrentShellSpec()
	SetShellSpec(spec)
	defer SetShellSpec(prev)
	fn()
}

func TestContractIsVersionAware(t *testing.T) {
	withSpec(t, ShellSpec{Mode: ShellModePowerShell, Bin: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, Version: "5.1", Flavor: "windows-powershell"}, func() {
		block := shellContractBlock()
		if !strings.Contains(block, "不支持 && / ||") {
			t.Fatal("5.1 契约必须明确说明不支持 &&")
		}
		if !strings.Contains(block, "curl.exe") {
			t.Fatal("5.1 契约必须提醒 curl 要写 curl.exe")
		}
	})
	withSpec(t, ShellSpec{Mode: ShellModePowerShell, Bin: `C:\Program Files\PowerShell\7\pwsh.exe`, Version: "7.4.2", Flavor: "pwsh"}, func() {
		block := shellContractBlock()
		if strings.Contains(block, "不支持 && / ||") {
			t.Fatal("7 的契约不该说「不支持 &&」——那会让模型绕远路")
		}
		if !strings.Contains(block, "支持 && 与 ||") {
			t.Fatal("7 的契约应说明支持 &&")
		}
	})
}

func TestContractForCmdDoesNotMentionPowerShellSyntax(t *testing.T) {
	withSpec(t, ShellSpec{Mode: ShellModeCmd, Bin: `C:\Windows\System32\cmd.exe`, Flavor: "cmd"}, func() {
		block := shellContractBlock()
		if strings.Contains(block, "PowerShell") {
			t.Fatal("cmd 契约不该再讲 PowerShell 语法")
		}
		if !strings.Contains(block, "2>nul") {
			t.Fatal("cmd 契约应给出 2>nul 写法")
		}
	})
}

// --- 执行器（只测本平台能跑的部分） ---

func TestRunShellBashOneShot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 bash 验证；Windows 下由 PowerShell 路径覆盖")
	}
	spec, err := ResolveShellWithOverride(ShellModeBash, "/bin/bash")
	if err != nil {
		t.Skipf("本机无 bash：%v", err)
	}
	SetShellSpec(spec)
	out := runShell(context.Background(), shellRequest{Command: "echo hello; echo oops 1>&2", Timeout: 10 * time.Second})
	if out.Err != nil {
		t.Fatalf("run: %v", out.Err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit=%d out=%q", out.ExitCode, out.Output)
	}
	if !strings.Contains(out.Output, "hello") || !strings.Contains(out.Output, "oops") {
		t.Fatalf("stdout/stderr 都应合并返回，得到 %q", out.Output)
	}
}

func TestRunShellPropagatesExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash 专用")
	}
	spec, err := ResolveShellWithOverride(ShellModeBash, "/bin/bash")
	if err != nil {
		t.Skipf("本机无 bash：%v", err)
	}
	SetShellSpec(spec)
	out := runShell(context.Background(), shellRequest{Command: "exit 7", Timeout: 10 * time.Second})
	if out.Err != nil || out.ExitCode != 7 {
		t.Fatalf("退出码必须来自命令本身，得到 code=%d err=%v", out.ExitCode, out.Err)
	}
}

func TestRunShellTimeoutKillsAndClassifies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash 专用")
	}
	spec, err := ResolveShellWithOverride(ShellModeBash, "/bin/bash")
	if err != nil {
		t.Skipf("本机无 bash：%v", err)
	}
	SetShellSpec(spec)
	out := runShell(context.Background(), shellRequest{Command: "sleep 5", Timeout: 300 * time.Millisecond})
	if !out.TimedOut {
		t.Fatalf("应判定超时，得到 %+v", out)
	}
	if got := ClassifyShellFailure(out); got != ErrClassTimeout {
		t.Fatalf("应归为 timeout，得到 %q", got)
	}
}

func TestRunShellConfigErrorWhenUnresolved(t *testing.T) {
	prev := CurrentShellSpec()
	SetShellResolveError(errors.New("没有可用的 shell"))
	defer SetShellSpec(prev)
	out := runShell(context.Background(), shellRequest{Command: "echo hi", Timeout: time.Second})
	if out.Err == nil {
		t.Fatal("未解析时必须直接报错")
	}
	if got := ClassifyShellFailure(out); got != ErrClassConfig {
		t.Fatalf("应归为 config，得到 %q", got)
	}
}

// --- 错误归类 ---

func TestClassifyShellFailureTable(t *testing.T) {
	cases := []struct {
		name string
		out  shellOutcome
		want string
	}{
		{"dialect-amp", shellOutcome{Output: "标记“&&”不是此版本中的有效语句分隔符。", ExitCode: 1}, ErrClassDialect},
		{"dialect-cmdlet", shellOutcome{Output: "grep : The term 'grep' is not recognized as the name of a cmdlet", ExitCode: 1}, ErrClassDialect},
		{"param", shellOutcome{Output: "error: unrecognized arguments: --foo", ExitCode: 2}, ErrClassParam},
		{"perm", shellOutcome{Output: "Permission denied", ExitCode: 1}, ErrClassPerm},
		{"notfound", shellOutcome{Output: "cat: no such file or directory", ExitCode: 1}, ErrClassNotFound},
		{"env", shellOutcome{Output: "'python' is not recognized", ExitCode: 9009}, ErrClassEnv},
		{"transport", shellOutcome{Output: "curl: (7) Failed to connect: connection refused"}, ErrClassTransport},
		{"plain-fail", shellOutcome{Output: "FAIL: TestFoo\n--- FAIL", ExitCode: 1}, errClassNone},
	}
	for _, c := range cases {
		if got := ClassifyShellFailure(c.out); got != c.want {
			t.Errorf("%s: 得到 %q want %q", c.name, got, c.want)
		}
	}
}

func TestErrorClassOfTransportNotBlamedOnTool(t *testing.T) {
	// 网络层失败不该被算成「工具不好用」——这是错误率口径的核心分歧点。
	res := actool.Errorf(`Get "https://x": EOF`)
	if got := ErrorClassOf(res, nil, context.Background()); got != ErrClassTransport {
		t.Fatalf("得到 %q", got)
	}
}

func TestErrorClassOfUnknownToolIsItsOwnClass(t *testing.T) {
	// worker 被裁掉了 Write，模型照着旧文案去调，SDK 只回一句 unknown tool。
	// 这类失败既不是参数写错也不是环境缺依赖，必须单独成类。
	res := actool.Errorf(`Error: unknown tool "Write"`)
	if got := ErrorClassOf(res, nil, context.Background()); got != ErrClassUnknownTool {
		t.Fatalf("得到 %q", got)
	}
}

func TestErrorClassOfParamNotEmpty(t *testing.T) {
	// 非 Shell 工具的参数契约错误过去被一律记成 env，把「调用方参数写错」误诊成
	// 「机器缺依赖」——list_assets 的「dsl 不能为空」就是这么被算错的。
	err := errors.New("未传 id/ids 时 dsl 不能为空：不允许无条件查询全部资产")
	if got := ErrorClassOf(actool.Result{}, err, context.Background()); got != ErrClassParam {
		t.Fatalf("得到 %q", got)
	}
}

// TestBashToolWiring 覆盖「工具层 → 执行器」这条接线本身。
//
// 这一层最容易出错而不被单元测试发现：执行器单独测是好的，但工具若还接在 SDK 的
// Bash 上（走 norma 的 shellCmd），换执行器就等于没生效——历史实现只在 worker 里
// 包装过，正是这个形态。所以这里从 WrapCrossPlatformBash 进、从 Result 出。
func TestBashToolWiring(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 bash 验证接线；Windows 下由 PowerShell 路径覆盖")
	}
	spec, err := ResolveShellWithOverride(ShellModeBash, "/bin/bash")
	if err != nil {
		t.Skipf("本机无 bash：%v", err)
	}
	SetShellSpec(spec)

	tool := WrapCrossPlatformBash([]actool.CoreTool{actool.NewBash()}, "", nil)[0]
	if tool.Name() != "Bash" {
		t.Fatalf("工具名必须保持 Bash，得到 %q", tool.Name())
	}
	if !strings.Contains(tool.Description(), "bash") {
		t.Fatalf("描述里应带上执行器契约，得到 %q", tool.Description())
	}

	res, err := tool.Call(context.Background(), json.RawMessage(`{"command":"echo wired-ok"}`), &actool.ToolContext{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError || !strings.Contains(res.Flatten(), "wired-ok") {
		t.Fatalf("输出不对：err=%v out=%q", res.IsError, res.Flatten())
	}

	// 非零退出码必须变成 IsError，且带上真实退出码。
	res, err = tool.Call(context.Background(), json.RawMessage(`{"command":"echo boom; exit 9"}`), &actool.ToolContext{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Flatten(), "exit code 9") {
		t.Fatalf("非零退出应报错并带退出码，得到 err=%v out=%q", res.IsError, res.Flatten())
	}
}

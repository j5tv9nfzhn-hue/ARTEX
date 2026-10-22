package agent

import (
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// TestEnsureUTF8Output 固定「命令输出回读」的口径：合法 UTF-8 原样返回，GBK 与
// UTF-16LE 还原成中文，实在认不出也要保证是合法 UTF-8（不能把非法字节送进 JSON）。
func TestEnsureUTF8Output(t *testing.T) {
	const want = "中文输出 mixed ASCII 123"

	if got := ensureUTF8Output(want); got != want {
		t.Fatalf("合法 UTF-8 被改动：%q", got)
	}
	if got := ensureUTF8Output(""); got != "" {
		t.Fatalf("空串应原样返回，实际 %q", got)
	}

	// 中文 Windows 的 OEM/ANSI 输出：GBK 字节直接塞进 Go 字符串就不合法了。
	gbk, _, err := transform.String(simplifiedchinese.GBK.NewEncoder(), want)
	if err != nil {
		t.Fatalf("构造 GBK 样本失败：%v", err)
	}
	if utf8.ValidString(gbk) {
		t.Fatalf("样本本身是合法 UTF-8，测不到问题：%q", gbk)
	}
	if got := ensureUTF8Output(gbk); got != want {
		t.Fatalf("GBK 还原失败：got=%q want=%q", got, want)
	}

	// PowerShell 重定向有时产出 UTF-16LE + BOM。
	units := utf16.Encode([]rune(want))
	buf := []byte{0xFF, 0xFE}
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	if got := ensureUTF8Output(string(buf)); got != want {
		t.Fatalf("UTF-16LE 还原失败：got=%q want=%q", got, want)
	}

	// 认不出的字节串：至少要是合法 UTF-8，否则写入 JSON/SQLite 会变成 U+FFFD。
	got := ensureUTF8Output("\xff\xfe\x00garbage\x81")
	if !utf8.ValidString(got) {
		t.Fatalf("输出仍不是合法 UTF-8：%q", got)
	}
}

// TestUtf8ResultKeepsNonTextBlocks 确认只清洗文本块，其它块（工具调用等）原样保留。
func TestUtf8ResultKeepsNonTextBlocks(t *testing.T) {
	in := actool.Result{Content: []llm.ContentBlock{
		llm.TextBlock("caf\u00e9"),
		{Type: llm.BlockToolUse, ID: "x", Name: "Bash"},
	}}
	out := utf8Result(in)
	if len(out.Content) != 2 {
		t.Fatalf("块数变了：%d", len(out.Content))
	}
	if out.Content[0].Text != "caf\u00e9" {
		t.Fatalf("文本块被改动：%q", out.Content[0].Text)
	}
	if out.Content[1].Type != llm.BlockToolUse || out.Content[1].ID != "x" {
		t.Fatalf("非文本块被改动：%+v", out.Content[1])
	}
	if got := resultText(out); got != "caf\u00e9" {
		t.Fatalf("resultText 拼接不符：%q", got)
	}
}

// TestMergeUTF8Env 固定子进程 UTF-8 环境的注入口径：只在 Windows+开关打开时补，
// 且不覆盖会话里显式配置过的键。
func TestMergeUTF8Env(t *testing.T) {
	got := mergeUTF8Env(nil, "windows", true)
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "PYTHONUTF8=1") || !strings.Contains(joined, "PYTHONIOENCODING=utf-8") {
		t.Fatalf("Windows 上应补 UTF-8 环境，实际 %v", got)
	}

	// 显式配置优先：已有键不重复、不覆盖。
	got = mergeUTF8Env([]string{"PYTHONUTF8=0", "PATH=C:\\x"}, "windows", true)
	adds := 0
	for _, kv := range got {
		if strings.EqualFold(kv, "PYTHONUTF8=1") {
			adds++
		}
		if kv == "PYTHONUTF8=0" && adds > 0 {
			t.Fatalf("覆盖了会话显式配置：%v", got)
		}
	}
	if adds != 0 {
		t.Fatalf("已有 PYTHONUTF8 时不该再追加：%v", got)
	}
	if len(got) != 3 || got[2] != "PYTHONIOENCODING=utf-8" {
		t.Fatalf("只该补缺失的键：%v", got)
	}

	if got := mergeUTF8Env(nil, "linux", true); len(got) != 0 {
		t.Fatalf("非 Windows 不应注入：%v", got)
	}
	if got := mergeUTF8Env(nil, "windows", false); len(got) != 0 {
		t.Fatalf("开关关闭时不应注入：%v", got)
	}
}

// TestDialectHintFor 固定「写法不对」的识别与提示口径。
func TestDialectHintFor(t *testing.T) {
	const psErr = "Select-String : 无法将“grep”项识别为 cmdlet、函数、脚本文件或可运行程序的名称。"

	hint := dialectHintFor(psErr, true, "windows")
	if hint == "" || !strings.Contains(hint, "2>$null") {
		t.Fatalf("应当给出 PowerShell 写法提示，实际 %q", hint)
	}
	if got := dialectHintFor(psErr, false, "windows"); got != "" {
		t.Fatalf("非错误结果不该加提示：%q", got)
	}
	if got := dialectHintFor(psErr, true, "linux"); got != "" {
		t.Fatalf("非 Windows 平台不该加提示：%q", got)
	}
	if got := dialectHintFor("Error: 404 Not Found", true, "windows"); got != "" {
		t.Fatalf("与写法无关的失败不该加提示：%q", got)
	}
	// 英文报错同样要认出来。
	if got := dialectHintFor("'grep' is not recognized as the name of a cmdlet", true, "windows"); got == "" {
		t.Fatalf("英文报错未识别")
	}
}

// TestWrapWindowsCommand 固定 Windows 命令组装顺序：UTF-8 前导 → 命令 → 退出码收尾。
func TestWrapWindowsCommand(t *testing.T) {
	t.Setenv("ARTEX_WINDOWS_UTF8", "1")
	t.Setenv("ARTEX_WINDOWS_EXIT_WRAP", "1")
	got := wrapWindowsCommand("dir")
	if !strings.HasPrefix(got, powershellUTF8Preamble) {
		t.Fatalf("缺少 UTF-8 前导：%q", got)
	}
	if !strings.Contains(got, "\ndir\nif ($null -eq $LASTEXITCODE)") {
		t.Fatalf("命令与退出码收尾顺序不对：%q", got)
	}

	// 关掉前导后仍保留退出码收尾（兼容旧行为）。
	t.Setenv("ARTEX_WINDOWS_UTF8", "0")
	got = wrapWindowsCommand("dir")
	if strings.Contains(got, powershellUTF8Preamble) {
		t.Fatalf("开关关闭时不该有前导：%q", got)
	}
	if !strings.Contains(got, "LASTEXITCODE") {
		t.Fatalf("退出码收尾丢了：%q", got)
	}
}

// TestShellContractMentionsExecutor 保证工具自述里一定点明执行器（模型的误判源头）。
func TestShellContractMentionsExecutor(t *testing.T) {
	if line := shellContractLine(); !strings.Contains(line, "执行") {
		t.Fatalf("一句话契约没提执行器：%q", line)
	}
	if block := shellContractBlock(); !strings.Contains(block, "执行器") {
		t.Fatalf("详细契约没提执行器：%q", block)
	}
}

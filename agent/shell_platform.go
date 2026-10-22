package agent

// 本文件只解决两件事，都是「命令在 Windows 上不稳定」的直接来源：
//
//  1. 模型不知道自己在跟谁说话。SDK 把这条工具叫做 Bash，描述里写的是
//     "runs through the shell"，而 Windows 上真正执行它的是 Windows PowerShell
//     5.1。模型只好凭直觉写 bash（2>/dev/null、export、grep、heredoc），失败后
//     再猜着换写法——这正是「AI 经常误会 PowerShell」的样子。契约必须写在工具上
//     （Description/Prompt 随 schema 一起下发），而不是只写在系统提示的某个角落。
//
//  2. 编码。中文 Windows 上 PowerShell 与原生程序的管道默认不是 UTF-8：输出是
//     GBK/OEM 字节，进 Go 字符串就是非法 UTF-8，落库/送模型时被替换成 U+FFFD，
//     模型看到的是乱码——它不会认为「编码错了」，只会认为命令失败，于是再换写法
//     重试。这里做三件事：给 PowerShell 加 UTF-8 前导、给子进程补 UTF-8 环境变量、
//     回读时对 GBK/UTF-16LE 做兜底解码。
//
// 开关：ARTEX_WINDOWS_UTF8=0 可整体关掉前导与子进程环境（排查时用），
// 输出兜底解码始终开启（它只在「本来就不是合法 UTF-8」时才动手）。

import (
	"os"
	"runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// powershellUTF8Preamble 是每条 Windows 命令前的前导：把管道两端的编码钉死成
// UTF-8（$OutputEncoding 决定「传给原生命令的参数」怎么编码，Console::OutputEncoding
// 决定「原生命令输出」怎么解码，chcp 65001 让子进程按 UTF-8 读写）。
// 末尾的 chcp 输出被丢弃，避免污染命令结果。
const powershellUTF8Preamble = "[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); " +
	"[Console]::InputEncoding=[Text.UTF8Encoding]::new($false); " +
	"$OutputEncoding=[Text.UTF8Encoding]::new($false); " +
	"$ProgressPreference='SilentlyContinue'; " +
	"chcp 65001 > $null;"

// windowsUTF8Enabled 是前导与子进程 UTF-8 环境的开关（默认开）。
func windowsUTF8Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ARTEX_WINDOWS_UTF8"))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// wrapWindowsCommand 组装 Windows 上最终要交给 PowerShell 的命令：
// UTF-8 前导 → 原命令 → 退出码收尾（见 wrapWindowsExitCode）。
func wrapWindowsCommand(cmd string) string {
	body := strings.TrimRight(cmd, " \t\r\n")
	if body == "" {
		return cmd
	}
	if windowsUTF8Enabled() {
		body = powershellUTF8Preamble + "\n" + body
	}
	return wrapWindowsExitCode(body)
}

// ensureUTF8Output 把一段命令输出转成合法 UTF-8。
//
// 只在字符串本身非法时才动手，因此对已经是 UTF-8 的输出零影响：
//   - UTF-16LE（PowerShell 重定向 / -OutputFormat 会产出）按 BOM 识别；
//   - 其余按 GB18030 解（GBK 的超集，覆盖中文 Windows 的 OEM/ANSI 输出）；
//   - 都失败才降级成 U+FFFD 替换，至少保证能进 JSON。
func ensureUTF8Output(s string) string {
	if s == "" || utf8.ValidString(s) {
		return s
	}
	b := []byte(s)
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		if dec, ok := decodeUTF16LE(b[2:]); ok {
			return dec
		}
	}
	if dec, ok := decodeGB18030(b); ok {
		return dec
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

func decodeUTF16LE(b []byte) (string, bool) {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	s := string(utf16.Decode(u))
	if !utf8.ValidString(s) {
		return "", false
	}
	return s, true
}

func decodeGB18030(b []byte) (string, bool) {
	out, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), b)
	if err != nil || !utf8.Valid(out) {
		return "", false
	}
	return string(out), true
}

// mergeChildUTF8Env 给 Bash 派生的子进程补 UTF-8 环境（Python 系最常踩：默认按
// 本地代码页读写 stdio）。已显式配置的键不覆盖——会话 env / 任务 env 优先。
func mergeChildUTF8Env(env []string) []string {
	return mergeUTF8Env(env, runtime.GOOS, windowsUTF8Enabled())
}

// mergeUTF8Env 是可测的纯函数版本：goos 与开关显式传入。
func mergeUTF8Env(env []string, goos string, enabled bool) []string {
	if goos != "windows" || !enabled {
		return env
	}
	have := make(map[string]bool, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			have[strings.ToUpper(kv[:i])] = true
		}
	}
	for _, kv := range []string{"PYTHONUTF8=1", "PYTHONIOENCODING=utf-8"} {
		key := strings.ToUpper(kv[:strings.IndexByte(kv, '=')])
		if !have[key] {
			env = append(env, kv)
		}
	}
	return env
}

// utf8Result 把工具结果里的文本块统一转成合法 UTF-8：命令输出是 GBK/UTF-16LE 时
// 在这里还原，模型与数据库都不会再看到 U+FFFD。
func utf8Result(res actool.Result) actool.Result {
	out := make([]llm.ContentBlock, len(res.Content))
	for i, blk := range res.Content {
		if blk.Type == llm.BlockText && blk.Text != "" {
			blk.Text = ensureUTF8Output(blk.Text)
		}
		out[i] = blk
	}
	res.Content = out
	return res
}

// resultText 拼接结果里的文本块，供错误分类与写法提示判断使用。
func resultText(res actool.Result) string {
	var b strings.Builder
	for _, blk := range res.Content {
		if blk.Type == llm.BlockText && blk.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// shellContractLine 是随工具描述下发的「一句话契约」：成本一行，收益是模型不必再猜。
func shellContractLine() string {
	switch runtime.GOOS {
	case "windows":
		return "本机是 Windows：命令由 Windows PowerShell 5.1 执行（powershell -NoProfile -NonInteractive -Command），" +
			"不是 bash 也不是 cmd——必须按 PowerShell 语法书写（详见工具说明）；写文件用 -Encoding utf8。"
	case "darwin":
		return "本机是 macOS：命令由 bash -c 执行，系统命令行工具是 BSD 变体而非 GNU。"
	default:
		return "命令由 bash -c 执行。"
	}
}

// shellContractBlock 是详细契约，挂在 Prompt 上（随 schema 一起给到模型）。
func shellContractBlock() string {
	switch runtime.GOOS {
	case "windows":
		return strings.Join([]string{
			"【执行器：Windows PowerShell 5.1（powershell -NoProfile -NonInteractive -Command）】",
			"- 重定向：2>/dev/null → 2>$null；>/dev/null → >$null；没有 heredoc（<<EOF），改用 @' ... '@ here-string 或先写文件。",
			"- 环境变量：VAR=x cmd → $env:VAR='x'; cmd；当前进程用 $env:VAR 读。",
			"- 串联：PS 5.1 不支持 && / ||，用 `a; if ($?) { b }` 或分行执行；分号总是安全。",
			"- 不要用 Unix 专属命令：grep→Select-String、sed→-replace、awk→ForEach-Object、head/tail→Select-Object -First/-Last、",
			"  cat→Get-Content、ls→Get-ChildItem、which→Get-Command、touch→New-Item、cp/mv→Copy-Item/Move-Item、rm -rf→Remove-Item -Recurse -Force。",
			"- curl 必须写 curl.exe（PowerShell 的 curl 是 Invoke-WebRequest 别名）；含 & 的 URL 要用双引号包住。",
			"- 引号：单引号里不展开变量，需要展开用双引号；反引号是转义符（不是命令替换），命令替换用 $(...)。",
			"- 编码：平台已把管道强制成 UTF-8；写文件显式 -Encoding utf8，读非 UTF-8 文件用 Get-Content -Encoding。输出若出现乱码，按编码问题处理，不要当成命令失败重试。",
			"- 复杂逻辑不要猜语法：改用 python -c \"...\" 一行脚本，或内置原生工具（http_request / http_fuzz / codec_crypto / extract_data / Grep / Glob / Read / Edit）。",
		}, "\n")
	case "darwin":
		return strings.Join([]string{
			"【执行器：bash -c（macOS，系统工具为 BSD 变体）】",
			"- grep 不支持 -P，用 -E 或内置 Grep 工具；sed -i 必须跟备份后缀（sed -i ''）；stat 用 -f 而不是 -c。",
			"- 二进制/头部检查优先用内置工具，而不是拼 strings/objdump 管道。",
		}, "\n")
	default:
		return "【执行器：bash -c】命令按 POSIX/bash 语法书写。"
	}
}

// windowsDialectSignals 是「写法不对」的典型报错片段（中英文各覆盖常见措辞）。
// 只用来决定「要不要追加一条写法提示」，不改变命令本身。
var windowsDialectSignals = []string{
	"不是内部或外部命令",
	"无法将",
	"is not recognized as the name of a cmdlet",
	"CommandNotFoundException",
	"意外的标记",
	"unexpected token",
	"missing closing",
	"缺少",
	"the ampersand operator",
	"the '<' operator is reserved",
	"cannot find path",
	"找不到路径",
}

// windowsDialectHint 在命令因为「shell 写法不对」而失败时，把正确写法直接塞回给
// 模型。目的是打断「换个写法再猜一次」的循环：告诉它是执行器不匹配，而不是目标
// 不存在或工具坏了。
func windowsDialectHint(out string, isError bool) string {
	return dialectHintFor(out, isError, runtime.GOOS)
}

// dialectHintFor 是可测的纯函数版本：goos 显式传入。
func dialectHintFor(out string, isError bool, goos string) string {
	if goos != "windows" || !isError || out == "" {
		return ""
	}
	lower := strings.ToLower(out)
	hit := false
	for _, sig := range windowsDialectSignals {
		if strings.Contains(lower, strings.ToLower(sig)) {
			hit = true
			break
		}
	}
	if !hit {
		return ""
	}
	return strings.Join([]string{
		"【写法提示（不是命令/目标的问题）】本机 Bash 工具的执行器是 Windows PowerShell 5.1，不是 bash/cmd。这条多半是写法不对：",
		"- 2>/dev/null → 2>$null；>/dev/null → >$null",
		"- VAR=x cmd → $env:VAR='x'; cmd",
		"- a && b → a; if ($?) { b }（PS 5.1 不支持 &&）",
		"- grep/sed/awk/head/tail/cat/ls/which/touch 换成 Select-String / -replace / ForEach-Object / Select-Object -First / Get-Content / Get-ChildItem / Get-Command / New-Item",
		"- curl 写成 curl.exe；含 & 的 URL 加双引号",
		"拿不准就别猜：改用 python -c \"...\" 或内置原生工具。",
	}, "\n")
}

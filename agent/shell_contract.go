package agent

// 执行器契约（任务 26 步骤 ③）。
//
// 契约必须按「模式 + 版本」生成，不能只写一句「本机是 Windows，用 PowerShell」：
//
//	PS 7 (pwsh)     支持 && / ||、默认 UTF-8、`>` 写 UTF-8
//	PS 5.1          **不支持** && / ||、`>` 默认写 UTF-16LE、curl 是别名
//	cmd.exe         支持 && / ||、无 grep/sed/awk、重定向语法不同
//	Git Bash        全 POSIX，路径是 /e/Git 形式，Windows 盘符要 /c
//
// 写错的代价是双向的：教模型用 5.1 没有的 &&，它会写出一条必然失败的命令；
// 把 7 当成 5.1 教，它会长年绕远路。所以这里按 spec.Flavor 分派。

import (
	"fmt"
	"strings"
)

// shellContractLine 是随工具描述下发的「一句话契约」：成本一行，收益是模型不必再猜。
func shellContractLine() string {
	spec := CurrentShellSpec()
	switch spec.Mode {
	case ShellModePowerShell:
		return fmt.Sprintf("本机 Bash 工具的执行器是 %s（%s，不是 bash 也不是 cmd）——命令必须按 PowerShell 语法书写（详见工具说明）。",
			powershellDisplayName(spec), spec.Bin)
	case ShellModeCmd:
		return fmt.Sprintf("本机 Bash 工具的执行器是 cmd.exe（%s），不是 bash 也不是 PowerShell——命令必须按 cmd 语法书写（详见工具说明）。", spec.Bin)
	case ShellModeBash:
		if spec.Version != "" {
			return fmt.Sprintf("本机 Bash 工具的执行器是 bash %s（%s），按 POSIX/bash 语法书写。", spec.Version, spec.Bin)
		}
		return "本机 Bash 工具的执行器是 bash，按 POSIX/bash 语法书写。"
	default:
		// 还没解析出执行器：如实说明，别让模型以为自己在 bash 里。
		return "注意：本机 Shell 执行器尚未解析成功，Bash 调用会直接报错（详见工具说明），请改用内置原生工具完成同样的事。"
	}
}

func powershellDisplayName(spec ShellSpec) string {
	if spec.Flavor == "pwsh" || spec.Major() >= 6 {
		return "PowerShell " + nonEmpty(spec.Version, "7")
	}
	return "Windows PowerShell " + nonEmpty(spec.Version, "5.1")
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// shellContractBlock 是详细契约，挂在 Prompt 上（随 schema 一起给到模型）。
func shellContractBlock() string {
	spec := CurrentShellSpec()
	switch spec.Mode {
	case ShellModePowerShell:
		if spec.Major() >= 6 || spec.Flavor == "pwsh" {
			return strings.Join([]string{
				"【执行器：" + powershellDisplayName(spec) + "（" + spec.Bin + "）】",
				"- 串联：支持 && 与 ||（PS 7 起）。需要兼容性时仍可用 `a; if ($?) { b }`。",
				"- 重定向：2>/dev/null → 2>$null；>/dev/null → >$null；没有 heredoc，用 @' ... '@ here-string 或先写文件。",
				"- 环境变量：VAR=x cmd → $env:VAR='x'; cmd；读取用 $env:VAR。",
				"- 不要用 Unix 专属命令：grep→Select-String、sed→-replace、awk→ForEach-Object、head/tail→Select-Object -First/-Last、cat→Get-Content、ls→Get-ChildItem、which→Get-Command。",
				"- curl 优先写 curl.exe（避免不同环境里被别名为 Invoke-WebRequest）；含 & 的 URL 用双引号包住。",
				"- 编码：平台已把管道强制为 UTF-8；写文件用 -Encoding utf8，读非 UTF-8 文件显式给 -Encoding。输出出现乱码按编码问题处理，不要当成命令失败重试。",
				"- 复杂逻辑不要猜语法：改用 python -c \"...\" 或 Read / Grep / Glob / Edit 这类文件工具。",
			}, "\n")
		}
		return strings.Join([]string{
			"【执行器：" + powershellDisplayName(spec) + "（" + spec.Bin + "）】",
			"- 串联：**不支持 && / ||**（5.1 会报「意外的标记」）。用 `a; if ($?) { b }`，或分行写。分号总是安全。",
			"- 重定向：2>/dev/null → 2>$null；>/dev/null → >$null；没有 heredoc，用 @' ... '@ here-string 或先写文件。",
			"- 环境变量：VAR=x cmd → $env:VAR='x'; cmd；读取用 $env:VAR。",
			"- 不要用 Unix 专属命令：grep→Select-String、sed→-replace、awk→ForEach-Object、head/tail→Select-Object -First/-Last、cat→Get-Content、ls→Get-ChildItem、which→Get-Command、touch→New-Item、cp/mv→Copy-Item/Move-Item、rm -rf→Remove-Item -Recurse -Force。",
			"- curl 必须写 curl.exe（PS 5.1 的 curl 是 Invoke-WebRequest 别名）；含 & 的 URL 用双引号包住。",
			"- 编码：平台已把管道强制为 UTF-8；**`>` 和 Out-File 默认写 UTF-16LE**，写文本文件显式 `-Encoding utf8`。输出出现乱码按编码问题处理，不要当成命令失败重试。",
			"- 引号：单引号不展开变量，要展开用双引号；反引号是转义符，命令替换用 $(...)。",
			"- 复杂逻辑不要猜语法：改用 python -c \"...\" 或 Read / Grep / Glob / Edit 这类文件工具。",
		}, "\n")
	case ShellModeCmd:
		return strings.Join([]string{
			"【执行器：cmd.exe（" + spec.Bin + "）】",
			"- 串联：支持 && 与 ||；管道 | 可用。分号不是命令分隔符，`;` 只会被当成参数的一部分。",
			"- 重定向：2>nul 丢弃 stderr；>nul 丢弃 stdout；**没有 /dev/null**。",
			"- 环境变量：`set VAR=x` 设置（仅当前会话）；读取用 %VAR%；`set VAR=x && cmd` 只影响该条命令链。",
			"- 没有 grep/sed/awk/head/tail/cat/ls：用 findstr、more、type、dir；复杂文本处理改用 python -c 或内置工具。",
			"- 路径用反斜杠；含空格的路径要加双引号。",
			"- 编码：平台已执行 chcp 65001（UTF-8）；输出乱码按编码问题处理，不要当成命令失败重试。",
			"- 复杂逻辑不要猜语法：改用 python -c \"...\" 或 Read / Grep / Glob / Edit 这类文件工具。",
		}, "\n")
	case ShellModeBash:
		out := []string{
			"【执行器：bash（" + spec.Bin + "）】命令按 POSIX/bash 语法书写。",
			"- 支持 && / || / 管道 / heredoc / 重定向 2>/dev/null。",
		}
		if strings.Contains(strings.ToLower(spec.Bin), "git") || strings.Contains(spec.Bin, ":\\") || strings.Contains(spec.Bin, ":/") {
			out = append(out,
				"- 这是 Git for Windows 的 bash：Windows 路径要写成 /c/Users/... 形式，原生的 Windows 程序仍可直接调用（如 curl.exe）。",
				"- 没有 sudo / systemctl / apt 等 Linux 专属命令；包管理用 winget/choco，若本机有。")
		}
		out = append(out, "- 复杂逻辑不要硬拼一行：写脚本或改用 Read / Grep / Glob / Edit 这类文件工具。")
		return strings.Join(out, "\n")
	default:
		return "【执行器：未解析】当前无法确定用哪个 Shell 执行命令，Bash 调用会返回配置错误。先去「设置 → 工具执行 → Shell」选一个本机真实存在的 Shell；在此之前只剩 Read / Grep / Glob 能读文件，联网与命令类工作都做不了，不要重试 Bash。"
	}
}

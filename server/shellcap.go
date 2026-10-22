package server

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// ShellCapability 是平台启动时对可用 shell 的一次探测结果（批次 5 / #9）。
//
// norma 的 Bash 工具在 Windows 上以 `powershell -NoProfile -NonInteractive -Command`
// 执行命令、其他平台以 `bash -c` 执行（norma/tool/task.go shellCmd）。模型经常按
// 提示词里的 Unix 直觉写命令，在 Windows 上会「语法不明」地失败——这里把探测结果
// 写启动日志、经 /api/health 暴露给前端，并把 Guidance 追加到 Bash 工具描述，
// 让模型在调用前就知道该用什么语法。
type ShellCapability struct {
	OS        string   `json:"os"`
	Executor  string   `json:"executor"`            // SDK Bash 在此平台实际调用的解释器
	Available []string `json:"available,omitempty"` // PATH 上探测到的 shell（按候选顺序）
	Guidance  string   `json:"guidance,omitempty"`  // 中文引导文案（仅非 bash 平台非空）
}

var (
	shellCapOnce sync.Once
	shellCap     ShellCapability
)

// shellCapability 返回进程生命周期内缓存的一次探测结果。
func shellCapability() ShellCapability {
	shellCapOnce.Do(func() { shellCap = detectShellCapability() })
	return shellCap
}

// detectShellCapability 探测本机可用 shell。刻意用 exec.LookPath 而非跑一条
// 命令：探测必须零副作用、零等待（启动路径），是否真正可用由首次 Bash 调用暴露。
func detectShellCapability() ShellCapability {
	c := ShellCapability{OS: runtime.GOOS}
	candidates := []string{"bash", "sh", "zsh", "pwsh", "powershell", "cmd"}
	if runtime.GOOS == "windows" {
		// Windows 上优先报告 PowerShell 家族：即使装了 Git Bash，SDK 仍走 PowerShell。
		candidates = []string{"pwsh", "powershell", "cmd", "bash", "sh"}
	}
	for _, name := range candidates {
		if _, err := exec.LookPath(name); err == nil {
			c.Available = append(c.Available, name)
		}
	}
	switch runtime.GOOS {
	case "windows":
		c.Executor = "powershell"
		c.Guidance = windowsShellGuidance
	case "darwin":
		c.Executor = "bash"
		c.Guidance = darwinShellGuidance
	default:
		c.Executor = "bash"
	}
	return c
}

// platformShellNote 供 wireTools 追加到 Bash 工具描述（按平台返回语法与工具链提示）。
func platformShellNote() string {
	g := strings.TrimSpace(shellCapability().Guidance)
	if g == "" {
		return ""
	}
	return "\n\n【平台环境提示】" + g
}

// windowsShellGuidance 是 Windows/PowerShell 的语法引导（#9）。写法上强调「模型
// 常见错误」而不是罗列完整语法——一句话说明执行器、一组高频对应关系、一条禁用清单。
const windowsShellGuidance = "当前平台为 Windows：Bash 工具实际以 PowerShell 执行（powershell -NoProfile -NonInteractive -Command），不是 bash。" +
	"常规 HTTP 请求、并发目录/参数 Fuzz、编解码/哈希/加解密/JWT、正则/JSON/HTML/JS 提取、端口探测及文件检索，请优先直接调用跨平台内置 Go 原生工具（http_request / http_fuzz / codec_crypto / extract_data / net_probe / Grep / Glob / Read），无需经过 PowerShell，也不要为此编写临时 .py 脚本。" +
	"确需通过 Bash 调用外部命令行工具时请按 PowerShell 语法书写：环境变量用 $env:NAME；调用 curl 必须写 curl.exe（PowerShell 中 curl 是 Invoke-WebRequest 别名）且含 & 的 URL 必须加双引号；不要使用 grep / sed / awk / export / head / tail 等 Unix 专属命令；PowerShell 5.1 不支持 &&，需要串联时用 ; 或分行执行。" +
	"编码：平台已把命令管道强制为 UTF-8，写文件请显式指定 UTF-8；输出若出现乱码，按编码问题处理（换读法/换工具），不要当成命令失败反复重试。"

// darwinShellGuidance 是 macOS (darwin) 的 BSD 用户态语法引导，避免模型混用 GNU 专属参数。
const darwinShellGuidance = "当前平台为 macOS (darwin)：Bash 工具使用 POSIX/bash 执行，但系统内置命令行工具为 BSD 变体而非 GNU。" +
	"常规 HTTP 请求、并发 Fuzz、编解码/加解密、数据提取、二进制头部检查与文件检索，请优先直接调用跨平台内置 Go 原生工具（http_request / http_fuzz / codec_crypto / extract_data / binary_inspect / net_probe / Grep / Glob / Read / Edit）。" +
	"确需调用系统命令时请注意 BSD 差异：grep 不支持 -P（Perl 正则），请用 -E 或内置 Grep 工具；sed -i 必须显式跟备份后缀（如 sed -i ''）；stat 使用 -f 而非 -c；读取 Mach-O 头部优先用 binary_inspect 或 otool。"

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
	if runtime.GOOS == "windows" {
		c.Executor = "powershell"
		c.Guidance = windowsShellGuidance
	} else {
		c.Executor = "bash"
	}
	return c
}

// platformShellNote 供 wireTools 追加到 Bash 工具描述（非 Windows 返回空串）。
func platformShellNote() string {
	g := strings.TrimSpace(shellCapability().Guidance)
	if g == "" {
		return ""
	}
	return "\n\n【平台环境提示】" + g
}

// windowsShellGuidance 是 Windows/PowerShell 的语法引导（#9）。写法上强调「模型
// 常见错误」而不是罗列完整语法——一句话说明执行器、一组高频对应关系、一条禁用清单。
const windowsShellGuidance = "当前平台为 Windows：Bash 工具实际以 PowerShell 执行（powershell -NoProfile -NonInteractive -Command），" +
	"不是 bash。请按 PowerShell 语法组织命令：环境变量 $env:NAME；命令替换 $(...)：管道后接 Where-Object / ForEach-Object / Select-String；" +
	"文件操作用 Get-ChildItem / Get-Content / Set-Content / Remove-Item -Recurse -Force；内联 python -c 等多行脚本建议先 Set-Content 写文件再执行。" +
	"不要使用 grep / sed / awk / export / head / tail 等 Unix 专属命令（用 Select-String、Get-Content -TotalCount 代替）；" +
	"PowerShell 5.1 不支持 &&，需要串联时用 ; 或分行执行。"

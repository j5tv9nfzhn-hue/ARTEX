package server

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/agent"
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

// shellCapability 返回当前执行器 + PATH 上的候选 shell。
//
// 注意：Executor 必须**每次现读**，不能缓存。执行器现在由设置页决定
// （server/shell_settings.go），用户在设置里切到 CMD 之后，/api/health 与环境快照
// 必须立刻反映新值；缓存在 sync.Once 里会一直报启动时的旧结论。
// PATH 探测结果才是可以缓存的（与设置无关，且 LookPath 有系统调用成本）。
func shellCapability() ShellCapability {
	shellCapOnce.Do(func() { shellCap = detectShellCapability() })
	c := shellCap
	spec := agent.CurrentShellSpec()
	if spec.Zero() {
		c.Executor = "unavailable"
		c.Guidance = shellUnavailableGuidance
		return c
	}
	c.Executor = string(spec.Mode)
	c.Guidance = guidanceForFlavor(spec)
	return c
}

// guidanceForFlavor 只给「与执行器无关、但平台相关」的建议 + 一句话指向工具自带契约。
//
// 具体语法（&& 是否可用、2>$null 还是 2>nul、curl 要不要写 .exe）一律由 Bash 工具
// 自带的契约负责（agent/shell_contract.go）。这里再讲一遍必然会出现两份文案，
// 而其中一份迟早会和用户选的实际执行器对不上——这正是要消灭的问题。
func guidanceForFlavor(spec agent.ShellSpec) string {
	// 历史教训：这段文案曾写着「优先用内置 http_request / http_fuzz / codec_crypto /
	// extract_data / net_probe，不要写临时脚本」。工具面收敛后那些工具已全部下线，
	// 于是它和 Worker 提示词（联网必须走 Shell + curl/脚本）正面冲突——模型每轮都
	// 会拿着一个不存在的工具名去撞墙。工具没了，指引必须跟着改，不能再留旧名字。
	base := "联网动作（HTTP 发包、并发目录/参数/IDOR 探测、编解码与加解密、正则/JSON/HTML/JS 提取、端口探测）" +
		"本系统没有专用工具，一律通过本 Shell 用 curl 或现场编写的 Python（requests 库）脚本完成；" +
		"只有「读写本地文件、按内容或文件名检索」用内置 Read / Grep / Glob / Edit，这类别绕 Shell。" +
		"Shell 的语法对照见本工具自带的「执行器」说明。"
	switch spec.Mode {
	case agent.ShellModePowerShell:
		return base + "（当前执行器：PowerShell——curl 是 Invoke-WebRequest 的别名，发包请写 curl.exe；串联命令用 ; 而不是 &&）"
	case agent.ShellModeBash:
		if spec.Version != "" {
			return base + "（当前执行器：bash " + spec.Version + "）"
		}
	}
	return base
}

// detectShellCapability 的 Available 用 exec.LookPath（零副作用、零等待）；
// 真正能不能跑由 agent 的解析器用「真跑一次探针」判定，两者互不替代。
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

// shellUnavailableGuidance 用于「一个可用 Shell 都没解析出来」的情形。
const shellUnavailableGuidance = "本机没有解析出可用的 Shell，Bash 工具会直接返回配置错误（不会静默换用别的执行器）。" +
	"请到「设置 → 工具执行器」选择一个本机存在的 Shell，或用 ARTEX_SHELL_BIN 指定可执行文件。在此之前只有 Read / Grep / Glob 能读文件，联网与命令类工作都得先修好执行器。"

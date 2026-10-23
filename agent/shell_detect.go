package agent

// 探测本机可用的 Shell，供设置页决定「哪些选项该出现」。
//
// 硬性要求（用户 2026-10-23）：
//   - 不做 auto：下拉框只放明确选项；
//   - Git Bash **检测到才出现**，没装就不给这个选项（而不是选了之后报错）；
//   - 任何情况下不静默回落。
//
// 与 ResolveShell 的关系：这里对每个模式各解析一次，用「能不能真跑起来」当可用性
// 判据（见 shell_probe.go 的理由）；用户选了某个模式后，运行时仍会独立解析一遍，
// 两次结论不一致（例如探测后被人删了文件）就在执行时报 config 错。

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ShellOption 是一个可选项及其本机可用性。
type ShellOption struct {
	Mode      ShellMode `json:"mode"`
	Label     string    `json:"label"`
	Available bool      `json:"available"`
	Bin       string    `json:"bin,omitempty"`
	Version   string    `json:"version,omitempty"`
	Flavor    string    `json:"flavor,omitempty"`
	Note      string    `json:"note,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// defaultResolveDeps 是生产环境的依赖注入：真实 PATH、真实文件系统、真实探测。
func defaultResolveDeps() ResolveDeps {
	return ResolveDeps{
		GOOS:     runtime.GOOS,
		Env:      os.Getenv,
		LookPath: exec.LookPath,
		Exists: func(p string) bool {
			st, err := os.Stat(p)
			return err == nil && !st.IsDir()
		},
		Probe: probeShellBinary,
		GitExecPath: func() (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "git", "--exec-path").Output()
			return strings.TrimSpace(string(out)), err
		},
	}
}

// DetectShellOptions 返回设置页该显示的选项集合。
//
// 顺序即推荐顺序：PowerShell（Windows 默认）→ cmd → Git Bash。
// 非 Windows 只返回 bash 一项（cmd / powershell 在该平台没有意义）。
func DetectShellOptions() []ShellOption {
	deps := defaultResolveDeps()
	modes := []ShellMode{ShellModeBash}
	if runtime.GOOS == "windows" {
		modes = []ShellMode{ShellModePowerShell, ShellModeCmd, ShellModeBash}
	}
	out := make([]ShellOption, 0, len(modes))
	for _, m := range modes {
		opt := ShellOption{Mode: m, Label: shellModeLabel(m)}
		spec, err := ResolveShell(m, deps)
		switch {
		case err == nil:
			opt.Available = true
			opt.Bin = spec.Bin
			opt.Version = spec.Version
			opt.Flavor = spec.Flavor
			opt.Note = spec.Note
		case err != nil:
			opt.Error = err.Error()
		}
		if m == ShellModeBash && runtime.GOOS == "windows" {
			// Git Bash 是可选依赖：没装就不显示（装了的判定即「bash 能真跑」）。
			opt.Label = "Git Bash"
		}
		out = append(out, opt)
	}
	return out
}

// shellModeLabel 是 UI 文案。刻意写清「谁在背后跑」，避免用户以为 PowerShell
// 选项等于 Windows PowerShell 5.1。
func shellModeLabel(m ShellMode) string {
	switch m {
	case ShellModePowerShell:
		return "PowerShell"
	case ShellModeCmd:
		return "CMD（cmd.exe）"
	case ShellModeBash:
		if runtime.GOOS == "windows" {
			return "Git Bash"
		}
		return "Bash"
	}
	return string(m)
}

// ResolveShellWithOverride 用设置页给出的显式路径（可为空）解析一次。
func ResolveShellWithOverride(mode ShellMode, override string) (ShellSpec, error) {
	deps := defaultResolveDeps()
	deps.Override = strings.TrimSpace(override)
	return ResolveShell(mode, deps)
}

// PlatformGOOS 暴露 runtime.GOOS（server 侧需要生成默认值时用）。
func PlatformGOOS() string { return runtime.GOOS }

// ResolveDefaultShell 按平台默认值解析一次（首次启动写默认设置时用）。
func ResolveDefaultShell() (ShellSpec, error) {
	return ResolveShell(DefaultShellMode(runtime.GOOS), defaultResolveDeps())
}

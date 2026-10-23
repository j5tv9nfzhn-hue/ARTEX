package agent

// Shell 真跑探测（任务 26 步骤 ①）。
//
// 为什么不能只查文件存在：Windows 的 Store/MSIX 会在
// %LOCALAPPDATA%\Microsoft\WindowsApps\ 放一个 0 字节的 reparse point 当
// 执行别名。os.Stat 说它在，CreateProcess 报 1312（ERROR_NO_SUCH_LOGON_SESSION）
// 或直接拒绝访问。Codex 为此踩过 #18937 / #30742 / #336。
//
// 所以这里一律真跑：带超时 spawn 一次，要求退出码 0 且能拿到版本号/ok 标记。
// 探测参数恒带 -NoLogo -NoProfile -NonInteractive：不带 -NoProfile 会加载
// oh-my-posh 之类的 profile，慢到几秒甚至直接失败（Codex #26853 / #6315）。

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// probeTimeout 是单次探测的上限。冷启动 powershell.exe 5.1 实测可到 6.88s
// （Codex #26998），所以给到 8s；超过这个数说明本机确实不可用。
const probeTimeout = 8 * time.Second

var (
	errProbeEmptyOutput      = errors.New("探测无输出")
	errProbeNonZero          = errors.New("探测退出码非 0")
	errProbeStorePlaceholder = errors.New("疑似 Store/MSIX 占位符（0 字节）")
)

// probeShellBinary 是 ResolveDeps.Probe 的默认实现：真跑一次。
func probeShellBinary(exe string, mode ShellMode) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	args := probeArgs(mode)
	cmd := exec.CommandContext(ctx, exe, args...)
	configureExternalProc(cmd)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("探测超时（%s）", probeTimeout)
	}
	if err != nil {
		// 1312 / 拒绝访问的原始错误在 Windows 上信息量最大，原样带上。
		if text != "" {
			return "", fmt.Errorf("%v：%s", err, probeFirstLine(text))
		}
		return "", err
	}
	if text == "" {
		return "", errProbeEmptyOutput
	}
	version := parseProbeVersion(text, mode)
	if version == "" && mode != ShellModeCmd {
		return "", fmt.Errorf("%w：%s", errProbeNonZero, probeFirstLine(text))
	}
	return version, nil
}

// probeArgs 是各模式的探针参数。输出被约定成两行：版本 + ok。
func probeArgs(mode ShellMode) []string {
	switch mode {
	case ShellModePowerShell:
		// $PSVersionTable.PSVersion 在 5.1 与 7 上都存在；ToString() 给 "5.1.19041.1" / "7.4.2"。
		return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command",
			"$PSVersionTable.PSVersion.ToString(); Write-Output 'artex-probe-ok'"}
	case ShellModeCmd:
		return []string{"/d", "/c", "echo %ComSpec% & echo artex-probe-ok"}
	default: // bash
		return []string{"-c", "echo $BASH_VERSION; echo artex-probe-ok"}
	}
}

// probeOkMarker 是探针成功标记。必须收到它，避免「进程活着但命令没被执行」
// 这类假阳性（例如 shell 被替换成别的程序却有退出码 0）。
const probeOkMarker = "artex-probe-ok"

// parseProbeVersion 从探测输出里取版本。要求同时出现 ok 标记，否则视为失败。
func parseProbeVersion(out string, mode ShellMode) string {
	if !strings.Contains(out, probeOkMarker) {
		return ""
	}
	lines := strings.Split(out, "\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.Contains(ln, probeOkMarker) {
			continue
		}
		if mode == ShellModeCmd {
			// cmd 探针输出的是 ComSpec 路径，没有版本概念。
			continue
		}
		if v := normalizeVersionString(ln); v != "" {
			return v
		}
	}
	if mode == ShellModeCmd {
		return ""
	}
	return ""
}

// normalizeVersionString 把 "5.1.19041.1" 这类串裁到前两段（展示用），
// 并校验首段是数字——防止把任意一行日志误当版本号。
func normalizeVersionString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// bash 的 $BASH_VERSION 可能带后缀，如 "5.3.15(1)-release"。
	if i := strings.IndexAny(s, "( "); i > 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	// PowerShell 的 Build/Revision 段对契约无意义，保留两段即可。
	if len(parts) >= 2 && startsWithDigit(parts[1]) {
		return parts[0] + "." + parts[1]
	}
	return parts[0]
}

func startsWithDigit(s string) bool {
	if s == "" {
		return false
	}
	return s[0] >= '0' && s[0] <= '9'
}

func probeFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

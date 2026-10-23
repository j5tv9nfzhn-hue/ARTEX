package agent

// 后台 Bash（run_in_background）。
//
// 现状与取舍：norma 的 TaskManager 在 Spawn 里写死 `shellCmd()`（Windows 上是
// `powershell -NoProfile -NonInteractive -Command`），SpawnSpec 没有 shell 字段，
// 也就是说**它只会用 PowerShell**。当前执行器恰好是 PowerShell 时，交给它是正确的；
// 用户手动把执行器切成 cmd / Git Bash 时再交给它，就等于偷偷换了执行器——这是
// 用户明确否决的行为。
//
// 所以这里分两条路：
//  1. 执行器 = PowerShell → 沿用 SDK 的 TaskManager：TaskOutput / TaskStop /
//     完成通知全都还能用，行为与之前一致；
//  2. 执行器 = cmd / bash → 自己 spawn（同一个 exe），输出写文件，返回 pid 与
//     日志路径，模型用 Read 读。代价是没有 TaskOutput 集成，收益是「设置里选什么
//     就真跑什么」。这条差异会写在返回文本里，不静默。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	actool "github.com/Autumn-27/norma/tool"
)

// shellBackgroundDelegable 报告这次后台调用能不能安心交给 SDK 的 TaskManager。
func shellBackgroundDelegable() bool {
	spec := CurrentShellSpec()
	return spec.Mode == ShellModePowerShell && !spec.Zero()
}

// runOwnBackground 用当前执行器直接起一个后台进程。
func runOwnBackground(ctx context.Context, command, workDir string, tc *actool.ToolContext) actool.Result {
	spec := CurrentShellSpec()
	if spec.Zero() {
		shellRT.mu.RLock()
		err := shellRT.err
		shellRT.mu.RUnlock()
		msg := "【配置错误：执行器不可用】"
		if err != nil {
			msg += err.Error()
		}
		return actool.Errorf(msg + "。请在设置页选择本机存在的 Shell，或设置 ARTEX_SHELL_BIN。")
	}

	dir, err := os.MkdirTemp("", "artex-bg-")
	if err != nil {
		return actool.Errorf("创建后台输出目录失败：" + err.Error())
	}
	logPath := filepath.Join(dir, "output.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		return actool.Errorf("创建后台输出文件失败：" + err.Error())
	}

	var cmd *exec.Cmd
	switch spec.Mode {
	case ShellModePowerShell:
		scriptPath := filepath.Join(dir, "cmd.ps1")
		if err := writePowerShellScript(scriptPath, buildPowerShellScript(command, workDir, true)); err != nil {
			_ = logFile.Close()
			_ = os.RemoveAll(dir)
			return actool.Errorf("写后台脚本失败：" + err.Error())
		}
		cmd = exec.Command(spec.Bin, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	case ShellModeCmd:
		cmd = exec.Command(spec.Bin, "/d", "/s", "/c", "chcp 65001>nul & "+command)
	default:
		cmd = exec.Command(spec.Bin, "-c", command)
	}
	if workDir != "" {
		cmd.Dir = workDir
	}
	if tc != nil && len(tc.Env) > 0 {
		cmd.Env = append(os.Environ(), tc.Env...)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureExternalProc(cmd)

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = os.RemoveAll(dir)
		return actool.Errorf("后台启动失败：" + err.Error())
	}
	pid := cmd.Process.Pid
	// 回收子进程（这里不需要它的结果，只是别留僵尸）。
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
	}()

	killHint := fmt.Sprintf("taskkill /PID %d /T /F", pid)
	if strings.ToLower(os.Getenv("GOOS")) == "linux" {
		killHint = fmt.Sprintf("kill -9 %d", pid)
	}
	return actool.Text(fmt.Sprintf(
		"已在后台启动（pid %d，执行器 %s）。输出写入：%s\n"+
			"用 Read 读取该文件查看进度与结果。\n"+
			"当前执行器不是 PowerShell，因此不受 TaskOutput/TaskStop 管理；需要停止时执行：%s\n"+
			"提示：后台任务不会随本轮对话结束而中断，但它没有退出码回传，判断完成请以日志末尾为准。",
		pid, spec.Mode, logPath, killHint))
}

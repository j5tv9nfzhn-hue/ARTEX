package server

// Shell 执行器的设置项（任务 26 步骤 ③）。
//
// 三条硬规则，都来自用户的明确决定：
//  1. 没有 auto：只接受 powershell / cmd / bash 三个值，其它一律 400；
//  2. 校验失败不写库：先真跑一次探测，跑不起来就不落盘，避免「存了一个用不了的
//     设置，下次启动整个 Bash 工具都不可用」；
//  3. 不静默回落：运行期执行器消失（卸载/盘符变化）→ 报 config 错误并提示去改设置，
//     绝不自己换一个 shell 顶上。

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/agent"
)

const (
	settingShellMode = "shell_mode"
	settingShellBin  = "shell_bin"
)

// shellSettingsPayload 是 GET /api/settings/shell 的响应体：
// 当前生效的执行器 + 本机可选项 + 预热池运行态。
func (s *Server) shellSettingsPayload() map[string]any {
	mode, _, _ := s.m.pg.GetSetting(settingShellMode)
	bin, _, _ := s.m.pg.GetSetting(settingShellBin)
	if strings.TrimSpace(mode) == "" {
		mode = string(agent.DefaultShellMode(agent.PlatformGOOS()))
	}
	return map[string]any{
		"shell_mode":  mode,                   // 用户选择（可能是无效值，UI 需自行比对）
		"shell_bin":   strings.TrimSpace(bin), // 显式覆盖路径，空=自动解析
		"options":     agent.DetectShellOptions(),
		"runtime":     agent.ShellRuntimeStatus(),
		"error_class": agent.ErrorClassLegend(),
	}
}

// getShellSettings 是 GET /api/settings/shell。只读：探测不写库、不改运行时。
func (s *Server) getShellSettings(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	writeJSON(w, 200, s.shellSettingsPayload())
}

// initShellRuntime 在服务启动时解析一次执行器。
//
// 失败不阻止启动（服务本身还能用内置工具），但会把原因记进运行时状态，
// 之后每次 Bash 调用都返回 config 类错误并带修复建议。
func (s *Server) initShellRuntime() {
	mode, ok, _ := s.m.pg.GetSetting(settingShellMode)
	mode = strings.TrimSpace(mode)
	if !ok || mode == "" {
		mode = string(agent.DefaultShellMode(agent.PlatformGOOS()))
		_ = s.m.pg.SetSetting(settingShellMode, mode)
	}
	bin, _, _ := s.m.pg.GetSetting(settingShellBin)
	if err := s.applyShellRuntime(mode, strings.TrimSpace(bin)); err != nil {
		log.Printf("[shell] 执行器初始化失败：%v（Bash 工具将返回 config 错误，直到设置修正）", err)
		return
	}
	spec := agent.CurrentShellSpec()
	log.Printf("[shell] 执行器：%s %s（%s）", spec.Mode, spec.Version, spec.Bin)
	agent.WarmShellPool(s.ctx)
}

// applyShellRuntime 解析并切换执行器；失败时把运行时置为「不可用」而不是沿用旧的。
//
// 为什么不沿用旧的：用户刚改了设置，如果新值跑不起来却继续用旧的，界面上显示的是
// 新值、实际跑的是旧 shell——这比直接报错难排查得多。
func (s *Server) applyShellRuntime(mode, bin string) error {
	parsed, ok := agent.ParseShellMode(mode)
	if !ok {
		err := fmt.Errorf("shell_mode 不合法（只接受 powershell / cmd / bash，不支持 auto）：%q", mode)
		agent.SetShellResolveError(err)
		return err
	}
	spec, err := agent.ResolveShellWithOverride(parsed, bin)
	if err != nil {
		agent.SetShellResolveError(err)
		return err
	}
	agent.SetShellSpec(spec)
	return nil
}

// shellSettingsChanged 处理 PUT /api/settings 里的 shell 字段。
//
// 返回 (changed, httpStatus, errMsg)。只有解析成功才写库；失败时库保持原值，
// 运行时进入「不可用」状态——界面能立刻看到错误，而不是等到下一条命令才发现。
func (s *Server) shellSettingsChanged(mode *string, bin *string) (bool, int, string) {
	curMode, _, _ := s.m.pg.GetSetting(settingShellMode)
	curMode = strings.TrimSpace(curMode)
	if curMode == "" {
		curMode = string(agent.DefaultShellMode(agent.PlatformGOOS()))
	}
	curBin, _, _ := s.m.pg.GetSetting(settingShellBin)
	curBin = strings.TrimSpace(curBin)

	nextMode, nextBin := curMode, curBin
	fingerprintChanged := false
	if mode != nil {
		v := strings.ToLower(strings.TrimSpace(*mode))
		if v != curMode {
			fingerprintChanged = true
		}
		nextMode = v
	}
	if bin != nil {
		v := strings.TrimSpace(*bin)
		if v != curBin {
			fingerprintChanged = true
		}
		nextBin = v
	}
	if !fingerprintChanged {
		return false, 0, ""
	}
	if err := s.applyShellRuntime(nextMode, nextBin); err != nil {
		return false, 400, err.Error()
	}
	if err := s.m.pg.SetSetting(settingShellMode, nextMode); err != nil {
		return false, 500, err.Error()
	}
	if err := s.m.pg.SetSetting(settingShellBin, nextBin); err != nil {
		return false, 500, err.Error()
	}
	// 预热池换成新执行器的：第一条命令就免冷启。
	agent.WarmShellPool(s.ctx)
	// 契约文案随执行器变化，必须让已建的 agent 重新读取（Description/Prompt 在
	// 建 agent 时就拼好了，不重建就还是旧文案）。
	s.invalidateProfileAgents()
	return true, 0, ""
}

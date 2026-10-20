package agent

import "strings"

// 本文件定义 Harness 会话的「能力域」（mode）及其策略档案。
//
// 为什么要有这一层：Harness 正在从「只会逆向/渗透」扩展成「渗透 + 开发 + 办公 + 通用」。
// 不同域的安全边界不应该散落在 UI、API、工具装配、prompt 里各写一遍——那样迟早互相打架，
// 也说不清「为什么这个会话能跑 rm -rf」。所以域 = 一份策略档案，所有地方都读它：
//
//   - 渗透/逆向（pentest）：**沿用现状，一行不改**。任务级的工作目录、审批、危险命令
//     处理仍走既有 settings 与任务链路（MainAgent/Planner/Worker）。
//   - 正常使用（general/dev/office）：跟成熟开源 agent harness 一致——
//     会话开始由用户**选目录**（不是预登记白名单），危险命令与 git 写操作**走审批**。
//
// 该档案在 Phase 0 只用于校验与展示；Phase 1 起由 ChatAgent 装配工具时读取。
const (
	ModeGeneral = "general" // 问答/写作/整理，默认值（= 今天 ChatAgent 的行为）
	ModeDev     = "dev"     // 软件开发：读改码、构建、测试、git
	ModeOffice  = "office"  // 文档/表格/PPT/PDF
	ModePentest = "pentest" // 渗透/逆向：路由到既有 MainAgent/Planner/Worker，不新增执行逻辑
)

// WorkspacePick* 说明「工作根从哪来」。
const (
	// WorkspacePickDataDir：沿用既有设置（数据目录 / 任务工作区），不做选择器。
	// 渗透与逆向属此类——它们的目录语义已经由任务链路定义好了。
	WorkspacePickDataDir = "settings"
	// WorkspacePickPicker：会话开始时由用户选目录，与 Claude Code / Codex CLI 的 cwd 一致。
	WorkspacePickPicker = "picker"
)

// Gate* 说明某类操作是否需要人工审批。inherit = 完全沿用既有权限链（不改行为）。
const (
	GateInherit = "inherit"
	GateApprove = "approve"
)

// ModePolicy 是一个域对外承诺的行为边界。新增域时只加一行，不散落到各处。
type ModePolicy struct {
	Mode string `json:"mode"`
	// Label 是 UI 上的中文名。
	Label string `json:"label"`
	// WorkspacePick 见 WorkspacePick*。
	WorkspacePick string `json:"workspace_pick"`
	// DangerCmd 决定 rm -rf / format / curl|sh 这类命令的处理方式。
	DangerCmd string `json:"danger_cmd"`
	// GitWrite 决定 commit/branch/checkout/reset 这类 git 写操作的处理方式。
	GitWrite string `json:"git_write"`
	// ExtraTools 是该域在 SDK 基础工具之外**额外**开放的工具（默认工具集不变）。
	// 例：dev 需要 TodoWrite 做长任务清单、WebFetch 查文档。
	ExtraTools []string `json:"extra_tools"`
}

// modePolicies 是唯一真值源。顺序即 UI 展示顺序。
var modePolicies = []ModePolicy{
	{
		Mode:          ModeGeneral,
		Label:         "通用",
		WorkspacePick: WorkspacePickPicker,
		DangerCmd:     GateApprove,
		GitWrite:      GateApprove,
		ExtraTools:    []string{"TodoWrite"},
	},
	{
		Mode:          ModeDev,
		Label:         "开发",
		WorkspacePick: WorkspacePickPicker,
		DangerCmd:     GateApprove,
		GitWrite:      GateApprove,
		ExtraTools:    []string{"TodoWrite", "WebFetch"},
	},
	{
		Mode:          ModeOffice,
		Label:         "办公",
		WorkspacePick: WorkspacePickPicker,
		DangerCmd:     GateApprove,
		GitWrite:      GateApprove,
		ExtraTools:    []string{"TodoWrite", "WebFetch"},
	},
	{
		Mode:          ModePentest,
		Label:         "渗透 / 逆向",
		WorkspacePick: WorkspacePickDataDir,
		DangerCmd:     GateInherit,
		GitWrite:      GateInherit,
		ExtraTools:    nil,
	},
}

// NormalizeMode 把空值/未知值归一到 general，并去掉大小写与空白差异。
// 老会话没有 mode 列时读到空串，这里统一成 general —— 与今天 ChatAgent 的行为一致。
func NormalizeMode(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	if m == "" {
		return ModeGeneral
	}
	if _, ok := ModePolicyFor(m); ok {
		return m
	}
	return ModeGeneral
}

// ModeValid 判断是否是可以持久化的合法域。
func ModeValid(m string) bool {
	_, ok := ModePolicyFor(strings.ToLower(strings.TrimSpace(m)))
	return ok
}

// ModePolicyFor 返回某个域的策略档案。
func ModePolicyFor(m string) (ModePolicy, bool) {
	m = strings.ToLower(strings.TrimSpace(m))
	for _, p := range modePolicies {
		if p.Mode == m {
			return p, true
		}
	}
	return ModePolicy{}, false
}

// ModePolicies 返回全部域档案的副本，供 API / UI 列出可选域。
func ModePolicies() []ModePolicy {
	out := make([]ModePolicy, len(modePolicies))
	copy(out, modePolicies)
	return out
}

// ModeSystemSpec 返回某个域追加到系统提示的行为约定。
// 空串 = 不追加：渗透/逆向与通用域保持原有提示，不做任何行为改变。
func ModeSystemSpec(mode, workRoot string) string {
	switch NormalizeMode(mode) {
	case ModeDev:
		return "**当前能力域：软件开发。** 工作根是 " + workRoot + "（即命令的 CWD，相对路径基于它）。\n" +
			"- 改之前先读：用 Read/Grep 确认调用点、命名与既有风格，不要凭记忆整文件重写。\n" +
			"- 改动小步聚焦：一次改一个可验证的点，改完立刻跑构建/测试确认，再继续下一步。\n" +
			"- 未经明确要求，不要执行 git commit/reset/checkout，也不要删除或覆盖用户未提及的文件。\n" +
			"- 汇报时说明：改了哪些文件、为什么改、用什么命令验证过。"
	case ModeOffice:
		return "**当前能力域：办公文档。** 工作根是 " + workRoot + "。\n" +
			"- 产出文件统一写到工作根下的 artifacts/ 目录，文件名带日期与主题。\n" +
			"- 回答里给出产出文件路径 + 一句话摘要；不要只把内容贴在对话里。"
	}
	return ""
}

// UsesDataDir 报告某个域是否沿用既有目录设置（渗透/逆向）。
func UsesDataDir(mode string) bool {
	p, ok := ModePolicyFor(mode)
	return ok && p.WorkspacePick == WorkspacePickDataDir
}

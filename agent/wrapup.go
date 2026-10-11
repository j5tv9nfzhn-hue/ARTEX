package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// 收尾提示词(wrap-up / settlement prompt):当 agent 因【步数耗尽(MaxTurns)】或
// 【超时(run_seconds/MaxDuration)】被终止时,SDK 的 settlement 阶段会注入这段提示,
// 让 agent 先把已识别但未写回的内容落库、再输出一句总结,避免烂尾。
//
// 每个 agent 的收尾提示词可在后台按需覆盖(存 agents.wrapup_prompt),留空则用这里的
// 内置默认。仅【提示词正文】可编辑;禁用哪些工具、收尾自身给几轮预算属代码固定策略。

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// 内置默认收尾提示词,按 agent key 索引。worker 复用历史上硬编码的 settleWrapUpPrompt
// (定义在 worker.go),planner/mainagent 各有一版;未命中的(自定义 agent)走通用兜底。
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: 各 agent 收尾阶段【自身】的轮数预算内置默认(可被后台 >0 覆盖)。
// 均给 10 轮,保证收尾阶段有足够步数落库。未命中走 genericWrapupTurns。
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "本轮规划的步数预算即将耗尽，但结束的只是【本轮】；系统仍会在态势变化时再次唤醒你继续规划。任务本身并未结束，因此不需要在这里收官整个计划。请落实本轮已经明确判断的结论，避免本轮白跑，但【不要为了收尾而强行编造意图】（本轮产生 0 个意图也是完全正常的结果）。(1) 如果已经确定【现在就该派发】的探索方向，使用一次 add_intent 批量提交，不要搁置已经决定的方向。(2) 已有事实或发现证明达成的目标，使用 prove_goal 标记为 met，不要遗漏。(3) 如果识别出需要分阶段执行的串行利用链，使用 TodoWrite 记录，以便下次唤醒后继续派发。处理完毕后直接结束本轮，不输出总结文本。"

const mainAgentWrapUpDefault = "步数预算即将耗尽，本次交互即将结束。不要再开始新的探索或操作。**只用一句纯文本**，以简体中文向用户总结当前进展、核心结论和建议的下一步。"

const genericWrapUpDefault = "即将因预算耗尽而结束。先保存已经完成但尚未记录的结果，再**只用一句纯文本**，以简体中文总结做了什么以及得到的核心结论（这句话将作为本次执行结果展示）。"

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        withWrapupLang(resolveWrapup(agentKey)),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// withWrapupLang 为非空收尾提示追加 langDirective。
// 收尾提示不同于系统提示，不会自动追加语言约束，因此这里显式追加，
// 确保最终一句话总结遵循选择的输出语言，默认简体中文。
// 各角色的业务指令和不输出总结的 Planner 行为保持不变。
func withWrapupLang(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return prompt
	}
	lang := resolveOutputLang()
	return prompt + langDirective(lang)
}

// ---------- 任务级超时收尾词（见 docs/任务级超时与收尾设计.md）----------
//
// 与 per-run 收尾词是【两套】：per-run 是"你这一次 run 的预算用完了"；任务超时是
// "整个任务到点、即将结束"。语义常相反（尤其 planner：per-run 说"别停继续规划"，
// 任务超时说"到点停止规划、做最后判定"）。只给 worker/planner 配置。

// WrapupTaskTimeoutOverride / …TurnsOverride：任务超时收尾词与轮数的 DB 覆盖
// （wire 到 agents.task_timeout_wrapup_prompt / _max_turns，仅 worker/planner）。
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**整个任务已达到时间上限，即将结束**（不是本次 run 的预算耗尽，而是整个探索结束）。这是最后一次机会：(1) 保存【全部】已经识别但尚未记录的结果：新资产使用 insert_assets，探索结论与事实使用 record_fact，已确认漏洞使用 report_finding。(2) 不要再启动任何新的命令或探测。(3) **最后只用一句纯文本**，以简体中文总结本意图获得的核心结论。"

const plannerTaskTimeoutDefault = "**整个任务已达到时间上限，即将结束**（不是本轮结束，而是整个任务结束）。请根据目前的【全部】事实和发现进行最后一次目标判定。已有证据证明达成的目标，使用 prove_goal 标记为 met，不要遗漏。**不要再生成任何新意图**（现在派发也不会再执行）。完成判定后直接结束，不输出总结文本。"

// TaskTimeoutWrapupDefault 返回某 agent 的任务超时内置默认收尾词（供后台占位/恢复默认）。
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // 未配置(mainagent/chat)返回空串
}

// resolveTaskTimeoutWrapup：DB 覆盖(非空) > 内置默认。空串表示该 agent 无任务超时词
// （非 worker/planner），此时调用方应回退 per-run 词。
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // 默认沿用 per-run 轮数
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true  → 本次 run 被任务 deadline 夹逼：因 Timeout 收尾=任务到点→任务超时词；
//     因 MaxTurns 收尾=夹逼窗口内步数先耗尽、任务还剩几分钟→回落 per-run 词。
//   - clamped=false → 任务还早：两种 reason 都用 per-run 词（即退化为 wrapupSettlement）。
//
// 交给 harness 的 PromptByReason 在收尾时按【实际】reason 现场挑，无 build 时错配。
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := withWrapupLang(resolveWrapup(agentKey))
	st := &harness.Settlement{
		Prompt:        perRun, // 兜底(也是非 clamped 时两种 reason 的取值)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  withWrapupLang(tt), // 任务到点
				harness.ReasonMaxTurns: perRun,             // 步数先耗尽、任务还剩时间
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}

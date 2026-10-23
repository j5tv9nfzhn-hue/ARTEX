package agent

// 单意图工具调用额度（任务 27）。
//
// 为什么需要它：现存 loopBreaker 只拦「同名工具 + 同参数」的重复调用，模型换一个
// 端口、换一个路径就能继续刷；而 SDK 的 MaxTurns 数的是**模型轮**——一轮里并发
// 5 个工具调用只算 1 轮。于是「一条意图刷掉上百次工具调用」在轮次账面上完全看不
// 出来，只在 token 账单和错误率上体现。
//
// 三层设计，可靠性依次升高：
//  1. 软提醒（用掉一半）：往对话流里插一条 user 消息，明确告知已用一半、请优先核心目标。
//     它只是提醒，**不能**当作停止机制。
//  2. 硬拒绝（到达上限）：调用**根本不进入工具实现**——闸门在 guardedTool 里，
//     不依赖模型是否听话。拒绝结果以「【额度】」开头，任何从结果文本推导 error_class
//     的地方都会归为 quota（界面上能直接看出「这不是工具坏了，是额度用完了」）；
//     拒绝次数另记进 QuotaOutcome.Refused 随交接单落库——账本 tool_usage 只记真的
//     执行过的调用，被拦下的不算一次工具错误，否则会污染错误率。
//  3. 强制收尾（同一时刻）：在那一次 tool_result 之后注入最高优先级的收尾指令，要求
//     输出 ① 已完成 ② 未解决的线索 ③ 建议的下一步 的结构化报告；报告由宿主落库，
//     合并进交接单（server/handoff.go）。
//
// 关于「清空工具定义」：norma v0.4.3 的 harness 只在 run 开始时取一次 schema，
// 唯一能中途改写 schema 的入口是 Settlement（且只能由 MaxTurns/MaxDuration 触发）。
// 但 worker 的收尾轮需要保留 record_fact / report_finding 才能把结果写回，把全部
// 工具从 schema 里剥掉会破坏收尾语义。因此这里的硬停止落在**调用闸门**上：
// 额度用尽后任何工具调用都不会被执行（无副作用、无网络、无命令），比"模型看不到
// 工具定义"更强——即使模型执意反复调用，也只是拿到一条错误结果。
// 与之配套，worker 的 MaxTurns 会被夹到同一量级，让 SDK 自己的边界也尽快到来。

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/Autumn-27/norma/llm"
)

const (
	// DefaultIntentToolCap 是单条意图的工具调用上限（用户 2026-10-23 定：50 次）。
	DefaultIntentToolCap = 50
	// intentToolCapCeil 是配置上限，防止误填一个天文数字让机制形同虚设。
	intentToolCapCeil = 500
)

// IntentToolCap 解析单条意图的工具调用上限。0 = 关闭本机制。
// ARTEX_INTENT_TOOL_CAP 可覆盖；非法值退回默认。
func IntentToolCap() int {
	raw := strings.TrimSpace(os.Getenv("ARTEX_INTENT_TOOL_CAP"))
	if raw == "" {
		return DefaultIntentToolCap
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return DefaultIntentToolCap
	}
	if n > intentToolCapCeil {
		n = intentToolCapCeil
	}
	return n
}

// intentBudget 是一条意图执行期间的调用额度计数器。
//
// 并发安全：一轮里可能有多个工具并行执行，计数与「提醒是否已发」都用原子量。
type intentBudget struct {
	cap  int
	soft int
	// calls 只统计**真正被执行**的调用；被拒绝的调用不递增（否则一次 run 里会被
	// 刷成几千，让日志和指标失真）。
	calls    atomic.Int32
	softSent atomic.Bool
	hardSent atomic.Bool
	refused  atomic.Int32
}

// intentTurnCapFactor 是「轮次上限 = 工具额度 × 该系数」。
// 取 2 的理由：一轮可以并发多个工具调用，所以轮数通常小于调用数；但收尾轮、
// 纯思考轮、写报告轮不消耗额度。2 倍既保证 SDK 的 MaxTurns 永远晚于额度闸门
// 生效（额度才是主边界），又能在额度被关闭或模型空转时兜住失控。
const intentTurnCapFactor = 2

// intentTurnBudget 把 worker 配置的 MaxTurns 夹到与工具额度同一量级。
//   - 额度关闭（cap=0）→ 原样返回，不动运维配好的值；
//   - 配置为 0（不限）或大于 额度×2 → 夹到 额度×2；
//   - 配置本来就小于该值 → 尊重配置（更早收尾不是问题）。
//
// 这样 MaxTurns 与额度是同一条线，不会出现「额度在 50、轮次配了 500」的错位。
func intentTurnBudget(maxTurns int) int {
	cap := IntentToolCap()
	if cap <= 0 {
		return maxTurns
	}
	limit := cap * intentTurnCapFactor
	if maxTurns <= 0 || maxTurns > limit {
		return limit
	}
	return maxTurns
}

// newIntentBudget 建额度；cap<=0 返回 nil（关闭）。
func newIntentBudget(cap int) *intentBudget {
	if cap <= 0 {
		return nil
	}
	soft := cap / 2
	if soft < 1 {
		soft = 1
	}
	return &intentBudget{cap: cap, soft: soft}
}

// Calls 返回已执行（或被计入）的调用数。
func (b *intentBudget) Calls() int {
	if b == nil {
		return 0
	}
	return int(b.calls.Load())
}

// Refused 返回被闸门拒绝的调用数。
func (b *intentBudget) Refused() int {
	if b == nil {
		return 0
	}
	return int(b.refused.Load())
}

// Exhausted 报告本意图是否已经用完额度。
func (b *intentBudget) Exhausted() bool {
	if b == nil {
		return false
	}
	return int(b.calls.Load()) >= b.cap
}

// beforeCall 是每次工具调用前的闸门。
//
// 返回值：
//   - allow=false → 调用方**不得执行**该工具，直接把拒绝结果返回给模型；
//   - extra 非空 → 这些消息要在本次 tool_result 之后注入对话流（软提醒 / 强制收尾）。
func (b *intentBudget) beforeCall() (allow bool, extra []llm.Message) {
	if b == nil {
		return true, nil
	}
	used := int(b.calls.Load())
	if used >= b.cap {
		b.refused.Add(1)
		// 只在第一次拒绝时补一发收尾指令：之后模型每一轮都可能再试，重复注入
		// 只会把上下文撑大。
		if b.hardSent.CompareAndSwap(false, true) {
			extra = append(extra, intentQuotaReportMessage(b.cap))
		}
		return false, extra
	}
	n := int(b.calls.Add(1))
	if n == b.soft && b.softSent.CompareAndSwap(false, true) {
		extra = append(extra, intentQuotaSoftMessage(n, b.cap))
	}
	if n == b.cap && b.hardSent.CompareAndSwap(false, true) {
		// 第 cap 次仍然执行（额度是「允许 N 次」），但同一条结果之后立刻要求收尾：
		// 模型下一轮已经没有可用工具，只该写报告。
		extra = append(extra, intentQuotaReportMessage(b.cap))
	}
	return true, extra
}

// intentQuotaSoftMessage 是 50% 时的提醒（明确指令，不是含糊的"请注意"）。
func intentQuotaSoftMessage(used, cap int) llm.Message {
	return llm.UserText(fmt.Sprintf(
		"【工具额度提醒】你已使用本条意图一半的工具调用额度（%d/%d）。"+
			"请优先处理核心目标，避免在次要路径上消耗过多：能合并的探测合并成一次调用，"+
			"已有证据足够下结论的就直接写回事实/漏洞，不要为了「确认」再跑一遍。"+
			"剩余额度用完后工具会被强制停用。", used, cap))
}

// intentQuotaReportMessage 是额度用尽时的最高优先级收尾指令。
func intentQuotaReportMessage(cap int) llm.Message {
	return llm.UserText(fmt.Sprintf(
		"【最高优先级 · 强制收尾】本条意图的工具调用额度已用尽（%d/%d），"+
			"**工具已被强制停用，任何后续工具调用都不会执行**。"+
			"请立即停止调用工具，只基于已有信息输出一份结构化进度报告，供下一轮或人工接手："+
			"\n\n## ① 已完成\n（已确认的结论；已写回的 fact / asset / finding 逐条列出，附节点或资产 id）"+
			"\n\n## ② 未解决的线索\n（已发现但尚未验证或未打透的点，说明卡在哪一步、缺什么证据）"+
			"\n\n## ③ 建议的下一步\n（接手者应优先执行的具体动作，按优先级排列）"+
			"\n\n要求：只写有证据支撑的内容；不要复述过程日志；不要输出与这三点无关的内容。", cap, cap))
}

// intentBudgetContextKey 把额度挂到 ctx 上，供 guardedTool 取用。
type intentBudgetContextKey struct{}

// WithIntentBudget 把本次 run 的额度挂到 ctx 上（b=nil 时原样返回）。
func WithIntentBudget(ctx context.Context, b *intentBudget) context.Context {
	if b == nil {
		return ctx
	}
	return context.WithValue(ctx, intentBudgetContextKey{}, b)
}

func intentBudgetFrom(ctx context.Context) *intentBudget {
	if ctx == nil {
		return nil
	}
	if b, ok := ctx.Value(intentBudgetContextKey{}).(*intentBudget); ok {
		return b
	}
	return nil
}

// quotaReportMaxRunes 是落库的交接报告上限（字符）。模型可能写很长，交接单只
// 需要可读的正文：超出按 2/3 头 + 1/3 尾保留——结论在开头、下一步清单常在结尾。
const quotaReportMaxRunes = 6000

func clipQuotaReport(s string) string {
	if runeLen(s) <= quotaReportMaxRunes {
		return s
	}
	return clipRunes(s, quotaReportMaxRunes*2/3, quotaReportMaxRunes/3)
}

// quotaRefusalText 是被拒绝调用的结果文本（**不执行**任何工具逻辑）。
// 前缀【额度】用于 error_class 归类，改动文案时不要动它。
const quotaRefusalText = "【额度】本次工具调用被拒绝：本条意图的工具额度已用尽，本次调用**未执行**（无副作用）。" +
	"请不要再调用任何工具，直接输出结构化进度报告：① 已完成 / ② 未解决的线索 / ③ 建议的下一步。"

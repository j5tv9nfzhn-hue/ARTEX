// Package metrics 采集工具调用栈的进程内可靠性计数（批次 5 / #1–3、#36、#165–171）：
//
//   - 工具调用与错误，按工具名 + 阶段归类（hook/parse/validate/exec/timeout）；
//   - 崩溃重放：启动时被重置的 running 意图为一次「重放尝试」，其最终落点决定成败；
//   - 中断恢复：任务/意图暂停打断的运行为一次「中断事件」，恢复后终态决定成败；
//   - 协议防御计数：未配对/未知的工具结果、缺失的结果。
//
// 刻意零依赖（仅标准库）：被插桩的底层包（db、guard、agent）可以安全 import 它，
// 不会形成环。计数在进程内、重启清零——这是「本次启动以来的健康度」视图，不是历史
// 账本（调用次数账本在 tool_usage 表、token 账本在 llm_usage 表）。
package metrics

import (
	"sort"
	"sync"
	"time"
)

// stage 常量：错误发生阶段。与前端 dashboard.reliability.stage.* 标签一一对应。
const (
	StageHook     = "hook"     // PreToolUse 钩子/拦截规则/审批阻断
	StageParse    = "parse"    // 入参不是合法 JSON（协议层）
	StageValidate = "validate" // 入参结构不符合 schema 粗校验
	StageExec     = "exec"     // 工具执行返回错误
	StageTimeout  = "timeout"  // 工具执行超时/被取消（非工具缺陷）
	// StageTransport 是网络/代理层失败：请求根本没走到目标，或链路在握手/读取时断掉
	// （EOF、schannel 握手失败、连接被拒、代理未监听）。它既不是工具缺陷，也不是模型
	// 选错工具或参数，混进「工具错误率」会让指标失真——实测 131 条失败里有 29% 属此类。
	// 与 OpenTelemetry GenAI 语义约定把 error.type 做成低基数分类同一思路。
	StageTransport = "transport"
)

const (
	// maxToolKeys 限制按工具计数的键数量。工具名实际有限（内置 + MCP），
	// 超限时并入 otherKey，保证内存有界。
	maxToolKeys = 512
	otherKey    = "(其他)"
	// maxTopTools 是快照里 top_tools 的条数上限。
	maxTopTools = 8
	// maxPendingMarkers 限制待结算的意图 ID 数量：超限后不再登记新标记（只记
	// 尝试/事件总数），避免异常循环下内存无界。正常量级（单机任务数）远达不到。
	maxPendingMarkers = 4096
)

type toolCounters struct {
	calls  int64
	errors int64
	stages map[string]int64
}

var (
	mu    sync.Mutex
	since = time.Now()

	tools map[string]*toolCounters

	unpairedResults int64
	unknownResults  int64
	missingResults  int64

	replayAttempts  int64
	replaySucceeded int64
	replayFailed    int64
	replayPending   = map[int64]struct{}{}

	interruptEvents    int64
	interruptSucceeded int64
	interruptFailed    int64
	interruptPending   = map[int64]struct{}{}
)

// --- 工具调用 ---

// ObserveToolCall 记一次进入执行的工具调用（尝试数）。被 PreToolUse 拦截、
// 因而没有进入执行的调用走 ObserveToolBlocked，避免重复计数。
func ObserveToolCall(tool string) {
	mu.Lock()
	defer mu.Unlock()
	countersFor(tool).calls++
}

// ObserveToolBlocked 记一次被钩子/拦截规则阻断的调用：既是一次尝试，
// 也是一次 stage=hook 的错误。
func ObserveToolBlocked(tool string) {
	mu.Lock()
	defer mu.Unlock()
	c := countersFor(tool)
	c.calls++
	addStage(c, StageHook)
}

// ObserveToolError 记一次已执行调用在指定阶段的错误。
func ObserveToolError(tool, stage string) {
	mu.Lock()
	defer mu.Unlock()
	addStage(countersFor(tool), stage)
}

// countersFor returns the counter bucket for tool. Caller holds mu.
func countersFor(tool string) *toolCounters {
	if tools == nil {
		tools = map[string]*toolCounters{}
	}
	if tool == "" {
		tool = "(unknown)"
	}
	c, ok := tools[tool]
	if !ok {
		if len(tools) >= maxToolKeys {
			tool = otherKey
			c = tools[tool]
			if c == nil {
				c = &toolCounters{stages: map[string]int64{}}
				tools[tool] = c
			}
			return c
		}
		c = &toolCounters{stages: map[string]int64{}}
		tools[tool] = c
	}
	return c
}

func addStage(c *toolCounters, stage string) {
	if stage == "" {
		stage = StageExec
	}
	c.errors++
	c.stages[stage]++
}

// --- 协议防御 ---

// ObserveToolResultUnpaired 记一次没有调用 ID、无法与任何 tool_use 配对的工具结果
// （调用方应丢弃该结果）。
func ObserveToolResultUnpaired() {
	mu.Lock()
	unpairedResults++
	mu.Unlock()
}

// ObserveToolResultUnknown 记一次带 ID 但当前回合没有对应 tool_use 的结果
// （例如恢复会话时带出的旧结果；结果仍保留）。
func ObserveToolResultUnknown() {
	mu.Lock()
	unknownResults++
	mu.Unlock()
}

// ObserveToolResultMissing 记一次执行结束仍未收到结果的调用（中断/事件丢失）。
func ObserveToolResultMissing() {
	mu.Lock()
	missingResults++
	mu.Unlock()
}

// --- 崩溃重放（#36） ---

// MarkReplayPending 登记一批「启动时从 running 重置回 open」的意图 ID：每个 ID 是
// 一次重放尝试，其后续终态经 IntentSettled 结算为成功/失败。
func MarkReplayPending(ids []int64) {
	if len(ids) == 0 {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	replayAttempts += int64(len(ids))
	if len(replayPending) >= maxPendingMarkers {
		return
	}
	for _, id := range ids {
		replayPending[id] = struct{}{}
	}
}

// --- 中断恢复 ---

// MarkInterruptPending 记一次中断事件（任务或意图暂停打断了运行中的意图），并把该
// 意图登记为待结算：恢复后第一次到达终态时经 IntentSettled 记账。
func MarkInterruptPending(intentID int64) {
	mu.Lock()
	defer mu.Unlock()
	interruptEvents++
	if len(interruptPending) < maxPendingMarkers {
		interruptPending[intentID] = struct{}{}
	}
}

// IntentSettled 把一个意图的终态结算给两张恢复表：state == "done" 记为成功，
// blocked/exhausted/stopped 记为失败；其它状态（open/running/paused/deleted）不结算。
// 没有登记过标记的意图只被忽略——调用方可以无脑在终态路径上报。
func IntentSettled(intentID int64, state string) {
	var success bool
	switch state {
	case "done":
		success = true
	case "blocked", "exhausted", "stopped":
	default:
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if _, pending := replayPending[intentID]; pending {
		delete(replayPending, intentID)
		if success {
			replaySucceeded++
		} else {
			replayFailed++
		}
	}
	if _, pending := interruptPending[intentID]; pending {
		delete(interruptPending, intentID)
		if success {
			interruptSucceeded++
		} else {
			interruptFailed++
		}
	}
}

// --- 快照 ---

// ToolStat 是单个工具的调用/错误统计。Errors 只算「自致失败」（不含 transport）。
type ToolStat struct {
	Tool   string `json:"tool"`
	Calls  int64  `json:"calls"`
	Errors int64  `json:"errors"`
	// Transport 是其中的网络/代理层失败数，已从 Errors 中剔除，单独列出。
	Transport int64            `json:"transport,omitempty"`
	Stages    map[string]int64 `json:"stages,omitempty"`
}

// ToolMetrics 是工具调用维度的聚合。
//
// 口径（重要）：ErrorRate 是【自致失败率】——只把平台/模型能控制的失败算进分子
// （parse/validate/hook/exec/timeout），transport（网络、代理、TLS）单列。仪表盘上
// 「工具错误率」看这个数才有行动含义；含环境的旧口径见 RawErrorRate。
type ToolMetrics struct {
	Calls     int64   `json:"calls"`
	Errors    int64   `json:"errors"`     // 自致失败数
	ErrorRate float64 `json:"error_rate"` // 自致失败/调用；无调用时为 0
	// RawErrors/RawErrorRate 是含 transport 的历史口径，保留用于对比与回归。
	RawErrors       int64            `json:"raw_errors"`
	RawErrorRate    float64          `json:"raw_error_rate"`
	TransportErrors int64            `json:"transport_errors"`
	ByStage         map[string]int64 `json:"by_stage"`
	TopTools        []ToolStat       `json:"top_tools"`
}

// RecoveryMetrics 是重放/中断两张恢复表的统一形状。
type RecoveryMetrics struct {
	Attempts    int64   `json:"attempts"` // 重放=被重置的意图数；中断=中断事件数
	Succeeded   int64   `json:"succeeded"`
	Failed      int64   `json:"failed"`
	Pending     int64   `json:"pending"`      // 尚未到达终态
	SuccessRate float64 `json:"success_rate"` // 成功/(成功+失败)；无结算时为 0
}

// ProtocolMetrics 是协议防御计数。
type ProtocolMetrics struct {
	UnpairedResults int64 `json:"unpaired_results"` // 无 ID、已丢弃的结果
	UnknownResults  int64 `json:"unknown_results"`  // 有 ID 但无对应 tool_use 的结果
	MissingResults  int64 `json:"missing_results"`  // 执行结束未收到结果的调用
}

// Snapshot 是 /api/metrics 的响应体（自进程启动累计）。
type Snapshot struct {
	Since      int64           `json:"since"` // unix 秒
	Tools      ToolMetrics     `json:"tools"`
	Replay     RecoveryMetrics `json:"replay"`
	Interrupts RecoveryMetrics `json:"interrupts"`
	Protocol   ProtocolMetrics `json:"protocol"`
}

// TakeSnapshot 返回当前计数的一致性快照。
func TakeSnapshot() Snapshot {
	mu.Lock()
	defer mu.Unlock()

	snap := Snapshot{Since: since.Unix()}
	snap.Tools.ByStage = map[string]int64{}
	top := make([]ToolStat, 0, len(tools))
	var rawErrors int64
	for name, c := range tools {
		transport := c.stages[StageTransport]
		own := c.errors - transport
		if own < 0 {
			own = 0
		}
		snap.Tools.Calls += c.calls
		snap.Tools.Errors += own
		snap.Tools.TransportErrors += transport
		rawErrors += c.errors
		for stage, n := range c.stages {
			snap.Tools.ByStage[stage] += n
		}
		top = append(top, ToolStat{Tool: name, Calls: c.calls, Errors: own, Transport: transport, Stages: copyStages(c.stages)})
	}
	snap.Tools.RawErrors = rawErrors
	if snap.Tools.Calls > 0 {
		snap.Tools.ErrorRate = float64(snap.Tools.Errors) / float64(snap.Tools.Calls)
		snap.Tools.RawErrorRate = float64(rawErrors) / float64(snap.Tools.Calls)
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Errors != top[j].Errors {
			return top[i].Errors > top[j].Errors
		}
		if top[i].Calls != top[j].Calls {
			return top[i].Calls > top[j].Calls
		}
		return top[i].Tool < top[j].Tool
	})
	if len(top) > maxTopTools {
		top = top[:maxTopTools]
	}
	snap.Tools.TopTools = top

	snap.Replay = recoveryLocked(replayAttempts, replaySucceeded, replayFailed, len(replayPending))
	snap.Interrupts = recoveryLocked(interruptEvents, interruptSucceeded, interruptFailed, len(interruptPending))
	snap.Protocol = ProtocolMetrics{
		UnpairedResults: unpairedResults,
		UnknownResults:  unknownResults,
		MissingResults:  missingResults,
	}
	return snap
}

func recoveryLocked(attempts, ok, fail int64, pending int) RecoveryMetrics {
	m := RecoveryMetrics{Attempts: attempts, Succeeded: ok, Failed: fail, Pending: int64(pending)}
	if settled := ok + fail; settled > 0 {
		m.SuccessRate = float64(ok) / float64(settled)
	}
	return m
}

func copyStages(in map[string]int64) map[string]int64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

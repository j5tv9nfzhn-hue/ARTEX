package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/artex/metrics"
	"github.com/Autumn-27/artex/sidequestion"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

// stoppedToolSummary 是「调用还没拿到结果，会话就结束了」这一行的文案。
// 与前端 transcript.toolStatus.stopped 对应；metadata.tool_status 才是判定依据，
// 文案只负责给人看。
const stoppedToolSummary = "已停止：会话结束，未收到执行结果"

// captureRun drives one agent turn-to-completion over Session.Prompt and emits a
// coalesced ActivityRecord per execution step (tool_use / tool_result / text /
// thinking / result). It is shared by every LLM agent in the system (worker,
// planner, …) so their execution is visible instead of a black box — the old
// agentcore.Run discarded every event. The emitted records carry only
// Kind/Tool/ToolUseID/IsError/Summary/Detail; the caller's emit fills in
// IntentID/Worker. Returns the final assistant text + terminal error.
//
// KindText/KindThinking arrive as streaming deltas (one event per fragment); a
// contiguous run is coalesced into a single record so the trace shows whole
// messages, not dozens of fragments.
func captureRun(ctx context.Context, opts agentcore.Options, input string, emit func(db.Activity)) (string, harness.TerminalReason, error) {
	s := agentcore.NewSession(opts)
	defer s.Close() // release the session's background-task manager (temp dir + processes)
	return captureRunSession(ctx, s, input, emit)
}

// captureRunSession is captureRun over an existing session, so a caller can run
// multiple prompts on the SAME conversation (e.g. a settlement round that reuses
// the worker's accumulated context after the main run hit max_turns).
func captureRunSession(ctx context.Context, s *agentcore.Session, input string, emit func(db.Activity)) (string, harness.TerminalReason, error) {
	ctx, auditTrace := intercept.WithTrace(ctx, input, approvalHistory(s.Messages()))
	defer auditTrace.Finish()
	var reason harness.TerminalReason
	rec := func(r db.Activity) {
		if r.Kind == "text" || r.Kind == "tool_result" {
			auditTrace.Append(db.InterceptContextEntry{Kind: r.Kind, Tool: r.Tool, ToolUseID: r.ToolUseID, Text: r.Detail, IsError: r.IsError})
		}
		if emit != nil {
			emit(r)
		}
	}
	toolNames := map[string]string{} // tool_use id -> name, to label results
	// awaiting 记录尚未收到结果的 tool_use；会话结束时残留 = 执行结束未收到结果的调用
	// （中断或事件丢失），经 defer 统一计入协议防御计数（#14）。
	awaiting := map[string]string{}
	defer func() {
		// 会话结束时仍未收到结果的调用（用户中断 / 事件丢失）：除了计入协议防御计数，
		// 还要补一条终止行 —— 否则界面上那一行会一直转圈，分不清「还在跑」和「已停止」。
		// tool_status 走 metadata：中断这类结构性结论只有后端知道，前端不该靠文案猜。
		for id, name := range awaiting {
			metrics.ObserveToolResultMissing()
			if ctx.Err() == nil {
				continue
			}
			rec(db.Activity{
				Kind:      "tool_result",
				Tool:      name,
				ToolUseID: id,
				IsError:   true,
				Summary:   stoppedToolSummary,
				Metadata:  json.RawMessage(`{"tool_status":"stopped"}`),
			})
		}
	}()

	var tbuf strings.Builder
	var tkind string
	flush := func() {
		if tbuf.Len() == 0 {
			return
		}
		s := strings.TrimSpace(tbuf.String())
		k := tkind
		tbuf.Reset()
		tkind = ""
		if s != "" {
			rec(db.Activity{Kind: k, Summary: firstLine(s, 200), Detail: s})
		}
	}
	addDelta := func(kind, text string) {
		if text == "" {
			return
		}
		// 先把这一小段原样推给界面（live 通道），再继续累积成整块记录。
		// 界面靠它做「单行滚动」；落库仍是下面的 coalesce 口径。
		emitLive(ctx, LiveDelta{Kind: kind, Text: text})
		if tkind != "" && tkind != kind {
			flush()
		}
		tkind = kind
		tbuf.WriteString(text)
	}
	lastTool := &runTrace{startedAt: time.Now()}
	var lastUsage *llm.Usage

	var finalText string
	var rerr error
	for ev, err := range s.Prompt(ctx, input) {
		if err != nil {
			flush()
			if ctx.Err() != nil { // engine/user cancellation, not a provider failure
				sum, detail := terminalText(ctx, &harness.Terminal{Reason: reason, Err: ctx.Err()}, lastTool)
				rec(activityWithUsage(db.Activity{Kind: "result", Summary: firstLine(sum, 400), Detail: detail}, lastUsage))
				return finalText, reason, ctx.Err()
			}
			rec(activityWithUsage(db.Activity{Kind: "result", IsError: true, Summary: firstLine("执行出错: "+err.Error(), 200), Detail: err.Error()}, lastUsage))
			return finalText, reason, err
		}
		switch ev.Kind {
		case harness.KindToolUse:
			if ev.ToolUse == nil {
				continue
			}
			flush()
			// 工具调用一开始就告诉界面（它往往在思考结束后立刻出现，比 tool_result 早得多）。
			emitLive(ctx, LiveDelta{Kind: "tool_use", Tool: ev.ToolUse.Name, ToolUseID: ev.ToolUse.ID})
			toolNames[ev.ToolUse.ID] = ev.ToolUse.Name
			awaiting[ev.ToolUse.ID] = ev.ToolUse.Name
			in := string(ev.ToolUse.Input)
			lastTool.start(ev.ToolUse.ID, ev.ToolUse.Name, in)
			auditTrace.Start(ev.ToolUse.ID, ev.ToolUse.Name, ev.ToolUse.Input)
			rec(db.Activity{Kind: "tool_use", Tool: ev.ToolUse.Name, ToolUseID: ev.ToolUse.ID,
				Summary: ev.ToolUse.Name + " " + firstLine(in, 200), Detail: in})
		case harness.KindToolResult:
			if ev.ToolResult == nil {
				continue
			}
			// 协议防御（#14）：没有调用 ID 的结果无法与任何 tool_use 配对——丢弃并
			// 计数，别让它在会话流里冒充某个调用的执行结果。
			if ev.ToolResult.ToolUseID == "" {
				metrics.ObserveToolResultUnpaired()
				continue
			}
			flush()
			out := blocksText(ev.ToolResult.Content)
			delete(awaiting, ev.ToolResult.ToolUseID)
			if _, known := toolNames[ev.ToolResult.ToolUseID]; !known {
				// 有 ID 但在本轮没有对应 tool_use（例如恢复会话带出的旧结果）：
				// 保留记录，单独计数以便观察。
				metrics.ObserveToolResultUnknown()
			}
			lastTool.done(ev.ToolResult.ToolUseID)
			auditTrace.Complete(ev.ToolResult.ToolUseID, out, ev.ToolResult.IsError)
			rec(db.Activity{Kind: "tool_result", Tool: toolNames[ev.ToolResult.ToolUseID], ToolUseID: ev.ToolResult.ToolUseID,
				IsError: ev.ToolResult.IsError, Summary: firstLine(out, 200), Detail: out})
		case harness.KindText:
			addDelta("text", ev.Text)
		case harness.KindThinking:
			addDelta("thinking", ev.Text)
		case harness.KindUsage:
			// live cumulative token usage (per model turn). Emitted as a non-rendered
			// "usage" activity carrying only the token fields; the UI uses the latest
			// one for a running session's live token count. Don't flush() here — the
			// buffered final-answer text must stay for the KindResult de-dup.
			if ev.Usage != nil {
				u := *ev.Usage
				lastUsage = &u
				// 会话预算：每收到一条用量事件就地结算（事件是本轮累计值，账本内部取最大）。
				// 没挂账本时是 nil 接收者调用，方法内已做保护 → no-op。
				ConvLedgerFrom(ctx).Observe(u.InputTokens, u.OutputTokens)
				rec(db.Activity{Kind: "usage",
					InputTokens: &u.InputTokens, OutputTokens: &u.OutputTokens,
					CacheReadTokens: &u.CacheReadTokens, CacheWriteTokens: &u.CacheWriteTokens})
			}
		case harness.KindResult:
			if ev.Terminal != nil {
				if ev.Terminal.Reason != harness.ReasonAbortedStreaming {
					sidequestion.Finish(ctx, ev.Terminal.Messages)
				}
				finalText = ev.Terminal.Text
				reason = ev.Terminal.Reason
				// the buffered tail text usually equals Terminal.Text (final answer);
				// drop it to avoid a duplicate record, the result row carries it.
				if tkind == "text" && strings.TrimSpace(tbuf.String()) == strings.TrimSpace(ev.Terminal.Text) {
					tbuf.Reset()
					tkind = ""
				}
				flush() // flush any trailing thinking / non-final text
				sum, detail := ev.Terminal.Text, ev.Terminal.Text
				if sum == "" || ev.Terminal.Reason == harness.ReasonAbortedTools || ev.Terminal.Reason == harness.ReasonAbortedStreaming {
					sum, detail = terminalText(ctx, ev.Terminal, lastTool)
				}
				u := ev.Terminal.Usage // cumulative token usage for this session
				// A terminal event can carry a non-nil Err for a plain user abort:
				// harness ends the turn with Reason=AbortedTools/AbortedStreaming
				// and Err=context.Canceled. terminalText above already words that
				// as "（运行被中断…）", but flagging the row IsError makes the
				// transcript render an abort in red as if it were a failure.
				//
				// The other abort path (the err branch above) already checks
				// ctx.Err() before setting IsError; this mirrors that judgement so
				// both paths agree. Kept identical to terminalText's aborted
				// predicate, including the reason=="" fallback.
				aborted := ev.Terminal.Reason == harness.ReasonAbortedStreaming ||
					ev.Terminal.Reason == harness.ReasonAbortedTools ||
					(ev.Terminal.Reason == "" && ctx.Err() != nil)
				rec(db.Activity{Kind: "result", IsError: ev.Terminal.Err != nil && !aborted,
					Summary: firstLine(sum, 400), Detail: detail,
					InputTokens: &u.InputTokens, OutputTokens: &u.OutputTokens,
					CacheReadTokens: &u.CacheReadTokens, CacheWriteTokens: &u.CacheWriteTokens})
				if ev.Terminal.Err != nil {
					rerr = ev.Terminal.Err
				}
			}
		}
	}
	flush() // safety: any unflushed text if the stream ended without KindResult
	return finalText, reason, rerr
}

func activityWithUsage(activity db.Activity, usage *llm.Usage) db.Activity {
	if usage == nil {
		return activity
	}
	u := *usage
	activity.InputTokens = &u.InputTokens
	activity.OutputTokens = &u.OutputTokens
	activity.CacheReadTokens = &u.CacheReadTokens
	activity.CacheWriteTokens = &u.CacheWriteTokens
	return activity
}

// blocksText concatenates the text of a tool-result's content blocks.
func blocksText(blocks []llm.ContentBlock) string {
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == llm.BlockText && bl.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(bl.Text)
		}
	}
	return b.String()
}

// firstLine returns a single-line, rune-capped preview for the summary column.
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if before, _, found := strings.Cut(s, "\n"); found {
		s = before
	}
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}

// Preserve the recorded session's visible messages, excluding thinking blocks.
// This is audit context; the judge receives only bounded, paired execution
// evidence selected from it, never assistant prose or thinking blocks.
func approvalHistory(messages []llm.Message) []db.InterceptContextEntry {
	var entries []db.InterceptContextEntry
	for _, message := range messages {
		for _, block := range message.Content {
			entry := db.InterceptContextEntry{Kind: string(message.Role)}
			switch block.Type {
			case llm.BlockText:
				entry.Text = block.Text
			case llm.BlockToolUse:
				entry.Kind, entry.Tool, entry.ToolUseID, entry.Text = "tool_use", block.Name, block.ID, string(block.Input)
			case llm.BlockToolResult:
				entry.Kind, entry.ToolUseID, entry.Text, entry.IsError = "tool_result", block.ToolUseID, blocksText(block.Content), block.IsError
			default:
				continue
			}
			entries = append(entries, entry)
		}
	}
	return entries
}

// P0 会话语义旁路转录的引擎侧挂载面（纯增量，零行为影响）：
//   - traceHooks：观测钩子装饰器，包在 steerHooks 外层，先记后委派；
//   - Engine.trace*：任务线/意图线/规划轮/主 Agent 聊天的语义事件入口。
//
// 所有入口 fail-silent：tr 为 nil（未启用）或写失败时全部静默跳过，绝不改变
// 既有控制流、不返回错误、不 panic。
package server

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/tracelog"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

// SetTraceLog wires the P0 semantic trace recorder (nil = off).
func (e *Engine) SetTraceLog(s *tracelog.Store) { e.tr = s }

// TraceLog exposes the store for the read-only trace API.
func (e *Engine) TraceLog() *tracelog.Store { return e.tr }

// traceEv records one task-thread orchestration event (task-<id>).
func (e *Engine) traceEv(taskID, actor, typ, name, detail string) {
	if e.tr == nil || taskID == "" {
		return
	}
	e.tr.Emit(tracelog.Event{Thread: "task-" + taskID, Actor: actor, TaskID: taskID, Type: typ, Name: name, Detail: detail})
}

// traceWorkEv records one task-thread event that concerns a specific intent
// (steer/kill orders), carrying the intent id on the envelope.
func (e *Engine) traceWorkEv(taskID string, intentID int64, actor, typ, name, detail string) {
	if e.tr == nil || taskID == "" {
		return
	}
	e.tr.Emit(tracelog.Event{Thread: "task-" + taskID, Actor: actor, TaskID: taskID,
		IntentID: strconv.FormatInt(intentID, 10), Type: typ, Name: name, Detail: detail})
}

// traceIntentEv records an intent lifecycle event on BOTH the task thread
// (orchestration timeline) and the per-intent thread (exp<e>-worker-i<i>,
// same key as the norma raw transcript) so each side reads as one story.
func (e *Engine) traceIntentEv(t *Task, intentID int64, actor, typ, name, detail string) {
	if e.tr == nil || t == nil {
		return
	}
	iid := strconv.FormatInt(intentID, 10)
	ev := tracelog.Event{Actor: actor, TaskID: t.ID, IntentID: iid, Type: typ, Name: name, Detail: detail}
	task := ev
	task.Thread = "task-" + t.ID
	e.tr.Emit(task)
	work := ev
	work.Thread = agent.WorkerSessionID(t.ExpID, intentID)
	e.tr.Emit(work)
}

// traceTurnEv records a planner round boundary on the task thread.
func (e *Engine) traceTurnEv(taskID string, round int, src, typ string) {
	if e.tr == nil || taskID == "" {
		return
	}
	e.tr.Emit(tracelog.Event{Thread: "task-" + taskID, Actor: "planner", TaskID: taskID, Type: typ, Round: round, Source: src})
}

// TraceChatTurn records a main-agent chat turn boundary on the main-agent
// thread (exp<e>-main[-s<seg>], same naming as the mainagent transcript).
func (e *Engine) TraceChatTurn(t *Task, seg int, typ, detail string) {
	if e.tr == nil || t == nil {
		return
	}
	e.tr.Emit(tracelog.Event{Thread: mainThreadID(t.ExpID, seg), Actor: "mainagent", TaskID: t.ID, Type: typ, Detail: detail})
}

// mainThreadID mirrors agent/mainagent.go session naming (see
// checkpoints.go mainAgentTranscriptPath).
func mainThreadID(expID int64, seg int) string {
	if seg > 0 {
		return fmt.Sprintf("exp%d-main-s%d", expID, seg)
	}
	return fmt.Sprintf("exp%d-main", expID)
}

// errDetail renders an optional cause for control-event details.
func errDetail(err error) string {
	if err == nil {
		return ""
	}
	return tracelog.Clip(err.Error(), 200)
}

// traceHooks is the observational decorator around the per-work hook runner
// (steerHooks → guard hooks). It emits tool.call / tool.result / tool.blocked
// / turn.nudge events and delegates unchanged. Implements harness.HookRunner
// with the exact steerHooks method set.
type traceHooks struct {
	inner    harness.HookRunner
	tr       *tracelog.Store
	taskID   string
	intentID int64
	thread   string
	label    string
}

func (h traceHooks) ev(typ, name, detail string, isErr bool) {
	if h.tr == nil {
		return
	}
	h.tr.Emit(tracelog.Event{Thread: h.thread, Actor: "worker", TaskID: h.taskID,
		IntentID: strconv.FormatInt(h.intentID, 10), Type: typ, Name: name, Detail: detail, Err: isErr})
}

func (h traceHooks) PreToolUse(ctx context.Context, name string, input []byte) (bool, string, []byte) {
	blocked, msg, out := false, "", []byte(nil)
	if h.inner != nil {
		blocked, msg, out = h.inner.PreToolUse(ctx, name, input)
	}
	if blocked {
		// steer_work 送达 / guard 拦截：本次工具未执行，记 blocked 而非 call。
		h.ev("tool.blocked", name, tracelog.Clip(msg, 200), false)
	} else {
		h.ev("tool.call", name, tracelog.Clip(string(input), 512), false)
	}
	return blocked, msg, out
}

func (h traceHooks) PostToolUse(ctx context.Context, name string, input, result []byte, isErr bool) {
	h.ev("tool.result", name, tracelog.Clip(string(result), 512), isErr)
	if h.inner != nil {
		h.inner.PostToolUse(ctx, name, input, result, isErr)
	}
}

func (h traceHooks) Stop(ctx context.Context, messages []llm.Message) (bool, []string, string) {
	if h.inner == nil {
		return false, nil, ""
	}
	prevent, blocking, msg := h.inner.Stop(ctx, messages)
	if prevent || len(blocking) > 0 {
		d := tracelog.Clip(msg, 200)
		if len(blocking) > 0 {
			d = tracelog.Clip(blocking[0], 200)
		}
		h.ev("turn.nudge", h.label, d, false)
	}
	return prevent, blocking, msg
}

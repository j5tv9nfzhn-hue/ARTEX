package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/harness"
)

// 结构化交接：一次运行结束（无论是正常收尾、撞步数上限、报错还是被终止）
// 都写一份「运行台账 + 交接单」。
//
// 关键原则：交接单里的事实部分（跑过什么、写回什么、最后输出、trace 区间）
// 全部由代码从活动流直读，不经过模型——模型"没说出口"的东西不会因此消失；
// 模型将来若补充主观判断（下一步/假设/负结果），只能落在 OpenWork 这一栏，
// 且被标注为「可推翻的建议」，永远不会污染事实层。
const handoffSchema = "artex.handoff/v1"

type handoffStep struct {
	ID      int64  `json:"id"`
	Worker  string `json:"worker,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Tool    string `json:"tool,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type handoffSource struct {
	TaskID   string `json:"task_id"`
	IntentID int64  `json:"intent_id"`
	RunID    int64  `json:"run_id"`
}

type handoffTrigger struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
	At     string `json:"at"`
}

type handoffScope struct {
	Objective string `json:"objective"`
}

type handoffProgress struct {
	StepsTotal int            `json:"steps_total"`
	Tail       []handoffStep  `json:"tail"`
	Written    map[string]int `json:"written"`
	LastOutput string         `json:"last_output,omitempty"`
}

type handoffPointers struct {
	SessionID  string  `json:"session_id"`
	TraceRange []int64 `json:"trace_range,omitempty"`
}

type handoffRecord struct {
	Schema   string          `json:"schema"`
	Source   handoffSource   `json:"source"`
	Trigger  handoffTrigger  `json:"trigger"`
	Scope    handoffScope    `json:"scope"`
	Progress handoffProgress `json:"progress"`
	Pointers handoffPointers `json:"pointers"`
	// OpenWork 是本版留白：模型补注（下一步/假设/负结果）在 P3 由 handoff_patch
	// 写入，且永远与上面的事实层物理分开。
	OpenWork map[string]any `json:"open_work,omitempty"`
}

// stopReasonFrom 把 SDK 的终止原因归一成台账里的 stop_reason。
func stopReasonFrom(reason harness.TerminalReason, err error) string {
	switch {
	case err != nil:
		return "blocked_error"
	case reason == harness.ReasonMaxTurns:
		return "max_turns"
	case reason == harness.ReasonTimeout:
		return "max_duration"
	}
	return ""
}

// recordIntentRun 写台账 + 交接单。失败只记日志：交接是增强，不能反过来
// 影响任务收尾本身。
func (e *Engine) recordIntentRun(t *Task, intent *db.Node, started time.Time, reason harness.TerminalReason, state, stopReason string, wrote agent.WriteCounts) {
	if e == nil || e.m == nil || e.m.pg == nil || t == nil || intent == nil {
		return
	}
	wb, _ := json.Marshal(wrote)
	run := &db.IntentRun{
		TaskID:         t.ID,
		ExplorationID:  t.ExpID,
		IntentID:       intent.ID,
		Mode:           "run",
		StartedAt:      started,
		TerminalReason: string(reason),
		EndState:       state,
		StopReason:     stopReason,
		WritesJSON:     string(wb),
	}
	runID, err := e.m.pg.RecordIntentRun(run)
	if err != nil {
		log.Printf("[handoff] task %s 意图 #%d 写运行台账失败: %v", t.ID, intent.ID, err)
		return
	}
	e.saveHandoff(t, intent, runID, reason, state, stopReason, wrote)
}

// saveHandoff 机械抽取交接单：活动流 + 写回计数 + 意图原文，零模型参与。
func (e *Engine) saveHandoff(t *Task, intent *db.Node, runID int64, reason harness.TerminalReason, state, stopReason string, wrote agent.WriteCounts) {
	acts, err := t.Store.ActivityTrace(intent.ID, 400)
	if err != nil {
		log.Printf("[handoff] task %s 意图 #%d 读活动流失败: %v", t.ID, intent.ID, err)
		acts = nil
	}
	tail := make([]handoffStep, 0, 20)
	lastOutput := ""
	var firstID, lastID int64
	for _, a := range acts {
		if firstID == 0 {
			firstID = a.ID
		}
		lastID = a.ID
		if a.Kind == "result" && strings.TrimSpace(a.Summary) != "" {
			lastOutput = a.Summary
		}
	}
	if n := len(acts); n > 20 {
		acts = acts[n-20:]
	}
	for _, a := range acts {
		tail = append(tail, handoffStep{
			ID: a.ID, Worker: a.Worker, Kind: a.Kind, Tool: a.Tool,
			IsError: a.IsError, Summary: clipText(a.Summary, 200),
		})
	}
	rec := handoffRecord{
		Schema: handoffSchema,
		Source: handoffSource{TaskID: t.ID, IntentID: intent.ID, RunID: runID},
		Trigger: handoffTrigger{
			Kind:   firstNonEmpty(stopReason, string(reason), state),
			Detail: strings.TrimSpace(strings.Join([]string{state, string(reason)}, " / ")),
			At:     time.Now().UTC().Format(time.RFC3339),
		},
		Scope: handoffScope{Objective: clipText(string(intent.Payload), 1200)},
		Progress: handoffProgress{
			StepsTotal: len(acts),
			Tail:       tail,
			Written: map[string]int{
				"facts": wrote.Facts, "assets": wrote.Assets, "findings": wrote.Findings,
			},
			LastOutput: clipText(lastOutput, 1200),
		},
		Pointers: handoffPointers{
			SessionID:  fmt.Sprintf("task%s-intent%d", t.ID, intent.ID),
			TraceRange: traceRange(firstID, lastID),
		},
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		log.Printf("[handoff] task %s 意图 #%d 序列化失败: %v", t.ID, intent.ID, err)
		return
	}
	sum := sha256.Sum256(raw)
	h := &db.IntentHandoff{
		RunID:         runID,
		TaskID:        t.ID,
		ExplorationID: t.ExpID,
		IntentID:      intent.ID,
		SchemaVer:     handoffSchema,
		RecordJSON:    string(raw),
		RenderText:    renderHandoffText(rec),
		MechSHA256:    hex.EncodeToString(sum[:]),
	}
	if _, err := e.m.pg.SaveIntentHandoff(h); err != nil {
		log.Printf("[handoff] task %s 意图 #%d 保存交接单失败: %v", t.ID, intent.ID, err)
	}
}

func traceRange(first, last int64) []int64 {
	if first == 0 && last == 0 {
		return nil
	}
	return []int64{first, last}
}

// renderHandoffText 生成注入给后续子代理的紧凑文本（事实层，可信）。
func renderHandoffText(rec handoffRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n【交接单 · 来自意图 #%d（%s）】\n", rec.Source.IntentID, handoffTriggerLabel(rec.Trigger.Kind))
	b.WriteString("── 事实（由系统从执行记录直读，可信）──\n")
	fmt.Fprintf(&b, "已写回：事实 %d / 资产 %d / 漏洞 %d\n",
		rec.Progress.Written["facts"], rec.Progress.Written["assets"], rec.Progress.Written["findings"])
	fmt.Fprintf(&b, "目标（意图原文，截断）：%s\n", clipText(rec.Scope.Objective, 400))
	if len(rec.Progress.Tail) > 0 {
		b.WriteString("最后若干步：\n")
		for _, s := range rec.Progress.Tail {
			label := s.Tool
			if label == "" {
				label = s.Kind
			}
			mark := ""
			if s.IsError {
				mark = " ×"
			}
			fmt.Fprintf(&b, "  · [%s]%s %s\n", label, mark, clipText(s.Summary, 120))
		}
	}
	if rec.Progress.LastOutput != "" {
		fmt.Fprintf(&b, "最后输出：%s\n", clipText(rec.Progress.LastOutput, 500))
	}
	if len(rec.Pointers.TraceRange) == 2 {
		fmt.Fprintf(&b, "细节：get_handoff(intent_id=%d) 取完整交接单；get_worker_trace(intent_id=%d) 看完整步骤（记录 %d..%d）。\n",
			rec.Source.IntentID, rec.Source.IntentID, rec.Pointers.TraceRange[0], rec.Pointers.TraceRange[1])
	} else {
		fmt.Fprintf(&b, "细节：get_handoff(intent_id=%d) 取完整交接单。\n", rec.Source.IntentID)
	}
	out := b.String()
	if len(out) > 6000 {
		out = out[:6000] + "\n…（交接单已截断，完整内容见 get_handoff）"
	}
	return out
}

func handoffTriggerLabel(kind string) string {
	switch kind {
	case "max_turns":
		return "步数耗尽"
	case "max_duration":
		return "运行超时"
	case "task_timeout":
		return "任务超时收尾"
	case "planner_kill":
		return "被规划者终止"
	case "goal_met_cancel":
		return "任务完成时取消"
	case "blocked_error":
		return "执行出错"
	}
	return kind
}

func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

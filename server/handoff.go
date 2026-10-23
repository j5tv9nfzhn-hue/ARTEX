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

// 结构化交接：一次运行结束（正常收尾、撞步数上限、报错、被终止、进程崩溃）都落一份
// 「运行台账 + 交接单」。
//
// 分两层，物理分开：
//
//	L1 事实层：跑过什么、写回什么、最后输出、可回读的步骤区间 —— 全部由代码从活动流
//	           直读，不经过模型；mech_sha256 只覆盖这一层，事后可校验有没有被改过。
//	L2 主观层：open_work（下一步/假设/负结果）—— 模型通过 handoff_patch 主动补注，
//	           存在 intent_open_work 表里，渲染时永远带「可推翻」标签。它不能覆盖
//	           也不能替代 L1：模型没说出口的东西由 L1 兜，模型说错的东西由 L2 标注。
const handoffSchema = "artex.handoff/v1"

type handoffStep struct {
	ID      int64  `json:"id"`
	Tool    string `json:"tool,omitempty"`
	Worker  string `json:"worker,omitempty"`
	Kind    string `json:"kind,omitempty"`
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

// handoffOpenWork 是 L2 主观层的快照（源数据在 intent_open_work，追加式）。
type handoffOpenWork struct {
	NextActions     []string `json:"next_actions,omitempty"`
	Hypotheses      []string `json:"hypotheses,omitempty"`
	NegativeResults []string `json:"negative_results,omitempty"`
	Advisory        bool     `json:"advisory"` // 恒 true：这一层永远只是建议
}

type handoffRecord struct {
	Schema   string           `json:"schema"`
	Source   handoffSource    `json:"source"`
	Trigger  handoffTrigger   `json:"trigger"`
	Scope    handoffScope     `json:"scope"`
	Progress handoffProgress  `json:"progress"`
	Pointers handoffPointers  `json:"pointers"`
	OpenWork *handoffOpenWork `json:"open_work,omitempty"`
	// Quota 非空 = 本条意图因**工具额度用尽**而结束，正文是模型被强制产出的
	// 结构化报告（① 已完成 / ② 未解决线索 / ③ 建议下一步）。
	Quota *handoffQuota `json:"quota,omitempty"`
}

// handoffQuota 是一次「工具额度用尽」收尾的自述报告（任务 27）。
// 与 Progress（系统直读的事实层）物理分开：这份正文由模型写，属于可推翻的描述。
type handoffQuota struct {
	Cap     int    `json:"cap"`
	Calls   int    `json:"calls"`
	Refused int    `json:"refused,omitempty"`
	Report  string `json:"report,omitempty"`
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

// beginIntentRun 在 run 真正开跑【之前】先落一行（ended_at 留空）。
//
// 这一行的意义全在崩溃场景：进程被强杀/断电时，这是唯一能说明「当时谁在跑」的现场。
// 启动时的 reaper 扫到 ended_at IS NULL 就知道该给谁补交接单，而不用靠模型还活着。
func (e *Engine) beginIntentRun(t *Task, intent *db.Node, started time.Time) int64 {
	if e == nil || e.m == nil || e.m.pg == nil || t == nil || intent == nil {
		return 0
	}
	id, err := e.m.pg.BeginIntentRun(&db.IntentRun{
		TaskID: t.ID, ExplorationID: t.ExpID, IntentID: intent.ID, StartedAt: started,
	})
	if err != nil {
		log.Printf("[handoff] task %s 意图 #%d 写运行台账(开始)失败: %v", t.ID, intent.ID, err)
		return 0
	}
	return id
}

// finishIntentRun 收尾台账 + 落交接单。失败只记日志：交接是增强，不能反过来
// 影响任务收尾本身。
//
// runID>0 走 UPDATE（正常路径）；=0 说明开始行没写成（例如台账表刚建失败），
// 退化成一次性写完整行，交接照样不丢。
func (e *Engine) finishIntentRun(runID int64, t *Task, intent *db.Node, started time.Time, reason harness.TerminalReason, state, stopReason, errText string, wrote agent.WriteCounts) {
	if e == nil || e.m == nil || e.m.pg == nil || t == nil || intent == nil {
		return
	}
	wb, _ := json.Marshal(wrote)
	if runID > 0 {
		if err := e.m.pg.FinishIntentRun(runID, state, stopReason, string(reason), string(wb), 0); err != nil {
			log.Printf("[handoff] task %s 意图 #%d 运行台账收尾失败: %v", t.ID, intent.ID, err)
		}
	} else {
		id, err := e.m.pg.RecordIntentRun(&db.IntentRun{
			TaskID: t.ID, ExplorationID: t.ExpID, IntentID: intent.ID, Mode: "run",
			StartedAt: started, TerminalReason: string(reason), EndState: state,
			StopReason: stopReason, WritesJSON: string(wb),
		})
		if err != nil {
			log.Printf("[handoff] task %s 意图 #%d 写运行台账失败: %v", t.ID, intent.ID, err)
		} else {
			runID = id
		}
	}
	e.saveHandoff(t, intent, runID, started, reason, state, stopReason, wrote)
	// P2：坏终态 → 立刻按策略决定「立即重跑 / 退避重跑 / 转人工」。
	// 成功则把回炉账本清掉（这条分支不再欠账）。paused 两者都不是：
	// 它会被恢复路径重新放回 frontier，不需要走回炉策略。
	switch {
	case state == "done":
		_ = e.m.pg.DeleteRetryState(t.ID, intent.ID)
	case isBadBranchState(state):
		e.kickRequeue(t, intent.ID, state, stopReason, errText)
	}
}

// errTextOf 把 error 折成台账里的错误文本（空 error = 空串）。
func errTextOf(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// settleInterruptedRun 给「起了头但进程没了」的 run 补收尾（reaper 用）：
// 置 end_state=interrupted，再用 L1 机械抽取补一张交接单——模型已经不在了，
// 但活动流还在，事实层照样能重建。
func (e *Engine) settleInterruptedRun(t *Task, intent *db.Node, runID int64, started time.Time) {
	if e == nil || e.m == nil || e.m.pg == nil || t == nil || intent == nil {
		return
	}
	if err := e.m.pg.FinishIntentRun(runID, "interrupted", "crash", "interrupted", "", 0); err != nil {
		log.Printf("[reaper] task %s 意图 #%d run %d 收尾失败: %v", t.ID, intent.ID, runID, err)
		return
	}
	e.saveHandoff(t, intent, runID, started, harness.TerminalReason("interrupted"), "interrupted", "crash", agent.WriteCounts{})
}

// saveHandoff 机械抽取交接单：活动流 + 写回计数 + 意图原文 + 本次 run 的模型补注。
func (e *Engine) saveHandoff(t *Task, intent *db.Node, runID int64, started time.Time, reason harness.TerminalReason, state, stopReason string, wrote agent.WriteCounts) {
	acts, err := t.Store.ActivityTrace(intent.ID, 400)
	if err != nil {
		log.Printf("[handoff] task %s 意图 #%d 读活动流失败: %v", t.ID, intent.ID, err)
		acts = nil
	}
	var firstID, lastID int64
	lastOutput := ""
	for _, a := range acts {
		if firstID == 0 {
			firstID = a.ID
		}
		lastID = a.ID
		if a.Kind == "result" && strings.TrimSpace(a.Summary) != "" {
			lastOutput = a.Summary
		}
	}
	tailSrc := acts
	if n := len(tailSrc); n > 20 {
		tailSrc = tailSrc[n-20:]
	}
	tail := make([]handoffStep, 0, len(tailSrc))
	for _, a := range tailSrc {
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
	// 额度用尽：把模型被强制写出的报告挂到交接单上，并把触发原因改写成 tool_quota
	// ——「这条意图为什么停」比「正常结束」重要得多，下一轮要靠它决定接不接。
	if wrote.Quota != nil {
		rec.Quota = &handoffQuota{
			Cap:     wrote.Quota.Cap,
			Calls:   wrote.Quota.Calls,
			Refused: wrote.Quota.Refused,
			Report:  clipText(wrote.Quota.Report, 6000),
		}
		rec.Trigger.Kind = "tool_quota"
		rec.Trigger.Detail = fmt.Sprintf("工具额度 %d/%d 用尽（拒绝 %d 次）", wrote.Quota.Calls, wrote.Quota.Cap, wrote.Quota.Refused)
	}
	// L2：这次 run 期间模型主动补的注。读失败不影响 L1 落库。
	if ow, err := e.m.pg.OpenWorkSince(t.ID, intent.ID, started); err == nil && len(ow) > 0 {
		rec.OpenWork = mergeOpenWork(ow)
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		log.Printf("[handoff] task %s 意图 #%d 序列化失败: %v", t.ID, intent.ID, err)
		return
	}
	// mech_sha256 只覆盖事实层：把 open_work 摘掉再算，模型改口也动不了这个指纹。
	mech := rec
	mech.OpenWork = nil
	mech.Quota = nil // 额度报告是模型写的，不进事实层指纹
	mechRaw, _ := json.Marshal(mech)
	sum := sha256.Sum256(mechRaw)
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

// mergeOpenWork 把多行补注按类别合并去重（保序）。
func mergeOpenWork(rows []db.IntentOpenWork) *handoffOpenWork {
	out := &handoffOpenWork{Advisory: true}
	seen := map[string]bool{}
	add := func(dst *[]string, s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		*dst = append(*dst, s)
	}
	for _, r := range rows { // 旧 → 新
		add(&out.NextActions, r.NextActions)
		add(&out.Hypotheses, r.Hypotheses)
		add(&out.NegativeResults, r.NegativeResults)
	}
	if len(out.NextActions) == 0 && len(out.Hypotheses) == 0 && len(out.NegativeResults) == 0 {
		return nil
	}
	return out
}

func traceRange(first, last int64) []int64 {
	if first == 0 && last == 0 {
		return nil
	}
	return []int64{first, last}
}

// renderHandoffText 生成注入给后续子代理的紧凑文本（≤6KB）。
// 事实层在前、主观层在后，且主观层带明确的可推翻标签——模型不该把上一任的猜测
// 当成已验证的结论。
func renderHandoffText(rec handoffRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n【交接单 · 来自意图 #%d（%s）】\n", rec.Source.IntentID, handoffTriggerLabel(rec.Trigger.Kind))
	b.WriteString("── 事实（由系统从执行记录直读，可信）──\n")
	fmt.Fprintf(&b, "已写回：事实 %d / 资产 %d / 漏洞 %d；共 %d 步\n",
		rec.Progress.Written["facts"], rec.Progress.Written["assets"],
		rec.Progress.Written["findings"], rec.Progress.StepsTotal)
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
	if rec.OpenWork != nil {
		b.WriteString("── 上一任的主观判断（未验证，可推翻；不要当成结论）──\n")
		for _, s := range rec.OpenWork.NextActions {
			fmt.Fprintf(&b, "  · 下一步: %s\n", clipText(s, 200))
		}
		for _, s := range rec.OpenWork.Hypotheses {
			fmt.Fprintf(&b, "  · 假设: %s\n", clipText(s, 200))
		}
		for _, s := range rec.OpenWork.NegativeResults {
			fmt.Fprintf(&b, "  · 已排除: %s\n", clipText(s, 200))
		}
	}
	if rec.Quota != nil && strings.TrimSpace(rec.Quota.Report) != "" {
		b.WriteString("── 额度用尽报告（模型自述，未经核实，可推翻）──\n")
		b.WriteString(strings.TrimSpace(rec.Quota.Report))
		b.WriteString("\n")
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
	case "llm_quota_exhausted":
		return "LLM 配额耗尽"
	case "tool_quota":
		return "工具额度用尽"
	case "crash", "interrupted":
		return "进程中断"
	case "human_stop":
		return "人工终止"
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

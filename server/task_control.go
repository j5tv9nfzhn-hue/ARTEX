package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

const maxBatchControlIDs = 100

// 任务与意图控制的中文错误；保留 paused 等枚举和 %w 包装。
// applyTaskControlWithCause 用于单条、批量控制，编排 pause 工具
// 再次读取 err.Error() 时也使用相同消息。
const (
	errTaskCtrlDeleting      = "任务正在删除，无法进行控制"
	errTaskCtrlTerminalPause = "已结束的任务无法暂停"
	errTaskCtrlAlreadyPaused = "任务已处于暂停状态"
	errTaskCtrlBatchSizeFmt  = "task_ids 的数量必须为 1~%d 个"

	errIntentCtrlInheritedReadonly = "继承的意图为只读，无法控制"
	errIntentCtrlOnlyRunningPause  = "只能暂停正在运行的意图"
	errIntentCtrlOnlyPausedResume  = "只能恢复已暂停的意图"
	errIntentCtrlStateConflictFmt  = "%w：该意图已不再处于 paused 状态"
	errIntentCtrlOnlyDeletable     = "只能删除处于等待中、运行中或已暂停状态的意图"
	errIntentCtrlReasonRequired    = "请输入删除原因"
)

type taskControlResult struct {
	ID     string `json:"id"`
	Paused bool   `json:"paused"`
	Queued bool   `json:"queued"`
	Status string `json:"status"`
}

type intentControlResult struct {
	ID      int64             `json:"id"`
	State   string            `json:"state"`
	Deleted *db.IntentCleanup `json:"deleted,omitempty"`
}

// parsedTaskID carries one requested batch id together with whether it parsed.
// Invalid ids are kept rather than dropped so the response can name them.
type parsedTaskID struct {
	id    string
	valid bool
}

// normalizeBatchTaskIDs trims, canonicalizes and de-duplicates the ids of one
// batch request while preserving the caller's order. Shared by every batch
// endpoint so they agree on what counts as a duplicate.
func normalizeBatchTaskIDs(raw []string) []parsedTaskID {
	seen := map[string]bool{}
	seenInvalid := map[string]bool{}
	taskIDs := make([]parsedTaskID, 0, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		id, valid := canonicalTaskID(trimmed)
		if !valid {
			if seenInvalid[trimmed] {
				continue
			}
			seenInvalid[trimmed] = true
			taskIDs = append(taskIDs, parsedTaskID{id: trimmed})
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		taskIDs = append(taskIDs, parsedTaskID{id: id, valid: true})
	}
	return taskIDs
}

type batchControlItem struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Status string `json:"status,omitempty"`
	Queued bool   `json:"queued,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (s *Server) resumeAdmissionMode(t *Task) string {
	if t == nil {
		return "resume"
	}
	lifecycle := t.lifecycleSnapshot()
	if lifecycle.QueueMode == "bootstrap" {
		return "bootstrap"
	}
	if lifecycle.FirstRunAt == 0 {
		goals, err := t.Store.ListByKind(db.KindGoal, 1)
		if err == nil && len(goals) == 0 {
			return "bootstrap"
		}
	}
	return "resume"
}

// applyTaskControl is shared by the single and batch endpoints. Task resume is
// deliberately limited to paused tasks; reruns and finding follow-ups use
// admitTask directly when they need to revive a terminal task.
func (s *Server) applyTaskControl(t *Task, action string) (taskControlResult, error) {
	return s.applyTaskControlWithCause(t, action, agent.AbortPausedByUser)
}

func (s *Server) applyTaskControlWithCause(t *Task, action string, pauseCause error) (taskControlResult, error) {
	if t == nil {
		return taskControlResult{}, fmt.Errorf("task not found")
	}
	out := taskControlResult{ID: t.ID}
	switch action {
	case "pause":
		s.concMu.Lock()
		defer s.concMu.Unlock()
		current, exists := s.m.Task(t.ID)
		if !exists || current != t || s.engine.IsDeleting(t.ID) {
			return out, fmt.Errorf(errTaskCtrlDeleting)
		}
		if !s.engine.beginTaskOperation(t.ID) {
			return out, fmt.Errorf(errTaskCtrlDeleting)
		}
		defer s.engine.decInflight(t.ID)
		lifecycle := t.lifecycleSnapshot()
		if isTerminalStatus(lifecycle.Status) {
			return out, fmt.Errorf(errTaskCtrlTerminalPause)
		}
		if lifecycle.Paused {
			return out, fmt.Errorf(errTaskCtrlAlreadyPaused)
		}
		wasQueued := lifecycle.Queued
		wasEnginePaused := s.engine.IsPaused(t.ID)
		if pauseCause == nil {
			pauseCause = agent.AbortPausedByUser
		}
		s.engine.Pause(t.ID, pauseCause)
		if err := s.m.ApplyTaskPauseReason(t.ID, pauseCauseReason(pauseCause)); err != nil {
			if !wasEnginePaused && !wasQueued {
				s.engine.Resume(t)
			}
			return out, err
		}
		// Main Agent is independently cancellable. Only cancel its current turn
		// after the persistent pause commits, so a failed control request is fully
		// compensated and does not lose an otherwise valid conversation turn.
		s.cancelTaskChat(t.ID, agent.AbortChatPausedWithTask)
		// 中断联动（批次 6 / #27、#28）：打断在跑的后台压缩（它刻意不随规划轮
		// 结束而停），并落一个可回滚的中断检查点。
		s.interruptTaskCompaction(t, agent.AbortPausedByUser)
		s.captureTaskCheckpoint(t, db.CheckpointReasonInterrupt, "任务已暂停（中断）", false)
		out.Paused, out.Status = true, "paused"
		go s.reconcileConcurrency()
	case "resume":
		queued, err := s.admitPausedTask(t)
		if err != nil {
			return out, err
		}
		out.Queued = queued
		out.Status = map[bool]string{true: "queued", false: "running"}[queued]
	default:
		return out, fmt.Errorf("action must be pause|resume")
	}
	log.Printf("[task] #%s %s", t.ID, logT(map[string]string{"pause": "已暂停", "resume": "已恢复"}[action]))
	return out, nil
}

// intentSummaryOf 从意图 payload 读取 summary，
// 让硬删除通知能在节点消失之前保留该摘要。
func intentSummaryOf(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok {
			return s
		}
	}
	return ""
}

func (s *Server) applyIntentControl(ctx context.Context, t *Task, iid int64, action, reason, mode string) (intentControlResult, error) {
	out := intentControlResult{ID: iid}
	node, err := t.Store.GetNode(iid)
	if err != nil {
		return out, err
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			return out, fmt.Errorf(errIntentCtrlInheritedReadonly)
		}
		return out, fmt.Errorf("intent not found")
	}
	if node.Kind != db.KindIntent {
		return out, fmt.Errorf("node is not an intent")
	}
	switch action {
	case "pause":
		if node.State != "running" {
			return out, fmt.Errorf(errIntentCtrlOnlyRunningPause)
		}
		if err := s.engine.ControlWork(ctx, iid, "pause"); err != nil {
			return out, err
		}
		out.State = "paused"
	case "resume":
		if node.State != "paused" {
			return out, fmt.Errorf(errIntentCtrlOnlyPausedResume)
		}
		changed, err := t.Store.CompareAndSetIntentState(iid, "paused", "open")
		if err != nil {
			return out, err
		}
		if !changed {
			return out, fmt.Errorf(errIntentCtrlStateConflictFmt, db.ErrIntentStateConflict)
		}
		t.Notify()
		out.State = "open"
	case "cancel":
		// 删除有两种模式：
		// soft（默认）将意图设为 state=deleted，并写入 delete_reason，
		// 保留意图、产物、lineage，不额外挂载 fact。
		// hard 物理级联删除该意图及仅由它支撑的专属后代，避免孤立数据；
		// 共享节点、goal、任务根事实保留。两种模式都向 Planner 发出 cancelled，
		// 附带意图内容与删除原因，触发重新规划。
		if node.State != "running" && node.State != "paused" && node.State != "open" {
			return out, fmt.Errorf(errIntentCtrlOnlyDeletable)
		}
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return out, fmt.Errorf(errIntentCtrlReasonRequired)
		}
		if node.State == "running" {
			if err := s.engine.ControlWork(ctx, iid, "cancel"); err != nil {
				return out, err
			}
		}
		summary := intentSummaryOf(node)
		if mode == "hard" {
			cleanup, err := t.Store.CancelIntent(iid)
			if err != nil {
				return out, err
			}
			s.cancelWorkerSide(t.ID, t.ExpID, iid)
			t.NotifyCancelled(iid, summary, reason)
			out.Deleted = &cleanup
			out.State = "" // 节点已删除；前端看到 Deleted 后从列表移除。
		} else {
			if _, err := t.Store.SoftDeleteIntent(iid, reason); err != nil {
				return out, err
			}
			s.cancelWorkerSide(t.ID, t.ExpID, iid)
			t.NotifyCancelled(iid, summary, reason)
			out.State = db.StateIntentDeleted
		}
	case "abandon":
		// 人工收口：把一条没跑完的分支标记为「已人工结案」（写 intent_closures 审计行）。
		// 不删意图、不改状态、不丢产物；只是从此不再算未收口，完成闸门不会再拦任务。
		// 用于回炉到上限后的兜底——这时只能由人来判断「这条方向确实不做了」。
		if err := s.m.CloseBranch(t, iid, reason); err != nil {
			return out, err
		}
		s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: fmt.Sprintf("意图 #%d 已人工收口（不再算未收口分支）：%s", iid, strings.TrimSpace(reason))})
		out.State = node.State // 状态不变，只是收口留痕
	default:
		return out, fmt.Errorf("action must be pause|resume|cancel|abandon")
	}
	return out, nil
}

func (s *Server) controlTasksBatch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var req struct {
		TaskIDs []string `json:"task_ids"`
		Action  string   `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.Action != "pause" && req.Action != "resume" {
		writeErr(w, 400, "action must be pause|resume")
		return
	}
	taskIDs := normalizeBatchTaskIDs(req.TaskIDs)
	if len(taskIDs) == 0 || len(taskIDs) > maxBatchControlIDs {
		writeErr(w, 400, fmt.Sprintf(trMsg(errTaskCtrlBatchSizeFmt), maxBatchControlIDs))
		return
	}
	items := make([]batchControlItem, 0, len(taskIDs))
	for _, parsed := range taskIDs {
		item := batchControlItem{ID: parsed.id}
		if !parsed.valid {
			item.Error = "bad task id"
			items = append(items, item)
			continue
		}
		t, ok := s.m.Task(parsed.id)
		if !ok {
			item.Error = "task not found"
			items = append(items, item)
			continue
		}
		result, err := s.applyTaskControl(t, req.Action)
		if err != nil {
			item.Error = err.Error()
		} else {
			item.OK, item.Status, item.Queued = true, result.Status, result.Queued
		}
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

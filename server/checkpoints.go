package server

// 批次 6 / #29–#37：检查点时间线与回滚。
//
// 设计（与 db/checkpoints.go 的语义一致）：平台在时间线上的关键点落一个「轻快照」
// ——活动 id 边界、transcript 文件大小、图计数/意图状态——回滚到某个检查点意味着
// 把之后的时间线隐藏（reverted_at 而非删除）、把 transcript 截断回快照大小、把
// 未完成意图重新置为待执行。工具副作用和已落库的图产物不回滚（UI 明示）。
//
// 捕捉点：
//   会话：用户消息落库后（user_message）、回合结束（turn_complete）、手动停止（interrupt）
//   任务：准入/恢复（admit）、意图终态（intent_settled）、任务暂停（interrupt）、
//         压缩被中断/重启恢复（interrupt / compaction_recover）
//
// 压缩联动（#27/#28/#37）：平台内可触及的压缩是 agent.Compactor（冷节点 digest）。
// 任务暂停时显式打断在跑的压缩；中断/重启后的恢复校验利用「AddDigest 事务原子」
// 的性质——图要么含该 digest 要么完全不含——只需校验覆盖边一致性并记录。

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func convCheckpointScope(id int64) string { return "conv-" + strconv.FormatInt(id, 10) }

func taskCheckpointScope(taskID string) string { return "task-" + taskID }

// transcriptFile mirrors transcript.Store.MainPath for a top-level session.
func (s *Server) transcriptFile(sessionID string) string {
	return filepath.Join(s.m.dir, "transcripts", sessionID+".jsonl")
}

func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return 0
	}
	return fi.Size()
}

var intentStateLabels = map[string]string{
	"open":      "已回到待执行",
	"running":   "执行中",
	"paused":    "已暂停",
	"done":      "已完成",
	"blocked":   "受阻",
	"exhausted": "预算耗尽",
	"stopped":   "被终止",
	"deleted":   "已删除",
}

var checkpointReasonLabels = map[string]string{
	db.CheckpointReasonUserMessage:       "用户消息",
	db.CheckpointReasonTurnComplete:      "回合结束",
	db.CheckpointReasonAdmit:             "任务准入",
	db.CheckpointReasonIntentSettled:     "意图结束",
	db.CheckpointReasonInterrupt:         "中断",
	db.CheckpointReasonCompactionRecover: "压缩恢复校验",
	db.CheckpointReasonRevert:            "回滚标记",
	db.CheckpointReasonManual:            "手动",
}

func checkpointReasonLabel(reason string) string {
	if l := checkpointReasonLabels[reason]; l != "" {
		return l
	}
	return reason
}

// --- capture ---

// captureConversationCheckpoint appends one checkpoint to a conversation timeline.
// boundaryActivityID = newest conversation_activities.id INCLUDED in the snapshot
// (revert hides ids greater than it). TranscriptSize is read at capture time.
func (s *Server) captureConversationCheckpoint(c *db.Conversation, reason, summary string, boundaryActivityID int64) *db.Checkpoint {
	if s == nil || s.m == nil || s.m.pg == nil || c == nil {
		return nil
	}
	path := s.transcriptFile(s.convBusyKey(c.ID))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cp, err := s.m.pg.CaptureCheckpoint(ctx, convCheckpointScope(c.ID), reason, summary, db.CheckpointSnapshot{
		ActivityID:     boundaryActivityID,
		TranscriptSize: fileSize(path),
		TranscriptPath: path,
	})
	if err != nil {
		log.Printf("[checkpoints] conv %d 快照落盘失败: %v", c.ID, err)
	}
	return cp
}

// captureConversationCheckpointNow captures with the current newest activity id
// as the boundary (used at points where "everything so far" is the snapshot).
func (s *Server) captureConversationCheckpointNow(c *db.Conversation, reason, summary string) *db.Checkpoint {
	if s == nil || s.m == nil || s.m.pg == nil || c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	maxAct, err := s.m.pg.MaxConversationActivityID(ctx, c.ID)
	if err != nil {
		log.Printf("[checkpoints] conv %d 活动边界读取失败: %v", c.ID, err)
		return nil
	}
	return s.captureConversationCheckpoint(c, reason, summary, maxAct)
}

// convFinishLabel maps the runner's finishStatus onto operator-readable Chinese.
func convFinishLabel(status string) string {
	switch status {
	case "completed":
		return "完成"
	case "stopped":
		return "已停止"
	case "failed":
		return "失败"
	default:
		return status
	}
}

// captureTaskCheckpoint appends one checkpoint to a task timeline. includeGraph
// adds the node/edge counters + intent-state map (admission points); per-intent
// settles skip it to stay cheap. Planner transcript size is always recorded.
func (s *Server) captureTaskCheckpoint(t *Task, reason, summary string, includeGraph bool) *db.Checkpoint {
	if s == nil || s.m == nil || s.m.pg == nil || t == nil {
		return nil
	}
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || taskID <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	maxAct, err := s.m.pg.MaxExplorationActivityID(ctx, t.ExpID)
	if err != nil {
		log.Printf("[checkpoints] task %s 活动边界读取失败: %v", t.ID, err)
		return nil
	}
	snap := db.CheckpointSnapshot{ActivityID: maxAct}
	snap.TranscriptPath = s.transcriptFile(fmt.Sprintf("exp%d-planner", t.ExpID))
	snap.TranscriptSize = fileSize(snap.TranscriptPath)
	if includeGraph {
		counts, intents, err := s.m.pg.ExplorationGraphCounts(ctx, t.ExpID)
		if err == nil {
			snap.NodeID = counts["max_node_id"]
			snap.Counts = counts
			snap.Intents = intents
		}
	}
	cp, err := s.m.pg.CaptureCheckpoint(ctx, taskCheckpointScope(t.ID), reason, summary, snap)
	if err != nil {
		log.Printf("[checkpoints] task %s 快照落盘失败: %v", t.ID, err)
	}
	return cp
}

// captureCompactionCheckpoint records a compaction interrupt/recovery on the task
// timeline, embedding the compaction boundary info in the snapshot.
func (s *Server) captureCompactionCheckpoint(t *Task, summary string, info map[string]any) *db.Checkpoint {
	if s == nil || s.m == nil || s.m.pg == nil || t == nil {
		return nil
	}
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || taskID <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	maxAct, _ := s.m.pg.MaxExplorationActivityID(ctx, t.ExpID)
	snap := db.CheckpointSnapshot{ActivityID: maxAct, Compaction: info}
	snap.TranscriptPath = s.transcriptFile(fmt.Sprintf("exp%d-planner", t.ExpID))
	snap.TranscriptSize = fileSize(snap.TranscriptPath)
	reason := db.CheckpointReasonInterrupt
	if kind, _ := info["kind"].(string); kind == "recover" {
		reason = db.CheckpointReasonCompactionRecover
	}
	cp, err := s.m.pg.CaptureCheckpoint(ctx, taskCheckpointScope(t.ID), reason, summary, snap)
	if err != nil {
		log.Printf("[checkpoints] task %s 压缩快照落盘失败: %v", t.ID, err)
	}
	return cp
}

// onIntentSettled is the Engine's settle hook (SetIntentSettledHook): capture a
// timeline checkpoint after every intent run reaches a settled state.
func (s *Server) onIntentSettled(t *Task, intentID int64, state string) {
	label := intentStateLabels[state]
	if label == "" {
		label = state
	}
	s.captureTaskCheckpoint(t, db.CheckpointReasonIntentSettled,
		fmt.Sprintf("意图 #%d 结束：%s", intentID, label), false)
}

// --- compaction linkage (#27/#28/#37) ---

// compactionObserver returns the lifecycle observer installed on one task's cold
// compactor: start → compaction_state=phase running; finish → idle, or on a
// cancellation/timeout interrupted + a timeline checkpoint.
func (s *Server) compactionObserver(t *Task) func(agent.CompactionEvent) {
	scope := taskCheckpointScope(t.ID)
	return func(ev agent.CompactionEvent) {
		if s == nil || s.m == nil || s.m.pg == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		switch ev.Stage {
		case "start":
			if err := s.m.pg.PutCompactionState(ctx, scope, db.CompactionPhaseRunning, map[string]any{
				"phase": ev.Phase, "info": ev.Info,
			}); err != nil {
				log.Printf("[compaction] task %s 状态落盘失败: %v", t.ID, err)
			}
		case "finish":
			boundary := map[string]any{"phase": ev.Phase, "info": ev.Info}
			if ev.Err != nil {
				boundary["error"] = ev.Err.Error()
				_ = s.m.pg.PutCompactionState(ctx, scope, db.CompactionPhaseInterrupted, boundary)
				s.captureCompactionCheckpoint(t, fmt.Sprintf(
					"压缩被中断（%s）：图保持压缩前一致性（digest 提交是事务性的），未完成分块将在下一轮压缩重做。",
					ev.Err.Error()), boundary)
			} else {
				_ = s.m.pg.PutCompactionState(ctx, scope, db.CompactionPhaseIdle, boundary)
			}
		}
	}
}

// interruptTaskCompaction cancels an in-flight background compaction when the
// task is paused/stopped. Compaction deliberately outlives a planner round, so
// nothing else would stop it.
func (s *Server) interruptTaskCompaction(t *Task, cause error) {
	if s == nil || t == nil {
		return
	}
	b := s.agentsForTask(t)
	if b == nil || b.pl == nil {
		return
	}
	if c := b.pl.Compactor(); c != nil && t.ExpID > 0 {
		if c.Interrupt(t.ExpID, cause) {
			log.Printf("[compaction] task %s 压缩已随任务暂停打断", t.ID)
		}
	}
}

// recoverCompactionState is the batch-6 #37 recovery consistency check, run for
// every reloaded task at startup: a persisted phase=running can only be stale
// (the in-process compactor state died with the previous process). Verification
// exploits transactional digest commits: the graph is always consistent — the
// check just confirms the covers set and records the outcome on the timeline.
func (s *Server) recoverCompactionState(t *Task) {
	if s == nil || s.m == nil || s.m.pg == nil || t == nil {
		return
	}
	scope := taskCheckpointScope(t.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := s.m.pg.GetCompactionState(ctx, scope)
	if err != nil || st == nil || st.Phase != db.CompactionPhaseRunning {
		return
	}
	boundary := map[string]any{"kind": "recover", "info": st.Boundary}
	if st.StartedAt != nil {
		boundary["stale_since"] = st.StartedAt.UTC().Format(time.RFC3339)
	}
	issues, verr := t.Store.DigestCoverageIssues()
	if verr != nil {
		boundary["verify_error"] = verr.Error()
	}
	if len(issues) > 0 {
		boundary["issues"] = issues
	}
	_ = s.m.pg.PutCompactionState(ctx, scope, db.CompactionPhaseInterrupted, boundary)
	var summary string
	switch {
	case verr != nil:
		summary = "发现上次进程遗留的压缩中状态，一致性校验读取失败（" + verr.Error() + "）；未完成分块将在下一轮压缩重做。"
	case len(issues) > 0:
		summary = "发现上次进程遗留的压缩中状态，一致性校验发现问题：" + strings.Join(issues, "；") + "；未完成分块将在下一轮压缩重做。"
	default:
		summary = "发现上次进程遗留的压缩中状态，一致性校验通过（digest 与覆盖边一致）；未完成分块将在下一轮压缩重做。"
	}
	log.Printf("[compaction] task %s 压缩恢复校验: %s", t.ID, summary)
	s.captureCompactionCheckpoint(t, summary, boundary)
}

// --- HTTP: conversation checkpoints ---

func (s *Server) pgListConversationCheckpoints(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	items, err := pg.ListCheckpoints(r.Context(), convCheckpointScope(c.ID), 80)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"checkpoints": items})
}

// pgRevertConversationCheckpoint rolls the conversation back to one checkpoint:
// hide the steps after the boundary, truncate the transcript to the recorded
// size, supersede newer checkpoints and append a revert marker.
func (s *Server) pgRevertConversationCheckpoint(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	cid, ok := pathInt(r, "cid")
	if !ok || cid <= 0 {
		writeErr(w, 400, "bad checkpoint id")
		return
	}
	ckpt, err := pg.GetCheckpoint(r.Context(), cid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ckpt == nil || ckpt.Scope != convCheckpointScope(c.ID) {
		writeErr(w, 404, "检查点不存在")
		return
	}
	if ckpt.Status != db.CheckpointActive {
		writeErr(w, 409, "该检查点已被后续回滚作废，请选择更新的检查点")
		return
	}
	busyKey := s.convBusyKey(c.ID)
	s.chatMu.Lock()
	busy := s.chatBusy[busyKey]
	s.chatMu.Unlock()
	if busy {
		writeErr(w, 409, "当前会话有一轮正在运行，请先停止再回滚")
		return
	}
	hidden, err := pg.HideConversationActivitiesAfter(r.Context(), c.ID, ckpt.Snapshot.ActivityID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	superseded, err := pg.SupersedeCheckpointsAfter(r.Context(), ckpt.Scope, ckpt.Seq)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	truncated := s.truncateTranscript(ckpt.Snapshot.TranscriptPath, ckpt.Snapshot.TranscriptSize)
	marker := s.captureConversationCheckpoint(c, db.CheckpointReasonRevert,
		fmt.Sprintf("回滚到检查点 #%d（%s）", ckpt.Seq, checkpointReasonLabel(ckpt.Reason)), ckpt.Snapshot.ActivityID)
	log.Printf("[checkpoints] conv %d 回滚到 #%d：隐藏 %d 步，作废 %d 个后续检查点", c.ID, ckpt.Seq, hidden, superseded)
	writeJSON(w, 200, map[string]any{
		"status": "reverted", "hidden": hidden, "superseded": superseded,
		"transcript_truncated": truncated, "checkpoint": marker,
	})
}

// --- HTTP: task checkpoints ---

func (s *Server) taskCheckpoints(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskByPath(w, r)
	if !ok {
		return
	}
	items, err := s.m.pg.ListCheckpoints(r.Context(), taskCheckpointScope(t.ID), 80)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"checkpoints": items})
}

// revertTaskCheckpoint rolls a task's timeline back to one checkpoint: hide later
// activities, reopen running/paused intents so a resume re-claims them, truncate
// the planner transcript, supersede newer checkpoints and append a revert marker.
// Requires the task not be actively running (pause it first); tool side effects
// and graph products written after the boundary are NOT rolled back.
func (s *Server) revertTaskCheckpoint(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskByPath(w, r)
	if !ok {
		return
	}
	cid, ok := pathInt(r, "cid")
	if !ok || cid <= 0 {
		writeErr(w, 400, "bad checkpoint id")
		return
	}
	ckpt, err := s.m.pg.GetCheckpoint(r.Context(), cid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ckpt == nil || ckpt.Scope != taskCheckpointScope(t.ID) {
		writeErr(w, 404, "检查点不存在")
		return
	}
	if ckpt.Status != db.CheckpointActive {
		writeErr(w, 409, "该检查点已被后续回滚作废，请选择更新的检查点")
		return
	}
	if s.occupiesConcurrencySlot(t) {
		writeErr(w, 409, "任务正在运行，请先暂停后再回滚时间线")
		return
	}
	hidden, err := s.m.pg.HideExplorationActivitiesAfter(r.Context(), t.ExpID, ckpt.Snapshot.ActivityID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	reopened, err := s.m.pg.ReopenInterruptedIntents(r.Context(), t.ExpID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	superseded, err := s.m.pg.SupersedeCheckpointsAfter(r.Context(), ckpt.Scope, ckpt.Seq)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	truncated := s.truncateTranscript(ckpt.Snapshot.TranscriptPath, ckpt.Snapshot.TranscriptSize)
	if hidden > 0 || len(reopened) > 0 {
		s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: fmt.Sprintf("时间线已回滚到检查点 #%d：隐藏 %d 个步骤，重排 %d 条未完成意图（恢复任务后继续执行）。",
				ckpt.Seq, hidden, len(reopened))})
	}
	marker := s.captureTaskCheckpoint(t, db.CheckpointReasonRevert,
		fmt.Sprintf("回滚到检查点 #%d（%s）", ckpt.Seq, checkpointReasonLabel(ckpt.Reason)), false)
	log.Printf("[checkpoints] task %s 回滚到 #%d：隐藏 %d 步，重排 %d 条意图，作废 %d 个检查点",
		t.ID, ckpt.Seq, hidden, len(reopened), superseded)
	writeJSON(w, 200, map[string]any{
		"status": "reverted", "hidden": hidden, "reopened": reopened,
		"superseded": superseded, "transcript_truncated": truncated, "checkpoint": marker,
	})
}

// taskByPath resolves {id} to a live manager task (404 when unknown/deleted).
func (s *Server) taskByPath(w http.ResponseWriter, r *http.Request) (*Task, bool) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad task id")
		return nil, false
	}
	t, exists := s.m.Task(strconv.FormatInt(id, 10))
	if !exists || t == nil {
		writeErr(w, 404, "任务不存在")
		return nil, false
	}
	return t, true
}

// truncateTranscript cuts a transcript file back to a snapshot size when it has
// grown past it. Paths are server-constructed, but re-validate the location so a
// corrupt snapshot can never touch an arbitrary file. Returns whether a
// truncation happened.
func (s *Server) truncateTranscript(path string, size int64) bool {
	if path == "" || s.m == nil {
		return false
	}
	root := filepath.Clean(filepath.Join(s.m.dir, "transcripts"))
	clean := filepath.Clean(path)
	if !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		log.Printf("[checkpoints] 拒绝截断 transcripts 之外的路径: %s", path)
		return false
	}
	fi, err := os.Stat(clean)
	if err != nil || fi.IsDir() {
		return false
	}
	if fi.Size() <= size {
		return false
	}
	if err := os.Truncate(clean, size); err != nil {
		log.Printf("[checkpoints] transcript 截断失败 %s: %v", clean, err)
		return false
	}
	return true
}

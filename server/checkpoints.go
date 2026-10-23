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
//         压缩被中断/重启恢复（interrupt / compaction_recover）、主 Agent 用户消息（user_message）
//
// 消息级回滚（pgRollbackConversationMessage / rollbackTaskMessage，气泡下方的回滚
// 箭头）：隐藏该用户消息及其之后的对话步骤、transcript 截断回该回合之前、后续检查点
// 作废，消息原文由前端回填输入框（本体不删）。边界取「该消息之前最后一个可见活动」
// 而非「上一个检查点」，因此首条消息也能一路回滚到空时间线，且与被跳过的失败回合
// 无关。两者的范围都只是「对话」：会话回滚只动 conversation_activities，任务里的
// 主 Agent 回滚只动 worker=mainagent 的行 —— 任务进度、意图状态与 Worker/规划器
// 记录一律不回退，所以也不需要先暂停任务。
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
	"exhausted": "步数耗尽",
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
//
// 所有落盘都走 sqliteWriteTimeout（见 sqlite_write.go）：检查点是回滚时间线的
// 事实源，锁竞争时这些写入必须排队等待而不是被更短的 ctx 打断——落盘失败没有
// 重试，丢失的检查点会直接表现为「回滚时间线上缺了一段」。

// captureConversationCheckpoint appends one checkpoint to a conversation timeline.
// boundaryActivityID = newest conversation_activities.id INCLUDED in the snapshot
// (revert hides ids greater than it). TranscriptSize is read at capture time.
func (s *Server) captureConversationCheckpoint(c *db.Conversation, reason, summary string, boundaryActivityID int64) *db.Checkpoint {
	if s == nil || s.m == nil || s.m.pg == nil || c == nil {
		return nil
	}
	path := s.transcriptFile(s.convBusyKey(c.ID))
	ctx, cancel := context.WithTimeout(context.Background(), sqliteWriteTimeout)
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
	ctx, cancel := context.WithTimeout(context.Background(), sqliteWriteTimeout)
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
	ctx, cancel := context.WithTimeout(context.Background(), sqliteWriteTimeout)
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

// mainAgentTranscriptPath mirrors agent/mainagent.go's session naming: segment 0
// keeps the legacy "exp<id>-main" transcript file, later segments suffix "-s<seg>".
func (s *Server) mainAgentTranscriptPath(t *Task, seg int) string {
	session := fmt.Sprintf("exp%d-main", t.ExpID)
	if seg > 0 {
		session = fmt.Sprintf("exp%d-main-s%d", t.ExpID, seg)
	}
	return s.transcriptFile(session)
}

// captureTaskMainMessageCheckpoint appends one checkpoint to a task timeline for
// a main-agent user turn (reason=user_message): boundary = the human turn's
// activity id, snapshot transcript = the MAIN agent transcript of that segment,
// sized before the turn ran. rollbackTaskMessage uses it to truncate the main
// agent's LLM context back to "just before this message".
func (s *Server) captureTaskMainMessageCheckpoint(t *Task, seg int, summary string, activityID int64) *db.Checkpoint {
	if s == nil || s.m == nil || s.m.pg == nil || t == nil {
		return nil
	}
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || taskID <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), sqliteWriteTimeout)
	defer cancel()
	path := s.mainAgentTranscriptPath(t, seg)
	cp, err := s.m.pg.CaptureCheckpoint(ctx, taskCheckpointScope(t.ID), db.CheckpointReasonUserMessage, summary, db.CheckpointSnapshot{
		ActivityID:     activityID,
		TranscriptSize: fileSize(path),
		TranscriptPath: path,
	})
	if err != nil {
		log.Printf("[checkpoints] task %s 主 Agent 消息快照落盘失败: %v", t.ID, err)
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
	ctx, cancel := context.WithTimeout(context.Background(), sqliteWriteTimeout)
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
	// 应用内通知：#N 子代理的结局。过程态（open/running/paused/deleted）在
	// emitSubagentMilestone 里被过滤掉，只留 done/blocked/exhausted/stopped。
	if t != nil {
		title := t.Name
		if title == "" {
			title = t.Description
		}
		emitSubagentMilestone(t.ID, title, intentID, state)
	}
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
		ctx, cancel := context.WithTimeout(context.Background(), sqliteWriteTimeout)
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
	ctx, cancel := context.WithTimeout(context.Background(), sqliteWriteTimeout)
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
	if s.isChatKeyBusy(busyKey) {
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

// --- HTTP: message-level rollback（消息气泡下方的回滚箭头）---

// findUserMessageCheckpoint returns the user_message checkpoint captured for one
// message (boundary == its activity id): its snapshot records the transcript
// size right before that turn ran, which is exactly where a "roll back to
// before this message" truncates. Nil when the checkpoint was pruned/never
// captured — the caller then skips truncation and logs.
func findUserMessageCheckpoint(items []db.Checkpoint, seq int64) *db.Checkpoint {
	for i := range items {
		if items[i].Reason == db.CheckpointReasonUserMessage && items[i].Snapshot.ActivityID == seq {
			return &items[i]
		}
	}
	return nil
}

// supersedeCheckpointsInRegion marks every ACTIVE checkpoint whose snapshot
// boundary points into the hidden region (activity_id > boundary — it saw steps
// the rollback just removed) as superseded. Checkpoints at or behind the
// boundary still describe visible state — including older revert markers whose
// boundary was explicit — and stay active. Returns the number newly superseded.
func (s *Server) supersedeCheckpointsInRegion(ctx context.Context, items []db.Checkpoint, boundary int64) (int64, error) {
	var n int64
	for _, cp := range items {
		if cp.Status != db.CheckpointActive || cp.Snapshot.ActivityID <= boundary {
			continue
		}
		ok, err := s.m.pg.SupersedeCheckpoint(ctx, cp.ID)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// pgRollbackConversationMessage rolls a conversation back to the state just
// before one user message: hide that message and every later step, truncate the
// transcript back to the size recorded before that turn, supersede the
// checkpoints pointing into the hidden region and append a revert marker. The
// message body is NOT deleted — the UI refills the composer with it. The
// boundary is the newest visible activity BELOW the message, so even the first
// message rolls back cleanly (to an empty timeline); the revert marker then
// anchors the new start. Requires the conversation to be idle.
func (s *Server) pgRollbackConversationMessage(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	seq, ok := pathInt(r, "seq")
	if !ok || seq <= 0 {
		writeErr(w, 400, "bad seq")
		return
	}
	kind, reverted, found, err := pg.ConvActivityMeta(c.ID, seq)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !found || reverted {
		writeErr(w, 404, "消息不存在或已被回滚隐藏")
		return
	}
	if kind != "user" {
		writeErr(w, 400, "只能回滚用户消息")
		return
	}
	busyKey := s.convBusyKey(c.ID)
	if s.isChatKeyBusy(busyKey) {
		writeErr(w, 409, "当前会话有一轮正在运行，请先停止再回滚")
		return
	}
	boundary, err := pg.MaxVisibleConversationActivityBefore(r.Context(), c.ID, seq)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	checkpoints, err := pg.ListCheckpoints(r.Context(), convCheckpointScope(c.ID), 80)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	hidden, err := pg.HideConversationActivitiesAfter(r.Context(), c.ID, boundary)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	superseded, err := s.supersedeCheckpointsInRegion(r.Context(), checkpoints, boundary)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	truncated := false
	if cp := findUserMessageCheckpoint(checkpoints, seq); cp != nil {
		truncated = s.truncateTranscript(cp.Snapshot.TranscriptPath, cp.Snapshot.TranscriptSize)
	} else {
		log.Printf("[checkpoints] conv %d 消息 #%d 缺少用户消息检查点，跳过 transcript 截断", c.ID, seq)
	}
	marker := s.captureConversationCheckpoint(c, db.CheckpointReasonRevert,
		fmt.Sprintf("回滚到消息 #%d 之前（隐藏 %d 步）", seq, hidden), boundary)
	log.Printf("[checkpoints] conv %d 回滚消息 #%d：隐藏 %d 步，作废 %d 个检查点", c.ID, seq, hidden, superseded)
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

// isChatKeyBusy checks whether an agent turn is in flight for busyKey. When a
// turn's context was just cancelled (e.g. the user clicked Stop) or its terminal
// SSE frame just landed while the goroutine is still unwinding into defer
// finishTaskChat/delete(chatBusy), it waits briefly so an immediate rollback
// does not fail with a transient 409.
func (s *Server) isChatKeyBusy(busyKey string) bool {
	deadline := time.Now().Add(1500 * time.Millisecond)
	for {
		s.chatMu.Lock()
		busy := s.chatBusy[busyKey]
		s.chatMu.Unlock()
		if !busy || time.Now().After(deadline) {
			return busy
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// taskActivelyExecuting reports whether a task currently has any Planner/Worker
// execution or claimable intent in flight. Unlike occupiesConcurrencySlot — which
// stays true for an unpaused task whenever an LLM provider is configured, even
// while the engine sits in engine_mode="idle" with 0 in-flight operations and no
// open/running intents — this predicate only blocks timeline/message rollback when
// background work is actually running or queued to be claimed.
func (s *Server) taskActivelyExecuting(t *Task) bool {
	if t == nil {
		return false
	}
	if s.engine.IsDeleting(t.ID) {
		return true
	}
	lifecycle := t.lifecycleSnapshot()
	if lifecycle.Queued || lifecycle.Paused || isTerminalStatus(lifecycle.Status) || s.engine.IsPaused(t.ID) {
		return false
	}
	if s.engine.inflightCount(t.ID) > 0 || s.engine.ActiveLLMCalls(t.ID) > 0 {
		return true
	}
	if s.engine.ReadyFor(t) && t.Store != nil {
		if active, err := t.Store.HasActiveIntent(); err != nil || active {
			return true
		}
	}
	return false
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
	if s.isChatKeyBusy(t.ID) {
		writeErr(w, 409, "主 Agent 正在运行，请先停止再回滚时间线")
		return
	}
	if s.taskActivelyExecuting(t) {
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
	if len(reopened) > 0 && !t.lifecycleSnapshot().Paused && !s.engine.IsPaused(t.ID) && !isTerminalStatus(t.lifecycleSnapshot().Status) {
		_, _ = s.applyTaskControlWithCause(t, "pause", agent.AbortPausedByUser)
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

// rollbackTaskMessage rolls one task's MAIN-AGENT CONVERSATION back to the state
// just before one user message: hide that message and the main-agent steps after
// it, truncate the main agent transcript back to the size recorded before that
// turn (so the orchestrator no longer sees the retracted message), supersede the
// checkpoints captured at or after it and append a revert marker.
//
// Scope is the conversation and nothing else: worker/planner lanes keep their
// rows, intents keep their state, and the task's progress is NOT rewound —— 回退
// 只作用于消息与对话。正因如此它也不要求先暂停任务，唯一的前提是主 Agent 没有
// 进行中的回合（那一段正写着这同一条对话）。Tool side effects and graph products
// are not rolled back (the confirm dialog states this).
func (s *Server) rollbackTaskMessage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskByPath(w, r)
	if !ok {
		return
	}
	seq, ok := pathInt(r, "seq")
	if !ok || seq <= 0 {
		writeErr(w, 400, "bad seq")
		return
	}
	kind, worker, reverted, found, err := t.Store.ActivityMeta(seq)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !found || reverted {
		writeErr(w, 404, "消息不存在或已被回滚隐藏")
		return
	}
	// 只认主 Agent 的用户消息：这是「这段对话」的锚点，不是任务时间线的锚点。
	if kind != "user" || worker != db.MainAgentWorker {
		writeErr(w, 400, "只能回滚主 Agent 的用户消息")
		return
	}
	if s.isChatKeyBusy(t.ID) {
		writeErr(w, 409, "主 Agent 正在运行，请先停止再回滚")
		return
	}
	// 不再要求任务暂停（taskActivelyExecuting）：回退只动这段对话，worker/规划器
	// 正在做的事与意图状态一概不碰，隐藏范围也被限定在主 Agent 自己的泳道里。
	boundary, err := s.m.pg.MaxVisibleMainAgentActivityBefore(r.Context(), t.ExpID, seq)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	checkpoints, err := s.m.pg.ListCheckpoints(r.Context(), taskCheckpointScope(t.ID), 80)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	hidden, err := s.m.pg.HideMainAgentActivitiesAfter(r.Context(), t.ExpID, boundary)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 作废判定按消息本身（seq）而不是按主 Agent 泳道的边界：worker/规划器的记录没有
	// 被隐藏，落在 boundary 与 seq 之间的检查点仍然描述可见状态，不该被连坐。
	superseded, err := s.supersedeCheckpointsInRegion(r.Context(), checkpoints, seq-1)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	truncated := false
	if cp := findUserMessageCheckpoint(checkpoints, seq); cp != nil {
		truncated = s.truncateTranscript(cp.Snapshot.TranscriptPath, cp.Snapshot.TranscriptSize)
	} else {
		log.Printf("[checkpoints] task %s 消息 #%d 缺少主 Agent 消息检查点，跳过 transcript 截断", t.ID, seq)
	}
	if hidden > 0 {
		s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: fmt.Sprintf("会话已回滚到消息 #%d 之前：隐藏 %d 个主 Agent 步骤（任务进度与 Worker/规划器记录不受影响）。", seq, hidden)})
	}
	marker := s.captureTaskCheckpoint(t, db.CheckpointReasonRevert,
		fmt.Sprintf("回滚到消息 #%d 之前（隐藏 %d 步）", seq, hidden), false)
	log.Printf("[checkpoints] task %s 回滚消息 #%d：隐藏 %d 个主 Agent 步骤，作废 %d 个检查点",
		t.ID, seq, hidden, superseded)
	writeJSON(w, 200, map[string]any{
		"status": "reverted", "hidden": hidden,
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

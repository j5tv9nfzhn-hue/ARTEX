package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// 完成闸门（Completion Gate）——「中断/报错/误停的分支不许被当成已完成」。
//
// 判定不依赖模型措辞：只看意图运行台账（intent_runs）里每条分支【最后一次】运行是
// 怎么结束的，以及图上的意图节点现在还停在哪，再加上之后有没有人来收口
// （intent_closures / 续派意图已 done）。见 db.UnresolvedBranches。
//
// 三种模式（设置页 · 实验功能，切换即时生效，每次完成判定读一次）：
//
//	off    : 完全关闭，行为与改造前一致。
//	shadow : 只记录（日志 + 活动流留痕），不拦、不回炉。默认值，用于先观察误报。
//	enforce: 真拦——不落 done，并按下面的策略把坏终态分支自动回炉。
const (
	settingBranchGateMode = "branch_gate_mode"

	branchGateOff     = "off"
	branchGateShadow  = "shadow"
	branchGateEnforce = "enforce"

	// 回炉上限（P2 §12）。超限不再自动重跑，转 needs_human：任务保持 running 但
	// 永远判不了完成，等人来「继续 / 收口 / 强制完成」。
	maxRequeuesPerIntent = 3
	maxRequeuesPerTask   = 12

	// 同一错误哈希连续命中这么多次就不再烧钱（§12：同一意图连续 2 次 blocked）。
	maxSameErrorStreak = 2

	// 强制完成的审计动作名（写在 intent_closures.action 上，intent_id=0）。
	closureActionForceDone = "force_done"
	closureActionAbandon   = "abandon"
)

// retryDelays 是退避序列（blocked 从第 1 次回炉起用它；exhausted 第 2 次起用它）。
var retryDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}

// ErrTaskHasUnresolvedBranches 由 Manager 的状态写入路径返回：即使某个未来新增的
// 调用点忘了先查闸门，也写不进 done。
var ErrTaskHasUnresolvedBranches = errors.New("任务仍有未收口分支，不能标记为完成")

// ErrTaskAbandonedOnly 是反作弊闸：分支全被「放弃」掉、且放弃之后没有任何新证据，
// 说明所谓「达成」没有正向证据支撑——只能由人强制完成（留审计）。
var ErrTaskAbandonedOnly = errors.New("分支全部被放弃且无新增证据，不能判定完成（需人工强制完成并留审计）")

// BranchGateMode 返回当前模式（默认 shadow）。
func (m *Manager) BranchGateMode() string {
	if m == nil || m.pg == nil {
		return branchGateShadow
	}
	v, ok, err := m.pg.GetSetting(settingBranchGateMode)
	if err != nil || !ok {
		return branchGateShadow
	}
	switch strings.TrimSpace(v) {
	case branchGateOff:
		return branchGateOff
	case branchGateShadow:
		return branchGateShadow
	case branchGateEnforce:
		return branchGateEnforce
	default:
		return branchGateShadow
	}
}

// SetBranchGateMode persists the mode (off|shadow|enforce). Invalid values are
// rejected so the UI cannot silently downgrade to a different behaviour.
func (m *Manager) SetBranchGateMode(mode string) error {
	mode = strings.TrimSpace(mode)
	switch mode {
	case branchGateOff, branchGateShadow, branchGateEnforce:
	default:
		return fmt.Errorf("非法模式 %q（只接受 off/shadow/enforce）", mode)
	}
	if m == nil || m.pg == nil {
		return errors.New("数据库未就绪")
	}
	return m.pg.SetSetting(settingBranchGateMode, mode)
}

// taskExpID resolves a task's exploration id (the branch graph lives there).
func (m *Manager) taskExpID(taskID string) (int64, error) {
	n, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil {
		return 0, err
	}
	var exp int64
	if err := m.pg.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, n).Scan(&exp); err != nil {
		return 0, err
	}
	return exp, nil
}

// UnresolvedBranches lists the branches that ended badly and were never closed.
func (m *Manager) UnresolvedBranches(taskID string) ([]db.IntentBlocker, error) {
	if m == nil || m.pg == nil {
		return nil, nil
	}
	exp, err := m.taskExpID(taskID)
	if err != nil {
		return nil, err
	}
	return m.pg.UnresolvedBranches(taskID, exp)
}

// completionBlocked is the Manager-level guard (G4): only enforce mode blocks, and
// only "done" is guarded (timeout/failed are honest terminal states).
//
// 两条拒绝理由：
//  1. 还有未收口分支（§10）；
//  2. 反作弊：分支全被 abandon 且放弃后无新增证据（§11）——此时只有人显式
//     force_done 能放行。
func (m *Manager) completionBlocked(id string) error {
	if m.BranchGateMode() != branchGateEnforce {
		return nil
	}
	// 人工强制完成是唯一出口，先看它。
	if forced, err := m.pg.HasForceDone(id); err == nil && forced {
		return nil
	}
	if err := m.abandonOnlyBlocked(id); err != nil {
		return err
	}
	bs, err := m.UnresolvedBranches(id)
	if err != nil {
		// 读台账失败不能把任务永久卡住：放行，但留下痕迹。
		log.Printf("[gate] task %s 读取未收口分支失败（放行）: %v", id, err)
		return nil
	}
	if len(bs) == 0 {
		return nil
	}
	return fmt.Errorf("%w（%d 条：%s）", ErrTaskHasUnresolvedBranches, len(bs), branchIDs(bs))
}

// abandonOnlyBlocked 实现 §11.3：一个目标下所有分支都是 abandon、且全任务没有新证据
// → 不允许把任务收成 done。判定都来自台账，不看模型措辞。
func (m *Manager) abandonOnlyBlocked(taskID string) error {
	closures, err := m.pg.IntentClosures(taskID, 500)
	if err != nil || len(closures) == 0 {
		return nil
	}
	abandoned, others := 0, 0
	var firstAt time.Time
	for _, c := range closures {
		switch c.Action {
		case closureActionAbandon:
			abandoned++
			firstAt = c.CreatedAt // DESC 序，最后覆盖到的是最早那条
		case closureActionForceDone:
			return nil
		default:
			others++
		}
	}
	if abandoned == 0 || others > 0 {
		return nil
	}
	exp, err := m.taskExpID(taskID)
	if err != nil || exp == 0 {
		return nil
	}
	if n, err := m.pg.EvidenceSince(exp, firstAt); err == nil && n > 0 {
		return nil // 放弃之后还有新证据 → 不是空手收工
	}
	return ErrTaskAbandonedOnly
}

// branchIDs renders " #7(步数耗尽) " style labels for logs / activity text.
func branchIDs(bs []db.IntentBlocker) string {
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		parts = append(parts, fmt.Sprintf("#%d(%s)", b.IntentID, branchStateLabel(b.EndState)))
	}
	return strings.Join(parts, "、")
}

// isBadBranchState 判断「这一次没跑完」：四种坏终态都需要有人（自动回炉或人工）收口。
func isBadBranchState(state string) bool {
	switch state {
	case "exhausted", "blocked", "stopped", "interrupted":
		return true
	}
	return false
}

// branchStateLabel maps an end_state to a short Chinese label for the activity
// stream (kept local so the gate never depends on web-side i18n).
func branchStateLabel(state string) string {
	switch state {
	case "exhausted":
		return "步数耗尽"
	case "blocked":
		return "受阻"
	case "stopped":
		return "被终止"
	case "interrupted":
		return "进程中断"
	}
	return state
}

// ---------- 面板 / API ----------

// branchView 是面板需要的一条分支：图上的状态 + 回炉账本。
type branchView struct {
	db.IntentBlocker
	Requeues   int    `json:"requeues"`
	NeedsHuman bool   `json:"needs_human"`
	NextRetry  string `json:"next_retry_at,omitempty"`
}

// taskBranches 是 GET /api/tasks/{id}/branches：任务详情页「未收口 N」角标 + 概览卡片用。
func (s *Server) taskBranches(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskByPath(w, r)
	if !ok {
		return
	}
	bs, err := s.m.UnresolvedBranches(t.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	views := make([]branchView, 0, len(bs))
	needsHuman := 0
	for _, b := range bs {
		v := branchView{IntentBlocker: b}
		if st, err := s.m.pg.RetryState(t.ID, b.IntentID); err == nil && st != nil {
			v.Requeues = st.Requeues
			v.NeedsHuman = st.NeedsHuman
			if st.NextRetryAt != nil {
				v.NextRetry = st.NextRetryAt.UTC().Format(time.RFC3339)
			}
		}
		if v.NeedsHuman {
			needsHuman++
		}
		views = append(views, v)
	}
	abandoned := 0
	if cl, err := s.m.pg.IntentClosures(t.ID, 500); err == nil {
		for _, c := range cl {
			if c.Action == closureActionAbandon {
				abandoned++
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"count":       len(views),
		"mode":        s.m.BranchGateMode(),
		"needs_human": needsHuman,
		"abandoned":   abandoned,
		"blockers":    views,
	})
}

// intentHandoff 是 GET /api/tasks/{id}/intents/{iid}/handoff：交接单查看器。
// 返回最近一张交接单（事实层 + 主观层快照）、该意图的全部补注、以及收口审计。
func (s *Server) intentHandoff(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskByPath(w, r)
	if !ok {
		return
	}
	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || iid <= 0 {
		writeErr(w, 400, "bad intent id")
		return
	}
	resp := map[string]any{"intent_id": iid, "handoff": nil, "open_work": []db.IntentOpenWork{}, "closures": []db.IntentClosure{}}
	h, err := s.m.pg.LatestIntentHandoffByTask(t.ID, iid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if h != nil {
		resp["handoff"] = map[string]any{
			"id": h.ID, "run_id": h.RunID, "schema": h.SchemaVer,
			"created_at": h.CreatedAt, "mech_sha256": h.MechSHA256,
			"summary": h.RenderText, "record": json.RawMessage(h.RecordJSON),
		}
	}
	if ow, err := s.m.pg.OpenWorkFor(t.ID, iid, 50); err == nil && ow != nil {
		resp["open_work"] = ow
	}
	if cl, err := s.m.pg.IntentClosures(t.ID, 500); err == nil {
		mine := make([]db.IntentClosure, 0, 4)
		for _, c := range cl {
			if c.IntentID == iid {
				mine = append(mine, c)
			}
		}
		resp["closures"] = mine
	}
	writeJSON(w, 200, resp)
}

// forceDone 是 POST /api/tasks/{id}/force-done：人工强制完成。
// 先落一条 force_done 审计（理由必填），再尝试把任务写成 done——闸门看到审计就放行。
func (s *Server) forceDone(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskByPath(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if err := s.m.ForceDone(t.ID, req.Reason); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, errTaskDeletingIntentControl)
		return
	}
	defer s.engine.decInflight(t.ID)
	won, err := s.m.SetTaskStatusGuarded(t.ID, "done")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !won {
		// 状态没写进去（例如正在暂停/删除）：审计已留痕，下次完成判定会放行。
		writeJSON(w, 200, map[string]any{"id": t.ID, "status": s.m.TaskStatus(t.ID), "forced": false})
		return
	}
	s.engine.cancelExec(t.ID, agent.AbortGoalMet)
	log.Printf("[gate] task %s 已人工强制完成: %s", t.ID, strings.TrimSpace(req.Reason))
	writeJSON(w, 200, map[string]any{"id": t.ID, "status": "done", "forced": true})
}

// ---------- 人工收口 / 强制完成 ----------

// CloseBranch 人工收口（control action=abandon）：只写 intent_closures 审计行。
// 不动意图状态、不删图谱产物——分支仍然是 exhausted/blocked，历史 run 全保留，
// 只是从「未收口」变成「已人工结案」，完成闸门不再据此拦任务。
func (m *Manager) CloseBranch(t *Task, iid int64, reason string) error {
	if m == nil || m.pg == nil {
		return errors.New("数据库未就绪")
	}
	if t == nil || t.Store == nil {
		return errors.New("task not found")
	}
	n, err := t.Store.GetNode(iid)
	if err != nil {
		return err
	}
	if n == nil || n.Kind != db.KindIntent {
		return errors.New("intent not found")
	}
	if !isBadBranchState(n.State) {
		return fmt.Errorf("意图 #%d 当前状态是 %s，只有没跑完的分支才需要人工收口", iid, n.State)
	}
	if closed, err := m.pg.IntentClosureSet(t.ID); err == nil && closed[iid] {
		return fmt.Errorf("意图 #%d 已经收口过", iid)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "人工确认放弃该分支"
	}
	if err := m.pg.CloseIntent(&db.IntentClosure{
		TaskID: t.ID, IntentID: iid,
		Actor: "human", Action: closureActionAbandon, ReasonCode: "human_abandon", Note: reason,
	}); err != nil {
		return err
	}
	// 收口之后这条分支不再需要回炉，清掉它的退避账本。
	_ = m.pg.DeleteRetryState(t.ID, iid)
	log.Printf("[gate] task %s 意图 #%d 已人工收口: %s", t.ID, iid, reason)
	return nil
}

// ForceDone 是「人工强制完成」的唯一入口：写一条 force_done 审计，之后
// completionBlocked 会放行。它不清理分支、不掩盖未收口——只是人工认账。
func (m *Manager) ForceDone(taskID, reason string) error {
	if m == nil || m.pg == nil {
		return errors.New("数据库未就绪")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("强制完成必须写明理由")
	}
	return m.pg.CloseIntent(&db.IntentClosure{
		TaskID: taskID, IntentID: 0,
		Actor: "human", Action: closureActionForceDone,
		ReasonCode: "human_force_done", Note: reason,
	})
}

// ---------- 完成闸门（G2/G3） ----------

// blockTaskCompletion is called right before a planner met→done / goalless done
// transition (G2 / G3). It returns true when the caller must NOT set done.
//
// enforce: 留痕 + 回炉未收口分支 + 返回 true（不落 done、不取消在跑 worker）。
// shadow : 只留痕，返回 false（行为与改造前一致）。
// off    : 直接返回 false。
func (e *Engine) blockTaskCompletion(t *Task) bool {
	if t == nil || e.m == nil || e.m.pg == nil {
		return false
	}
	mode := e.m.BranchGateMode()
	if mode == branchGateOff {
		return false
	}
	bs, err := e.m.pg.UnresolvedBranches(t.ID, t.ExpID)
	if err != nil {
		log.Printf("[gate] task %s 读取未收口分支失败（放行）: %v", t.ID, err)
		return false
	}
	if len(bs) == 0 {
		return false
	}
	if mode == branchGateShadow {
		log.Printf("[gate][shadow] task %s 有 %d 条未收口分支：%s（影子模式，未拦截）", t.ID, len(bs), branchIDs(bs))
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: fmt.Sprintf("（实验·影子模式）检测到 %d 条未收口分支：%s —— 强制模式下任务不会判定完成", len(bs), branchIDs(bs))})
		return false
	}
	log.Printf("[gate] task %s 有 %d 条未收口分支，拒绝完成：%s", t.ID, len(bs), branchIDs(bs))
	e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
		Summary: fmt.Sprintf("存在 %d 条未收口分支，任务不判定完成：%s（按策略回炉）", len(bs), branchIDs(bs))})
	e.requeueBranches(t, bs)
	return true
}

// ---------- P2：回炉策略机 ----------

// requeueDecision 是一条分支「下一步怎么走」的判定结果（纯函数算出来，便于单测）。
type requeueDecision struct {
	Action string        // now | defer | human | skip
	Delay  time.Duration // defer 时有效
	Reason string        // 写进日志/活动流/账本
}

// planRequeue 把「状态 + 历史 + 上限」折叠成一个决定，不做任何副作用。
func planRequeue(now time.Time, b db.IntentBlocker, retry *db.BranchRetry, taskRequeues int, errHash string) requeueDecision {
	streak := 0
	if errHash != "" && retry.ErrorHash == errHash {
		streak = retry.ErrorStreak + 1
	}
	// 1. 上限：单分支到顶 → 转人工。
	if retry.Requeues >= maxRequeuesPerIntent {
		return requeueDecision{Action: "human", Reason: fmt.Sprintf("已回炉 %d 次仍没跑完", retry.Requeues)}
	}
	// 2. 整任务到顶 → 转人工（避免一个任务无限烧 token）。
	if taskRequeues >= maxRequeuesPerTask {
		return requeueDecision{Action: "human", Reason: fmt.Sprintf("本任务累计回炉 %d 次，已达上限", taskRequeues)}
	}
	// 3. 同一个错误反复出现 → 转人工（再重试也是同一个死法）。
	if errHash != "" && streak >= maxSameErrorStreak {
		return requeueDecision{Action: "human", Reason: "同一错误连续复现，不再自动重试"}
	}
	switch b.EndState {
	case "exhausted":
		// 步数/时长耗尽：第一次立刻重来（换个角度往往就能过），之后退避。
		if retry.Requeues == 0 {
			return requeueDecision{Action: "now", Reason: "步数/时长耗尽，立即重跑"}
		}
		return requeueDecision{Action: "defer", Delay: backoffFor(retry.Requeues), Reason: "步数/时长耗尽，退避重跑"}
	case "blocked":
		// 配额类只给一次机会（再跑还是没额度）。
		if b.StopReason == db.IntentBlockedLLMQuota && retry.Requeues >= 1 {
			return requeueDecision{Action: "human", Reason: "LLM 配额耗尽，已重试过一次"}
		}
		return requeueDecision{Action: "defer", Delay: backoffFor(retry.Requeues), Reason: "受阻，退避重跑"}
	case "stopped":
		// 被规划者/人主动终止：方向是主动放弃的，不自动重来——要么重派新意图，
		// 要么人工收口。任务因此停在 running，不会被判完成。
		return requeueDecision{Action: "human", Reason: "分支被主动终止，需人工决定重派或收口"}
	}
	return requeueDecision{Action: "skip", Reason: "状态无需回炉"}
}

func backoffFor(done int) time.Duration {
	if done <= 0 {
		return 0
	}
	if done > len(retryDelays) {
		return retryDelays[len(retryDelays)-1]
	}
	return retryDelays[done-1]
}

// kickRequeue 是「一次 run 以坏终态结束」之后的自动回炉入口（P2 自动化）。
// 只有 enforce 模式会真的动；shadow 只把「本应怎么做」写进日志/活动流。
func (e *Engine) kickRequeue(t *Task, intentID int64, nodeState, stopReason, errText string) {
	if e == nil || e.m == nil || e.m.pg == nil || t == nil || intentID <= 0 {
		return
	}
	mode := e.m.BranchGateMode()
	if mode == branchGateOff {
		return
	}
	// 任务级收尾（超时/失败/deadline）或已暂停时不要回炉：这时分支重开也没人认领，
	// 只会在面板上多出一条「看起来还没跑完」的假象。
	if e.isSettling(t.ID) || e.IsPaused(t.ID) || isTerminalStatus(e.m.TaskStatus(t.ID)) {
		return
	}
	retry, err := e.m.pg.RetryState(t.ID, intentID)
	if err != nil {
		log.Printf("[gate] task %s 意图 #%d 读回炉账本失败: %v", t.ID, intentID, err)
		return
	}
	taskRequeues, _ := e.m.pg.TaskRequeues(t.ID)
	b := db.IntentBlocker{IntentID: intentID, EndState: nodeState, StopReason: stopReason, NodeState: nodeState, Attempt: retry.Requeues + 1}
	d := planRequeue(time.Now().UTC(), b, retry, taskRequeues, errorHash(errText))
	if mode == branchGateShadow {
		log.Printf("[gate][shadow] task %s 意图 #%d %s → 本应 %s（%s）", t.ID, intentID, nodeState, d.Action, d.Reason)
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: fmt.Sprintf("（实验·影子模式）意图 #%d %s：本应 %s（%s），未执行", intentID, branchStateLabel(nodeState), requeueActionLabel(d.Action), d.Reason)})
		return
	}
	e.applyRequeue(t, intentID, retry, d, errorHash(errText), nodeState, stopReason)
}

// requeueBranches 是完成闸门（G2/G3）用的批量版本：把当前所有未收口分支按同一套
// 策略过一遍。注意它不改变判定结果——闸门已经决定不落 done 了。
func (e *Engine) requeueBranches(t *Task, bs []db.IntentBlocker) {
	for _, b := range bs {
		e.kickRequeue(t, b.IntentID, b.NodeState, b.StopReason, "")
	}
}

// applyRequeue 落账本并执行决定（只允许在 enforce 模式调用）。
func (e *Engine) applyRequeue(t *Task, intentID int64, retry *db.BranchRetry, d requeueDecision, errHash, nodeState, stopReason string) {
	now := time.Now().UTC()
	retry.LastEndState = nodeState
	retry.LastReason = stopReason
	if errHash != "" {
		if retry.ErrorHash == errHash {
			retry.ErrorStreak++
		} else {
			retry.ErrorHash = errHash
			retry.ErrorStreak = 1
		}
	}
	switch d.Action {
	case "now", "defer":
		if d.Action == "now" {
			if ok, err := t.Store.ReopenIntent(intentID); err == nil && ok {
				retry.Requeues++
				retry.NextRetryAt = nil
				_ = e.m.pg.SaveRetryState(retry)
				log.Printf("[gate] task %s 意图 #%d 已回炉（第 %d 次）: %s", t.ID, intentID, retry.Requeues, d.Reason)
				e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
					Summary: fmt.Sprintf("意图 #%d 已回炉重跑（第 %d 次）：%s", intentID, retry.Requeues, d.Reason)})
				t.Notify()
				return
			} else if err != nil {
				log.Printf("[gate] task %s 意图 #%d 回炉失败: %v", t.ID, intentID, err)
			}
			// ReopenIntent 返回 false = 已被别处重开/删除，账本照写不误。
			retry.NextRetryAt = nil
			_ = e.m.pg.SaveRetryState(retry)
			return
		}
		next := now.Add(d.Delay)
		retry.NextRetryAt = &next
		_ = e.m.pg.SaveRetryState(retry)
		log.Printf("[gate] task %s 意图 #%d 将在 %s 后回炉: %s", t.ID, intentID, d.Delay, d.Reason)
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: fmt.Sprintf("意图 #%d 将在 %s 后重跑：%s", intentID, humanDelay(d.Delay), d.Reason)})
	case "human":
		retry.NeedsHuman = true
		retry.NextRetryAt = nil
		_ = e.m.pg.SaveRetryState(retry)
		log.Printf("[gate] task %s 意图 #%d 转人工处理: %s", t.ID, intentID, d.Reason)
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: fmt.Sprintf("意图 #%d 需要人工处理（%s）：可继续重跑、人工收口或强制完成", intentID, d.Reason)})
		t.Notify()
	}
}

// ProcessDueRetries 被维护协程周期调用：把到点的分支真正放回 frontier。
func (e *Engine) ProcessDueRetries() {
	if e == nil || e.m == nil || e.m.pg == nil || e.m.BranchGateMode() != branchGateEnforce {
		return
	}
	due, err := e.m.pg.DueRetries(time.Now().UTC(), 50)
	if err != nil {
		log.Printf("[gate] 读取待回炉队列失败: %v", err)
		return
	}
	for _, r := range due {
		t, ok := e.m.Task(r.TaskID)
		if !ok || t == nil {
			_ = e.m.pg.DeleteRetryState(r.TaskID, r.IntentID)
			continue
		}
		if e.IsPaused(t.ID) || e.IsDeleting(t.ID) || isTerminalStatus(t.lifecycleSnapshot().Status) {
			continue // 任务暂停/结束：先把账本留着，恢复后再回炉
		}
		ok2, err := t.Store.ReopenIntent(r.IntentID)
		if err != nil {
			log.Printf("[gate] task %s 意图 #%d 到点回炉失败: %v", t.ID, r.IntentID, err)
			continue
		}
		if !ok2 {
			// 已经被别处重开/删除/收口 —— 账本作废。
			_ = e.m.pg.DeleteRetryState(r.TaskID, r.IntentID)
			continue
		}
		r.Requeues++
		r.NextRetryAt = nil
		if err := e.m.pg.SaveRetryState(&r); err != nil {
			log.Printf("[gate] task %s 意图 #%d 回炉账本落库失败: %v", t.ID, r.IntentID, err)
		}
		log.Printf("[gate] task %s 意图 #%d 到点回炉（第 %d 次）", t.ID, r.IntentID, r.Requeues)
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: fmt.Sprintf("意图 #%d 退避结束，已放回待执行队列（第 %d 次重跑）", r.IntentID, r.Requeues)})
		t.Notify()
	}
}

// errorHash 把错误文本折成一个短哈希，用来识别「同一个死法反复出现」。
func errorHash(errText string) string {
	errText = strings.TrimSpace(errText)
	if errText == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(errText))
	return hex.EncodeToString(sum[:])[:12]
}

func requeueActionLabel(action string) string {
	switch action {
	case "now":
		return "立即回炉"
	case "defer":
		return "退避后回炉"
	case "human":
		return "转人工"
	}
	return "不回炉"
}

func humanDelay(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	return fmt.Sprintf("%d 分钟", int(d.Minutes()))
}

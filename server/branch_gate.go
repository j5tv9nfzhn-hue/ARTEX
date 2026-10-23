package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// 完成闸门（Completion Gate）——「中断/报错/误停的分支不许被当成已完成」。
//
// 判定不依赖模型措辞：只看意图运行台账（intent_runs）里每条分支【最后一次】
// 运行是怎么结束的，以及之后有没有人来收口（intent_closures / 续派意图已 done）。
// 见 db/intent_ledger.go 的 UnresolvedBranches。
//
// 三种模式（设置页 · 实验功能，切换即时生效，每 run / 每次完成判定读一次）：
//
//	off    : 完全关闭，行为与改造前一致。
//	shadow : 只记录（日志 + 活动流留痕），不拦、不回炉。默认值，用于先观察误报。
//	enforce: 真拦——不落 done、不取消在跑 worker，并把未收口分支重新放回 frontier。
const (
	settingBranchGateMode = "branch_gate_mode"

	branchGateOff     = "off"
	branchGateShadow  = "shadow"
	branchGateEnforce = "enforce"

	// maxRequeueAttempts 是同一意图被自动回炉的次数上限。到顶后不再自动重跑，
	// 分支保持未收口（任务因此也不能被判完成），等人处理。
	maxRequeueAttempts = 3
)

// ErrTaskHasUnresolvedBranches 由 Manager 的状态写入路径返回：即使某个未来新增的
// 调用点忘了先查闸门，也写不进 done。
var ErrTaskHasUnresolvedBranches = errors.New("任务仍有未收口分支，不能标记为完成")

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
func (m *Manager) completionBlocked(id string) error {
	if m.BranchGateMode() != branchGateEnforce {
		return nil
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
		Actor: "human", Action: "abandon", ReasonCode: "human_abandon", Note: reason,
	}); err != nil {
		return err
	}
	log.Printf("[gate] task %s 意图 #%d 已人工收口: %s", t.ID, iid, reason)
	return nil
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

// taskBranches 是 GET /api/tasks/{id}/branches：任务详情页「未收口 N」角标用。
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
	if bs == nil {
		bs = []db.IntentBlocker{}
	}
	writeJSON(w, 200, map[string]any{
		"count":    len(bs),
		"mode":     s.m.BranchGateMode(),
		"blockers": bs,
	})
}

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
		Summary: fmt.Sprintf("存在 %d 条未收口分支，任务不判定完成：%s（已放回待执行队列）", len(bs), branchIDs(bs))})
	e.requeueBranches(t, bs)
	return true
}

// requeueBranches puts bad-ended branches back on the frontier ("回炉").
// 只重开仍在坏终态的意图；已到尝试上限的不再重开（分支保持未收口，等人处理）。
func (e *Engine) requeueBranches(t *Task, bs []db.IntentBlocker) {
	if t == nil || t.Store == nil {
		return
	}
	var requeued []string
	for _, b := range bs {
		switch b.NodeState {
		case "exhausted", "blocked", "stopped":
		default:
			continue // 已被别处重开/删除，别抢
		}
		if b.Attempt >= maxRequeueAttempts {
			continue
		}
		ok, err := t.Store.ReopenIntent(b.IntentID)
		if err != nil {
			log.Printf("[gate] task %s 意图 #%d 回炉失败: %v", t.ID, b.IntentID, err)
			continue
		}
		if ok {
			requeued = append(requeued, fmt.Sprintf("#%d", b.IntentID))
		}
	}
	if len(requeued) == 0 {
		return
	}
	log.Printf("[gate] task %s 已回炉: %s", t.ID, strings.Join(requeued, "、"))
	e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
		Summary: fmt.Sprintf("以下意图已重新放回待执行队列继续跑：%s", strings.Join(requeued, "、"))})
	t.Notify() // 唤醒 planner / 调度
}

package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// 回炉状态机（P2）+ 主观交接层（P3）的运行时表。
//
//	branch_retries   每条意图一行：被回炉了几次、上次错的是什么、下次什么时候再试、
//	                 是不是已经转人工（needs_human）。策略与判定在 server/branch_gate.go，
//	                 这里只负责持久化，保证「重启后回炉进度不丢」。
//	intent_open_work 模型补注（下一步/假设/负结果）：append-only，与 intent_handoffs 的
//	                 事实层物理分开。事实层可以机验（mech_sha256），这一层永远只是
//	                 【可被后人推翻的建议】，读的时候必须带这个标签。
const intentRuntimeSchema = `
CREATE TABLE IF NOT EXISTS branch_retries (
    task_id        TEXT NOT NULL,
    intent_id      BIGINT NOT NULL,
    requeues       INTEGER NOT NULL DEFAULT 0,
    error_hash     TEXT NOT NULL DEFAULT '',
    error_streak   INTEGER NOT NULL DEFAULT 0,
    needs_human    INTEGER NOT NULL DEFAULT 0,
    next_retry_at  @TS@,
    last_end_state TEXT NOT NULL DEFAULT '',
    last_reason    TEXT NOT NULL DEFAULT '',
    updated_at     @TS@ NOT NULL DEFAULT @NOW@,
    PRIMARY KEY (task_id, intent_id)
);
CREATE INDEX IF NOT EXISTS idx_branch_retries_due ON branch_retries(next_retry_at);

CREATE TABLE IF NOT EXISTS intent_open_work (
    id               @SERIAL@ PRIMARY KEY,
    task_id          TEXT NOT NULL,
    intent_id        BIGINT NOT NULL,
    author           TEXT NOT NULL DEFAULT '',
    dedupe_key       TEXT NOT NULL DEFAULT '',
    next_actions     TEXT NOT NULL DEFAULT '',
    hypotheses       TEXT NOT NULL DEFAULT '',
    negative_results TEXT NOT NULL DEFAULT '',
    created_at       @TS@ NOT NULL DEFAULT @NOW@
);
CREATE INDEX IF NOT EXISTS idx_intent_open_work ON intent_open_work(task_id, intent_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_intent_open_work ON intent_open_work(task_id, intent_id, dedupe_key);
`

// EnsureIntentRuntimeTables creates the retry / open-work tables (idempotent).
func (d *DB) EnsureIntentRuntimeTables() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(pgTypeSQL(intentRuntimeSchema)); err != nil {
		return err
	}
	return tx.Commit()
}

// DB 暴露底层句柄：agent 侧工具（handoff_patch / abandon_intent）只需要少量写操作，
// 与其在 ExplorationStore 上复制一遍方法，不如给一个受控的出口。
func (s *ExplorationStore) DB() *DB { return s.db }

// ---------- run 生命周期（reaper 依赖「跑之前先落行」） ----------

// BeginIntentRun 在 run 开始时落一行（ended_at 留空）。进程若中途崩溃/被强杀，
// 这行会一直是 ended_at IS NULL，启动时的 reaper 据此把这次运行认成 interrupted，
// 并用机械抽取补一张交接单——不需要模型还活着。
func (d *DB) BeginIntentRun(r *IntentRun) (int64, error) {
	var attempt int
	if err := d.QueryRow(
		`SELECT COALESCE(MAX(attempt),0) FROM intent_runs WHERE task_id=$1 AND intent_id=$2`,
		r.TaskID, r.IntentID).Scan(&attempt); err != nil {
		return 0, err
	}
	started := r.StartedAt
	if started.IsZero() {
		started = time.Now().UTC()
	}
	mode := r.Mode
	if mode == "" {
		mode = "run"
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO intent_runs(task_id, exploration_id, intent_id, attempt, mode, started_at,
                        terminal_reason, end_state, stop_reason, writes_json, steps_used)
VALUES ($1,$2,$3,$4,$5,$6,'','running','','',0) RETURNING id`,
		r.TaskID, r.ExplorationID, r.IntentID, attempt+1, mode, started).Scan(&id)
	return id, err
}

// FinishIntentRun 给一次 run 收尾（幂等：只更新还没收尾的行）。
func (d *DB) FinishIntentRun(runID int64, endState, stopReason, terminalReason, writesJSON string, steps int) error {
	if runID <= 0 {
		return nil
	}
	if endState == "" {
		endState = "done"
	}
	_, err := d.Exec(`
UPDATE intent_runs
SET ended_at=$2, end_state=$3, stop_reason=$4, terminal_reason=$5, writes_json=$6, steps_used=$7
WHERE id=$1 AND ended_at IS NULL`,
		runID, time.Now().UTC(), endState, stopReason, terminalReason, writesJSON, steps)
	return err
}

// OrphanIntentRuns 返回「起了头但没收尾」的 run（进程崩溃留下的现场）。
func (d *DB) OrphanIntentRuns() ([]IntentRun, error) {
	rows, err := d.Query(`
SELECT id, task_id, exploration_id, intent_id, attempt, mode, started_at, ended_at,
       terminal_reason, end_state, stop_reason, writes_json, steps_used
FROM intent_runs WHERE ended_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IntentRun
	for rows.Next() {
		var r IntentRun
		var started, ended *time.Time
		if err := rows.Scan(&r.ID, &r.TaskID, &r.ExplorationID, &r.IntentID, &r.Attempt, &r.Mode,
			&started, &ended, &r.TerminalReason, &r.EndState, &r.StopReason, &r.WritesJSON, &r.StepsUsed); err != nil {
			return nil, err
		}
		if started != nil {
			r.StartedAt = *started
		}
		if ended != nil {
			r.EndedAt = *ended
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------- 回炉状态 ----------

// BranchRetry 是一条分支的回炉账本。
type BranchRetry struct {
	TaskID       string     `json:"task_id"`
	IntentID     int64      `json:"intent_id"`
	Requeues     int        `json:"requeues"`
	ErrorHash    string     `json:"error_hash,omitempty"`
	ErrorStreak  int        `json:"error_streak,omitempty"`
	NeedsHuman   bool       `json:"needs_human"`
	NextRetryAt  *time.Time `json:"next_retry_at,omitempty"`
	LastEndState string     `json:"last_end_state,omitempty"`
	LastReason   string     `json:"last_reason,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// RetryState 读一条分支的回炉账本（没有则返回零值 + nil）。
func (d *DB) RetryState(taskID string, intentID int64) (*BranchRetry, error) {
	var r BranchRetry
	var next *time.Time
	var nh int
	err := d.QueryRow(`
SELECT task_id, intent_id, requeues, error_hash, error_streak, needs_human,
       next_retry_at, last_end_state, last_reason, updated_at
FROM branch_retries WHERE task_id=$1 AND intent_id=$2`, taskID, intentID).
		Scan(&r.TaskID, &r.IntentID, &r.Requeues, &r.ErrorHash, &r.ErrorStreak, &nh,
			&next, &r.LastEndState, &r.LastReason, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &BranchRetry{TaskID: taskID, IntentID: intentID}, nil
		}
		return nil, err
	}
	r.NeedsHuman = nh != 0
	r.NextRetryAt = next
	return &r, nil
}

// SaveRetryState 覆盖写一条回炉账本。
func (d *DB) SaveRetryState(r *BranchRetry) error {
	var next any
	if r.NextRetryAt != nil {
		next = r.NextRetryAt.UTC()
	}
	_, err := d.Exec(`
INSERT INTO branch_retries(task_id, intent_id, requeues, error_hash, error_streak, needs_human,
                           next_retry_at, last_end_state, last_reason, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT(task_id, intent_id) DO UPDATE SET
  requeues=excluded.requeues, error_hash=excluded.error_hash, error_streak=excluded.error_streak,
  needs_human=excluded.needs_human, next_retry_at=excluded.next_retry_at,
  last_end_state=excluded.last_end_state, last_reason=excluded.last_reason,
  updated_at=excluded.updated_at`,
		r.TaskID, r.IntentID, r.Requeues, r.ErrorHash, r.ErrorStreak, boolToInt(r.NeedsHuman),
		next, r.LastEndState, r.LastReason, time.Now().UTC())
	return err
}

// DeleteRetryState 清掉一条分支的回炉账本（分支已收口 / 已跑完时）。
func (d *DB) DeleteRetryState(taskID string, intentID int64) error {
	_, err := d.Exec(`DELETE FROM branch_retries WHERE task_id=$1 AND intent_id=$2`, taskID, intentID)
	return err
}

// DueRetries 返回「到点该回炉」的分支（不含已转人工的）。
func (d *DB) DueRetries(now time.Time, limit int) ([]BranchRetry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.Query(`
SELECT task_id, intent_id, requeues, error_hash, error_streak, needs_human,
       next_retry_at, last_end_state, last_reason, updated_at
FROM branch_retries
WHERE needs_human=0 AND next_retry_at IS NOT NULL AND next_retry_at <= $1
ORDER BY next_retry_at LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BranchRetry
	for rows.Next() {
		var r BranchRetry
		var next *time.Time
		var nh int
		if err := rows.Scan(&r.TaskID, &r.IntentID, &r.Requeues, &r.ErrorHash, &r.ErrorStreak,
			&nh, &next, &r.LastEndState, &r.LastReason, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.NeedsHuman = nh != 0
		r.NextRetryAt = next
		out = append(out, r)
	}
	return out, rows.Err()
}

// TaskRequeues 是整任务累计回炉次数（每任务上限用）。
func (d *DB) TaskRequeues(taskID string) (int, error) {
	var n int
	err := d.QueryRow(`SELECT COALESCE(SUM(requeues),0) FROM branch_retries WHERE task_id=$1`, taskID).Scan(&n)
	return n, err
}

// NeedsHumanBranches 返回该任务里已转人工处理的分支数。
func (d *DB) NeedsHumanBranches(taskID string) (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM branch_retries WHERE task_id=$1 AND needs_human=1`, taskID).Scan(&n)
	return n, err
}

// ---------- 主观交接层（handoff_patch） ----------

// IntentOpenWork 是模型对「接下来怎么走」的补注。它永远是建议，不是事实：
// 与 intent_handoffs 的事实层分开存，渲染时单独加「可推翻」标签。
type IntentOpenWork struct {
	ID              int64     `json:"id"`
	TaskID          string    `json:"task_id"`
	IntentID        int64     `json:"intent_id"`
	Author          string    `json:"author,omitempty"`
	DedupeKey       string    `json:"dedupe_key,omitempty"`
	NextActions     string    `json:"next_actions,omitempty"`
	Hypotheses      string    `json:"hypotheses,omitempty"`
	NegativeResults string    `json:"negative_results,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// SaveOpenWork 追加一条补注。dedupe_key 冲突（同一个 key 重复写）时静默忽略，
// 返回 inserted=false —— 幂等键让「模型反复补同一句」不会把表撑爆。
func (d *DB) SaveOpenWork(w *IntentOpenWork) (bool, error) {
	if w.DedupeKey == "" {
		w.DedupeKey = openWorkDedupeKey(w)
	}
	res, err := d.Exec(`
INSERT INTO intent_open_work(task_id, intent_id, author, dedupe_key, next_actions, hypotheses, negative_results)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT(task_id, intent_id, dedupe_key) DO NOTHING`,
		w.TaskID, w.IntentID, w.Author, w.DedupeKey, w.NextActions, w.Hypotheses, w.NegativeResults)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	return true, nil
}

// OpenWorkFor 读一条意图的补注（新的在后）。
func (d *DB) OpenWorkFor(taskID string, intentID int64, limit int) ([]IntentOpenWork, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.Query(`
SELECT id, task_id, intent_id, author, dedupe_key, next_actions, hypotheses, negative_results, created_at
FROM intent_open_work WHERE task_id=$1 AND intent_id=$2 ORDER BY id DESC LIMIT $3`,
		taskID, intentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IntentOpenWork
	for rows.Next() {
		var w IntentOpenWork
		if err := rows.Scan(&w.ID, &w.TaskID, &w.IntentID, &w.Author, &w.DedupeKey,
			&w.NextActions, &w.Hypotheses, &w.NegativeResults, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// OpenWorkSince 只取这次 run 期间写的补注（>= since）。
func (d *DB) OpenWorkSince(taskID string, intentID int64, since time.Time) ([]IntentOpenWork, error) {
	rows, err := d.Query(`
SELECT id, task_id, intent_id, author, dedupe_key, next_actions, hypotheses, negative_results, created_at
FROM intent_open_work WHERE task_id=$1 AND intent_id=$2 AND created_at >= $3 ORDER BY id`,
		taskID, intentID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IntentOpenWork
	for rows.Next() {
		var w IntentOpenWork
		if err := rows.Scan(&w.ID, &w.TaskID, &w.IntentID, &w.Author, &w.DedupeKey,
			&w.NextActions, &w.Hypotheses, &w.NegativeResults, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ---------- 收口审计读取（面板「被放弃 N」/ 交接单查看器） ----------

// IntentClosures 读一个任务的收口记录（新的在前）。
func (d *DB) IntentClosures(taskID string, limit int) ([]IntentClosure, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := d.Query(`
SELECT id, task_id, intent_id, actor, action, reason_code, note, evidence_ids, created_at
FROM intent_closures WHERE task_id=$1 ORDER BY id DESC LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IntentClosure
	for rows.Next() {
		var c IntentClosure
		if err := rows.Scan(&c.ID, &c.TaskID, &c.IntentID, &c.Actor, &c.Action, &c.ReasonCode,
			&c.Note, &c.EvidenceIDs, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// HasForceDone 判断该任务是否已被人显式强制完成（反作弊的合法出口）。
func (d *DB) HasForceDone(taskID string) (bool, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM intent_closures WHERE task_id=$1 AND action='force_done'`, taskID).Scan(&n)
	return n > 0, err
}

// EvidenceSince 数「某个时间点之后新写入的 fact/finding」——反作弊用：
// 全部分支被 abandon 却一条新证据都没有，就不允许判完成。
func (d *DB) EvidenceSince(expID int64, since time.Time) (int, error) {
	var n int
	err := d.QueryRow(`
SELECT COUNT(*) FROM exploration_nodes
WHERE exploration_id=$1 AND kind IN ('fact','finding') AND created_at >= $2`, expID, since.UTC()).Scan(&n)
	return n, err
}

// boolToInt 让 bool 以 0/1 落 INTEGER 列：SQLite 与 PostgreSQL 都接受，
// 避免把 Go bool 直接绑到 PG 的 INTEGER 参数上（类型不匹配）。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// openWorkDedupeKey 由补注内容算出幂等键：同一条意图下重复提交同样的三句话只落一行。
func openWorkDedupeKey(w *IntentOpenWork) string {
	h := sha256.New()
	for _, part := range []string{w.NextActions, w.Hypotheses, w.NegativeResults} {
		h.Write([]byte(strings.Join(strings.Fields(part), " ")))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

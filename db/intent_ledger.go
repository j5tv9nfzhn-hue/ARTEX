package db

import (
	"database/sql"
	"errors"
	"time"
)

// 意图运行台账 / 交接单 / 收口审计 / 续派链。
//
// 这四张表回答一个此前无法回答的问题：「这条探索方向到底收口了没有？」
// 意图自身的 state 只说明【这一次】怎么结束的（running→exhausted 等），
// 无法区分「跑完了」「被掐断了」「被人误停了」「有人明确放弃了」。
// 引擎的完成闸门（server/branch_gate.go）与结构化交接（server/handoff.go）
// 都读这几张表，判定一律由代码计算，不看模型措辞。
const intentLedgerSchema = `
CREATE TABLE IF NOT EXISTS intent_runs (
    id              @SERIAL@ PRIMARY KEY,
    task_id         TEXT NOT NULL,
    exploration_id  BIGINT NOT NULL DEFAULT 0,
    intent_id       BIGINT NOT NULL,
    attempt         INTEGER NOT NULL DEFAULT 1,
    mode            TEXT NOT NULL DEFAULT 'fresh',
    started_at      @TS@ NOT NULL DEFAULT @NOW@,
    ended_at        @TS@,
    terminal_reason TEXT NOT NULL DEFAULT '',
    end_state       TEXT NOT NULL DEFAULT '',
    stop_reason     TEXT NOT NULL DEFAULT '',
    writes_json     TEXT NOT NULL DEFAULT '',
    steps_used      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_intent_runs_task ON intent_runs(task_id, intent_id);
CREATE INDEX IF NOT EXISTS idx_intent_runs_exp  ON intent_runs(exploration_id);

CREATE TABLE IF NOT EXISTS intent_handoffs (
    id             @SERIAL@ PRIMARY KEY,
    run_id         BIGINT NOT NULL DEFAULT 0,
    task_id        TEXT NOT NULL,
    exploration_id BIGINT NOT NULL DEFAULT 0,
    intent_id      BIGINT NOT NULL,
    schema_ver     TEXT NOT NULL DEFAULT 'artex.handoff/v1',
    record_json    TEXT NOT NULL DEFAULT '',
    render_text    TEXT NOT NULL DEFAULT '',
    mech_sha256    TEXT NOT NULL DEFAULT '',
    created_at     @TS@ NOT NULL DEFAULT @NOW@
);
CREATE INDEX IF NOT EXISTS idx_intent_handoffs_task   ON intent_handoffs(task_id, intent_id, id);
CREATE INDEX IF NOT EXISTS idx_intent_handoffs_exp    ON intent_handoffs(exploration_id, intent_id, id);

CREATE TABLE IF NOT EXISTS intent_closures (
    id           @SERIAL@ PRIMARY KEY,
    task_id      TEXT NOT NULL,
    intent_id    BIGINT NOT NULL,
    actor        TEXT NOT NULL DEFAULT '',
    action       TEXT NOT NULL DEFAULT '',
    reason_code  TEXT NOT NULL DEFAULT '',
    note         TEXT NOT NULL DEFAULT '',
    evidence_ids TEXT NOT NULL DEFAULT '',
    created_at   @TS@ NOT NULL DEFAULT @NOW@
);
CREATE INDEX IF NOT EXISTS idx_intent_closures_intent ON intent_closures(task_id, intent_id);

CREATE TABLE IF NOT EXISTS intent_links (
    exploration_id BIGINT NOT NULL,
    src_intent     BIGINT NOT NULL,
    dst_intent     BIGINT NOT NULL,
    kind           TEXT NOT NULL DEFAULT 'continues',
    created_at     @TS@ NOT NULL DEFAULT @NOW@,
    PRIMARY KEY (exploration_id, src_intent, dst_intent, kind)
);
CREATE INDEX IF NOT EXISTS idx_intent_links_src ON intent_links(exploration_id, src_intent);
`

// EnsureIntentLedgerTables creates the run/ handoff / closure / link tables.
// Idempotent; called once at manager start (same slot as EnsureLLMUsageTable).
func (d *DB) EnsureIntentLedgerTables() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(pgTypeSQL(intentLedgerSchema)); err != nil {
		return err
	}
	return tx.Commit()
}

// IntentRun is one execution of one intent. One row per run; attempt counts runs
// of the same intent so the gate can tell "first try" from "requeued retry".
type IntentRun struct {
	ID             int64     `json:"id"`
	TaskID         string    `json:"task_id"`
	ExplorationID  int64     `json:"exploration_id"`
	IntentID       int64     `json:"intent_id"`
	Attempt        int       `json:"attempt"`
	Mode           string    `json:"mode"`
	StartedAt      time.Time `json:"started_at"`
	EndedAt        time.Time `json:"ended_at"`
	TerminalReason string    `json:"terminal_reason"`
	EndState       string    `json:"end_state"`
	StopReason     string    `json:"stop_reason"`
	WritesJSON     string    `json:"writes_json"`
	StepsUsed      int       `json:"steps_used"`
}

// IntentHandoff is the structured hand-off record produced when a run settles.
type IntentHandoff struct {
	ID            int64     `json:"id"`
	RunID         int64     `json:"run_id"`
	TaskID        string    `json:"task_id"`
	ExplorationID int64     `json:"exploration_id"`
	IntentID      int64     `json:"intent_id"`
	SchemaVer     string    `json:"schema_ver"`
	RecordJSON    string    `json:"record_json"`
	RenderText    string    `json:"render_text"`
	MechSHA256    string    `json:"mech_sha256"`
	CreatedAt     time.Time `json:"created_at"`
}

// IntentClosure is the audit row that closes a branch: either a successor
// finished it, a human closed it, or someone (human only) forced the task done.
type IntentClosure struct {
	ID          int64     `json:"id"`
	TaskID      string    `json:"task_id"`
	IntentID    int64     `json:"intent_id"`
	Actor       string    `json:"actor"`
	Action      string    `json:"action"`
	ReasonCode  string    `json:"reason_code"`
	Note        string    `json:"note"`
	EvidenceIDs string    `json:"evidence_ids"`
	CreatedAt   time.Time `json:"created_at"`
}

// IntentBlocker is one branch that ended badly and was never closed.
type IntentBlocker struct {
	IntentID   int64  `json:"intent_id"`
	EndState   string `json:"end_state"`
	StopReason string `json:"stop_reason"`
	Attempt    int    `json:"attempt"`
	NodeState  string `json:"node_state"`
	HasHandoff bool   `json:"has_handoff"`
}

// isBadNodeState 判断意图节点是否停在「没跑完、需要收口」的状态。
// 注意 interrupted 不是节点状态（崩溃后节点仍是 running，由 ResetRunningIntents 放开），
// 所以这里只认三种落库终态。未收口判定以【节点状态】为准，run 只提供原因。
func isBadNodeState(state string) bool {
	switch state {
	case "exhausted", "blocked", "stopped":
		return true
	}
	return false
}

// isBadRunState reports whether a run ending in this state means "the work did
// not finish normally" — the states the completion gate cares about.
func isBadRunState(state string) bool {
	switch state {
	case "exhausted", "blocked", "stopped", "interrupted":
		return true
	}
	return false
}

// RecordIntentRun appends one run row (already finished). attempt is derived from
// the existing run count for this intent so callers never have to track it.
func (d *DB) RecordIntentRun(r *IntentRun) (int64, error) {
	var attempt int
	if err := d.QueryRow(
		`SELECT COALESCE(MAX(attempt),0)+1 FROM intent_runs WHERE task_id=$1 AND intent_id=$2`,
		r.TaskID, r.IntentID).Scan(&attempt); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	started := r.StartedAt
	if started.IsZero() {
		started = now
	}
	mode := r.Mode
	if mode == "" {
		mode = "fresh"
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO intent_runs(task_id, exploration_id, intent_id, attempt, mode, started_at, ended_at,
                        terminal_reason, end_state, stop_reason, writes_json, steps_used)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		r.TaskID, r.ExplorationID, r.IntentID, attempt, mode, started, now,
		r.TerminalReason, r.EndState, r.StopReason, r.WritesJSON, r.StepsUsed).Scan(&id)
	return id, err
}

// IntentRuns returns every run of a task, newest first (bounded).
func (d *DB) IntentRuns(taskID string) ([]IntentRun, error) {
	rows, err := d.Query(`
SELECT id, task_id, exploration_id, intent_id, attempt, mode, started_at, ended_at,
       terminal_reason, end_state, stop_reason, writes_json, steps_used
FROM intent_runs WHERE task_id=$1 ORDER BY id DESC LIMIT 2000`, taskID)
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

// IntentStates maps intent node id → current state for one exploration.
func (d *DB) IntentStates(expID int64) (map[int64]string, error) {
	rows, err := d.Query(`SELECT id, state FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'`, expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// ContinuationResolved returns the set of intents that have at least one
// successor intent which finished with state=done (i.e. the branch was carried on
// and successfully completed).
func (d *DB) ContinuationResolved(expID int64) (map[int64]bool, error) {
	rows, err := d.Query(`
SELECT DISTINCT l.src_intent
FROM intent_links l
JOIN exploration_nodes n ON n.id = l.dst_intent AND n.exploration_id = l.exploration_id
WHERE l.exploration_id=$1 AND l.kind='continues' AND n.kind='intent' AND n.state='done'`, expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// IntentClosureSet returns the set of explicitly closed intents of a task.
func (d *DB) IntentClosureSet(taskID string) (map[int64]bool, error) {
	rows, err := d.Query(`SELECT DISTINCT intent_id FROM intent_closures WHERE task_id=$1`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// UnresolvedBranches is THE single source of truth for "is this task allowed to
// be marked done". A branch is unresolved when its latest run ended badly and
// nothing closed it afterwards:
//   - a successor intent reached done, or
//   - someone/something wrote an intent_closures row.
//
// goal_met_cancel / task_timeout are excluded only for FIRST runs: if a branch
// already failed once and is cancelled again by a completion, that completion is
// exactly the silent-skip we must not allow.
func (d *DB) UnresolvedBranches(taskID string, expID int64) ([]IntentBlocker, error) {
	runs, err := d.IntentRuns(taskID)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	closures, err := d.IntentClosureSet(taskID)
	if err != nil {
		return nil, err
	}
	resolved, err := d.ContinuationResolved(expID)
	if err != nil {
		return nil, err
	}
	states, err := d.IntentStates(expID)
	if err != nil {
		return nil, err
	}
	handoffs, err := d.intentHandoffSet(taskID)
	if err != nil {
		return nil, err
	}
	byIntent := map[int64][]IntentRun{}
	var order []int64
	for _, r := range runs { // newest first
		if _, ok := byIntent[r.IntentID]; !ok {
			order = append(order, r.IntentID)
		}
		byIntent[r.IntentID] = append(byIntent[r.IntentID], r)
	}
	var out []IntentBlocker
	for _, iid := range order {
		rs := byIntent[iid]
		latest := rs[0]
		nodeState := states[iid]
		// 「未收口」看的是【图上的状态】：只有还停在坏终态的意图才算挂着。
		// 已经回炉成 open / 又跑起来的 running / 正常 done / deleted 都不算——
		// 否则一次崩溃留下的 interrupted run 会在分支重开后继续挡完成。
		if !isBadNodeState(nodeState) {
			continue
		}
		if !isBadRunState(latest.EndState) {
			continue
		}
		if closures[iid] || resolved[iid] {
			continue
		}
		if latest.StopReason == "goal_met_cancel" || latest.StopReason == "task_timeout" {
			priorBad := false
			for _, p := range rs[1:] {
				if isBadRunState(p.EndState) {
					priorBad = true
					break
				}
			}
			if !priorBad {
				continue
			}
		}
		out = append(out, IntentBlocker{
			IntentID:   iid,
			EndState:   nodeState,
			StopReason: latest.StopReason,
			Attempt:    latest.Attempt,
			NodeState:  nodeState,
			HasHandoff: handoffs[iid],
		})
		if len(out) >= 200 {
			break
		}
	}
	return out, nil
}

// intentHandoffSet 返回已经有交接单的意图集合（面板上标注「有交接单」用）。
func (d *DB) intentHandoffSet(taskID string) (map[int64]bool, error) {
	rows, err := d.Query(`SELECT DISTINCT intent_id FROM intent_handoffs WHERE task_id=$1`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var iid int64
		if err := rows.Scan(&iid); err != nil {
			return nil, err
		}
		out[iid] = true
	}
	return out, rows.Err()
}

// SaveIntentHandoff stores one structured hand-off record.
func (d *DB) SaveIntentHandoff(h *IntentHandoff) (int64, error) {
	ver := h.SchemaVer
	if ver == "" {
		ver = "artex.handoff/v1"
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO intent_handoffs(run_id, task_id, exploration_id, intent_id, schema_ver, record_json, render_text, mech_sha256)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		h.RunID, h.TaskID, h.ExplorationID, h.IntentID, ver, h.RecordJSON, h.RenderText, h.MechSHA256).Scan(&id)
	return id, err
}

// LatestIntentHandoffByTask returns the newest hand-off for an intent.
func (d *DB) LatestIntentHandoffByTask(taskID string, intentID int64) (*IntentHandoff, error) {
	return d.scanIntentHandoff(d.QueryRow(`
SELECT id, run_id, task_id, exploration_id, intent_id, schema_ver, record_json, render_text, mech_sha256, created_at
FROM intent_handoffs WHERE task_id=$1 AND intent_id=$2 ORDER BY id DESC LIMIT 1`, taskID, intentID))
}

// IntentHandoff is the store-scoped accessor used by the agent lane (it knows the
// exploration, not the task registry id).
func (s *ExplorationStore) IntentHandoff(intentID int64) (*IntentHandoff, error) {
	return s.db.scanIntentHandoff(s.db.QueryRow(`
SELECT id, run_id, task_id, exploration_id, intent_id, schema_ver, record_json, render_text, mech_sha256, created_at
FROM intent_handoffs WHERE exploration_id=$1 AND intent_id=$2 ORDER BY id DESC LIMIT 1`, s.expID, intentID))
}

func (d *DB) scanIntentHandoff(row interface {
	Scan(dest ...any) error
}) (*IntentHandoff, error) {
	var h IntentHandoff
	if err := row.Scan(&h.ID, &h.RunID, &h.TaskID, &h.ExplorationID, &h.IntentID, &h.SchemaVer,
		&h.RecordJSON, &h.RenderText, &h.MechSHA256, &h.CreatedAt); err != nil {
		// 没有交接单不是错误：调用方（get_handoff / worker 注入）据此走「无交接」分支。
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &h, nil
}

// CloseIntent records a closure (audit trail) for one branch.
func (d *DB) CloseIntent(c *IntentClosure) error {
	_, err := d.Exec(`
INSERT INTO intent_closures(task_id, intent_id, actor, action, reason_code, note, evidence_ids)
VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		c.TaskID, c.IntentID, c.Actor, c.Action, c.ReasonCode, c.Note, c.EvidenceIDs)
	return err
}

// LinkContinuation records "src is continued by dst" for one exploration.
func (s *ExplorationStore) LinkContinuation(src, dst int64) error {
	_, err := s.db.Exec(`
INSERT INTO intent_links(exploration_id, src_intent, dst_intent, kind) VALUES ($1,$2,$3,'continues')
ON CONFLICT (exploration_id, src_intent, dst_intent, kind) DO NOTHING`, s.expID, src, dst)
	return err
}

// Continuations returns src intent id → successor intent ids for one exploration.
func (s *ExplorationStore) Continuations() (map[int64][]int64, error) {
	rows, err := s.db.Query(`SELECT src_intent, dst_intent FROM intent_links WHERE exploration_id=$1 AND kind='continues'`, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var src, dst int64
		if err := rows.Scan(&src, &dst); err != nil {
			return nil, err
		}
		out[src] = append(out[src], dst)
	}
	return out, rows.Err()
}

package db

// Reviewer 判决台账。
//
// 一张表，只增不改：每次 worker run 结束由 Reviewer 写一行，Planner 侧读最近 N 行。
// 与 intent_closures / intent_handoffs 的区别：
//
//	intent_handoffs  —— Worker 的**原始**交接单（含 tail、last_output、L2 自注），
//	                    只有 Reviewer 与人能看，Planner 永远不读。
//	reviewer_verdicts —— Reviewer 的**产物**（Fact/Clue/Verdict），Planner 唯一的入口。
//
// 唯一索引建在 run_id 上：一次 run 只应有一个判决。重跑产生新的 run_id，自然留痕。

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

const reviewerVerdictSchema = `
CREATE TABLE IF NOT EXISTS reviewer_verdicts (
    id             @SERIAL@ PRIMARY KEY,
    task_id        BIGINT NOT NULL DEFAULT 0,
    exploration_id BIGINT NOT NULL DEFAULT 0,
    intent_id      BIGINT NOT NULL DEFAULT 0,
    run_id         BIGINT NOT NULL DEFAULT 0,
    verdict        TEXT NOT NULL DEFAULT 'pivot',
    attribution    TEXT NOT NULL DEFAULT 'none',
    confidence     REAL NOT NULL DEFAULT 0,
    decided_by     TEXT NOT NULL DEFAULT 'rules',
    summary        TEXT NOT NULL DEFAULT '',
    facts_json     TEXT NOT NULL DEFAULT '[]',
    clues_json     TEXT NOT NULL DEFAULT '[]',
    carry_json     TEXT NOT NULL DEFAULT '[]',
    negative_json  TEXT NOT NULL DEFAULT '[]',
    sanitized      BOOLEAN NOT NULL DEFAULT 0,
    raw_bytes      INTEGER NOT NULL DEFAULT 0,
    evidence_json  TEXT NOT NULL DEFAULT '[]',
    created_at     @TS@ NOT NULL DEFAULT @NOW@
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_reviewer_run ON reviewer_verdicts(run_id);
CREATE INDEX IF NOT EXISTS idx_reviewer_task ON reviewer_verdicts(task_id, id);

-- 证据快照：**只存清洗后的正文**。原始报文永远不进这张表，所以 fetch_evidence
-- 无论怎么被调用都拿不到未清洗内容——这是那条回溯通道的安全底座。
CREATE TABLE IF NOT EXISTS reviewer_evidence (
    id          @SERIAL@ PRIMARY KEY,
    task_id     BIGINT NOT NULL DEFAULT 0,
    intent_id   BIGINT NOT NULL DEFAULT 0,
    run_id      BIGINT NOT NULL DEFAULT 0,
    kind        TEXT NOT NULL DEFAULT 'tail',
    body        TEXT NOT NULL DEFAULT '',
    body_sha256 TEXT NOT NULL DEFAULT '',
    touches     INTEGER NOT NULL DEFAULT 0,
    created_at  @TS@ NOT NULL DEFAULT @NOW@
);
CREATE INDEX IF NOT EXISTS idx_reviewer_evidence_task ON reviewer_evidence(task_id, id);
`

// EnsureReviewerVerdictTable 幂等建表，manager 启动时调用（与 EnsureClueCacheTables 同法）。
func (d *DB) EnsureReviewerVerdictTable() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(pgTypeSQL(reviewerVerdictSchema)); err != nil {
		return err
	}
	return tx.Commit()
}

// ReviewerVerdictInput 是一次判决的写入形态。
type ReviewerVerdictInput struct {
	TaskID        int64
	ExplorationID int64
	IntentID      int64
	RunID         int64
	Verdict       string
	Attribution   string
	Confidence    float64
	DecidedBy     string
	Summary       string
	FactsJSON     string
	CluesJSON     string
	CarryJSON     string
	NegativeJSON  string
	Sanitized     bool
	RawBytes      int
	EvidenceJSON  string
}

// ReviewerVerdictRow 是读取形态。
type ReviewerVerdictRow struct {
	ID            int64     `json:"id"`
	TaskID        int64     `json:"task_id"`
	ExplorationID int64     `json:"exploration_id"`
	IntentID      int64     `json:"intent_id"`
	RunID         int64     `json:"run_id"`
	Verdict       string    `json:"verdict"`
	Attribution   string    `json:"attribution"`
	Confidence    float64   `json:"confidence"`
	DecidedBy     string    `json:"decided_by"`
	Summary       string    `json:"summary"`
	FactsJSON     string    `json:"facts_json"`
	CluesJSON     string    `json:"clues_json"`
	CarryJSON     string    `json:"carry_json"`
	NegativeJSON  string    `json:"negative_json"`
	Sanitized     bool      `json:"sanitized"`
	RawBytes      int       `json:"raw_bytes"`
	EvidenceJSON  string    `json:"evidence_json"`
	CreatedAt     time.Time `json:"created_at"`
}

func blankJSON(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}

// PutReviewerVerdict 写一条判决。同一 run_id 重复写会覆盖（重试/补写场景），
// 靠唯一索引保证「一次 run 一个判决」。
func (s *ExplorationStore) PutReviewerVerdict(in ReviewerVerdictInput) (int64, error) {
	if s == nil || s.db == nil {
		return 0, sql.ErrConnDone
	}
	verdict := in.Verdict
	if verdict == "" {
		verdict = "pivot"
	}
	didBy := in.DecidedBy
	if didBy == "" {
		didBy = "rules"
	}
	attr := in.Attribution
	if attr == "" {
		attr = "none"
	}
	_, err := s.db.Exec(
		`INSERT INTO reviewer_verdicts
		   (task_id, exploration_id, intent_id, run_id, verdict, attribution, confidence,
		    decided_by, summary, facts_json, clues_json, carry_json, negative_json,
		    sanitized, raw_bytes, evidence_json, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,`+sqlNowExpr()+`)
		 ON CONFLICT(run_id) DO UPDATE SET
		    verdict=excluded.verdict, attribution=excluded.attribution,
		    confidence=excluded.confidence, decided_by=excluded.decided_by,
		    summary=excluded.summary, facts_json=excluded.facts_json,
		    clues_json=excluded.clues_json, carry_json=excluded.carry_json,
		    negative_json=excluded.negative_json, sanitized=excluded.sanitized,
		    raw_bytes=excluded.raw_bytes, evidence_json=excluded.evidence_json`,
		in.TaskID, in.ExplorationID, in.IntentID, in.RunID, verdict, attr, in.Confidence,
		didBy, in.Summary, blankJSON(in.FactsJSON), blankJSON(in.CluesJSON),
		blankJSON(in.CarryJSON), blankJSON(in.NegativeJSON), in.Sanitized, in.RawBytes,
		blankJSON(in.EvidenceJSON))
	if err != nil {
		return 0, err
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM reviewer_verdicts WHERE run_id=$1`, in.RunID).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// ListReviewerVerdicts 取某任务最近的判决（最新在前）。
func (s *ExplorationStore) ListReviewerVerdicts(taskID int64, limit int) ([]ReviewerVerdictRow, error) {
	if s == nil || s.db == nil {
		return nil, sql.ErrConnDone
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT id, task_id, exploration_id, intent_id, run_id, verdict, attribution,
		        confidence, decided_by, summary, facts_json, clues_json, carry_json,
		        negative_json, sanitized, raw_bytes, evidence_json, created_at
		   FROM reviewer_verdicts WHERE task_id=$1 ORDER BY id DESC LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewerVerdictRow
	for rows.Next() {
		var r ReviewerVerdictRow
		if err := rows.Scan(&r.ID, &r.TaskID, &r.ExplorationID, &r.IntentID, &r.RunID,
			&r.Verdict, &r.Attribution, &r.Confidence, &r.DecidedBy, &r.Summary,
			&r.FactsJSON, &r.CluesJSON, &r.CarryJSON, &r.NegativeJSON,
			&r.Sanitized, &r.RawBytes, &r.EvidenceJSON, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReviewedIntentIDs 返回已有判决的意图 id 集合。Planner 的注入面用它把未被
// Reviewer 处理过的原始事实折叠掉——这就是「物理切断」在读取侧的实现。
func (s *ExplorationStore) ReviewedIntentIDs(taskID int64) (map[int64]bool, error) {
	if s == nil || s.db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := s.db.Query(`SELECT DISTINCT intent_id FROM reviewer_verdicts WHERE task_id=$1`, taskID)
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
		if id > 0 {
			out[id] = true
		}
	}
	return out, rows.Err()
}

// ReviewerVerdictsByIntents 按意图 id 批量取判决（每个意图取最新一条）。
// 注入面用它把「已判决 / 未判决」分开，键是意图 id，天然按任务隔离。
func (s *ExplorationStore) ReviewerVerdictsByIntents(ids []int64) ([]ReviewerVerdictRow, error) {
	if s == nil || s.db == nil || len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 500 {
		ids = ids[:500]
	}
	q := `SELECT id, task_id, exploration_id, intent_id, run_id, verdict, attribution,
	             confidence, decided_by, summary, facts_json, clues_json, carry_json,
	             negative_json, sanitized, raw_bytes, evidence_json, created_at
	        FROM reviewer_verdicts WHERE intent_id IN (?` + strings.Repeat(",?", len(ids)-1) + `)
	       ORDER BY id DESC`
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[int64]bool, len(ids))
	var out []ReviewerVerdictRow
	for rows.Next() {
		var r ReviewerVerdictRow
		if err := rows.Scan(&r.ID, &r.TaskID, &r.ExplorationID, &r.IntentID, &r.RunID,
			&r.Verdict, &r.Attribution, &r.Confidence, &r.DecidedBy, &r.Summary,
			&r.FactsJSON, &r.CluesJSON, &r.CarryJSON, &r.NegativeJSON,
			&r.Sanitized, &r.RawBytes, &r.EvidenceJSON, &r.CreatedAt); err != nil {
			return nil, err
		}
		if seen[r.IntentID] {
			continue // 同一意图多次重跑，只取最新一条
		}
		seen[r.IntentID] = true
		out = append(out, r)
	}
	return out, rows.Err()
}

// DecodeReviewerInts 把整数数组列解成 []int64（evidence_ref 用）。
func DecodeReviewerInts(raw string) []int64 {
	if raw == "" {
		return nil
	}
	var out []int64
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// DecodeReviewerStrings 把字符串数组列解成 []string。
func DecodeReviewerStrings(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// DecodeReviewerList 把 *_json 列解成 map 列表，供接口层直接回给前端。
func DecodeReviewerList(raw string) []map[string]any {
	if raw == "" {
		return nil
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// ---- 证据快照 ------------------------------------------------------------

// PutReviewerEvidence 落一条**已清洗**的证据快照，返回 ref id。
// 调用方必须保证 body 已过 agent.SanitizeReviewerText——本函数不做清洗，
// 只做长度兜底与哈希留存。
func (s *ExplorationStore) PutReviewerEvidence(taskID, intentID, runID int64, kind, body string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, sql.ErrConnDone
	}
	r := []rune(body)
	if len(r) > 8000 {
		body = string(r[:8000])
	}
	if kind == "" {
		kind = "tail"
	}
	sum := sha256.Sum256([]byte(body))
	_, err := s.db.Exec(
		`INSERT INTO reviewer_evidence (task_id, intent_id, run_id, kind, body, body_sha256, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,`+sqlNowExpr()+`)`,
		taskID, intentID, runID, kind, body, hex.EncodeToString(sum[:]))
	if err != nil {
		return 0, err
	}
	var id int64
	if err := s.db.QueryRow(
		`SELECT id FROM reviewer_evidence WHERE task_id=$1 AND body_sha256=$2 ORDER BY id DESC LIMIT 1`,
		taskID, hex.EncodeToString(sum[:])).Scan(&id); err != nil {
		return 0, err
	}
	// 顺手裁剪：写入路径上做，省一个后台任务；失败只影响体积，不影响正确性。
	if _, err := s.PruneReviewerEvidence(taskID, reviewerEvidenceKeep); err != nil {
		return id, nil
	}
	return id, nil
}

// reviewerEvidenceKeep 是单任务在线保留的证据条数上限。超出的按 id 从旧到新删掉。
//
// 为什么要限：每条 ≤8000 rune，一个任务最多 2 条/run。不设上限时，长任务
// （历史样本 1,662 次调用）能把这张表撑到几百 KB~MB 级，而且它平时根本没人读——
// 纯冷数据。100 条足够覆盖「最近若干次判决还需要回溯取证」的窗口。
const reviewerEvidenceKeep = 100

// PruneReviewerEvidence 把某任务的历史证据裁到最近 keep 条，返回删除行数。
func (s *ExplorationStore) PruneReviewerEvidence(taskID int64, keep int) (int64, error) {
	if s == nil || s.db == nil {
		return 0, sql.ErrConnDone
	}
	if keep <= 0 {
		keep = reviewerEvidenceKeep
	}
	res, err := s.db.Exec(
		`DELETE FROM reviewer_evidence
		  WHERE task_id=$1 AND id NOT IN (
		        SELECT id FROM reviewer_evidence WHERE task_id=$1 ORDER BY id DESC LIMIT $2)`,
		taskID, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PurgeReviewerEvidenceForTask 删掉某任务的全部证据（归档/删除级联用）。
func (s *ExplorationStore) PurgeReviewerEvidenceForTask(taskID int64) (int64, error) {
	if s == nil || s.db == nil {
		return 0, sql.ErrConnDone
	}
	res, err := s.db.Exec(`DELETE FROM reviewer_evidence WHERE task_id=$1`, taskID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetReviewerEvidence 按 ref id 取快照。返回的 body 仍是落库时那份清洗结果；
// 调用方（fetch_evidence 工具）会再洗一次，做纵深防御。
func (s *ExplorationStore) GetReviewerEvidence(refID int64) (ReviewerEvidenceRow, error) {
	var out ReviewerEvidenceRow
	if s == nil || s.db == nil {
		return out, sql.ErrConnDone
	}
	row := s.db.QueryRow(
		`SELECT id, task_id, intent_id, run_id, kind, body, touches, created_at
		   FROM reviewer_evidence WHERE id=$1`, refID)
	err := row.Scan(&out.ID, &out.TaskID, &out.IntentID, &out.RunID, &out.Kind, &out.Body,
		&out.Touches, &out.CreatedAt)
	return out, err
}

// TouchReviewerEvidence 给一次取证访问记账。
func (s *ExplorationStore) TouchReviewerEvidence(refID int64) error {
	if s == nil || s.db == nil {
		return sql.ErrConnDone
	}
	_, err := s.db.Exec(`UPDATE reviewer_evidence SET touches=touches+1 WHERE id=$1`, refID)
	return err
}

// HasReviewerVerdict 报告某次 run 是否已有判决落库。engine 用它做
// 「先落库、再唤醒 Planner」的硬闸门。
func (s *ExplorationStore) HasReviewerVerdict(runID int64) (bool, error) {
	if s == nil || s.db == nil || runID <= 0 {
		return false, nil
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM reviewer_verdicts WHERE run_id=$1`, runID).Scan(&n)
	return n > 0, err
}

// ReviewerEvidenceRow 是证据快照的读取形态。
type ReviewerEvidenceRow struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	IntentID  int64     `json:"intent_id"`
	RunID     int64     `json:"run_id"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	Touches   int       `json:"touches"`
	CreatedAt time.Time `json:"created_at"`
}

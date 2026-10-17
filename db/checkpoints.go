package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 检查点时间线（批次 6 / #29–#37）。
//
// 一个检查点是「时间线上一个可寻址、可回放的快照」：scope 为 "conv-<id>"（会话）
// 或 "task-<id>"（任务），seq 在 scope 内单调递增（时间线地址）。快照刻意只存
// 引用与边界——活动 id 边界、transcript 文件大小、图计数/意图状态——绝不复制
// 全量消息：体量可控、回滚语义明确。回滚 = 隐藏边界之后的活动（reverted_at）、
// 把 transcript 截断回快照大小、把未完成意图重新置为待执行；工具副作用与已落库
// 的图产物不回滚（UI 明示）。
//
// 状态：active（可用）/ superseded（被一次更早检查点的回滚作废）。reason 记录
// 检查点从哪来：user_message / turn_complete / admit / intent_settled /
// interrupt / compaction_recover / revert / manual。

const (
	CheckpointActive     = "active"
	CheckpointSuperseded = "superseded"
)

// 检查点来源。前端按这些值选择图标/文案（未知值按 manual 呈现）。
const (
	CheckpointReasonUserMessage       = "user_message"
	CheckpointReasonTurnComplete      = "turn_complete"
	CheckpointReasonAdmit             = "admit"
	CheckpointReasonIntentSettled     = "intent_settled"
	CheckpointReasonInterrupt         = "interrupt"
	CheckpointReasonCompactionRecover = "compaction_recover"
	CheckpointReasonRevert            = "revert"
	CheckpointReasonManual            = "manual"
)

// Checkpoint is one row of the timeline.
type Checkpoint struct {
	ID        int64              `json:"id"`
	Scope     string             `json:"scope"`
	Seq       int64              `json:"seq"`
	Reason    string             `json:"reason"`
	Status    string             `json:"status"`
	Summary   string             `json:"summary"`
	Snapshot  CheckpointSnapshot `json:"snapshot"`
	CreatedAt time.Time          `json:"created_at"`
}

// CheckpointSnapshot is the light reference set captured at a timeline point.
// All fields are optional; a missing transcript path/size means "no file yet / not
// tracked". Counts/Intents/Compaction are task-graph context used by the recovery
// check and by the timeline UI to describe the boundary.
type CheckpointSnapshot struct {
	// ActivityID is the max activity id INCLUDED in the boundary: revert hides
	// activities with id > ActivityID. 0 = hide nothing (boundary at start).
	ActivityID int64 `json:"activity_id"`
	// TranscriptSize is the transcript file's byte size at capture time and
	// TranscriptPath the file it refers to (absolute, server-local). Revert
	// truncates the file back to this size.
	TranscriptSize int64  `json:"transcript_size,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	// NodeID is the max exploration node id at capture; Counts/Intents describe
	// the graph boundary (task scopes only).
	NodeID  int64              `json:"node_id,omitempty"`
	Counts  map[string]int64   `json:"counts,omitempty"`
	Intents map[string][]int64 `json:"intents,omitempty"`
	// Compaction mirrors the compaction_state boundary when the checkpoint was
	// produced by a compaction interrupt/recovery.
	Compaction map[string]any `json:"compaction,omitempty"`
}

const checkpointCols = `id, scope, seq, reason, status, summary, snapshot_json, created_at`

func scanCheckpoint(row interface{ Scan(...any) error }) (*Checkpoint, error) {
	var (
		c    Checkpoint
		snap []byte
	)
	if err := row.Scan(&c.ID, &c.Scope, &c.Seq, &c.Reason, &c.Status, &c.Summary, &snap, &c.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(snap, &c.Snapshot)
	return &c, nil
}

// CaptureCheckpoint appends one checkpoint to a scope's timeline. seq is assigned
// in the same transaction (max+1). Old entries are pruned to the newest
// checkpointsPerScope so a long-lived scope cannot grow without bound.
func (d *DB) CaptureCheckpoint(ctx context.Context, scope, reason, summary string, snapshot CheckpointSnapshot) (*Checkpoint, error) {
	if scope == "" {
		return nil, errors.New("checkpoint: scope required")
	}
	if reason == "" {
		reason = CheckpointReasonManual
	}
	snapB, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq),0)+1 FROM checkpoints WHERE scope=$1`, scope).Scan(&seq); err != nil {
		return nil, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
INSERT INTO checkpoints(scope, seq, reason, status, summary, snapshot_json)
VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		scope, seq, reason, CheckpointActive, summary, string(jsonbClean(snapB))).Scan(&id); err != nil {
		return nil, err
	}
	// 保留上限：只删最旧的超出部分，superseded 与 active 一视同仁（时间线历史）。
	if _, err := tx.ExecContext(ctx, `
DELETE FROM checkpoints WHERE scope=$1 AND seq <= (
  SELECT COALESCE(MAX(seq),0) - $2 FROM checkpoints WHERE scope=$1
)`, scope, checkpointPerScope); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	c, err := d.GetCheckpoint(ctx, id)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// checkpointPerScope caps retained timeline entries per scope.
const checkpointPerScope = 80

// GetCheckpoint loads one checkpoint by id (nil when missing).
func (d *DB) GetCheckpoint(ctx context.Context, id int64) (*Checkpoint, error) {
	c, err := scanCheckpoint(d.QueryRowContext(ctx,
		`SELECT `+checkpointCols+` FROM checkpoints WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// ListCheckpoints returns a scope's timeline, newest first. limit<=0 means 50.
func (d *DB) ListCheckpoints(ctx context.Context, scope string, limit int) ([]Checkpoint, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx,
		`SELECT `+checkpointCols+` FROM checkpoints WHERE scope=$1 ORDER BY seq DESC LIMIT $2`, scope, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Checkpoint{}
	for rows.Next() {
		c, err := scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// SupersedeCheckpointsAfter marks every checkpoint newer than seq as superseded
// (a revert to an earlier point invalidated them). Returns how many were marked.
func (d *DB) SupersedeCheckpointsAfter(ctx context.Context, scope string, seq int64) (int64, error) {
	res, err := d.ExecContext(ctx, `
UPDATE checkpoints SET status=$1 WHERE scope=$2 AND seq>$3 AND status<>$1`,
		CheckpointSuperseded, scope, seq)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// --- revert helpers ---

// HideConversationActivitiesAfter hides (does not delete) every conversation step
// with id greater than the boundary — the visible timeline rolls back while the
// removed steps stay auditable in the table.
func (d *DB) HideConversationActivitiesAfter(ctx context.Context, convID, boundaryID int64) (int64, error) {
	res, err := d.ExecContext(ctx, `
UPDATE conversation_activities SET reverted_at=(strftime('%Y-%m-%d %H:%M:%f+00:00','now'))
WHERE conversation_id=$1 AND id>$2 AND reverted_at IS NULL`, convID, boundaryID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// HideExplorationActivitiesAfter is HideConversationActivitiesAfter for a task's
// exploration activity stream.
func (d *DB) HideExplorationActivitiesAfter(ctx context.Context, explorationID, boundaryID int64) (int64, error) {
	res, err := d.ExecContext(ctx, `
UPDATE activity SET reverted_at=(strftime('%Y-%m-%d %H:%M:%f+00:00','now'))
WHERE exploration_id=$1 AND id>$2 AND reverted_at IS NULL`, explorationID, boundaryID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ReopenInterruptedIntents flips intents stuck in running/paused back to open so a
// {@code resume} re-claims them (task rollback: 重置状态 + 重新入队). Unlike
// ResetRunningIntents this is not a crash replay (no replay metric) — the caller
// requeues deliberately. Returns the number of reopened intents, plus their ids.
func (d *DB) ReopenInterruptedIntents(ctx context.Context, explorationID int64) ([]int64, error) {
	rows, err := d.QueryContext(ctx, `
SELECT id FROM exploration_nodes
WHERE exploration_id=$1 AND kind='intent' AND state IN ('running','paused')`, explorationID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if _, err := d.ExecContext(ctx, `
UPDATE exploration_nodes SET state='open', completed_at=NULL, blocked_reason=NULL
WHERE exploration_id=$1 AND kind='intent' AND state IN ('running','paused')`, explorationID); err != nil {
		return nil, err
	}
	return ids, nil
}

// DigestCoverageIssues runs the批次 6 / #37 digest consistency check over one
// exploration: (a) a member covered by 2+ active digests (torn/duplicated covers),
// (b) an active digest carrying no covers edges (empty fold). Returns
// operator-readable issue strings; empty = consistent. Read-only.
func (s *ExplorationStore) DigestCoverageIssues() ([]string, error) {
	var issues []string
	rows, err := s.db.Query(`
SELECT e.dst_id, COUNT(DISTINCT e.src_id) AS n
FROM exploration_edges e
JOIN exploration_nodes d ON d.id=e.src_id AND d.exploration_id=e.exploration_id
WHERE e.exploration_id=$1 AND e.rel=$2 AND d.kind=$3 AND d.state=$4
GROUP BY e.dst_id HAVING COUNT(DISTINCT e.src_id) > 1
ORDER BY e.dst_id`, s.expID, RelCovers, KindDigest, StateDigestActive)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var member int64
		var n int
		if err := rows.Scan(&member, &n); err != nil {
			rows.Close()
			return nil, err
		}
		issues = append(issues, fmt.Sprintf("节点 #%d 被 %d 个活动 digest 同时覆盖", member, n))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	empty, err := s.db.Query(`
SELECT d.id FROM exploration_nodes d
WHERE d.exploration_id=$1 AND d.kind=$2 AND d.state=$3
  AND NOT EXISTS (SELECT 1 FROM exploration_edges e
                  WHERE e.exploration_id=d.exploration_id AND e.src_id=d.id AND e.rel=$4)
ORDER BY d.id`, s.expID, KindDigest, StateDigestActive, RelCovers)
	if err != nil {
		return nil, err
	}
	defer empty.Close()
	for empty.Next() {
		var id int64
		if err := empty.Scan(&id); err != nil {
			return nil, err
		}
		issues = append(issues, fmt.Sprintf("活动 digest #%d 没有任何覆盖成员", id))
	}
	return issues, empty.Err()
}

// MaxExplorationActivityID returns the newest VISIBLE activity id of an
// exploration (0 when none) — the boundary value a task checkpoint snapshots.
// Steps hidden by an earlier rollback are excluded: the boundary must describe
// the visible timeline, otherwise a checkpoint captured right after a rollback
// would claim hidden steps as included.
func (d *DB) MaxExplorationActivityID(ctx context.Context, explorationID int64) (int64, error) {
	var id sql.NullInt64
	err := d.QueryRowContext(ctx,
		`SELECT MAX(id) FROM activity WHERE exploration_id=$1 AND reverted_at IS NULL`, explorationID).Scan(&id)
	return id.Int64, err
}

// MaxConversationActivityID returns the newest visible conversation step id (0
// when none). Same visibility rule as MaxExplorationActivityID.
func (d *DB) MaxConversationActivityID(ctx context.Context, convID int64) (int64, error) {
	var id sql.NullInt64
	err := d.QueryRowContext(ctx,
		`SELECT MAX(id) FROM conversation_activities WHERE conversation_id=$1 AND reverted_at IS NULL`, convID).Scan(&id)
	return id.Int64, err
}

// MaxVisibleConversationActivityBefore returns the newest VISIBLE conversation
// step id strictly below beforeID (0 when none) — the boundary a message-level
// rollback snapshots ("the state just before that message").
func (d *DB) MaxVisibleConversationActivityBefore(ctx context.Context, convID, beforeID int64) (int64, error) {
	var id sql.NullInt64
	err := d.QueryRowContext(ctx,
		`SELECT MAX(id) FROM conversation_activities WHERE conversation_id=$1 AND id<$2 AND reverted_at IS NULL`,
		convID, beforeID).Scan(&id)
	return id.Int64, err
}

// MaxVisibleExplorationActivityBefore is MaxVisibleConversationActivityBefore
// for a task's exploration activity stream.
func (d *DB) MaxVisibleExplorationActivityBefore(ctx context.Context, explorationID, beforeID int64) (int64, error) {
	var id sql.NullInt64
	err := d.QueryRowContext(ctx,
		`SELECT MAX(id) FROM activity WHERE exploration_id=$1 AND id<$2 AND reverted_at IS NULL`,
		explorationID, beforeID).Scan(&id)
	return id.Int64, err
}

// SupersedeCheckpoint marks one checkpoint superseded. Used by the message-level
// rollback, which supersedes exactly the checkpoints whose boundary points into
// the hidden region (a seq-range cutoff would also wrongly catch older revert
// markers whose boundary lies before the cut). Reports whether the row changed.
func (d *DB) SupersedeCheckpoint(ctx context.Context, id int64) (bool, error) {
	res, err := d.ExecContext(ctx, `
UPDATE checkpoints SET status=$1 WHERE id=$2 AND status<>$1`,
		CheckpointSuperseded, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ExplorationGraphCounts returns the node/edge/intent counters captured into task
// checkpoints ("双图状态" boundary, kept light on purpose).
func (d *DB) ExplorationGraphCounts(ctx context.Context, explorationID int64) (map[string]int64, map[string][]int64, error) {
	var nodes, edges, maxNode int64
	if err := d.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM exploration_nodes WHERE exploration_id=$1),
  (SELECT COUNT(*) FROM exploration_edges WHERE exploration_id=$1),
  COALESCE((SELECT MAX(id) FROM exploration_nodes WHERE exploration_id=$1),0)`,
		explorationID).Scan(&nodes, &edges, &maxNode); err != nil {
		return nil, nil, err
	}
	counts := map[string]int64{"nodes": nodes, "edges": edges, "max_node_id": maxNode}
	intents := map[string][]int64{"open": {}, "running": {}, "paused": {}, "done": {}, "blocked": {}, "exhausted": {}, "stopped": {}}
	rows, err := d.QueryContext(ctx, `
SELECT state, id FROM exploration_nodes
WHERE exploration_id=$1 AND kind='intent' AND state IN ('open','running','paused','done','blocked','exhausted','stopped')`,
		explorationID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var id int64
		if err := rows.Scan(&state, &id); err != nil {
			return nil, nil, err
		}
		intents[state] = append(intents[state], id)
	}
	return counts, intents, rows.Err()
}

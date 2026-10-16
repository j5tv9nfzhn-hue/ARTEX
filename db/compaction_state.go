package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// 压缩状态（批次 6 / #27、#28、#37）。
//
// 平台内可触及的压缩 = agent.Compactor 的冷节点 digest 压缩（会话级压缩在 norma
// SDK/agentcore 内，平台不可钩）。每个任务 scope="task-<id>" 一行：
//
//	running     一次后台上压已开始（边界信息含 phase/members/active_digests）
//	idle        最近一次压缩正常结束
//	interrupted 压缩被任务暂停/停止打断，或进程重启后发现残留的 running
//
// 恢复一致性校验：重启后 phase 仍是 running 说明压缩进程已死（进程内 Compactor
// 同刻不可能还在跑）——AddDigest 是事务性的，图要么含该 digest 要么完全不含，
// 不存在半提交，故处理 = 标记 interrupted + 落一个 compaction_recover 检查点，
// 未完成的分块会在下一轮压缩重做。
const (
	CompactionPhaseIdle        = "idle"
	CompactionPhaseRunning     = "running"
	CompactionPhaseInterrupted = "interrupted"
)

// CompactionState is one task scope's compaction lifecycle row.
type CompactionState struct {
	Scope     string         `json:"scope"`
	Phase     string         `json:"phase"`
	Boundary  map[string]any `json:"boundary"`
	StartedAt *time.Time     `json:"started_at,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// PutCompactionState upserts a scope's compaction phase + boundary info. Writing
// phase=running restarts the started_at clock.
func (d *DB) PutCompactionState(ctx context.Context, scope, phase string, boundary map[string]any) error {
	if scope == "" {
		return errors.New("compaction_state: scope required")
	}
	b, err := json.Marshal(boundary)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `
INSERT INTO compaction_state(scope, phase, boundary_json, started_at, updated_at)
VALUES ($1, $2, $3, (strftime('%Y-%m-%d %H:%M:%f+00:00','now')), (strftime('%Y-%m-%d %H:%M:%f+00:00','now')))
ON CONFLICT(scope) DO UPDATE SET
  phase=EXCLUDED.phase,
  boundary_json=EXCLUDED.boundary_json,
  started_at=CASE WHEN EXCLUDED.phase=$4 THEN (strftime('%Y-%m-%d %H:%M:%f+00:00','now')) ELSE compaction_state.started_at END,
  updated_at=(strftime('%Y-%m-%d %H:%M:%f+00:00','now'))`,
		scope, phase, string(jsonbClean(b)), CompactionPhaseRunning)
	return err
}

// GetCompactionState loads one scope's row (nil when missing).
func (d *DB) GetCompactionState(ctx context.Context, scope string) (*CompactionState, error) {
	var (
		out       CompactionState
		b         []byte
		startedAt sql.NullTime
	)
	err := d.QueryRowContext(ctx, `
SELECT scope, phase, boundary_json, started_at, updated_at
FROM compaction_state WHERE scope=$1`, scope).
		Scan(&out.Scope, &out.Phase, &b, &startedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(b, &out.Boundary)
	if startedAt.Valid {
		out.StartedAt = &startedAt.Time
	}
	return &out, nil
}

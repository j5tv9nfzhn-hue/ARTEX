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
	// InjectedBytes / DigestTokensEst 是最近一次冷摘要的产出规模（第三阶段）。
	// nil = 该作用域还没跑过一次带统计的压缩（老数据 / 从没压过）。
	InjectedBytes   *int64 `json:"injected_bytes,omitempty"`
	DigestTokensEst *int64 `json:"digest_tokens_est,omitempty"`
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
INSERT INTO compaction_state(scope, phase, boundary_json, started_at, updated_at, injected_bytes, digest_tokens_est)
VALUES ($1, $2, $3, (strftime('%Y-%m-%d %H:%M:%f+00:00','now')), (strftime('%Y-%m-%d %H:%M:%f+00:00','now')), $5, $6)
ON CONFLICT(scope) DO UPDATE SET
  phase=EXCLUDED.phase,
  boundary_json=EXCLUDED.boundary_json,
  started_at=CASE WHEN EXCLUDED.phase=$4 THEN (strftime('%Y-%m-%d %H:%M:%f+00:00','now')) ELSE compaction_state.started_at END,
  updated_at=(strftime('%Y-%m-%d %H:%M:%f+00:00','now')),
  -- 只在本次带值时覆盖：start 事件没有产出规模,不能让 0 把上一轮的数字冲掉。
  injected_bytes=CASE WHEN EXCLUDED.injected_bytes IS NULL THEN compaction_state.injected_bytes ELSE EXCLUDED.injected_bytes END,
  digest_tokens_est=CASE WHEN EXCLUDED.digest_tokens_est IS NULL THEN compaction_state.digest_tokens_est ELSE EXCLUDED.digest_tokens_est END`,
		scope, phase, string(jsonbClean(b)), CompactionPhaseRunning,
		boundaryInt(boundary, "injected_bytes"), boundaryInt(boundary, "digest_tokens_est"))
	return err
}

// boundaryInt 从 boundary map 里取一个非负整数；缺失或类型不对返回 nil（写 NULL）。
// JSON 解出来的数字是 float64，这里统一收敛，避免 1e+06 这类表示写坏列。
func boundaryInt(boundary map[string]any, key string) any {
	v, ok := boundary[key]
	if !ok || v == nil {
		return nil
	}
	switch n := v.(type) {
	case int:
		if n < 0 {
			return nil
		}
		return int64(n)
	case int64:
		if n < 0 {
			return nil
		}
		return n
	case float64:
		if n < 0 {
			return nil
		}
		return int64(n)
	}
	return nil
}

// GetCompactionState loads one scope's row (nil when missing).
func (d *DB) GetCompactionState(ctx context.Context, scope string) (*CompactionState, error) {
	var (
		out       CompactionState
		b         []byte
		startedAt sql.NullTime
		injected  sql.NullInt64
		digTok    sql.NullInt64
	)
	err := d.QueryRowContext(ctx, `
SELECT scope, phase, boundary_json, started_at, updated_at, injected_bytes, digest_tokens_est
FROM compaction_state WHERE scope=$1`, scope).
		Scan(&out.Scope, &out.Phase, &b, &startedAt, &out.UpdatedAt, &injected, &digTok)
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
	if injected.Valid {
		v := injected.Int64
		out.InjectedBytes = &v
	}
	if digTok.Valid {
		v := digTok.Int64
		out.DigestTokensEst = &v
	}
	return &out, nil
}

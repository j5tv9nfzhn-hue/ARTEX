package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// 活动流的自动保留（「保底」）。
//
// 存储页的「按天清理」是人工的一次性动作；这里补的是**兜底**：默认保留 14 天，
// 并且行数超过 10 万时按「最旧优先」裁到上限。两条闸门互相兜住：
//   - 天数管「最近一个账期」：14 天足够回放/审计一次任务的全过程；
//   - 行数管「体积」：≈2KB/行 × 10 万 ≈ 200MB，再忙的机器也不会把盘吃穿。
//
// 三条约束（都为了不误删）：
//  1. 只删**已结束**探索（status <> 'open'）的活动。正在跑的任务哪怕挂了很久也不动。
//  2. 行数闸门有 3 天保护期：比 3 天新的行即使超限也不动——正在看的时间线不会突然缺页。
//  3. 分批 + 单批一个事务 + 单写协程（WriteQueued）：写锁只按「一批」持有。

const (
	ActivityRetentionSetting     = "activity_retention_days"
	ActivityRetentionMaxRowsKey  = "activity_retention_max_rows"
	ActivityRetentionDefaultDays = 14
	ActivityRetentionDefaultRows = 100000
	ActivityRetentionMaxDays     = 3650
	// ActivityPruneFloorDays 是行数闸门的保护期：比它新的行永远不因「超限」被删。
	ActivityPruneFloorDays = 3
	// 单轮上限：一次后台循环最多删这么多行，剩下的留给下一轮。
	ActivityPruneBatchDefault   = 5000
	ActivityPruneMaxRowsDefault = 100000
)

// ActivityPruneResult 是单轮裁剪的结果，直接进日志。
type ActivityPruneResult struct {
	Deleted int64 `json:"deleted"`
	Batches int   `json:"batches"`
	Capped  int64 `json:"capped"`
}

// ActivityRetentionDays 读保留天数；未设置 = 默认 14 天，显式 0 = 关闭。
func (d *DB) ActivityRetentionDays() (int, error) {
	raw, ok, err := d.GetSetting(ActivityRetentionSetting)
	if err != nil {
		return 0, err
	}
	if !ok {
		return ActivityRetentionDefaultDays, nil
	}
	n, cerr := strconv.Atoi(strings.TrimSpace(raw))
	if cerr != nil || n < 0 {
		return ActivityRetentionDefaultDays, nil
	}
	if n > ActivityRetentionMaxDays {
		n = ActivityRetentionMaxDays
	}
	return n, nil
}

// ActivityRetentionMaxRows 读行数上限；未设置 = 默认 10 万，显式 0 = 不限。
func (d *DB) ActivityRetentionMaxRows() (int, error) {
	raw, ok, err := d.GetSetting(ActivityRetentionMaxRowsKey)
	if err != nil {
		return 0, err
	}
	if !ok {
		return ActivityRetentionDefaultRows, nil
	}
	n, cerr := strconv.Atoi(strings.TrimSpace(raw))
	if cerr != nil || n < 0 {
		return ActivityRetentionDefaultRows, nil
	}
	if n > 10000000 {
		n = 10000000
	}
	return n, nil
}

// SetActivityRetention 保存两个闸门（days 0 = 关闭，maxRows 0 = 不限）。
func (d *DB) SetActivityRetention(days, maxRows int) error {
	if days < 0 {
		days = 0
	}
	if days > ActivityRetentionMaxDays {
		days = ActivityRetentionMaxDays
	}
	if maxRows < 0 {
		maxRows = 0
	}
	if maxRows > 10000000 {
		maxRows = 10000000
	}
	if err := d.SetSetting(ActivityRetentionSetting, strconv.Itoa(days)); err != nil {
		return err
	}
	return d.SetSetting(ActivityRetentionMaxRowsKey, strconv.Itoa(maxRows))
}

// pruneActivity 跑一批删除，返回本批影响行数。
func (d *DB) pruneActivity(ctx context.Context, query, modifier string, batch int) (int64, error) {
	var n int64
	err := d.WriteQueued(func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, query, modifier, batch)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

// PruneActivityOlderThan 按天数批量删除「早于 days 天」且所属探索已结束的活动行。
// 返回本轮删除总数；达到 maxRows 上限会提前返回，剩下的留给下一轮。
func (d *DB) PruneActivityOlderThan(ctx context.Context, days, batch, maxRows int) (ActivityPruneResult, error) {
	var out ActivityPruneResult
	if days <= 0 {
		return out, nil
	}
	if batch <= 0 {
		batch = ActivityPruneBatchDefault
	}
	if maxRows <= 0 {
		maxRows = ActivityPruneMaxRowsDefault
	}
	modifier := fmt.Sprintf("-%d days", days)
	// 时间比较沿用存储页人工清理同一套写法（datetime() 而不是 julianday），
	// 避免历史行的 TEXT 格式与 Go 时间格式之间的时区/精度差异。
	const del = `DELETE FROM activity WHERE id IN (
  SELECT a.id FROM activity a
    JOIN explorations e ON e.id = a.exploration_id
   WHERE e.status <> 'open'
     AND datetime(a.created_at) < datetime('now', $1)
   ORDER BY a.id
   LIMIT $2)`
	for out.Deleted < int64(maxRows) {
		n, err := d.pruneActivity(ctx, del, modifier, batch)
		if err != nil {
			return out, err
		}
		out.Batches++
		out.Deleted += n
		if n < int64(batch) {
			break
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// PruneActivityToRowCap 把活动行数裁到 capRows 以下：只动已结束探索、且只动
// 早于 ActivityPruneFloorDays 的行（保护正在看的时间线）。capRows <= 0 = 不限。
func (d *DB) PruneActivityToRowCap(ctx context.Context, capRows, batch int) (ActivityPruneResult, error) {
	var out ActivityPruneResult
	if capRows <= 0 {
		return out, nil
	}
	if batch <= 0 {
		batch = ActivityPruneBatchDefault
	}
	var total int64
	if err := d.ReadDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM activity`).Scan(&total); err != nil {
		return out, err
	}
	if total <= int64(capRows) {
		return out, nil
	}
	floor := fmt.Sprintf("-%d days", ActivityPruneFloorDays)
	const del = `DELETE FROM activity WHERE id IN (
  SELECT a.id FROM activity a
    JOIN explorations e ON e.id = a.exploration_id
   WHERE e.status <> 'open'
     AND datetime(a.created_at) < datetime('now', $1)
   ORDER BY a.id
   LIMIT $2)`
	budget := total - int64(capRows)
	for out.Capped < budget {
		n, err := d.pruneActivity(ctx, del, floor, batch)
		if err != nil {
			return out, err
		}
		out.Batches++
		out.Capped += n
		if n < int64(batch) {
			break
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
	}
	return out, nil
}

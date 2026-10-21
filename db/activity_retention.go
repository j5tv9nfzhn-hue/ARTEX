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
// 存储页的「按天清理」是人工的一次性动作；这里补的是**兜底**：设置一个保留天数
// 后，后台每 6 小时把超过天数的执行轨迹按批删掉。默认 0 = 关闭，用户不设就永远
// 不自动删。
//
// 三条约束：
//  1. 只删**已结束**探索（status <> 'open'）的活动。正在跑的任务哪怕挂了很久也不
//     动——把还在跑的时间线删出空洞，比库大一点更糟。
//  2. 分批 + 单批一个事务（默认 5000 行/批，单轮上限 10 万行）：写锁持有时间是
//     「一批」而不是「全表」，读不会被一次长删除拖住。
//  3. 走单写协程（WriteQueued）：与正常写入共用同一条写路径，不额外抢锁。

const ActivityRetentionSetting = "activity_retention_days"

const (
	ActivityPruneBatchDefault   = 5000
	ActivityPruneMaxRowsDefault = 100000
	ActivityRetentionMaxDays    = 3650
)

// ActivityPruneResult 是单轮裁剪的结果，直接进日志。
type ActivityPruneResult struct {
	Deleted int64 `json:"deleted"`
	Batches int   `json:"batches"`
}

// ActivityRetentionDays 读保留天数；未设置/解析失败一律 0（= 关闭）。
func (d *DB) ActivityRetentionDays() (int, error) {
	raw, ok, err := d.GetSetting(ActivityRetentionSetting)
	if err != nil || !ok {
		return 0, err
	}
	n, cerr := strconv.Atoi(strings.TrimSpace(raw))
	if cerr != nil || n < 0 {
		return 0, nil
	}
	if n > ActivityRetentionMaxDays {
		n = ActivityRetentionMaxDays
	}
	return n, nil
}

// SetActivityRetentionDays 保存保留天数（0 = 关闭）。
func (d *DB) SetActivityRetentionDays(days int) error {
	if days < 0 {
		days = 0
	}
	if days > ActivityRetentionMaxDays {
		days = ActivityRetentionMaxDays
	}
	return d.SetSetting(ActivityRetentionSetting, strconv.Itoa(days))
}

// PruneActivityOlderThan 按批删除「早于 days 天」且所属探索已结束的活动行。
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
		var n int64
		err := d.WriteQueued(func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, del, modifier, batch)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
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

package server

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
)

// 活动流的自动保留（保底）。
//
// 存储页的「按天清理」是人工动作；这条后台循环补的是兜底：用户把「自动保留天数」
// 设成 N（默认 0 = 关闭）后，进程启动 3 分钟跑第一次，之后每 6 小时跑一次，按批
// 删除超过 N 天、且所属探索已结束的活动行。
//
// 为什么不做成「滚动窗口、无脑删」：
//   - 默认关闭，删除永远是用户显式打开的结果；
//   - 只动已结束的探索（见 db.PruneActivityOlderThan）；
//   - 单轮有行数上限，删不完留给下一轮，不长时间霸占写锁。

const (
	activityRetentionInterval   = 6 * time.Hour
	activityRetentionStartDelay = 3 * time.Minute
	activityRetentionBudget     = 5 * time.Minute
)

// activityPruneState 是这条循环的最近一次运行结果，只服务存储页展示。
type activityPruneState struct {
	mu          sync.Mutex
	lastRun     time.Time
	lastDeleted int64
	lastErr     string
}

func (a *activityPruneState) record(at time.Time, deleted int64, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastRun = at
	a.lastDeleted = deleted
	if err != nil {
		a.lastErr = err.Error()
	} else {
		a.lastErr = ""
	}
}

func (a *activityPruneState) snapshot() (time.Time, int64, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastRun, a.lastDeleted, a.lastErr
}

// startActivityRetention 启动兜底循环。数据库没就绪时直接不启动。
func (s *Server) startActivityRetention() {
	if s == nil || s.m == nil || s.m.pg == nil {
		return
	}
	go func() {
		timer := time.NewTimer(activityRetentionStartDelay)
		defer timer.Stop()
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
		}
		s.runActivityRetention()
		ticker := time.NewTicker(activityRetentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.runActivityRetention()
			}
		}
	}()
}

// runActivityRetention 跑一轮。天数 <= 0 时什么都不做（连删除语句都不发）。
func (s *Server) runActivityRetention() {
	if s == nil || s.m == nil || s.m.pg == nil {
		return
	}
	days, err := s.m.pg.ActivityRetentionDays()
	if err != nil {
		log.Printf("[activity-retention] 读取保留天数失败：%v", err)
		s.activityPrune.record(time.Now(), 0, err)
		return
	}
	if days <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, activityRetentionBudget)
	defer cancel()
	res, err := s.m.pg.PruneActivityOlderThan(ctx, days, db.ActivityPruneBatchDefault, db.ActivityPruneMaxRowsDefault)
	if err != nil {
		log.Printf("[activity-retention] 清理失败（保留 %d 天）：%v", days, err)
		s.activityPrune.record(time.Now(), 0, err)
		return
	}
	if res.Deleted > 0 {
		s.m.pg.ReclaimFreePages()
		log.Printf("[activity-retention] 已清理 %d 条活动（保留 %d 天，%d 批）", res.Deleted, days, res.Batches)
	}
	s.activityPrune.record(time.Now(), res.Deleted, nil)
}

// activityRetentionInfo 是 /storage/stats 里 activity_retention 那一段。
func (s *Server) activityRetentionInfo() map[string]any {
	days := 0
	if s.m != nil && s.m.pg != nil {
		if n, err := s.m.pg.ActivityRetentionDays(); err == nil {
			days = n
		}
	}
	last, deleted, lastErr := s.activityPrune.snapshot()
	out := map[string]any{
		"days":             days,
		"last_deleted":     deleted,
		"interval_minutes": int(activityRetentionInterval / time.Minute),
	}
	if !last.IsZero() {
		out["last_run_unix_ms"] = last.UnixMilli()
	}
	if lastErr != "" {
		out["last_error"] = lastErr
	}
	return out
}

// storageActivityRetention 保存「自动保留天数」。0 = 关闭。
func (s *Server) storageActivityRetention(w http.ResponseWriter, r *http.Request) {
	if !s.storageReady(w) {
		return
	}
	var req struct {
		Days *int `json:"days"`
	}
	if !storageGuardJSON(w, r, &req) {
		return
	}
	if req.Days == nil {
		writeErr(w, http.StatusBadRequest, "缺少 days（0 表示关闭）")
		return
	}
	days := *req.Days
	if days < 0 {
		days = 0
	}
	if days > db.ActivityRetentionMaxDays {
		writeErr(w, http.StatusBadRequest, "days 必须为 0 或 1–3650")
		return
	}
	if err := s.m.pg.SetActivityRetentionDays(days); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败："+err.Error())
		return
	}
	log.Printf("[activity-retention] 保留天数已设为 %d（0 = 关闭自动清理）", days)
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "saved": true})
}

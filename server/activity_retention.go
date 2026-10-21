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
// 存储页的「按天清理」是人工动作；这条后台循环补的是兜底：默认保留 14 天，行数
// 超过 10 万（≈200MB）再按最旧优先裁到上限。启动 3 分钟跑第一次，之后每 6 小时
// 一次。
//
// 三条都是为了不误删：默认值保守（14 天/10 万行）、只动已结束的探索、行数闸门有
// 3 天保护期（见 db.PruneActivityToRowCap）。

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
	lastCapped  int64
	lastErr     string
}

func (a *activityPruneState) record(at time.Time, deleted, capped int64, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastRun = at
	a.lastDeleted = deleted
	a.lastCapped = capped
	if err != nil {
		a.lastErr = err.Error()
	} else {
		a.lastErr = ""
	}
}

func (a *activityPruneState) snapshot() (time.Time, int64, int64, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastRun, a.lastDeleted, a.lastCapped, a.lastErr
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

// runActivityRetention 跑一轮：先按天数裁，再按行数上限裁。两个闸门都关时直接返回。
func (s *Server) runActivityRetention() {
	if s == nil || s.m == nil || s.m.pg == nil {
		return
	}
	days, err := s.m.pg.ActivityRetentionDays()
	if err != nil {
		log.Printf("[activity-retention] 读取保留天数失败：%v", err)
		s.activityPrune.record(time.Now(), 0, 0, err)
		return
	}
	capRows, err := s.m.pg.ActivityRetentionMaxRows()
	if err != nil {
		log.Printf("[activity-retention] 读取行数上限失败：%v", err)
		s.activityPrune.record(time.Now(), 0, 0, err)
		return
	}
	if days <= 0 && capRows <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, activityRetentionBudget)
	defer cancel()
	var byDays, byCap db.ActivityPruneResult
	if days > 0 {
		byDays, err = s.m.pg.PruneActivityOlderThan(ctx, days, db.ActivityPruneBatchDefault, db.ActivityPruneMaxRowsDefault)
		if err != nil {
			log.Printf("[activity-retention] 按天数清理失败（保留 %d 天）：%v", days, err)
			s.activityPrune.record(time.Now(), 0, 0, err)
			return
		}
	}
	// 按天数删完再按行数兜底：两段共用单写协程，顺序执行不会互相加锁。
	byCap, err = s.m.pg.PruneActivityToRowCap(ctx, capRows, db.ActivityPruneBatchDefault)
	if err != nil {
		log.Printf("[activity-retention] 按行数上限清理失败（上限 %d）：%v", capRows, err)
		s.activityPrune.record(time.Now(), byDays.Deleted, 0, err)
		return
	}
	deleted := byDays.Deleted + byCap.Capped
	if deleted > 0 {
		s.m.pg.ReclaimFreePages()
		log.Printf("[activity-retention] 已清理 %d 条活动（天数 %d 天删 %d，行数上限 %d 删 %d）",
			deleted, days, byDays.Deleted, capRows, byCap.Capped)
	}
	s.activityPrune.record(time.Now(), byDays.Deleted, byCap.Capped, nil)
}

// activityRetentionInfo 是 /storage/stats 里 activity_retention 那一段。
func (s *Server) activityRetentionInfo() map[string]any {
	days, capRows := db.ActivityRetentionDefaultDays, db.ActivityRetentionDefaultRows
	if s.m != nil && s.m.pg != nil {
		if n, err := s.m.pg.ActivityRetentionDays(); err == nil {
			days = n
		}
		if n, err := s.m.pg.ActivityRetentionMaxRows(); err == nil {
			capRows = n
		}
	}
	last, deleted, capped, lastErr := s.activityPrune.snapshot()
	out := map[string]any{
		"days":             days,
		"max_rows":         capRows,
		"floor_days":       db.ActivityPruneFloorDays,
		"last_deleted":     deleted,
		"last_capped":      capped,
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

// storageActivityRetention 保存两个闸门。days 0 = 关闭按天清理；max_rows 0 = 不限行数。
func (s *Server) storageActivityRetention(w http.ResponseWriter, r *http.Request) {
	if !s.storageReady(w) {
		return
	}
	var req struct {
		Days    *int `json:"days"`
		MaxRows *int `json:"max_rows"`
	}
	if !storageGuardJSON(w, r, &req) {
		return
	}
	days, err := s.m.pg.ActivityRetentionDays()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取现有策略失败："+err.Error())
		return
	}
	capRows, err := s.m.pg.ActivityRetentionMaxRows()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取现有策略失败："+err.Error())
		return
	}
	if req.Days != nil {
		days = *req.Days
	}
	if req.MaxRows != nil {
		capRows = *req.MaxRows
	}
	if days < 0 || days > db.ActivityRetentionMaxDays {
		writeErr(w, http.StatusBadRequest, "days 必须为 0 或 1–3650")
		return
	}
	if capRows < 0 || capRows > 10000000 {
		writeErr(w, http.StatusBadRequest, "max_rows 必须为 0（不限）或 1–10000000")
		return
	}
	if err := s.m.pg.SetActivityRetention(days, capRows); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败："+err.Error())
		return
	}
	log.Printf("[activity-retention] 策略已保存：保留 %d 天、上限 %d 行（0 = 不限）", days, capRows)
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "max_rows": capRows, "saved": true})
}

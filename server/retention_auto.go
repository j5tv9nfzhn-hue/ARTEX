package server

import (
	"context"
	"log"
	"sync"
	"time"
)

// 保留策略的「保底」执行。
//
// 现状（2026-10-21 只读核对）：usage / server_logs / 通知的保留策略只有**手动**的
// 「预览 → 执行」，不点就永远不删；只有 activity 那条另有自动兜底
// （server/activity_retention.go）。这里补上对等物：
//
//   - 不另写 SQL：复用 db 里那条带审计、带外键保护、带 token 时效校验的
//     CreateRetentionPreview → ExecuteRetention 路径，等于「后台替用户点一次」；
//   - 每轮最多 retentionAutoRounds 次，每次每表 ≤ policy.MaxRowsPerTable 行，
//     写锁按「一次事务」持有，不会长时间卡住引擎；
//   - policy.Enabled=false 时整体不动——自动执行不越过用户的开关；
//   - 只有真的删了行才 ReclaimFreePages，避免无谓的重写。
const (
	retentionAutoStartDelay = 5 * time.Minute
	retentionAutoInterval   = 6 * time.Hour
	retentionAutoBudget     = 10 * time.Minute
	retentionAutoRounds     = 8
)

// retentionAutoState 是最近一次自动清理的结果，只进日志/排查用。
type retentionAutoState struct {
	mu       sync.Mutex
	lastRun  time.Time
	lastRows int64
	lastErr  string
}

func (a *retentionAutoState) record(at time.Time, rows int64, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastRun = at
	a.lastRows = rows
	if err != nil {
		a.lastErr = err.Error()
	} else {
		a.lastErr = ""
	}
}

func (a *retentionAutoState) snapshot() (time.Time, int64, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastRun, a.lastRows, a.lastErr
}

// startRetentionAuto 启动兜底循环；数据库没就绪时直接不启动。
func (s *Server) startRetentionAuto() {
	if s == nil || s.m == nil || s.m.pg == nil || s.ctx == nil {
		return
	}
	go func() {
		timer := time.NewTimer(retentionAutoStartDelay)
		defer timer.Stop()
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
		}
		s.runRetentionAuto()
		ticker := time.NewTicker(retentionAutoInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.runRetentionAuto()
			}
		}
	}()
}

// runRetentionAuto 跑一轮：策略关闭就跳过；否则反复「预览 → 执行」直到没有候选。
func (s *Server) runRetentionAuto() {
	if s == nil || s.m == nil || s.m.pg == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, retentionAutoBudget)
	defer cancel()

	policy, err := s.m.pg.RetentionPolicy(ctx)
	if err != nil {
		log.Printf("[retention-auto] 读取保留策略失败：%v", err)
		s.retentionAuto.record(time.Now(), 0, err)
		return
	}
	if !policy.Enabled {
		s.retentionAuto.record(time.Now(), 0, nil)
		return
	}

	var deleted int64
	rounds := 0
	for ; rounds < retentionAutoRounds; rounds++ {
		if ctx.Err() != nil {
			break
		}
		preview, err := s.m.pg.CreateRetentionPreview(ctx)
		if err != nil {
			log.Printf("[retention-auto] 生成清理预览失败（第 %d 轮）：%v", rounds+1, err)
			s.retentionAuto.record(time.Now(), deleted, err)
			return
		}
		candidates := 0
		for _, c := range preview.Counts {
			candidates += c.Selected
		}
		if candidates == 0 {
			break // 没有过期数据：正常收敛
		}
		exec, err := s.m.pg.ExecuteRetention(ctx, preview.Token)
		if err != nil {
			log.Printf("[retention-auto] 执行清理失败（第 %d 轮，候选 %d 行）：%v", rounds+1, candidates, err)
			s.retentionAuto.record(time.Now(), deleted, err)
			return
		}
		var n int64
		for _, c := range exec.Counts {
			n += c.Deleted
		}
		deleted += n
		// 一行都没删掉（全被外键保护挡下）时再跑一轮也是空转。
		if n == 0 || !exec.Committed {
			break
		}
	}
	if deleted > 0 {
		s.m.pg.ReclaimFreePages()
		log.Printf("[retention-auto] 自动清理完成：%d 轮共删除 %d 行（策略：usage %d 天 / 日志 %d 天 / 通知 %d 天，每表每次上限 %d 行）",
			rounds, deleted, policy.UsageDays, policy.LogDays, policy.NotificationDays, policy.MaxRowsPerTable)
	}
	s.retentionAuto.record(time.Now(), deleted, nil)
}

package server

import (
	"context"
	"log"
	"time"

	"github.com/Autumn-27/artex/db"
)

// P2 的后台维护：崩溃现场修复（reaper）+ 到点回炉（retry sweeper）。
//
// 两者都只在「任务还能跑」时动作：暂停/删除/终态的任务一律跳过，账本留着不丢，
// 等它恢复后自然会被下一轮扫到。

const retrySweepInterval = 10 * time.Second

// StartMaintenance 启一次后台协程：先补崩溃现场，再周期性地把到点的分支放回队列。
// 由 server 启动时调用一次；测试里不调用（NewEngine 不起 goroutine）。
func (e *Engine) StartMaintenance(ctx context.Context) {
	if e == nil || e.m == nil || e.m.pg == nil {
		return
	}
	go func() {
		if n, err := e.ReapOrphanRuns(); err != nil {
			log.Printf("[reaper] 启动扫描失败: %v", err)
		} else if n > 0 {
			log.Printf("[reaper] 修复 %d 条中断运行，已补交接单", n)
		}
		tick := time.NewTicker(retrySweepInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				e.ProcessDueRetries()
			}
		}
	}()
}

// ReapOrphanRuns 扫「起了头但没收尾」的 run（ended_at IS NULL）。
//
// 能走到这里的只有进程被强杀/断电那种情况：正常路径（暂停、终止、收尾）都会先把
// 台账行收掉。对每一行：
//  1. 置 end_state=interrupted、stop_reason=crash；
//  2. 用 L1 机械抽取补一张交接单（模型已经不在了，但活动流还在）；
//  3. 若意图节点还卡在 running，放回 open，让 worker 接着跑。
func (e *Engine) ReapOrphanRuns() (int, error) {
	if e == nil || e.m == nil || e.m.pg == nil {
		return 0, nil
	}
	runs, err := e.m.pg.OrphanIntentRuns()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range runs {
		t, live := e.m.Task(r.TaskID)
		if !live || t == nil {
			// 启动早期内存里还没有这个任务：用最小上下文重建。Store 只依赖
			// exploration id，补交接单不需要整个任务对象活着——事实层来自活动流。
			t = &Task{ID: r.TaskID, ExpID: r.ExplorationID, Store: e.m.pg.Exploration(r.ExplorationID)}
		}
		intent, err := t.Store.GetNode(r.IntentID)
		if err != nil || intent == nil {
			_ = e.m.pg.FinishIntentRun(r.ID, "interrupted", "crash", "interrupted", "", 0)
			n++
			continue
		}
		e.settleInterruptedRun(t, intent, r.ID, r.StartedAt)
		// 节点若还卡在 running（上一次进程没来得及放手），放回 frontier。
		// 只有拿到内存里活着的 Task 才 Notify——重建出来的副本没有唤醒通道。
		if intent.State == "running" {
			if err := transitionIntentState(t.Store, r.IntentID, "running", "open"); err == nil && live {
				t.Notify()
			}
		}
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: "检测到一次被强制中断的运行（进程退出），已按执行记录补交接单，并将该意图放回队列"})
		n++
	}
	return n, nil
}

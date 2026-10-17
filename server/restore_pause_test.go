package server

import (
	"context"
	"strconv"
	"testing"
)

// 重启加载持久化任务时，未终态任务必须一律以暂停态载入：无论上次是运行中还是
// 排队，都不能自动拉起运行（否则上游 LLM 暂不可用时会连续报错）。
func TestRestoreTaskRuntimesLoadsTasksPaused(t *testing.T) {
	m1, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 2)
	for _, name := range []string{"restart pause running", "restart pause queued"} {
		task, err := m1.CreateTask(name, "context", nil, 3600, 0)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	// 第二个任务模拟「排队中」：重启前已准入但还在 FIFO 里等空闲位。
	if err := m1.EnqueueTask(ids[1], "resume"); err != nil {
		t.Fatal(err)
	}
	// 模拟重启：关掉旧 Manager，用同一数据库重新载入（内存句柄全部重建）。
	if err := m1.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{m: m, engine: NewEngine(m), ctx: ctx, chatBusy: map[string]bool{}}
	t.Cleanup(func() {
		cancel()
		for _, task := range m.List() {
			s.engine.StopTask(task.ID)
		}
		for _, id := range ids {
			if n, err := strconv.ParseInt(id, 10, 64); err == nil {
				_ = m.pg.DeleteTask(n)
			}
		}
		m.Close()
	})

	s.restoreTaskRuntimes()

	for _, id := range ids {
		task, ok := m.Task(id)
		if !ok {
			t.Fatalf("task %s 未随重启载入", id)
		}
		if lifecycle := task.lifecycleSnapshot(); !lifecycle.Paused || lifecycle.Queued {
			t.Fatalf("task %s 重启后未以暂停态载入: paused=%v queued=%v", id, lifecycle.Paused, lifecycle.Queued)
		}
		if !s.engine.IsPaused(id) {
			t.Fatalf("task %s 引擎未标记暂停，loop 可能自动开跑", id)
		}
		if s.occupiesConcurrencySlot(task) {
			t.Fatalf("task %s 暂停后仍占并发位", id)
		}
		n, _ := strconv.ParseInt(id, 10, 64)
		pt, err := m.pg.GetTask(n)
		if err != nil {
			t.Fatal(err)
		}
		if !pt.Paused || pt.Queued {
			t.Fatalf("task %s 数据库未持久化暂停: paused=%v queued=%v", id, pt.Paused, pt.Queued)
		}
	}
}

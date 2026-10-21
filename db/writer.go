package db

// 单写协程 + 批量提交。
//
// SQLite 是「一写多读」：同一时刻只允许一个写事务。此前每条写入各自开事务，并发一高
// 就在文件锁上排队（busy_timeout 内干等），而且提交次数 = 调用次数——Windows 上每次
// 提交还要过一次 fsync（常常还要穿过杀毒软件的过滤驱动）。
//
// 这里把「独立、可整体丢弃」的写入收进一个队列，由唯一一个写协程执行：同一时间窗口内
// 到达的多条写入合成一个事务提交，锁只抢一次、fsync 只做一次。调用方仍然同步拿到结果
// （提交完成后才返回），所以「写完立刻能按 id 读回」的语义不变。
//
// 边界：有强事务语义的写入（读-改-写、跨表一致性）继续走各自的 d.Begin()，不进队列；
// 队列里的 job 也不能再调用 WriteQueued（单协程会自锁）。

import (
	"context"
	"database/sql"
	"time"
)

const (
	// writeBatchWindow 是「凑批」的等待窗口:第一条写入到达后最多再等这么久收同伴。
	// 5ms 远小于一次 fsync 的代价,攒到 2 条就已经回本。
	writeBatchWindow = 5 * time.Millisecond
	// writeBatchMax 是单批上限:太大时锁持有时间变长,读会被 WAL 写事务拖慢。
	writeBatchMax = 200
	// writeQueueDepth 是队列缓冲:满了以后 submit 阻塞在 channel 上(背压),不丢写。
	writeQueueDepth = 512
)

type writeJob struct {
	run  func(*sql.Tx) error
	done chan error
}

type writeQueue struct {
	db *DB
	ch chan *writeJob
}

func newWriteQueue(d *DB) *writeQueue {
	q := &writeQueue{db: d, ch: make(chan *writeJob, writeQueueDepth)}
	go q.loop()
	return q
}

// writeQueue 懒初始化:测试里直接 &DB{DB: raw} 构造的句柄也能用。
func (d *DB) writer() *writeQueue {
	d.wqMu.Lock()
	defer d.wqMu.Unlock()
	if d.wq == nil {
		d.wq = newWriteQueue(d)
	}
	return d.wq
}

// WriteQueued 把一个独立写入提交给单写协程,等它所在批次提交完成后返回。
// ctx 不传:后台写入的等待预算由 busy_timeout 兜底(见 config.SQLiteBusyTimeout),
// 用请求 ctx 反而会在排队时把「本来会成功」的写入打断。
func (d *DB) WriteQueued(run func(*sql.Tx) error) error {
	return d.writer().submit(context.Background(), run)
}

func (q *writeQueue) submit(ctx context.Context, run func(*sql.Tx) error) error {
	job := &writeJob{run: run, done: make(chan error, 1)}
	select {
	case q.ch <- job:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-job.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// loop 是唯一执行写事务的协程。第一批之后窗口内到达的写入会并进同一批。
func (q *writeQueue) loop() {
	for first := range q.ch {
		batch := []*writeJob{first}
		timer := time.NewTimer(writeBatchWindow)
	drain:
		for len(batch) < writeBatchMax {
			select {
			case j := <-q.ch:
				batch = append(batch, j)
			case <-timer.C:
				break drain
			}
		}
		timer.Stop()
		q.commit(batch)
	}
}

// commit 用一个事务提交整批。任一条失败则整批回滚并通知全部等待者——批内的写入都是
// 可整体重放的 activity 追加,回滚不会丢语义;换成「一条失败只丢一条」需要在事务外
// 重试,反而把锁持有时间拉长。
func (q *writeQueue) commit(batch []*writeJob) {
	tx, err := q.db.Begin()
	if err != nil {
		for _, j := range batch {
			j.done <- err
		}
		return
	}
	for _, j := range batch {
		if err := j.run(tx); err != nil {
			_ = tx.Rollback()
			for _, k := range batch {
				k.done <- err
			}
			return
		}
	}
	if err := tx.Commit(); err != nil {
		for _, j := range batch {
			j.done <- err
		}
		return
	}
	for _, j := range batch {
		j.done <- nil
	}
}

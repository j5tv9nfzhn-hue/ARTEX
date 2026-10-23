package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 第三阶段：compaction_state 增 injected_bytes / digest_tokens_est。
//
// 两件事必须同时成立，否则这个改动就是负资产：
//  1. 老库（没有这两列）打开后能自动补列，且能正常读写；
//  2. start 事件不带产出规模时，**不能**把上一轮的数字冲成 0 或 NULL。
func TestCompactionStateMetricsColumns(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "compaction-metrics.db") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	// 造一个「没有新列」的老库形态。
	con, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := con.Exec(`CREATE TABLE compaction_state (
		scope TEXT PRIMARY KEY,
		phase TEXT NOT NULL DEFAULT 'idle',
		boundary_json TEXT NOT NULL DEFAULT '{}',
		started_at TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00','now'))
	)`); err != nil {
		con.Close()
		t.Fatalf("fixture schema: %v", err)
	}
	if err := con.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("老库补列失败: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	for _, col := range []string{"injected_bytes", "digest_tokens_est"} {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('compaction_state') WHERE name=?`, col).Scan(&n); err != nil {
			t.Fatalf("查列 %s: %v", col, err)
		}
		if n != 1 {
			t.Fatalf("列 %s 没有被补上", col)
		}
	}

	// 一次带统计的 finish。
	if err := d.PutCompactionState(ctx, "task-1", CompactionPhaseIdle, map[string]any{
		"phase": "minor", "injected_bytes": 1234, "digest_tokens_est": 987,
	}); err != nil {
		t.Fatalf("写入统计: %v", err)
	}
	st, err := d.GetCompactionState(ctx, "task-1")
	if err != nil || st == nil {
		t.Fatalf("读回: %v %v", st, err)
	}
	if st.InjectedBytes == nil || *st.InjectedBytes != 1234 {
		t.Fatalf("injected_bytes = %v，期望 1234", st.InjectedBytes)
	}
	if st.DigestTokensEst == nil || *st.DigestTokensEst != 987 {
		t.Fatalf("digest_tokens_est = %v，期望 987", st.DigestTokensEst)
	}

	// start 事件不带统计：不能冲掉上一轮的数字。
	if err := d.PutCompactionState(ctx, "task-1", CompactionPhaseRunning, map[string]any{"phase": "major"}); err != nil {
		t.Fatalf("写入 start: %v", err)
	}
	st2, err := d.GetCompactionState(ctx, "task-1")
	if err != nil || st2 == nil {
		t.Fatalf("读回 start: %v %v", st2, err)
	}
	if st2.InjectedBytes == nil || *st2.InjectedBytes != 1234 {
		t.Fatalf("start 事件把 injected_bytes 冲掉了：%v", st2.InjectedBytes)
	}
	if st2.DigestTokensEst == nil || *st2.DigestTokensEst != 987 {
		t.Fatalf("start 事件把 digest_tokens_est 冲掉了：%v", st2.DigestTokensEst)
	}

	// 从没跑过压缩的作用域：两列都是 NULL，不是 0。
	st3, err := d.GetCompactionState(ctx, "task-never")
	if err != nil {
		t.Fatalf("读回空作用域: %v", err)
	}
	if st3 != nil {
		t.Fatalf("不存在的作用域应返回 nil，得到 %+v", st3)
	}
}

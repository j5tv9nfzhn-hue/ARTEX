package server

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

// 回炉策略矩阵（报告 §9）：锁住「什么情况自动重跑、什么情况必须停手交给人」。
// 这些是纯函数判定，不碰数据库，改策略时先改这里。
func TestPlanRequeueMatrix(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name         string
		b            db.IntentBlocker
		retry        *db.BranchRetry
		taskRequeues int
		errHash      string
		want         string
	}{
		{"exhausted 首次立即回炉", db.IntentBlocker{EndState: "exhausted"}, &db.BranchRetry{}, 0, "", "now"},
		{"exhausted 第二次退避", db.IntentBlocker{EndState: "exhausted"}, &db.BranchRetry{Requeues: 1}, 1, "", "defer"},
		{"blocked 退避", db.IntentBlocker{EndState: "blocked"}, &db.BranchRetry{}, 0, "", "defer"},
		{"stopped 不自动、转人工", db.IntentBlocker{EndState: "stopped"}, &db.BranchRetry{}, 0, "", "human"},
		{"单分支到上限", db.IntentBlocker{EndState: "exhausted"}, &db.BranchRetry{Requeues: maxRequeuesPerIntent}, maxRequeuesPerIntent, "", "human"},
		{"整任务到上限", db.IntentBlocker{EndState: "blocked"}, &db.BranchRetry{Requeues: 1}, maxRequeuesPerTask, "", "human"},
		{"同一错误连续复现", db.IntentBlocker{EndState: "blocked"}, &db.BranchRetry{Requeues: 1, ErrorHash: "abc", ErrorStreak: 1}, 1, "abc", "human"},
		{"配额耗尽只重试一次", db.IntentBlocker{EndState: "blocked", StopReason: db.IntentBlockedLLMQuota}, &db.BranchRetry{Requeues: 1}, 1, "", "human"},
	}
	for _, c := range cases {
		got := planRequeue(now, c.b, c.retry, c.taskRequeues, c.errHash)
		if got.Action != c.want {
			t.Fatalf("%s: action=%s want=%s (%s)", c.name, got.Action, c.want, got.Reason)
		}
	}
}

func TestBackoffFor(t *testing.T) {
	if backoffFor(0) != 0 {
		t.Fatalf("第 0 次回炉不应有退避")
	}
	if backoffFor(1) != retryDelays[0] {
		t.Fatalf("第 1 次应退避 %v，得到 %v", retryDelays[0], backoffFor(1))
	}
	if backoffFor(99) != retryDelays[len(retryDelays)-1] {
		t.Fatalf("超出序列应取最后一档")
	}
}

func TestErrorHash(t *testing.T) {
	if errorHash("") != "" {
		t.Fatalf("空错误不应有哈希")
	}
	if errorHash("connection reset") != errorHash("connection reset ") {
		t.Fatalf("同一错误（仅尾部空白不同）应得到同一哈希")
	}
	if errorHash("a") == errorHash("b") {
		t.Fatalf("不同错误不应同哈希")
	}
}

package db

import "testing"

// ToolStats 从账本读，不从 activity 读：activity 会随任务数据一起被清理，
// 而「哪个工具错误率高」是清理之后还要回答的问题。
func TestToolStatsReadsLedger(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	const (
		toolA = "zz_test_stats_a"
		toolB = "zz_test_stats_b"
	)
	cleanup := func() {
		_, _ = d.Exec(`DELETE FROM tool_usage WHERE tool_key IN ($1,$2)`, toolA, toolB)
	}
	cleanup()
	defer cleanup()

	rows := []*ToolUsage{
		{ToolKey: toolA, AgentKey: "worker", ExplorationID: 5},
		{ToolKey: toolA, AgentKey: "worker", ExplorationID: 5, IsError: true},
		{ToolKey: toolA, AgentKey: "worker", ExplorationID: 6},
		{ToolKey: toolB, AgentKey: "worker", ExplorationID: 5, IsError: true},
	}
	for _, row := range rows {
		if err := d.InsertToolUsage(row); err != nil {
			t.Fatalf("insert %s: %v", row.ToolKey, err)
		}
	}

	byTool := func(expID *int64, q string) map[string]ToolStat {
		stats, err := d.ToolStats(expID, q)
		if err != nil {
			t.Fatalf("ToolStats(%v,%q): %v", expID, q, err)
		}
		out := map[string]ToolStat{}
		for _, s := range stats {
			out[s.Tool] = s
		}
		return out
	}

	all := byTool(nil, "")
	if got := all[toolA]; got.Total != 3 || got.Errors != 1 {
		t.Fatalf("toolA = %+v, want total=3 errors=1", got)
	}
	if got := all[toolB]; got.Total != 1 || got.Errors != 1 {
		t.Fatalf("toolB = %+v, want total=1 errors=1", got)
	}

	exp := int64(5)
	scoped := byTool(&exp, "")
	if got := scoped[toolA]; got.Total != 2 || got.Errors != 1 {
		t.Fatalf("exploration filter: toolA = %+v, want total=2 errors=1", got)
	}
	if got := scoped[toolB]; got.Total != 1 || got.Errors != 1 {
		t.Fatalf("exploration filter: toolB = %+v, want total=1 errors=1", got)
	}

	// 账本刻意不存工具入参，q 只能先在 activity 里解析成工具名。没有匹配时返回空，
	// 而不是退化成「全部工具」——那会让筛选看起来没生效。
	if got := byTool(nil, "zzz-no-such-keyword"); len(got) != 0 {
		t.Fatalf("keyword with no match returned %d tools, want 0", len(got))
	}
}

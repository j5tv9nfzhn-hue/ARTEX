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
		{ToolKey: toolA, AgentKey: "worker", ExplorationID: 5, IsError: true, ErrorClass: "param"},
		{ToolKey: toolA, AgentKey: "worker", ExplorationID: 6, IsError: true, ErrorClass: "param"},
		{ToolKey: toolA, AgentKey: "worker", ExplorationID: 6, IsError: true, ErrorClass: "quota"},
		{ToolKey: toolB, AgentKey: "worker", ExplorationID: 5, IsError: true, ErrorClass: "dialect"},
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
	// 同工具的多条 error_class 行必须合并成一条 ToolStat，而不是各占一行。
	if got := all[toolA]; got.Total != 4 || got.Errors != 3 {
		t.Fatalf("toolA = %+v, want total=4 errors=3", got)
	}
	if got := all[toolA].ErrorClasses; got["param"] != 2 || got["quota"] != 1 || len(got) != 2 {
		t.Fatalf("toolA error classes = %+v, want param=2 quota=1", got)
	}
	if got := all[toolB]; got.Total != 1 || got.Errors != 1 {
		t.Fatalf("toolB = %+v, want total=1 errors=1", got)
	}
	if got := all[toolB].ErrorClasses; got["dialect"] != 1 {
		t.Fatalf("toolB error classes = %+v, want dialect=1", got)
	}
	// 没有失败的工具不该带空 map（前端据此决定画不画分类行）。
	if got := all[toolA].ErrorClasses["nonexistent"]; got != 0 {
		t.Fatalf("unexpected class bucket: %d", got)
	}

	exp := int64(5)
	scoped := byTool(&exp, "")
	if got := scoped[toolA]; got.Total != 2 || got.Errors != 1 {
		t.Fatalf("exploration filter: toolA = %+v, want total=2 errors=1", got)
	}
	if got := scoped[toolA].ErrorClasses; got["param"] != 1 || len(got) != 1 {
		t.Fatalf("exploration filter: toolA error classes = %+v, want param=1", got)
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

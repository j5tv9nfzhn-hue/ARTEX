package db

import (
	"context"
	"strings"
	"testing"
)

// 触发信号的核心不变量：只有「确实不存在」且「确实重复」才报缺口。
// 少报会让沉淀机制形同虚设，多报则提议表被噪声淹没——两者都比没有更糟。

func seedMiss(t *testing.T, d *DB, skill, agent string, taskID any, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := d.Exec(`INSERT INTO skill_usage(skill,agent_key,task_id,found)
VALUES($1,$2,$3,false)`, skill, agent, taskID); err != nil {
			t.Fatalf("写入未命中记录: %v", err)
		}
	}
}

func TestSkillGapsNeedsRepeatedMisses(t *testing.T) {
	// 单次未命中更可能是模型措辞偏差，不构成缺口。
	d := openProposalDB(t)
	ctx := context.Background()
	seedMiss(t, d, "gate-typo", "planner", 11, 1)
	seedMiss(t, d, "gate-real", "planner", 12, 4)

	gaps, err := d.SkillGaps(ctx, DefaultSkillGapThreshold)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]SkillGap{}
	for _, g := range gaps {
		byName[g.Skill] = g
	}
	if _, leaked := byName["gate-typo"]; leaked {
		t.Error("单次未命中不应构成缺口")
	}
	real, ok := byName["gate-real"]
	if !ok {
		t.Fatalf("gate-real 缺口缺失: %+v", byName)
	}
	if real.Misses != 4 {
		t.Errorf("Misses = %d，应为 4", real.Misses)
	}
}

func TestSkillGapsIgnoresHits(t *testing.T) {
	// 命中（found=true）是"技能有用"，不是缺口。把它算进去会让最常用的
	// 技能反复出现在补技能列表里，方向完全反了。
	d := openProposalDB(t)
	ctx := context.Background()
	for i := 0; i < 9; i++ {
		if _, err := d.Exec(`INSERT INTO skill_usage(skill,agent_key,task_id,found)
VALUES('hits-only','planner',$1,true)`, i); err != nil {
			t.Fatal(err)
		}
	}
	gaps, err := d.SkillGaps(ctx, DefaultSkillGapThreshold)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gaps {
		if g.Skill == "hits-only" {
			t.Fatal("命中记录被当成了缺口")
		}
	}
}

func TestSkillGapsReportsAgentBreadth(t *testing.T) {
	// 广度决定优先级：一个 agent 反复要，和三个 agent 都要，不是同一件事。
	//
	// 断言只针对本测试写入的技能名：包内所有测试共用同一个测试库，别人的数据
	// 会一并进入结果集，对下标或总数的断言必然互相干扰。
	d := openProposalDB(t)
	ctx := context.Background()
	seedMiss(t, d, "breadth-narrow", "planner", 101, 5)
	seedMiss(t, d, "breadth-wide", "planner", 102, 2)
	seedMiss(t, d, "breadth-wide", "worker", 103, 2)
	seedMiss(t, d, "breadth-wide", "reviewer", 104, 2)

	gaps, err := d.SkillGaps(ctx, DefaultSkillGapThreshold)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]SkillGap{}
	for _, g := range gaps {
		byName[g.Skill] = g
	}
	wide, ok := byName["breadth-wide"]
	if !ok {
		t.Fatalf("breadth-wide 缺口缺失: %+v", byName)
	}
	if wide.Agents != 3 {
		t.Errorf("breadth-wide.Agents = %d，应为 3", wide.Agents)
	}
	if wide.Tasks != 3 {
		t.Errorf("breadth-wide.Tasks = %d，应为 3", wide.Tasks)
	}
	if len(wide.AgentKeys) != 3 {
		t.Errorf("breadth-wide.AgentKeys = %v，应有 3 个去重项", wide.AgentKeys)
	}
	narrow := byName["breadth-narrow"]
	if narrow.Agents != 1 {
		t.Errorf("breadth-narrow.Agents = %d，应为 1", narrow.Agents)
	}
}

func TestSkillGapsSortsByMisses(t *testing.T) {
	d := openProposalDB(t)
	ctx := context.Background()
	seedMiss(t, d, "sort-low", "planner", 201, 3)
	seedMiss(t, d, "sort-high", "planner", 202, 9)
	gaps, err := d.SkillGaps(ctx, DefaultSkillGapThreshold)
	if err != nil {
		t.Fatal(err)
	}
	// 断言相对次序而非绝对下标——结果集里还有别的测试写入的技能。
	posLow, posHigh := -1, -1
	for i, g := range gaps {
		switch g.Skill {
		case "sort-low":
			posLow = i
		case "sort-high":
			posHigh = i
		}
	}
	if posLow < 0 || posHigh < 0 {
		t.Fatalf("sort-low/sort-high 未出现在结果中: %+v", gaps)
	}
	if posHigh > posLow {
		t.Errorf("未命中多的应排在前面：sort-high 在 %d，sort-low 在 %d", posHigh, posLow)
	}
}

func TestSkillGapEvidenceIsCheckable(t *testing.T) {
	// evidence 是审阅者唯一的核对依据。它必须带上可复算的数字，
	// 否则提议就只是一段无法核对的散文。
	g := SkillGap{Skill: "x", Misses: 7, Agents: 2, Tasks: 3,
		AgentKeys: []string{"a", "b"}}
	e := SkillGapEvidence(g)
	for _, want := range []string{"x7", "agents=2", "tasks=3", "[a b]"} {
		if !strings.Contains(e, want) {
			t.Errorf("evidence %q 缺少 %q", e, want)
		}
	}
	empty := SkillGapEvidence(SkillGap{Skill: "y", Misses: 4})
	if !strings.Contains(empty, "无 agent 记录") {
		t.Errorf("无 agent 时应显式说明，实际 %q", empty)
	}
}

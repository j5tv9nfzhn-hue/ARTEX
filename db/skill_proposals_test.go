package db

import (
	"context"
	"strings"
	"testing"
)

// 沉淀门控要成立，靠的是"提议不会自己变成技能"。下面每条都针对那个不变量：
// 未审阅的提议不写盘、已审阅的不能改、审阅记录可追溯。

func openProposalDB(t *testing.T) *DB {
	t.Helper()
	// Reuse the package's shared test database so the proposals table lands in
	// the same place the application will read it from, rather than a throwaway
	// file that would never exercise the real schema application path.
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("数据库不可用: %v", err)
	}
	if err := d.ensureSkillProposalTable(); err != nil {
		t.Fatalf("建表: %v", err)
	}
	return d
}

func TestSkillProposalStagesWithoutWriting(t *testing.T) {
	d := openProposalDB(t)
	ctx := context.Background()

	p, err := d.ProposeSkill(ctx, SkillProposal{
		ID: "p1", Skill: "demo", Action: "create",
		Content: "## When to Use\n\n演示。\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != SkillProposalPending {
		t.Fatalf("新提议状态 = %q，应为 pending", p.Status)
	}

	got, err := d.GetSkillProposal(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != p.Content {
		t.Error("内容在暂存过程中被改动")
	}
	if got.Origin != "agent" {
		t.Errorf("Origin = %q，应默认为 agent", got.Origin)
	}
}

func TestSkillProposalRequiresEvidence(t *testing.T) {
	// 没有依据的提议仍可提交（Agent 可能确实不知道依据），但审阅者必须能
	// 看出它是空的——Evidence 字段留空就是那个信号。
	d := openProposalDB(t)
	ctx := context.Background()
	p, err := d.ProposeSkill(ctx, SkillProposal{ID: "p2", Skill: "demo", Action: "create", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Evidence != "" {
		t.Errorf("未提供依据时 Evidence = %q，应为空以便审阅者识别", p.Evidence)
	}

	if _, err := d.ProposeSkill(ctx, SkillProposal{ID: "p3", Skill: "demo", Action: "create", Content: "x", Evidence: "retest=7"}); err != nil {
		t.Fatal(err)
	}
	got, _ := d.GetSkillProposal(ctx, "p3")
	if got.Evidence != "retest=7" {
		t.Errorf("依据未保存: %q", got.Evidence)
	}
}

func TestSkillProposalReviewIsOnceOnly(t *testing.T) {
	d := openProposalDB(t)
	ctx := context.Background()
	if _, err := d.ProposeSkill(ctx, SkillProposal{ID: "p4", Skill: "demo", Action: "create", Content: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReviewSkillProposal(ctx, "p4", SkillProposalApplied, "内容属实"); err != nil {
		t.Fatalf("首次审阅失败: %v", err)
	}
	// 二次审阅必须失败，否则同一条提议能被反复改判，历史不可信。
	if _, err := d.ReviewSkillProposal(ctx, "p4", SkillProposalRejected, "反悔"); err != ErrSkillProposalConflict {
		t.Fatalf("二次审阅 err = %v，应为 ErrSkillProposalConflict", err)
	}
	got, _ := d.GetSkillProposal(ctx, "p4")
	if got.Status != SkillProposalApplied || got.Note != "内容属实" {
		t.Errorf("审阅结论被覆盖: status=%q note=%q", got.Status, got.Note)
	}
}

func TestSkillProposalUpdateContentBeforeReview(t *testing.T) {
	d := openProposalDB(t)
	ctx := context.Background()
	if _, err := d.ProposeSkill(ctx, SkillProposal{ID: "p-edit", Skill: "demo", Action: "create", Content: "旧正文"}); err != nil {
		t.Fatal(err)
	}
	updated, err := d.UpdateSkillProposalContent(ctx, "p-edit", "审阅者修订后的正文")
	if err != nil {
		t.Fatalf("待审提议修订失败: %v", err)
	}
	if updated.Content != "审阅者修订后的正文" {
		t.Fatalf("修订内容未生效: %q", updated.Content)
	}
	if _, err := d.ReviewSkillProposal(ctx, "p-edit", SkillProposalApplied, "已修订通过"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpdateSkillProposalContent(ctx, "p-edit", "已审阅后篡改"); err != ErrSkillProposalConflict {
		t.Fatalf("已审阅提议不应允许修改正文, got err=%v", err)
	}
	counts, err := d.CountSkillProposals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts[SkillProposalApplied] < 1 {
		t.Fatalf("CountSkillProposals 计数异常: %+v", counts)
	}
	all, err := d.ListSkillProposals(ctx, "all", 50)
	if err != nil || len(all) == 0 {
		t.Fatalf("ListSkillProposals(all) 失败: err=%v len=%d", err, len(all))
	}
}

func TestSkillProposalRejectsBadInput(t *testing.T) {
	d := openProposalDB(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		p    SkillProposal
	}{
		{"无 id", SkillProposal{Skill: "demo", Content: "x"}},
		{"空内容", SkillProposal{ID: "a", Skill: "demo", Content: "   "}},
		{"非法技能名", SkillProposal{ID: "b", Skill: "Demo/../x", Content: "x"}},
	} {
		if _, err := d.ProposeSkill(ctx, tc.p); err == nil {
			t.Errorf("%s: 期望报错，实际通过", tc.name)
		}
	}
}

func TestSkillProposalIdempotent(t *testing.T) {
	d := openProposalDB(t)
	ctx := context.Background()
	// 同一 id 重复提交（会话重试）不得产生第二条记录。
	for i := 0; i < 2; i++ {
		if _, err := d.ProposeSkill(ctx, SkillProposal{ID: "dup", Skill: "demo", Action: "create", Content: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := d.ListSkillProposals(ctx, SkillProposalPending, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range list {
		if p.ID == "dup" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("重复 id 产生了 %d 条记录", n)
	}
}

func TestSkillLinterWarnsOnIncidentLogs(t *testing.T) {
	// 内容纪律：捕获教训而非日志。事件编号与日期密集出现即告警。
	log := strings.Repeat("修复 #1234 与 #2345，见 2026-01-02。", 10)
	warnings := LintSkillContent(log)
	if !hasRule(warnings, "incident-log-shape") {
		t.Errorf("事故日志形态未被告警，得到 %v", warnings)
	}

	// 正常技能：规则本身带编号和日期也不该被误伤。
	ok := "## Procedure\n\n1. 按 CVE-2021-44228 的缓解方案配置。\n2. 用 #1234 追踪回归。\n"
	if hasRule(LintSkillContent(ok), "incident-log-shape") {
		t.Error("正常内容被误判为事故日志")
	}
}

func TestSkillLinterWarnsOnOversizedBody(t *testing.T) {
	body := strings.Repeat("步骤。", 12000) // 36000 字符
	if !hasRule(LintSkillContent(body), "oversized-body") {
		t.Error("超长正文未告警")
	}
	if hasRule(LintSkillContent("短内容"), "oversized-body") {
		t.Error("短正文被误告警")
	}
}

func hasRule(ws []LintWarning, rule string) bool {
	for _, w := range ws {
		if w.Rule == rule {
			return true
		}
	}
	return false
}

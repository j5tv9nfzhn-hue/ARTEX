package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 门控要成立，靠的是一个不变量：**提议不会自己变成技能**。
// 下面每条都盯住它的一个面：暂存不落盘、审阅才落盘、审阅一次、重复提交幂等、
// 以及门控关闭时能退回直写。
//
// 测试直接调 proposeOrWrite / reviewProposal / gapReport，而不穿过 actool.CoreTool：
// 后者是 Build 出来的不可透明结构，测它只能测到参数解包，真正承载行为的逻辑
// 反倒被挡住。

func newSkillServer(t *testing.T) (*Server, *db.DB, string) {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test database not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("database unavailable (%v) — skipping", err)
	}
	dir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Server{m: &Manager{pg: pg}, skillDir: dir}, pg, dir
}

func skillDoc(name string) string {
	return buildSkillDocument(name, "测试用技能", "## When to Use\n\n测试。\n")
}

func skillFileExists(dir, name, file string) bool {
	if file == "" {
		file = "SKILL.md"
	}
	_, err := os.Stat(filepath.Join(dir, name, file))
	return err == nil
}

func findProposal(t *testing.T, pg *db.DB, skill string) *db.SkillProposal {
	t.Helper()
	list, err := pg.ListSkillProposals(context.Background(), db.SkillProposalPending, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := range list {
		if list[i].Skill == skill {
			return &list[i]
		}
	}
	t.Fatalf("未找到技能 %s 的待审提议", skill)
	return nil
}

func TestSkillGateStagesInsteadOfWriting(t *testing.T) {
	s, pg, dir := newSkillServer(t)
	if err := pg.SetBool(settingSkillReviewGate, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.proposeOrWrite(context.Background(), "gated-skill", "", skillDoc("gated-skill"),
		"测试依据", "agent"); err != nil {
		t.Fatal(err)
	}
	if skillFileExists(dir, "gated-skill", "SKILL.md") {
		t.Fatal("门控开启时写入仍然落盘了")
	}
	p := findProposal(t, pg, "gated-skill")
	if p.Status != db.SkillProposalPending {
		t.Errorf("状态 = %q，应为 pending", p.Status)
	}
	// 提议里必须是落地后的完整文件（含 frontmatter），否则审阅者得自己拼装，
	// 审阅就失去意义了。
	if !strings.HasPrefix(p.Content, "---\nname: gated-skill\n") {
		t.Errorf("提议内容不是完整 SKILL.md：%q", p.Content[:min(40, len(p.Content))])
	}
}

func TestSkillReviewApproveWritesDisk(t *testing.T) {
	s, pg, dir := newSkillServer(t)
	if err := pg.SetBool(settingSkillReviewGate, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.proposeOrWrite(context.Background(), "approved-skill", "", skillDoc("approved-skill"),
		"测试依据", "agent"); err != nil {
		t.Fatal(err)
	}
	p := findProposal(t, pg, "approved-skill")
	if _, err := s.reviewProposal(context.Background(), p.ID, "approve", "依据属实"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if !skillFileExists(dir, "approved-skill", "SKILL.md") {
		t.Fatal("批准后未写入磁盘")
	}
	got, err := pg.GetSkillProposal(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != db.SkillProposalApplied || got.Note != "依据属实" {
		t.Errorf("审阅结论未留档: status=%q note=%q", got.Status, got.Note)
	}
}

func TestSkillReviewRejectLeavesNoFile(t *testing.T) {
	s, pg, dir := newSkillServer(t)
	if err := pg.SetBool(settingSkillReviewGate, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.proposeOrWrite(context.Background(), "rejected-skill", "", skillDoc("rejected-skill"),
		"测试依据", "agent"); err != nil {
		t.Fatal(err)
	}
	p := findProposal(t, pg, "rejected-skill")
	if _, err := s.reviewProposal(context.Background(), p.ID, "reject", "内容是日志不是规则"); err != nil {
		t.Fatal(err)
	}
	if skillFileExists(dir, "rejected-skill", "SKILL.md") {
		t.Fatal("否决后不应落盘")
	}
	// 留档：被否决的提议要能回答「为什么当时没批」。
	got, _ := pg.GetSkillProposal(context.Background(), p.ID)
	if got.Status != db.SkillProposalRejected || got.Note == "" {
		t.Errorf("否决应留档: status=%q note=%q", got.Status, got.Note)
	}
}

func TestSkillReviewIsOnceOnly(t *testing.T) {
	s, pg, dir := newSkillServer(t)
	if err := pg.SetBool(settingSkillReviewGate, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.proposeOrWrite(context.Background(), "once-only-skill", "", skillDoc("once-only-skill"),
		"测试依据", "agent"); err != nil {
		t.Fatal(err)
	}
	p := findProposal(t, pg, "once-only-skill")
	if _, err := s.reviewProposal(context.Background(), p.ID, "approve", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.reviewProposal(context.Background(), p.ID, "reject", "反悔"); err == nil {
		t.Fatal("二次审阅应被拒绝")
	}
	// 关键：反悔不能改判，历史必须可信。
	got, _ := pg.GetSkillProposal(context.Background(), p.ID)
	if got.Status != db.SkillProposalApplied {
		t.Errorf("状态被反悔改写为 %q", got.Status)
	}
	if !skillFileExists(dir, "once-only-skill", "SKILL.md") {
		t.Error("已批准的技能文件不应消失")
	}
}

func TestSkillReviewRejectsBadDecision(t *testing.T) {
	s, pg, _ := newSkillServer(t)
	if err := pg.SetBool(settingSkillReviewGate, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.proposeOrWrite(context.Background(), "bad-decision-skill", "", skillDoc("bad-decision-skill"),
		"", "agent"); err != nil {
		t.Fatal(err)
	}
	p := findProposal(t, pg, "bad-decision-skill")
	// 拼错的 decision 不能被当成 reject 静默处理。
	if _, err := s.reviewProposal(context.Background(), p.ID, "aprove", ""); err == nil {
		t.Fatal("非法 decision 应报错而不是被当作 reject")
	}
	got, _ := pg.GetSkillProposal(context.Background(), p.ID)
	if got.Status != db.SkillProposalPending {
		t.Errorf("非法 decision 不应改变状态，实际 %q", got.Status)
	}
}

func TestSkillGateOffWritesDirectly(t *testing.T) {
	// 门控可关：那是用户的显式选择，不是默认值。关掉后应退回原有直写行为。
	s, pg, dir := newSkillServer(t)
	if err := pg.SetBool(settingSkillReviewGate, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.proposeOrWrite(context.Background(), "direct-skill", "", skillDoc("direct-skill"),
		"", "agent"); err != nil {
		t.Fatal(err)
	}
	if !skillFileExists(dir, "direct-skill", "SKILL.md") {
		t.Fatal("门控关闭时应直接写盘")
	}
	list, _ := pg.ListSkillProposals(context.Background(), db.SkillProposalPending, 100)
	for _, p := range list {
		if p.Skill == "direct-skill" {
			t.Fatal("门控关闭时不应登记提议")
		}
	}
}

func TestStageSkillProposalAlwaysStagesEvenWhenGateOff(t *testing.T) {
	// propose_skill 工具走 stageSkillProposal，即使门控关闭也必须进入待审队列。
	s, pg, dir := newSkillServer(t)
	if err := pg.SetBool(settingSkillReviewGate, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.stageSkillProposal(context.Background(), "always-staged-skill", "", skillDoc("always-staged-skill"),
		"缺口未命中记录", "agent"); err != nil {
		t.Fatal(err)
	}
	if skillFileExists(dir, "always-staged-skill", "SKILL.md") {
		t.Fatal("stageSkillProposal 即使在门控关闭时也不应直接落盘")
	}
	p := findProposal(t, pg, "always-staged-skill")
	if p.Status != db.SkillProposalPending {
		t.Fatalf("status=%q, want pending", p.Status)
	}
}

func TestSkillProposalsHTTPAndContentEdit(t *testing.T) {
	s, pg, dir := newSkillServer(t)
	if err := pg.SetBool(settingSkillReviewGate, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.stageSkillProposal(context.Background(), "http-review-skill", "", skillDoc("http-review-skill"),
		"测试 HTTP 审阅", "curator"); err != nil {
		t.Fatal(err)
	}
	p := findProposal(t, pg, "http-review-skill")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/skills/proposals", s.fsListSkillProposals)
	mux.HandleFunc("POST /api/skills/proposals/{id}/review", s.fsReviewSkillProposal)

	req := httptest.NewRequest(http.MethodGet, "/api/skills/proposals?status=all", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/skills/proposals status=%d body=%s", rec.Code, rec.Body.String())
	}

	editedContent := buildSkillDocument("http-review-skill", "修订后的技能描述", "## 何时使用\n\n人工修订后批准。\n")
	payload, _ := json.Marshal(map[string]any{
		"decision": "approve",
		"note":     "修订正文后批准",
		"content":  editedContent,
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/skills/proposals/"+p.ID+"/review", bytes.NewReader(payload))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("POST review status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "http-review-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected approved skill on disk: %v", err)
	}
	if string(raw) != editedContent {
		t.Fatalf("disk content mismatch:\ngot:  %s\nwant: %s", string(raw), editedContent)
	}
}

func TestSkillProposalsBatchReviewAndMerge(t *testing.T) {
	s, pg, dir := newSkillServer(t)
	if err := pg.SetBool(settingSkillReviewGate, true); err != nil {
		t.Fatal(err)
	}
	doc1 := buildSkillDocument("case-web-101", "案例101", "## 已验证的有效路径\n\n- 路径A\n\n## 失败、误区与止损\n\n- 止损A\n")
	doc2 := buildSkillDocument("case-web-102", "案例102", "## 已验证的有效路径\n\n- 路径B\n\n## 失败、误区与止损\n\n- 止损B\n")
	if _, err := s.stageSkillProposal(context.Background(), "case-web-101", "", doc1, "task=101", "curator"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.stageSkillProposal(context.Background(), "case-web-102", "", doc2, "task=102", "curator"); err != nil {
		t.Fatal(err)
	}
	p1 := findProposal(t, pg, "case-web-101")
	p2 := findProposal(t, pg, "case-web-102")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/skills/proposals/merge", s.fsMergeSkillProposals)
	mux.HandleFunc("POST /api/skills/proposals/batch-review", s.fsBatchReviewSkillProposals)
	mux.HandleFunc("GET /api/system/verification-capabilities", s.sysVerificationCapabilities)

	mergePayload, _ := json.Marshal(map[string]any{
		"ids":            []string{p1.ID, p2.ID},
		"name":           "merged-coupon-tactics",
		"description":    "合并优惠券并发与越权复盘经验",
		"apply":          true,
		"reject_sources": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/skills/proposals/merge", bytes.NewReader(mergePayload))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST merge status=%d body=%s", rec.Code, rec.Body.String())
	}
	mergedRaw, err := os.ReadFile(filepath.Join(dir, "merged-coupon-tactics", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected merged skill on disk: %v", err)
	}
	if !strings.Contains(string(mergedRaw), "路径A") || !strings.Contains(string(mergedRaw), "路径B") {
		t.Fatalf("merged content missing source paths:\n%s", string(mergedRaw))
	}

	reqCap := httptest.NewRequest(http.MethodGet, "/api/system/verification-capabilities", nil)
	recCap := httptest.NewRecorder()
	mux.ServeHTTP(recCap, reqCap)
	if recCap.Code != http.StatusOK || !strings.Contains(recCap.Body.String(), "altcha_v1_sha256_sha512") {
		t.Fatalf("GET verification-capabilities unexpected response: %d %s", recCap.Code, recCap.Body.String())
	}
}

func TestSkillGateDefaultsOn(t *testing.T) {
	// 默认方向必须是安全的那一侧。取不到开关时（数据库不可用）同样按开启处理。
	s, pg, _ := newSkillServer(t)
	if err := pg.SetBool(settingSkillReviewGate, true); err != nil {
		t.Fatal(err)
	}
	if !s.SkillReviewGateEnabled() {
		t.Fatal("显式开启后应报告为开启")
	}
	if err := pg.SetBool(settingSkillReviewGate, false); err != nil {
		t.Fatal(err)
	}
	if s.SkillReviewGateEnabled() {
		t.Fatal("显式关闭后应报告为关闭")
	}
	var nilServer *Server
	if !nilServer.SkillReviewGateEnabled() {
		t.Fatal("取不到开关时应偏向安全的一侧（开启）")
	}
}

func TestSkillProposalIDIsContentDerived(t *testing.T) {
	// 同一份提议重复提交（会话重试）必须落到同一条记录，审阅者才不必
	// 自己辨认哪些是重复的。
	a := skillProposalID("s", "", "content")
	if a != skillProposalID("s", "", "content") {
		t.Error("相同内容应得到相同 id")
	}
	if a == skillProposalID("s", "", "other") {
		t.Error("不同内容不应得到相同 id")
	}
	if a == skillProposalID("other", "", "content") {
		t.Error("不同技能名不应得到相同 id")
	}
}

func TestSkillGapsReportSkipsExistingSkills(t *testing.T) {
	// 目录里已有的技能不是缺口。不做这个判断的话，Agent 会对着一个早就
	// 存在的技能提提案。
	s, pg, dir := newSkillServer(t)
	for i := 0; i < 5; i++ {
		for _, name := range []string{"gaprep-existing", "gaprep-absent"} {
			if _, err := pg.Exec(`INSERT INTO skill_usage(skill,agent_key,task_id,found)
VALUES($1,'planner',$2,false)`, name, i); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "gaprep-existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := s.gapReport(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "gaprep-existing") {
		t.Errorf("已存在的技能不应作为缺口出现：%s", out)
	}
	if !strings.Contains(out, "gaprep-absent") {
		t.Errorf("确实缺失的技能应出现：%s", out)
	}
	// 缺口清单要能被审阅者复算，所以必须带上 evidence。
	if !strings.Contains(out, "x5") {
		t.Errorf("缺口条目应带可复算的未命中次数：%s", out)
	}
}

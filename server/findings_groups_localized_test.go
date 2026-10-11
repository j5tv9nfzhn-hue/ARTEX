package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 漏洞后续 API 中文化回归测试，复用 assertChineseError。
// TestDeepenFindingBodyTooLargeLocalized 真实验证 413 正文；
// MaxBytesReader 解码阶段即返回，不访问 s.m.pg，因此空 Server 即可测试。
func TestDeepenFindingBodyTooLargeLocalized(t *testing.T) {
	s := &Server{}
	body := `{"description":"` + strings.Repeat("a", 33<<10) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/exploration/findings/1/deepen", strings.NewReader(body))
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	s.deepenFinding(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d，预期 = %d（正文 %q）", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应 JSON 失败：%v（正文 %q）", err, rec.Body.String())
	}
	const want = "请求正文过大"
	if resp.Error != want {
		t.Fatalf("响应文案 = %q，预期 = %q", resp.Error, want)
	}
	assertChineseError(t, "body_too_large", resp.Error)
}

// TestFindingFollowUpAuditSummaryLocalized 固定后续意图活动摘要为中文。
// AddFindingFollowUpIntent 需要数据库事务，故无数据库时只检查常量。
// 活动摘要与模型读取的用户 description payload 分离，仅用于时间线展示。
func TestFindingFollowUpAuditSummaryLocalized(t *testing.T) {
	assertChineseError(t, "follow_up_summary", auditFindingFollowUpSummary)
}

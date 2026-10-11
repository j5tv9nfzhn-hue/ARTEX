package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Autumn-27/artex/db"
)

// containsHangul 检测韩文并返回第一个命中的字符。
// 中文、英文、数字和符号均允许，技术原文不应被误判为语言残留。
func containsHangul(s string) (rune, bool) {
	for _, r := range s {
		if unicode.Is(unicode.Hangul, r) {
			return r, true
		}
	}
	return 0, false
}

func sampleFindingNode(t *testing.T) *db.Node {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"vulnclass": "SQL Injection",
		"name":      "登录表单 SQL 注入",
		"severity":  "high",
		"summary":   "登录参数中确认存在报错型 SQL 注入。",
		"evidence":  map[string]string{"poc": "' OR '1'='1"},
	})
	if err != nil {
		t.Fatalf("序列化 payload 失败：%v", err)
	}
	return &db.Node{ID: 1, Kind: "finding", Payload: payload}
}

func sampleDBFinding() *db.DBFinding {
	return &db.DBFinding{
		ID:              123,
		VulnClass:       "SQL Injection",
		Name:            "登录表单 SQL 注入",
		Severity:        "high",
		Summary:         "登录参数中确认存在报错型 SQL 注入。",
		Evidence:        "' OR '1'='1",
		Status:          "confirmed",
		Report:          "详细分析正文。",
		CreatedAt:       time.Date(2026, 10, 4, 2, 30, 0, 0, time.UTC),
		EvidenceVersion: 2,
		TaskDescription: "演示目标渗透测试",
		TrafficBindings: []db.FindingTrafficBinding{
			{
				ID:   9,
				Role: "request",
				Note: "注入 payload 的发送位置",
				Snapshot: db.TrafficEvidenceSnapshot{
					Method: "POST",
					URL:    "https://sandbox.local/login",
					Status: 200,
				},
			},
		},
	}
}

// TestReportMarkdownLocalized 验证探索图报告 report.Markdown 的中文结构，不允许韩文残留。
func TestReportMarkdownLocalized(t *testing.T) {
	out := Markdown(Input{
		Title:       "演示任务",
		Goal:        "全面检查沙箱",
		GeneratedAt: time.Date(2026, 10, 4, 2, 30, 0, 0, time.UTC),
		AssetCounts: map[string]int{"host": 2, "url": 5},
		Findings:    []*db.Node{sampleFindingNode(t)},
	})
	if r, ok := containsHangul(out); ok {
		t.Fatalf("报告结构仍有韩文残留 %q：\n%s", string(r), out)
	}
	for _, want := range []string{
		"# 渗透测试报告：演示任务",
		"- **任务目标**：全面检查沙箱",
		"- **生成时间**：",
		"## 概述",
		"- 已确认漏洞：**1** 个",
		"## 漏洞",
		"**PoC / 证据：**",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("报告结构缺少 %q", want)
		}
	}
}

// TestReportMarkdownEmptyLocalized 验证没有漏洞时的中文提示。
func TestReportMarkdownEmptyLocalized(t *testing.T) {
	out := Markdown(Input{Title: "空任务", GeneratedAt: time.Now()})
	if r, ok := containsHangul(out); ok {
		t.Fatalf("空报告仍有韩文残留 %q：\n%s", string(r), out)
	}
	if !strings.Contains(out, "_本次没有已确认漏洞。_") {
		t.Errorf("空报告提示文案不是中文：\n%s", out)
	}
}

// TestFindingsMarkdownLocalized 验证汇总和独立 Markdown 的中文结构与严重等级标签。
func TestFindingsMarkdownLocalized(t *testing.T) {
	f := sampleDBFinding()
	out := FindingsMarkdown([]*db.DBFinding{f}, time.Date(2026, 10, 4, 2, 30, 0, 0, time.UTC))
	if r, ok := containsHangul(out); ok {
		t.Fatalf("漏洞汇总报告仍有韩文残留 %q：\n%s", string(r), out)
	}
	for _, want := range []string{
		"# 漏洞汇总报告",
		"- **生成时间**：",
		"- **漏洞总数**：1 个",
		"## 概述",
		"| 严重等级 | 数量 |",
		"| 严重 |", "| 高危 |", "| 中危 |", "| 低危 |", // 严重等级标签与 UI status.severity 一致。
		"## 漏洞详情",
		"- **类型**：SQL Injection",
		"- **状态**：confirmed",
		"- **所属任务**：演示目标渗透测试",
		"- **发现时间**：",
		"**证据：**",
		"**详细报告：**",
		"## 关联流量证据",
		"证据版本：2，绑定数量：1。",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("汇总报告缺少 %q", want)
		}
	}

	single := SingleFindingMarkdown(f, time.Date(2026, 10, 4, 2, 30, 0, 0, time.UTC))
	if r, ok := containsHangul(single); ok {
		t.Fatalf("单条报告仍有韩文残留 %q：\n%s", string(r), single)
	}
	for _, want := range []string{
		"- **严重等级**：high",
		"## 概述",
		"## 证据",
		"## 详细报告",
		"[请求报文](evidence/123/9/request.http)",
		"[响应报文](evidence/123/9/response.http)",
	} {
		if !strings.Contains(single, want) {
			t.Errorf("单条报告缺少 %q", want)
		}
	}
}

// TestFindingsCSVLocalized 验证 BOM 后表头使用中文且没有韩文；数据值保持原文。
func TestFindingsCSVLocalized(t *testing.T) {
	raw := FindingsCSV([]*db.DBFinding{sampleDBFinding()})
	out := strings.TrimPrefix(string(raw), "\xEF\xBB\xBF")
	header := strings.SplitN(out, "\n", 2)[0]
	if r, ok := containsHangul(header); ok {
		t.Fatalf("CSV 表头仍有韩文残留 %q：%s", string(r), header)
	}
	for _, col := range []string{"ID", "名称", "类型", "严重度", "状态", "所属任务", "发现时间", "总览", "流量证据数量", "流量证据 ID"} {
		if !strings.Contains(header, col) {
			t.Errorf("CSV 表头缺少 %q 列：%s", col, header)
		}
	}
}

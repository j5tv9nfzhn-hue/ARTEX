package notify

import (
	"strings"
	"testing"
)

// 邮件正文、HTML 标签和 email.go 验证错误的中文化回归测试。
// hasHan、hasHangul、assertChinese 由同包 notify_localized_test.go 提供。
// 标题、类型、资产和概述使用 ASCII，确保脚本字符检查只反映标签而非用户数据。
// 严重等级与状态标签均使用中文。
// TestHTMLItemLabelsLocalized 打开单条邮件所有标签分支，验证中文结构且无韩文。
func TestHTMLItemLabelsLocalized(t *testing.T) {
	m := Message{
		HomeURL: "https://example.com/panel",
		Items: []Item{{
			Name:       "sqli-login", // Title() = Name
			VulnClass:  "injection",  // 与 Title() 不同，才能显示类型行。
			Severity:   "high",       // SeverityLabel → 高危（中文）。
			Summary:    "login form is injectable",
			Assets:     []string{"host-a.example.com"},
			DetailURL:  "https://example.com/f/1",
			FromStatus: "pending", // IsStatusChange()=true → 显示状态变更行。
			ToStatus:   "fixed",
		}},
	}
	out := htmlBody(m, 0)
	if hasHangul(out) {
		t.Errorf("邮件正文仍有韩文残留：\n%s", out)
	}
	for _, want := range []string{
		"状态变更", "类型", "资产", "概述", "查看详情", "在平台查看全部",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("邮件正文缺少 %q 标签：\n%s", want, out)
		}
	}
}

// TestHTMLBatchIntroLocalized 检查批量摘要有、无时间窗口的中文标题。
func TestHTMLBatchIntroLocalized(t *testing.T) {
	items := []Item{{Name: "a", Severity: "high"}, {Name: "b", Severity: "low"}}

	withWindow := htmlBatchIntro(Message{Batch: true, WindowMinutes: 30, Items: items})
	assertChinese(t, "htmlBatchIntro（有时间窗口）", withWindow)
	for _, want := range []string{"最近 30 分钟", "新增", "2 个漏洞"} {
		if !strings.Contains(withWindow, want) {
			t.Errorf("时间窗口标题缺少 %q：%q", want, withWindow)
		}
	}

	noWindow := htmlBatchIntro(Message{Batch: true, WindowMinutes: 0, Items: items})
	assertChinese(t, "htmlBatchIntro（无时间窗口）", noWindow)
	if !strings.Contains(noWindow, "新增 2 个漏洞") {
		t.Errorf("无时间窗口的标题应包含 %q：%q", "新增 2 个漏洞", noWindow)
	}
	if strings.Contains(noWindow, "分钟") {
		t.Errorf("未配置时间窗口，标题却包含“分钟”：%q", noWindow)
	}
}

// TestEmailValidateLocalized 检查四类 SMTP 配置错误使用中文，
// 并准确指出问题字段；这些消息直接展示给配置通知渠道的用户。
func TestEmailValidateLocalized(t *testing.T) {
	cases := []struct {
		name   string
		cfg    map[string]any
		substr string
	}{
		{"缺少服务器地址", map[string]any{"port": float64(25), "from": "a@b.c", "to": []any{"d@e.f"}}, "SMTP"},
		{"端口超出范围", map[string]any{"host": "h"}, "端口"},
		{"缺少发件人", map[string]any{"host": "h", "port": float64(25), "to": []any{"d@e.f"}}, "发件人"},
		{"缺少收件人", map[string]any{"host": "h", "port": float64(25), "from": "a@b.c"}, "收件人"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := (emailChannel{}).Validate(tc.cfg)
			if err == nil {
				t.Fatalf("应验证失败：%v", tc.cfg)
			}
			assertChinese(t, "Validate("+tc.name+")", err.Error())
			if !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("错误消息应包含 %q，收到 %q", tc.substr, err.Error())
			}
		})
	}
}

package notify

import (
	"strings"
	"testing"
)

// hasHan 检测 CJK 统一汉字，用于确认中文标签确实包含中文。
func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// hasHangul 检测韩文音节，用于阻止韩文残留回归。
func hasHangul(s string) bool {
	for _, r := range s {
		if r >= 0xAC00 && r <= 0xD7A3 {
			return true
		}
	}
	return false
}

// assertChinese 断言标签包含汉字且不含韩文。
func assertChinese(t *testing.T, where, got string) {
	t.Helper()
	if hasHangul(got) {
		t.Errorf("%s：仍有韩文残留：%q", where, got)
	}
	if !hasHan(got) {
		t.Errorf("%s：未包含中文：%q", where, got)
	}
}

// TestSeverityLabelLocalized 检查所有严重等级枚举的中文展示标签。
// Telegram、邮件、HTML、Markdown、Webhook、飞书、钉钉和企业微信共用这些标签。
func TestSeverityLabelLocalized(t *testing.T) {
	// 严重、高危、中危、低危，与 UI status.severity 一致。
	want := map[string]string{
		"critical": "严重",
		"high":     "高危",
		"medium":   "中危",
		"low":      "低危",
	}
	for sev, label := range want {
		got := SeverityLabel(sev)
		assertChinese(t, "SeverityLabel("+sev+")", got)
		if !strings.Contains(got, label) {
			t.Errorf("SeverityLabel(%q)=%q，应包含 %q", sev, got, label)
		}
	}
	// 未知严重等级原样返回，不编造不存在的标签。
	if got := SeverityLabel("made_up"); got != "made_up" {
		t.Errorf("未知严重等级应保留原值，收到 %q", got)
	}
}

// TestStatusLabelLocalized 检查九种处置状态的中文标签，
// 与 UI status.finding 命名空间一致，避免状态通知与界面不一致。
func TestStatusLabelLocalized(t *testing.T) {
	want := map[string]string{
		"pending":        "待处理",
		"in_progress":    "处理中",
		"confirmed":      "已确认",
		"resolved":       "已处理",
		"fixed":          "已修复",
		"false_positive": "误报",
		"ignored":        "忽略",
		"duplicate":      "重复",
		"risk_accepted":  "接受风险",
	}
	for status, label := range want {
		got := StatusLabel(status)
		assertChinese(t, "StatusLabel("+status+")", got)
		if got != label {
			t.Errorf("StatusLabel(%q)=%q，预期 %q", status, got, label)
		}
	}
	// 未知状态原样返回，不编造标签。
	if got := StatusLabel("weird_status"); got != "weird_status" {
		t.Errorf("未知状态应保留原值，收到 %q", got)
	}
}

// TestItemTitlePlaceholderLocalized 检查名称和类型都为空时使用中文占位标题“未命名漏洞”，
// 不得发送空标题。
func TestItemTitlePlaceholderLocalized(t *testing.T) {
	got := Item{}.Title()
	assertChinese(t, "Item{}.Title()", got)
	// 名称优先，其次类型，最后占位标题，验证回退逻辑。
	if n := (Item{Name: "登录 SQLi"}).Title(); n != "登录 SQLi" {
		t.Errorf("名称优先逻辑失效，收到 %q", n)
	}
	if v := (Item{VulnClass: "XSS"}).Title(); v != "XSS" {
		t.Errorf("类型回退逻辑失效，收到 %q", v)
	}
}

// TestAssetLineLocalized 检查资产列表的 ASCII 分隔符与中文总数表述。
// 保留逗号分隔格式，语言改动不应改变列表结构。
func TestAssetLineLocalized(t *testing.T) {
	// 未超出上限：列出所有资产，使用 ASCII 逗号分隔。
	if got := assetLine([]string{"a.example.com", "b.example.com"}, 3); got != "a.example.com, b.example.com" {
		t.Errorf("分隔符应为 ASCII 逗号，收到 %q", got)
	}
	// 超出上限：列出前 limit 个，随后显示“等 N个”，N 为全部资产数量。
	got := assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if hasHangul(got) {
		t.Errorf("资产列表仍有韩文残留：%q", got)
	}
	if !strings.Contains(got, "等 5个") {
		t.Errorf("总数 5 应表示为“等 5个”，收到 %q", got)
	}
}

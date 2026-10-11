package notify

import (
	"strings"
	"testing"
)

// asciiItems 只生成 ASCII 标题、类型和概述，
// 以便完整输出中的韩文残留能准确定位到标签回归，而不是用户内容。
func asciiItems(n int) []Item {
	items := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, Item{
			FindingID: int64(i + 1),
			Name:      "login-flaw",
			VulnClass: "SQLi",
			Severity:  "high",
			Summary:   "SQL injection via q param",
			Assets:    []string{"a.example.com"},
		})
	}
	return items
}

// TestMarkdownTitleLocalized 检查 markdownTitle 的中文标题。
// 钉钉、企业微信、飞书、HTML、Telegram 和 Webhook 共用该标题，需保持一致。
func TestMarkdownTitleLocalized(t *testing.T) {
	// 批量摘要：“漏洞摘要 · 共 N 个”。
	got := markdownTitle(Message{Batch: true, Items: asciiItems(3)})
	assertChinese(t, "markdownTitle(batch)", got)
	if !strings.Contains(got, "漏洞摘要") || !strings.Contains(got, "共 3 个") {
		t.Errorf("批量标题应为“漏洞摘要 · 共 3 个”，收到 %q", got)
	}
	// 没有条目：“漏洞通知”。
	empty := markdownTitle(Message{})
	assertChinese(t, "markdownTitle(empty)", empty)
	if empty != "漏洞通知" {
		t.Errorf("空消息标题应为“漏洞通知”，收到 %q", empty)
	}
}

// TestMarkdownBatchIntroLocalized 检查时间窗口、数量和截断提示使用中文。
// 数据保持 ASCII，让韩文检查仅检测标题模板。
func TestMarkdownBatchIntroLocalized(t *testing.T) {
	items := asciiItems(3)

	// 有时间窗口：“最近 N 分钟新增 N 个漏洞”。
	win := markdownBatchIntro(Message{Batch: true, WindowMinutes: 30}, items, 3)
	if hasHangul(win) {
		t.Errorf("时间窗口标题仍有韩文残留：%q", win)
	}
	if !strings.Contains(win, "最近 30 分钟") || !strings.Contains(win, "新增 3 个漏洞") {
		t.Errorf("时间窗口标题应为“最近 30 分钟新增 3 个漏洞”，收到 %q", win)
	}

	// 无时间窗口：“新增 N 个漏洞”，不包含分钟数。
	noWin := markdownBatchIntro(Message{Batch: true}, items, 3)
	if hasHangul(noWin) {
		t.Errorf("标题仍有韩文残留：%q", noWin)
	}
	if !strings.Contains(noWin, "新增 3 个漏洞") || strings.Contains(noWin, "分钟") {
		t.Errorf("无时间窗口的标题应为“新增 3 个漏洞”，不应包含“分钟”，收到 %q", noWin)
	}

	// 只发送部分条目时：“本条仅包含前 N 个，其余 N 个将在后续消息中发送”。
	trunc := markdownBatchIntro(Message{Batch: true, WindowMinutes: 30}, items, 5)
	if hasHangul(trunc) {
		t.Errorf("截断说明仍有韩文残留：%q", trunc)
	}
	for _, want := range []string{"前 3 个", "其余 2 个", "后续消息中"} {
		if !strings.Contains(trunc, want) {
			t.Errorf("截断说明应包含 %q，收到 %q", want, trunc)
		}
	}
}

// TestWriteItemLabelsLocalized 检查三个 Markdown 渠道共享的单条正文标签
// （状态变更、类型、资产、概述）与详情链接使用中文。
func TestWriteItemLabelsLocalized(t *testing.T) {
	it := Item{
		Name:       "login-flaw",
		VulnClass:  "SQLi",
		Severity:   "high",
		Summary:    "SQL injection via q param",
		Assets:     []string{"a.example.com"},
		DetailURL:  "https://platform.example/finding/1",
		FromStatus: "pending",
		ToStatus:   "fixed",
	}
	var b strings.Builder
	writeItem(&b, it, "", true)
	got := b.String()

	assertChinese(t, "writeItem(single)", got)
	for _, want := range []string{
		"**状态变更**：待处理 → 已修复",
		"**类型**：SQLi",
		"**资产**：a.example.com",
		"**概述**：SQL injection via q param",
		"[查看详情](https://platform.example/finding/1)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("单条渲染应包含 %q：\n%s", want, got)
		}
	}
}

// TestMarkdownBodyFooterLocalized 检查批量正文末尾的平台链接为中文；
// 仅有 HomeURL 时追加。
func TestMarkdownBodyFooterLocalized(t *testing.T) {
	m := Message{Batch: true, HomeURL: "https://platform.example", Items: asciiItems(2)}
	body, kept := markdownBody(m, 0)
	if kept != 2 {
		t.Fatalf("不设上限 (0) 时应包含全部 2 条，收到 %d", kept)
	}
	if !strings.Contains(body, "[在平台查看全部](https://platform.example)") {
		t.Errorf("正文末尾应有“在平台查看全部”链接：\n%s", body)
	}
}

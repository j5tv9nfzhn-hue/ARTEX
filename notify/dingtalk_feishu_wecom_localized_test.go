package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// asciiStatusChangeItem 构造纯 ASCII 状态变更数据。
// 这样完整输出中的韩文只可能来自标签，能准确检测标签的语言回归。
func asciiStatusChangeItem() Item {
	return Item{
		FindingID:  7,
		Name:       "login-flaw",
		VulnClass:  "SQLi",
		Severity:   "high",
		Summary:    "SQL injection via q param",
		Assets:     []string{"a.example.com"},
		DetailURL:  "https://platform.example/finding/7",
		FromStatus: "pending",
		ToStatus:   "fixed",
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化卡片失败：%v", err)
	}
	return string(b)
}

// TestFeishuItemLinesLocalized 检查飞书单条卡片的状态变更、类型、资产、概述标签。
// 它们应与 markdown.go 的中文标签一致。feishu.go 使用自身副本，
// 因此需独立回归断言，不能只依赖三个 Markdown 渠道的共享函数测试。
func TestFeishuItemLinesLocalized(t *testing.T) {
	got := feishuItemLines(asciiStatusChangeItem())
	assertChinese(t, "feishuItemLines", got)
	for _, want := range []string{
		"**状态变更**：待处理 → 已修复",
		"**类型**：SQLi",
		"**资产**：a.example.com",
		"**概述**：SQL injection via q param",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("飞书单条正文应包含 %q：\n%s", want, got)
		}
	}
}

// TestFeishuCardButtonsLocalized 检查“查看详情”“在平台查看全部”按钮。
// 使用 ASCII 数据验证整张卡片没有韩文残留。
func TestFeishuCardButtonsLocalized(t *testing.T) {
	// 单条消息：有 DetailURL 时显示“查看详情”。
	single, _ := feishuCard(Message{Items: []Item{asciiStatusChangeItem()}})
	singleJSON := mustJSON(t, single)
	if hasHangul(singleJSON) {
		t.Errorf("飞书单条卡片仍有韩文残留：\n%s", singleJSON)
	}
	if !strings.Contains(singleJSON, "查看详情") {
		t.Errorf("飞书单条卡片应有“查看详情”按钮：\n%s", singleJSON)
	}

	// 批量消息：有 HomeURL 时显示“在平台查看全部”。
	batch, _ := feishuCard(Message{Batch: true, HomeURL: "https://platform.example", Items: asciiItems(2)})
	batchJSON := mustJSON(t, batch)
	if hasHangul(batchJSON) {
		t.Errorf("飞书批量卡片仍有韩文残留：\n%s", batchJSON)
	}
	if !strings.Contains(batchJSON, "在平台查看全部") {
		t.Errorf("飞书批量卡片应有“在平台查看全部”按钮：\n%s", batchJSON)
	}
}

// TestDingTalkActionCardButtonLocalized 通过 Send 和模拟接收端检查
// 钉钉 ActionCard 的 singleTitle 为“查看详情”。
func TestDingTalkActionCardButtonLocalized(t *testing.T) {
	var singleTitle string
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(_ *testing.T, body map[string]any, _ *http.Request) {
		card, _ := body["actionCard"].(map[string]any)
		singleTitle, _ = card["singleTitle"].(string)
	})
	m := Message{Items: []Item{asciiStatusChangeItem()}}
	if _, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, m); err != nil {
		t.Fatalf("投递失败：%v", err)
	}
	if singleTitle != "查看详情" {
		t.Errorf("钉钉 ActionCard singleTitle 应为“查看详情”，收到 %q", singleTitle)
	}
}

// TestChinaPlatformValidateLocalized 检查钉钉、飞书、企业微信的配置错误使用中文，
// 同时保留 Webhook 标识；这些错误由通知保存、测试 API 直接显示。
func TestChinaPlatformValidateLocalized(t *testing.T) {
	for _, kind := range []string{KindDingTalk, KindFeishu, KindWeCom} {
		ch, ok := Get(kind)
		if !ok {
			t.Fatalf("%s 渠道未注册", kind)
		}

		// 空配置：“未设置 Webhook 地址”。
		missing := ch.Validate(map[string]any{})
		if missing == nil {
			t.Fatalf("%s：空配置应验证失败", kind)
		}
		assertChinese(t, kind+" missing", missing.Error())
		if !strings.Contains(missing.Error(), "Webhook") || !strings.Contains(missing.Error(), "未设置") {
			t.Errorf("%s：缺失错误应为“未设置 Webhook 地址”，收到 %q", kind, missing)
		}

		// 无效 ftp 地址：“Webhook 地址无效：…”。
		bad := ch.Validate(map[string]any{"webhook": "ftp://x"})
		if bad == nil {
			t.Fatalf("%s：ftp 地址应验证失败", kind)
		}
		if hasHangul(bad.Error()) {
			t.Errorf("%s：无效地址错误仍有韩文残留：%q", kind, bad)
		}
		if !strings.Contains(bad.Error(), "Webhook 地址无效") {
			t.Errorf("%s：无效地址错误应以“Webhook 地址无效”开头，收到 %q", kind, bad)
		}
	}
}

// TestChinaPlatformSendErrorsLocalized 检查写入 last_error 的发送失败原因。
// DingTalk、Feishu、WeCom 与 UI 渠道标签一致。模拟服务器 errmsg 为 ASCII，
// 因此只检查包装消息是否有韩文残留，不修改平台错误原文。
func TestChinaPlatformSendErrorsLocalized(t *testing.T) {
	// 业务错误路径：HTTP 200，但正文 errcode 或 code 不为 0。
	bizCases := []struct {
		kind     string
		resp     string
		wantSubs []string
	}{
		{KindDingTalk, `{"errcode":310000,"errmsg":"keyword not matched"}`, []string{"DingTalk 返回错误", "310000"}},
		{KindFeishu, `{"code":19021,"msg":"sign error"}`, []string{"Feishu 返回错误", "19021"}},
		{KindWeCom, `{"errcode":45009,"errmsg":"freq out of limit"}`, []string{"WeCom 请求被限流", "45009"}},
		{KindWeCom, `{"errcode":93000,"errmsg":"invalid webhook"}`, []string{"WeCom 返回错误", "93000"}},
	}
	for _, tc := range bizCases {
		srv := capturePost(t, tc.resp, nil)
		ch, ok := Get(tc.kind)
		if !ok {
			t.Fatalf("%s 渠道未注册", tc.kind)
		}
		_, err := ch.Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg())
		if err == nil {
			t.Fatalf("%s：%s 响应应返回错误", tc.kind, tc.resp)
		}
		if hasHangul(err.Error()) {
			t.Errorf("%s：发送错误仍有韩文残留：%q", tc.kind, err)
		}
		for _, sub := range tc.wantSubs {
			if !strings.Contains(err.Error(), sub) {
				t.Errorf("%s：发送错误应包含 %q，收到 %q", tc.kind, sub, err)
			}
		}
	}

	// 响应不是 JSON 时走“无法解析 <平台> 响应”错误路径。
	parseCases := []struct {
		kind     string
		platform string
	}{
		{KindDingTalk, "DingTalk"},
		{KindFeishu, "Feishu"},
		{KindWeCom, "WeCom"},
	}
	for _, pc := range parseCases {
		srv := capturePost(t, `not-json`, nil)
		ch, ok := Get(pc.kind)
		if !ok {
			t.Fatalf("%s 渠道未注册", pc.kind)
		}
		_, err := ch.Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg())
		if err == nil {
			t.Fatalf("%s：非 JSON 响应应返回错误", pc.kind)
		}
		want := pc.platform + "无法解析 "
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s：解析错误应包含 %q，收到 %q", pc.kind, want, err)
		}
	}
}

package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Telegram、Webhook 和共享 HTTP 层的中文化回归测试。
// 检查消息标签、批量标题、配置验证、发送错误及五个渠道共用的 validateHTTPURL。
// 复用同包 notify_localized_test.go 的 hasHan、hasHangul、assertChinese。
// 用户数据为 ASCII，严重等级、状态标签为中文，完整输出不得残留韩文。
// TestTelegramItemLabelsLocalized 打开所有单条标签分支进行验证。
func TestTelegramItemLabelsLocalized(t *testing.T) {
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
	out, kept := telegramHTML(m)
	if kept != 1 {
		t.Fatalf("单条消息应报告 1，收到 %d", kept)
	}
	if hasHangul(out) {
		t.Errorf("Telegram 正文仍有韩文残留：\n%s", out)
	}
	for _, want := range []string{"状态变更", "类型", "资产", "总览", "查看详情"} {
		if !strings.Contains(out, want) {
			t.Errorf("Telegram 正文缺少 %q 标签：\n%s", want, out)
		}
	}
}

// TestTelegramBatchTitleLocalized 检查批量标题的截断与时间窗口分支。
// 必须与 markdownTitle 的中文表述一致。
func TestTelegramBatchTitleLocalized(t *testing.T) {
	items := []Item{{Name: "a", Severity: "high"}, {Name: "b", Severity: "low"}}

	// 超出上限且有时间窗口：摘要、总数、前 N 个、其余、后续消息、最近 N 分钟。
	over := telegramBatchTitle(Message{WindowMinutes: 30}, items, 5)
	assertChinese(t, "telegramBatchTitle（超出上限）", over)
	for _, want := range []string{"漏洞摘要 · 共 5 个", "前 2 个", "其余 3 个", "后续消息", "最近 30 分钟"} {
		if !strings.Contains(over, want) {
			t.Errorf("截断标题缺少 %q：%q", want, over)
		}
	}

	// 未超出上限且无时间窗口：不应出现后续消息提示。
	full := telegramBatchTitle(Message{}, items, 2)
	assertChinese(t, "telegramBatchTitle（全部）", full)
	if !strings.Contains(full, "漏洞摘要 · 共 2 个") {
		t.Errorf("全部标题应包含“漏洞摘要 · 共 2 个”：%q", full)
	}
	if strings.Contains(full, "后续消息") {
		t.Errorf("未超出上限，却出现“后续消息”提示：%q", full)
	}
}

// TestTelegramValidateLocalized 检查缺失字段的中文错误，
// 准确指出字段，保留技术标识 Bot Token 和 Chat ID。
func TestTelegramValidateLocalized(t *testing.T) {
	if err := (telegramChannel{}).Validate(map[string]any{}); err == nil {
		t.Fatal("缺少 bot_token 应验证失败")
	} else {
		assertChinese(t, "telegram Validate(bot_token)", err.Error())
		if !strings.Contains(err.Error(), "Bot Token") {
			t.Errorf("应提示缺少 Bot Token：%q", err.Error())
		}
	}
	if err := (telegramChannel{}).Validate(map[string]any{"bot_token": "t"}); err == nil {
		t.Fatal("缺少 chat_id 应验证失败")
	} else {
		assertChinese(t, "telegram Validate(chat_id)", err.Error())
		if !strings.Contains(err.Error(), "Chat ID") {
			t.Errorf("应提示缺少 Chat ID：%q", err.Error())
		}
	}
}

// TestWebhookValidateLocalized 检查三类通用 Webhook 配置错误，
// 使用中文并说明具体原因。
func TestWebhookValidateLocalized(t *testing.T) {
	if err := (webhookChannel{}).Validate(map[string]any{}); err == nil {
		t.Fatal("缺少 url 应验证失败")
	} else {
		assertChinese(t, "webhook Validate(url)", err.Error())
		if !strings.Contains(err.Error(), "目标 URL") {
			t.Errorf("应提示缺少目标 URL：%q", err.Error())
		}
	}
	// 不支持的方法：使用中文并展示允许的方法列表。
	if err := (webhookChannel{}).Validate(map[string]any{"url": "https://example.com/hook", "method": "DELETE"}); err == nil {
		t.Fatal("DELETE 应验证失败")
	} else {
		assertChinese(t, "webhook Validate(method)", err.Error())
		if !strings.Contains(err.Error(), "GET/POST/PUT/PATCH") {
			t.Errorf("应提示允许的方法列表：%q", err.Error())
		}
	}
	// 模板语法错误：中文包装，底层 Go 模板错误保留原文。
	if err := (webhookChannel{}).Validate(map[string]any{"url": "https://example.com/hook", "body_template": "{{"}); err == nil {
		t.Fatal("损坏的模板应验证失败")
	} else {
		assertChinese(t, "webhook Validate(template)", err.Error())
		if !strings.Contains(err.Error(), "模板") {
			t.Errorf("应提示模板错误：%q", err.Error())
		}
	}
}

// TestValidateHTTPURLLocalized 检查 Telegram、Webhook、钉钉、飞书、企业微信
// 共享的 URL 验证错误为中文，避免与上层 API 地址错误包装语言不一致。
func TestValidateHTTPURLLocalized(t *testing.T) {
	// 不支持的协议。
	if err := validateHTTPURL("ftp://example.com/x"); err == nil {
		t.Fatal("应拒绝 ftp 协议")
	} else {
		assertChinese(t, "validateHTTPURL(scheme)", err.Error())
		if !strings.Contains(err.Error(), "http") {
			t.Errorf("应提示支持的协议 (http/https)：%q", err.Error())
		}
	}
	// 缺少主机名。
	if err := validateHTTPURL("http://"); err == nil {
		t.Fatal("应拒绝缺少主机名的地址")
	} else {
		assertChinese(t, "validateHTTPURL(host)", err.Error())
	}
}

// TestHTTPLocalTargetBlockedLocalized 检查本地、链路本地地址的中文拦截错误，
// 保留允许例外请求的环境变量名称。
func TestHTTPLocalTargetBlockedLocalized(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "") // 显式关闭例外选项，确保执行拦截路径。
	err := blockInternalDial("tcp", "127.0.0.1:25", nil)
	if err == nil {
		t.Fatal("默认应拦截本地地址")
	}
	assertChinese(t, "blockInternalDial", err.Error())
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("拦截错误应提示例外配置方法 (%s)：%q", AllowLocalTargetsEnv, err.Error())
	}
}

// TestHTTPRedactHelpersLocalized 检查地址脱敏占位符和发送错误中的“未知错误”分支。
func TestHTTPRedactHelpersLocalized(t *testing.T) {
	if got := redactRequestTarget(""); !strings.Contains(got, "无法解析") {
		t.Errorf("无法解析的地址应使用中文占位符，收到 %q", got)
	}
	// *url.Error 的 Err 为 nil 分支。
	got := redactTransportError(&url.Error{Op: "Get", URL: "http://api.example", Err: nil})
	if hasHangul(got) {
		t.Errorf("脱敏后的发送错误仍有韩文残留：%q", got)
	}
	if !strings.Contains(got, "未知错误") {
		t.Errorf("Err 为 nil 时应显示“未知错误”，收到 %q", got)
	}
}

// TestHTTPStatusErrorsLocalized 通过真实 doJSON 往返验证拒绝、服务器错误、限流分类。
// 127.0.0.1 httptest 地址默认被阻止，仅在本测试中显式允许。
func TestHTTPStatusErrorsLocalized(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "1")
	cases := []struct {
		code int
		want string
	}{
		{http.StatusForbidden, "拒绝请求"},
		{http.StatusInternalServerError, "发生错误"},
		{http.StatusTooManyRequests, "限流或"},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte("body"))
		}))
		_, err := doJSON(context.Background(), http.MethodPost, srv.URL, nil, map[string]any{"a": 1})
		srv.Close()
		if err == nil {
			t.Fatalf("HTTP %d 应返回错误", tc.code)
		}
		assertChinese(t, "doJSON(HTTP status)", err.Error())
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("HTTP %d 错误应包含 %q，收到 %q", tc.code, tc.want, err.Error())
		}
	}
}

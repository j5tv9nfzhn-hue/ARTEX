package server

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 通知设置 API 与测试消息中文化回归测试，复用 assertChineseError。
// TestNotifyErrorConstantsLocalized 检查十类错误和三类测试消息常量。
func TestNotifyErrorConstantsLocalized(t *testing.T) {
	cases := map[string]string{
		"notifyErrBadJSON":         notifyErrBadJSON,
		"notifyErrKindInvalidFmt":  fmt.Sprintf(notifyErrKindInvalidFmt, "email / webhook"),
		"notifyErrNameMissing":     notifyErrNameMissing,
		"notifyErrNameEmpty":       notifyErrNameEmpty,
		"notifyErrModeInvalid":     notifyErrModeInvalid,
		"notifyErrRateNegative":    notifyErrRateNegative,
		"notifyErrChannelID":       notifyErrChannelID,
		"notifyErrKindUnregFmt":    fmt.Sprintf(notifyErrKindUnregFmt, "bogus"),
		"notifyErrDeliveryID":      notifyErrDeliveryID,
		"notifyErrChannelNotFound": notifyErrChannelNotFound,
		"notifyTestName":           notifyTestName,
		"notifyTestClass":          notifyTestClass,
		"notifyTestSummary":        notifyTestSummary,
	}
	for label, msg := range cases {
		assertChineseError(t, label, msg)
	}
}

// TestNotifyChannelLookupErrLocalized 真实验证渠道不存在的 404 中文正文。
// notifyChannelLookupErr 只依赖 w、err，无需 Server、数据库，直接传入错误标识即可。
func TestNotifyChannelLookupErrLocalized(t *testing.T) {
	rec := httptest.NewRecorder()
	notifyChannelLookupErr(rec, db.ErrNotificationChannelNotFound)
	if rec.Code != 404 {
		t.Fatalf("状态码 = %d，预期 = 404（正文 %q）", rec.Code, rec.Body.String())
	}
	got := decodeErrorField(t, rec.Body.Bytes())
	if got != notifyErrChannelNotFound {
		t.Fatalf("响应文案 = %q，预期 = %q", got, notifyErrChannelNotFound)
	}
	assertChineseError(t, "channel_not_found", got)
}

// TestNotifyTestMessageLocalized 用纯函数构造渠道测试消息，
// 检查标题、分类、概述为中文，无需数据库或网络。
func TestNotifyTestMessageLocalized(t *testing.T) {
	const base = "https://artex.example.test"
	msg := notifyTestMessage(base)
	if len(msg.Items) != 1 {
		t.Fatalf("测试消息项数 = %d，预期 = 1", len(msg.Items))
	}
	item := msg.Items[0]
	assertChineseError(t, "test.name", item.Name)
	assertChineseError(t, "test.class", item.VulnClass)
	assertChineseError(t, "test.summary", item.Summary)
	if msg.HomeURL != base || item.DetailURL != base {
		t.Fatalf("未保留链接：HomeURL=%q DetailURL=%q（预期 %q）", msg.HomeURL, item.DetailURL, base)
	}
}

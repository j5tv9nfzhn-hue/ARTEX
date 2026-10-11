package notify

import (
	"errors"
	"strings"
	"testing"
)

// 通知配置保存、更新时的 mask.go 与 filter.go 错误通过 writeErr(400) 展示，
// 必须使用中文且不含韩文。channel.go 没有用户可见文案，不在本测试范围。
// hasHan、hasHangul、assertChinese 由 notify_localized_test.go 提供。
// TestFilterValidateLocalized 检查错误 min_severity 值的保存、更新错误。
func TestFilterValidateLocalized(t *testing.T) {
	err := Filter{MinSeverity: "hgih"}.Validate()
	if err == nil {
		t.Fatal("最低严重等级无效，但未返回错误")
	}
	assertChinese(t, "Filter.Validate", err.Error())
	// low、medium、high、critical 是配置枚举，必须原样保留。
	for _, tok := range []string{"low", "medium", "high", "critical"} {
		if !strings.Contains(err.Error(), tok) {
			t.Errorf("严重等级标识 %q 丢失：%q", tok, err.Error())
		}
	}
	// 有效值和空值均应通过验证。
	if err := (Filter{MinSeverity: "high"}).Validate(); err != nil {
		t.Errorf("high 有效，但返回错误：%v", err)
	}
	if err := (Filter{}).Validate(); err != nil {
		t.Errorf("空最低严重等级有效，但返回错误：%v", err)
	}
}

// TestPrepareConfigUpdateUnknownKindLocalized 检查使用未注册渠道类型更新配置时的 400 错误。
func TestPrepareConfigUpdateUnknownKindLocalized(t *testing.T) {
	_, err := PrepareConfigUpdate("definitely-not-a-channel", map[string]any{}, map[string]any{})
	if err == nil {
		t.Fatal("渠道类型未注册，但未返回错误")
	}
	assertChinese(t, "PrepareConfigUpdate unknown kind", err.Error())
}

// TestDestinationChangedErrorLocalized 真实触发 ErrDestinationChangedWithoutCredentials：
// PATCH 只修改 Webhook 目标 url，未重新提交 headers 凭据，服务器应返回 400。
func TestDestinationChangedErrorLocalized(t *testing.T) {
	_, err := PrepareConfigUpdate("webhook",
		map[string]any{
			"url":     "https://old.example.com/hook",
			"headers": map[string]any{"Authorization": "Bearer real-token"},
		},
		map[string]any{"url": "https://attacker.example.com/hook"},
	)
	if err == nil {
		t.Fatal("目标地址已更改且未重新提交凭据，但未返回错误")
	}
	var de *ErrDestinationChangedWithoutCredentials
	if !errors.As(err, &de) {
		t.Fatalf("错误类型与预期不同：%T", err)
	}
	assertChinese(t, "ErrDestinationChangedWithoutCredentials", err.Error())
	// 目标键和缺少的凭据键都是配置字段名，必须原样出现在消息中。
	if !strings.Contains(err.Error(), "url") || !strings.Contains(err.Error(), "headers") {
		t.Errorf("缺少字段键名：%q", err.Error())
	}
}

// TestRejectMaskedInContainersLocalized 在 PATCH 的 headers 对象内部放入掩码标记，
// 真实验证结构化凭据拒绝路径。
func TestRejectMaskedInContainersLocalized(t *testing.T) {
	_, err := PrepareConfigUpdate("webhook",
		map[string]any{},
		map[string]any{"headers": map[string]any{"Authorization": MaskedPrefix + ":…abc123"}},
	)
	if err == nil {
		t.Fatal("结构体内部插入了掩码标记，但未返回错误")
	}
	assertChinese(t, "rejectMaskedInContainers", err.Error())
	// 消息保留 __masked__ 字面量，便于操作员识别问题值。
	if !strings.Contains(err.Error(), MaskedPrefix) {
		t.Errorf("消息缺少掩码标记：%q", err.Error())
	}
}

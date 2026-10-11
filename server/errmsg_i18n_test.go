package server

import (
	"testing"
	"unicode"

	"github.com/Autumn-27/artex/config"
)

// 检查中文源消息、旧 ko 别名及英语/西班牙语词库，不改变底层错误原文。
func TestTrMsgSwitchesLanguage(t *testing.T) {
	previous := config.CurrentLanguage
	t.Cleanup(func() { config.CurrentLanguage = previous })
	hasScript := func(s string, script *unicode.RangeTable) bool {
		for _, r := range s {
			if unicode.Is(script, r) {
				return true
			}
		}
		return false
	}

	const source = errAssetIDRequired
	if !hasScript(source, unicode.Han) || hasScript(source, unicode.Hangul) {
		t.Fatalf("源消息必须为中文且无韩文：%q", source)
	}
	config.CurrentLanguage = nil
	if got := trMsg(source); got != source {
		t.Fatalf("未接入运行时配置时必须保留中文源消息：%q", got)
	}
	for _, language := range []string{"zh", "ko", "fr", ""} {
		config.CurrentLanguage = func() string { return language }
		if got := trMsg(source); got != source {
			t.Fatalf("%s：中文、旧别名与未知语言回退应返回源消息：%q", language, got)
		}
	}
	for _, language := range []string{"en", "es"} {
		config.CurrentLanguage = func() string { return language }
		got := trMsg(source)
		if got == "" || got == source || hasScript(got, unicode.Han) || hasScript(got, unicode.Hangul) {
			t.Fatalf("%s：应使用对应词库，而不是回退到中文或韩文：%q", language, got)
		}
	}
	config.CurrentLanguage = func() string { return "en" }
	const diagnostic = "some-lower-layer-error: connection refused"
	if got := trMsg(diagnostic); got != diagnostic {
		t.Fatalf("底层非词库错误应保持原文：%q", got)
	}
}

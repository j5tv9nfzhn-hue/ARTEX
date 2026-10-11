package server

import (
	"testing"
	"unicode"

	"github.com/Autumn-27/artex/config"
)

// 检查中文源消息在各种运行时语言取值下保持不变，不改变底层错误原文。
func TestTrMsgFallsBackToChinese(t *testing.T) {
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
	// 本仓库只保留简体中文：任何语言取值都回退到中文源消息。
	for _, language := range []string{"zh", "ko", "en", "es", "fr", ""} {
		config.CurrentLanguage = func() string { return language }
		if got := trMsg(source); got != source {
			t.Fatalf("%s：应回退到中文源消息，实际 %q", language, got)
		}
	}
	config.CurrentLanguage = func() string { return "en" }
	const diagnostic = "some-lower-layer-error: connection refused"
	if got := trMsg(diagnostic); got != diagnostic {
		t.Fatalf("底层非词库错误应保持原文：%q", got)
	}
}

package db

import (
	"testing"
	"unicode"
)

// assertRetestReasonChinese 断言复测原因包含汉字且不含韩文。
// FinishFindingRetest 和 RecoverFindingRetests 将常量嵌入 SQL，
// 固定常量即可同时保护复测面板的展示文案。
func assertRetestReasonChinese(t *testing.T, name, s string) {
	t.Helper()
	if s == "" {
		t.Fatalf("%s：字符串为空", name)
	}
	hasHan := false
	for _, r := range s {
		if unicode.Is(unicode.Hangul, r) {
			t.Fatalf("%s：仍有韩文残留：%q", name, s)
		}
		if unicode.Is(unicode.Han, r) {
			hasHan = true
		}
	}
	if !hasHan {
		t.Fatalf("%s：未包含中文：%q", name, s)
	}
}

// TestFindingRetestReasonsLocalized 检查 finding_retests.error 中的两类终止原因。
// 它们与 server/conversations.go 的 convRetest* 共用同一列和面板，必须统一为中文。
func TestFindingRetestReasonsLocalized(t *testing.T) {
	assertRetestReasonChinese(t, "retestNoConclusionReason", retestNoConclusionReason)
	assertRetestReasonChinese(t, "retestServiceRestartReason", retestServiceRestartReason)
}

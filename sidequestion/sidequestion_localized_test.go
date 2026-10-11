package sidequestion

import (
	"errors"
	"fmt"
	"testing"
	"unicode"
)

// assertChinese fails if msg is empty, carries a CJK Han ideograph (= leftover
// untranslated Chinese), or has no Hangul at all. ASCII field names and the
// "1–4000"-style ranges are fine; only Han marks an unlocalized string.
func assertChinese(t *testing.T, label, msg string) {
	t.Helper()
	if msg == "" {
		t.Fatalf("%s：消息为空", label)
	}
	han := false
	for _, r := range msg {
		if unicode.Is(unicode.Hangul, r) {
			t.Fatalf("%s：仍有韩文残留：%q", label, msg)
		}
		if unicode.Is(unicode.Han, r) {
			han = true
		}
	}
	if !han {
		t.Fatalf("%s：未包含中文：%q", label, msg)
	}
}

// TestSideQuestionOutputsLocalized 检查旁问包的用户错误和固定答案为中文。
// 这些消息经 /api/.../side-questions 显示于聊天旁问面板，不得残留韩文。
func TestSideQuestionOutputsLocalized(t *testing.T) {
	assertChinese(t, "ErrContextBudget", ErrContextBudget.Error())
	assertChinese(t, "errSideModelInterrupted", errSideModelInterrupted.Error())
	assertChinese(t, "errSideNoAnswer", errSideNoAnswer.Error())
	assertChinese(t, "msgSideToolUnavailable", msgSideToolUnavailable)
	assertChinese(t, "errSideSummaryCallCap", errSideSummaryCallCap.Error())
	assertChinese(t, "sideSummaryFailedPrefix", sideSummaryFailedPrefix)
	assertChinese(t, "errSideSummaryIncomplete", errSideSummaryIncomplete.Error())
	assertChinese(t, "errSideSummaryOverBudget", errSideSummaryOverBudget.Error())
	assertChinese(t, "errSideHistoryCursor", errSideHistoryCursor.Error())
	assertChinese(t, "errSideCompactionStalled", errSideCompactionStalled.Error())
}

// TestSideQuestionBudgetSentinelPreserved: localizing the message must not break
// callers that branch on the ErrContextBudget sentinel with errors.Is (see
// context_test.go, which relies on it after the recovery path gives up).
func TestSideQuestionBudgetSentinelPreserved(t *testing.T) {
	if !errors.Is(fmt.Errorf("prepare: %w", ErrContextBudget), ErrContextBudget) {
		t.Fatal("ErrContextBudget 标识的 errors.Is 判断失效")
	}
}

// TestSideQuestionBrainPreserved 固定回答与摘要提示中的中文模型语义，
// 避免语言整理改变已使用的指令、工具约束与摘要格式。
func TestSideQuestionBrainPreserved(t *testing.T) {
	for _, c := range []struct{ label, text string }{
		{"instruction", instruction},
		{"summaryInstruction", summaryInstruction},
	} {
		han := false
		for _, r := range c.text {
			if unicode.Is(unicode.Han, r) {
				han = true
				break
			}
		}
		if !han {
			t.Errorf("%s：模型提示词不再为中文（存在行为偏移风险）", c.label)
		}
	}
}

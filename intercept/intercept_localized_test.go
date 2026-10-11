package intercept

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
)

// hasHangul reports whether s contains any Hangul syllable.
func hasHangul(s string) bool {
	for _, r := range s {
		if r >= 0xAC00 && r <= 0xD7A3 {
			return true
		}
	}
	return false
}

// hasHan reports whether s contains any CJK Han ideograph.
func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// assertChinese fails if s lacks Hangul or still carries Han ideographs. Reverting
// any localized message back to Chinese trips hasHan, so the test is not vacuous.
func assertChinese(t *testing.T, label, s string) {
	t.Helper()
	if !hasHan(s) {
		t.Errorf("%s：未包含中文：%q", label, s)
	}
	if hasHangul(s) {
		t.Errorf("%s：仍有韩文残留：%q", label, s)
	}
}

// TestInterceptMessagesLocalized pins the user-facing judge/approval messages to
// Simplified Chinese. These surface in the approval record reason, the activity stream, and
// the 409 response for an already-decided request.
func TestInterceptMessagesLocalized(t *testing.T) {
	for _, c := range []struct{ name, s string }{
		{"msgReviewContextIncomplete", msgReviewContextIncomplete},
		{"msgModelApprovalFailed", msgModelApprovalFailed},
		{"msgModelOutputUnparsable", msgModelOutputUnparsable},
		{"reasonWorkCanceled", reasonWorkCanceled},
		{"reasonWorkCanceledPreExec", reasonWorkCanceledPreExec},
		{"reasonApprovalTimeout", reasonApprovalTimeout},
		{"reasonManualDeny", reasonManualDeny},
		{"reasonManualAllow", reasonManualAllow},
		{"ErrAlreadyDecided", ErrAlreadyDecided.Error()},
	} {
		assertChinese(t, c.name, c.s)
	}
}

// TestJudgeActionLabelLocalized 检查允许、阻止、请求确认三类中文标签；
// 未知 action 仍原样返回，不伪造标签。
func TestJudgeActionLabelLocalized(t *testing.T) {
	for _, action := range []string{"allow", "deny", "ask"} {
		assertChinese(t, "judgeActionLabel("+action+")", judgeActionLabel(action))
	}
	if got := judgeActionLabel("weird"); got != "weird" {
		t.Errorf("judgeActionLabel(weird) = %q, want passthrough", got)
	}
}

// TestDefaultMessageLocalized checks the rule-derived deny/ask messages are Simplified Chinese
// and still embed the rule name, while allow stays empty.
func TestDefaultMessageLocalized(t *testing.T) {
	for _, action := range []string{"deny", "ask"} {
		msg := defaultMessage(action, "R1")
		assertChinese(t, "defaultMessage("+action+")", msg)
		if !strings.Contains(msg, "R1") {
			t.Errorf("defaultMessage(%s) dropped rule name: %q", action, msg)
		}
	}
	if got := defaultMessage("allow", "R1"); got != "" {
		t.Errorf("defaultMessage(allow) = %q, want empty", got)
	}
}

// TestToolApprovalSummaryLocalized 检查审批摘要中文化且能被 transcript.tsx 解析。
// pending_id 使用 /\(#(\d+)\)/，工具名使用 /工具\s+(\S+)\s+审批/。
func TestToolApprovalSummaryLocalized(t *testing.T) {
	s := fmt.Sprintf(msgToolApprovalRequestFmt, "Bash", 42)
	assertChinese(t, "msgToolApprovalRequestFmt", s)
	if !strings.Contains(s, "(#42)") {
		t.Errorf("summary lost the (#N) marker (transcript.tsx pending_id regex): %q", s)
	}
	if !strings.Contains(s, "工具 Bash 审批") {
		t.Errorf("摘要不符合“工具 X 审批”格式（transcript.tsx 工具名正则）：%q", s)
	}
}

package llmpool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode"

	"github.com/Autumn-27/norma/llm"
)

// hasHangul reports whether s contains any Hangul character.
func hasHangul(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

// hasHanzi reports whether s contains any CJK (Han) character.
func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// assertSurfacedExhaustion checks the error an exhausted chain hands back: it must
// still wrap the ErrExhausted sentinel (callers rely on errors.Is), read as a
// Simplified Chinese message (Han present, no Hangul; provider diagnostics preserved), and lead with the
// localized sentinel text. failProv's body is ASCII ("anthropic: status 402:
// nope"), so the whole surfaced string must be Hangul-free.
func assertSurfacedExhaustion(t *testing.T, label string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: exhausted chain returned nil error", label)
	}
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("%s: surfaced error no longer wraps ErrExhausted: %v", label, err)
	}
	msg := err.Error()
	if hasHangul(msg) {
		t.Errorf("%s: surfaced error still carries Hangul: %q", label, msg)
	}
	if !strings.Contains(msg, "anthropic: status 402: nope") {
		t.Errorf("%s: provider error text was not preserved verbatim: %q", label, msg)
	}
	if !strings.HasPrefix(msg, ErrExhausted.Error()) {
		t.Errorf("%s: surfaced error does not lead with the localized sentinel: %q", label, msg)
	}
}

// The chain-exhaustion error is user-facing: a worker whose whole LLM chain fails
// records it through agent/capture.go as a "result" activity shown in the run
// transcript. So its text must be Simplified Chinese, while its identity (errors.Is) must be
// preserved for callers that branch on the sentinel.
func TestExhaustedErrorLocalized(t *testing.T) {
	assertChineseErrText(t, "ErrExhausted", ErrExhausted.Error())

	// Drive both surfaced paths so a future edit to either wrap site is caught.
	streamChain := New([]*Member{
		member(1, "a", 10, failProv("a", 402)),
		member(2, "b", 5, failProv("b", 402)),
	}, NewRegistry(nil, nil))
	_, serr := drain(streamChain.Stream(context.Background(), llm.CompletionRequest{}))
	assertSurfacedExhaustion(t, "Stream", serr)

	completeChain := New([]*Member{
		member(1, "a", 10, failProv("a", 402)),
		member(2, "b", 5, failProv("b", 402)),
	}, NewRegistry(nil, nil))
	_, _, _, cerr := completeChain.Complete(context.Background(), llm.CompletionRequest{})
	assertSurfacedExhaustion(t, "Complete", cerr)
}

// assertChineseErrText fails unless s contains Han and no Hangul character.
func assertChineseErrText(t *testing.T, label, s string) {
	t.Helper()
	if !hasHan(s) {
		t.Errorf("%s：未包含中文：%q", label, s)
	}
	if hasHangul(s) {
		t.Errorf("%s：仍有韩文残留：%q", label, s)
	}
}

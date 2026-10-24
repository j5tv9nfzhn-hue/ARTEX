package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

func TestLoopBreakerGatesBeforeFourthSameContextCall(t *testing.T) {
	lb := newLoopBreaker()
	in := json.RawMessage(`{"url":"https://example.invalid/api/123?q=one","method":"GET"}`)
	for i := 0; i < 3; i++ {
		release, blocked := lb.before(context.Background(), "http_request", in)
		if blocked != nil {
			t.Fatal("premature block")
		}
		result := actool.Text(`{"status":422,"body":"rejected","body_truncated":false}`)
		result.Extra = []llm.Message{llm.UserText("preserve guidance")}
		result.Content = append(result.Content, llm.ContentBlock{Type: llm.BlockThinking, Thinking: "preserve non-text"})
		result = lb.inspect(context.Background(), "http_request", in, result, nil)
		release()
		if len(result.Extra) != 1 || result.Content[1].Type != llm.BlockThinking {
			t.Fatal("result structure lost")
		}
	}
	release, blocked := lb.before(context.Background(), "http_request", json.RawMessage(`{"url":"https://example.invalid/api/123?q=one"}`))
	release()
	if blocked == nil || !blocked.IsError || !strings.Contains(blocked.Flatten(), "未执行外部调用") {
		t.Fatal("fourth request not blocked")
	}
	for _, other := range []json.RawMessage{
		json.RawMessage(`{"url":"https://example.invalid/api/123?q=two"}`),
		json.RawMessage(`{"url":"https://example.invalid/api/124?q=two"}`),
		json.RawMessage(`{"url":"https://example.invalid/api/123?q=two","headers":{"Authorization":"Bearer other"}}`),
	} {
		release, blocked = lb.before(context.Background(), "http_request", other)
		release()
		if blocked != nil {
			t.Fatal("unrelated context blocked")
		}
	}
}

func TestLoopBreakerDoesNotInterpretOpaqueOrTruncatedOutput(t *testing.T) {
	lb := newLoopBreaker()
	in := json.RawMessage(`{"url":"https://example.invalid/api"}`)
	for i := 0; i < 5; i++ {
		release, blocked := lb.before(context.Background(), "http_request", in)
		if blocked != nil {
			t.Fatal("truncated result tripped gate")
		}
		lb.inspect(context.Background(), "http_request", in, actool.Text(`{"status":500,"body":"partial","body_truncated":true}`), nil)
		release()
	}
	if loopTargetKey("Bash", json.RawMessage(`{"command":"echo 422"}`)) != "" {
		t.Fatal("opaque command must not be a hard-gate target")
	}
	if loopTargetKey("http_fuzz", json.RawMessage(`{"url_template":"https://example.invalid/{{FUZZ}}"}`)) == "" {
		t.Fatal("real fuzz schema not recognized")
	}
}

func TestSkillHistoryRestoresOnlyPairedSuccess(t *testing.T) {
	msgs := []llm.Message{
		{Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "ok", Name: "Skill", Input: json.RawMessage(`{"skill":"api"}`)}, {Type: llm.BlockToolUse, ID: "bad", Name: "Skill", Input: json.RawMessage(`{"name":"camoufox"}`)}, {Type: llm.BlockToolUse, ID: "pending", Name: "Skill", Input: json.RawMessage(`{"name":"expert-triage-pivot"}`)}}},
		{Content: []llm.ContentBlock{llm.ToolResultText("ok", "loaded", false), llm.ToolResultText("bad", "failed", true)}},
	}
	var got []string
	seedUnlockFromHistory(msgs, func(name string) { got = append(got, CanonicalSkillAlias(name)) })
	if len(got) != 1 || got[0] != "api-recon" {
		t.Fatalf("unexpected restored skills: %v", got)
	}
}

func TestFuzzOutcomeRequiresActualCompleteCounters(t *testing.T) {
	for _, raw := range []string{`{"total_tested":1}`, `{"hits_count":-1,"total_tested":1,"error_count":0}`} {
		if sig, _ := classifyNegativeOutcome("http_fuzz", actool.Text(raw)); sig != "" {
			t.Fatal("incomplete counters classified as negative outcome")
		}
	}
	for _, raw := range []string{`{"hits_count":0,"total_tested":2,"error_count":0}`, `{"hits_count":0,"total_tested":2,"error_count":2}`} {
		if sig, _ := classifyNegativeOutcome("http_fuzz", actool.Text(raw)); sig == "" {
			t.Fatal("actual negative fuzz counters not classified")
		}
	}
}

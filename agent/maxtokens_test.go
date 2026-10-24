package agent

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// asciiReq 造一个「输入正好是 n 个 ASCII 字符」的请求，便于把估算值算成定值：
// 文本块 = (n+3)/4 + 3，消息固定开销 = 4。
func asciiReq(n, maxTokens int) llm.CompletionRequest {
	return llm.CompletionRequest{
		Messages:  []llm.Message{llm.UserText(strings.Repeat("a", n))},
		MaxTokens: maxTokens,
	}
}

func TestEstimateTextTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii-4", "abcd", 1},
		{"ascii-8", "abcdefgh", 2},
		// 中文按 1 字符 ≈ 1 token 高估——钳制方向上偏安全的一侧。
		{"cjk", "中文测试", 4},
		{"mixed", "abcd中文", 3},
	}
	for _, c := range cases {
		if got := estimateTextTokens(c.in); got != c.want {
			t.Errorf("%s: estimateTextTokens(%q) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

func TestEstimateRequestTokensCountsSystemAndTools(t *testing.T) {
	req := llm.CompletionRequest{
		System:   []string{strings.Repeat("a", 400)},                    // 100 + 4
		Messages: []llm.Message{llm.UserText(strings.Repeat("a", 400))}, // 100 + 3 + 4
		Tools: []llm.ToolSchema{{ // name 4→1 + desc 400→100 + 8 + schema {"k":"v"} 9→3
			Name:        "abcd",
			Description: strings.Repeat("a", 400),
			InputSchema: map[string]any{"k": "v"},
		}},
	}
	want := 104 + 107 + 1 + 100 + 8 + 3
	if got := estimateRequestTokens(req); got != want {
		t.Fatalf("estimateRequestTokens = %d, want %d", got, want)
	}
}

func TestDecideMaxTokens(t *testing.T) {
	cases := []struct {
		name          string
		cfg           Config
		promptChars   int
		asked         int
		wantEffective int
		wantReason    string
		wantStandDown bool
	}{
		{
			// 用户没填上限：harness 兜底发来的 32768 被清回 0，字段省略，
			// 由端点自己的默认值决定——不再把输出锁死在 32K。
			name:          "upstream-default",
			cfg:           Config{ContextWindowK: 200},
			promptChars:   8000, // est 2007
			asked:         32768,
			wantEffective: 0,
			wantReason:    "upstream_default",
		},
		{
			// Anthropic 的 max_tokens 是必填字段，清成 0 会被兜成 8192，故保持原样。
			name:          "anthropic-keeps-request",
			cfg:           Config{ContextWindowK: 200, Format: llm.FormatAnthropic},
			promptChars:   8000,
			asked:         32768,
			wantEffective: 32768,
			wantReason:    "within_limits",
		},
		{
			// 窗口够、用户手填了 64K：按手填值发，不再被任何「模型上限」压回去。
			name:          "user-filled-kept",
			cfg:           Config{ContextWindowK: 200, MaxTokens: 65536},
			promptChars:   8000,
			asked:         65536,
			wantEffective: 65536,
			wantReason:    "within_limits",
		},
		{
			// 长输入把窗口吃掉大半：按剩余空间压（与模型自身能力无关）。
			name:          "window-headroom",
			cfg:           Config{ContextWindowK: 32},
			promptChars:   4000, // est 1007 → headroom = 32000-1007-2048 = 28945
			asked:         32768,
			wantEffective: 28945,
			wantReason:    "window_headroom",
		},
		{
			// 剩余空间已经不够 4K：不钳成 445 那种必截断的小值，原样发出去让溢出自愈。
			name:          "stand-down-floor",
			cfg:           Config{ContextWindowK: 32},
			promptChars:   118000, // est 29507 → headroom = 445
			asked:         32768,
			wantEffective: 32768,
			wantReason:    "stand_down_headroom",
			wantStandDown: true,
		},
		{
			// 输入本身就装不下：钳 max_tokens 毫无意义，交给溢出/压缩。
			name:          "stand-down-overflow",
			cfg:           Config{ContextWindowK: 32},
			promptChars:   126000, // est 31507 → headroom < 0
			asked:         32768,
			wantEffective: 32768,
			wantReason:    "stand_down_overflow",
			wantStandDown: true,
		},
		{
			// 小上限保持小：内部摘要（800）绝不被抬到 4096。
			name:          "never-raise",
			cfg:           Config{ContextWindowK: 200},
			promptChars:   4000,
			asked:         800,
			wantEffective: 800,
			wantReason:    "within_limits",
		},
		{
			// 未设限就保持未设限（OpenAI 兼容协议下字段省略）。
			name:          "unset",
			cfg:           Config{ContextWindowK: 200},
			promptChars:   4000,
			asked:         0,
			wantEffective: 0,
			wantReason:    "unset",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := c.cfg.decideMaxTokens(asciiReq(c.promptChars, c.asked))
			if d.Effective != c.wantEffective {
				t.Fatalf("effective = %d, want %d (decision %+v)", d.Effective, c.wantEffective, d)
			}
			if d.Reason != c.wantReason {
				t.Fatalf("reason = %q, want %q (decision %+v)", d.Reason, c.wantReason, d)
			}
			if d.StandDown != c.wantStandDown {
				t.Fatalf("standDown = %v, want %v", d.StandDown, c.wantStandDown)
			}
		})
	}
}

// 钳制只用模型能力与窗口做判断，绝不复用用户配置的 MaxTokens：
// 否则「连接测试固定要 8192」这类内部调用会被用户的小配置误伤（推理模型先把
// 预算烧在思考上，正文为空，测试报「模型无回复」）。
func TestDecideMaxTokensIgnoresConfiguredCap(t *testing.T) {
	cfg := Config{ContextWindowK: 200, MaxTokens: 256}
	d := cfg.decideMaxTokens(asciiReq(4000, 8192))
	if d.Effective != 8192 {
		t.Fatalf("effective = %d, want 8192（配置不参与钳制）", d.Effective)
	}
}

type maxtokensCaptureProvider struct {
	got llm.CompletionRequest
}

func (p *maxtokensCaptureProvider) Stream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.got = req
	return func(yield func(llm.StreamEvent, error) bool) {}
}

func (p *maxtokensCaptureProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.got = req
	return llm.Message{}, "stop", llm.Usage{}, nil
}

func TestClampProviderAppliesDecision(t *testing.T) {
	// 窗口装得下：请求原样透传，不再按任何「模型输出上限」压。
	inner := &maxtokensCaptureProvider{}
	prov := &clampProvider{inner: inner, cfg: Config{ContextWindowK: 200}}

	_, _, _, err := prov.Complete(context.Background(), asciiReq(4000, 32000))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if inner.got.MaxTokens != 32000 {
		t.Fatalf("inner MaxTokens = %d, want 32000（无上限钳制）", inner.got.MaxTokens)
	}

	// 窗口装不下：按余量往下压，流式走同一套决策。
	inner2 := &maxtokensCaptureProvider{}
	sp := &clampProvider{inner: inner2, cfg: Config{ContextWindowK: 32}}
	for range sp.Stream(context.Background(), asciiReq(4000, 32000)) {
	}
	if inner2.got.MaxTokens != 28945 {
		t.Fatalf("stream inner MaxTokens = %d, want 28945（按窗口余量钳制）", inner2.got.MaxTokens)
	}
}

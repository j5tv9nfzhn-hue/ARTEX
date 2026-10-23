package agent

import (
	"errors"
	"testing"
)

// 声明窗口的解析：默认 200K，超过可信上限按上限算并标记 clamped。
// 这条是本次上下文修复的核心——1000K 的声明以前会原样当窗口用，
// 阈值被抬到 800K，压缩永远不触发。
func TestResolveContextWindowTokens(t *testing.T) {
	cases := []struct {
		declared    int
		want        int
		wantClamped bool
	}{
		{0, 200000, false},     // 未填 → 默认 200K
		{128, 128000, false},   // 正常声明
		{200, 200000, false},   // 正好在上限
		{256, 200000, true},    // 超上限 → 夹到 200K
		{1000, 200000, true},   // 线上真实的坑：声明 100 万
		{8, 32000, false},      // 低于下限 → 抬到 32K（不 clamp，属于纠偏）
		{-5, 200000, false},    // 负数按未填处理
	}
	for _, c := range cases {
		got, clamped := ResolveContextWindowTokens(c.declared)
		if got != c.want || clamped != c.wantClamped {
			t.Errorf("声明 %dK：got (%d, %v)，want (%d, %v)", c.declared, got, clamped, c.want, c.wantClamped)
		}
	}
}

// 上限可以被显式抬高（真·1M 窗口的模型）。
func TestContextWindowCapOverride(t *testing.T) {
	t.Setenv("ARTEX_CONTEXT_WINDOW_CAP_K", "1000")
	got, clamped := ResolveContextWindowTokens(1000)
	if got != 1000000 || clamped {
		t.Errorf("ARTEX_CONTEXT_WINDOW_CAP_K=1000 时 got (%d, %v)，want (1000000, false)", got, clamped)
	}
	// 非法值回落默认上限
	t.Setenv("ARTEX_CONTEXT_WINDOW_CAP_K", "abc")
	if got, _ := ResolveContextWindowTokens(1000); got != 200000 {
		t.Errorf("非法上限时 got %d，want 200000", got)
	}
}

// 触发点 = min(80%, 窗口 − 预留)，且不低于窗口的 1/4。
// 期望值与 ZCode 实测一致：128K → 75%、200K → 80%、1M → 80%。
func TestCompactionTriggerTokens(t *testing.T) {
	cases := []struct{ window, want int }{
		{32000, 24000},
		{128000, 96000},
		{200000, 160000},
		{400000, 320000},
		{1000000, 800000},
	}
	for _, c := range cases {
		if got := CompactionTriggerTokens(c.window); got != c.want {
			t.Errorf("窗口 %d：触发点 %d，want %d（%.1f%%）", c.window, got, c.want, float64(got)*100/float64(c.window))
		}
	}
}

// 溢出识别 + 从报错里捞真实窗口。
func TestContextOverflowDetection(t *testing.T) {
	cases := []struct {
		msg   string
		want  bool
		limit int
	}{
		{`openai: status 400: {"error":{"message":"This model's maximum context length is 131072 tokens."}}`, true, 131072},
		{`Your input exceeds the context window of this model. Please adjust your input and try again.`, true, 0},
		{`exceed_context_size_error: request (8588 tokens) exceeds the available context size (8192 tokens)`, true, 8192},
		{`openai: status 413: Request Entity Too Large`, true, 0},
		{`openai: status 429: rate limit exceeded`, false, 0},
		{`connection reset by peer`, false, 0},
		{``, false, 0},
	}
	for _, c := range cases {
		var err error
		if c.msg != "" {
			err = errors.New(c.msg)
		}
		if got := IsContextOverflowError(err); got != c.want {
			t.Errorf("%q：IsContextOverflow=%v，want %v", c.msg, got, c.want)
		}
		if got := ProviderContextLimit(err); got != c.limit {
			t.Errorf("%q：ProviderContextLimit=%d，want %d", c.msg, got, c.limit)
		}
	}
}

package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
)

// 上下文压缩的验收口径：触发点 = 用户设置的上下文窗口 × 80%（ARTEX_COMPACT_RATIO 可调）。
//
// 反解只影响"交给 SDK 的等效窗口"，所以断言直接打在实际交给 SDK 的 Config 上；
// 另外再用真正的 compaction.Compactor 跑一遍 Pre（见 TestBuiltinCompactionFiresAtTrigger），
// 确认镜像函数算出来的数不是纸上谈兵。
func TestCompactionTriggerHitsGoldenRatio(t *testing.T) {
	for _, window := range []int{160000, 200000, 400000, 1000000} {
		cfg := compactionConfig(window)
		got := compactionTrigger(cfg)
		want := int(float64(window) * defaultCompactRatio)
		if got != want {
			t.Errorf("窗口 %d：触发点 %d，期望 %d（实际比例 %.1f%%）",
				window, got, want, float64(got)*100/float64(window))
		}
		// 虚报窗口会让压缩永远来不及（只能等 API 返 413），这里钉死不许超。
		if cfg.ContextWindow > window {
			t.Errorf("窗口 %d：等效窗口 %d 大于真实窗口", window, cfg.ContextWindow)
		}
	}
}

// 小窗口：摘要预留 + 一轮增长是固定开销，占比会高到反解撞上真实窗口上限。
// 这时允许提前压，但绝不能晚于 80% —— 晚压意味着压不动，只能等 API 报 413。
func TestCompactionTriggerNeverLate(t *testing.T) {
	for _, window := range []int{32000, 48000, 64000, 100000, 128000} {
		got := compactionTrigger(compactionConfig(window))
		if limit := int(float64(window) * defaultCompactRatio); got > limit {
			t.Errorf("窗口 %d：触发点 %d 晚于 80%%（%d）", window, got, limit)
		}
		if got < window/4 {
			t.Errorf("窗口 %d：触发点 %d 过早（< 窗口 25%%）", window, got)
		}
	}
}

// 端到端：真正的 compaction.Compactor 在 触发点 上还不压、触发点 + 1 就压。
// 镜像函数若与 SDK 漂移（预留/缓冲/一轮增长/MicroCompact 门槛任一处变了），这条会先红。
func TestBuiltinCompactionFiresAtTrigger(t *testing.T) {
	for _, window := range []int{32000, 48000, 64000, 100000, 128000, 160000, 200000, 400000, 1000000} {
		cfg := compactionConfig(window)
		triggerTok := compactionTrigger(cfg)
		c := compaction.New(*cfg, func(context.Context, []llm.Message) (string, error) {
			return "摘要", nil
		})
		msgs := compactionTestHistory(24) // 多于 KeepRecent(8)，保证有可摘要的头部
		// 触发点前后各一格：1M 窗口上硬触发线用 >= 比较，触发点本身就会压，
		// 所以"还没压"这一侧取 triggerTok-1 才与分档无关。
		if out := c.Pre(context.Background(), msgs, triggerTok-1); llm.LastBoundaryIndex(out) >= 0 {
			t.Errorf("窗口 %d：%d tok（%d%%）就压了，早于目标触发点",
				window, triggerTok-1, (triggerTok-1)*100/window)
		}
		if out := c.Pre(context.Background(), msgs, triggerTok+1); llm.LastBoundaryIndex(out) < 0 {
			t.Errorf("窗口 %d：%d tok（%d%%）没压，晚于目标触发点",
				window, triggerTok+1, (triggerTok+1)*100/window)
		}
	}
}

// ARTEX_COMPACT_RATIO 是唯一的手动旋钮：支持 0.8 与 80 两种写法，越界夹逼，非法回落默认。
func TestCompactionRatioFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want float64
	}{
		{"", defaultCompactRatio},
		{"0.85", 0.85},
		{"85", 0.85},
		{"0.1", minCompactRatio},
		{"150", maxCompactRatio}, // >100 按百分比解释后仍越界 → 夹到上限
		{"abc", defaultCompactRatio},
	}
	for _, c := range cases {
		t.Setenv("ARTEX_COMPACT_RATIO", c.env)
		if got := CompactionRatio(); got != c.want {
			t.Errorf("ARTEX_COMPACT_RATIO=%q：got %v，want %v", c.env, got, c.want)
		}
	}
}

// compactionTestHistory 造 n 条纯文本历史：不含可压缩工具结果，
// 这样 Pre 的门槛计数不会被 MicroCompact 改写成"重新估算"的值。
func compactionTestHistory(n int) []llm.Message {
	msgs := make([]llm.Message, 0, n)
	for i := 0; i < n; i++ {
		role := llm.RoleUser
		if i%2 == 1 {
			role = llm.RoleAssistant
		}
		msgs = append(msgs, llm.Message{
			Role:    role,
			Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf("第 %d 轮的历史内容", i))},
		})
	}
	return msgs
}

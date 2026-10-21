package agent

import "testing"

// 上下文压缩的验收口径：触发点 = 用户设置的上下文窗口 × 80%。
//
// 反解只影响"交给 SDK 的等效窗口"，所以断言直接打在 SDK 的阈值算法上：
// 如果哪天 norma 改了 reservedForSummary / buffer / toolResultGrowthGuess 的分档
// 而这里没跟着改，这两条测试会先红——比线上等到 413 才知道便宜得多。
func TestCompactionTriggerHitsGoldenRatio(t *testing.T) {
	for _, window := range []int{160000, 200000, 400000, 1000000} {
		got := compactionTrigger(compactionTriggerWindow(window, defaultCompactRatio))
		want := int(float64(window) * defaultCompactRatio)
		if got != want {
			t.Errorf("窗口 %d：触发点 %d，期望 %d（实际比例 %.1f%%）",
				window, got, want, float64(got)*100/float64(window))
		}
	}
}

// 窗口太小时（预留 + 一轮增长的固定开销占比过高）反解会撞上真实窗口上限：
// 这种情况下允许提前压，但绝不能晚于 80% —— 晚压意味着压不动，只能等 API 报 413。
func TestCompactionTriggerNeverLate(t *testing.T) {
	for _, window := range []int{32000, 64000, 100000, 128000} {
		got := compactionTrigger(compactionTriggerWindow(window, defaultCompactRatio))
		if limit := int(float64(window) * defaultCompactRatio); got > limit {
			t.Errorf("窗口 %d：触发点 %d 晚于 80%%（%d）", window, got, limit)
		}
		if got < window/4 {
			t.Errorf("窗口 %d：触发点 %d 过早（< 窗口 25%%）", window, got)
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

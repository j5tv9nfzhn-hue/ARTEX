
package server

import "testing"

// TestAutoVerifyDecision 钉住「阈值 + 采样」两段口径：达到阈值强制复核、低于阈值按
// 比例抽查、off 一律不自动复核、未知严重度不参与判定、非法阈值按默认档兜底。
// 这些断言不依赖数据库，与 shouldAutoVerify 用的默认口径是同一套常量。
func TestAutoVerifyDecision(t *testing.T) {
	cases := []struct {
		name      string
		severity  string
		threshold string
		sample    float64
		want      bool
	}{
		{"高危阈值-严重达阈值", "critical", verifyThresholdHigh, 0, true},
		{"高危阈值-高危达阈值", "high", verifyThresholdHigh, 0, true},
		{"高危阈值-中危不抽查", "medium", verifyThresholdHigh, 0, false},
		{"中危阈值-中危达阈值", "medium", verifyThresholdMedium, 0, true},
		{"中危阈值-低危不抽查", "low", verifyThresholdMedium, 0, false},
		{"低危阈值-低危达阈值", "low", verifyThresholdLow, 0, true},
		{"仅严重阈值-高危不抽查", "high", verifyThresholdCritical, 0, false},
		{"仅严重阈值-严重达阈值", "critical", verifyThresholdCritical, 0, true},
		{"大小写与空白容忍", "  HIGH ", verifyThresholdHigh, 0, true},
		{"关闭-严重也不复核", "critical", verifyThresholdOff, 100, false},
		{"抽查100-低于阈值也复核", "low", verifyThresholdHigh, 100, true},
		{"非法阈值回退默认档", "critical", "nonsense", 0, true},
		{"未知严重度不判定", "weird", verifyThresholdLow, 100, false},
	}
	for _, c := range cases {
		got, reason := autoVerifyDecision(c.severity, c.threshold, c.sample)
		if got != c.want {
			t.Fatalf("%s: autoVerifyDecision(%q, %q, %v) = %v，期望 %v", c.name, c.severity, c.threshold, c.sample, got, c.want)
		}
		if got && reason == "" {
			t.Fatalf("%s: 自动复核必须留下原因，否则复核记录里看不出为什么跑", c.name)
		}
	}
}

// TestAutoVerifyDecisionReasons 钉住原因前缀：前端按 auto:severity / auto:sample
// 把复核记录显示成「强制 / 抽样」，改前缀会静默影响界面文案。
func TestAutoVerifyDecisionReasons(t *testing.T) {
	_, forced := autoVerifyDecision("high", verifyThresholdHigh, 0)
	if forced != "auto:severity=high" {
		t.Fatalf("达阈值的原因应是 auto:severity=<等级>，got %q", forced)
	}
	_, sampled := autoVerifyDecision("low", verifyThresholdHigh, 100)
	if sampled != "auto:sample" {
		t.Fatalf("抽查的原因应是 auto:sample，got %q", sampled)
	}
}

package server

import "testing"

// TestShouldAutoVerifySeverityPolicy 钉住复核的成本口径：高危一律复核（不采样），
// 中低危按采样率，未知严重度不自动复核。
func TestShouldAutoVerifySeverityPolicy(t *testing.T) {
	t.Setenv(verifySampleRateEnv, "0")
	for _, sev := range []string{"critical", "high", "CRITICAL", " high "} {
		ok, reason := shouldAutoVerify(sev)
		if !ok || reason == "" {
			t.Fatalf("%q 必须强制复核（采样率 0 也要复核）", sev)
		}
	}
	for _, sev := range []string{"medium", "low"} {
		if ok, _ := shouldAutoVerify(sev); ok {
			t.Fatalf("%q 在采样率 0 时不应自动复核", sev)
		}
	}
	if ok, _ := shouldAutoVerify("weird"); ok {
		t.Fatal("未知严重度不应自动复核")
	}

	t.Setenv(verifySampleRateEnv, "1")
	for _, sev := range []string{"medium", "low"} {
		if ok, _ := shouldAutoVerify(sev); !ok {
			t.Fatalf("%q 在采样率 1 时必须复核", sev)
		}
	}

	// 非法配置值退回默认 0.25，而不是把复核整体关掉或全开。
	t.Setenv(verifySampleRateEnv, "not-a-number")
	if got := verifySampleRate(); got != 0.25 {
		t.Fatalf("非法采样率应退回默认值，got %v", got)
	}
}

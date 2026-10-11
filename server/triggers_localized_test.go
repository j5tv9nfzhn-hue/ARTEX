package server

import "testing"

// 触发器设置中文错误回归测试，复用 assertChineseError。
// TestTriggerErrorConstantsLocalized 检查三类常量。
func TestTriggerErrorConstantsLocalized(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"no_condition", errTriggerNoCondition},
		{"tool_set_empty", errTriggerToolSetEmpty},
		{"custom_only", errTriggerCustomOnly},
	}
	for _, c := range cases {
		assertChineseError(t, c.name, c.msg)
	}
}

// TestValidateTriggerLocalized 调用纯函数 validateTrigger，
// 缺少条件、未选择工具时返回中文错误，有效输入返回空字符串。
// 结果由 pgCreateTrigger、pgUpdateTrigger 以 writeErr(400) 直接返回。
func TestValidateTriggerLocalized(t *testing.T) {
	if msg := validateTrigger(&triggerReq{}); msg != errTriggerNoCondition {
		t.Fatalf("条件缺失文案 = %q，预期 = %q", msg, errTriggerNoCondition)
	}
	if msg := validateTrigger(&triggerReq{OnToolCall: true}); msg != errTriggerToolSetEmpty {
		t.Fatalf("未选择工具文案 = %q，预期 = %q", msg, errTriggerToolSetEmpty)
	}
	if msg := validateTrigger(&triggerReq{OnFinding: true}); msg != "" {
		t.Fatalf("有效输入文案 = %q，预期为空字符串", msg)
	}
	if msg := validateTrigger(&triggerReq{OnToolCall: true, ToolNames: []string{"nmap"}}); msg != "" {
		t.Fatalf("有效工具调用文案 = %q，预期为空字符串", msg)
	}
}

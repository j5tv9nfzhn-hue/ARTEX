package server

import "testing"

// 资产与任务拦截规则验证器的中文化回归测试。
// validateAssetInterceptRuleReq、validateTaskInterceptRuleReq 是无数据库纯函数，
// 既固定常量，也真实调用错误路径。复用 assertChineseError，要求汉字存在且无韩文。
// pattern、exact_ip、cidr、kind、action、block、allow 等协议标识保留 ASCII。
func TestAssetInterceptErrorsLocalized(t *testing.T) {
	// 固定错误常量。
	for _, c := range []struct{ label, msg string }{
		{"pattern_empty", errAssetInterceptPatternEmpty},
		{"bad_exact_ip", errAssetInterceptInvalidExactIPFmt},
		{"bad_cidr", errAssetInterceptInvalidCIDRFmt},
		{"bad_kind", errAssetInterceptInvalidKindFmt},
		{"bad_action", errTaskInterceptInvalidAction},
	} {
		assertChineseError(t, c.label, c.msg)
	}

	// 实际执行 validateAssetInterceptRuleReq 的四个拒绝分支。
	for _, c := range []struct {
		label string
		req   assetInterceptRuleReq
	}{
		{"empty_pattern", assetInterceptRuleReq{Kind: "exact_ip", Pattern: "   "}},
		{"invalid_exact_ip", assetInterceptRuleReq{Kind: "exact_ip", Pattern: "not-an-ip"}},
		{"invalid_cidr", assetInterceptRuleReq{Kind: "cidr", Pattern: "nonsense"}},
		{"invalid_kind", assetInterceptRuleReq{Kind: "nope", Pattern: "example.com"}},
	} {
		req := c.req
		err := validateAssetInterceptRuleReq(&req)
		if err == nil {
			t.Fatalf("%s：未返回错误（验证通过）", c.label)
		}
		assertChineseError(t, c.label, err.Error())
	}

	// 不得拒绝有效输入。
	okReq := assetInterceptRuleReq{Kind: "cidr", Pattern: "192.168.0.0/16"}
	if err := validateAssetInterceptRuleReq(&okReq); err != nil {
		t.Fatalf("有效 CIDR 被拒绝：%v", err)
	}

	// 实际执行 validateTaskInterceptRuleReq 的 action 拒绝分支。
	badAction := taskInterceptRuleReq{Action: "nope", Kind: "exact_domain", Pattern: "example.com"}
	if err := validateTaskInterceptRuleReq(&badAction); err == nil {
		t.Fatalf("bad_action_path：未返回错误（验证通过）")
	} else {
		assertChineseError(t, "bad_action_path", err.Error())
	}

	// action 为空时归一为默认 block，应通过验证。
	defaultAction := taskInterceptRuleReq{Action: "", Kind: "exact_domain", Pattern: "example.com"}
	if err := validateTaskInterceptRuleReq(&defaultAction); err != nil {
		t.Fatalf("默认 action 归一化失败：%v", err)
	}
	if defaultAction.Action != "block" {
		t.Fatalf("默认 action 未归一为 block：%q", defaultAction.Action)
	}
}

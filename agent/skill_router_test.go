package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func dummyTool(name, desc string) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        name,
		Description: desc,
		Schema:      map[string]any{"type": "object"},
		Run: func(_ context.Context, _ json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return actool.Text("ok"), nil
		},
	})
}

func TestBuildTacticalRoutingHintMatchesDistilledCaseSkill(t *testing.T) {
	skillDesc := "Load a specialized skill.\n<available_skills>\n" +
		"- web-vuln-tactics: Web 漏洞深度利用\n" +
		"- ai-coding-sandbox-tactics: AI 编程沙箱、GPU 推理控制面与 BYOK Agent 渗透 [含配套 references 实战手册，调用即自动附带加载]\n" +
		"- wasm-cloud-saas-tactics: WASM 前端签名逆向、云存储 (OSS/STS) 预签名目录穿越与 AI 内容 SaaS 渗透 [含配套 references 实战手册，调用即自动附带加载]\n" +
		"- report-payment-gateway: 外部实战报告提炼经验：围绕「支付网关签名绕过与双花」，含 HMAC-SHA256、notify_url 回调伪造\n" +
		"- case-web-42: 任务复盘经验（web_security）：围绕「商城优惠券并发重放审计」，已验证 2 条路径、发现 1 个漏洞；含关键词 Race Condition、coupon_redeem、Redis 分布式锁绕过。遇到同类目标或同类故障时加载。\n" +
		"- case-re-99: 任务复盘经验（software_re）：围绕「libsign.so 签名算法逆向」，已验证 1 条路径、1 个样本；含关键词 ghidra_decompile_function、HMAC-SHA256、libsign.so。遇到同类目标或同类故障时加载。\n" +
		"</available_skills>"

	tools := []actool.CoreTool{
		dummyTool("Skill", skillDesc),
		dummyTool("tactical_kb", "Tactical knowledge base"),
		dummyTool("mcp__Playwright__browser_navigate", "Navigate browser"),
		dummyTool("mcp__Ghidra__decompile_function", "Decompile in Ghidra"),
	}

	def := DeferredInfo{
		GlobalNames: []string{"mcp__Playwright__browser_navigate"},
	}

	hint := BuildTacticalRoutingHint("请对目标商城的 coupon_redeem 优惠券接口做并发重放与越权测试", tools, def)
	if !strings.Contains(hint, "case-web-42") {
		t.Fatalf("expected distilled skill case-web-42 in routing hint, got:\n%s", hint)
	}
	if !strings.Contains(hint, "历史任务沉淀复盘经验") {
		t.Fatalf("expected distilled skill title in routing hint, got:\n%s", hint)
	}
	if !strings.Contains(hint, "web-vuln-tactics") {
		t.Fatalf("expected builtin web-vuln-tactics alongside distilled skill, got:\n%s", hint)
	}
	if !strings.Contains(hint, "Playwright") || !strings.Contains(hint, "Ghidra") {
		t.Fatalf("expected both active (Playwright) and skill-gated (Ghidra) MCP servers in routing hint, got:\n%s", hint)
	}
	if strings.Contains(hint, "case-re-99") {
		t.Fatalf("unrelated RE case skill should not match coupon web task:\n%s", hint)
	}

	sandboxHint := BuildTacticalRoutingHint("审计目标 Next.js + Supabase AI Coding 沙箱环境的 /coder/command 与 byok_base_url", tools, def)
	if !strings.Contains(sandboxHint, "ai-coding-sandbox-tactics") {
		t.Fatalf("expected ai-coding-sandbox-tactics in sandbox routing hint, got:\n%s", sandboxHint)
	}

	wasmHint := BuildTacticalRoutingHint("逆向前端 crypto_util.wasm 签名算法并测试阿里云 OSS upload/sign-url 预签名目录穿越", tools, def)
	if !strings.Contains(wasmHint, "wasm-cloud-saas-tactics") {
		t.Fatalf("expected wasm-cloud-saas-tactics in wasm routing hint, got:\n%s", wasmHint)
	}

	reportHint := BuildTacticalRoutingHint("审计支付网关 notify_url 回调伪造与签名绕过", tools, def)
	if !strings.Contains(reportHint, "report-payment-gateway") || !strings.Contains(reportHint, "外部实战报告提炼经验") {
		t.Fatalf("expected report-payment-gateway in routing hint, got:\n%s", reportHint)
	}

	reHint := BuildTacticalRoutingHint("分析样本 libsign.so 中的 HMAC-SHA256 签名校验函数", tools, def)
	if !strings.Contains(reHint, "case-re-99") {
		t.Fatalf("expected distilled RE skill case-re-99 in routing hint, got:\n%s", reHint)
	}
}

func TestCanonicalSkillAliasesIncludeNewCombatPackages(t *testing.T) {
	if got := CanonicalSkillAlias("sandbox"); got != "ai-coding-sandbox-tactics" {
		t.Fatalf("CanonicalSkillAlias(sandbox) = %q, want ai-coding-sandbox-tactics", got)
	}
	if got := CanonicalSkillAlias("wasm"); got != "wasm-cloud-saas-tactics" {
		t.Fatalf("CanonicalSkillAlias(wasm) = %q, want wasm-cloud-saas-tactics", got)
	}
}

func TestGuardedToolNormalizesAndCoercesInput(t *testing.T) {
	var captured string
	inner := actool.Build(actool.Spec{
		Name:        "sample_tool",
		Description: "Sample tool",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer"},
				"enabled":   map[string]any{"type": "boolean"},
				"tags":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required":             []any{"file_path", "limit"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			captured = string(in)
			return actool.Text("ok"), nil
		},
	})

	decorated := guardPanic(DecorateTool(inner, "", nil))
	rawAware, ok := decorated.(interface{ AcceptsRawInput() bool })
	if !ok || !rawAware.AcceptsRawInput() {
		t.Fatalf("expected guardedTool to implement AcceptsRawInput() == true")
	}

	// Pass wrapped params + alias "path"->"file_path" + stringified integer/boolean/array
	res, err := decorated.Call(context.Background(), json.RawMessage(`{"params":{"path":"/app/main.go","limit":"25","enabled":"true","tags":"a, b"}}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("expected normalized tool call to succeed, got err=%v, res=%+v", err, res)
	}
	if !strings.Contains(captured, `"file_path":"/app/main.go"`) || !strings.Contains(captured, `"limit":25`) || !strings.Contains(captured, `"enabled":true`) {
		t.Fatalf("unexpected normalized payload: %s", captured)
	}
}

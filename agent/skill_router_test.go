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
		"- case-web-42: 任务复盘经验（web_security）：围绕「商城优惠券并发重放审计」，已验证 2 条路径、发现 1 个漏洞；含关键词 Race Condition、coupon_redeem、Redis 分布式锁绕过。遇到同类目标或同类故障时加载。\n" +
		"- case-re-99: 任务复盘经验（software_re）：围绕「libsign.so 签名算法逆向」，已验证 1 条路径、1 个样本；含关键词 ghidra_decompile_function、HMAC-SHA256、libsign.so。遇到同类目标或同类故障时加载。\n" +
		"</available_skills>"

	tools := []actool.CoreTool{
		dummyTool("Skill", skillDesc),
		dummyTool("tactical_kb", "Tactical knowledge base"),
	}

	hint := BuildTacticalRoutingHint("请对目标商城的 coupon_redeem 优惠券接口做并发重放与越权测试", tools, DeferredInfo{})
	if !strings.Contains(hint, "case-web-42") {
		t.Fatalf("expected distilled skill case-web-42 in routing hint, got:\n%s", hint)
	}
	if !strings.Contains(hint, "历史任务沉淀复盘经验") {
		t.Fatalf("expected distilled skill title in routing hint, got:\n%s", hint)
	}
	if !strings.Contains(hint, "web-vuln-tactics") {
		t.Fatalf("expected builtin web-vuln-tactics alongside distilled skill, got:\n%s", hint)
	}
	if strings.Contains(hint, "case-re-99") {
		t.Fatalf("unrelated RE case skill should not match coupon web task:\n%s", hint)
	}

	reHint := BuildTacticalRoutingHint("分析样本 libsign.so 中的 HMAC-SHA256 签名校验函数", tools, DeferredInfo{})
	if !strings.Contains(reHint, "case-re-99") {
		t.Fatalf("expected distilled RE skill case-re-99 in routing hint, got:\n%s", reHint)
	}
}

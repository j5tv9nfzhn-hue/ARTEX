package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTacticalKBCaseSkillsMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(dir, "case-web-42"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: case-web-42\ndescription: 任务复盘经验（web_security）：围绕「商城优惠券并发重放审计」，含关键词 coupon_redeem、Race Condition。\n---\n\n" +
		"# 商城优惠券并发重放审计 · 任务复盘手册\n\n" +
		"## 已验证的有效路径\n\n1. **并发压测 coupon_redeem** → 触发 1 个确认漏洞\n\n" +
		"## 已确认漏洞与可利用点\n\n- [HIGH] **优惠券双花**（Race Condition）\n\n" +
		"## 失败、误区与止损\n\n- **触发 3-Strike 熔断**：单账号连续重放超过 3 次后触发分布式锁。\n"
	if err := os.WriteFile(filepath.Join(dir, "case-web-42", "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_SKILL_DIR", dir)

	ts := NewToolSet(nil, "")
	res, err := ts.tacticalKBTool().Call(context.Background(), json.RawMessage(`{"mode":"case_skills","query":"coupon_redeem"}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", res.Flatten())
	}
	out := res.Flatten()
	for _, want := range []string{"case-web-42", "并发压测 coupon_redeem", "优惠券双花", "触发 3-Strike 熔断"} {
		if !strings.Contains(out, want) {
			t.Errorf("case_skills output missing %q:\n%s", want, out)
		}
	}
}

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/permission"

	actool "github.com/Autumn-27/norma/tool"
)

// 额度闸门的三段行为：软提醒在 50%、硬拒绝在 100%、收尾指令只发一次。
// 这几条是任务 27 的全部契约，任何一条回退都会让「模型换参数继续刷」重新出现。
func TestIntentBudgetSoftAndHard(t *testing.T) {
	b := newIntentBudget(10) // 软阈值 = 5
	var softAt, reportAt, refusals int
	for i := 1; i <= 20; i++ {
		allow, extra := b.beforeCall()
		switch {
		case !allow:
			refusals++
		case i == 5:
			if len(extra) != 1 || !strings.Contains(extra[0].Text(), "一半") {
				t.Fatalf("第 5 次应只发软提醒，实际 %+v", extra)
			}
			softAt = i
		case i == 10:
			if len(extra) != 1 || !strings.Contains(extra[0].Text(), "① 已完成") {
				t.Fatalf("第 10 次应发结构化收尾指令，实际 %+v", extra)
			}
			reportAt = i
		}
	}
	if softAt != 5 {
		t.Errorf("软提醒没在第 50%% 触发（%d）", b.soft)
	}
	if reportAt != 10 {
		t.Error("硬上限那一次没有要求收尾")
	}
	// 第 11..20 次全部拒绝，且**不再**重复注入收尾指令。
	if refusals != 10 {
		t.Errorf("拒绝次数 %d，期望 10", refusals)
	}
	if b.Calls() != 10 {
		t.Errorf("计数 %d，被拒绝的调用不应计入", b.Calls())
	}
	if !b.Exhausted() {
		t.Error("额度已用尽但 Exhausted=false")
	}
}

// 被拒绝的调用不能递增计数——否则一次 run 内会把计数器刷到几千，指标失真。
func TestIntentBudgetRefusalDoesNotCount(t *testing.T) {
	b := newIntentBudget(3)
	for i := 0; i < 3; i++ {
		if allow, _ := b.beforeCall(); !allow {
			t.Fatalf("第 %d 次不该被拒", i+1)
		}
	}
	for i := 0; i < 50; i++ {
		allow, extra := b.beforeCall()
		if allow {
			t.Fatal("额度用尽后仍放行")
		}
		if i > 0 && len(extra) > 0 {
			t.Fatal("收尾指令被重复注入")
		}
	}
	if b.Calls() != 3 {
		t.Errorf("计数被拒绝的调用污染：%d", b.Calls())
	}
	if b.Refused() != 50 {
		t.Errorf("Refused=%d，期望 50", b.Refused())
	}
}

// nil 额度（关闭机制）必须完全不影响调用。
func TestIntentBudgetDisabled(t *testing.T) {
	var b *intentBudget // 通过 newIntentBudget(0) 得到的也是 nil
	if b2 := newIntentBudget(0); b2 != nil {
		t.Fatalf("cap=0 应返回 nil，得到 %+v", b2)
	}
	allow, extra := b.beforeCall()
	if !allow || len(extra) != 0 {
		t.Fatalf("nil 额度不该拦截：allow=%v extra=%d", allow, len(extra))
	}
	if b.Exhausted() || b.Calls() != 0 || b.Refused() != 0 {
		t.Error("nil 额度不应有状态")
	}
}

// 环境变量覆盖 + 非法值回落。
func TestIntentToolCapEnv(t *testing.T) {
	t.Setenv("ARTEX_INTENT_TOOL_CAP", "")
	if got := IntentToolCap(); got != DefaultIntentToolCap {
		t.Errorf("默认上限 %d，期望 %d", got, DefaultIntentToolCap)
	}
	t.Setenv("ARTEX_INTENT_TOOL_CAP", "0")
	if got := IntentToolCap(); got != 0 {
		t.Errorf("0 应表示关闭，得到 %d", got)
	}
	t.Setenv("ARTEX_INTENT_TOOL_CAP", "abc")
	if got := IntentToolCap(); got != DefaultIntentToolCap {
		t.Errorf("非法值应回落默认，得到 %d", got)
	}
	t.Setenv("ARTEX_INTENT_TOOL_CAP", "9999")
	if got := IntentToolCap(); got != intentToolCapCeil {
		t.Errorf("超大值应被夹到 %d，得到 %d", intentToolCapCeil, got)
	}
}

// 端到端：guardedTool 在额度用尽后**不执行**工具实现（副作用为零），
// 并把拒绝归到 error_class=quota。
func TestGuardedToolQuotaGateBlocksExecution(t *testing.T) {
	calls := 0
	inner := &countingTool{onCall: func() { calls++ }}
	g := &guardedTool{CoreTool: inner}

	b := newIntentBudget(2)
	ctx := WithIntentBudget(context.Background(), b)
	tc := &actool.ToolContext{}

	for i := 0; i < 2; i++ {
		res, err := g.Call(ctx, json.RawMessage(`{}`), tc)
		if err != nil || res.IsError {
			t.Fatalf("第 %d 次应当成功：err=%v res=%+v", i+1, err, res)
		}
	}
	if calls != 2 {
		t.Fatalf("底层工具被调用 %d 次，期望 2", calls)
	}
	res, err := g.Call(ctx, json.RawMessage(`{}`), tc)
	if err != nil {
		t.Fatalf("拒绝不应返回 Go error（要成为一条 tool_result）：%v", err)
	}
	if !res.IsError {
		t.Fatal("额度用尽后应返回错误结果")
	}
	if calls != 2 {
		t.Fatalf("被拒绝的调用执行了工具实现（副作用！）：%d", calls)
	}
	if !strings.HasPrefix(res.Flatten(), "【额度】") {
		t.Fatalf("拒绝文本缺少【额度】前缀，error_class 会归错类：%q", res.Flatten())
	}
	if class := ErrorClassOf(res, nil, ctx); class != ErrClassQuota {
		t.Fatalf("error_class=%q，期望 %q", class, ErrClassQuota)
	}
}

// countingTool 是最小可用的 CoreTool，用来观察「底层到底被调用了几次」。
type countingTool struct {
	onCall func()
}

func (c *countingTool) Name() string                           { return "Bash" }
func (c *countingTool) Description() string                    { return "test tool" }
func (c *countingTool) Prompt() string                         { return "" }
func (c *countingTool) InputSchema() map[string]any            { return map[string]any{} }
func (c *countingTool) IsReadOnly(json.RawMessage) bool        { return true }
func (c *countingTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (c *countingTool) CheckPermissions(context.Context, json.RawMessage, permission.Context) permission.Decision {
	return permission.Decision{Behavior: permission.Allow}
}
func (c *countingTool) Call(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
	if c.onCall != nil {
		c.onCall()
	}
	return actool.Text("ok"), nil
}

func TestIntentTurnBudgetClampedToQuota(t *testing.T) {
	// 默认额度 50 → 轮次上限 100。
	t.Setenv("ARTEX_INTENT_TOOL_CAP", "")
	if got := intentTurnBudget(0); got != 100 {
		t.Fatalf("MaxTurns=0（不限）应夹到 100，得到 %d", got)
	}
	if got := intentTurnBudget(500); got != 100 {
		t.Fatalf("MaxTurns=500 应夹到 100，得到 %d", got)
	}
	if got := intentTurnBudget(30); got != 30 {
		t.Fatalf("MaxTurns=30 本来就更小，应原样返回，得到 %d", got)
	}
	// 额度关闭 → 不动运维配置。
	t.Setenv("ARTEX_INTENT_TOOL_CAP", "0")
	if got := intentTurnBudget(0); got != 0 {
		t.Fatalf("额度关闭时 MaxTurns=0 应原样返回，得到 %d", got)
	}
	if got := intentTurnBudget(300); got != 300 {
		t.Fatalf("额度关闭时 MaxTurns=300 应原样返回，得到 %d", got)
	}
	// 小额度 → 上限随之下移。
	t.Setenv("ARTEX_INTENT_TOOL_CAP", "10")
	if got := intentTurnBudget(0); got != 20 {
		t.Fatalf("额度 10 时轮次上限应为 20，得到 %d", got)
	}
}

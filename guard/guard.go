// Package guard implements the safety boundary layer (docs §11): an audit log,
// user-configured intercept-rule evaluation, and Observer/G5 failure attribution.
// Every tool call passes through the PreToolUse hook before executing.
// (The RoE authorization-scope mechanism was removed; a replacement may be added
// later.) Destructive/exfil gating is no longer hard-coded here — it lives in the
// DB intercept rules (seeded as ordinary [内置] rules, so users can disable or
// delete them), evaluated via applyIntercept.
package guard

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/artex/metrics"
	"github.com/Autumn-27/norma/hook"
)

// AuditEntry records one gated tool call.
type AuditEntry struct {
	TS      int64  `json:"ts"`
	CallID  string `json:"call_id,omitempty"` // intercept trace 的 ToolUseID；空 = 无法配对（trace 未启用/关联不唯一）
	Tool    string `json:"tool"`
	Action  string `json:"action"` // allow|block
	Reason  string `json:"reason,omitempty"`
	Command string `json:"command,omitempty"`
}

// Guard enforces the side-effect policy via agent-core hooks.
type Guard struct {
	mu          sync.Mutex
	audit       []AuditEntry
	attrib      map[string]int // failure attribution counts (Observer / G5)
	reg         *hook.Registry
	interceptor *intercept.Interceptor // optional; nil disables user-configured rules
}

// New creates a Guard without user-configured intercept rules (used for pentest
// tasks where the Interceptor is not yet available).
func New() *Guard { return newGuard(nil) }

// NewWithInterceptor creates a Guard with user-configured intercept rules.
func NewWithInterceptor(ic *intercept.Interceptor) *Guard { return newGuard(ic) }

func newGuard(ic *intercept.Interceptor) *Guard {
	g := &Guard{attrib: map[string]int{}, interceptor: ic}
	g.reg = hook.NewRegistry().
		On(hook.PreToolUse, g.preToolUse).
		On(hook.PostToolUse, g.postToolUse)
	return g
}

// Hooks returns the hook registry to attach to an agent session.
func (g *Guard) Hooks() *hook.Registry { return g.reg }

func (g *Guard) preToolUse(ctx context.Context, ev hook.Event) hook.Result {
	// Extract the shell-command surface for the audit log: Bash + the interactive-shell
	// tools (shell_open's command, shell_send's text). Destructive/exfil gating is no
	// longer hard-coded here — it now lives in the DB intercept rules, evaluated by
	// applyIntercept below. Other tools record an empty command.
	var cmd string
	switch ev.ToolName {
	case "Bash", "shell_open":
		var in struct {
			Command string `json:"command"`
		}
		if len(ev.Input) > 0 {
			// 尽力抽取命令供审计；入参解析错误不在钩子层计数（执行包装层
			// guardedTool.Call 会对同一次调用计数，避免一次失败记两笔）。
			_ = json.Unmarshal(ev.Input, &in)
		}
		cmd = in.Command
	case "shell_send":
		var in struct {
			Text string `json:"text"`
		}
		if len(ev.Input) > 0 {
			_ = json.Unmarshal(ev.Input, &in)
		}
		cmd = in.Text
	case "apply_patch":
		// 多文件补丁的审计面就是它要动的那批文件：记全路径列表，
		// 否则审计日志里会出现「改了东西但 command 为空」的记录。
		var in struct {
			Patches []struct {
				FilePath string `json:"file_path"`
			} `json:"patches"`
		}
		if len(ev.Input) > 0 {
			_ = json.Unmarshal(ev.Input, &in)
		}
		files := make([]string, 0, len(in.Patches))
		for _, p := range in.Patches {
			if p.FilePath != "" {
				files = append(files, p.FilePath)
			}
		}
		cmd = strings.Join(files, ", ")
	}
	// Claim the intercept trace call exactly ONCE, up front, so this audit row and
	// the intercept record share the same call ID (trace 的 ToolUseID，#15/#17)。
	// 只对进入拦截规则体系的工具 claim：未启用的工具不占用 trace 上下文快照。
	var callID string
	if g.interceptor != nil && g.interceptor.IsToolEnabled(ev.ToolName) {
		ctx, callID = intercept.ClaimCall(ctx, ev.ToolName, ev.Input)
	}
	g.record(callID, ev.ToolName, "allow", "", cmd)
	return g.applyIntercept(ctx, ev, callID)
}

// applyIntercept evaluates user-configured intercept rules against the tool call.
// Both rules and the fallback judge receive the complete tool input. The caller
// (preToolUse) has already claimed the trace call for enabled tools — re-claiming
// here would find the call taken and blank the correlation, so don't.
func (g *Guard) applyIntercept(ctx context.Context, ev hook.Event, callID string) hook.Result {
	if g.interceptor == nil {
		return hook.Result{}
	}
	if !g.interceptor.IsToolEnabled(ev.ToolName) {
		return hook.Result{}
	}
	dec, matched := g.interceptor.Match(ev.ToolName, ev.Input)
	if !matched {
		// No rule matched. Ask the LLM fallback judge (if enabled); when it is off
		// or unwired, keep current behavior and allow.
		d, judged := g.interceptor.Judge(ctx, ev.ToolName, ev.Input)
		if !judged {
			return hook.Result{}
		}
		dec = d
	}
	switch dec.Action {
	case "deny":
		// 观测:deny 命中不阻塞审批,直接记一条 denied（历史/任务拦截页可见）。
		g.interceptor.Log(ctx, intercept.ConvIDFromContext(ctx), dec, ev.ToolName, ev.Input, "denied")
		return g.block(callID, ev.ToolName, systemBlockMessage(dec.Message), "")
	case "allow":
		// Record explicit rule and model approvals so review details remain auditable.
		g.interceptor.Log(ctx, intercept.ConvIDFromContext(ctx), dec, ev.ToolName, ev.Input, "allowed")
		return hook.Result{}
	case "ask":
		// 平台治理拦截 framing 保留中文原文。g.block 将其作为 hook.Result.Message
		// 返回模型 tool_result，引导 Agent 将拦截视为平台规则而非目标防御，避免尝试绕过。
		// ask 路径不同于 deny，不经过 Interceptor.Log；g.record 同时将整条消息写入
		// GET /api/audit 的 entries[].reason，因此同一消息兼作模型输入与审计展示。
		// 不为本次语言重构拆分这两条通路。
		// Worker 上下文已经取消时立即阻止，不创建 pending 记录，避免孤立数据库记录，
		// 并让 execOne 快速完成，减少与 drainSynthetic 的竞争。
		if ctx.Err() != nil {
			return g.block(callID, ev.ToolName, systemBlockMessage("工作已取消，平台安全管控阻止执行"), "")
		}
		convID := intercept.ConvIDFromContext(ctx)
		if !g.interceptor.HandleAsk(ctx, convID, dec, ev.ToolName, ev.Input) {
			return g.block(callID, ev.ToolName, systemBlockMessage("人工审批未通过（用户拒绝或审批超时）"), "")
		}
		return hook.Result{}
	}
	return hook.Result{}
}

// systemBlockMessage frames an intercept block as an ARTEX platform-governance
// decision so the agent does not mistake it for a target-side defense.
//
// The bare reasons ("禁止执行此工具" / "用户拒绝") read exactly like a WAF/403 on
// the target, so a pentest agent's instinct is to bypass them — rewrite the
// command, swap the payload, re-encode, retry. That is both futile (the platform
// blocks the class of action, not one string) and wrong (it's a policy decision,
// not an obstacle to defeat). This prefix states plainly that the block comes
// from the platform, is not the target's protection, and that the operation is
// forbidden — so the agent pivots to another approach instead of evading it.
// Audit/history rows keep the raw reason (see Interceptor.Log); only the
// model-facing tool_result carries this framing.
func systemBlockMessage(reason string) string {
	return "【ARTEX 平台管控·非目标防御】此调用被平台拦截。" +
		"原因：" + reason + "。此操作被禁止。"
}

var reBlocked = regexp.MustCompile(`(?i)\b(403|forbidden|waf|blocked|rate.?limit|429|captcha|denied)\b`)

// postToolUse is the Observer failure-attribution hook (G5): it classifies tool
// results into blocked / error / ok so the planner can change strategy instead
// of giving up at a WAF.
func (g *Guard) postToolUse(_ context.Context, ev hook.Event) hook.Result {
	if ev.ToolName != "Bash" {
		return hook.Result{}
	}
	class := "ok"
	switch {
	case reBlocked.Match(ev.Result):
		class = "blocked"
	case ev.IsError:
		class = "error"
	}
	g.mu.Lock()
	g.attrib[class]++
	g.mu.Unlock()
	return hook.Result{}
}

// Attributions returns failure-attribution counts (Observer / G5).
func (g *Guard) Attributions() map[string]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]int, len(g.attrib))
	for k, v := range g.attrib {
		out[k] = v
	}
	return out
}

// block records a blocked call and returns the hook result. The call counts as one
// attempt + one stage=hook error in the reliability metrics.
func (g *Guard) block(callID, tool, reason, cmd string) hook.Result {
	metrics.ObserveToolBlocked(tool)
	g.record(callID, tool, "block", reason, cmd)
	return hook.Result{Decision: "block", Message: reason}
}

func (g *Guard) record(callID, tool, action, reason, cmd string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.audit = append(g.audit, AuditEntry{TS: time.Now().Unix(), CallID: callID, Tool: tool, Action: action, Reason: reason, Command: cmd})
	if len(g.audit) > 2000 {
		g.audit = g.audit[len(g.audit)-2000:]
	}
}

// Audit returns a snapshot of recent gated calls (most recent last).
func (g *Guard) Audit() []AuditEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]AuditEntry, len(g.audit))
	copy(out, g.audit)
	return out
}

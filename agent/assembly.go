package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"runtime/debug"
	"time"

	"github.com/Autumn-27/artex/metrics"
	"github.com/Autumn-27/artex/stageerr"
	actool "github.com/Autumn-27/norma/tool"
)

// DeferredInfo carries the deferred-tools wiring an agent needs to build its
// Options: the MCP tool names whose schemas are withheld, the subset listed in the
// global system-prompt block (non-skill-gated), and the shared session unlock set.
// UnlockSkill unlocks a named skill's MCPs — hosts call it to rebuild the unlock set
// from history on a resumed session (design doc C2).
type DeferredInfo struct {
	FindingGuidance string            // derived from the final permitted tools, including DB overrides
	Deferred        []string          // all MCP tool names (schema withheld)
	GlobalNames     []string          // MCP names to list in the system-prompt block
	Unlock          *actool.UnlockSet // shared call-gate; nil when no MCP tools
	UnlockSkill     func(skillName string)
}

// ToolAugment, if set, returns the EXTRA tools an agent should see beyond its
// built-in base set — the agent's visible skills (packed into one Skill meta-tool)
// and visible MCP servers (expanded to mcp__server__tool). It also returns the
// DeferredInfo describing how those MCP tools are deferred/gated. The server wires
// it to the PG agent_visibility table. cleanup releases any spawned MCP clients.
//
// When nil, agents run with only their built-in tools — behavior is unchanged
// until a user assigns a skill/MCP to the agent in the UI.
var ToolAugment func(ctx context.Context, agentKey string) (extra []actool.CoreTool, def DeferredInfo, cleanup func())

// AugmentTools returns base plus the agent's visible skill/MCP tools, the
// DeferredInfo, and a cleanup func the caller must defer (closes MCP clients).
// Built-in base tools are kept as-is — never filtered (内置工具留代码层，不做可见性过滤).
func AugmentTools(ctx context.Context, agentKey string, base []actool.CoreTool) ([]actool.CoreTool, DeferredInfo, func()) {
	var (
		def     DeferredInfo
		cleanup = func() {}
		out     = base
	)
	if ToolAugment != nil {
		var extra []actool.CoreTool
		var cl func()
		extra, def, cl = ToolAugment(ctx, agentKey)
		if cl != nil {
			cleanup = cl
		}
		if len(extra) > 0 {
			out = append(append([]actool.CoreTool{}, base...), extra...)
		}
	}
	// DB tools table has the final say on the built-in tools: drop the ones this
	// agent isn't bound to (or that are disabled) and swap in overridden
	// descriptions/schemas + default injection. MCP/skill/host tools have no row
	// and pass through untouched, so deferred/unlock wiring stays consistent.
	if ToolResolve != nil {
		out = ToolResolve(ctx, agentKey, out)
	}
	out, def.FindingGuidance = findingWorkflowTools(agentKey, out)
	for i, t := range out {
		out[i] = guardPanic(t)
	}
	return out, def, cleanup
}

// guardPanic turns a panicking tool handler into an ordinary tool error. The
// harness runs each tool on its own goroutine, so a panic inside a handler can't
// be recovered by the caller that started the run — it takes the whole process
// down, and on restart the agent replays the same call and crashes again. Applied
// last, so it covers every tool the agent can reach: domain, SDK, MCP and skill.
//
// 这一层同时是平台对工具执行的统一边界（批次 5 / #1–3、#14）：
//   - 调用/错误计数（按工具名 + 阶段）进 metrics；
//   - 入参结构防御：畸形 JSON / 非对象入参在这里被换成可读中文错误；
//   - 兜底超时：无 deadline 的运行给一个宽松上限，防工具把整条运行钉死。
func guardPanic(t actool.CoreTool) actool.CoreTool { return &guardedTool{CoreTool: t} }

type guardedTool struct{ actool.CoreTool }

// toolFallbackTimeout 是无 deadline 运行（没有墙钟预算的对话轮/规划轮）下单个工具
// 调用的兜底上限。多数工具自身另有更短的超时（Bash 默认 120s、上限 600s；自定义
// command/script 120s；http 30s），这里只防「既不认 ctx 超时也永不返回」的工具把
// 整条运行挂死。取宽松值，避免误杀合法的长任务。
const toolFallbackTimeout = 30 * time.Minute

// maxToolOutputChars 是单条工具输出进入对话历史的字符上限（映射到 agentcore 的
// MaxToolOutputChars；SDK 默认 30000，我们收紧到 12000）。为什么敢收紧：超限部分
// 不会丢——SDK 把全文落盘到 run 的 ToolOutputDir（cmd-output/），历史里只留头部
// 12k + 落盘指针，需要时 agent 可用 Read/Bash 重读全文；而每轮对话都会把历史
// 里的工具输出重新发给模型，30000 字符的旧输出等于每轮多烧约 8k token。12k 头部
// 足以判断绝大多数命令结果，仅扫描器全量列表这类大输出会被截断换指针。
const maxToolOutputChars = 12000

func (g *guardedTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (res actool.Result, err error) {
	name := g.Name()
	metrics.ObserveToolCall(name)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[tools] %s panic: %v\n%s", name, r, debug.Stack())
			metrics.ObserveToolError(name, metrics.StageExec)
			res, err = actool.Errorf(stageerr.Msgf(stageerr.StageToolExec,
				"工具 %s 内部错误：%v（本次调用已失败，可换个参数或改用别的工具）", name, r).Error()), nil
		}
	}()
	if msg, stage := toolInputError(in, g.InputSchema()); msg != "" {
		metrics.ObserveToolError(name, stage)
		return actool.Errorf(msg), nil
	}
	callCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, toolFallbackTimeout)
		defer cancel()
	}
	res, err = g.CoreTool.Call(callCtx, in, tc)
	if err != nil || res.IsError {
		stage := metrics.StageExec
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			stage = metrics.StageTimeout
		}
		metrics.ObserveToolError(name, stage)
	}
	return res, err
}

// toolInputError 是协议防御的第一道闸（#14）。SDK 在分发前已按 schema 校验过一次，
// 但声明 AcceptsRawInput 的工具（自解析参数）会跳过该校验；这里补与 schema 无关的
// 结构校验：空入参放过；非法 JSON → stage=parse；schema 声明 object 而实参不是对象
// → stage=validate。返回空串表示通过。错误文案面向模型，强调「怎么改」。
func toolInputError(in json.RawMessage, schema map[string]any) (msg, stage string) {
	trimmed := bytes.TrimSpace(in)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", ""
	}
	if !json.Valid(trimmed) {
		return "参数不是合法 JSON（stage=tool_parse）：无法解析本次入参。请按工具的参数说明重新组织后提交，不要发送截断或带注释的 JSON。", metrics.StageParse
	}
	if typ, _ := schema["type"].(string); typ == "object" {
		var obj map[string]json.RawMessage
		if json.Unmarshal(trimmed, &obj) != nil {
			return "参数必须是 JSON 对象（stage=tool_validate）：当前入参不是键值对结构。请检查工具参数说明后重试。", metrics.StageValidate
		}
	}
	return "", ""
}

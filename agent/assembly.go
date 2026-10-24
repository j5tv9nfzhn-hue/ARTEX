package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/metrics"
	"github.com/Autumn-27/artex/stageerr"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

// DeferredInfo carries the deferred-tools wiring an agent needs to build its
// Options: the MCP tool names whose schemas are withheld, the subset listed in the
// global system-prompt block (non-skill-gated), and the shared session unlock set.
// UnlockSkill unlocks a named skill's MCPs — hosts call it to rebuild the unlock set
// from history on a resumed session (design doc C2).
type DeferredInfo struct {
	FindingGuidance string            // derived from the final permitted tools, including DB overrides
	Deferred        []string          // MCP/custom tool names whose schemas are withheld
	GlobalNames     []string          // MCP names to list in the system-prompt block
	MCPCatalog      string            // structured Chinese descriptions + parameter hints for active global MCP tools
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
	// 【Reviewer 硬闸门】Reviewer 是纯裁决角色：单次 LLM 调用、无 harness 循环、
	// 不持有任何工具。它在运行时的身份只由 agentKey 决定（见 IsReviewerKey），
	// 与 agents.role 无关。这里做兜底拦截——即便将来有代码路径误把 reviewer 送进
	// 工具装配，它拿到的也是空工具面，绝不可能拿到 Worker 的执行工具。
	if IsReviewerKey(agentKey) {
		log.Printf("[tools] %s 是裁决角色，不参与工具面装配，返回空工具集", agentKey)
		return nil, DeferredInfo{}, func() {}
	}
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
	// 工具面瘦身放在最后一步（ToolResolve 之后、guardPanic 之前）：
	//   - 之后：库里人工改过的说明也一视同仁地被瘦身，否则「代码默认被压、人工改的
	//     不压」会让体积随谁改过而漂移；
	//   - 之前：guardPanic 仍是最外层，入参纠偏/超时/计数这些边界行为不变。
	// 瘦身只改 Description/InputSchema，并在末尾追加常驻的 get_tool_help 兜底。
	// 能力路由放在瘦身之前：路由要读完整的 Description 才能判准，瘦身把说明
	// 截到 72 字之后再做匹配，等于拿残句去猜语义。
	if intent := routeIntentFrom(ctx); intent != "" {
		out = routeToolFace(intent, out, routeAlwaysKeep(def))
		def = pruneDeferredInfo(out, def)
	}
	if leanEnabled() {
		out = leanToolList(out)
	}
	lb := newLoopBreaker()
	for i, t := range out {
		// 最内层先套「按资源键串行」：工具因此不再需要全局独占——同一个文件、同一张
		// 图、同一个 MCP 服务之外的调用可以真并行（见 tools_concurrency.go）。
		out[i] = guardPanicWithBreaker(lockResources(t), lb)
	}
	return out, def, cleanup
}

// routeAlwaysKeep 返回动态路由不得摘掉的 deferred 工具名。DeferredTools 的
// schema 由 SDK 扣住并经 Search/ExecuteExtraTool 惰性加载；若路由把 CoreTool
// 本体删掉，DeferredInfo、全局速查表与 Skill 解锁都会指向不存在的工具。
// 存在 skill-gated（deferred 但不在 GlobalNames）的工具时连 Skill 一起保住，
// 否则模型连解锁入口都没有。
func routeAlwaysKeep(def DeferredInfo) map[string]bool {
	if len(def.Deferred) == 0 {
		return nil
	}
	global := make(map[string]bool, len(def.GlobalNames))
	for _, name := range def.GlobalNames {
		global[name] = true
	}
	keep := make(map[string]bool, len(def.Deferred)+1)
	gated := false
	for _, name := range def.Deferred {
		keep[name] = true
		if !global[name] {
			gated = true
		}
	}
	if gated {
		keep["Skill"] = true
	}
	return keep
}

// pruneDeferredInfo 把 DeferredInfo 收敛到最终工具面：路由摘掉的原生 MCP 工具
// 不得继续出现在「已挂载可直调」的名单里。MCPCatalog 是上游按全量 GlobalNames
// 预拼的一整段文本，名单有删减时无法逐行可靠裁剪，宁可整段不发（工具名仍在
// available-deferred-tools 块中，参数结构可用 SearchExtraTools 按需取）。
func pruneDeferredInfo(tools []actool.CoreTool, def DeferredInfo) DeferredInfo {
	present := make(map[string]bool, len(tools))
	for _, t := range tools {
		present[t.Name()] = true
	}
	filter := func(names []string) []string {
		out := make([]string, 0, len(names))
		for _, name := range names {
			if present[name] {
				out = append(out, name)
			}
		}
		return out
	}
	def.Deferred = filter(def.Deferred)
	globals := filter(def.GlobalNames)
	if len(globals) != len(def.GlobalNames) {
		def.MCPCatalog = ""
	}
	def.GlobalNames = globals
	return def
}

// guardPanic turns a panicking tool handler into an ordinary tool error. The
// harness runs each tool on its own goroutine, so a panic inside a handler can't
// be recovered by the caller that started the run — it takes the whole process
// down, and on restart the agent replays the same call and crashes again. Applied
// last, so it covers every tool the agent can reach: domain, SDK, MCP and skill.
//
// 这一层同时是平台对工具执行的统一边界（批次 5 / #1–3、#14）：
//   - 调用/错误计数（按工具名 + 阶段）进 metrics；
//   - 入参结构防御与智能纠偏：畸形 JSON / 非对象入参在这里被换成可读中文错误，
//     常见类型错配（字符串整数/布尔值、误包一层 params/arguments）自动修复；
//   - 兜底超时：无 deadline 的运行给一个宽松上限，防工具把整条运行钉死。
func guardPanic(t actool.CoreTool) actool.CoreTool { return &guardedTool{CoreTool: t} }

func guardPanicWithBreaker(t actool.CoreTool, lb *loopBreaker) actool.CoreTool {
	if g, ok := t.(*guardedTool); ok {
		// Reusing a tool must not reuse or mutate another run's breaker state.
		return &guardedTool{CoreTool: g.CoreTool, breaker: lb}
	}
	return &guardedTool{CoreTool: t, breaker: lb}
}

type guardedTool struct {
	actool.CoreTool
	breaker *loopBreaker
}

// AcceptsRawInput opts guardedTool into handling its own pre-call input
// normalization and schema validation so stringified numbers/booleans or
// accidental {"params":{...}} wrappers are repaired before validation.
func (g *guardedTool) AcceptsRawInput() bool { return true }

type rawInputAware interface{ AcceptsRawInput() bool }

func innerAcceptsRawInput(t actool.CoreTool) bool {
	if r, ok := t.(rawInputAware); ok {
		return r.AcceptsRawInput()
	}
	return false
}

// toolFallbackTimeout 是无 deadline 运行（没有墙钟预算的对话轮/规划轮）下单个工具
// 调用的兜底上限。多数工具自身另有更短的超时（Bash 默认 120s、上限 600s；自定义
// command/script 120s；http 30s），这里只防「既不认 ctx 超时也永不返回」的工具把
// 整条运行挂死。取宽松值，避免误杀合法的长任务。
const toolFallbackTimeout = 30 * time.Minute

// defaultMaxToolOutputChars 是「窗口未知」时单条工具输出进入对话历史的字符上限
// （映射到 agentcore 的 MaxToolOutputChars）。
//
// 第三阶段起运行期一律由 ToolOutputChars(window) 推导（窗口 1/8，夹 4K–24K：
// 128K 窗口 → 16000）；这个常量只剩兜底与 SDK 默认值(30000)之间的夹角作用。
const defaultMaxToolOutputChars = 12000

// maxToolOutputChars 是旧名，保留给仍在注释/测试里引用的地方。
const maxToolOutputChars = defaultMaxToolOutputChars

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
	schema := g.InputSchema()
	if msg, stage := toolInputError(in, schema); msg != "" {
		metrics.ObserveToolError(name, stage)
		return actool.Errorf(msg), nil
	}
	in = normalizeToolInput(name, in, schema)
	if name != "Skill" && !innerAcceptsRawInput(g.CoreTool) && len(schema) > 0 {
		if verr := actool.ValidateInput(schema, in); verr != nil {
			metrics.ObserveToolError(name, metrics.StageValidate)
			return actool.Errorf(inputErrorHint(name, schema, in, verr)), nil
		}
	}
	callCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, toolFallbackTimeout)
		defer cancel()
	}
	release, blocked := g.breaker.before(callCtx, name, in)
	if blocked != nil {
		metrics.ObserveToolError(name, metrics.StageExec)
		return *blocked, nil
	}
	defer release()
	// 单意图工具额度闸门（任务 27）：额度用尽后调用**不进入工具实现**，不是提示、
	// 不是靠模型自觉。被拒绝的调用计 error_class=quota，用来回头调阈值。
	budget := intentBudgetFrom(callCtx)
	allow, budgetExtra := budget.beforeCall()
	if !allow {
		metrics.ObserveToolError(name, metrics.StageExec)
		return actool.Result{
			IsError: true,
			Content: []llm.ContentBlock{llm.TextBlock(quotaRefusalText)},
			Extra:   budgetExtra,
		}, nil
	}
	res, err = g.CoreTool.Call(callCtx, in, tc)
	// 统一输出裁剪（第三阶段）：head 70% / tail 30% + 全量落盘。
	// 只处理「工具自己没裁过」的长文本——SDK 的 tool.Capture、MCP 与自定义工具
	// 可能已经裁过，重复裁剪会把落盘指针切掉。
	res = clippedResult(tc, res)
	if err != nil || res.IsError {
		// 失败按「记在谁头上」分档（timeout / transport / exec，见 tool_failure_class.go）：
		// 网络与超时不该算进工具自身的错误率。
		metrics.ObserveToolError(name, classifyToolFailure(res, err, callCtx))
	}
	if g.breaker != nil {
		res = g.breaker.inspect(callCtx, name, in, res, err)
	}
	// 软提醒 / 强制收尾指令随本次 tool_result 一起进入对话流。
	if len(budgetExtra) > 0 {
		res.Extra = append(res.Extra, budgetExtra...)
	}
	return res, err
}

// toolInputError 是协议防御的第一道闸（#14）。
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

// normalizeToolInput repairs common non-semantic LLM argument mistakes before
// schema validation and execution:
//  1. Unwraps accidental {"params":{...}} or {"arguments":{...}} envelopes when
//     the tool schema itself does not declare a "params"/"arguments" property.
//  2. Maps common field aliases when a required field is missing (path <-> file_path,
//     q <-> query, cmd -> command).
//  3. Coerces stringified integers/numbers/booleans ("10" -> 10, "true" -> true)
//     and single strings for string-array fields to match the declared schema.
func normalizeToolInput(toolName string, in json.RawMessage, schema map[string]any) json.RawMessage {
	if toolName == "Skill" || len(schema) == 0 {
		return in
	}
	trimmed := bytes.TrimSpace(in)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return in
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		return in
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return in
	}
	changed := false

	// 1. Unwrap accidental {"params":{...}} or {"arguments":{...}} envelopes.
	for _, wrapKey := range []string{"params", "arguments"} {
		if _, declared := props[wrapKey]; declared {
			continue
		}
		rawWrap, exists := obj[wrapKey]
		if !exists {
			continue
		}
		var inner map[string]json.RawMessage
		if json.Unmarshal(rawWrap, &inner) == nil && len(inner) > 0 {
			for k, v := range inner {
				if _, already := obj[k]; !already {
					obj[k] = v
				}
			}
			delete(obj, wrapKey)
			changed = true
		}
	}

	// 2. Common alias repair for missing required properties.
	aliasPairs := [][2]string{
		{"file_path", "path"},
		{"file_path", "filepath"},
		{"path", "file_path"},
		{"path", "filepath"},
		{"query", "q"},
		{"q", "query"},
		{"command", "cmd"},
	}
	for _, pair := range aliasPairs {
		canon, alt := pair[0], pair[1]
		if _, canonDeclared := props[canon]; !canonDeclared {
			continue
		}
		if _, altDeclared := props[alt]; altDeclared {
			continue
		}
		if _, hasCanon := obj[canon]; !hasCanon {
			if altVal, hasAlt := obj[alt]; hasAlt && len(altVal) > 0 && string(altVal) != "null" {
				obj[canon] = altVal
				delete(obj, alt)
				changed = true
			}
		}
	}

	// 2b. mode 型工具漏传 mode 时，按已给出的判别字段反推唯一 mode（steps→chain 等）。
	//     只在唯一可判定时生效，并写日志——不做语义猜测，也不替模型选它没表达的模式。
	if inferred, ok := inferModeField(toolName, obj); ok {
		if b, err := json.Marshal(inferred); err == nil {
			obj["mode"] = b
			changed = true
			log.Printf("[tools] %s 未传 mode，已按入参反推 mode=%s", toolName, inferred)
		}
	}

	// 3. Scalar type coercion based on declared schema property types.
	for k, rawVal := range obj {
		pRaw, ok := props[k]
		if !ok || len(rawVal) == 0 || string(rawVal) == "null" {
			continue
		}
		pm, ok := pRaw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := pm["type"].(string)
		switch typ {
		case "integer":
			var s string
			if json.Unmarshal(rawVal, &s) == nil {
				if iv, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
					if b, err := json.Marshal(iv); err == nil {
						obj[k] = b
						changed = true
					}
				}
			}
		case "number":
			var s string
			if json.Unmarshal(rawVal, &s) == nil {
				if fv, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
					if b, err := json.Marshal(fv); err == nil {
						obj[k] = b
						changed = true
					}
				}
			}
		case "boolean":
			var s string
			if json.Unmarshal(rawVal, &s) == nil {
				switch strings.ToLower(strings.TrimSpace(s)) {
				case "true", "1", "yes":
					obj[k] = json.RawMessage("true")
					changed = true
				case "false", "0", "no":
					obj[k] = json.RawMessage("false")
					changed = true
				}
			}
		case "string":
			var s string
			if json.Unmarshal(rawVal, &s) != nil {
				// Coerce numeric or boolean scalar into string if property expects string
				var num json.Number
				dec := json.NewDecoder(bytes.NewReader(rawVal))
				dec.UseNumber()
				if err := dec.Decode(&num); err == nil {
					if b, err := json.Marshal(num.String()); err == nil {
						obj[k] = b
						changed = true
					}
				} else {
					var bv bool
					if json.Unmarshal(rawVal, &bv) == nil {
						if b, err := json.Marshal(strconv.FormatBool(bv)); err == nil {
							obj[k] = b
							changed = true
						}
					}
				}
			}
		case "array":
			if items, ok := pm["items"].(map[string]any); ok && items["type"] == "string" {
				var s string
				if json.Unmarshal(rawVal, &s) == nil {
					if b, err := json.Marshal([]string{s}); err == nil {
						obj[k] = b
						changed = true
					}
				}
			}
		}
	}

	if !changed {
		return in
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return in
	}
	return out
}

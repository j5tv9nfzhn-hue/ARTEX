package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/traffic"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/skill"
	actool "github.com/Autumn-27/norma/tool"
)

// maxNativeMCPToolSchemas is the maximum number of directly-visible MCP tools whose
// full JSON Schemas are exposed directly in the model's native tools list (in addition
// to being callable via ExecuteExtraTool). Keeping directly-visible MCP tools native
// up to this threshold allows models to call mcp__<Server>__<Tool> directly on Turn 1
// without an extra discovery round-trip.
const maxNativeMCPToolSchemas = 40

// maxCompanionRefBytes caps the total bytes of references/*.md auto-injected when
// a multi-file skill package is invoked via Skill({"name":"..."}).
const maxCompanionRefBytes = 16384

// wireAgentAugment connects the PG agent_visibility table into the agent runtime:
// an agent's visible skills are loaded from the filesystem and packed into one
// Skill meta-tool; its visible MCP servers are connected and expanded to
// mcp__server__tool. skillDir is the root directory of all skill subdirectories.
func wireAgentAugment(pg *db.DB, skillDir string, hostTools func() ([]actool.CoreTool, map[string][]string)) {
	agent.ToolAugment = func(ctx context.Context, agentKey string) ([]actool.CoreTool, agent.DeferredInfo, func()) {
		a, err := pg.GetAgentByKey(agentKey)
		if err != nil || a == nil {
			return nil, agent.DeferredInfo{}, nil
		}
		runInfo := agent.RunInfoFrom(ctx)
		var extra []actool.CoreTool

		// --- skills: load visible skills into reg (used for the Skill meta-tool
		// and to know which MCP servers are skill-gated). Skills with zero explicit
		// agent_visibility bindings on disk are also visible by default to core agents. ---
		var reg *skill.Registry
		if allReg, err := skill.LoadDir(skillDir); err == nil && allReg != nil {
			names, _ := pg.AgentSkillNames(a.ID)
			nameSet := make(map[string]bool, len(names))
			for _, n := range names {
				nameSet[n] = true
			}
			reg = skill.NewRegistry()
			for _, s := range allReg.List() {
				if s.Dir == "" {
					continue
				}
				dirName := filepath.Base(s.Dir)
				visible := nameSet[dirName]
				if !visible && isDefaultMCPAgent(a.Key) {
					if sa, serr := pg.SkillAgents(dirName); serr == nil && len(sa) == 0 {
						visible = true
					}
				}
				if visible {
					reg.Add(s)
				}
			}
			if len(reg.List()) == 0 {
				reg = nil
			}
		}
		// A server named by any visible skill's `mcps:` is skill-gated: its tools
		// are deferred + locked (not in the global block) until that skill loads.
		gated := map[string]bool{}
		skillByMCPServer := map[string][]string{}
		if reg != nil {
			for _, s := range reg.List() {
				for _, srv := range s.MCPs {
					gated[srv] = true
					skillByMCPServer[srv] = append(skillByMCPServer[srv], s.Name)
				}
			}
		}

		// --- mcp: connect enabled servers that are directly visible to this agent
		// OR skill-gated (named in a visible skill's mcps field). Directly-visible
		// (or enabled with zero explicit agent bindings on a core execution role) →
		// global (unlocked from session start, and exposed natively in tools list
		// up to maxNativeMCPToolSchemas). Strictly skill-gated (!directVisible &&
		// skillGated) → deferred until that skill is invoked.
		var closers []io.Closer
		serverTools := map[string][]string{} // server name → its tool names
		toolSummaries := map[string]string{} // tool name → one-line description + param hint
		mcpToolByName := map[string]actool.CoreTool{}
		var deferredNames, globalNames []string
		globalSet := map[string]bool{}
		directMCPCount := 0
		{
			mcpIDs, _ := pg.AgentVisible(a.ID, "mcp")
			want := idSet(mcpIDs)
			all, _ := pg.ListMCP()
			for _, m := range all {
				// Managed RE providers must not also be opened by per-run generic MCP clients.
				managed, managedErr := pg.REMCPManaged(m.ID)
				if managedErr != nil || managed {
					continue
				}
				if !m.Enabled {
					continue
				}
				directVisible := want[m.ID]
				if !directVisible && isDefaultMCPAgent(a.Key) {
					if ra, rerr := pg.ResourceAgents("mcp", m.ID); rerr == nil && len(ra) == 0 {
						directVisible = true
					}
				}
				skillGated := gated[m.Name]
				if !directVisible && !skillGated {
					continue // neither directly visible nor referenced by a visible skill
				}
				cl, err := connectMCP(ctx, m)
				if err != nil {
					log.Printf("[mcp] %s 连接失败: %v", m.Name, err)
					continue
				}
				closers = append(closers, cl)
				ts, err := cl.Tools(ctx)
				if err != nil {
					log.Printf("[mcp] %s tools/list 失败: %v", m.Name, err)
					continue
				}
				if len(m.Tools) == 0 && len(ts) > 0 {
					cached := make([]db.MCPTool, 0, len(ts))
					for _, t := range ts {
						cached = append(cached, db.MCPTool{Name: t.Name(), Description: t.Description()})
					}
					_ = pg.SaveMCPTools(m.ID, cached)
				}
				for _, t := range ts {
					metered := meterTool(t, pg, t.Name(), a.Key, runInfo)
					extra = append(extra, metered)
					mcpToolByName[t.Name()] = metered
					serverTools[m.Name] = append(serverTools[m.Name], t.Name())
					toolSummaries[t.Name()] = formatMCPToolSummary(t)
					if directVisible {
						// directly visible → available and unlocked from session start
						globalNames = append(globalNames, t.Name())
						globalSet[t.Name()] = true
						directMCPCount++
						if directMCPCount > maxNativeMCPToolSchemas {
							deferredNames = append(deferredNames, t.Name())
						}
					} else {
						// strictly skill-gated tools stay out of globalNames and have schemas withheld until unlocked
						deferredNames = append(deferredNames, t.Name())
					}
				}
			}
		}

		// Shared call-gate: global MCP tools are unlocked from the start; skill-gated
		// ones are unlocked when their skill loads (or replayed from history — C2).
		unlock := actool.NewUnlockSet(globalNames...)
		unlockSkill := func(skillName string) {
			if reg == nil {
				return
			}
			skillName = canonicalVisibleSkillName(reg, skillName)
			if s, ok := reg.Get(skillName); ok {
				for _, srv := range s.MCPs {
					unlock.Add(serverTools[srv]...)
				}
			}
		}

		// Skill meta-tool: on load, unlock the skill's MCPs, reveal their names
		// plus compact parameter hints, AND auto-load any companion references/*.md
		// files so multi-file skill packages deliver their pitfalls/bypasses/PoCs in 1 step.
		if reg != nil {
			reg.OnInvoke = func(s skill.Skill) string {
				unlockSkill(s.Name)
				var parts []string
				var reveal []string
				var catalogLines []string
				for _, srv := range s.MCPs {
					for _, n := range serverTools[srv] {
						if !globalSet[n] {
							reveal = append(reveal, n)
						}
						if sum := toolSummaries[n]; sum != "" {
							catalogLines = append(catalogLines, sum)
						}
					}
				}
				if blk := actool.RenderDeferredToolsBlock(reveal); blk != "" {
					parts = append(parts, blk)
				}
				if len(catalogLines) > 0 {
					parts = append(parts, "【本技能关联的 MCP 工具已解锁 · 可直接以工具全名发起调用或通过 ExecuteExtraTool 一步调用】：\n"+strings.Join(catalogLines, "\n"))
				}
				if companion := loadSkillCompanionDocs(s.Dir); companion != "" {
					parts = append(parts, companion)
				}
				return strings.Join(parts, "\n\n")
			}
			extra = append(extra, meterSkillTool(reg.Tool(), pg, reg, a.Key, runInfo))
		}

		// host tools (traffic / orchestration / custom) — added to every agent's base;
		// ToolResolve then keeps them only for agents the tool is bound to. Custom
		// tools flagged deferred contribute their names to the deferred wiring so
		// their schema is withheld (SearchExtraTools/ExecuteExtraTool), same as MCP.
		if hostTools != nil {
			ht, deferredBinds := hostTools()
			extra = append(extra, ht...)
			for name, boundAgents := range deferredBinds {
				if !contains(boundAgents, a.Key) {
					continue // only defer names this agent is actually bound to
				}
				deferredNames = append(deferredNames, name) // schema withheld from the prompt
				globalNames = append(globalNames, name)     // advertised in the deferred block
				globalSet[name] = true
				if unlock != nil {
					unlock.Add(name) // global deferred → callable from the start
				}
			}
		}

		// When MCP tools are mounted and all of them are directly exposed in tools
		// (so deferredNames is empty and agentcore won't auto-register ExecuteExtraTool),
		// register our smart ExecuteExtraTool + SearchExtraTools so models that invoke
		// MCP tools either directly (mcp__Server__tool) OR via ExecuteExtraTool both succeed.
		if len(deferredNames) == 0 && len(mcpToolByName) > 0 {
			extra = append(extra,
				buildSmartSearchExtraTools(mcpToolByName),
				buildSmartExecuteExtraTool(mcpToolByName, unlock, unlockSkill, skillByMCPServer),
			)
		}

		cleanup := func() {
			for _, c := range closers {
				_ = c.Close()
			}
		}
		def := agent.DeferredInfo{
			Deferred:    deferredNames,
			GlobalNames: globalNames,
			MCPCatalog:  buildMCPCatalogBlock(globalNames, toolSummaries),
			Unlock:      unlock,
			UnlockSkill: unlockSkill,
		}
		return extra, def, cleanup
	}
}

// loadSkillCompanionDocs reads companion Markdown/text files under <skillDir>/references
// and lists any scripts under <skillDir>/scripts so invoking a multi-file skill package
// via Skill({"name":"..."}) delivers its full tactical reference material in one call.
func loadSkillCompanionDocs(skillDir string) string {
	if strings.TrimSpace(skillDir) == "" {
		return ""
	}
	var sections []string
	refDir := filepath.Join(skillDir, "references")
	if entries, err := os.ReadDir(refDir); err == nil && len(entries) > 0 {
		budget := maxCompanionRefBytes
		var refBlocks []string
		for _, e := range entries {
			if e.IsDir() || budget <= 0 {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".md" && ext != ".txt" && ext != ".json" {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(refDir, e.Name()))
			if rerr != nil {
				continue
			}
			text := strings.TrimSpace(string(data))
			if text == "" {
				continue
			}
			if len(text) > budget {
				text = text[:budget] + "\n...(truncated)"
			}
			budget -= len(text)
			refBlocks = append(refBlocks, fmt.Sprintf("#### 📄 references/%s\n%s", e.Name(), text))
		}
		if len(refBlocks) > 0 {
			sections = append(sections, "【本技能包配套实战参考手册（已随 Skill 调用自动加载，请直接遵循其中的防踩坑红线、绕过方案与攻击推导链）】：\n\n"+strings.Join(refBlocks, "\n\n"))
		}
	}

	scriptsDir := filepath.Join(skillDir, "scripts")
	if entries, err := os.ReadDir(scriptsDir); err == nil && len(entries) > 0 {
		var scriptNames []string
		for _, e := range entries {
			if !e.IsDir() {
				scriptNames = append(scriptNames, filepath.Join(skillDir, "scripts", e.Name()))
			}
		}
		if len(scriptNames) > 0 {
			sections = append(sections, "【本技能包配套脚本路径】：\n- "+strings.Join(scriptNames, "\n- "))
		}
	}
	return strings.Join(sections, "\n\n")
}

func resolveMCPToolByQuery(tools map[string]actool.CoreTool, query string) (actool.CoreTool, bool) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, false
	}
	if t, ok := tools[q]; ok {
		return t, true
	}
	lower := strings.ToLower(q)
	var names []string
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ln := strings.ToLower(n)
		if ln == lower || strings.HasSuffix(ln, "__"+lower) {
			return tools[n], true
		}
	}
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), lower) {
			return tools[n], true
		}
	}
	return nil, false
}

func buildSmartSearchExtraTools(tools map[string]actool.CoreTool) actool.CoreTool {
	var ordered []string
	for n := range tools {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)
	return actool.Build(actool.Spec{
		Name:        actool.SearchExtraToolsName,
		Description: "检索已挂载的外部 MCP / 扩展工具 Schema。支持 \"select:<工具名>\" 精确查询或关键词搜索。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "\"select:<工具名>\" 或搜索关键词",
				},
			},
			"required": []any{"query"},
		},
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var args struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(in, &args)
			q := strings.TrimSpace(args.Query)
			q = strings.TrimPrefix(q, "select:")
			var matches []actool.CoreTool
			seen := map[string]bool{}
			for _, part := range strings.Split(q, ",") {
				if t, ok := resolveMCPToolByQuery(tools, part); ok && !seen[t.Name()] {
					seen[t.Name()] = true
					matches = append(matches, t)
				}
			}
			if len(matches) == 0 {
				for _, n := range ordered {
					matches = append(matches, tools[n])
				}
			}
			var b strings.Builder
			fmt.Fprintf(&b, "已找到 %d 个可用 MCP 工具（可直接调用工具全名，或通过 %s({\"tool_name\":\"<name>\",\"params\":{...}}) 调用）：\n",
				len(matches), actool.ExecuteExtraToolName)
			for _, t := range matches {
				schemaJSON, _ := json.Marshal(t.InputSchema())
				fmt.Fprintf(&b, "\n## %s\n%s\nparams schema: %s\n", t.Name(), t.Description(), string(schemaJSON))
			}
			return actool.Text(b.String()), nil
		},
	})
}

func buildSmartExecuteExtraTool(
	tools map[string]actool.CoreTool,
	unlock *actool.UnlockSet,
	unlockSkill func(string),
	skillByMCPServer map[string][]string,
) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        actool.ExecuteExtraToolName,
		Description: "一步调用已挂载的外部 MCP / 扩展工具。传入 tool_name（如 mcp__<服务名>__<工具名>）与 params 参数对象即可直接执行。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool_name": map[string]any{
					"type":        "string",
					"description": "要调用的 MCP / 扩展工具全名（如 mcp__Playwright__browser_navigate）",
				},
				"params": map[string]any{
					"type":        "object",
					"description": "传递给目标工具的参数对象",
				},
			},
			"required": []any{"tool_name"},
		},
		RawInput: true,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(in, &raw); err != nil {
				return actool.Errorf("ExecuteExtraTool 参数必须是合法 JSON 对象: " + err.Error()), nil
			}
			toolName := ""
			for _, k := range []string{"tool_name", "name", "tool"} {
				if v, ok := raw[k]; ok {
					_ = json.Unmarshal(v, &toolName)
					if strings.TrimSpace(toolName) != "" {
						break
					}
				}
			}
			toolName = strings.TrimSpace(toolName)
			if toolName == "" {
				return actool.Errorf("ExecuteExtraTool 缺少必填字段 tool_name"), nil
			}
			t, ok := resolveMCPToolByQuery(tools, toolName)
			if !ok {
				return actool.Errorf("未找到 MCP 工具: " + toolName), nil
			}
			if unlock != nil && !unlock.Has(t.Name()) {
				// Auto-unlock if the gating skill is visible to this agent
				if srv := extractMCPServerFromTool(t.Name()); srv != "" && unlockSkill != nil {
					for _, sk := range skillByMCPServer[srv] {
						unlockSkill(sk)
					}
				}
				if !unlock.Has(t.Name()) {
					return actool.Errorf("工具 " + t.Name() + " 尚未解锁，请先调用对应的 Skill 加载关联技能"), nil
				}
			}
			var params json.RawMessage
			for _, pk := range []string{"params", "arguments", "args", "input"} {
				if v, exists := raw[pk]; exists && len(v) > 0 && string(v) != "null" {
					params = v
					break
				}
			}
			if len(params) == 0 {
				// Collect any remaining top-level keys as params
				rest := map[string]json.RawMessage{}
				for k, v := range raw {
					if k != "tool_name" && k != "name" && k != "tool" {
						rest[k] = v
					}
				}
				if len(rest) > 0 {
					params, _ = json.Marshal(rest)
				} else {
					params = json.RawMessage("{}")
				}
			}
			return t.Call(ctx, params, tc)
		},
	})
}

func extractMCPServerFromTool(toolName string) string {
	if !strings.HasPrefix(toolName, "mcp__") {
		return ""
	}
	rest := strings.TrimPrefix(toolName, "mcp__")
	if idx := strings.Index(rest, "__"); idx > 0 {
		return rest[:idx]
	}
	return ""
}

func isDefaultMCPAgent(agentKey string) bool {
	switch agentKey {
	case "worker", "pentest", "auto", "mainagent", "planner":
		return true
	default:
		return false
	}
}

func formatMCPToolSummary(t actool.CoreTool) string {
	desc := strings.TrimSpace(strings.ReplaceAll(t.Description(), "\n", " "))
	if len(desc) > 180 {
		desc = desc[:180] + "…"
	}
	schema := t.InputSchema()
	var reqParams, optParams []string
	reqSet := map[string]bool{}
	if reqRaw, ok := schema["required"].([]any); ok {
		for _, r := range reqRaw {
			if s, ok := r.(string); ok {
				reqSet[s] = true
			}
		}
	} else if reqStrs, ok := schema["required"].([]string); ok {
		for _, s := range reqStrs {
			reqSet[s] = true
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		var keys []string
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			typ := ""
			if pm, ok := props[k].(map[string]any); ok {
				if ts, ok := pm["type"].(string); ok && ts != "" {
					typ = ":" + ts
				}
			}
			if reqSet[k] {
				reqParams = append(reqParams, k+"*"+typ)
			} else {
				optParams = append(optParams, k+typ)
			}
		}
	}
	paramsHint := ""
	allP := append(reqParams, optParams...)
	if len(allP) > 0 {
		paramsHint = " | 参数(*必填): " + strings.Join(allP, ", ")
	}
	if desc == "" {
		desc = "MCP 扩展工具"
	}
	return "- `" + t.Name() + "`: " + desc + paramsHint
}

func buildMCPCatalogBlock(globalNames []string, summaries map[string]string) string {
	var lines []string
	for _, n := range globalNames {
		if s := summaries[n]; s != "" {
			lines = append(lines, s)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "【已挂载 MCP 扩展工具速查与主动调用规约】\n" +
		"当前会话已挂载并解锁以下外部 MCP 能力工具。当任务涉及对应场景（如驱动无头浏览器渲染 SPA/提取登录态/验证 XSS、查询或操作 ScopeSentry 等外部扫描平台、执行逆向分析或调用自定义安全工具）时，**请优先主动调用对应的 MCP 工具**：\n" +
		"1. 【直接调用或一步直调（推荐）】若下方列出的参数已明确，无需先调 SearchExtraTools，可直接调用工具全名 `mcp__<服务名>__<工具名>({...})` 或调用 `ExecuteExtraTool({\"tool_name\":\"<下方工具全名>\",\"params\":{...}})` 一步执行！\n" +
		"2. 【按需查 Schema】仅当某复杂对象参数结构不明确时，先调 `SearchExtraTools({\"query\":\"select:<工具全名>\"})` 获取完整 JSON Schema，再调用。\n" +
		strings.Join(lines, "\n")
}

func idSet(ids []int64) map[int64]bool {
	m := make(map[int64]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// seedPrompts writes each built-in agent's code-default prompt body into
// agent_prompts on startup — first-insert only (SeedPromptIfEmpty is a no-op once
// any version exists), so the DB becomes the authoritative editable source while
// user edits survive restarts. Runs after seedBuiltins has created the agent rows.
func seedPrompts(pg *db.DB) {
	for key, tmpl := range agent.BuiltinPromptSeeds() {
		a, err := pg.GetAgentByKey(key)
		if err != nil || a == nil {
			log.Printf("[prompts] seed %s 跳过: agent 不存在 (%v)", key, err)
			continue
		}
		if err := pg.SeedPromptIfEmpty(a.ID, tmpl); err != nil {
			log.Printf("[prompts] seed %s 失败: %v", key, err)
		}
	}
}

// wireTools seeds the built-in tool catalog (idempotent, first-insert only so page
// edits survive restart) and wires the DB tools table into the agent runtime: at
// tool-assembly time each built-in tool is filtered by its agent binding / enabled
// flag and, if kept, wrapped so the model sees the DB-overridden description/schema
// and 缺省入参 get injected. MCP/skill/host tools have no row and pass through.
func wireTools(pg *db.DB, domainReg map[string]actool.CoreTool) {
	agent.FindingTrafficBindingEnabled = func() bool { return pg.GetBool(settingAgentTrafficBinding, false) }
	for _, s := range agent.BuiltinToolSeeds() {
		schema, _ := json.Marshal(s.Schema)
		agents, _ := json.Marshal(s.Agents)
		if err := pg.SeedTool(s.Key, s.Desc, schema, agents); err != nil {
			log.Printf("[tools] seed %s 失败: %v", s.Key, err)
		}
	}
	trafficAgents, _ := json.Marshal([]string{"worker"})
	for _, t := range traffic.SeedToolMetas() {
		schema, _ := json.Marshal(t.InputSchema())
		if err := pg.SeedTool(t.Name(), t.Description(), schema, trafficAgents); err != nil {
			log.Printf("[tools] seed %s 失败: %v", t.Name(), err)
		}
	}
	const bashInteractiveShellNote = "\n\n需要【交互输入】的程序（msfconsole / ssh 交互登录 / mysql、psql、python 等 REPL / 密码或 yes/no 提示 / nc 反弹 shell）不要用 Bash（它没有 stdin、会卡住），改用 shell_open 开交互会话（用完 shell_close）。一次性、非交互命令仍用 Bash。"
	agent.ToolResolve = func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool {
		rows, err := pg.ListTools()
		if err != nil {
			log.Printf("[tools] 读取工具表失败，按代码默认放行: %v", err)
			return tools
		}
		byKey := make(map[string]*db.Tool, len(rows))
		for _, t := range rows {
			byKey[t.Key] = t
		}
		runInfo := agent.RunInfoFrom(ctx)
		resolve := func(t actool.CoreTool, row *db.Tool) actool.CoreTool {
			var schema map[string]any
			if len(row.Schema) > 0 {
				_ = json.Unmarshal(row.Schema, &schema)
			}
			return meterTool(agent.DecorateTool(t, row.Description, schema), pg, row.Key, agentKey, runInfo)
		}
		out := tools[:0:0]
		for _, t := range tools {
			row, known := byKey[t.Name()]
			if !known { // SDK/meta tool: no tools row → visibility & defaults untouched
				// 但仍必须计量。此前这里直接 append 原样放行，导致 Bash / Write /
				// Read / Edit / Skill / ExecuteExtraTool 这些**真正在干活**的工具
				// 完全没有 tool_usage 记录：某安装 20,521 次调用里只有 50 个 key，
				// 脚本与文件读写全被计成 0，「模型到底靠什么在干活」这个问题
				// 从数据上无法回答，优化也就无从下手。
				// MCP 工具已在上面按服务逐个 meterTool 过，这里跳过以免重复计数。
				if strings.HasPrefix(t.Name(), "mcp__") {
					out = append(out, t)
					continue
				}
				out = append(out, meterTool(t, pg, t.Name(), agentKey, runInfo))
				continue
			}
			if !row.Enabled || !contains(row.Agents, agentKey) {
				continue // disabled globally or not bound to this agent → drop
			}
			out = append(out, resolve(t, row))
		}
		if len(domainReg) > 0 {
			inList := make(map[string]bool, len(tools))
			for _, t := range tools {
				inList[t.Name()] = true
			}
			for _, row := range rows {
				if row.Kind == toolKindShell || !row.Enabled || !contains(row.Agents, agentKey) || inList[row.Key] {
					continue
				}
				inst, ok := domainReg[row.Key]
				if !ok {
					continue
				}
				out = append(out, resolve(inst, row))
			}
		}
		var shellHints []string
		for _, row := range rows {
			if row.Kind == toolKindShell && row.Enabled && contains(row.Agents, agentKey) {
				shellHints = append(shellHints, "- "+row.Key+": "+row.Description)
			}
		}
		if len(shellHints) > 0 {
			note := "\n\n以下工具已安装在此 bash 环境中，可直接通过 Bash 调用：\n" + strings.Join(shellHints, "\n")
			for i, t := range out {
				if t.Name() == "Bash" {
					out[i] = agent.DecorateTool(t, t.Description()+note, t.InputSchema())
					break
				}
			}
		}
		if !actool.InteractiveShellDisabled() {
			if a, err := pg.GetAgentByKey(agentKey); err == nil && a != nil && a.InteractiveShell {
				out = append(out, actool.ShellSessionTools()...)
				for i, t := range out {
					if t.Name() == "Bash" {
						out[i] = agent.DecorateTool(t, t.Description()+bashInteractiveShellNote, t.InputSchema())
						break
					}
				}
			}
		}
		if note := platformShellNote(); note != "" {
			for i, t := range out {
				if t.Name() == "Bash" {
					out[i] = agent.DecorateTool(t, t.Description()+note, t.InputSchema())
					break
				}
			}
		}
		return out
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func buildDomainReg(as *db.AssetStore) map[string]actool.CoreTool {
	if as == nil {
		return nil
	}
	serverTS := agent.NewToolSet(nil, "")
	serverTS.SetAssetStore(as, as.Companies())
	reg := make(map[string]actool.CoreTool)
	for _, t := range serverTS.AllDomainTools() {
		reg[t.Name()] = t
	}
	return reg
}

func jsonStrSlice(raw json.RawMessage) []string {
	var out []string
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func jsonStrMap(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

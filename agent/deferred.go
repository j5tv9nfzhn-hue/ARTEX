package agent

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

// deferredSystem builds an agent's system-prompt segments and cache boundary from
// its DeferredInfo. When globally-available MCP tools are present, their names +
// proactive invocation guidance render into an <available-deferred-tools> block
// placed as the LAST system-prompt segment, with DynamicBoundary set so the whole
// (session-fixed) system prompt — including the block — is cached (design doc
// §2.1 / C1). Skill-gated MCP names are NOT in this block; they surface when their
// skill loads.
func deferredSystem(sysText string, def DeferredInfo) (system []string, boundary int) {
	sysText += def.FindingGuidance
	block := renderActiveMCPBlock(def.GlobalNames)
	if block == "" {
		system = []string{sysText}
		boundary = len(system) // b >= len → 整段 system 一个缓存块
		return system, boundary
	}
	if def.MCPCatalog != "" {
		block += "\n\n" + def.MCPCatalog
	}
	system = []string{sysText, block}
	boundary = len(system) // b >= len → whole system prompt cached (SDK guard)
	return system, boundary
}

func renderActiveMCPBlock(names []string) string {
	if len(names) == 0 {
		return ""
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString("<available-deferred-tools>\n")
	for _, n := range sorted {
		b.WriteString(n)
		b.WriteByte('\n')
	}
	b.WriteString("</available-deferred-tools>\n")
	b.WriteString("IMPORTANT / 主动调用规约：上方列出的外部 MCP / 扩展工具已在当前会话中挂载并解锁就绪。当任务涉及对应能力时，请主动优先调用：\n")
	b.WriteString("1. 直接调用（推荐）：若工具已在当前工具列表中，直接以工具全名（如 mcp__<服务名>__<工具名>）发起调用；\n")
	b.WriteString("2. 通过 " + actool.ExecuteExtraToolName + " 一步直调：调用 " + actool.ExecuteExtraToolName + "({\"tool_name\":\"<工具全名>\",\"params\":{...}}) 一步执行（仅当复杂参数结构不明确时，才先调 " + actool.SearchExtraToolsName + " 查询 Schema）。")
	return b.String()
}

// seedUnlockFromHistory restores only successfully completed Skill calls.
// Failed, pending, unpaired calls never grant skill-gated MCP access.
func seedUnlockFromHistory(msgs []llm.Message, unlockSkill func(string)) {
	if unlockSkill == nil {
		return
	}
	pending := map[string]string{}
	for _, m := range msgs {
		for _, block := range m.Content {
			if block.Type == llm.BlockToolUse && block.Name == "Skill" && block.ID != "" {
				var in struct {
					Name   string `json:"name"`
					Skill  string `json:"skill"`
					Legacy string `json:"skill_name"`
				}
				if json.Unmarshal(block.Input, &in) != nil {
					continue
				}
				chosen := ""
				conflict := false
				for _, raw := range []string{in.Name, in.Skill, in.Legacy} {
					n := strings.TrimSpace(raw)
					if n == "" {
						continue
					}
					if chosen != "" && CanonicalSkillAlias(chosen) != CanonicalSkillAlias(n) {
						conflict = true
					}
					if chosen == "" {
						chosen = n
					}
				}
				if chosen != "" && !conflict {
					pending[block.ID] = chosen
				}
			}
			if block.Type == llm.BlockToolResult {
				if name, ok := pending[block.ToolUseID]; ok {
					delete(pending, block.ToolUseID)
					if !block.IsError {
						unlockSkill(name)
					}
				}
			}
		}
	}
}

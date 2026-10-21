package agent

import (
	"encoding/json"
	"strings"
)

// 战术知识库的静态部分导出给外部 MCP 服务端（server/mcp_tools_l1.go）。
//
// 只导出「不依赖 ToolSet / 数据库」的那一半：漏洞打法卡片与业务不变量清单。
// 另一半（past_findings / past_assets）由服务端直接查库，避免把 agent 的
// 整个 ToolSet 拖进 MCP 进程。这样同一份知识只有一处定义，不会出现两份。

// KBVulnPatternsJSON 按分类/关键词过滤内置漏洞打法卡片。
// category 为空表示不过滤；query 命中 title/high_yield/fast_probes 任一即可。
func KBVulnPatternsJSON(category, query string, limit int) (string, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	cat := strings.ToLower(strings.TrimSpace(category))
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]vulnPatternCard, 0, len(builtinVulnPatternCards))
	for _, card := range builtinVulnPatternCards {
		if cat != "" && !strings.EqualFold(card.Category, cat) {
			continue
		}
		if q != "" {
			hay := strings.ToLower(card.Category + " " + card.Title + " " + strings.Join(card.HighYield, " ") + " " + strings.Join(card.FastProbes, " "))
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, card)
		if len(out) >= limit {
			break
		}
	}
	payload := map[string]any{"mode": "vuln_patterns", "count": len(out), "items": out}
	if len(out) == 0 {
		payload["hint"] = "没有命中；category 可选值见 vuln_pattern_cards 的全量分类，或去掉 query 重试"
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// KBLogicProfilesJSON 按目标类型/别名过滤业务不变量清单。
func KBLogicProfilesJSON(query string, limit int) (string, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]logicProfile, 0, len(builtinLogicProfiles))
	for _, p := range builtinLogicProfiles {
		if q != "" {
			hay := strings.ToLower(p.Target + " " + strings.Join(p.Aliases, " ") + " " + strings.Join(p.Finger, " "))
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, p)
		if len(out) >= limit {
			break
		}
	}
	payload := map[string]any{"mode": "logic_invariants", "count": len(out), "items": out}
	if len(out) == 0 {
		payload["hint"] = "没有命中；当前内置业务类型见 items 为空时的 logic_profiles 列表"
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

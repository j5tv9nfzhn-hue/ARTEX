package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcpserver"
)

// L1 = 知识库只读：把 ARTEX 已经攒下的资产图、发现、公司、任务暴露给外部
// harness。全部只读，不改一行数据；这也是「不重复扫描已知目标」的前提。

// mcpL1Tools 返回 L1 六个工具。
func (s *Server) mcpL1Tools() []mcpserver.Tool {
	if s.m == nil || s.m.pg == nil {
		return nil
	}
	return []mcpserver.Tool{
		{
			Name:        "list_assets",
			Title:       "资产查询",
			Description: "查资产图。给 query 做关键词检索；给 type 按类型列举（root_domain/ip/subdomain/app/service/endpoint）；给 company_id 或 task_id 按归属过滤；都不给则返回各类型数量概览。",
			Tier:        mcpserver.L1,
			ReadOnly:    true,
			Schema: map[string]any{
				"type":       "object",
				"properties": mcpProps(map[string]string{"query": "关键词（域名、IP、URL、指纹）", "type": "资产类型", "company_id": "公司 id", "task_id": "任务 id"}),
			},
			Handler: s.mcpListAssets,
		},
		{
			Name:        "list_findings",
			Title:       "漏洞查询",
			Description: "分页查历史发现。可按 task_id / severity / status / vuln_class / query 过滤，返回总数与条目（含摘要与证据片段）。",
			Tier:        mcpserver.L1,
			ReadOnly:    true,
			Schema: map[string]any{
				"type":       "object",
				"properties": mcpProps(map[string]string{"task_id": "任务 id（字符串）", "severity": "critical/high/medium/low", "status": "pending/confirmed/false_positive/…", "vuln_class": "漏洞分类", "query": "关键词", "page": "页码，默认 1", "page_size": "每页条数，默认 20，上限 100"}),
			},
			Handler: s.mcpListFindings,
		},
		{
			Name:        "list_companies",
			Title:       "公司/范围",
			Description: "列出公司与其授权范围（domain/CIDR/ICP），以及每家公司的资产数量。",
			Tier:        mcpserver.L1,
			ReadOnly:    true,
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
			Handler:     s.mcpListCompanies,
		},
		{
			Name:        "list_tasks",
			Title:       "任务列表",
			Description: "列出任务（id/名称/状态/分析域/目标摘要），可按 status 过滤。用 get_task 取单个任务详情。",
			Tier:        mcpserver.L1,
			ReadOnly:    true,
			Schema: map[string]any{
				"type":       "object",
				"properties": mcpProps(map[string]string{"status": "created/running/paused/done/failed/timeout", "limit": "最多返回条数，默认 50"}),
			},
			Handler: s.mcpListTasks,
		},
		{
			Name:        "get_task",
			Title:       "任务详情",
			Description: "按 id 取任务详情，附资产类型计数，便于判断这个目标已经挖到哪一步。",
			Tier:        mcpserver.L1,
			ReadOnly:    true,
			Schema: map[string]any{
				"type":       "object",
				"properties": mcpProps(map[string]string{"id": "任务 id"}),
				"required":   []string{"id"},
			},
			Handler: s.mcpGetTask,
		},
		{
			Name:        "tactical_kb",
			Title:       "战术知识库",
			Description: "本地静态经验检索。mode=vuln_patterns 按分类取打法卡片；mode=logic_invariants 取业务不变量清单；mode=past_findings / past_assets 跨任务检索历史结论与资产。",
			Tier:        mcpserver.L1,
			ReadOnly:    true,
			Schema: map[string]any{
				"type": "object",
				"properties": mcpProps(map[string]string{
					"mode":     "vuln_patterns | logic_invariants | past_findings | past_assets",
					"query":    "关键词/目标类型（如 ai_gateway、jwt、ssrf）",
					"category": "vuln_patterns 模式下的分类，如 bola_idor / ssrf_cloud",
					"limit":    "最多返回条数，默认 8，上限 25",
				}),
				"required": []string{"mode"},
			},
			Handler: s.mcpTacticalKB,
		},
	}
}

// mcpProps 把「字段 → 说明」压成 JSON Schema 的 properties。
// 全部按 string 声明（数字型参数在 handler 里再做宽松解析），
// 避免不同客户端对 integer/number 的校验严格程度差异。
func mcpProps(desc map[string]string) map[string]any {
	out := make(map[string]any, len(desc))
	for k, v := range desc {
		out[k] = map[string]any{"type": "string", "description": v}
	}
	return out
}

// decodeMCPArgs 把外部传入的 JSON 解到结构体；空参当作 {}。
func decodeMCPArgs(raw json.RawMessage, v any) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("参数解析失败: %v", err)
	}
	return nil
}

// mcpInt 宽松解析数字参数：兼容 12 / "12" / 12.0（不同客户端序列化差异）。
func mcpInt(v any, def int) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		t = strings.TrimSpace(t)
		if t == "" {
			return def
		}
		var n int
		if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func (s *Server) mcpListAssets(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Query     string `json:"query"`
		Type      string `json:"type"`
		CompanyID any    `json:"company_id"`
		TaskID    any    `json:"task_id"`
		Limit     any    `json:"limit"`
		Offset    any    `json:"offset"`
	}
	if err := decodeMCPArgs(raw, &a); err != nil {
		return "", err
	}
	limit := clampInt(mcpInt(a.Limit, 50), 1, 200)
	offset := clampInt(mcpInt(a.Offset, 0), 0, 1<<20)
	store := s.m.pg.Assets()
	typ := strings.TrimSpace(a.Type)
	companyID := int64(mcpInt(a.CompanyID, 0))
	taskID := int64(mcpInt(a.TaskID, 0))
	query := strings.TrimSpace(a.Query)

	var (
		rows  []*db.Asset
		err   error
		scope string
	)
	switch {
	case query != "":
		rows, err = store.Search(query, limit)
		scope = "query=" + query
	case companyID > 0:
		rows, err = store.QueryByCompany(companyID, typ, limit, offset)
		scope = fmt.Sprintf("company_id=%d type=%s", companyID, emptyAs(typ, "*"))
	case taskID > 0:
		rows, err = store.QueryByTask(taskID, typ, limit, offset)
		scope = fmt.Sprintf("task_id=%d type=%s", taskID, emptyAs(typ, "*"))
	case typ != "":
		rows, err = store.QueryByType(typ, limit, offset)
		scope = "type=" + typ
	default:
		counts := map[string]int{}
		for _, t := range []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"} {
			if n, e := store.CountByType(t); e == nil {
				counts[t] = n
			}
		}
		return mcpJSONString(map[string]any{"counts": counts, "hint": "给 query / type / company_id / task_id 可以列具体条目"})
	}
	if err != nil {
		return "", err
	}
	return mcpJSONString(map[string]any{"scope": scope, "count": len(rows), "items": rows})
}

func (s *Server) mcpListFindings(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		TaskID    string `json:"task_id"`
		Severity  string `json:"severity"`
		Status    string `json:"status"`
		VulnClass string `json:"vuln_class"`
		Query     string `json:"query"`
		Page      any    `json:"page"`
		PageSize  any    `json:"page_size"`
	}
	if err := decodeMCPArgs(raw, &a); err != nil {
		return "", err
	}
	page := clampInt(mcpInt(a.Page, 1), 1, 10000)
	size := clampInt(mcpInt(a.PageSize, 20), 1, 100)
	f := db.FindingFilter{
		TaskID:    strings.TrimSpace(a.TaskID),
		Severity:  strings.TrimSpace(a.Severity),
		Status:    strings.TrimSpace(a.Status),
		VulnClass: strings.TrimSpace(a.VulnClass),
		Query:     strings.TrimSpace(a.Query),
	}
	rows, total, err := s.m.pg.ListFindingsPage(f, page, size)
	if err != nil {
		return "", err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, f2 := range rows {
		items = append(items, map[string]any{
			"id": f2.ID, "task_id": f2.TaskID, "vuln_class": f2.VulnClass, "name": f2.Name,
			"severity": f2.Severity, "status": f2.Status, "summary": clipRunes(f2.Summary, 400),
			"evidence": clipRunes(f2.Evidence, 600), "worker": f2.Worker, "created_at": f2.CreatedAt,
		})
	}
	return mcpJSONString(map[string]any{"total": total, "page": page, "page_size": size, "items": items})
}

func (s *Server) mcpListCompanies(_ context.Context, raw json.RawMessage) (string, error) {
	rows, err := s.m.pg.Assets().Companies().ListCompanies()
	if err != nil {
		return "", err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		items = append(items, map[string]any{
			"id": c.ID, "name": c.Name, "asset_count": c.AssetCount, "scopes": c.Scope,
		})
	}
	return mcpJSONString(map[string]any{"count": len(items), "items": items})
}

func (s *Server) mcpListTasks(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Status string `json:"status"`
		Limit  any    `json:"limit"`
	}
	if err := decodeMCPArgs(raw, &a); err != nil {
		return "", err
	}
	rows, err := s.m.pg.ListTasks()
	if err != nil {
		return "", err
	}
	want := strings.TrimSpace(a.Status)
	filtered := make([]*db.Task, 0, len(rows))
	for _, t := range rows {
		if want != "" && t.Status != want {
			continue
		}
		filtered = append(filtered, t)
	}
	limit := clampInt(mcpInt(a.Limit, 50), 1, 500)
	items := mcpTaskBriefs(filtered, limit)
	return mcpJSONString(map[string]any{"total": len(filtered), "returned": len(items), "items": items})
}

func (s *Server) mcpGetTask(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		ID any `json:"id"`
	}
	if err := decodeMCPArgs(raw, &a); err != nil {
		return "", err
	}
	id := int64(mcpInt(a.ID, 0))
	if id <= 0 {
		return "", fmt.Errorf("id 必填且为正整数")
	}
	t, err := s.m.pg.GetTask(id)
	if err != nil {
		return "", err
	}
	if t == nil {
		return "", fmt.Errorf("任务 %d 不存在", id)
	}
	counts, _ := s.m.pg.Assets().CountsByTypeForTask(id)
	return mcpJSONString(map[string]any{"task": t, "asset_counts": counts})
}

func (s *Server) mcpTacticalKB(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Mode     string `json:"mode"`
		Query    string `json:"query"`
		Category string `json:"category"`
		Limit    any    `json:"limit"`
	}
	if err := decodeMCPArgs(raw, &a); err != nil {
		return "", err
	}
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	query := strings.TrimSpace(a.Query)
	category := strings.TrimSpace(a.Category)
	limit := clampInt(mcpInt(a.Limit, 8), 1, 25)

	switch mode {
	case "vuln_patterns":
		text, err := agent.KBVulnPatternsJSON(category, query, limit)
		if err != nil {
			return "", err
		}
		return text, nil
	case "logic_invariants":
		text, err := agent.KBLogicProfilesJSON(query, limit)
		if err != nil {
			return "", err
		}
		return text, nil
	case "past_findings":
		f := db.FindingFilter{Query: query}
		rows, total, err := s.m.pg.ListFindingsPage(f, 1, limit)
		if err != nil {
			return "", err
		}
		items := make([]map[string]any, 0, len(rows))
		for _, x := range rows {
			items = append(items, map[string]any{
				"id": x.ID, "task_id": x.TaskID, "vuln_class": x.VulnClass, "name": x.Name,
				"severity": x.Severity, "status": x.Status, "summary": clipRunes(x.Summary, 300),
			})
		}
		return mcpJSONString(map[string]any{"mode": mode, "total": total, "items": items})
	case "past_assets":
		rows, err := s.m.pg.Assets().Search(query, limit)
		if err != nil {
			return "", err
		}
		return mcpJSONString(map[string]any{"mode": mode, "count": len(rows), "items": rows})
	}
	return "", fmt.Errorf("未知 mode: %s（可选 vuln_patterns / logic_invariants / past_findings / past_assets）", mode)
}

// mcpL3Tools 预留给 P5 的编排委派；默认 L3 档未开启，这里返回空。
func (s *Server) mcpL3Tools() []mcpserver.Tool { return nil }

// --- 小工具 ---

func mcpJSONString(v any) (string, error) {
	_, text, err := mcpJSON(v)
	return text, err
}

func emptyAs(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcpserver"
)

// L3 = 编排委派：让外部 harness 把「这条线索值得深挖」变成一个真正的 ARTEX 任务，
// 由后台 agent 继续跑，harness 自己去做别的。默认关（写库 + 起任务），
// 需要在控制台显式打开 L3。
//
// 刻意不提供 task_cancel：取消 = 删任务（连资产、发现、transcript 一起清理），
// 属于「永久不暴露」那一类。要停就跑 task_pause，人在控制台决定要不要删。

func (s *Server) mcpL3Tools() []mcpserver.Tool {
	if s.m == nil || s.m.pg == nil {
		return nil
	}
	return []mcpserver.Tool{
		{
			Name:        "task_create",
			Title:       "创建任务",
			Description: "把一个目标交给 ARTEX 后台任务。description 写清范围与约束，goal 写要达成的验证目标。返回 task_id，用 task_status 跟进。属于写操作，仅在 L3 开启时可用。",
			Tier:        mcpserver.L3,
			ReadOnly:    false,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description":     map[string]any{"type": "string", "description": "任务描述：目标范围、授权边界、已知线索"},
					"goal":            map[string]any{"type": "string", "description": "验证目标，例如『确认 /api/user 是否存在越权读取』"},
					"name":            map[string]any{"type": "string", "description": "可选任务名"},
					"analysis_domain": map[string]any{"type": "string", "enum": []string{"web_security", "software_re"}, "description": "分析域，默认 web_security"},
					"company_ids":     map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "可选：归属公司 id"},
					"timeout_seconds": map[string]any{"type": "integer", "description": "任务超时秒数，默认 0（不限）"},
					"token_budget":    map[string]any{"type": "integer", "description": "token 预算，默认 0（不限）"},
				},
				"required": []string{"description", "goal"},
			},
			Handler: s.mcpTaskCreate,
		},
		{
			Name:        "task_status",
			Title:       "任务进度",
			Description: "查任务状态、进度与产出数量（资产/发现），用于轮询自己刚创建的任务。",
			Tier:        mcpserver.L3,
			ReadOnly:    true,
			Schema: map[string]any{
				"type":       "object",
				"properties": mcpProps(map[string]string{"id": "任务 id"}),
				"required":   []string{"id"},
			},
			Handler: s.mcpTaskStatus,
		},
		{
			Name:        "task_pause",
			Title:       "暂停任务",
			Description: "暂停一个正在运行的任务（可再用 task_resume 恢复）。不删除任何数据。",
			Tier:        mcpserver.L3,
			ReadOnly:    false,
			Schema: map[string]any{
				"type":       "object",
				"properties": mcpProps(map[string]string{"id": "任务 id"}),
				"required":   []string{"id"},
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				return s.mcpTaskControl(ctx, raw, "pause")
			},
		},
		{
			Name:        "task_resume",
			Title:       "恢复任务",
			Description: "恢复一个被暂停的任务。",
			Tier:        mcpserver.L3,
			ReadOnly:    false,
			Schema: map[string]any{
				"type":       "object",
				"properties": mcpProps(map[string]string{"id": "任务 id"}),
				"required":   []string{"id"},
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				return s.mcpTaskControl(ctx, raw, "resume")
			},
		},
	}
}

func (s *Server) mcpTaskCreate(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Description    string  `json:"description"`
		Goal           string  `json:"goal"`
		Name           string  `json:"name"`
		AnalysisDomain string  `json:"analysis_domain"`
		CompanyIDs     []int64 `json:"company_ids"`
		Timeout        int     `json:"timeout_seconds"`
		TokenBudget    int     `json:"token_budget"`
	}
	if err := decodeMCPArgs(raw, &a); err != nil {
		return "", err
	}
	desc := strings.TrimSpace(a.Description)
	goal := strings.TrimSpace(a.Goal)
	if desc == "" || goal == "" {
		return "", fmt.Errorf("description 与 goal 都是必填")
	}
	domain := strings.TrimSpace(a.AnalysisDomain)
	if domain != "" && domain != "web_security" && domain != "software_re" {
		return "", fmt.Errorf("analysis_domain 只能是 web_security 或 software_re")
	}
	opts := db.TaskCreateOptions{
		AnalysisDomain: domain,
		Name:           strings.TrimSpace(a.Name),
		CompanyIDs:     a.CompanyIDs,
		TimeoutSeconds: a.Timeout,
		TokenBudget:    a.TokenBudget,
	}
	t, err := s.m.CreateTaskWithOptions(desc, goal, opts)
	if err != nil {
		return "", err
	}
	return mcpJSONString(map[string]any{
		"task_id": t.ID, "status": t.Status, "name": t.Name,
		"hint": "用 task_status 轮询进度；发现与资产会在任务运行中陆续入库",
	})
}

func (s *Server) mcpTaskStatus(_ context.Context, raw json.RawMessage) (string, error) {
	id, err := mcpTaskID(raw)
	if err != nil {
		return "", err
	}
	t, err := s.m.pg.GetTask(id)
	if err != nil {
		return "", err
	}
	if t == nil {
		return "", fmt.Errorf("任务 %d 不存在", id)
	}
	counts, _ := s.m.pg.Assets().CountsByTypeForTask(id)
	findings, totalFindings, _ := s.m.pg.ListFindingsPage(db.FindingFilter{TaskID: fmt.Sprintf("%d", id)}, 1, 5)
	recent := make([]map[string]any, 0, len(findings))
	for _, f := range findings {
		recent = append(recent, map[string]any{
			"id": f.ID, "severity": f.Severity, "name": f.Name, "vuln_class": f.VulnClass,
			"status": f.Status, "summary": clipRunes(f.Summary, 200),
		})
	}
	return mcpJSONString(map[string]any{
		"task":            mcpTaskBriefs([]*db.Task{t}, 1)[0],
		"asset_counts":    counts,
		"finding_total":   totalFindings,
		"recent_findings": recent,
	})
}

func (s *Server) mcpTaskControl(_ context.Context, raw json.RawMessage, action string) (string, error) {
	id, err := mcpTaskID(raw)
	if err != nil {
		return "", err
	}
	t, ok := s.m.Task(strconv.FormatInt(id, 10))
	if !ok || t == nil {
		return "", fmt.Errorf("任务 %d 不存在", id)
	}
	res, err := s.applyTaskControl(t, action)
	if err != nil {
		return "", err
	}
	return mcpJSONString(map[string]any{
		"task_id": id, "action": action, "status": res.Status, "queued": res.Queued,
	})
}

func mcpTaskID(raw json.RawMessage) (int64, error) {
	var a struct {
		ID any `json:"id"`
	}
	if err := decodeMCPArgs(raw, &a); err != nil {
		return 0, err
	}
	id := int64(mcpInt(a.ID, 0))
	if id <= 0 {
		return 0, fmt.Errorf("id 必填且为正整数")
	}
	return id, nil
}

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
			// 建任务是「新增」，不破坏既有数据；但会驱动 ARTEX 对目标出网。
			Destructive: false,
			Idempotent:  false,
			OpenWorld:   true,
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
		{
			Name:        "artex_status",
			Title:       "ARTEX 态势",
			Description: "查 ARTEX 本体的运行态势：活动任务、全部任务及状态、运行/排队/暂停计数、并发上限。想知道 ARTEX 现在在干什么、能干什么，先调这个——不要读源码、不要翻文档、不要猜。",
			Tier:        mcpserver.L3,
			ReadOnly:    true,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"include_done": map[string]any{"type": "boolean", "description": "是否包含已结束（done/failed/timeout）的任务，默认 false"},
					"limit":        map[string]any{"type": "integer", "description": "最多返回多少个任务，默认 20，上限 50"},
				},
			},
			Handler: s.mcpArtexStatus,
		},
		{
			Name:        "task_steer",
			Title:       "给任务下指令",
			Description: "任务跑偏、需要补充约束或新增方向时，把一段人类指令注入任务。mode=hint（默认）记为提示节点，planner 下一轮会读到；mode=intent 直接插入一条高优先级意图，planner 优先处理。不暂停任务、不删除数据。",
			Tier:        mcpserver.L3,
			ReadOnly:    false,
			Destructive: false,
			Idempotent:  false,
			OpenWorld:   true,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":   map[string]any{"type": "integer", "description": "任务 id（task_create 返回的 task_id）"},
					"text": map[string]any{"type": "string", "description": "要补充的方向、约束或验收标准；写具体，不要写『继续加油』"},
					"mode": map[string]any{"type": "string", "enum": []string{"hint", "intent"}, "description": "hint=提示（默认）；intent=高优先级意图"},
				},
				"required": []string{"id", "text"},
			},
			Handler: s.mcpTaskSteer,
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

// mcpArtexStatus 返回 ARTEX 本体的运行态势。
//
// 外部 AI 想「接管」ARTEX 时先调这个，而不是去读源码或猜能力边界：
// 它一次性给出活动任务、全部任务、运行/排队/暂停计数与并发上限。
func (s *Server) mcpArtexStatus(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		IncludeDone bool `json:"include_done"`
		Limit       int  `json:"limit"`
	}
	_ = decodeMCPArgs(raw, &a)
	limit := a.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	activeID := ""
	if at := s.m.ActiveTask(); at != nil {
		activeID = at.ID
	}

	rows := s.m.List()
	briefs := make([]map[string]any, 0, len(rows))
	running, queued, paused, finished := 0, 0, 0, 0
	for _, t := range rows {
		terminal := t.Status == "done" || t.Status == "failed" || t.Status == "timeout" || t.CompletedAt > 0
		switch {
		case terminal:
			finished++
		case t.Queued:
			queued++
		case t.Paused:
			paused++
		default:
			running++
		}
		if terminal && !a.IncludeDone {
			continue
		}
		if len(briefs) >= limit {
			continue
		}
		briefs = append(briefs, map[string]any{
			"id":              t.ID,
			"name":            t.Name,
			"status":          t.Status,
			"analysis_domain": t.AnalysisDomain,
			"paused":          t.Paused,
			"queued":          t.Queued,
			"active":          t.ID == activeID,
			"goal":            clipRunes(t.Goal, 200),
			"created_at":      t.CreatedAt,
		})
	}

	enabled, concLimit := s.m.ConcurrencyLimit()
	return mcpJSONString(map[string]any{
		"server":      "artex",
		"active_task": activeID,
		"task_total":  len(rows),
		"running":     running,
		"queued":      queued,
		"paused":      paused,
		"finished":    finished,
		"concurrency": map[string]any{"enabled": enabled, "limit": concLimit},
		"tasks":       briefs,
		"hint":        "给任务下指令用 task_steer；看单个任务详情用 task_status；开新任务用 task_create",
	})
}

// mcpTaskSteer 把一段人类指令注入正在跑的任务。
//
// 两种落点：
//   - hint（默认）：写一个 KindHint 节点，planner 下一轮规划时会读到，不改变优先级；
//   - intent：直接插一条高优先级意图，planner 优先处理——相当于人从控制台插队。
//
// 两者都不暂停任务；要叫停走 task_pause。
func (s *Server) mcpTaskSteer(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		ID   any    `json:"id"`
		Text string `json:"text"`
		Mode string `json:"mode"`
	}
	if err := decodeMCPArgs(raw, &a); err != nil {
		return "", err
	}
	id := int64(mcpInt(a.ID, 0))
	if id <= 0 {
		return "", fmt.Errorf("id 必填且为正整数")
	}
	text := strings.TrimSpace(a.Text)
	if text == "" {
		return "", fmt.Errorf("text 必填：写清要补充的方向、约束或验收标准")
	}
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	if mode == "" {
		mode = "hint"
	}
	if mode != "hint" && mode != "intent" {
		return "", fmt.Errorf("mode 只能是 hint 或 intent")
	}

	t, ok := s.m.Task(strconv.FormatInt(id, 10))
	if !ok || t == nil {
		return "", fmt.Errorf("任务 %d 不存在", id)
	}

	var nodeID int64
	var err error
	if mode == "intent" {
		nodeID, err = t.Store.AddIntent(map[string]any{"summary": text}, 9, nil, "human")
	} else {
		nodeID, err = t.Store.AddNode(db.KindHint, map[string]any{"text": text}, 0, "active", "human", nil)
	}
	if err != nil {
		return "", err
	}
	t.Notify()

	return mcpJSONString(map[string]any{
		"task_id": id,
		"mode":    mode,
		"node_id": nodeID,
		"hint":    "已注入；用 task_status 观察 planner 是否采纳",
	})
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/skill"
	actool "github.com/Autumn-27/norma/tool"
)

func meterSkillTool(inner actool.CoreTool, pg *db.DB, reg *skill.Registry, agentKey string, run agent.RunInfo) actool.CoreTool {
	if inner == nil || pg == nil {
		return inner
	}
	return &meteredSkill{CoreTool: inner, pg: pg, reg: reg, agentKey: agentKey, taskID: run.TaskID, sessID: run.SessionID}
}

type meteredSkill struct {
	actool.CoreTool
	pg       *db.DB
	reg      *skill.Registry
	agentKey string
	taskID   int64
	sessID   string
}

func (m *meteredSkill) Description() string { return m.Prompt() }

// The canonical field is SDK v0.4.3's name. Legacy spellings are advertised as
// compatibility alternatives so validation does not reject them before repair.
func (m *meteredSkill) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":       map[string]any{"type": "string", "minLength": 1, "description": "规范字段：要加载的可见技能全名"},
			"args":       map[string]any{"type": "string", "description": "可选补充上下文"},
			"skill":      map[string]any{"type": "string", "minLength": 1, "description": "旧版兼容别名；新调用使用 name"},
			"skill_name": map[string]any{"type": "string", "minLength": 1, "description": "旧版兼容别名；新调用使用 name"},
		},
		"anyOf":                []any{map[string]any{"required": []any{"name"}}, map[string]any{"required": []any{"skill"}}, map[string]any{"required": []any{"skill_name"}}},
		"additionalProperties": false,
	}
}

func (m *meteredSkill) Prompt() string {
	if m.reg == nil || len(m.reg.List()) == 0 {
		return m.CoreTool.Description()
	}
	var b strings.Builder
	b.WriteString("加载当前可见专业技能包及其操作规约。任务匹配时先调用 Skill({\"name\":\"<下方技能全名>\"})；仅使用真实目录中的名称，不猜测未配置的 MCP 或能力。\n<available_skills>\n")
	for _, s := range m.reg.List() {
		tag := ""
		if len(s.MCPs) > 0 {
			tag = fmt.Sprintf(" [关联 MCP：%s；调用成功后按既有权限解锁]", strings.Join(s.MCPs, ", "))
		}
		fmt.Fprintf(&b, "- %s: %s%s\n", s.Name, s.Description, tag)
	}
	b.WriteString("</available_skills>")
	return b.String()
}

// Prefer an exact visible custom name before applying historical aliases.
func canonicalVisibleSkillName(reg *skill.Registry, name string) string {
	name = strings.TrimSpace(name)
	if reg != nil {
		if s, ok := reg.Get(name); ok {
			return s.Name
		}
	}
	name = agent.CanonicalSkillAlias(name)
	if reg != nil {
		for _, s := range reg.List() {
			if strings.EqualFold(s.Name, name) {
				return s.Name
			}
		}
	}
	return name
}

func (m *meteredSkill) resolveInput(in json.RawMessage) (string, json.RawMessage, error) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(in, &args); err != nil || args == nil {
		return "", nil, fmt.Errorf("Skill 入参必须是 JSON 对象")
	}
	for k := range args {
		if k != "name" && k != "skill" && k != "skill_name" && k != "args" {
			return "", nil, fmt.Errorf("Skill 不支持字段 %s", k)
		}
	}
	canonical := func(name string) string { return canonicalVisibleSkillName(m.reg, name) }
	name := ""
	for _, k := range []string{"name", "skill", "skill_name"} {
		raw, ok := args[k]
		if !ok {
			continue
		}
		var v string
		if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v) == "" {
			return "", nil, fmt.Errorf("Skill 字段 %s 必须为非空字符串", k)
		}
		v = canonical(v)
		if name != "" && name != v {
			return "", nil, fmt.Errorf("Skill 名称字段冲突；请只提供规范 name")
		}
		name = v
	}
	if name == "" {
		return "", nil, fmt.Errorf("Skill 缺少 name")
	}
	if raw, ok := args["args"]; ok {
		var v string
		if string(raw) == "null" || json.Unmarshal(raw, &v) != nil {
			return name, nil, fmt.Errorf("Skill args 必须为字符串")
		}
	}
	delete(args, "skill")
	delete(args, "skill_name")
	args["name"], _ = json.Marshal(name)
	normalized, err := json.Marshal(args)
	return name, normalized, err
}

func (m *meteredSkill) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	name, normalized, normalizeErr := m.resolveInput(in)
	var res actool.Result
	var err error
	if normalizeErr != nil {
		res = actool.Errorf("%s", normalizeErr.Error())
	} else {
		res, err = m.CoreTool.Call(ctx, normalized, tc)
	}
	status, errMsg := "ok", ""
	if err != nil {
		status = "error"
		errMsg = err.Error()
	} else if res.IsError {
		status = "error"
		errMsg = res.Flatten()
	}
	if name == "" {
		name = "<invalid>"
	}
	if runes := []rune(errMsg); len(runes) > 2048 {
		errMsg = string(runes[:2048]) + "…"
	}
	if e := m.pg.RecordSkillUsage(db.SkillUsage{Skill: name, AgentKey: m.agentKey, TaskID: m.taskID, SessionID: m.sessID, Status: status, Error: errMsg}); e != nil {
		log.Printf("[skills] 记录调用失败 (%s/%s): %v", m.agentKey, name, e)
	}
	return res, err
}

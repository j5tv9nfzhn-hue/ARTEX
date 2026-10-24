package server

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"

	actool "github.com/Autumn-27/norma/tool"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

type toolUsageRecorder interface {
	InsertToolUsage(*db.ToolUsage) error
}

// meteredTool records one row immediately before a catalog tool is invoked. It
// embeds the resolved tool so schema overrides, defaults and permission behavior
// remain unchanged.
type meteredTool struct {
	actool.CoreTool
	recorder toolUsageRecorder
	toolKey  string
	agentKey string
	ri       agent.RunInfo
}

func meterTool(t actool.CoreTool, recorder toolUsageRecorder, toolKey, agentKey string, ri agent.RunInfo) actool.CoreTool {
	if recorder == nil {
		return t
	}
	return &meteredTool{CoreTool: t, recorder: recorder, toolKey: toolKey, agentKey: agentKey, ri: ri}
}

// unknownToolResult 认出一条「模型调用了不存在的工具」的 activity。
//
// 这类调用根本没解析到工具，走不进 meteredTool，所以账本里从来没有它们——部署库
// 里 worker 的 Write 是 0 行、全库 Write 的 is_error 也是 0，而 activity 里明明躺着
// Error: unknown tool "Write"。错误率因此被系统性低估。
func unknownToolResult(r db.Activity) bool {
	if r.Kind != "tool_result" || !r.IsError || strings.TrimSpace(r.Tool) == "" {
		return false
	}
	return strings.Contains(strings.ToLower(r.Detail), "unknown tool")
}

// meterUnknownTool 给这类调用补一行账本，类别单独记成 unknown_tool。
func (e *Engine) meterUnknownTool(t *Task, r db.Activity) {
	if e == nil || e.m == nil || e.m.pg == nil || t == nil {
		return
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	if err := e.m.pg.InsertToolUsage(&db.ToolUsage{
		ToolKey:       r.Tool,
		AgentKey:      r.Worker,
		TaskID:        taskID,
		ExplorationID: t.ExpID,
		IsError:       true,
		ErrorClass:    agent.ErrClassUnknownTool,
	}); err != nil {
		log.Printf("[toolusage] insert unknown tool %s: %v", r.Tool, err)
	}
}

func (m *meteredTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (res actool.Result, err error) {
	// 账本改成「调用结束后」写：只有拿到结果才知道这次是不是错误。
	// 用 defer 保证 panic / 提前返回也记一行——「调用了却没记」会让错误率被
	// 系统性低估，而这正是「工具错误率到底多高」这个问题的唯一数据源。
	defer func() {
		isErr := err != nil || res.IsError
		class := ""
		if isErr {
			class = agent.ErrorClassOf(res, err, ctx)
		}
		if insErr := m.recorder.InsertToolUsage(&db.ToolUsage{
			ToolKey:       m.toolKey,
			AgentKey:      m.agentKey,
			TaskID:        m.ri.TaskID,
			ExplorationID: m.ri.ExplorationID,
			IntentID:      m.ri.IntentID,
			SessionID:     m.ri.SessionID,
			IsError:       isErr,
			ErrorClass:    class,
		}); insErr != nil {
			log.Printf("[toolusage] insert %s: %v", m.toolKey, insErr)
		}
	}()
	return m.CoreTool.Call(ctx, input, tc)
}

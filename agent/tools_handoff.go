package agent

import (
	"context"
	"encoding/json"
	"fmt"

	actool "github.com/Autumn-27/norma/tool"
)

// get_handoff：读某条意图最近一次执行的【结构化交接单】。
//
// 为什么需要它：换一个子代理接着做时，光靠一段文本总结会丢掉"上一位没说出口"
// 的东西（半成品假设、被中断的利用链、已经排除的方向）。交接单把事实层
// （跑过什么、写回什么、最后输出、可回读的步骤区间）固定成结构化字段，
// 由服务端在 run 收尾时机械抽取，不经过模型复述。
func (t *ToolSet) getHandoff() actool.CoreTool {
	return t.readExpTool("get_handoff",
		"取某条意图(work)最近一次执行的结构化交接单：它的目标原文、已写回的事实/资产/漏洞计数、最后若干步（含报错标记）、最后输出、以及可回读的步骤区间。用于「接着上一条没跑完的方向继续」时拿到事实层交接，比文本总结更完整。没有交接单（从未执行/台账未启用）会明确报错。",
		obj(map[string]any{"intent_id": idp("意图(work) id")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			h, err := t.ts.IntentHandoff(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if h == nil {
				return actool.Errorf(fmt.Sprintf("意图 %d 还没有交接单（可能尚未执行过，或该 run 早于台账上线）", id)), nil
			}
			return jsonResult(map[string]any{
				"intent_id":  h.IntentID,
				"schema":     h.SchemaVer,
				"created_at": h.CreatedAt,
				"summary":    h.RenderText,
				"record":     json.RawMessage(h.RecordJSON),
			})
		})
}

// GetHandoffTool exposes the tool for cross-task orchestration (same pattern as
// the other exported accessors in tools.go).
func (t *ToolSet) GetHandoffTool() actool.CoreTool { return t.getHandoff() }

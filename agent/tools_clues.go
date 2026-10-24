package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// 任务与线索缓存的两个端点：写（cache_clue）与读（query_clues）。
//
// 它们不是「又一个记录工具」。record_fact 写的是**结论**（喂给探索图、给规划者
// 看），这里写的是**材料**（喂给下一个 worker，让它不用把同样的地方再摸一遍）：
// 端点、参数、JS 里泄露的密钥、异常报错指纹、后台配置暴露……
//
// 关键约束（用户明确要求）：这些材料**不进聊天上下文**——不占 worker 的窗口，
// 不参与压缩，也不会因为上下文被截断而消失。worker 需要时用 query_clues 取，
// 宿主在连续失败时自动塞一小段摘要（见 clue_hint.go）。

// clueStoreOf 返回本 ToolSet 绑定的线索缓存（不可用时 nil）。
func (t *ToolSet) clueStoreOf() *db.ClueStore {
	if t == nil || t.ts == nil {
		return nil
	}
	return t.ts.Clues()
}

// Clues 暴露线索缓存给 run 装配层（worker 把它放进 ctx，供失败自动提示使用）。
func (t *ToolSet) Clues() *db.ClueStore { return t.clueStoreOf() }

// clueItem 是 cache_clue 的单条入参。
type clueItem struct {
	Kind       string `json:"kind"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	Source     string `json:"source"`
	Confidence string `json:"confidence"`
	IntentID   int64  `json:"intent_id"`
}

var clueKindDoc = "asset（域名/IP/端口/服务指纹）| endpoint（方法+路径+参数名）| param（参数名+位置+可疑点）| secret（JS/配置里的密钥、令牌、内网地址）| error（异常报错，按指纹去重）| credential（凭据片段：用户名/角色/令牌的存在性，不要写明文口令）| config（Swagger/actuator/.git/调试开关等暴露）| note（其它可复用结论）"

func (t *ToolSet) cacheClueTool() actool.CoreTool {
	return t.writeExpTool("cache_clue",
		"把【后来者能直接用的原料】写进任务线索缓存（外挂记忆体，不占聊天上下文）。"+
			"与 record_fact 分工：record_fact 写结论（给规划者），cache_clue 写原料（给下一个 worker）。"+
			"看到新端点/参数/密钥/报错/配置暴露就**立刻写**——它不会随上下文压缩消失。同 (kind,key) 重复写会自动合并，不必怕写重。"+
			"只写结构化材料，不写流水账与明文口令；clues 数组可一次写多条。",
		obj(map[string]any{
			"kind":       str("分类：" + clueKindDoc),
			"key":        str("检索键，省略时取 value 前 120 字；同键合并去重"),
			"value":      str("材料正文（主机、状态码、参数名、证据片段）"),
			"source":     str("产生它的工具名"),
			"confidence": str("observed | inferred（默认）"),
			"clues":      map[string]any{"type": "array", "description": "批量写入，元素同上述字段", "items": map[string]any{"type": "object"}},
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			cs := t.clueStoreOf()
			if cs == nil || t.taskID == 0 {
				return actool.Errorf("cache_clue 需要任务上下文（任务 id + 探索库）：当前 run 不在任务内，线索缓存不可用。请改用 record_fact。"), nil
			}
			var a struct {
				clueItem
				Clues []clueItem `json:"clues"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败：" + err.Error()), nil
			}
			items := a.Clues
			if len(items) == 0 {
				items = []clueItem{a.clueItem}
			}
			ri := RunInfoFrom(ctx)
			producer := fmt.Sprintf("%s work#%d", t.worker, ri.IntentID)
			ins := make([]db.ClueInput, 0, len(items))
			for _, it := range items {
				intent := it.IntentID
				if intent == 0 {
					intent = ri.IntentID
				}
				ins = append(ins, db.ClueInput{
					Kind: it.Kind, Key: it.Key, Value: it.Value,
					Source: it.Source, Confidence: it.Confidence,
					ProducedBy: producer, IntentID: intent,
				})
			}
			created, updated, firstErr := cs.PutMany(t.taskID, ins)
			out := map[string]any{"created": created, "merged": updated, "total": len(ins)}
			if firstErr != nil {
				out["error"] = firstErr.Error()
			}
			stats, _ := cs.Stats(t.taskID)
			if len(stats) > 0 {
				out["cache"] = stats
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) queryCluesTool() actool.CoreTool {
	return t.readExpTool("query_clues",
		"检索任务线索缓存：之前任何 work 发现的端点、参数、密钥、报错指纹、配置暴露。"+
			"用在：接手新意图不知道从哪下手、当前思路连续失败（**先看缓存里有没有别的入口，换分支而不是原地重试**）、怀疑已经测过、报漏洞前确认证据。"+
			"q 对 key/value 做子串匹配（主机名、路径片段、报错关键字）。",
		obj(map[string]any{
			"kind":  str("只取某一类：" + clueKindDoc + "；留空=全部"),
			"q":     str("关键字，子串匹配 key/value"),
			"limit": map[string]any{"type": "integer", "description": "默认 20，上限 200"},
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			cs := t.clueStoreOf()
			if cs == nil || t.taskID == 0 {
				return actool.Errorf("query_clues 需要任务上下文（任务 id + 探索库）：当前 run 不在任务内。"), nil
			}
			var a struct {
				Kind     string `json:"kind"`
				Q        string `json:"q"`
				Limit    int    `json:"limit"`
				MarkUsed *bool  `json:"mark_used"`
			}
			_ = json.Unmarshal(in, &a)
			rows, err := cs.Query(t.taskID, a.Kind, a.Q, a.Limit)
			if err != nil {
				return actool.Errorf("线索缓存查询失败：" + err.Error()), nil
			}
			mark := a.MarkUsed == nil || *a.MarkUsed
			if mark && len(rows) > 0 {
				ids := make([]int64, 0, len(rows))
				for _, r := range rows {
					ids = append(ids, r.ID)
				}
				_ = cs.Touch(ids)
			}
			type view struct {
				ID         int64  `json:"id"`
				Kind       string `json:"kind"`
				Key        string `json:"key"`
				Value      string `json:"value"`
				Source     string `json:"source"`
				Confidence string `json:"confidence"`
				Seen       int    `json:"seen"`
				Used       int    `json:"used"`
			}
			out := make([]view, 0, len(rows))
			for _, r := range rows {
				v := view{ID: r.ID, Kind: r.Kind, Key: r.Key, Value: r.Value, Source: r.Source,
					Confidence: r.Confidence, Seen: r.SeenCount, Used: r.UsedCount}
				// 单条过长会重新把上下文吃掉——缓存的价值在于「压缩后的材料」。
				v.Value = truncRunesLog(v.Value, 600)
				out = append(out, v)
			}
			res := map[string]any{"clues": out, "count": len(out)}
			if len(out) == 0 {
				res["hint"] = "缓存里没有匹配的线索。换一个关键字（主机名/路径片段/报错词），或先把这一轮已看到的东西用 cache_clue 写进去。"
			}
			return jsonResult(res)
		})
}

// clueSummaryLine 把一条线索压成一行给日志/提示用。
func clueSummaryLine(c db.Clue) string {
	key := strings.TrimSpace(c.Key)
	val := strings.TrimSpace(c.Value)
	if key != "" && strings.HasPrefix(strings.ToLower(key), strings.ToLower(val)) {
		val = ""
	}
	line := c.Kind + " · " + key
	if val != "" {
		line += " — " + truncRunesLog(val, 160)
	}
	src := c.Source
	if src == "" {
		src = c.ProducedBy
	}
	if src != "" {
		line += "（来源 " + src + "，" + c.Confidence + "）"
	}
	return line
}

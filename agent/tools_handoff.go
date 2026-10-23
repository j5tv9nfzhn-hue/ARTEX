package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// 交接相关的三个工具（P1 的 get_handoff + P3 的 handoff_patch / read_trace_range）。
//
// 三者的分工：
//   get_handoff      读 L1 事实层（系统机械抽取，可信）
//   handoff_patch    写 L2 主观层（模型补注，永远标注为「可推翻」）
//   read_trace_range 回读原始步骤（比交接单更细，用于「他到底卡在哪一步」）

// getHandoff 取某条意图最近一次执行的结构化交接单。
func (t *ToolSet) getHandoff() actool.CoreTool {
	return t.readExpTool("get_handoff",
		"取某条意图(work)最近一次执行的结构化交接单：它的目标、已写回的事实/资产/漏洞、最后若干步（含报错标记）、最后输出、结束原因，以及上一任留下的主观补注（下一步/假设/已排除，标注为可推翻）。用于「接着上一条没跑完的方向继续」时拿到事实层交接，比文本总结更完整。",
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
			if err != nil || h == nil {
				return actool.Errorf(fmt.Sprintf("意图 %d 还没有交接单（可能从未执行过，或该 run 早于台账上线）", id)), nil
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

// handoffPatch 让模型在【运行中】就把「下一步 / 假设 / 负结果」落库。
//
// 为什么必须现在写：等到收尾（settlement）再说，撞墙/被杀时这些话就永远丢了。
// 写进 intent_open_work，与事实层物理分开；收尾时由 saveHandoff 合并进交接单，
// 渲染给下一位时带「可推翻」标签。
func (t *ToolSet) handoffPatch() actool.CoreTool {
	return t.readExpTool("handoff_patch",
		"给本条意图（或指定的历史意图）补一条【主观交接】：接下来该做什么(next_actions)、你目前的假设(hypotheses)、已经排除的负结果(negative_results)。中途随时可写，写得越具体，换人接着做时越不会丢判断。注意：这一层只是【可被推翻的建议】，不会替代已写回的事实；同一内容重复提交会被去重。",
		obj(map[string]any{
			"intent_id":        intp("可选：要补注的意图 id；省略=本条意图"),
			"next_actions":     str("可选：接下来应该做的具体动作（每行一条，建议 1-5 条）"),
			"hypotheses":       str("可选：尚未验证的假设（每行一条）"),
			"negative_results": str("可选：已经排除/证实不通的方向（每行一条）"),
			"dedupe_key":       str("可选：幂等键；同一 key 重复提交只落一条（省略=按内容自动去重）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID        json.RawMessage `json:"intent_id"`
				NextActions     string          `json:"next_actions"`
				Hypotheses      string          `json:"hypotheses"`
				NegativeResults string          `json:"negative_results"`
				DedupeKey       string          `json:"dedupe_key"`
			}
			_ = json.Unmarshal(in, &a)
			iid := pid(a.IntentID)
			if iid <= 0 {
				iid = t.ownerNode
			}
			if iid <= 0 {
				return actool.Errorf("缺少 intent_id（当前上下文没有绑定意图）"), nil
			}
			if t.ts == nil || t.ts.DB() == nil {
				return actool.Errorf("存储未就绪"), nil
			}
			// 补注必须挂在意图节点上：planner/main 的 ownerNode 不是意图（是任务根 fact
			// 或 0），它们必须显式传 intent_id，否则会把主观层写到一个没人读的节点上。
			if err := t.requireIntentNode(iid); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			next := strings.TrimSpace(a.NextActions)
			hyp := strings.TrimSpace(a.Hypotheses)
			neg := strings.TrimSpace(a.NegativeResults)
			if next == "" && hyp == "" && neg == "" {
				return actool.Errorf("next_actions / hypotheses / negative_results 至少要有一项"), nil
			}
			if len(next)+len(hyp)+len(neg) > 4000 {
				return actool.Errorf("补注过长（合计上限 4000 字），请精简后重写"), nil
			}
			taskID := strconv.FormatInt(t.taskID, 10)
			if t.taskID <= 0 {
				return actool.Errorf("任务上下文缺失，无法写补注"), nil
			}
			inserted, err := t.ts.DB().SaveOpenWork(&db.IntentOpenWork{
				TaskID: taskID, IntentID: iid, Author: t.worker,
				DedupeKey:   strings.TrimSpace(a.DedupeKey),
				NextActions: next, Hypotheses: hyp, NegativeResults: neg,
			})
			if err != nil {
				return actool.Errorf("写补注失败: " + err.Error()), nil
			}
			if !inserted {
				return jsonResult(map[string]any{"intent_id": iid, "deduplicated": true})
			}
			return jsonResult(map[string]any{
				"intent_id": iid, "saved": true,
				"next_actions": len(splitLines(next)), "hypotheses": len(splitLines(hyp)),
				"negative_results": len(splitLines(neg)),
				"note":             "已记录为【主观建议】，收尾时会并入交接单并标注为可推翻",
			})
		})
}

// readTraceRange 按活动 id 区间回读原始步骤（get_worker_trace 的窄带版本）。
func (t *ToolSet) readTraceRange() actool.CoreTool {
	return t.readExpTool("read_trace_range",
		"回读某条意图在某段活动 id 区间内的原始步骤（交接单里给了 trace_range，可以从中挑一段细看）。用于定位「他到底卡在哪一步」，比整段 trace 省 token。",
		obj(map[string]any{
			"intent_id": intp("意图(work) id"),
			"from":      intp("起始活动 id（含）"),
			"to":        intp("结束活动 id（含）"),
			"limit":     intp("可选：最多返回条数（默认 120，上限 300）"),
		}, "intent_id", "from", "to"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				From     json.RawMessage `json:"from"`
				To       json.RawMessage `json:"to"`
				Limit    json.RawMessage `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			iid := pid(a.IntentID)
			from, to := pid(a.From), pid(a.To)
			if iid <= 0 || from <= 0 || to <= 0 {
				return actool.Errorf("intent_id / from / to 均为必填"), nil
			}
			if to < from {
				from, to = to, from
			}
			if t.ts == nil {
				return actool.Errorf("存储未就绪"), nil
			}
			limit := int(pid(a.Limit))
			if limit <= 0 {
				limit = 120
			}
			if limit > 300 {
				limit = 300
			}
			acts, err := t.ts.ActivityTrace(iid, 2000)
			if err != nil {
				return actool.Errorf("读活动流失败: " + err.Error()), nil
			}
			out := make([]map[string]any, 0, limit)
			for _, act := range acts {
				if act.ID < from || act.ID > to {
					continue
				}
				item := map[string]any{"id": act.ID, "worker": act.Worker, "kind": act.Kind}
				if act.Tool != "" {
					item["tool"] = act.Tool
				}
				if act.IsError {
					item["is_error"] = true
				}
				if s := clip(act.Summary, 600); s != "" {
					item["summary"] = s
				}
				out = append(out, item)
				if len(out) >= limit {
					break
				}
			}
			return jsonResult(map[string]any{
				"intent_id": iid, "from": from, "to": to, "count": len(out), "activities": out,
			})
		})
}

// abandonIntent 让规划者显式放弃一条分支（反作弊版）。
//
// 与「重派」相对：abandon 是承认这条方向不做了。它必须给理由（闭集）并留审计，
// 而且 duplicate / out_of_scope 必须附证据 id——否则模型的最优解会变成
// 「把不顺利的都 abandon 掉，再 prove_goal 收工」。
func (t *ToolSet) abandonIntent() actool.CoreTool {
	return t.readExpTool("abandon_intent",
		"显式放弃一条不会再做的分支（意图）。必须给 reason_code：duplicate(与别的分支重复) / out_of_scope(超出任务范围) / infeasible(技术不可行) / superseded(已被其它分支覆盖) / unsafe(违反约束或安全边界) / other(其它，需写 note)。duplicate 与 out_of_scope 必须附 evidence_ids（指向已有的 fact/finding），否则会被拒绝。放弃只影响「这条分支还挂不挂着」，不产生任何证据，也不能让目标自动达成。",
		obj(map[string]any{
			"intent_id":    intp("要放弃的意图 id"),
			"reason_code":  str("duplicate|out_of_scope|infeasible|superseded|unsafe|other"),
			"note":         str("一句话说明为什么放弃（other 必填）"),
			"evidence_ids": str("可选：证据节点 id，逗号分隔（duplicate/out_of_scope 必填）"),
		}, "intent_id", "reason_code"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID    json.RawMessage `json:"intent_id"`
				ReasonCode  string          `json:"reason_code"`
				Note        string          `json:"note"`
				EvidenceIDs string          `json:"evidence_ids"`
			}
			_ = json.Unmarshal(in, &a)
			iid := pid(a.IntentID)
			if iid <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			if t.ts == nil || t.ts.DB() == nil {
				return actool.Errorf("存储未就绪"), nil
			}
			if err := t.requireIntentNode(iid); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			code := strings.TrimSpace(strings.ToLower(a.ReasonCode))
			if !abandonReasonCodes[code] {
				return actool.Errorf("reason_code 必须是 duplicate|out_of_scope|infeasible|superseded|unsafe|other"), nil
			}
			note := strings.TrimSpace(a.Note)
			if code == "other" && note == "" {
				return actool.Errorf("reason_code=other 时必须在 note 里写明原因"), nil
			}
			var evidence []int64
			for _, part := range strings.Split(a.EvidenceIDs, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				if n, err := strconv.ParseInt(part, 10, 64); err == nil && n > 0 {
					evidence = append(evidence, n)
				}
			}
			if (code == "duplicate" || code == "out_of_scope") && len(evidence) == 0 {
				return actool.Errorf(fmt.Sprintf("reason_code=%s 必须附 evidence_ids（指向已有的 fact/finding），否则视为绕过完成闸门", code)), nil
			}
			// 证据必须是本任务里真实存在的 fact/finding，不能是编的 id。
			for _, eid := range evidence {
				n, err := t.ts.GetNodeWithSources(eid)
				if err != nil || n == nil {
					return actool.Errorf(fmt.Sprintf("evidence_ids 里的 %d 不存在于本任务", eid)), nil
				}
				if n.Kind != db.KindFact && n.Kind != db.KindFinding {
					return actool.Errorf(fmt.Sprintf("evidence_ids 里的 %d 不是 fact/finding（是 %s）", eid, n.Kind)), nil
				}
			}
			taskID := strconv.FormatInt(t.taskID, 10)
			if t.taskID <= 0 {
				return actool.Errorf("任务上下文缺失"), nil
			}
			if t.ts.DB() == nil {
				return actool.Errorf("存储未就绪"), nil
			}
			// 已经收口过的不重复写（幂等）。
			if closed, err := t.ts.DB().IntentClosureSet(taskID); err == nil && closed[iid] {
				return actool.Errorf(fmt.Sprintf("意图 %d 已经收口过", iid)), nil
			}
			ev := make([]string, 0, len(evidence))
			for _, e := range evidence {
				ev = append(ev, strconv.FormatInt(e, 10))
			}
			if err := t.ts.DB().CloseIntent(&db.IntentClosure{
				TaskID: taskID, IntentID: iid, Actor: t.worker, Action: "abandon",
				ReasonCode: code, Note: note, EvidenceIDs: strings.Join(ev, ","),
			}); err != nil {
				return actool.Errorf("写入收口审计失败: " + err.Error()), nil
			}
			_ = t.ts.DB().DeleteRetryState(taskID, iid)
			return jsonResult(map[string]any{
				"intent_id": iid, "abandoned": true, "reason_code": code,
				"evidence_ids": evidence,
				"note":         "已记入收口审计；该分支不再算未收口，但这不会产生任何证据，目标仍需独立证据才能达成",
			})
		})
}

// requireIntentNode 确认 id 是本任务里真实存在的意图节点。
// 交接补注与放弃审计都以 intent 为键，键错了不会报错，只会让信息写进无人读的节点。
func (t *ToolSet) requireIntentNode(id int64) error {
	if id <= 0 {
		return fmt.Errorf("缺少 intent_id（当前上下文没有绑定意图）")
	}
	n, err := t.ts.GetNodeWithSources(id)
	if err != nil || n == nil {
		return fmt.Errorf("意图 %d 不存在于本任务", id)
	}
	if n.Kind != db.KindIntent {
		return fmt.Errorf("节点 %d 不是意图（是 %s）；请传 intent_id，而不是当前根节点", id, n.Kind)
	}
	return nil
}

// abandonReasonCodes 是闭集——新增理由要同时想清楚「它能不能被滥用」。
var abandonReasonCodes = map[string]bool{
	"duplicate": true, "out_of_scope": true, "infeasible": true,
	"superseded": true, "unsafe": true, "other": true,
}

// clip 截断到 n 字节（按 rune 安全切分）。
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// GetHandoffTool / HandoffPatchTool / ReadTraceRangeTool / AbandonIntentTool 供
// 跨任务编排复用（与 tools.go 里其他导出 accessor 同一套模式）。
func (t *ToolSet) GetHandoffTool() actool.CoreTool     { return t.getHandoff() }
func (t *ToolSet) HandoffPatchTool() actool.CoreTool   { return t.handoffPatch() }
func (t *ToolSet) ReadTraceRangeTool() actool.CoreTool { return t.readTraceRange() }
func (t *ToolSet) AbandonIntentTool() actool.CoreTool  { return t.abandonIntent() }

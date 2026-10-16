package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// pgListCommands returns tool executions (any tool) from the activity table.
func (s *Server) pgListCommands(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 0)
	size := atoiDefault(q.Get("size"), 50)
	keyword := q.Get("q")

	records, total, err := s.m.PG().ListCommands(commandTaskFilter(q.Get("task")), keyword, page, size)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"commands": records, "total": total})
}

// pgToolStats returns per-tool call counts for the tool-execution history, under
// the same task/keyword filters the list takes — the summary describes the whole
// filtered set, not the page on screen.
func (s *Server) pgToolStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pg := s.m.PG()
	if pg == nil {
		writeJSON(w, 200, map[string]any{"stats": []db.ToolStat{}})
		return
	}
	stats, err := pg.ToolStats(commandTaskFilter(q.Get("task")), q.Get("q"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"stats": stats})
}

// commandTaskFilter parses the ?task= exploration id. Absent or unparsable → nil
// (no filter), matching the list endpoint's long-standing lenient behaviour.
func commandTaskFilter(v string) *int64 {
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// pgListLLMRecords returns paginated LLM call records.
func (s *Server) pgListLLMRecords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 0)
	size := atoiDefault(q.Get("size"), 50)
	model := q.Get("model")
	session := q.Get("session")
	task := q.Get("task")

	records, total, err := s.m.PG().ListLLMRecords(model, session, task, page, size)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	resp := map[string]any{"records": records, "total": total}
	// 费用(读时估算)：逐行计价；另对过滤全集(非仅本页)做一次按模型的补充聚合再
	// 求和，得到 cost_total。未配置价格表时两组字段都不出现，也不发起补充查询。
	if pricing := s.pricingTable(); pricing != nil {
		for i := range records {
			if cost, ok := pricing.CostOf(records[i].Model, records[i].InputTokens, records[i].OutputTokens,
				records[i].CacheRead, records[i].CacheWrite); ok {
				records[i].Cost = cost
			}
		}
		if totals, err := s.m.PG().LLMRecordModelTotals(model, session, task); err == nil {
			totalCost := 0.0
			for _, mt := range totals {
				if cost, ok := pricing.CostOf(mt.Model, mt.InputTokens, mt.OutputTokens, mt.CacheRead, mt.CacheWrite); ok {
					totalCost += cost
				}
			}
			resp["cost_total"] = totalCost
		}
	}
	writeJSON(w, 200, resp)
}

// pgTokenByModel returns a task's LLM token usage grouped by model, from the
// always-on llm_usage metering ledger. Accurate even with per-agent model bindings,
// pool rotation/failover, and interrupted runs (every call is metered on success or
// error). No recording toggle required.
func (s *Server) pgTokenByModel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("task"))
	if id == "" {
		writeErr(w, 400, "missing task")
		return
	}
	pg := s.m.PG()
	if pg == nil {
		writeJSON(w, 200, map[string]any{"models": []db.ModelTokenStat{}})
		return
	}
	models, err := pg.TokenByModel(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 费用(读时估算)：模型维度齐全，逐行直接计价；cost_total 供任务总览显示。
	// 未配置价格表时 cost 全部为零(omitempty 省略)、cost_total 不出现。
	if pricing := s.pricingTable(); pricing != nil {
		totalCost := 0.0
		for i := range models {
			if cost, ok := pricing.CostOf(models[i].Model, models[i].InputTokens, models[i].OutputTokens,
				models[i].CacheReadTokens, models[i].CacheWriteTokens); ok {
				models[i].Cost = cost
				totalCost += cost
			}
		}
		writeJSON(w, 200, map[string]any{"models": models, "cost_total": totalCost})
		return
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// pgTokenByWorker returns a task's LLM token usage grouped by agent role (泳道),
// from the same always-on llm_usage ledger as pgTokenByModel. This is the dimension
// the by-model view cannot show: with per-agent model bindings one role's spend is
// flattened across models, while the worker column keeps "哪个角色烧的 token" intact
// (llmrec derives it from the session prefix: worker/planner/main/goals/compactor…).
func (s *Server) pgTokenByWorker(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("task"))
	if id == "" {
		writeErr(w, 400, "missing task")
		return
	}
	pg := s.m.PG()
	if pg == nil {
		writeJSON(w, 200, map[string]any{"workers": []db.WorkerTokenStat{}})
		return
	}
	workers, err := pg.TokenByWorker(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"workers": workers})
}

// pgUsageStats returns global llm_usage aggregates for the dashboard's "new" token
// view: per-profile totals (all-time) + per-(profile, day) buckets for the daily
// chart. Sourced from the always-on metering ledger, so it is accurate across
// per-agent bindings, pool rotation, and interrupted runs.
func (s *Server) pgUsageStats(w http.ResponseWriter, r *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeJSON(w, 200, map[string]any{"by_profile": []db.ProfileUsage{}, "daily": []db.ProfileDayUsage{}})
		return
	}
	days := atoiDefault(r.URL.Query().Get("days"), 365)
	byProfile, err := pg.UsageByProfile()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	daily, err := pg.UsageDaily(days)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	resp := map[string]any{"by_profile": byProfile, "daily": daily}
	// 费用(读时估算)：by_profile/daily 原 SQL 按 (profile[, day]) 分组、丢了模型
	// 维度，无法直接计价(价格按模型)。配置了价格表时改用 (profile, model) /
	// (profile, day, model) 的补充聚合逐桶计价，再把 cost_usd 折回原字段——原有
	// 字段全部保留。归档任务的聚合没有模型维度、无法计价，故 cost_usd 只覆盖活动
	// 账本。未配置价格表时字段全省略，也不发起补充查询。
	if pricing := s.pricingTable(); pricing != nil {
		if rows, err := pg.UsageByProfileModel(); err == nil {
			costByProfile := map[string]float64{}
			for _, row := range rows {
				if cost, ok := pricing.CostOf(row.Model, row.InputTokens, row.OutputTokens, row.CacheRead, row.CacheWrite); ok {
					costByProfile[row.ProfileName] += cost
				}
			}
			for i := range byProfile {
				byProfile[i].CostUSD = costByProfile[byProfile[i].ProfileName]
			}
		}
		if rows, err := pg.UsageDailyModel(days); err == nil {
			costByDay := map[string]float64{}
			for _, row := range rows {
				if cost, ok := pricing.CostOf(row.Model, row.InputTokens, row.OutputTokens, row.CacheRead, row.CacheWrite); ok {
					costByDay[row.ProfileName+"\x00"+row.Date] += cost
				}
			}
			for i := range daily {
				daily[i].CostUSD = costByDay[daily[i].ProfileName+"\x00"+daily[i].Date]
			}
		}
	}
	writeJSON(w, 200, resp)
}

// pgLLMTasks returns distinct recorded tasks with counts, for the page's task
// picker (pick a task → filter the list, then delete its conversations).
func (s *Server) pgLLMTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.m.PG().LLMTasks()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 费用(读时估算)：LLMTasks 只有 task_id + count，没有模型维度，无法直接计价；
	// 用一次 (task_id, model) 补充聚合在 Go 侧逐组计价、按任务求和。仅配置了价格
	// 表时才发起这次查询。未命中模型的费用为 0(omitempty 省略字段)。
	if pricing := s.pricingTable(); pricing != nil {
		if totals, err := s.m.PG().LLMTaskModelTotals(); err == nil {
			byTask := map[string]float64{}
			for _, tt := range totals {
				if cost, ok := pricing.CostOf(tt.Model, tt.InputTokens, tt.OutputTokens, tt.CacheRead, tt.CacheWrite); ok {
					byTask[tt.TaskID] += cost
				}
			}
			for i := range tasks {
				tasks[i].Cost = byTask[tasks[i].TaskID]
			}
		}
	}
	writeJSON(w, 200, map[string]any{"tasks": tasks})
}

// pgDeleteLLMRecords removes all LLM records for one exact task_id (the same
// match the task picker uses). Empty task → 400. Returns rows deleted.
func (s *Server) pgDeleteLLMRecords(w http.ResponseWriter, r *http.Request) {
	task := strings.TrimSpace(r.URL.Query().Get("task"))
	if task == "" {
		writeErr(w, 400, "missing task")
		return
	}
	n, err := s.m.PG().DeleteLLMRecords(task)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// pgGetLLMRecord returns a single LLM record with full request/response bodies.
func (s *Server) pgGetLLMRecord(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid id")
		return
	}
	rec, err := s.m.PG().GetLLMRecord(id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, rec)
}

// Package report renders a deliverable Markdown report from the exploration
// graph: exploration process, key facts, pitfalls/traps, and confirmed findings (docs §15 P5).
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Input bundles what the report needs.
type Input struct {
	Title       string
	Goal        string
	GeneratedAt time.Time
	AssetCounts map[string]int
	Findings    []*db.Node
	Intents     []*db.Node
	Facts       []*db.Node
	Hints       []*db.Node
	Activities  []db.Activity
}

type findingView struct {
	VulnClass string
	Name      string
	Severity  string
	Summary   string
	PoC       string
}

func parseFinding(n *db.Node) findingView {
	var p struct {
		VulnClass string `json:"vulnclass"`
		Name      string `json:"name"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Evidence  struct {
			PoC string `json:"poc"`
		} `json:"evidence"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return findingView{p.VulnClass, p.Name, p.Severity, p.Summary, p.Evidence.PoC}
}

var sevRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "": 4}

type reportIntentItem struct {
	ID      int64
	State   string
	Summary string
	Reason  string
}

type reportToolErrorStat struct {
	Tool    string
	Calls   int
	Errors  int
	Tripped bool
	Samples []string
}

func parseReportIntents(nodes []*db.Node) (done []reportIntentItem, pitfalls []reportIntentItem) {
	sorted := make([]*db.Node, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			sorted = append(sorted, n)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, n := range sorted {
		var m map[string]any
		_ = json.Unmarshal(n.Payload, &m)
		sum := firstMapString(m, "summary", "text", "name", "title")
		if sum == "" {
			sum = strings.TrimSpace(string(n.Payload))
		}
		sum = compactInline(sum, 320)
		if sum == "" {
			sum = fmt.Sprintf("意图 #%d", n.ID)
		}
		reason := strings.TrimSpace(n.BlockedReason)
		if reason == "" {
			reason = strings.TrimSpace(n.DeleteReason)
		}
		if reason == "" {
			reason = firstMapString(m, "reason", "blocked_reason", "error", "delete_reason")
		}
		reason = compactInline(reason, 360)
		st := strings.ToLower(strings.TrimSpace(n.State))
		item := reportIntentItem{
			ID:      n.ID,
			State:   st,
			Summary: sum,
			Reason:  reason,
		}
		switch st {
		case "done":
			done = append(done, item)
		case "", "open", "running", "pending":
			// still active or queued
		default:
			pitfalls = append(pitfalls, item)
		}
	}
	return done, pitfalls
}

func summarizeToolPitfalls(activities []db.Activity) ([]reportToolErrorStat, bool) {
	byTool := map[string]*reportToolErrorStat{}
	anyTripped := false
	for _, a := range activities {
		name := strings.TrimSpace(a.Tool)
		switch a.Kind {
		case "tool_use", "tool_call":
			if name == "" {
				continue
			}
			st := byTool[name]
			if st == nil {
				st = &reportToolErrorStat{Tool: name}
				byTool[name] = st
			}
			st.Calls++
		case "tool_result":
			if name == "" {
				continue
			}
			st := byTool[name]
			if st == nil {
				st = &reportToolErrorStat{Tool: name}
				byTool[name] = st
			}
			if a.IsError {
				st.Errors++
				if sample := compactInline(a.Summary, 200); sample != "" && len(st.Samples) < 2 {
					dup := false
					for _, ex := range st.Samples {
						if ex == sample {
							dup = true
							break
						}
					}
					if !dup {
						st.Samples = append(st.Samples, sample)
					}
				}
			}
			if isLoopTripSummary(a.Summary) {
				st.Tripped = true
				anyTripped = true
			}
		}
		if !anyTripped && isLoopTripSummary(a.Summary) {
			anyTripped = true
		}
	}
	var out []reportToolErrorStat
	for _, st := range byTool {
		if st.Errors > 0 || st.Tripped {
			out = append(out, *st)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Errors != out[j].Errors {
			return out[i].Errors > out[j].Errors
		}
		return out[i].Tool < out[j].Tool
	})
	return out, anyTripped
}

func isLoopTripSummary(s string) bool {
	return strings.Contains(s, "专家战术熔断提醒") ||
		strings.Contains(s, "3-Strike") ||
		strings.Contains(s, "三次相同负反馈") ||
		strings.Contains(s, "三次相同")
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func compactInline(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.Join(strings.Fields(s), " ")
	if maxRunes > 0 {
		r := []rune(s)
		if len(r) > maxRunes {
			s = string(r[:maxRunes]) + "…"
		}
	}
	return s
}

// Markdown renders the report.
func Markdown(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 渗透测试报告：%s\n\n", nz(in.Title, "未命名任务"))
	fmt.Fprintf(&b, "- **任务目标**：%s\n", nz(in.Goal, "（未指定）"))
	fmt.Fprintf(&b, "- **生成时间**：%s\n\n", in.GeneratedAt.Format("2006-01-02 15:04:05"))

	doneIntents, pitfallIntents := parseReportIntents(in.Intents)
	toolPitfalls, loopTripped := summarizeToolPitfalls(in.Activities)
	var validFacts []*db.Node
	for _, f := range in.Facts {
		if f != nil && f.State != db.StateOrigin {
			validFacts = append(validFacts, f)
		}
	}

	// summary
	fmt.Fprintf(&b, "## 概述\n\n")
	fmt.Fprintf(&b, "- 已确认漏洞：**%d** 个\n", len(in.Findings))
	if len(in.Intents) > 0 || len(validFacts) > 0 || len(pitfallIntents) > 0 || len(toolPitfalls) > 0 {
		fmt.Fprintf(&b, "- 探索路径与踩坑：已完成意图 **%d** 个，受阻/止损意图 **%d** 个，关键事实 **%d** 条\n",
			len(doneIntents), len(pitfallIntents), len(validFacts))
	}
	fmt.Fprintf(&b, "- 资产： ")
	var types []string
	for t := range in.AssetCounts {
		types = append(types, t)
	}
	sort.Strings(types)
	for i, t := range types {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %d", t, in.AssetCounts[t])
	}
	b.WriteString("\n\n")

	// Exploration process & attack chain
	if len(doneIntents) > 0 {
		b.WriteString("## 探索过程与推导链\n\n")
		for i, it := range doneIntents {
			if i >= 25 {
				fmt.Fprintf(&b, "- …（其余 %d 条已完成探索步骤已省略）\n", len(doneIntents)-i)
				break
			}
			fmt.Fprintf(&b, "%d. %s\n", i+1, it.Summary)
		}
		b.WriteString("\n")
	}

	// Key technical facts & slot triage
	if len(validFacts) > 0 {
		b.WriteString("## 关键技术事实与槽位定性\n\n")
		for i, fn := range validFacts {
			if i >= 25 {
				fmt.Fprintf(&b, "- …（其余 %d 条事实记录已省略）\n", len(validFacts)-i)
				break
			}
			var m map[string]any
			_ = json.Unmarshal(fn.Payload, &m)
			sum := compactInline(firstMapString(m, "summary", "text", "name"), 360)
			if sum == "" {
				continue
			}
			conf := firstMapString(m, "confidence")
			tag := "待核对"
			switch conf {
			case "observed":
				tag = "观察"
			case "inferred":
				tag = "推测"
			}
			fmt.Fprintf(&b, "- **[%s]** %s", tag, sum)
			if ev := compactInline(firstMapString(m, "evidence", "detail"), 240); ev != "" {
				fmt.Fprintf(&b, "（证据摘录：`%s`）", strings.ReplaceAll(ev, "`", "'"))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Pitfalls, traps, blocked intents & 3-Strike circuit-breaker lessons
	if len(pitfallIntents) > 0 || len(toolPitfalls) > 0 || loopTripped {
		b.WriteString("## 踩坑记录、受阻陷阱与 3-Strike 止损\n\n")
		b.WriteString("> 以下记录本任务在渗透与逆向推演中遇到的无效方向、防御陷阱与工具报错止损点，供后续复盘与沉淀技能包防错使用。\n\n")
		for i, it := range pitfallIntents {
			if i >= 20 {
				break
			}
			reason := it.Reason
			if reason == "" {
				reason = "前置条件未满足或目标防御拦截，已止损切换方向"
			}
			fmt.Fprintf(&b, "- **[意图%s]** %s —— **踩坑/止损原因**：%s\n",
				strings.ToUpper(it.State), it.Summary, reason)
		}
		for i, tp := range toolPitfalls {
			if i >= 10 {
				break
			}
			fmt.Fprintf(&b, "- **[工具异常 · `%s`]** 累计调用 %d 次，失败 %d 次", tp.Tool, tp.Calls, tp.Errors)
			if tp.Tripped {
				b.WriteString("（触发 3-Strike 同键止损熔断）")
			}
			if len(tp.Samples) > 0 {
				fmt.Fprintf(&b, "；典型报错特征：%s", strings.Join(tp.Samples, "；"))
			}
			b.WriteString("\n")
		}
		if loopTripped {
			b.WriteString("- **[3-Strike 止损铁律]** 同一端点/槽位连续三次返回相同负反馈（固定错误码、长度无变化或 WAF 拦截）时，严禁原样重放，必须切换鉴权上下文、HTTP 方法、编码方式或转向相邻业务端点。\n")
		}
		b.WriteString("\n")
	}

	// Human hints & steering
	if len(in.Hints) > 0 {
		b.WriteString("## 人工提示与战术转向\n\n")
		for i, h := range in.Hints {
			if h == nil || i >= 12 {
				continue
			}
			var m map[string]any
			_ = json.Unmarshal(h.Payload, &m)
			txt := compactInline(firstMapString(m, "text", "summary"), 360)
			if txt != "" {
				fmt.Fprintf(&b, "- %s\n", txt)
			}
		}
		b.WriteString("\n")
	}

	// findings
	fmt.Fprintf(&b, "## 漏洞\n\n")
	if len(in.Findings) == 0 {
		b.WriteString("_本次没有已确认漏洞。_\n\n")
	} else {
		fs := make([]findingView, 0, len(in.Findings))
		for _, n := range in.Findings {
			fs = append(fs, parseFinding(n))
		}
		sort.SliceStable(fs, func(i, j int) bool { return sevRank[fs[i].Severity] < sevRank[fs[j].Severity] })
		for i, f := range fs {
			fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")))
			fmt.Fprintf(&b, "%s\n\n", nz(f.Summary, ""))
			if f.PoC != "" {
				fmt.Fprintf(&b, "**PoC / 证据：**\n\n```\n%s\n```\n\n", f.PoC)
			}
		}
	}

	return b.String()
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

package report

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// SoftwareInput reuses existing task-scoped facts without claiming that they are
// structured RE evidence objects or verified runtime observations.
type SoftwareInput struct {
	Title       string
	Goal        string
	GeneratedAt time.Time
	Goals       []*db.Node
	Facts       []*db.Node
	Intents     []*db.Node
	Hints       []*db.Node
	Activities  []db.Activity
	Samples     []db.RESample
	Sessions    []db.RESession
	Evidences   []db.REEvidence
	Truncated   bool
}

// SoftwareMarkdown renders a non-vulnerability deliverable. The persisted
// node state "confirmed" is NOT interpreted as verified program behavior.
func SoftwareMarkdown(in SoftwareInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 软件逆向分析记录：%s\n\n", reportText(nz(in.Title, "未命名任务"), 512))
	fmt.Fprintf(&b, "- **任务目标**：%s\n", reportText(in.Goal, 4000))
	fmt.Fprintf(&b, "- **生成时间**：%s\n", in.GeneratedAt.Format("2006-01-02 15:04:05"))
	b.WriteString("- **报告性质**：任务事实与证据摘录的汇总，不等同于完整反编译或动态验证。无漏洞不代表任务失败。\n\n")
	if in.Truncated {
		b.WriteString("> 当前报告仅包含最近最多 200 条事实、100 个目标；更早记录请在图谱和会话中查询。\n\n")
	}
	b.WriteString("## 交付目标\n\n")
	if len(in.Goals) == 0 {
		b.WriteString("尚未登记拆解后的交付目标。\n\n")
	}
	for _, n := range in.Goals {
		if n == nil {
			continue
		}
		var p struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		state := "待完成"
		if n.State == "met" {
			state = "已标记达成（请核对证明链）"
		}
		fmt.Fprintf(&b, "- #%d · %s · %s\n", n.ID, state, reportText(p.Text, 2000))
	}

	doneIntents, pitfallIntents := parseReportIntents(in.Intents)
	toolPitfalls, loopTripped := summarizeToolPitfalls(in.Activities)

	if len(doneIntents) > 0 {
		b.WriteString("\n## 逆向推导过程与验证路径\n\n")
		for i, it := range doneIntents {
			if i >= 25 {
				fmt.Fprintf(&b, "- …（其余 %d 条已完成逆向步骤已省略）\n", len(doneIntents)-i)
				break
			}
			fmt.Fprintf(&b, "%d. %s\n", i+1, reportText(it.Summary, 400))
		}
	}

	if len(pitfallIntents) > 0 || len(toolPitfalls) > 0 || loopTripped {
		b.WriteString("\n## 逆向踩坑记录、误区陷阱与止损\n\n")
		b.WriteString("> 以下记录逆向分析过程中遇到的壳/混淆陷阱、工具报错、死胡同与止损切换点，供后续复盘与沉淀技能包防错使用。\n\n")
		for i, it := range pitfallIntents {
			if i >= 20 {
				break
			}
			reason := it.Reason
			if reason == "" {
				reason = "静态/动态分析受阻，已止损切换路径"
			}
			fmt.Fprintf(&b, "- **[意图%s]** %s —— **踩坑/止损原因**：%s\n",
				strings.ToUpper(it.State), reportText(it.Summary, 320), reportText(reason, 360))
		}
		for i, tp := range toolPitfalls {
			if i >= 10 {
				break
			}
			fmt.Fprintf(&b, "- **[工具异常 · `%s`]** 累计调用 %d 次，失败 %d 次", reportText(tp.Tool, 80), tp.Calls, tp.Errors)
			if tp.Tripped {
				b.WriteString("（触发 3-Strike 同键止损熔断）")
			}
			if len(tp.Samples) > 0 {
				fmt.Fprintf(&b, "；典型报错特征：%s", reportText(strings.Join(tp.Samples, "；"), 300))
			}
			b.WriteString("\n")
		}
		if loopTripped {
			b.WriteString("- **[3-Strike 止损铁律]** 同一静态/动态分析命令连续三次失败或无新增符号/控制流信息时，必须切换定位锚点（字符串/导入表/交叉引用/动态断点），禁止原样重试。\n")
		}
	}

	if len(in.Hints) > 0 {
		b.WriteString("\n## 人工提示与逆向转向\n\n")
		for i, h := range in.Hints {
			if h == nil || i >= 12 {
				continue
			}
			var m map[string]any
			_ = json.Unmarshal(h.Payload, &m)
			txt := compactInline(firstMapString(m, "text", "summary"), 360)
			if txt != "" {
				fmt.Fprintf(&b, "- %s\n", reportText(txt, 360))
			}
		}
	}

	b.WriteString("\n## 分析事实与证据\n\n")
	count := 0
	for _, n := range in.Facts {
		if n == nil || n.State == "origin" {
			continue
		}
		var p struct {
			Summary    string `json:"summary"`
			Detail     string `json:"detail"`
			Evidence   string `json:"evidence"`
			Confidence string `json:"confidence"`
		}
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		count++
		confidence := "可信程度未标注，待核对"
		switch p.Confidence {
		case "observed":
			confidence = "观察记录（不自动等于运行验证）"
		case "inferred":
			confidence = "推测，待验证"
		}
		fmt.Fprintf(&b, "### 事实 #%d：%s\n\n", n.ID, reportText(nz(p.Summary, "未命名记录"), 512))
		fmt.Fprintf(&b, "**认知标签**：%s\n\n", confidence)
		if strings.TrimSpace(p.Detail) != "" {
			fmt.Fprintf(&b, "%s\n\n", reportText(p.Detail, 8000))
		}
		b.WriteString("**证据摘录**：\n\n")
		if strings.TrimSpace(p.Evidence) == "" {
			b.WriteString("未附证据；不得仅凭此条记录判定行为已验证。\n\n")
		} else {
			// Indented text cannot terminate a fence supplied by an untrusted sample.
			text := reportText(p.Evidence, 8000)
			for _, line := range strings.Split(text, "\n") {
				b.WriteString("    " + line + "\n")
			}
			b.WriteString("\n")
		}
	}
	if count == 0 {
		b.WriteString("尚无分析事实。请先识别样本，再记录与用户问题相关的观察与证据。\n\n")
	}
	if len(in.Samples) > 0 || len(in.Sessions) > 0 || len(in.Evidences) > 0 {
		b.WriteString("## 样本库与分析会话摘要\n\n")
		if len(in.Samples) > 0 {
			b.WriteString("### 登记样本\n\n")
			for _, s := range in.Samples {
				fmt.Fprintf(&b, "- **%s** · SHA-256: `%s` · 大小: %d 字节\n", reportText(nz(s.Name, "未命名样本"), 256), reportText(s.SHA256, 64), s.Size)
			}
			b.WriteString("\n")
		}
		if len(in.Sessions) > 0 {
			b.WriteString("### 分析会话\n\n")
			for _, s := range in.Sessions {
				fmt.Fprintf(&b, "- 会话 #%d · 样本 `%s` · 状态: `%s`", s.ID, reportText(s.SampleSHA, 64), reportText(s.State, 32))
				if strings.TrimSpace(s.Message) != "" {
					fmt.Fprintf(&b, " · %s", reportText(s.Message, 512))
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		if len(in.Evidences) > 0 {
			b.WriteString("### 最近结构化证据索引\n\n")
			for _, e := range in.Evidences {
				fmt.Fprintf(&b, "- 证据 #%d（会话 #%d）· 操作 `%s` (`%s`) · 输出 SHA-256: `%s` · 错误=%t · 截断=%t\n",
					e.ID, e.SessionID, reportText(e.Operation, 64), reportText(e.Tool, 128), reportText(e.OutputSHA, 64), e.IsError, e.Truncated)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("## 范围与限制\n\n- 本报告不自动验证样本身份、地址映射或证据真实性；应核对 SHA-256、模块、地址类型与原始工具输出。\n- 基础样本检查不执行样本，也不提供完整函数或类型恢复。深度能力以实际配置的 Provider 为准。\n- 推测与缺少证据的记录仍需后续验证；本报告未将它们提升为已确认结论。\n- 专用样本库、分析会话及结构化证据管理属于后续阶段。\n")
	return b.String()
}

func reportText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit]) + "\n…（正文截断，完整内容请查原始事实节点）"
	}
	// Keep embedded sample HTML as text in Markdown renderers.
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

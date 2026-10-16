package notify

import (
	"fmt"
	"strings"
)

// 本文件渲染两类**任务级事件**的共享部分：task_status_changed（任务完成/失败/
// 超时/暂停）与 approval_pending（工具调用在等待人工审批）。
//
// 它们与漏洞事件的区别不只是字段不同：漏洞消息的主角是「级别」，任务事件没有
// 级别概念，主角是「发生了什么、哪个任务、为什么」——人必须知道，否则任务会
// 一直卡在等处理的状态里。因此判类型、取标题、拼字段都收敛在这里，六个渠道
// 渲染器只负责各自的转义与排版。

// maxTaskTitleRunes 限制任务标题在消息里的长度。name 为空时标题回退到整段
// description，不截断会把消息头挤爆、把正文顶出可视区。
const maxTaskTitleRunes = 60

// isTaskKind 报告事件类型是否属于任务级事件（无严重级别语义）。
func isTaskKind(kind string) bool {
	return kind == EventTaskStatusChanged || kind == EventApprovalPending
}

// taskEvent 报告条目是否为任务级事件。空 Kind（旧版快照）按漏洞语义处理。
func taskEvent(it Item) bool { return isTaskKind(it.Kind) }

// taskStatusLabels 是任务状态的中文展示名。done/failed/timeout/paused 是
// emit 侧允许的全部取值；未知值原样回显，不臆造。
var taskStatusLabels = map[string]map[string]string{
	"zh": {"done": "已完成", "failed": "失败", "timeout": "已超时", "paused": "已暂停"},
}

// TaskStatusLabel 把任务状态翻译成当前输出语言的展示名。未知状态原样回显。
func TaskStatusLabel(status string) string {
	if v, ok := taskStatusLabels[labelLang()][status]; ok {
		return v
	}
	return status
}

// taskTitle 返回任务展示名：快照里的标题优先，缺失时退化为「任务 #ID」——
// 通知绝不允许出现空标题。回退不用「未知任务」当主线，ID 才是回链能定位的东西，
// 只有在连 ID 都没有时才用它。
func taskTitle(it Item) string {
	if t := strings.TrimSpace(it.TaskTitle); t != "" {
		return TruncateRunes(OneLine(t, 0), maxTaskTitleRunes)
	}
	if it.TaskID > 0 {
		return fmt.Sprintf("任务 #%d", it.TaskID)
	}
	return "未知任务"
}

// taskRefText 返回「标题（#ID）」形式的任务引用：标题让人认出是哪个任务，
// ID 让它能被精确定位（同名任务不混淆）。标题缺失时只回显回退名。
func taskRefText(it Item) string {
	title := taskTitle(it)
	if it.TaskID > 0 && strings.TrimSpace(it.TaskTitle) != "" {
		return fmt.Sprintf("%s（#%d）", title, it.TaskID)
	}
	return title
}

// taskEventTitle 返回任务级事件的标题（未转义，转义由各渠道负责）：
// task_status_changed → 「任务状态变化：{任务标题}」；approval_pending → 「等待人工审批」。
func taskEventTitle(it Item) string {
	if it.Kind == EventApprovalPending {
		return "等待人工审批"
	}
	return "任务状态变化：" + taskTitle(it)
}

// detailLinkLabel 返回回链文案：审批问「去哪办」，任务问「看进展」，
// 漏洞沿用「查看详情」。
func detailLinkLabel(it Item) string {
	switch it.Kind {
	case EventApprovalPending:
		return "前往审批"
	case EventTaskStatusChanged:
		return "查看任务"
	default:
		return "查看详情"
	}
}

// taskEventFields 返回任务级事件的正文键值对（按展示顺序，值为未转义原文，
// 转义由各渠道负责）。空的可选项直接跳过，不输出「工具：」这类空行。
func taskEventFields(it Item) [][2]string {
	if it.Kind == EventApprovalPending {
		var out [][2]string
		if it.ToolName != "" {
			out = append(out, [2]string{"工具", it.ToolName})
		}
		if it.TaskID > 0 || strings.TrimSpace(it.TaskTitle) != "" {
			out = append(out, [2]string{"任务", taskRefText(it)})
		}
		if it.ConversationID > 0 {
			out = append(out, [2]string{"会话", fmt.Sprintf("#%d", it.ConversationID)})
		}
		return out
	}
	out := [][2]string{{"状态", TaskStatusLabel(it.TaskStatus)}}
	if it.TaskID > 0 || strings.TrimSpace(it.TaskTitle) != "" {
		out = append(out, [2]string{"任务", taskRefText(it)})
	}
	if it.StatusReason != "" {
		out = append(out, [2]string{"原因", it.StatusReason})
	}
	return out
}

// taskEventLine 把任务级事件压成一行（未转义），供汇总列表使用——
// 汇总里每条只能占一行，否则几十条会把消息撑成一篇文章。
func taskEventLine(it Item) string {
	if it.Kind == EventApprovalPending {
		line := "等待人工审批"
		if it.ToolName != "" {
			line += " · 工具 " + it.ToolName
		}
		hasRef := it.TaskID > 0 || strings.TrimSpace(it.TaskTitle) != ""
		if hasRef {
			line += " · " + taskRefText(it)
		} else if it.ConversationID > 0 {
			line += fmt.Sprintf(" · 会话 #%d", it.ConversationID)
		}
		return line
	}
	line := "任务状态变化 · " + TaskStatusLabel(it.TaskStatus) + " · " + taskRefText(it)
	if it.StatusReason != "" {
		line += " — " + it.StatusReason
	}
	return line
}

package server

import (
	"fmt"
	"strings"
	"testing"
)

// engine.go、engine_timeout.go、goals.go 的展示活动摘要中文化回归测试。
// 这些任务级摘要没有 node_id，不会被 planner.workerOutput、get_worker_output
// 按意图 node_id 回读 result、text，因此不改变模型输入。
// 完成任务目标等种子意图和未命名任务等模型输入保持既有中文，不在本测试范围。
// 复用 assertChineseError。[[G132]]
func TestRuntimeActivitySummariesLocalized(t *testing.T) {
	// 无格式参数的固定摘要，直接检查其为中文。
	plain := map[string]string{
		"goalless_task_done": goallessTaskDoneSummary,
		"goal_breakdown_r0":  goalBreakdownRound0Summary,
		"queued_no_llm":      queuedNoLLMSummary,
		"queued_fifo":        queuedFIFOSummary,
	}
	for label, msg := range plain {
		assertChineseError(t, label, msg)
	}

	// 包含 %d 的模板保留格式参数，真实格式化后检查中文及数值已填入。
	fmtCases := map[string]string{
		"planner_round":       plannerRoundSummaryFmt,
		"timeout_final_round": timeoutFinalRoundSummaryFmt,
		"queued_conc_limit":   queuedConcurrencyLimitSummaryFmt,
	}
	for label, f := range fmtCases {
		if !strings.Contains(f, "%d") {
			t.Fatalf("%s：格式占位符 %%d 丢失：%q", label, f)
		}
		got := fmt.Sprintf(f, 7)
		assertChineseError(t, label, got)
		if !strings.Contains(got, "7") {
			t.Fatalf("%s：格式参数未反映在结果中：%q", label, got)
		}
	}
}

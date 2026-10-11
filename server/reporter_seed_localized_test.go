package server

import "testing"

// TestReporterSeedLabelsLocalized 检查 reporterAgentName 与 reporterAgentDescription 的中文 UI 标签。
// 它们通过 agentDTO 显示，不进入 renderSystem 或基于模型的选择；
// report_finding 的 OnToolCall 触发使用 reporter key，保持确定性。
// ReporterDefaultPrompt、reporterToolCallMessage 保留模型输入格式，不在本测试范围。
// 复用 assertChineseError。[[F35]] [[G133]]
func TestReporterSeedLabelsLocalized(t *testing.T) {
	assertChineseError(t, "reporter_name", reporterAgentName)
	assertChineseError(t, "reporter_description", reporterAgentDescription)
}

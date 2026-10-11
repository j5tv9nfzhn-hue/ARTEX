package server

import "testing"

// 任务恢复前置校验中文化回归测试，复用 assertChineseError。
// admitTaskWhen(requirePaused=true) 先经过 concMu、任务映射和引擎门禁，
// 无数据库、引擎时固定两个常量。它们只用于单条 409 与批量 items[].error；
// 编排 action=pause 不进入恢复分支，故不是模型工具输入。
func TestGoalResumeErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg string
	}{
		{"resume_terminal", errGoalResumeTerminal},
		{"resume_not_paused", errGoalResumeNotPaused},
	} {
		assertChineseError(t, c.label, c.msg)
	}
}

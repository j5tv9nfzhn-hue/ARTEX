package server

import "testing"

// 复测 HTTP 错误中文化回归测试，复用 assertChineseError。
// startFindingRetest 先通过 s.pg 数据库门禁，无数据库时固定四类常量。
// 工具说明、参数、工具结果和内置 Agent 种子保留中文，不在这些 HTTP 断言范围。
func TestFindingRetestErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg string
	}{
		{"notes_too_long", errFindingRetestNotesTooLong},
		{"agent_missing", errFindingRetestAgentMissing},
		{"tool_required", errFindingRetestToolRequired},
		{"service_stopping", errFindingRetestServiceStopping},
	} {
		assertChineseError(t, c.label, c.msg)
	}
}

package server

import "testing"

// 流量证据 HTTP 错误中文化回归测试，复用 assertChineseError。
// 四类错误位于 s.m.pg.GetFinding 数据库门禁之后，无数据库时固定常量。
// 共享预览的校验、二进制占位符、get_finding_traffic 工具描述保留模型输入语义。
func TestFindingTrafficErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg string
	}{
		{"inherited_readonly", errFindingTrafficInheritedReadonly},
		{"select_required", errFindingTrafficSelectRequired},
		{"version_required", errFindingTrafficVersionRequired},
		{"binding_ids_required", errFindingTrafficBindingIDsRequired},
	} {
		assertChineseError(t, c.label, c.msg)
	}
}

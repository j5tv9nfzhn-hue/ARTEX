package server

import "testing"

// Worker 数量校验中文化回归测试，复用 assertChineseError。
// SetWorkers(n<=0) 在 m.pg.SetSetting 前返回，空 Manager 即可测试。
// 该消息仅从设置 PUT 显示为 HTTP 400，不进入模型工具；启动恢复诊断不在此断言范围。
func TestSetWorkersErrorLocalized(t *testing.T) {
	err := (&Manager{}).SetWorkers(0)
	if err == nil {
		t.Fatal("Worker 数量为 0 时应拒绝")
	}
	assertChineseError(t, "set_workers_nonpositive", err.Error())
}

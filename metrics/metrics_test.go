package metrics

import "testing"

// TestToolErrorRateExcludesTransport 固定仪表盘口径：transport（网络/代理）不计入
// 「工具错误率」，但保留在 raw_error_rate 与 by_stage 里可追溯。
func TestToolErrorRateExcludesTransport(t *testing.T) {
	mu.Lock()
	tools = nil
	mu.Unlock()

	// 4 次调用：2 次成功、1 次代理 EOF、1 次工具自身失败。
	ObserveToolCall("http_request")
	ObserveToolCall("http_request")
	ObserveToolCall("http_request")
	ObserveToolError("http_request", StageTransport)
	ObserveToolCall("Bash")
	ObserveToolError("Bash", StageExec)

	snap := TakeSnapshot()
	if snap.Tools.Calls != 4 {
		t.Fatalf("调用数 = %d，want 4", snap.Tools.Calls)
	}
	if snap.Tools.Errors != 1 {
		t.Fatalf("自致失败 = %d，want 1（transport 不计）", snap.Tools.Errors)
	}
	if snap.Tools.TransportErrors != 1 {
		t.Fatalf("transport = %d，want 1", snap.Tools.TransportErrors)
	}
	if snap.Tools.RawErrors != 2 {
		t.Fatalf("含环境失败 = %d，want 2", snap.Tools.RawErrors)
	}
	if got, want := snap.Tools.ErrorRate, 0.25; got != want {
		t.Fatalf("error_rate = %v，want %v", got, want)
	}
	if got, want := snap.Tools.RawErrorRate, 0.5; got != want {
		t.Fatalf("raw_error_rate = %v，want %v", got, want)
	}
	if snap.Tools.ByStage[StageTransport] != 1 || snap.Tools.ByStage[StageExec] != 1 {
		t.Fatalf("by_stage = %v，want transport=1 exec=1", snap.Tools.ByStage)
	}
	// top_tools 按自致失败排序：Bash(1) 在 http_request(0) 之前。
	if len(snap.Tools.TopTools) != 2 || snap.Tools.TopTools[0].Tool != "Bash" {
		t.Fatalf("top_tools 排序不符：%+v", snap.Tools.TopTools)
	}
	if snap.Tools.TopTools[1].Transport != 1 || snap.Tools.TopTools[1].Errors != 0 {
		t.Fatalf("http_request 的 transport 未单列：%+v", snap.Tools.TopTools[1])
	}
}

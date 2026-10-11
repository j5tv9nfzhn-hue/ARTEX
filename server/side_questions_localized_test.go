package server

import "testing"

// TestSideQuestionAPIErrorsLocalized 检查 writeErr 与 e.Error 展示的旁问消息
// 包含汉字且没有韩文，与 sidequestion 包共同显示的面板统一语言，复用 assertChineseError。
func TestSideQuestionAPIErrorsLocalized(t *testing.T) {
	for _, c := range []struct{ label, msg string }{
		{"ctx_not_saved", sideErrCtxNotSaved},
		{"model_config_changed", sideErrModelConfigChanged},
		{"service_unavailable", sideErrServiceUnavailable},
		{"task_archived", sideErrTaskArchived},
		{"worker_deleted", sideErrWorkerDeleted},
		{"bad_question", sideErrBadQuestion},
		{"task_archiving", sideErrTaskArchiving},
		{"request_id_reused", sideErrRequestIDReused},
		{"no_snapshot", sideErrNoSnapshot},
		{"concurrency_limit", sideErrConcurrencyLimit},
		{"answer_stopped", sideErrAnswerStopped},
		{"answer_timeout", sideErrAnswerTimeout},
	} {
		assertChineseError(t, c.label, c.msg)
	}
}

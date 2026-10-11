package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Worker 干预 API 中文化回归测试，复用 assertChineseError。
// 固定十一类响应常量；生命周期与意图状态依赖 engine、Store，
// 无数据库时检查常量，下面四类输入校验测试覆盖真实 HTTP 响应。
func TestIntentInterventionErrorConstantsLocalized(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"request_too_large", errIntentRequestTooLarge},
		{"message_empty", errIntentMessageEmpty},
		{"message_too_long", errIntentMessageTooLong},
		{"bad_request_id", errIntentBadRequestID},
		{"task_deleting", errIntentTaskDeleting},
		{"task_paused", errIntentTaskPaused},
		{"task_queued", errIntentTaskQueued},
		{"task_terminal", errIntentTaskTerminal},
		{"task_settling", errIntentTaskSettling},
		{"inherited_readonly", errIntentInheritedReadonly},
		{"not_paused", errIntentNotPaused},
	}
	for _, c := range cases {
		assertChineseError(t, c.name, c.msg)
	}
}

// TestSendWorkerMessageInputValidationLocalized 检查四类输入验证的真实正文。
// 只读取 s.m.Task 内存映射与请求，无需 engine、Store、数据库，
// tasks 中放一个任务即可验证常量到响应的连线。
func TestSendWorkerMessageInputValidationLocalized(t *testing.T) {
	s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}}

	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/t1/intents/1/message", strings.NewReader(body))
		req.SetPathValue("id", "t1")
		req.SetPathValue("iid", "1")
		rec := httptest.NewRecorder()
		s.sendWorkerMessage(rec, req)
		return rec
	}
	errBody := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		var resp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析响应 JSON 失败：%v（正文 %q）", err, rec.Body.String())
		}
		return resp.Error
	}

	cases := []struct {
		name string
		body string
		code int
		want string
	}{
		// 超过 64KB 限制的有效 JSON；MaxBytesReader 在读取时返回超限。
		{"request_too_large", `{"message":"` + strings.Repeat("a", maxWorkerMessageBytes+1024) + `"}`, http.StatusRequestEntityTooLarge, errIntentRequestTooLarge},
		{"message_empty", `{"message":"  "}`, http.StatusBadRequest, errIntentMessageEmpty},
		{"message_too_long", `{"message":"` + strings.Repeat("中", 4001) + `"}`, http.StatusBadRequest, errIntentMessageTooLong},
		{"bad_request_id", `{"message":"你好","request_id":"包含 空格"}`, http.StatusBadRequest, errIntentBadRequestID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := call(c.body)
			if rec.Code != c.code {
				t.Fatalf("状态码 = %d，预期 = %d（正文 %q）", rec.Code, c.code, rec.Body.String())
			}
			if got := errBody(t, rec); got != c.want {
				t.Fatalf("响应文案 = %q，预期 = %q", got, c.want)
			}
			assertChineseError(t, c.name, errBody(t, rec))
		})
	}
}

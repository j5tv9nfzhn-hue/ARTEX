package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 目标管理 API 中文化回归测试，复用 assertChineseError 和 decodeErrorField。
// TestGoalErrorConstantsLocalized 检查七类常量；目标查询、保存需要 Store、数据库，
// 无数据库时固定常量，输入验证和删除屏障由下面的 HTTP 测试执行。
func TestGoalErrorConstantsLocalized(t *testing.T) {
	cases := map[string]string{
		"task_deleting_add":    errGoalTaskDeletingAdd,
		"task_deleting_edit":   errGoalTaskDeletingEdit,
		"task_deleting_delete": errGoalTaskDeletingDelete,
		"text_empty":           errGoalTextEmpty,
		"not_found":            errGoalNotFound,
		"read_after_add":       errGoalReadAfterAdd,
		"read_after_edit":      errGoalReadAfterEdit,
	}
	for label, msg := range cases {
		assertChineseError(t, label, msg)
	}
}

// TestGoalHandlersResponsesLocalized 检查实际 HTTP 错误正文。
// 三个处理器先读任务映射、检查删除屏障、再验证正文；
// tasks 中一个任务与空 Engine 即可，不访问 Store 或数据库。
func TestGoalHandlersResponsesLocalized(t *testing.T) {
	// 已建立 deleting 屏障：beginTaskOperation 返回 false，HTTP 409。
	deletingServer := func() *Server {
		s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
		s.engine.deleting.Store("t1", true)
		return s
	}
	// 无删除屏障：beginTaskOperation 返回 true，继续输入验证。
	liveServer := func() *Server {
		return &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
	}

	newReq := func(body, gid string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/t1/goals", strings.NewReader(body))
		req.SetPathValue("id", "t1")
		if gid != "" {
			req.SetPathValue("gid", gid)
		}
		return req
	}

	cases := []struct {
		name    string
		handler func(*Server) http.HandlerFunc
		server  func() *Server
		body    string
		gid     string
		code    int
		want    string
	}{
		{
			name:    "add/task-deleting",
			handler: func(s *Server) http.HandlerFunc { return s.addGoal },
			server:  deletingServer,
			body:    `{"text":"证明 SQL 注入"}`,
			code:    http.StatusConflict,
			want:    errGoalTaskDeletingAdd,
		},
		{
			name:    "add/text-empty",
			handler: func(s *Server) http.HandlerFunc { return s.addGoal },
			server:  liveServer,
			body:    `{"text":"   "}`,
			code:    http.StatusBadRequest,
			want:    errGoalTextEmpty,
		},
		{
			name:    "edit/task-deleting",
			handler: func(s *Server) http.HandlerFunc { return s.editGoal },
			server:  deletingServer,
			body:    `{"text":"修改后的目标"}`,
			gid:     "5",
			code:    http.StatusConflict,
			want:    errGoalTaskDeletingEdit,
		},
		{
			name:    "edit/text-empty",
			handler: func(s *Server) http.HandlerFunc { return s.editGoal },
			server:  liveServer,
			body:    `{"text":"  "}`,
			gid:     "5",
			code:    http.StatusBadRequest,
			want:    errGoalTextEmpty,
		},
		{
			name:    "delete/task-deleting",
			handler: func(s *Server) http.HandlerFunc { return s.deleteGoal },
			server:  deletingServer,
			body:    ``,
			gid:     "5",
			code:    http.StatusConflict,
			want:    errGoalTaskDeletingDelete,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.server()
			rec := httptest.NewRecorder()
			c.handler(s)(rec, newReq(c.body, c.gid))
			if rec.Code != c.code {
				t.Fatalf("状态码 = %d，预期 = %d（正文 %q）", rec.Code, c.code, rec.Body.String())
			}
			got := decodeErrorField(t, rec.Body.Bytes())
			if got != c.want {
				t.Fatalf("响应文案 = %q，预期 = %q", got, c.want)
			}
			assertChineseError(t, c.name, got)
		})
	}
}

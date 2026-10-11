package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// server.go 用户响应中文化回归测试，复用 assertChineseError、decodeErrorField。
// 覆盖 writeErr 与格式化错误、聊天不可用原因、LLM 与网页搜索检查，以及 fallbackChat
// 的意图、提示确认和状态摘要。模型输入与工具标识不在这些 HTTP 常量断言范围。
// TestServerErrorConstantsLocalized 固定常量，并用真实参数填充格式模板。
func TestServerErrorConstantsLocalized(t *testing.T) {
	plain := map[string]string{
		"intent_control":        errTaskDeletingIntentControl,
		"intent_rerun":          errTaskDeletingIntentRerun,
		"intent_not_rerunnable": errIntentNotRerunnable,
		"source_invalid":        errCreateTaskSourceInvalid,
		"intercept_rules":       errCreateTaskInterceptRules,
		"category_invalid":      errCreateTaskCategoryInvalid,
		"company_invalid":       errCreateTaskCompanyInvalid,
		"llm_profile_invalid":   errLLMProfileInvalid,
		"asset_store_disabled":  errAssetStoreDisabled,
		"asset_id_required":     errAssetIDRequired,
		"python_not_detected":   errPythonNotDetected,
		"notify_base_url":       errNotifyBaseURLScheme,
		"notify_digest":         errNotifyDigestRange,
		"new_session":           errTaskDeletingNewSession,
		"new_message":           errTaskDeletingNewMessage,
		"main_agent_busy":       errMainAgentBusy,
		// F34：不经过 writeErr 的用户可见响应。
		"chat_no_profile":       errChatNoLLMProfile,
		"chat_no_active":        errChatNoActiveLLMProfile,
		"chat_not_ready":        errChatLLMNotReady,
		"llm_test_no_api_key":   errLLMTestNoAPIKey,
		"web_search_no_results": errWebSearchProbeNoResults,
		// fallbackChat 的两类确认响应，后面拼接用户输入文本。
		"fallback_intent": fallbackIntentInjected,
		"fallback_hint":   fallbackHintRecorded,
	}
	for label, msg := range plain {
		assertChineseError(t, label, msg)
	}

	formatted := map[string]string{
		"source_limit":     fmt.Sprintf(errCreateTaskSourceLimit, 8),
		"source_not_found": fmt.Sprintf(errCreateTaskSourceNotFound, 999),
		"company_limit":    fmt.Sprintf(errCreateTaskCompanyLimit, 32),
		"llm_profile_404":  fmt.Sprintf(errLLMProfileNotFound, 7),
		// 规则模式状态摘要，填入资产、待处理意图、已确认漏洞数量。
		"fallback_status": fmt.Sprintf(fallbackChatStatus, 3, 2, 1),
	}
	for label, msg := range formatted {
		assertChineseError(t, label, msg)
	}
}

// TestServerHandlersResponsesLocalized 检查不访问数据库的实际 HTTP 正文。
// 依赖数据库、引擎、资产库的路径只固定常量，确保响应连线与文案检查均有效。
func TestServerHandlersResponsesLocalized(t *testing.T) {
	// 服务器已建立删除屏障，beginTaskOperation、IsDeleting 判定为删除中。
	deletingServer := func() *Server {
		s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
		s.engine.deleting.Store("t1", true)
		return s
	}
	// 没有删除屏障的服务器继续进行输入验证。
	liveServer := func() *Server {
		return &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
	}

	cases := []struct {
		name    string
		req     func() *http.Request
		handler func(*Server) http.HandlerFunc
		server  func() *Server
		code    int
		want    string
	}{
		{
			name: "controlIntent/task-deleting",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/api/tasks/t1/intents/5/control", strings.NewReader(`{"action":"pause"}`))
				r.SetPathValue("id", "t1")
				r.SetPathValue("iid", "5")
				return r
			},
			handler: func(s *Server) http.HandlerFunc { return s.controlIntent },
			server:  deletingServer,
			code:    http.StatusConflict,
			want:    errTaskDeletingIntentControl,
		},
		{
			name: "rerunIntent/task-deleting",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/api/tasks/t1/intents/5/rerun", nil)
				r.SetPathValue("id", "t1")
				r.SetPathValue("iid", "5")
				return r
			},
			handler: func(s *Server) http.HandlerFunc { return s.rerunIntent },
			server:  deletingServer,
			code:    http.StatusConflict,
			want:    errTaskDeletingIntentRerun,
		},
		{
			name: "rerunBlocked/task-deleting",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/api/tasks/t1/intents/rerun-blocked", nil)
				r.SetPathValue("id", "t1")
				return r
			},
			handler: func(s *Server) http.HandlerFunc { return s.rerunBlocked },
			server:  deletingServer,
			code:    http.StatusConflict,
			want:    errTaskDeletingIntentRerun,
		},
		{
			name: "createTask/source-limit",
			req: func() *http.Request {
				// MaxTaskSourceCount=8，九条来源在进入循环前返回超限错误。
				r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(
					`{"goal":"目标","source_task_ids":["1","2","3","4","5","6","7","8","9"]}`))
				return r
			},
			handler: func(s *Server) http.HandlerFunc { return s.createTask },
			server:  liveServer,
			code:    http.StatusBadRequest,
			want:    fmt.Sprintf(errCreateTaskSourceLimit, 8),
		},
		{
			name: "createTask/source-invalid",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(
					`{"goal":"目标","source_task_ids":["abc"]}`))
				return r
			},
			handler: func(s *Server) http.HandlerFunc { return s.createTask },
			server:  liveServer,
			code:    http.StatusBadRequest,
			want:    errCreateTaskSourceInvalid,
		},
		{
			name: "createTask/source-not-found",
			req: func() *http.Request {
				// 任务 999 不在 tasks 映射中，直接返回不存在，不访问数据库。
				r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(
					`{"goal":"目标","source_task_ids":["999"]}`))
				return r
			},
			handler: func(s *Server) http.HandlerFunc { return s.createTask },
			server:  liveServer,
			code:    http.StatusBadRequest,
			want:    fmt.Sprintf(errCreateTaskSourceNotFound, 999),
		},
		{
			name: "createTask/company-limit",
			req: func() *http.Request {
				// MaxTaskCompanyCount=32，三十三家公司由纯函数 NormalizeTaskCompanyIDs 拒绝。
				ids := make([]string, 33)
				for i := range ids {
					ids[i] = fmt.Sprintf("%d", i+1)
				}
				body := `{"goal":"目标","company_ids":[` + strings.Join(ids, ",") + `]}`
				return httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
			},
			handler: func(s *Server) http.HandlerFunc { return s.createTask },
			server:  liveServer,
			code:    http.StatusBadRequest,
			want:    fmt.Sprintf(errCreateTaskCompanyLimit, 32),
		},
		{
			name: "createTask/llm-profile-invalid",
			req: func() *http.Request {
				// llm_profile_ids:[0] 在 loadProfileConfig 数据库访问前被拒绝。
				r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(
					`{"goal":"目标","llm_profile_ids":[0]}`))
				return r
			},
			handler: func(s *Server) http.HandlerFunc { return s.createTask },
			server:  liveServer,
			code:    http.StatusBadRequest,
			want:    errLLMProfileInvalid,
		},
		{
			name: "taskCoverageGraph/asset-store-disabled",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/api/tasks/t1/coverage-graph", nil)
				r.SetPathValue("id", "t1")
				return r
			},
			handler: func(s *Server) http.HandlerFunc { return s.taskCoverageGraph },
			server:  liveServer, // Manager.assets==nil → Assets()==nil → 503
			code:    http.StatusServiceUnavailable,
			want:    errAssetStoreDisabled,
		},
		{
			name: "taskAssetRefs/asset-id-required",
			req: func() *http.Request {
				// asset_id 缺失时 ParseInt 返回 0，<=0 立即返回 400，不调用 Assets()。
				r := httptest.NewRequest(http.MethodGet, "/api/tasks/t1/asset-refs", nil)
				r.SetPathValue("id", "t1")
				return r
			},
			handler: func(s *Server) http.HandlerFunc { return s.taskAssetRefs },
			server:  liveServer,
			code:    http.StatusBadRequest,
			want:    errAssetIDRequired,
		},
		{
			// profile_id 与 api_key 均为空时，在 TestConnection 前返回状态 200 的
			// writeJSON {ok:false,error}，不查询数据库；liveServer 的全局 APIKey 也为空。
			name: "testLLM/no-api-key",
			req: func() *http.Request {
				return httptest.NewRequest(http.MethodPost, "/api/llm/test", strings.NewReader(`{}`))
			},
			handler: func(s *Server) http.HandlerFunc { return s.testLLM },
			server:  liveServer,
			code:    http.StatusOK,
			want:    errLLMTestNoAPIKey,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.server()
			rec := httptest.NewRecorder()
			c.handler(s)(rec, c.req())
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

// TestChatUnavailableReasonLocalized 验证 pg=nil 时返回 errChatLLMNotReady，
// 确保真实返回与中文常量相连；缺少配置、有效配置的两条数据库路径固定常量。
func TestChatUnavailableReasonLocalized(t *testing.T) {
	s := &Server{m: &Manager{}} // m.pg == nil，直接进入第三个返回分支。
	got := s.chatUnavailableReason()
	if got != errChatLLMNotReady {
		t.Fatalf("chatUnavailableReason() = %q，预期 = %q", got, errChatLLMNotReady)
	}
	assertChineseError(t, "chat_not_ready", got)
}

// TestFallbackCommandParsing 以纯函数验证中文“意图”“提示”和英文 intent、hint。
// 只剥离关键字，保留去除首尾空白后的参数，无需 Store 或数据库。
func TestFallbackCommandParsing(t *testing.T) {
	cases := []struct {
		name, in, cmd, text string
	}{
		{"korean-intent", "意图 优先确认 SQLi 注入点", "intent", "优先确认 SQLi 注入点"},
		{"korean-hint", "提示 先检查登录表单", "hint", "先检查登录表单"},
		{"chinese-intent", "意图 扫描端口", "intent", "扫描端口"}, // 保留已有中文命令兼容性。
		{"chinese-hint", "提示 看登录", "hint", "看登录"},       // 保留已有英文命令兼容性。
		{"english-intent", "intent scan ports", "intent", "scan ports"},
		{"english-hint", "hint try admin:admin", "hint", "try admin:admin"},
		{"status-korean", "请告知当前状态", "", "请告知当前状态"},
		{"status-empty", "   ", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, text := fallbackCommand(c.in)
			if cmd != c.cmd || text != c.text {
				t.Fatalf("fallbackCommand(%q) = (%q, %q)，预期 = (%q, %q)", c.in, cmd, text, c.cmd, c.text)
			}
		})
	}
}

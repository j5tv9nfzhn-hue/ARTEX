package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 跨域预检必须放行 API 实际使用的全部方法与头部。此前 Allow-Methods 少了 PATCH、
// Allow-Headers 少了 Authorization：任务元数据/分类/漏洞等 PATCH 接口在跨域部署下
// 会被浏览器预检直接拦掉（同源部署则完全正常，所以很容易漏测）。
func TestCORSPreflightAllowsAPIMethodsAndAuthHeader(t *testing.T) {
	t.Parallel()
	called := false
	h := cors(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodOptions, "/api/tasks/1", nil)
	req.Header.Set("Origin", "https://ui.example")
	req.Header.Set("Access-Control-Request-Method", "PATCH")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status=%d, want 204", rec.Code)
	}
	if called {
		t.Fatal("预检不应到达业务 handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin=%q, want *", got)
	}

	methods := rec.Header().Get("Access-Control-Allow-Methods")
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if !strings.Contains(methods, m) {
			t.Fatalf("Allow-Methods=%q 缺少 %s", methods, m)
		}
	}

	headers := strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers"))
	for _, want := range []string{"content-type", "authorization"} {
		if !strings.Contains(headers, want) {
			t.Fatalf("Allow-Headers=%q 缺少 %s", headers, want)
		}
	}
}

// 非预检请求必须原样透传，并照样带上 CORS 头——即这次改动没有动到既有行为。
func TestCORSPassesThroughNonPreflightRequests(t *testing.T) {
	t.Parallel()
	h := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("handler 收到的 method=%s, want PATCH", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodPatch, "/api/tasks/1", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://ui.example")
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Fatalf("body=%q 被改动", rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin=%q, want *", got)
	}
}

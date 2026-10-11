package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 认证响应中文化回归测试。无数据库检查两条路径：
// requireAuth 中间件直接执行 HTTP 401 响应，其余依赖数据库的处理器固定常量。
// 复用 assertChineseError，要求中文且不含韩文。
// TestRequireAuthMessagesLocalized 检查缺少或无效 token 的真实响应正文。
func TestRequireAuthMessagesLocalized(t *testing.T) {
	s := &Server{jwtKey: []byte(strings.Repeat("k", 32))}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := s.requireAuth(next)

	cases := []struct {
		name   string
		bearer string
		want   string
	}{
		{"没有 token", "", authErrUnauthorized},
		{"token 无效", "Bearer not-a-valid-token", authErrTokenInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
			if c.bearer != "" {
				r.Header.Set("Authorization", c.bearer)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("状态码 = %d，预期 401", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, c.want) {
				t.Errorf("响应正文缺少 %q：%s", c.want, body)
			}
			assertChineseError(t, c.name, body)
		})
	}
}

// TestAuthErrorConstantsLocalized 固定所有认证处理器的中文常量，
// 覆盖 requireAuth 无法直接调用的依赖数据库路径。
func TestAuthErrorConstantsLocalized(t *testing.T) {
	consts := map[string]string{
		"authErrUnauthorized":         authErrUnauthorized,
		"authErrTokenInvalid":         authErrTokenInvalid,
		"authErrPasswordAlreadySet":   authErrPasswordAlreadySet,
		"authErrPasswordEmpty":        authErrPasswordEmpty,
		"authErrNewPasswordEmpty":     authErrNewPasswordEmpty,
		"authErrPasswordHash":         authErrPasswordHash,
		"authErrSaveFailedPrefix":     authErrSaveFailedPrefix,
		"authErrTokenGen":             authErrTokenGen,
		"authErrBadRequest":           authErrBadRequest,
		"authErrPasswordNotInit":      authErrPasswordNotInit,
		"authErrCurrentPasswordWrong": authErrCurrentPasswordWrong,
		"authErrBadCredential":        authErrBadCredential,
		"authErrPasswordTooShort":     authErrPasswordTooShort,
		"authErrSetupTokenInvalid":    authErrSetupTokenInvalid,
		"authErrTooManyAttempts":      authErrTooManyAttempts,
	}
	for name, msg := range consts {
		assertChineseError(t, name, msg)
	}
}

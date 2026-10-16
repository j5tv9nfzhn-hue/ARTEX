package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Autumn-27/artex/db"
)

// 首启管理员账户抢占问题的真实数据库集成测试：
// 拒绝没有设置令牌的初始化、正确令牌初始化、修改密码及 reset-password.sh
// 外部重置后的旧 token 失效、登录失败限流、环境变量初始化。无数据库时跳过。

type authTestRig struct {
	t *testing.T
	s *Server
	h http.Handler
}

func newAuthTestRig(t *testing.T) *authTestRig {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	clear := func() { _, _ = d.Exec(`DELETE FROM settings WHERE key = $1`, authPassKey) }
	clear()
	t.Cleanup(clear)
	t.Setenv(setupTokenEnv, "")
	t.Setenv(adminPasswordEnv, "")

	s := &Server{m: &Manager{pg: d}, jwtKey: []byte(strings.Repeat("s", 32))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/init", s.authInit)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("POST /api/auth/change-password", s.authChangePassword)
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return &authTestRig{t: t, s: s, h: s.requireAuth(mux)}
}

func (r *authTestRig) do(method, path, token string, body map[string]any) (int, map[string]any) {
	r.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (r *authTestRig) expect(code int, got int, body map[string]any, wantErr string) {
	r.t.Helper()
	if got != code {
		r.t.Fatalf("状态码 = %d，预期 %d (body=%v)", got, code, body)
	}
	if wantErr != "" {
		if msg, _ := body["error"].(string); msg != wantErr {
			r.t.Fatalf("错误文案 = %q，预期 %q", msg, wantErr)
		}
	}
}

func TestAuthInitRequiresSetupTokenDB(t *testing.T) {
	rig := newAuthTestRig(t)
	s := rig.s
	s.bootstrapAuth()
	if s.cachedPassHash() != "" {
		t.Fatal("空数据库中不应设置密码")
	}
	setup := s.currentSetupToken()
	if setup == "" {
		t.Fatal("启动时应创建设置令牌")
	}

	code, body := rig.do(http.MethodGet, "/api/auth/status", "", nil)
	rig.expect(200, code, body, "")
	if body["initialized"] != false || body["setup_token_required"] != true {
		t.Fatalf("初始化前 status 与预期不同：%v", body)
	}

	// 报告中的 PoC：只发送密码而没有设置令牌，必须被拒绝。
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "attacker-set-password-123!"})
	rig.expect(403, code, body, authErrSetupTokenInvalid)
	if _, has := body["token"]; has {
		t.Fatal("拒绝响应不应包含 token")
	}
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "attacker-set-password-123!", "setup_token": "guess"})
	rig.expect(403, code, body, authErrSetupTokenInvalid)
	if h := s.reloadPassHash(); h != "" {
		t.Fatal("被拒绝的调用不应保存密码")
	}

	// 即使设置令牌正确，服务器仍应检查密码最小长度。
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "short", "setup_token": setup})
	rig.expect(400, code, body, authErrPasswordTooShort)
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "", "setup_token": setup})
	rig.expect(400, code, body, authErrPasswordEmpty)

	// 正确设置令牌与密码 → 发放 token，允许访问保护路由。
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "operator-pass-123", "setup_token": " " + setup + "\n"})
	rig.expect(200, code, body, "")
	tok1, _ := body["token"].(string)
	if tok1 == "" {
		t.Fatal("初始化成功后应包含 token")
	}
	code, body = rig.do(http.MethodGet, "/api/ping", tok1, nil)
	rig.expect(200, code, body, "")
	code, body = rig.do(http.MethodGet, "/api/ping", "", nil)
	rig.expect(401, code, body, authErrUnauthorized)
	code, body = rig.do(http.MethodGet, "/api/auth/status", "", nil)
	rig.expect(200, code, body, "")
	if body["initialized"] != true || body["setup_token_required"] != false {
		t.Fatalf("初始化后 status 与预期不同：%v", body)
	}

	// 初始化后，即使知道设置令牌也不能再次初始化。
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "second-pass-123", "setup_token": setup})
	rig.expect(403, code, body, authErrPasswordAlreadySet)

	// 登录时用户名、密码必须同时正确。
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "wrong-pass-123"})
	rig.expect(401, code, body, authErrBadCredential)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "admin", "password": "operator-pass-123"})
	rig.expect(401, code, body, authErrBadCredential)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "operator-pass-123"})
	rig.expect(200, code, body, "")
	tok2, _ := body["token"].(string)
	code, body = rig.do(http.MethodGet, "/api/ping", tok2, nil)
	rig.expect(200, code, body, "")

	// 修改密码后旧 token 全部失效，只有响应中的新 token 有效。
	code, body = rig.do(http.MethodPost, "/api/auth/change-password", tok1, map[string]any{"old_password": "nope-nope-123", "new_password": "changed-pass-123"})
	rig.expect(401, code, body, authErrCurrentPasswordWrong)
	code, body = rig.do(http.MethodPost, "/api/auth/change-password", tok1, map[string]any{"old_password": "operator-pass-123", "new_password": "short"})
	rig.expect(400, code, body, authErrPasswordTooShort)
	code, body = rig.do(http.MethodPost, "/api/auth/change-password", tok1, map[string]any{"old_password": "operator-pass-123", "new_password": "changed-pass-123"})
	rig.expect(200, code, body, "")
	tok3, _ := body["token"].(string)
	if tok3 == "" {
		t.Fatal("修改密码响应应包含新 token")
	}
	for _, old := range []string{tok1, tok2} {
		code, body = rig.do(http.MethodGet, "/api/ping", old, nil)
		rig.expect(401, code, body, authErrTokenInvalid)
	}
	code, body = rig.do(http.MethodGet, "/api/ping", tok3, nil)
	rig.expect(200, code, body, "")
	code, body = rig.do(http.MethodPost, "/api/auth/change-password", tok1, map[string]any{"old_password": "changed-pass-123", "new_password": "another-pass-123"})
	rig.expect(401, code, body, authErrUnauthorized)

	// 模拟 reset-password.sh 在进程外修改哈希，无需重启即可使旧 token 失效。
	// passHashCacheTTL 内旧 token 仍可能通过；缓存窗口结束后必须拒绝。
	// 测试将窗口设为 0，立即检查失效与新密码登录。
	resetHash, err := bcrypt.GenerateFromPassword([]byte("reset-pass-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.m.pg.SetSetting(authPassKey, string(resetHash)); err != nil {
		t.Fatal(err)
	}
	code, body = rig.do(http.MethodGet, "/api/ping", tok3, nil)
	rig.expect(200, code, body, "")
	prevTTL := passHashCacheTTL
	passHashCacheTTL = 0
	t.Cleanup(func() { passHashCacheTTL = prevTTL })
	code, body = rig.do(http.MethodGet, "/api/ping", tok3, nil)
	rig.expect(401, code, body, authErrTokenInvalid)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "changed-pass-123"})
	rig.expect(401, code, body, authErrBadCredential)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "reset-pass-123"})
	rig.expect(200, code, body, "")
	tok4, _ := body["token"].(string)
	code, body = rig.do(http.MethodGet, "/api/ping", tok4, nil)
	rig.expect(200, code, body, "")
}

func TestAuthLoginThrottledDB(t *testing.T) {
	rig := newAuthTestRig(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("operator-pass-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.s.m.pg.SetSetting(authPassKey, string(hash)); err != nil {
		t.Fatal(err)
	}
	rig.s.bootstrapAuth()
	for i := 0; i < authFreeFailures; i++ {
		code, body := rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "brute-force-attempt"})
		rig.expect(401, code, body, authErrBadCredential)
	}
	code, body := rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "operator-pass-123"})
	rig.expect(429, code, body, authErrTooManyAttempts)
	if _, has := body["token"]; has {
		t.Fatal("冷却期间即使密码正确也不应发放 token")
	}
	rig.s.limiter().reset(clientKey(httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)))
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "operator-pass-123"})
	rig.expect(200, code, body, "")
}

func TestAuthBootstrapAdminPasswordEnvDB(t *testing.T) {
	rig := newAuthTestRig(t)
	t.Setenv(adminPasswordEnv, "env-admin-pass-123")
	rig.s.bootstrapAuth()
	code, body := rig.do(http.MethodGet, "/api/auth/status", "", nil)
	rig.expect(200, code, body, "")
	if body["initialized"] != true {
		t.Fatalf("应通过环境变量初始化：%v", body)
	}
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "attacker-set-password-123!", "setup_token": rig.s.currentSetupToken()})
	rig.expect(403, code, body, authErrPasswordAlreadySet)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "env-admin-pass-123"})
	rig.expect(200, code, body, "")
	tok, _ := body["token"].(string)
	code, body = rig.do(http.MethodGet, "/api/ping", tok, nil)
	rig.expect(200, code, body, "")

	// 忽略过短的环境变量密码，仍使用设置令牌初始化流程。
	_, _ = rig.s.m.pg.Exec(`DELETE FROM settings WHERE key = $1`, authPassKey)
	t.Setenv(adminPasswordEnv, "short")
	s2 := &Server{m: rig.s.m, jwtKey: rig.s.jwtKey}
	s2.bootstrapAuth()
	if s2.cachedPassHash() != "" {
		t.Fatal("应忽略不足 8 个字符的环境变量密码")
	}
	if s2.currentSetupToken() == "" {
		t.Fatal("忽略无效密码后应发放设置令牌")
	}
}

package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 首启账户抢占修复的无数据库单元测试：设置令牌比较、密码哈希绑定的签名、
// CORS 允许列表、失败限流和环回绑定判断；完整数据库流程由 auth_init_db_test.go 覆盖。

func TestSigningKeyBoundToPasswordHash(t *testing.T) {
	s := &Server{jwtKey: []byte(strings.Repeat("k", 32))}
	if !bytes.Equal(s.signingKey(""), s.jwtKey) {
		t.Fatal("未设置密码时应直接使用文件密钥（兼容无数据库测试）")
	}
	tok, err := signJWT(s.signingKey("hash-A"))
	if err != nil {
		t.Fatal(err)
	}
	if !verifyJWT(tok, s.signingKey("hash-A")) {
		t.Fatal("同一哈希派生的密钥应验证成功")
	}
	if verifyJWT(tok, s.signingKey("hash-B")) {
		t.Fatal("密码哈希更改后，旧 token 应失效")
	}
	if verifyJWT(tok, s.signingKey("")) {
		t.Fatal("哈希派生密钥签发的 token 不应通过原始密钥验证")
	}
}

func TestVerifyTokenInvalidatedByPasswordChange(t *testing.T) {
	s := &Server{jwtKey: []byte(strings.Repeat("v", 32))}
	s.setPassHash("hash-A")
	tok, err := signJWT(s.signingKey("hash-A"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.verifyToken(tok) {
		t.Fatal("使用当前哈希签发的 token 应通过验证")
	}
	s.setPassHash("hash-B")
	if s.verifyToken(tok) {
		t.Fatal("修改密码后旧 token 不应通过验证")
	}
	if s.verifyToken("") {
		t.Fatal("空 token 不应通过验证")
	}
}

func TestSetupTokenFromEnvAndRandom(t *testing.T) {
	t.Setenv(setupTokenEnv, "fixed-setup-token")
	s := &Server{}
	if !s.checkSetupToken("  fixed-setup-token \n") {
		t.Fatal("环境变量令牌应忽略首尾空白后匹配")
	}
	if s.checkSetupToken("fixed-setup-tokeN") || s.checkSetupToken("") {
		t.Fatal("应拒绝其他值或空值")
	}

	t.Setenv(setupTokenEnv, "")
	s = &Server{}
	tok := s.currentSetupToken()
	if len(tok) != setupTokenLen {
		t.Fatalf("随机设置令牌长度 = %d，预期 %d", len(tok), setupTokenLen)
	}
	if s.currentSetupToken() != tok {
		t.Fatal("已生成令牌在清除前应保持不变")
	}
	if !s.checkSetupToken(tok) || s.checkSetupToken(tok[:len(tok)-1]) {
		t.Fatal("随机令牌仅在完全一致时通过验证")
	}
	s.clearSetupToken()
	if s.currentSetupToken() == tok {
		t.Fatal("完成初始化并清除后应生成新令牌")
	}
}

func TestAuthLimiterCooldown(t *testing.T) {
	l := newAuthLimiter()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	const key = "203.0.113.9"

	for i := 0; i < authFreeFailures; i++ {
		if !l.allow(key) {
			t.Fatalf("第 %d 次失败前应允许请求", i+1)
		}
		l.fail(key)
	}
	if l.allow(key) {
		t.Fatalf("失败 %d 次后应在冷却期间阻止请求", authFreeFailures)
	}
	now = now.Add(authCooldownBase + time.Second)
	if !l.allow(key) {
		t.Fatal("首次冷却结束后应再次允许请求")
	}
	l.fail(key)
	now = now.Add(authCooldownBase + time.Second)
	if l.allow(key) {
		t.Fatal("每次额外失败应使冷却时间翻倍")
	}
	now = now.Add(authCooldownBase)
	if !l.allow(key) {
		t.Fatal("双倍冷却结束后应允许请求")
	}
	l.reset(key)
	l.fail(key)
	if !l.allow(key) {
		t.Fatal("成功重置后，一次失败仍在允许范围内")
	}
	for i := 0; i < 20; i++ {
		l.fail(key)
	}
	if got := l.clients[key].until.Sub(now); got > authCooldownMax {
		t.Fatalf("冷却时间超出上限：%v > %v", got, authCooldownMax)
	}
	now = now.Add(authFailureWindow + time.Second)
	if !l.allow(key) {
		t.Fatal("失败记录窗口结束后应清除记录并允许请求")
	}
	if l.allow("198.51.100.1") != true {
		t.Fatal("不应影响其他客户端")
	}
}

func TestCORSAllowlist(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := corsWith(map[string]bool{"http://localhost:5173": true}, ok)

	call := func(method, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/auth/init", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	if rec := call(http.MethodPost, ""); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("无 Origin 头的同源请求不应附加 CORS 头")
	}
	rec := call(http.MethodPost, "https://evil.example")
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("不应向允许列表之外的来源返回 Access-Control-Allow-Origin")
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Fatal("响应因来源不同而不同，应包含 Vary: Origin")
	}
	if rec := call(http.MethodOptions, "https://evil.example"); rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("未允许来源的预检请求：code=%d ACAO=%q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
	rec = call(http.MethodPost, "http://LOCALHOST:5173")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://LOCALHOST:5173" {
		t.Fatalf("允许来源应忽略大小写并原样回显：%q", got)
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatal("允许来源应可使用 Authorization 头")
	}

	wild := corsWith(map[string]bool{"*": true}, ok)
	r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	r.Header.Set("Origin", "https://anything.example")
	w := httptest.NewRecorder()
	wild.ServeHTTP(w, r)
	if w.Header().Get("Access-Control-Allow-Origin") != "https://anything.example" {
		t.Fatal("显式设置 \"*\" 时应允许所有来源（兼容旧行为）")
	}
}

func TestCORSAllowedOriginsEnv(t *testing.T) {
	t.Setenv(corsOriginsEnv, "")
	got := corsAllowedOrigins()
	for _, o := range defaultCORSOrigins {
		if !got[o] {
			t.Fatalf("默认允许列表缺少 %s", o)
		}
	}
	t.Setenv(corsOriginsEnv, " https://UI.example.com , http://10.0.0.2:5173,, ")
	got = corsAllowedOrigins()
	if !got["https://ui.example.com"] || !got["http://10.0.0.2:5173"] || len(got) != 2 {
		t.Fatalf("环境变量列表解析结果不符：%v", got)
	}
	if got["http://localhost:5173"] {
		t.Fatal("设置环境变量后不应再包含默认值")
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		":8787":           false,
		"0.0.0.0:8787":    false,
		"[::]:8787":       false,
		"10.0.0.5:8787":   false,
		"127.0.0.1:8787":  true,
		"127.0.0.1":       true,
		"localhost:8787":  true,
		"[::1]:8787":      true,
		"192.168.0.10:80": false,
	}
	for addr, want := range cases {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v，预期 %v", addr, got, want)
		}
	}
}

func TestClientKeyIgnoresForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	r.RemoteAddr = "198.51.100.7:40000"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := clientKey(r); got != "198.51.100.7" {
		t.Fatalf("限流键应使用 TCP 对端地址，而不是可伪造的 X-Forwarded-For：%q", got)
	}
	if id := clientLogID(r); !strings.Contains(id, "198.51.100.7:40000") || !strings.Contains(id, "1.2.3.4") {
		t.Fatalf("日志标识应同时包含两个值：%q", id)
	}
}

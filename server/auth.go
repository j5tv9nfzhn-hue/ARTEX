package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	jwtKeyFilename = "jwt.key"
	authPassKey    = "auth.password_hash"
	jwtTTL         = 7 * 24 * time.Hour
	keyChars       = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// setupTokenEnv pins the first-run setup token to a known value (headless /
	// automated deployments). When unset a random token is generated at startup and
	// printed to the server console only.
	setupTokenEnv = "ARTEX_SETUP_TOKEN"
	// adminPasswordEnv initialises the admin password at startup without the /setup
	// screen (headless deployments). Ignored once a password is set.
	adminPasswordEnv = "ARTEX_ADMIN_PASSWORD"
	// minPasswordLen mirrors the /setup screen's client-side rule, enforced server side.
	minPasswordLen = 8
	setupTokenLen  = 32
)

// 认证端点的中文错误响应直接展示为登录、密码设置页面的 toast。
// 凭据错误与 auth.login.errorCredential 一致，token 等技术术语保留原文。
const (
	authErrUnauthorized         = "需要身份验证"
	authErrTokenInvalid         = "token 无效或已过期"
	authErrPasswordAlreadySet   = "密码已设置"
	authErrPasswordEmpty        = "请输入密码"
	authErrPasswordTooShort     = "密码长度至少 8 位"
	authErrNewPasswordEmpty     = "请输入新密码"
	authErrPasswordHash         = "密码加密失败"
	authErrSaveFailedPrefix     = "保存失败："
	authErrTokenGen             = "生成 token 失败"
	authErrBadRequest           = "请求格式不正确"
	authErrPasswordNotInit      = "密码尚未初始化，请先设置密码"
	authErrCurrentPasswordWrong = "当前密码不正确"
	authErrBadCredential        = "用户名或密码不正确"
	authErrSetupTokenInvalid    = "设置令牌不正确。请输入服务器控制台日志 [auth] 行中打印的设置令牌"
	authErrTooManyAttempts      = "失败次数过多，请稍后再试"
)

// loadOrCreateJWTKey reads the 32-byte signing key from keyDir/jwt.key. keyDir is
// the project base dir (next to the executable), NOT the browsable workspace root
// (dataDir) — the signing key must never be listable/downloadable via the file
// manager. Legacy installs kept it at dataDir/jwt.key; if present there and not yet
// at the new location, it is migrated (key preserved, so sessions stay valid) and
// the old file removed so it disappears from the workspace. On first run a random
// key is generated and persisted.
func loadOrCreateJWTKey(keyDir, dataDir string) ([]byte, error) {
	path := filepath.Join(keyDir, jwtKeyFilename)
	// one-time migration out of the old in-workspace location.
	if legacy := filepath.Join(dataDir, jwtKeyFilename); legacy != path {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if data, rerr := os.ReadFile(legacy); rerr == nil {
				if werr := os.WriteFile(path, data, 0o600); werr == nil {
					_ = os.Remove(legacy)
					log.Printf(logT("[auth] 已将 JWT 密钥从 %s 迁移到 %s（移出可被发现的工作区）"), legacy, path)
				}
			}
		}
	}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	key, err := randomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate jwt key: %w", err)
	}
	if err := os.WriteFile(path, []byte(key), 0600); err != nil {
		return nil, fmt.Errorf("write jwt key: %w", err)
	}
	log.Printf(logT("[auth] 已将新的 JWT 密钥写入 %s"), path)
	return []byte(key), nil
}

// randomToken returns n characters drawn from keyChars with crypto/rand.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	for i := range buf {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(keyChars))))
		if err != nil {
			return "", err
		}
		buf[i] = keyChars[v.Int64()]
	}
	return string(buf), nil
}

// signJWT issues a 7-day HS256 token for the fixed admin account.
func signJWT(key []byte) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "ARTEX-REA",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(jwtTTL)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}).SignedString(key)
}

// verifyJWT returns true when tokenStr is a valid, non-expired HS256 token.
func verifyJWT(tokenStr string, key []byte) bool {
	t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return key, nil
	})
	return err == nil && t.Valid
}

// signingKey derives the HS256 key that signs and verifies tokens for the given
// stored password hash: HMAC-SHA256(jwt.key, hash). Binding the key to the hash
// means every password change — /api/auth/change-password, reset-password.sh, or
// any direct DB edit — invalidates all tokens issued before it, so a session that
// was obtained with a leaked or attacker-set password dies with that password.
// With no password set the raw file key is used; production never issues a token
// in that state (authInit stores the hash first), and DB-less tests keep working.
func (s *Server) signingKey(hash string) []byte {
	if hash == "" {
		return s.jwtKey
	}
	m := hmac.New(sha256.New, s.jwtKey)
	m.Write([]byte(hash))
	return m.Sum(nil)
}

// readPassHash fetches the stored bcrypt hash ("" when unset / no DB).
func (s *Server) readPassHash() string {
	if s.m == nil || s.m.pg == nil {
		return ""
	}
	h, _, _ := s.m.pg.GetSetting(authPassKey)
	return h
}

// passHashCacheTTL bounds how long token verification trusts the in-memory hash
// before re-reading it from the DB, so a password reset done outside this process
// (reset-password.sh) revokes old sessions within this window without a restart,
// while steady-state requests cost at most one settings lookup per window.
var passHashCacheTTL = 2 * time.Second

// cachedPassHash returns the password hash, re-reading it from the DB once the
// cache is older than passHashCacheTTL.
func (s *Server) cachedPassHash() string {
	s.authMu.Lock()
	fresh := s.passHashLoaded && time.Since(s.passHashAt) < passHashCacheTTL
	h := s.passHash
	s.authMu.Unlock()
	if fresh {
		return h
	}
	return s.reloadPassHash()
}

func (s *Server) setPassHash(h string) {
	s.authMu.Lock()
	s.passHash = h
	s.passHashLoaded = true
	s.passHashAt = time.Now()
	s.authMu.Unlock()
}

// reloadPassHash re-reads the hash from the DB (the password may have been changed
// outside this process by reset-password.sh) and refreshes the cache.
func (s *Server) reloadPassHash() string {
	h := s.readPassHash()
	s.setPassHash(h)
	return h
}

// verifyToken validates tok against the key derived from the current password
// hash. On failure it refreshes the cached hash once and retries, so an
// out-of-process password reset takes effect without a restart.
func (s *Server) verifyToken(tok string) bool {
	if tok == "" {
		return false
	}
	hash := s.cachedPassHash()
	if verifyJWT(tok, s.signingKey(hash)) {
		return true
	}
	if fresh := s.reloadPassHash(); fresh != hash {
		return verifyJWT(tok, s.signingKey(fresh))
	}
	return false
}

// bootstrapAuth runs once at startup. It caches the stored password hash and, when
// no password is set yet, either initialises it from ARTEX_ADMIN_PASSWORD (headless
// deployments) or issues the first-run setup token that /api/auth/init requires.
func (s *Server) bootstrapAuth() {
	if s.m == nil || s.m.pg == nil {
		return
	}
	hash := s.reloadPassHash()
	if hash != "" {
		return
	}
	if pw := os.Getenv(adminPasswordEnv); pw != "" {
		if utf8.RuneCountInString(pw) < minPasswordLen {
			log.Printf(logT("[auth] %s 少于 %d 个字符，已忽略。请使用设置令牌方式初始化"), adminPasswordEnv, minPasswordLen)
		} else if h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost); err != nil {
			log.Printf(logT("[auth] 生成 %s 哈希失败: %v"), adminPasswordEnv, err)
		} else if err := s.m.pg.SetSetting(authPassKey, string(h)); err != nil {
			log.Printf(logT("[auth] 保存 %s 失败: %v"), adminPasswordEnv, err)
		} else {
			s.setPassHash(string(h))
			log.Printf(logT("[auth] 已通过环境变量 %s 初始化管理员密码（用户名 ARTEX）"), adminPasswordEnv)
			return
		}
	}
	s.currentSetupToken()
}

// currentSetupToken returns the setup token that gates /api/auth/init while no
// password is set, generating (and printing to the console) one on first use.
// ARTEX_SETUP_TOKEN, when set, is used verbatim and never logged.
func (s *Server) currentSetupToken() string {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.setupToken != "" {
		return s.setupToken
	}
	if v := strings.TrimSpace(os.Getenv(setupTokenEnv)); v != "" {
		s.setupToken = v
		log.Printf(logT("[auth] 管理员密码尚未设置。设置令牌为环境变量 %s 的值（不输出到日志）"), setupTokenEnv)
		return s.setupToken
	}
	tok, err := randomToken(setupTokenLen)
	if err != nil {
		log.Printf(logT("[auth] 生成设置令牌失败: %v"), err)
		return ""
	}
	s.setupToken = tok
	log.Printf(logT("[auth] 管理员密码尚未设置。设置令牌: %s"), tok)
	log.Print(logT("[auth] 请在首个页面（/setup）的“设置令牌”栏中输入上述值，或将其作为 POST /api/auth/init 请求体中的 setup_token 发送。该令牌不会通过网络传输，只能在此控制台日志中查看"))
	return s.setupToken
}

// checkSetupToken compares the client-supplied token with the current one in
// constant time. An empty server-side token (generation failed) never matches.
func (s *Server) checkSetupToken(given string) bool {
	want := s.currentSetupToken()
	given = strings.TrimSpace(given)
	return want != "" && subtle.ConstantTimeCompare([]byte(want), []byte(given)) == 1
}

func (s *Server) clearSetupToken() {
	s.authMu.Lock()
	s.setupToken = ""
	s.authMu.Unlock()
}

// limiter returns the per-client failed-attempt limiter shared by login and init.
func (s *Server) limiter() *authLimiter {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.loginLimiter == nil {
		s.loginLimiter = newAuthLimiter()
	}
	return s.loginLimiter
}

// authLimiter throttles repeated authentication failures per client address: the
// first few failures are free, after that each further failure doubles a cooldown
// during which the client gets 429. A success clears the client's record.
type authLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]*authAttempts
}

type authAttempts struct {
	fails int
	last  time.Time
	until time.Time
}

const (
	authFreeFailures    = 5
	authFailureWindow   = 10 * time.Minute
	authCooldownBase    = 30 * time.Second
	authCooldownMax     = 10 * time.Minute
	authLimiterMaxItems = 10000
)

func newAuthLimiter() *authLimiter {
	return &authLimiter{now: time.Now, clients: map[string]*authAttempts{}}
}

// allow reports whether the client may attempt authentication right now.
func (l *authLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.clients[key]
	if a == nil {
		return true
	}
	now := l.now()
	if now.Sub(a.last) > authFailureWindow {
		delete(l.clients, key)
		return true
	}
	return !now.Before(a.until)
}

// fail records a failed attempt and, past the free allowance, starts a cooldown.
func (l *authLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a := l.clients[key]
	if a == nil || now.Sub(a.last) > authFailureWindow {
		if len(l.clients) >= authLimiterMaxItems {
			l.clients = map[string]*authAttempts{}
		}
		a = &authAttempts{}
		l.clients[key] = a
	}
	a.fails++
	a.last = now
	if extra := a.fails - authFreeFailures; extra >= 0 {
		cool := authCooldownBase << uint(extra)
		if cool > authCooldownMax || cool <= 0 {
			cool = authCooldownMax
		}
		a.until = now.Add(cool)
	}
}

func (l *authLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.clients, key)
	l.mu.Unlock()
}

// clientKey identifies the client for throttling and audit logs. The throttle key
// is the TCP peer only — X-Forwarded-For is client-controlled and would let an
// attacker dodge the limiter (or lock out others) by forging it. The forwarded
// value is still surfaced in the log line so an operator behind a reverse proxy
// can see the real origin.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func clientLogID(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		return r.RemoteAddr + " (X-Forwarded-For: " + xff + ")"
	}
	return r.RemoteAddr
}

// isLoopbackAddr reports whether a listen address only accepts local connections.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	if host == "" { // ":8787" / "0.0.0.0:8787" style — every interface
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// WarnIfExposed logs a warning when the admin password is still unset and the
// server accepts connections from other hosts: the setup token blocks a remote
// takeover, but the port should still not be reachable before setup is done.
func (s *Server) WarnIfExposed(addr string) {
	if s.m == nil || s.m.pg == nil || s.cachedPassHash() != "" || isLoopbackAddr(addr) {
		return
	}
	log.Printf(logT("[auth] 警告：管理员密码尚未设置，但服务器绑定在 %q，其他主机也可以连接。虽然没有设置令牌无法初始化，但在完成初始设置之前请勿将此端口暴露到外部网络（默认 127.0.0.1:8787）"), addr)
}

// extractToken reads the JWT from Authorization: Bearer header,
// artex_token cookie, or ?token= query param (for SSE connections).
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("artex_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

// requireAuth wraps h with JWT validation.
// /api/auth/* and /api/health are exempt.
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/auth/") || p == "/api/health" {
			h.ServeHTTP(w, r)
			return
		}
		tok := extractToken(r)
		if tok == "" {
			writeErr(w, 401, authErrUnauthorized)
			return
		}
		if !s.verifyToken(tok) {
			writeErr(w, 401, authErrTokenInvalid)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// GET /api/auth/status — reports whether the admin password has been initialised.
// While it is not, setup_token_required tells the /setup screen to ask for the
// console setup token (and makes sure one has been issued and printed).
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	hash := s.reloadPassHash()
	if hash == "" {
		s.currentSetupToken()
	}
	writeJSON(w, 200, map[string]any{"initialized": hash != "", "setup_token_required": hash == ""})
}

// POST /api/auth/init — sets the password for the first time; rejected if already
// set. The route is reachable without a session (nothing exists yet to log in
// with), so it is gated by the setup token printed on the server console: a
// remote client who merely reaches the port cannot claim the admin account.
func (s *Server) authInit(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, 429, authErrTooManyAttempts)
		return
	}
	var req struct {
		Password   string `json:"password"`
		SetupToken string `json:"setup_token"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, authErrBadRequest)
		return
	}
	// Serialise the check-then-set so two concurrent first-run requests cannot both
	// pass the "not yet set" check.
	s.initMu.Lock()
	defer s.initMu.Unlock()
	if existing := s.reloadPassHash(); existing != "" {
		writeErr(w, 403, authErrPasswordAlreadySet)
		return
	}
	if !s.checkSetupToken(req.SetupToken) {
		s.limiter().fail(key)
		log.Printf(logT("[auth] 拒绝初始设置：设置令牌不匹配 (client=%s)"), clientLogID(r))
		writeErr(w, 403, authErrSetupTokenInvalid)
		return
	}
	if req.Password == "" {
		writeErr(w, 400, authErrPasswordEmpty)
		return
	}
	if utf8.RuneCountInString(req.Password) < minPasswordLen {
		writeErr(w, 400, authErrPasswordTooShort)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, authErrPasswordHash)
		return
	}
	if err := pg.SetSetting(authPassKey, string(hash)); err != nil {
		writeErr(w, 500, trMsg(authErrSaveFailedPrefix)+err.Error())
		return
	}
	s.setPassHash(string(hash))
	s.clearSetupToken()
	s.limiter().reset(key)
	tok, err := signJWT(s.signingKey(string(hash)))
	if err != nil {
		writeErr(w, 500, authErrTokenGen)
		return
	}
	log.Printf(logT("[auth] 已初始化管理员密码 (client=%s)"), clientLogID(r))
	writeJSON(w, 200, map[string]any{"token": tok})
}

// POST /api/auth/change-password — changes the admin password. Requires a valid
// token (this route is under /api/auth/* which requireAuth exempts, so the token
// is validated here) AND the current password. Every previously issued token
// stops working (the signing key is bound to the hash); the response carries a
// fresh token so the caller's own session continues.
func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if !s.verifyToken(extractToken(r)) {
		writeErr(w, 401, authErrUnauthorized)
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, 429, authErrTooManyAttempts)
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, authErrBadRequest)
		return
	}
	if req.NewPassword == "" {
		writeErr(w, 400, authErrNewPasswordEmpty)
		return
	}
	if utf8.RuneCountInString(req.NewPassword) < minPasswordLen {
		writeErr(w, 400, authErrPasswordTooShort)
		return
	}
	hash, ok, _ := pg.GetSetting(authPassKey)
	if !ok || hash == "" {
		writeErr(w, 403, authErrPasswordNotInit)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)); err != nil {
		s.limiter().fail(key)
		log.Printf(logT("[auth] 拒绝修改密码：当前密码不匹配 (client=%s)"), clientLogID(r))
		writeErr(w, 401, authErrCurrentPasswordWrong)
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, authErrPasswordHash)
		return
	}
	if err := pg.SetSetting(authPassKey, string(newHash)); err != nil {
		writeErr(w, 500, trMsg(authErrSaveFailedPrefix)+err.Error())
		return
	}
	s.setPassHash(string(newHash))
	s.limiter().reset(key)
	tok, err := signJWT(s.signingKey(string(newHash)))
	if err != nil {
		writeErr(w, 500, authErrTokenGen)
		return
	}
	log.Printf(logT("[auth] 已修改管理员密码，所有现有会话均已失效 (client=%s)"), clientLogID(r))
	writeJSON(w, 200, map[string]any{"ok": true, "token": tok})
}

// POST /api/auth/login — validates username/password and returns a JWT.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, 429, authErrTooManyAttempts)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, authErrBadRequest)
		return
	}
	hash, ok, _ := pg.GetSetting(authPassKey)
	if !ok || hash == "" {
		writeErr(w, 403, authErrPasswordNotInit)
		return
	}
	// Always run the bcrypt comparison so a wrong username costs the same as a
	// wrong password (no username oracle, no timing shortcut).
	pwErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password))
	validUser := req.Username == "ARTEX-HYDRA" || req.Username == "ARTEX-REA" || req.Username == "ARTEX"
	if !validUser || pwErr != nil {
		s.limiter().fail(key)
		log.Printf(logT("[auth] 登录失败 (client=%s)"), clientLogID(r))
		writeErr(w, 401, authErrBadCredential)
		return
	}
	s.setPassHash(hash)
	s.limiter().reset(key)
	tok, err := signJWT(s.signingKey(hash))
	if err != nil {
		writeErr(w, 500, authErrTokenGen)
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}

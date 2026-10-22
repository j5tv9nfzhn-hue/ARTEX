package server

// 设置页的密码卡：改密码、以及「忘记旧密码」时的重置。
//
// 两条路分别对应两件事：
//
//	POST /api/auth/change-password  改密码：要旧密码（auth.go 里本来就有的接口）
//	POST /api/password/reset        重置：不要旧密码，但要一个已登录的会话
//
// 重置为什么不校验旧密码：桌面单用户场景里，「能登录进来」本身就等于「是操作者」，
// 再问一次旧密码只是把「我忘了密码」这个真实困境原样弹回去。真正忘记密码、连会话
// 都没有的人走 `artex-rea.exe -reset-password`（命令行版，见 cmd/artex）。
//
// 也不能把它挂在 /api/auth/ 前缀下：那一块被 requireAuth 整体豁免，放进去等于
// 开了个无鉴权的改密码接口。所以单独注册在 /api/password/reset，受 requireAuth 保护。

import (
	"log"
	"net/http"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// passwordResetRoutes 注册重置密码接口。
func (s *Server) passwordResetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/password/reset", s.passwordReset)
}

// passwordReset 用当前会话把管理员密码换成新的，不校验旧密码。
func (s *Server) passwordReset(w http.ResponseWriter, r *http.Request) {
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
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, authErrBadRequest)
		return
	}
	if req.Password == "" {
		writeErr(w, 400, authErrNewPasswordEmpty)
		return
	}
	if utf8.RuneCountInString(req.Password) < minPasswordLen {
		writeErr(w, 400, authErrPasswordTooShort)
		return
	}
	hash, ok, _ := pg.GetSetting(authPassKey)
	if !ok || hash == "" {
		// 没有已设密码时不该走重置：那是 /api/auth/init 的活（要 setup token）。
		writeErr(w, 403, authErrPasswordNotInit)
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
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
	// 换密码 = 换 JWT 签名密钥（signingKey 由 hash 派生），所以包括当前这条会话在内、
	// 手机端的 token 也一起失效。这里如实告诉前端「重新登录」，不偷偷续一个 token。
	log.Printf(logT("[auth] 已重置管理员密码，所有会话失效 (client=%s)"), clientLogID(r))
	writeJSON(w, 200, map[string]any{"ok": true, "relogin": true})
}

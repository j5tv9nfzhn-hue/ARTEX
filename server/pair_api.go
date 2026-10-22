package server

// 手机绑定：网页上出一张「配对码」，App 扫一下就把地址和登录凭据一次性拿到，
// 不用再让用户抄 IP、抄密码。
//
// 流程（三个角色各一步）：
//
//	网页（已登录）  POST /api/pair/code      → 服务端发一个一次性配对码 + 它的二维码
//	手机 App       扫二维码，读出 ARTEXPAIR1|<host:port>|<code>
//	手机 App        POST /api/auth/pair-claim → 换回一个 JWT，写入本地会话即完成登录
//
// 为什么 claim 挂在 /api/auth/ 下面：requireAuth 只豁免 /api/auth/* 与 /api/health，
// 而 claim 的本意就是「还没有 token 的人才来调」。放在别处就得给它单独开后门，
// 那反而更难看。安全边界由「一次性 + 10 分钟 + 限流」来兜：码本身是 32^8 ≈ 1.1e12
// 的空间，配上失败计数，猜不中。
//
// 码只在内存里：进程重启就作废，这正好——重启后监听地址可能都变了。

import (
	"crypto/rand"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// pairPayloadPrefix 是二维码内容的前缀，App 用它区分「这是 ARTEX 的配对码」。
	pairPayloadPrefix = "ARTEXPAIR1"
	pairCodeLength    = 8
	// pairCodeTTL 是配对码有效期：够用户拿起手机，又不至于长期挂在屏幕上。
	pairCodeTTL = 10 * time.Minute
	// pairMaxPending 限制同时有效的码数量，防止频繁点「重新生成」把内存堆满。
	pairMaxPending = 16
)

// pairAlphabet 去掉了 0/O/1/I：配对码允许手输，别让人分不清字符。
const pairAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

type pairTicket struct {
	code    string
	host    string // 手机要访问的 host:port，例如 192.168.1.61:8787
	created time.Time
}

type pairStore struct {
	mu    sync.Mutex
	items map[string]*pairTicket
}

var pairTickets = &pairStore{items: map[string]*pairTicket{}}

// newPairCode 生成一个配对码。32 字母表正好整除 256，取模不引入偏差。
func newPairCode() (string, error) {
	buf := make([]byte, pairCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, pairCodeLength)
	for i, b := range buf {
		out[i] = pairAlphabet[int(b)%len(pairAlphabet)]
	}
	return string(out), nil
}

// issue 发放一个配对码，顺手清掉过期与超量的旧码。
func (p *pairStore) issue(host string) (*pairTicket, error) {
	code, err := newPairCode()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	t := &pairTicket{code: code, host: host, created: now}
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.items {
		if now.Sub(v.created) > pairCodeTTL {
			delete(p.items, k)
		}
	}
	for len(p.items) >= pairMaxPending {
		oldestKey, oldest := "", now
		for k, v := range p.items {
			if v.created.Before(oldest) || oldestKey == "" {
				oldestKey, oldest = k, v.created
			}
		}
		if oldestKey == "" {
			break
		}
		delete(p.items, oldestKey)
	}
	p.items[code] = t
	return t, nil
}

// claim 取出并销毁一个配对码。一次性：无论成败，命中即焚。
func (p *pairStore) claim(code string) (*pairTicket, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.items[code]
	if !ok {
		return nil, false
	}
	delete(p.items, code)
	if time.Since(t.created) > pairCodeTTL {
		return nil, false
	}
	return t, true
}

// pairPayload 拼出二维码里的内容。用竖线分隔而不是 JSON：短，且扫码器不会给你
// 加上引号或转义。
func pairPayload(host, code string) string {
	return pairPayloadPrefix + "|" + host + "|" + code
}

// pairRoutes 注册配对相关接口。
func (s *Server) pairRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/pair/code", s.pairCreateCode)
	// 免登录入口，必须留在 /api/auth/ 前缀下（见文件头说明）。
	mux.HandleFunc("POST /api/auth/pair-claim", s.pairClaim)
}

// pairCreateCode 发放配对码（需要已登录：能打开这个页面的人就是操作者）。
func (s *Server) pairCreateCode(w http.ResponseWriter, r *http.Request) {
	addr := s.ListenAddr()
	host := ""
	if urls := LanURLs(addr); len(urls) > 0 {
		host = strings.TrimPrefix(urls[0], "http://")
	}
	lanOpen := host != ""
	if !lanOpen {
		// 只监听回环时也给一个码，方便本机自测；页面会提示手机连不上。
		host = "127.0.0.1:" + LanPort(addr)
	}
	ticket, err := pairTickets.issue(host)
	if err != nil {
		writeErr(w, 500, "生成配对码失败")
		return
	}
	payload := pairPayload(ticket.host, ticket.code)
	qr := ""
	if svg, qerr := QRCodeSVG(payload); qerr == nil {
		qr = svg
	}
	writeJSON(w, 200, map[string]any{
		"code":       ticket.code,
		"host":       ticket.host,
		"payload":    payload,
		"qr_svg":     qr,
		"lan_open":   lanOpen,
		"expires_at": ticket.created.Add(pairCodeTTL).Unix(),
	})
}

// pairClaim 用配对码换 token。这是唯一一个「没有 token 也能拿到 token」的新入口，
// 所以每一步都从严：限流、密码必须已初始化、码一次性。
func (s *Server) pairClaim(w http.ResponseWriter, r *http.Request) {
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, 429, authErrTooManyAttempts)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, authErrBadRequest)
		return
	}
	hash := s.cachedPassHash()
	if hash == "" {
		// 密码都没设，发 token 等于把还没设防的实例交出去。
		writeErr(w, 403, authErrPasswordNotInit)
		return
	}
	ticket, ok := pairTickets.claim(req.Code)
	if !ok {
		s.limiter().fail(key)
		log.Printf(logT("[auth] 拒绝绑定：配对码无效或已过期 (client=%s)"), clientLogID(r))
		writeErr(w, 410, "配对码无效或已过期，请回到电脑上重新生成")
		return
	}
	tok, err := signJWT(s.signingKey(hash))
	if err != nil {
		writeErr(w, 500, authErrTokenGen)
		return
	}
	s.limiter().reset(key)
	log.Printf(logT("[auth] 手机配对成功，已签发会话 (client=%s)"), clientLogID(r))
	writeJSON(w, 200, map[string]any{
		"token":    tok,
		"base_url": "http://" + ticket.host,
		"version":  BuildVersion,
	})
}

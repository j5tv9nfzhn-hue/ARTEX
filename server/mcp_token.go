package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// 对外 MCP 接入的三项设置。都落在 settings 表（mcp_server_token 走凭据加密，
// 见 db/settings.go settingsSecretKeys），与控制台其它开关同一本账。
const (
	settingMCPEnabled = "mcp_server_enabled" // 是否对 http://host/mcp 开放
	settingMCPTiers   = "mcp_server_tiers"   // 逗号分隔的档位，默认 L0,L1
	settingMCPToken   = "mcp_server_token"   // Bearer token（凭据加密列）
)

// mcpTokenPrefix 让 token 在日志/界面里一眼可辨，也便于密钥扫描器识别。
const mcpTokenPrefix = "artex_mcp_"

// mcpTokenValue 返回当前 token；没有就生成一个并持久化。
//
// 惰性生成的好处：用户没看「对外接入」卡片之前，磁盘上不存在可用凭据；
// 一旦卡片展示过一次，token 就固定下来，配置到客户端里的不会自己变。
func (s *Server) mcpTokenValue() string {
	if s.m == nil || s.m.pg == nil {
		return ""
	}
	if v, ok, err := s.m.pg.GetSetting(settingMCPToken); err == nil && ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	tok, err := newMCPToken()
	if err != nil {
		return ""
	}
	if err := s.m.pg.SetSetting(settingMCPToken, tok); err != nil {
		return ""
	}
	return tok
}

// rotateMCPToken 生成新 token 并覆盖旧值；旧 token 立刻失效。
func (s *Server) rotateMCPToken() (string, error) {
	if s.m == nil || s.m.pg == nil {
		return "", errors.New("数据库未就绪")
	}
	tok, err := newMCPToken()
	if err != nil {
		return "", err
	}
	if err := s.m.pg.SetSetting(settingMCPToken, tok); err != nil {
		return "", err
	}
	return tok, nil
}

func newMCPToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return mcpTokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// mcpAuthorize 校验 Authorization: Bearer <token>。用常数时间比较，
// 避免通过响应时间侧信道猜 token。
func (s *Server) mcpAuthorize(r *http.Request) error {
	hdr := strings.TrimSpace(r.Header.Get("Authorization"))
	const scheme = "bearer "
	if len(hdr) <= len(scheme) || !strings.EqualFold(hdr[:len(scheme)], scheme) {
		return errors.New("missing bearer token")
	}
	got := strings.TrimSpace(hdr[len(scheme):])
	want := s.mcpTokenValue()
	if want == "" || got == "" {
		return errors.New("token not configured")
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return errors.New("invalid token")
	}
	return nil
}

// MCPTokenForCLI 供命令行与 headless 实例读取（必要时生成）当前 token。
//
// 和 mcpTokenValue 是同一份存储、同一套加密，只是不依赖 *Server：
// CLI 里没有 Server 实例，但 token 必须和 console 用的是同一个，
// 否则附着时会 401。
func MCPTokenForCLI(pg *db.DB) (string, error) {
	if pg == nil {
		return "", errors.New("数据库未就绪")
	}
	if v, ok, err := pg.GetSetting(settingMCPToken); err == nil && ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), nil
	}
	tok, err := newMCPToken()
	if err != nil {
		return "", err
	}
	if err := pg.SetSetting(settingMCPToken, tok); err != nil {
		return "", err
	}
	return tok, nil
}

// maskToken 只保留前缀与末 4 位，供界面默认展示；完整值由用户主动「显示」。
func maskToken(tok string) string {
	if len(tok) <= len(mcpTokenPrefix)+8 {
		return tok
	}
	return tok[:len(mcpTokenPrefix)+6] + "…" + tok[len(tok)-4:]
}

package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// ---------- mcp_servers.env 的出站解密 / 回显掩码 / 保存合并（计划 #49） ----------
//
// 落库形态由 db 层统一负责（db/mcp_env.go）：敏感值是 enc:v1: 密文。这一层管三件事：
//
//   - 出站：connectMCP / ScopeSentry 客户端用 db.MCPEnvPlain 拿明文后再拨号——
//     解不开（主密钥丢失/密文损坏）就明确报错，绝不把密文当明文发出去（那只会
//     得到一个原因离现场很远的 401）；
//   - 回显：GET /api/mcp 把敏感值换成 notify 掩码（明文与密文都不出网）；
//   - 保存：整份 env 回传时，值为掩码的键还原为库中原值。前端 MCP 编辑页就是
//     「读出 KEY=value 文本→整份提交」，不还原就会把字面量 "__masked__…" 写进
//     库：鉴权静默失效且没有任何报错。

// maskMCPEnvForResponse 返回 env 的回显形态：敏感值替换为 notify 掩码。
// 非敏感键原样保留；空值保持空串（UI 显示 KEY= 仍能看出该键存在）。这是
// mcp_servers.env 对外回显的唯一漏斗。
func maskMCPEnvForResponse(raw json.RawMessage) json.RawMessage {
	env, _ := db.MCPEnvPlain(raw)
	changed := false
	for k, v := range env {
		if !db.MCPEnvSecretKey(k) {
			continue
		}
		switch {
		case v == "":
			// 空值没有可掩码的内容。
		case db.CredentialEncrypted(v):
			// 解不开的密文：回显成不带提示的裸掩码——它的全部信息量就是
			// 「这里有一把读不出的凭据」，看不出长度或尾字符。
			env[k] = notify.MaskedPrefix
			changed = true
		default:
			env[k] = notify.MaskedValue(v)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	b, err := json.Marshal(env)
	if err != nil {
		return raw // 理论上不可达；宁可回显原值也不 500
	}
	return b
}

// maskMCPForResponse 返回一个 env 已掩码的副本，供列表接口序列化。
func maskMCPForResponse(m *db.MCPServer) *db.MCPServer {
	cp := *m
	cp.Env = maskMCPEnvForResponse(m.Env)
	return &cp
}

// mergeMaskedMCPEnv 处理「整份 env 回传、其中敏感键是掩码」的保存：掩码键还原
// 为库中原值。找不到可保持的原值（新建服务器，或库里该键已不存在）时**拒绝
// 保存**——把掩码字面量存进库是静默的鉴权失效，宁可按 notify.PrepareConfigUpdate
// 的同一取舍让操作者重填一次。非掩码值不改动；加密由 SaveMCP 的写入口统一做。
func mergeMaskedMCPEnv(pg *db.DB, m *db.MCPServer) error {
	env := map[string]string{}
	if len(m.Env) > 0 {
		if err := json.Unmarshal(m.Env, &env); err != nil {
			return fmt.Errorf("环境变量必须是 {KEY: VALUE} 对象：%w", err)
		}
	}
	var masked []string
	for k, v := range env {
		if notify.IsMasked(v) {
			masked = append(masked, k)
		}
	}
	if len(masked) == 0 {
		return nil
	}
	stored, err := pg.MCPStoredEnv(m.ID, m.Name)
	if err != nil {
		return err
	}
	// 解不出来的存量值保留密文，随后原样回写——保存不能成为销毁读不出的凭据的
	// 路径（丢了主密钥只该表现为「解不开」，不该表现为「被覆盖没了」）。
	storedEnv, _ := db.MCPEnvPlain(stored)
	var missing []string
	for _, k := range masked {
		orig, ok := storedEnv[k]
		if !ok || orig == "" {
			missing = append(missing, k)
			continue
		}
		env[k] = orig
	}
	if len(missing) > 0 {
		return fmt.Errorf("环境变量 %s 提交的是掩码值，但库中没有可保持的原值：请重新填写完整值，或删除该键后再保存",
			strings.Join(missing, "、"))
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	m.Env = b
	return nil
}

package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// MCP env/header 值的静态加密（计划 #49）：mcp_servers.env 里的敏感值
// （X-API-Key / Authorization / *_TOKEN 等）与 LLM Key 共用同一把主密钥和同一个
// enc:v1: 前缀（credential_crypto.go）。读写口径：
//
//   - 写：SaveMCP 是唯一入口，敏感值未加密的当场加密；已是密文的值原样保留。
//     SaveMCP 会被反复调用（browser MCP 的代理同步走「读→改→整份回写」），
//     二次加密会把上一次的密文再包一层，值再也解不回来。
//   - 读：MCPEnvPlain 解出明文，只供**出站使用**（拉起连接/发请求）；对外回显由
//     server 层做掩码（notify.MaskedValue），密文与明文都不许出现在响应里。
//   - 兼容：没有 enc:v1: 前缀的存量值按明文处理，行为与升级前逐字一致；未安装
//     主密钥时 encryptCredential 原样返回，整套机制退化为明文存储。

// mcpEnvSecretMarkers 判定 env/header 名是否承载凭据：小写子串匹配。
//
// 用子串而不是白名单，是因为 MCP 生态的命名无法穷举（X-API-Key、GITHUB_TOKEN、
// Authorization、SMTP_PASSWORD…），漏判一个就等于让一把真凭据明文落库。宁可
// 误判（某个恰好含 "key" 的非敏感变量被加密、回显成掩码），也不漏判。
var mcpEnvSecretMarkers = []string{
	"key", "token", "secret", "password", "passwd", "pwd",
	"authorization", "auth", "credential",
}

// MCPEnvSecretKey 报告某个 env/header 名是否为凭据字段。server 层的回显掩码
// 与保存合并在同一判定上，两处必须用同一个函数，否则会出现「存的是明文、
// 回显不掩码」或反之的缝隙。
func MCPEnvSecretKey(name string) bool {
	lower := strings.ToLower(name)
	for _, m := range mcpEnvSecretMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// MCPEnvPlain 解码 mcp_servers.env 并解密敏感值：出站路径（connectMCP /
// ScopeSentry 客户端 / 状态探测）用它拿到可直接使用的明文。
//
// 返回的 map 始终可用：能解出来的键是明文；解不出来的敏感键保留原密文（绝不
// 放空串——那会让请求带着空 Authorization 头发出去，401 的原因离现场很远），
// 同时返回非 nil error 让必须成功的调用方（真正去连接的路径）明确拒绝。
// 允许降级的调用方（只读状态探测、回显掩码）可以忽略 error 继续用这个 map。
func MCPEnvPlain(raw json.RawMessage) (map[string]string, error) {
	out := map[string]string{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return map[string]string{}, fmt.Errorf("MCP 环境变量不是 JSON 对象：%w", err)
		}
	}
	var unreadable []string
	for k, v := range out {
		if !MCPEnvSecretKey(k) || !credentialEncrypted(v) {
			continue
		}
		plain, err := decryptCredential(v)
		if err != nil {
			unreadable = append(unreadable, fmt.Sprintf("%s（%v）", k, err))
			continue
		}
		out[k] = plain
	}
	if len(unreadable) > 0 {
		return out, fmt.Errorf("MCP 环境变量解密失败：%s", strings.Join(unreadable, "；"))
	}
	return out, nil
}

// encryptMCPEnvSecrets 是 SaveMCP 的 env 归一化/加密步骤（写入口唯一漏斗）。
//
// 已是密文的值绝不二次加密；空值原样保留（空串不是凭据，加密它只会让「显式
// 清空」变形）。env 必须是 {KEY: VALUE} 对象：旧行为会原样存下非法 JSON，
// 但那种行到运行期只会以「环境变量没生效」的形式浮出来，不如在写入口拒绝。
func encryptMCPEnvSecrets(raw json.RawMessage) (json.RawMessage, error) {
	var env map[string]string
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("MCP 环境变量必须是 {KEY: VALUE} 对象：%w", err)
	}
	for k, v := range env {
		if v == "" || !MCPEnvSecretKey(k) || credentialEncrypted(v) {
			continue
		}
		enc, err := encryptCredential(v)
		if err != nil {
			return nil, err
		}
		env[k] = enc
	}
	return json.Marshal(env)
}

// CredentialEncrypted 报告一个存储值是否已带凭据密文前缀。供 server 层区分
// 「解不出来的密文」（回显成裸掩码）与「可读明文」（回显带末位提示的掩码）。
func CredentialEncrypted(stored string) bool { return credentialEncrypted(stored) }

// MCPStoredEnv returns the stored env JSON of one MCP server, found by id (id>0)
// or else by name, or nil when there is no such row. Used by the API layer to
// merge masked secret values back to their stored originals on save.
func (d *DB) MCPStoredEnv(id int64, name string) (json.RawMessage, error) {
	var raw []byte
	var err error
	switch {
	case id > 0:
		err = d.QueryRow(`SELECT env FROM mcp_servers WHERE id=$1`, id).Scan(&raw)
	case name != "":
		err = d.QueryRow(`SELECT env FROM mcp_servers WHERE name=$1`, name).Scan(&raw)
	default:
		return nil, nil
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

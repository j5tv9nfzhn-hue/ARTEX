package db

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
)

// 供应商 API Key 的静态加密（计划 #49）。
//
// ── 威胁模型要说清楚，否则会高估它的作用 ─────────────────────────────────────
//
// 主密钥放在应用目录下的独立文件里（server 启动时读入并调用 SetCredentialKey）。
// 因此**能读到这个文件的人就能解��**。它防的是：
//
//   - 直接拷走 artex.db 做备份/分享/上传（单文件即可还原的明文泄露）
//   - 在文件管理器里预览数据库内容时看到 key
//   - 数据库文件被第三方工具解析时直接读出可用凭据
//
// 它不防能读取应用目录的本地攻击者，也不防已在本机执行的恶意进程。要覆盖后者
// 需要接操作系统凭据库（Windows DPAPI / macOS Keychain / Linux Secret Service），
// 那是另一个量级的工程，且会让"拷贝数据目录到另一台机器"这种正常操作失效。
//
// 选 AES-256-GCM 而不是别的：GCM 同时提供机密性与完整性——密钥被改动时解密会
// 直接失败，而不是悄悄产出垃圾明文。
//
// ── 为什么是前缀标记而不是一次性重写存量 ─────────────────────────────────────
//
// 密文带 `enc:v1:` 前缀；读到的值没有这个前缀就按明文处理。这样：
//
//   - 存量明文 key 在升级后仍可正常读取，不存在"升级即失联"���窗口；
//   - 每条 profile 在下次被保存时自然升级为密文，不需要迁移脚本；
//   - 万一主密钥文件丢失，用户只需要重填 key，而不会因为无法解密而卡在启动。
//
// 代价是明文与密文会共存一段时间。这是刻意的：一次性的全表迁移脚本一旦中途
// 失败，损坏的是**所有**供应商凭据。

const credentialPrefix = "enc:v1:"

var credentialKey struct {
	mu  sync.RWMutex
	key []byte
}

// SetCredentialKey 安装用于加解密凭据的主密钥。传 nil 表示不加密（明文存储）。
//
// 由 server 层在启动时调用：db 包不知道应用目录在哪，而主密钥必须与数据库文件
// 分开存放——同文件同目录等于没加密。
func SetCredentialKey(key []byte) {
	credentialKey.mu.Lock()
	defer credentialKey.mu.Unlock()
	if len(key) == 0 {
		credentialKey.key = nil
		return
	}
	// 无论调用方给的是 32 字节原始密钥还是一段文本，都归一化成 AES-256 需要的
	// 32 字节。这里走 SHA-256 而不是直接截断：截断会丢掉密钥文件里超出 32 字节
	// 的熵。
	sum := sha256.Sum256(key)
	credentialKey.key = sum[:]
}

// ErrCredentialKeyMissing 表示没有安装主密钥却读到了密文。
//
// 通常意味着主密钥文件丢了或权限不对。**不能**降级成返回空串：那会让请求带着
// 空 Authorization 头发出去，供应商返回 401，而错误信息离真正的原因很远。
var ErrCredentialKeyMissing = errors.New("凭据已加密但未安装主密钥")

func currentCredentialKey() ([]byte, error) {
	credentialKey.mu.RLock()
	defer credentialKey.mu.RUnlock()
	if len(credentialKey.key) == 0 {
		return nil, ErrCredentialKeyMissing
	}
	return credentialKey.key, nil
}

// encryptCredential 加密一条凭据。没安装主密钥时原样返回，由调用方按明文存。
func encryptCredential(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	key, err := currentCredentialKey()
	if err != nil {
		return plain, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	// 每次都用新 nonce。GCM 下 nonce 复用会 catastrophically 泄漏密钥流，
	// 而这里加密的是长期不变的凭据，重复 encrypt 同一明文是常态。
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return credentialPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// decryptCredential 解密一条凭据。
//
// 没有前缀 = 存量明文，原样返回，因此升级不需要迁移步骤。
// 装了主密钥但值没前缀、或没装主密钥但值有前缀，都按上面各自的理由处理。
func decryptCredential(stored string) (string, error) {
	if stored == "" || !strings.HasPrefix(stored, credentialPrefix) {
		return stored, nil
	}
	key, err := currentCredentialKey()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(stored[len(credentialPrefix):])
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("凭据密文长度异常")
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		// 认证失败 = 密钥不对或密文被改过。**不能**返回明文或空串继续跑。
		return "", errors.New("凭据解密失败：主密钥与该记录不匹配，或记录已被篡改")
	}
	return string(plain), nil
}

// credentialEncrypted reports whether a stored value is already ciphertext.
// 用于判断某条 profile 是否还需要做一次性升级写入。
func credentialEncrypted(stored string) bool {
	return strings.HasPrefix(stored, credentialPrefix)
}

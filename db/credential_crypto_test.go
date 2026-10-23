package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// credential tests pin the at-rest encryption contract (计划 #49): plaintext
// never lands in the database once a master key is installed, legacy plaintext
// keeps working, and a ciphertext row is unreadable — loudly — without the key.

func TestCredentialRoundtrip(t *testing.T) {
	SetCredentialKey([]byte("unit-test-master-key"))
	defer SetCredentialKey(nil)

	plain := "sk-live-abcdef123456"
	enc, err := encryptCredential(plain)
	if err != nil {
		t.Fatal(err)
	}
	if enc == plain {
		t.Fatal("encryptCredential returned the plaintext verbatim")
	}
	if !credentialEncrypted(enc) {
		t.Fatalf("ciphertext missing %s prefix: %q", credentialPrefix, enc)
	}
	dec, err := decryptCredential(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != plain {
		t.Fatalf("roundtrip = %q, want %q", dec, plain)
	}

	// GCM with a fresh nonce per call: the same secret must not produce the
	// same ciphertext twice (nonce reuse would leak the keystream).
	enc2, err := encryptCredential(plain)
	if err != nil {
		t.Fatal(err)
	}
	if enc2 == enc {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext")
	}

	// Tamper: flipping the body must fail authentication, never yield garbage.
	// The replacement byte must DIFFER from the original first character — a
	// blind "A" is a no-op ~1/64 of runs (the flake this guards against), which
	// would let a broken decrypt slip through as a false pass.
	body := strings.TrimPrefix(enc, credentialPrefix)
	flip := "A"
	if strings.HasPrefix(body, "A") {
		flip = "B"
	}
	tampered := credentialPrefix + flip + body[1:]
	if _, err := decryptCredential(tampered); err == nil {
		t.Fatal("tampered ciphertext decrypted without error")
	}
}

func TestCredentialWrongKeyFails(t *testing.T) {
	SetCredentialKey([]byte("key-number-one"))
	enc, err := encryptCredential("sk-secret")
	if err != nil {
		t.Fatal(err)
	}
	SetCredentialKey([]byte("key-number-two"))
	if _, err := decryptCredential(enc); err == nil {
		t.Fatal("decrypt with the wrong master key must fail, not return data")
	}
	// Missing key is its own explicit error, not a silent empty string.
	SetCredentialKey(nil)
	if _, err := decryptCredential(enc); !errors.Is(err, ErrCredentialKeyMissing) {
		t.Fatalf("missing-key decrypt error = %v, want %v", err, ErrCredentialKeyMissing)
	}
}

func TestCredentialNoKeyPassthrough(t *testing.T) {
	SetCredentialKey(nil)

	// No master key = legacy plaintext mode: values store and read verbatim.
	enc, err := encryptCredential("plain-key")
	if err != nil || enc != "plain-key" {
		t.Fatalf("no-key encrypt = %q, %v; want passthrough", enc, err)
	}
	dec, err := decryptCredential("plain-key")
	if err != nil || dec != "plain-key" {
		t.Fatalf("no-key decrypt = %q, %v; want passthrough", dec, err)
	}
	if credentialEncrypted("plain-key") {
		t.Fatal("plaintext flagged as ciphertext")
	}
	// Empty values stay empty on both sides.
	if enc, _ := encryptCredential(""); enc != "" {
		t.Fatalf("empty encrypt = %q", enc)
	}
	if dec, _ := decryptCredential(""); dec != "" {
		t.Fatalf("empty decrypt = %q", dec)
	}
}

func TestSettingsSecretKeysEncryptedAtRest(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("database unavailable (%v) — skipping", err)
	}
	defer d.Close()
	SetCredentialKey([]byte("settings-test-master-key"))
	defer SetCredentialKey(nil)

	if err := d.SetSetting("brave_search_api_key", "bsk-secret-value"); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM settings WHERE key='brave_search_api_key'`)

	var raw string
	if err := d.QueryRow(`SELECT value FROM settings WHERE key=$1`, "brave_search_api_key").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "bsk-secret-value" || !credentialEncrypted(raw) {
		t.Fatalf("brave key stored as %q, want encrypted with %s prefix", raw, credentialPrefix)
	}
	got, ok, err := d.GetSetting("brave_search_api_key")
	if err != nil || !ok || got != "bsk-secret-value" {
		t.Fatalf("GetSetting = %q ok=%v err=%v, want plaintext roundtrip", got, ok, err)
	}

	// Non-secret settings are stored verbatim (no accidental blanket encryption).
	if err := d.SetSetting("llm_pool_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM settings WHERE key='llm_pool_enabled'`)
	if err := d.QueryRow(`SELECT value FROM settings WHERE key=$1`, "llm_pool_enabled").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "true" {
		t.Fatalf("non-secret setting stored as %q, want %q", raw, "true")
	}
}

func TestProfileKeyEncryptedAtRest(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("database unavailable (%v) — skipping", err)
	}
	defer d.Close()
	SetCredentialKey([]byte("profile-test-master-key"))
	defer SetCredentialKey(nil)

	id, err := d.SaveProfile(&LLMProfile{Name: "t-cred-rest", Format: "openai", Model: "m", APIKey: "sk-rest-secret"})
	if err != nil {
		t.Fatal(err)
	}
	var pid int64
	_ = d.QueryRow(`SELECT COALESCE(provider_id,0) FROM llm_profiles WHERE id=$1`, id).Scan(&pid)
	defer func() {
		// 供应商是为这条模型自动建的，也要删：留着一条用测试密钥加密的行，后续
		// 用例（同一个共享测试库）在没装主密钥的情况下读它会整表失败。
		_, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id)
		if pid > 0 {
			_, _ = d.DeleteProvider(pid)
		}
	}()

	var raw string
	if err := d.QueryRow(`SELECT api_key FROM llm_profiles WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "sk-rest-secret" || !credentialEncrypted(raw) {
		t.Fatalf("api_key stored as %q, want encrypted with %s prefix", raw, credentialPrefix)
	}

	// Read funnel decrypts; the STORED hint is derived from the plaintext tail
	// (…cret), never from ciphertext — see SaveProfile. (Single-row loads select
	// profileColsKey and do not carry the hint column; ListProfiles does.)
	p, err := d.ProfileByID(id)
	if err != nil || p == nil {
		t.Fatalf("ProfileByID: %+v err=%v", p, err)
	}
	if p.APIKey != "sk-rest-secret" {
		t.Fatalf("ProfileByID key = %q, want plaintext roundtrip", p.APIKey)
	}
	var storedHint string
	if err := d.QueryRow(`SELECT COALESCE(api_key_hint,'') FROM llm_profiles WHERE id=$1`, id).Scan(&storedHint); err != nil {
		t.Fatal(err)
	}
	if storedHint != "…cret" {
		t.Fatalf("stored hint = %q, want …cret (from plaintext, not ciphertext)", storedHint)
	}

	// Blank-key update keeps the stored credential (and keeps it decryptable).
	p.APIKey = ""
	p.Name = "t-cred-rest-renamed"
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	again, err := d.ProfileByID(id)
	if err != nil || again.APIKey != "sk-rest-secret" {
		t.Fatalf("after blank update key = %q err=%v, want kept", again.APIKey, err)
	}

	// Legacy plaintext row (written before the key existed) reads back fine —
	// the lazy-upgrade window promised in credential_crypto.go.
	//
	// 供应商重构后，Key 的事实来源是 llm_providers；这条用例要验证的是「没有供应商
	// 归属的老行」仍能直读自己的明文列，所以先把 provider_id 摘掉。
	if _, err := d.Exec(`UPDATE llm_profiles SET api_key='sk-legacy-plain', provider_id=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	legacy, err := d.ProfileByID(id)
	if err != nil || legacy.APIKey != "sk-legacy-plain" {
		t.Fatalf("legacy plaintext read = %q err=%v, want passthrough", legacy.APIKey, err)
	}
}

// 凭据池废弃后的升级路径：绑了凭据的供应商/模型行，开库时必须把 Key 原样折叠
// 回自己那一行再解绑——否则升级后这些配置就变成「Key 读不出来」，界面显示成
// 未配置 Key，用户只能重填。
func TestOpenFoldsCredentialPoolIntoProviders(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "legacy-creds.db") +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"

	// 先用一个正常库写入「旧形态」数据：Key 在 credentials 行上，两行都绑着它。
	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("建库：%v", err)
	}
	enc, err := encryptCredential("sk-pooled-7777")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	res, err := d.Exec(`INSERT INTO credentials(name,api_key,api_key_hint) VALUES('旧凭据',$1,$2)`,
		enc, "…7777")
	if err != nil {
		t.Fatalf("insert credential: %v", err)
	}
	credID, _ := res.LastInsertId()
	// 供应商内联列故意留空：旧读路径只能靠凭据行拿到 Key 与 hint。
	if _, err := d.Exec(`INSERT INTO llm_providers(name,format,api_key,api_key_hint,credential_id)
VALUES('zz-legacy-pool','openai',NULL,NULL,$1)`, credID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	var pid int64
	if err := d.QueryRow(`SELECT id FROM llm_providers WHERE name='zz-legacy-pool'`).Scan(&pid); err != nil {
		t.Fatalf("读供应商 id: %v", err)
	}
	prow, err := d.Exec(`INSERT INTO llm_profiles(name,format,model,api_key,credential_id,provider_id)
VALUES('zz-legacy-pool-model','openai','m-pool','',$1,$2)`, credID, pid)
	if err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	modelID, _ := prow.LastInsertId()
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 再次打开 = 一次升级。折叠加解绑都应当发生。
	d2, err := Open(dsn)
	if err != nil {
		t.Fatalf("二次打开：%v", err)
	}
	defer d2.Close()

	var apiKey string
	var credIDAfter sql.NullInt64
	if err := d2.QueryRow(`SELECT COALESCE(api_key,''),credential_id FROM llm_providers WHERE id=?`, pid).
		Scan(&apiKey, &credIDAfter); err != nil {
		t.Fatalf("读供应商：%v", err)
	}
	if credIDAfter.Valid {
		t.Fatalf("供应商仍绑着凭据 %d，未解绑", credIDAfter.Int64)
	}
	if apiKey == "" {
		t.Fatal("供应商的 api_key 没有被折叠回来")
	}
	if plain, err := decryptCredential(apiKey); err != nil || plain != "sk-pooled-7777" {
		t.Fatalf("折叠后的 Key = %q err=%v，want sk-pooled-7777", plain, err)
	}

	// 读路径必须立刻用得上这把 Key（否则升级后第一次请求就 401）。
	p, err := d2.ProfileByID(modelID)
	if err != nil || p == nil {
		t.Fatalf("读模型：%v %v", p, err)
	}
	if p.APIKey != "sk-pooled-7777" {
		t.Fatalf("模型读到的 Key = %q，want sk-pooled-7777", p.APIKey)
	}
	// 卡片显示的是 hint：它必须跟着折叠过来，否则升级后界面显示「未配置 Key」
	// 而请求其实能发出去，是最难自证的那种不一致。
	list, err := d2.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	var hint string
	for _, x := range list {
		if x.ID == modelID {
			hint = x.APIKeyHint
		}
	}
	if hint != "…7777" {
		t.Fatalf("模型的 hint = %q，want …7777", hint)
	}

	// 幂等：credential_id 已为空，再开一次不应改动任何东西。
	if err := d2.Close(); err != nil {
		t.Fatalf("close 2: %v", err)
	}
	d3, err := Open(dsn)
	if err != nil {
		t.Fatalf("三次打开：%v", err)
	}
	defer d3.Close()
	p3, err := d3.ProfileByID(modelID)
	if err != nil || p3 == nil || p3.APIKey != "sk-pooled-7777" {
		t.Fatalf("重开一次后 Key 变了：%+v err=%v", p3, err)
	}
}

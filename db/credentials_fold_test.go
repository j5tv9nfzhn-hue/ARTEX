package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// 旧库升级时，credential_id 绑定必须被「折叠」进 llm_providers.api_key，然后清空。
//
// 为什么必须有这条用例：任务 25 删掉了凭据池（凭证 Tab 与 db/credentials.go），
// 但现场库里 `llm_providers.credential_id` 仍有值。如果折叠没跑或跑在 backfill
// 之后，升级后的库会出现「供应商没有 Key」——所有模型立刻 401，而且从界面上看
// 是一次「升级把配置弄丢了」。这条路径在新建库上永远不会触发，只能用老库形态测。

// seedLegacyCredentialBinding 造一个「凭据池时代」的库：一条凭据 + 一个绑定它的供应商
// + 挂在该供应商下的两个模型（一个自身无 Key、一个自身也有 Key）。
func seedLegacyCredentialBinding(t *testing.T, dsn string) {
	t.Helper()
	con, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer con.Close()

	stmts := []string{
		`INSERT INTO credentials(id, name, api_key, api_key_hint) VALUES (1, 'main', 'enc:v1:LEGACY', '')`,
		`INSERT INTO llm_providers(id, name, format, base_url, api_key, credential_id)
		 VALUES (1, 'old-provider', 'openai', 'https://api.example.com', NULL, 1)`,
		`INSERT INTO llm_profiles(id, provider_id, name, format, model, api_key, api_key_hint)
		 VALUES (10, 1, 'm-bound', 'openai', 'gpt-x', NULL, ''),
		        (11, 1, 'm-inline', 'openai', 'gpt-y', 'sk-inline', '...line')`,
	}
	for _, stmt := range stmts {
		if _, err := con.Exec(stmt); err != nil {
			t.Fatalf("seed: %v\n  > %s", err, stmt)
		}
	}
}

func TestFoldCredentialsIntoProviders(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "legacy-cred.db") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	con, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	for _, stmt := range sqliteStatements(schemaSQLiteSQL) {
		if _, err := con.Exec(stmt); err != nil {
			con.Close()
			t.Fatalf("build fixture schema: %v\n  > %s", err, stmt)
		}
	}
	_ = con.Close()
	seedLegacyCredentialBinding(t, dsn)

	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("打开老库失败：%v", err)
	}
	defer d.Close()

	// 1. 绑定的供应商应拿到凭据里的 Key（原文保留：折叠只是搬家，不重新加密）。
	var key sql.NullString
	var credID sql.NullInt64
	if err := d.QueryRow(`SELECT api_key, credential_id FROM llm_providers WHERE id=1`).Scan(&key, &credID); err != nil {
		t.Fatalf("query provider: %v", err)
	}
	if !key.Valid || key.String != "enc:v1:LEGACY" {
		t.Fatalf("Key 应被折叠进 api_key，得到 %+v", key)
	}
	if credID.Valid {
		t.Fatalf("折叠后 credential_id 必须清空，得到 %+v", credID)
	}

	// 2. 模型自己的内联 Key 不能被供应商的 Key 覆盖，绑定标记要一起清掉。
	var inline sql.NullString
	var inlineCred sql.NullInt64
	if err := d.QueryRow(`SELECT api_key, credential_id FROM llm_profiles WHERE id=11`).Scan(&inline, &inlineCred); err != nil {
		t.Fatalf("query profile: %v", err)
	}
	if !inline.Valid || inline.String != "sk-inline" {
		t.Fatalf("模型自身的内联 Key 不该被改动，得到 %+v", inline)
	}
	if inlineCred.Valid {
		t.Fatalf("模型上的 credential_id 也必须清空，得到 %+v", inlineCred)
	}

	// 说明：这里不校验「读路径解出明文 Key」——折叠只是把密文原样搬家，解密需要
	// 主密钥，而测试库没有安装它（credential.key 属于运行时资产）。密文搬家是否
	// 成功，上面的 api_key 断言已经覆盖；解密路径由 TestProfileKeyEncryptedAtRest 覆盖。
}

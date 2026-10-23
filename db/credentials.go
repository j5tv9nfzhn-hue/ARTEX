package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 凭据独立实体（计划.md 46–47）：供应商 API Key 从 llm_profiles 抽出，一份凭据
// 可被多个模型配置引用。加密口径与 llm_profiles.api_key 完全一致（enc:v1:
// AES-256-GCM，见 db/credential_crypto.go）；对外只暴露 api_key_hint（明文尾 4 位）。
//
// 兼容策略（不破坏既有读取）：
//   - 读：profile.credential_id 非空 → 取该凭据的 key；否则沿用 profile 自己的
//     内联列（旧库/旧行为不变）。见 db/config.go 的 profileCols*。
//   - 迁移：MigrateInlineProfileKeys 把「有内联 key 且未绑定凭据」的 profile
//     惰性转成凭据行并回填 credential_id；幂等、可重复启动、失败不改动原行。
type Credential struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	APIKey     string    `json:"-"` // never serialized to UI
	APIKeyHint string    `json:"api_key_hint,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// ProfileNames 是当前引用该凭据的模型配置名（删除前提示用）。
	ProfileNames []string `json:"profile_names,omitempty"`
}

// ErrCredentialNotFound 表示要更新的凭据不存在（删除是幂等的，不报这个）。
var ErrCredentialNotFound = errors.New("凭据不存在")

// credentialHint 由**明文**推导对外提示（末 4 位）。绝不能由密文推导：
// "…9f2a" 是用户唯一认得出的部分，显示 base64 尾巴既无用又是新的泄露面。
func credentialHint(plain string) string {
	if len(plain) < 4 {
		return ""
	}
	return "…" + plain[len(plain)-4:]
}

// ListCredentials returns all credentials with their hints and the names of the
// model profiles referencing each one. Keys are NOT included.
func (d *DB) ListCredentials() ([]*Credential, error) {
	rows, err := d.Query(`SELECT id,name,COALESCE(api_key_hint,''),created_at,updated_at
FROM credentials ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []*Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.Name, &c.APIKeyHint, &c.CreatedAt, &c.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, &c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	// 引用关系一次查完，避免每个凭据一条查询（N+1）。
	refs, err := d.Query(`SELECT credential_id,name FROM llm_profiles
WHERE credential_id IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer refs.Close()
	byID := map[int64]*Credential{}
	for _, c := range out {
		byID[c.ID] = c
	}
	for refs.Next() {
		var id int64
		var name string
		if err := refs.Scan(&id, &name); err != nil {
			return nil, err
		}
		if c := byID[id]; c != nil {
			c.ProfileNames = append(c.ProfileNames, name)
		}
	}
	return out, refs.Err()
}

// CredentialByID returns one credential with its decrypted key, or nil.
// Used by the runtime read path when a profile binds a credential.
func (d *DB) CredentialByID(id int64) (*Credential, error) {
	var c Credential
	var stored string
	err := d.QueryRow(`SELECT id,name,COALESCE(api_key,''),COALESCE(api_key_hint,''),created_at,updated_at
FROM credentials WHERE id=$1`, id).Scan(&c.ID, &c.Name, &stored, &c.APIKeyHint, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if credentialEncrypted(stored) {
		plain, derr := decryptCredential(stored)
		if derr != nil {
			return nil, fmt.Errorf("credential %d: %w", c.ID, derr)
		}
		stored = plain
	}
	c.APIKey = stored
	return &c, nil
}

// SaveCredential inserts (id==0) or updates a credential. Empty key on update
// keeps the stored one (same contract as SaveProfile). Returns the row id.
func (d *DB) SaveCredential(c *Credential) (int64, error) {
	if c.ID == 0 {
		stored, err := encryptCredential(c.APIKey)
		if err != nil {
			return 0, err
		}
		var id int64
		err = d.QueryRow(`INSERT INTO credentials(name,api_key,api_key_hint)
VALUES ($1,NULLIF($2,''),NULLIF($3,'')) RETURNING id`,
			c.Name, stored, credentialHint(c.APIKey)).Scan(&id)
		return id, err
	}
	if c.APIKey == "" {
		res, err := d.Exec(`UPDATE credentials SET name=$1 WHERE id=$2`, c.Name, c.ID)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, ErrCredentialNotFound
		}
		return c.ID, nil
	}
	stored, err := encryptCredential(c.APIKey)
	if err != nil {
		return 0, err
	}
	res, err := d.Exec(`UPDATE credentials SET name=$1,api_key=$2,api_key_hint=$3 WHERE id=$4`,
		c.Name, stored, credentialHint(c.APIKey), c.ID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrCredentialNotFound
	}
	return c.ID, nil
}

// DeleteCredential removes a credential and unbinds every profile pointing at it,
// returning how many profiles lost their binding. The unbind is explicit (not only
// the FK's ON DELETE SET NULL) so the behaviour is identical on both engines and
// does not depend on the runtime foreign_keys pragma.
func (d *DB) DeleteCredential(id int64) (int, error) {
	tx, err := d.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// 计数口径仍是「有多少个模型会因此失去 Key」——用户在删除确认里看到的就是这个
	// 数。绑定本身落在供应商行上，所以既算直接绑定的模型行，也算经由供应商绑定的。
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM llm_profiles p
WHERE p.credential_id=$1 OR p.provider_id IN (SELECT v.id FROM llm_providers v WHERE v.credential_id=$1)`, id).Scan(&n); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE llm_providers SET credential_id=NULL WHERE credential_id=$1`, id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE llm_profiles SET credential_id=NULL WHERE credential_id=$1`, id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM credentials WHERE id=$1`, id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// CredentialMigrationResult reports what MigrateInlineProfileKeys did.
type CredentialMigrationResult struct {
	// Migrated counts profiles whose inline key became a credential row.
	Migrated int
	// Skipped lists profiles left untouched because their stored key could not be
	// read (typically a ciphertext row and a missing master key file). They keep
	// working exactly as before — the read path already surfaces the same error.
	Skipped []int64
}

// MigrateInlineProfileKeys moves every profile that still carries an inline key
// into a credentials row and back-fills credential_id (计划 46–47 的幂等迁移路径)。
//
// 幂等：只处理 credential_id IS NULL 且 api_key 非空的行，迁移后两类条件都不再
// 成立，重复启动/重复调用都是空操作。
//
// 不丢明文：先把存量值解密成明文，再由写入漏斗重新加密进 credentials。读不出来
// （主密钥缺失/密文损坏）的行原样跳过并记录在 Skipped 里——绝不因此丢 key，
// 也绝不 panic；该行的读取行为与迁移前完全一致。
//
// 迁移成功后清空 profile 的内联 api_key/api_key_hint：绑定态下读路径只认凭据行，
// 留着旧副本只会造成「改的是 A、用的是 B」的错觉。
func (d *DB) MigrateInlineProfileKeys() (CredentialMigrationResult, error) {
	var res CredentialMigrationResult
	// 内联 Key 现在住在供应商行上（重构前是 llm_profiles）——迁移的目标随之改变：
	// 一个供应商一把 Key，抽出一条凭据，然后镜像回它名下的模型行。
	// 未归属供应商的历史行（provider_id IS NULL，只可能出现在回填失败的瞬间）
	// 仍按旧规则处理，保证这条路不会因为重构而漏掉任何一把 Key。
	rows, err := d.Query(`SELECT id,name,COALESCE(api_key,'') FROM llm_providers
WHERE credential_id IS NULL AND COALESCE(api_key,'') <> ''
ORDER BY id`)
	if err != nil {
		return res, err
	}
	type pending struct {
		id   int64
		name string
		key  string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.name, &p.key); err != nil {
			rows.Close()
			return res, err
		}
		todo = append(todo, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return res, err
	}
	rows.Close()

	for _, p := range todo {
		plain, derr := decryptCredential(p.key)
		if derr != nil {
			res.Skipped = append(res.Skipped, p.id)
			continue
		}
		if err := d.migrateOneInlineKey(p.id, p.name, plain, true); err != nil {
			return res, err
		}
		res.Migrated++
	}
	// 兜底：还没有供应商归属的模型行（旧库回填失败/新库刚建）继续走原路径。
	legacy, err := d.Query(`SELECT id,name,COALESCE(api_key,'') FROM llm_profiles
WHERE provider_id IS NULL AND credential_id IS NULL AND COALESCE(api_key,'') <> ''
ORDER BY id`)
	if err != nil {
		return res, err
	}
	var legacyTodo []pending
	for legacy.Next() {
		var p pending
		if err := legacy.Scan(&p.id, &p.name, &p.key); err != nil {
			legacy.Close()
			return res, err
		}
		legacyTodo = append(legacyTodo, p)
	}
	if err := legacy.Err(); err != nil {
		legacy.Close()
		return res, err
	}
	legacy.Close()
	for _, p := range legacyTodo {
		plain, derr := decryptCredential(p.key)
		if derr != nil {
			res.Skipped = append(res.Skipped, p.id)
			continue
		}
		if err := d.migrateOneInlineKey(p.id, p.name, plain, false); err != nil {
			return res, err
		}
		res.Migrated++
	}
	return res, nil
}

// migrateOneInlineKey 在一条事务里建凭据并把所有者改绑过去：崩在中间也不会
// 出现「Key 两边都不在」。
//
// isProvider 区分 id 属于哪张表——供应商与模型的 id 各自自增，拿一个 id 去另一张表
// 里更新会静默改错行。
func (d *DB) migrateOneInlineKey(ownerID int64, profileName, plain string, isProvider bool) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	name, err := uniqueCredentialName(tx, profileName, ownerID)
	if err != nil {
		return err
	}
	stored, err := encryptCredential(plain)
	if err != nil {
		return err
	}
	var cid int64
	if err := tx.QueryRow(`INSERT INTO credentials(name,api_key,api_key_hint)
VALUES ($1,NULLIF($2,''),NULLIF($3,'')) RETURNING id`, name, stored, credentialHint(plain)).Scan(&cid); err != nil {
		return err
	}
	if isProvider {
		if _, err := tx.Exec(`UPDATE llm_providers SET credential_id=$1,api_key=NULL,api_key_hint=NULL WHERE id=$2`,
			cid, ownerID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`UPDATE llm_profiles SET credential_id=$1,api_key=NULL,api_key_hint=NULL WHERE id=$2`,
			cid, ownerID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// uniqueCredentialName 给迁移出来的凭据取名：优先沿用配置名（同名即同一把
// key 的直觉），被既有凭据占用时退到「名字 (配置 #id)」。配置 id 唯一，故该
// 兜底名也唯一，重复运行不会产生第二份。
func uniqueCredentialName(tx *sql.Tx, base string, profileID int64) (string, error) {
	if base == "" {
		base = fmt.Sprintf("profile #%d", profileID)
	}
	candidates := []string{base, fmt.Sprintf("%s (配置 #%d)", base, profileID)}
	for _, name := range candidates {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM credentials WHERE name=$1`, name).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return name, nil
		}
	}
	return "", fmt.Errorf("无法为配置 #%d 生成唯一凭据名", profileID)
}

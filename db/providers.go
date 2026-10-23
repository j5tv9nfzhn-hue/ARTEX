package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ---------- LLM providers（模型的上游端点） ----------
//
// 重构前「一家供应商」是隐式的：format + base_url + proxy + key 四个字段各自
// 存在 llm_profiles 上，同一家开三个模型就要重复填三遍。现在它是一条独立记录，
// llm_profiles.provider_id 指过来，模型只保留「模型自己」的参数。
//
// 读路径不要求调用方改：config.go 的 profileCols* 用 LEFT JOIN 把供应商列铺平，
// 所以 db.LLMProfile / DTO / 轮询 / 能力探测 / 任务链看到的仍是同一份扁平结构。
// 这是刻意的——把「存储规范化」和「接口扁平」分开，重构才不会波及 40 多个调用点。

// LLMProvider 是一家供应商（端点 + 鉴权 + 端点级配额）。
type LLMProvider struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Format     string `json:"format"`
	BaseURL    string `json:"base_url,omitempty"`
	Proxy      string `json:"proxy,omitempty"`
	APIKey     string `json:"-"` // 明文只在内存里，永不序列化
	APIKeyHint string `json:"api_key_hint,omitempty"`
	// CredentialID 非空 = Key 以凭据行为准（此时内联列清空）。
	CredentialID   *int64 `json:"credential_id,omitempty"`
	CredentialName string `json:"credential_name,omitempty"`
	// 端点级限速：一个 Key 一份额度，所以按供应商而不是按模型计。
	RatePerSecond float64 `json:"rate_per_second"`
	RatePerMinute float64 `json:"rate_per_minute"`
	// 网关会话头：同一条网关链路对所有模型一致，故也放在这一层。
	SessionHeaderKey string `json:"session_header_key,omitempty"`
	// ModelCount / Models 是读路径的附加信息：列表接口一次带回该供应商下的模型，
	// 前端据此渲染「供应商卡片 + 模型行」，不必再发 N 个请求。
	ModelCount int           `json:"model_count"`
	Models     []*LLMProfile `json:"models,omitempty"`
}

var (
	ErrProviderNotFound = errors.New("供应商不存在")
	ErrProviderNotEmpty = errors.New("该供应商下还有模型，先删除或移走这些模型")
	ErrProviderNameUsed = errors.New("供应商名称已被占用")
)

const providerSelect = `SELECT v.id,v.name,v.format,COALESCE(v.base_url,''),COALESCE(v.proxy,''),COALESCE(v.api_key,''),
COALESCE(v.api_key_hint,''),v.credential_id,COALESCE((SELECT c.name FROM credentials c WHERE c.id=v.credential_id),''),
v.rate_per_second,v.rate_per_minute,COALESCE(v.session_header_key,'')
FROM llm_providers v`

func scanProvider(sc interface{ Scan(...any) error }, v *LLMProvider) error {
	var credID sql.NullInt64
	var stored string
	if err := sc.Scan(&v.ID, &v.Name, &v.Format, &v.BaseURL, &v.Proxy, &stored,
		&v.APIKeyHint, &credID, &v.CredentialName,
		&v.RatePerSecond, &v.RatePerMinute, &v.SessionHeaderKey); err != nil {
		return err
	}
	if credID.Valid && credID.Int64 > 0 {
		id := credID.Int64
		v.CredentialID = &id
	}
	if credentialEncrypted(stored) {
		plain, err := decryptCredential(stored)
		if err != nil {
			return fmt.Errorf("provider %d: %w", v.ID, err)
		}
		stored = plain
	}
	v.APIKey = stored
	return nil
}

// ListProviders returns every provider with its models attached, in id order.
// One query for the providers and one for all models (grouped in Go) — the
// settings page renders the whole tree at once.
func (d *DB) ListProviders() ([]*LLMProvider, error) {
	rows, err := d.Query(providerSelect + ` ORDER BY v.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LLMProvider
	byID := map[int64]*LLMProvider{}
	for rows.Next() {
		var v LLMProvider
		if err := scanProvider(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, &v)
		byID[v.ID] = &v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	profs, err := d.ListProfiles()
	if err != nil {
		return nil, err
	}
	for _, p := range profs {
		if v, ok := byID[p.ProviderID]; ok {
			v.Models = append(v.Models, p)
		}
	}
	for _, v := range out {
		v.ModelCount = len(v.Models)
	}
	return out, nil
}

// ProviderByID loads one provider (api key included) or nil.
func (d *DB) ProviderByID(id int64) (*LLMProvider, error) {
	if id <= 0 {
		return nil, nil
	}
	var v LLMProvider
	err := scanProvider(d.QueryRow(providerSelect+` WHERE v.id=$1`, id), &v)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// SaveProvider inserts (id==0) or updates a provider.
//
// 凭据绑定语义与 SaveProfile 完全一致：nil = 不动绑定，0 = 显式解绑，>0 = 绑定。
// 绑定后内联 Key 列被清空，避免「改的是 A、用的是 B」的旧副本。
//
// 写完之后做两件事，都是为了让「存储规范化」对其它读路径不可见：
//   - 把端点字段镜像回该供应商下所有 profile 的历史列（老版本二进制、外部脚本
//     直接读 llm_profiles 时仍然自洽）；
//   - 端点身份（format / base_url）变了就作废这些模型的能力快照——旧端点探到
//     的窗口对新端点没有意义。
func (d *DB) SaveProvider(v *LLMProvider) (int64, error) {
	if v == nil {
		return 0, errors.New("空的供应商")
	}
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		return 0, errors.New("供应商名称不能为空")
	}
	switch v.Format {
	case "openai", "anthropic", "openai-responses":
	default:
		return 0, fmt.Errorf("不支持的格式 %q", v.Format)
	}
	bindID := int64(0)
	if v.CredentialID != nil {
		bindID = *v.CredentialID
	}
	if bindID > 0 {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM credentials WHERE id=$1`, bindID).Scan(&n); err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, ErrCredentialNotFound
		}
	}
	// 名称唯一，但只在「换了个名字」时检查——原样保存不该因为自己而失败。
	var clash int
	if err := d.QueryRow(`SELECT COUNT(*) FROM llm_providers WHERE name=$1 AND id<>$2`, v.Name, v.ID).Scan(&clash); err != nil {
		return 0, err
	}
	if clash > 0 {
		return 0, ErrProviderNameUsed
	}

	hint := v.APIKeyHint
	if len(v.APIKey) >= 4 {
		hint = "…" + v.APIKey[len(v.APIKey)-4:]
	}
	stored, err := encryptCredential(v.APIKey)
	if err != nil {
		return 0, err
	}
	inlineKey, inlineHint := stored, hint
	var credentialArg any
	if bindID > 0 {
		inlineKey, inlineHint, credentialArg = "", "", bindID
	}

	prevFormat, prevBase := "", ""
	if v.ID > 0 {
		// 读失败不阻断保存：与 SaveProfile 的 staleCaps 处理同一口径。
		_ = d.QueryRow(`SELECT format,COALESCE(base_url,'') FROM llm_providers WHERE id=$1`, v.ID).
			Scan(&prevFormat, &prevBase)
	}

	var id int64
	if v.ID == 0 {
		err = d.QueryRow(`INSERT INTO llm_providers(name,format,base_url,proxy,api_key,api_key_hint,credential_id,rate_per_second,rate_per_minute,session_header_key)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$7,$8,$9,$10) RETURNING id`,
			v.Name, v.Format, v.BaseURL, v.Proxy, inlineKey, inlineHint, credentialArg,
			v.RatePerSecond, v.RatePerMinute, v.SessionHeaderKey).Scan(&id)
		if err != nil {
			return 0, err
		}
	} else {
		id = v.ID
		// 三种绑定状态一条语句覆盖：内联列只在「提供了新 key」或「绑定了凭据」时
		// 改写；没表态（key 留空且 credential_id 为 nil）时原样保留。
		credMode := 0
		if v.CredentialID != nil {
			if bindID > 0 {
				credMode = 1
			} else {
				credMode = 2
			}
		}
		_, err = d.Exec(`UPDATE llm_providers SET
name=$1,format=$2,base_url=NULLIF($3,''),proxy=NULLIF($4,''),
api_key      = CASE WHEN $11 = 1 THEN NULL WHEN $5 <> '' THEN NULLIF($5,'') ELSE api_key END,
api_key_hint = CASE WHEN $11 = 1 THEN NULL WHEN $5 <> '' THEN NULLIF($6,'') ELSE api_key_hint END,
rate_per_second=$7,rate_per_minute=$8,session_header_key=$9,
credential_id = CASE WHEN $11 = 1 THEN $12 WHEN $11 = 2 THEN NULL ELSE credential_id END
WHERE id=$10`,
			v.Name, v.Format, v.BaseURL, v.Proxy, stored, hint,
			v.RatePerSecond, v.RatePerMinute, v.SessionHeaderKey, id, credMode, bindID)
		if err != nil {
			return 0, err
		}
	}
	v.ID = id
	if err := d.mirrorProviderToProfiles(id); err != nil {
		return id, err
	}
	if prevFormat != "" && (prevFormat != v.Format || prevBase != v.BaseURL) {
		_ = d.clearProviderCaps(id)
	}
	return id, nil
}

// mirrorProviderToProfiles 把供应商的端点字段写回其下所有模型的历史列。
//
// 为什么要镜像：llm_profiles 的 format/base_url/api_key/... 这组列在本次重构后
// 不再是事实来源，但仍是「任何只读这张表的代码」看到的唯一东西——备份导出、
// 外部脚本、以及万一要临时回退到旧二进制。镜像的成本是一条 UPDATE，收益是这张
// 表在任何单表视图下都不自相矛盾。
func (d *DB) mirrorProviderToProfiles(providerID int64) error {
	if providerID <= 0 {
		return nil
	}
	_, err := d.Exec(`UPDATE llm_profiles SET
format=(SELECT v.format FROM llm_providers v WHERE v.id=$1),
base_url=(SELECT v.base_url FROM llm_providers v WHERE v.id=$1),
proxy=(SELECT v.proxy FROM llm_providers v WHERE v.id=$1),
rate_per_second=(SELECT v.rate_per_second FROM llm_providers v WHERE v.id=$1),
rate_per_minute=(SELECT v.rate_per_minute FROM llm_providers v WHERE v.id=$1),
session_header_key=(SELECT v.session_header_key FROM llm_providers v WHERE v.id=$1),
api_key=(SELECT CASE WHEN v.credential_id IS NOT NULL THEN NULL ELSE v.api_key END FROM llm_providers v WHERE v.id=$1),
api_key_hint=(SELECT CASE WHEN v.credential_id IS NOT NULL THEN NULL ELSE v.api_key_hint END FROM llm_providers v WHERE v.id=$1),
credential_id=(SELECT v.credential_id FROM llm_providers v WHERE v.id=$1)
WHERE provider_id=$1`, providerID)
	return err
}

// clearProviderCaps 作废某供应商下所有模型的能力快照（端点变了 → 旧快照无意义）。
func (d *DB) clearProviderCaps(providerID int64) error {
	rows, err := d.Query(`SELECT id FROM llm_profiles WHERE provider_id=$1`, providerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		_ = d.ClearModelCaps(id)
	}
	return nil
}

// DeleteProvider removes an empty provider. Providers with models are refused:
// deleting them would cascade into task chains, agent bindings and conversations,
// and "silently unhook 3 tasks" is not a good default for a settings page.
func (d *DB) DeleteProvider(id int64) error {
	if id <= 0 {
		return ErrProviderNotFound
	}
	var models int
	if err := d.QueryRow(`SELECT COUNT(*) FROM llm_profiles WHERE provider_id=$1`, id).Scan(&models); err != nil {
		return err
	}
	if models > 0 {
		return fmt.Errorf("%w（%d 个）", ErrProviderNotEmpty, models)
	}
	res, err := d.Exec(`DELETE FROM llm_providers WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProviderNotFound
	}
	return nil
}

// ---------- 端点补丁（旧客户端写模型的兼容路径） ----------

// EndpointPatch 是「一次模型保存顺带带来的端点级改动」。
//
// 为什么需要它：重构前前端把端点字段和模型字段混在一个请求里提交；重构后新界面
// 只提交模型字段 + provider_id。为了让旧客户端（以及手机端）在过渡期继续可用，
// 又能让「改了 Base URL」真的落到供应商行上，请求体里这几个字段用指针接收：
// nil = 没提这个字段（不动供应商），非 nil = 明确要改。
type EndpointPatch struct {
	Format           *string
	BaseURL          *string
	Proxy            *string
	SessionHeaderKey *string
	RatePerSecond    *float64
	RatePerMinute    *float64
	// CredentialID: nil = 不动；0 = 解绑；>0 = 绑定。
	CredentialID *int64
	// APIKey 非空 = 换一把 Key（明文）。
	APIKey string
}

// empty 报告这份补丁是否什么都没说，避免无谓的供应商读写。
func (p EndpointPatch) empty() bool {
	return p.Format == nil && p.BaseURL == nil && p.Proxy == nil && p.SessionHeaderKey == nil &&
		p.RatePerSecond == nil && p.RatePerMinute == nil && p.CredentialID == nil && p.APIKey == ""
}

// applyEndpointPatch 把补丁套到供应商行上并落库。
func (d *DB) applyEndpointPatch(providerID int64, patch EndpointPatch) error {
	if providerID <= 0 || patch.empty() {
		return nil
	}
	v, err := d.ProviderByID(providerID)
	if err != nil {
		return err
	}
	if v == nil {
		return ErrProviderNotFound
	}
	if patch.Format != nil {
		v.Format = *patch.Format
	}
	if patch.BaseURL != nil {
		v.BaseURL = *patch.BaseURL
	}
	if patch.Proxy != nil {
		v.Proxy = *patch.Proxy
	}
	if patch.SessionHeaderKey != nil {
		v.SessionHeaderKey = *patch.SessionHeaderKey
	}
	if patch.RatePerSecond != nil {
		v.RatePerSecond = *patch.RatePerSecond
	}
	if patch.RatePerMinute != nil {
		v.RatePerMinute = *patch.RatePerMinute
	}
	if patch.CredentialID != nil {
		id := *patch.CredentialID
		v.CredentialID = &id
	}
	if patch.APIKey != "" {
		v.APIKey = patch.APIKey
	}
	_, err = d.SaveProvider(v)
	return err
}

// newProviderForProfile 给「没有 provider_id 的旧式保存」造一条供应商。
//
// 不按端点自动合并：加密是随机 nonce，两条同样的 Key 密文并不相等，用密文做合并
// 键会漏；用端点字面量合并又会把「同一网关下两家不同账号」错并成一个。宁可先
// 1:1 建出来，让用户在界面上显式把模型挪进已有的供应商。
func (d *DB) newProviderForProfile(p *LLMProfile) (int64, error) {
	name, err := d.uniqueProviderName(p.Name)
	if err != nil {
		return 0, err
	}
	v := &LLMProvider{
		Name:             name,
		Format:           p.Format,
		BaseURL:          p.BaseURL,
		Proxy:            p.Proxy,
		APIKey:           p.APIKey,
		RatePerSecond:    p.RatePerSecond,
		RatePerMinute:    p.RatePerMinute,
		SessionHeaderKey: p.SessionHeaderKey,
		CredentialID:     p.CredentialID,
	}
	return d.SaveProvider(v)
}

// uniqueProviderName 在重名时追加 (2)/(3)…。供应商名是给人看的，不该因为撞名
// 让一次保存失败。
func (d *DB) uniqueProviderName(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "供应商"
	}
	name := base
	for i := 2; i < 100; i++ {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM llm_providers WHERE name=$1`, name).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return name, nil
		}
		name = fmt.Sprintf("%s (%d)", base, i)
	}
	return "", ErrProviderNameUsed
}

// MoveProfileToProvider 把一条模型改挂到另一家供应商。
//
// 用途：建模型时选错了供应商，或者在回填之后想把同端点下的几条模型并到一起。
// 端点随之改变，所以这条模型的能力快照必须作废——否则新端点会继续沿用旧端点探到
// 的窗口与输出上限。
func (d *DB) MoveProfileToProvider(profileID, providerID int64) error {
	if profileID <= 0 || providerID <= 0 {
		return ErrProviderNotFound
	}
	v, err := d.ProviderByID(providerID)
	if err != nil {
		return err
	}
	if v == nil {
		return ErrProviderNotFound
	}
	res, err := d.Exec(`UPDATE llm_profiles SET provider_id=$1 WHERE id=$2`, providerID, profileID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLLMProfileNotFound
	}
	if err := d.mirrorProviderToProfiles(providerID); err != nil {
		return err
	}
	_ = d.ClearModelCaps(profileID)
	return nil
}

// ---------- 一次性回填 ----------

// sqlRunner 是 *sql.Conn 与 *sql.Tx 的公共子集，回填在开库阶段用前者。
type sqlRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// backfillLLMProviders 把 provider_id 为空的存量配置收进供应商行。
//
// 合并规则：format + base_url + proxy + 凭据 + **解密后的 Key 明文** 全相同 →
// 同一条供应商。用明文而不是密文比较，是因为 encryptCredential 每次都带新的
// nonce，同样的 Key 存两次密文也不同。
//
// 模型改名：1:1 回填时旧配置的 name 往往是供应商名（线上 #19「新疆中转站」其实
// 是供应商名，模型是 Qwen3.8-Flash-Next）。供应商拿走这个名字后，模型若还叫
// 同一个名字，界面上就会出现「新疆中转站 / 新疆中转站」两行同名。此时把模型名
// 改成模型 id —— 名字冲突才跳过。
//
// 幂等：只处理 provider_id IS NULL 的行；新库上来就是空操作。
func backfillLLMProviders(ctx context.Context, con sqlRunner) error {
	rows, err := con.QueryContext(ctx, `SELECT id,name,format,COALESCE(base_url,''),COALESCE(proxy,''),model,
COALESCE(api_key,''),COALESCE(api_key_hint,''),COALESCE(credential_id,0),rate_per_second,rate_per_minute,COALESCE(session_header_key,'')
FROM llm_profiles WHERE provider_id IS NULL ORDER BY id`)
	if err != nil {
		return err
	}
	type legacy struct {
		id                                  int64
		name, format, baseURL, proxy, model string
		storedKey, hint                     string
		credID                              int64
		rps, rpm                            float64
		sessionHeader                       string
	}
	var pending []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.id, &l.name, &l.format, &l.baseURL, &l.proxy, &l.model,
			&l.storedKey, &l.hint, &l.credID, &l.rps, &l.rpm, &l.sessionHeader); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(pending) == 0 {
		return nil
	}

	key := func(l legacy) string {
		plain := l.storedKey
		if credentialEncrypted(plain) {
			if dec, derr := decryptCredential(plain); derr == nil {
				plain = dec
			}
		}
		return strings.Join([]string{l.format, l.baseURL, l.proxy, fmt.Sprint(l.credID), plain}, "\x00")
	}
	byKey := map[string]int64{}
	for _, l := range pending {
		k := key(l)
		providerID, seen := byKey[k]
		if !seen {
			// 供应商名要进卡片标题，旧配置的 name 有时是空串或纯空白。
			base := strings.TrimSpace(l.name)
			if base == "" {
				base = strings.TrimSpace(l.model)
			}
			if base == "" {
				base = "供应商"
			}
			// 名字可能已被（另一批的）供应商占用 → 加后缀，绝不因此中断开库。
			name := base
			for i := 2; ; i++ {
				var n int
				if err := con.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_providers WHERE name=$1`, name).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					break
				}
				name = fmt.Sprintf("%s (%d)", base, i)
			}
			var credArg any
			if l.credID > 0 {
				credArg = l.credID
			}
			inlineKey, inlineHint := l.storedKey, l.hint
			if l.credID > 0 {
				inlineKey, inlineHint = "", ""
			}
			if err := con.QueryRowContext(ctx, `INSERT INTO llm_providers(name,format,base_url,proxy,api_key,api_key_hint,credential_id,rate_per_second,rate_per_minute,session_header_key)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$7,$8,$9,$10) RETURNING id`,
				name, l.format, l.baseURL, l.proxy, inlineKey, inlineHint, credArg, l.rps, l.rpm, l.sessionHeader).Scan(&providerID); err != nil {
				return err
			}
			byKey[k] = providerID
		}
		if _, err := con.ExecContext(ctx, `UPDATE llm_profiles SET provider_id=$1 WHERE id=$2`, providerID, l.id); err != nil {
			return err
		}
		// 1:1 且同名 → 把模型名让给模型 id，避免卡片上两行同名。
		legacyName := strings.TrimSpace(l.name)
		if model := strings.TrimSpace(l.model); model != "" && legacyName != "" && legacyName != model {
			var owner string
			if err := con.QueryRowContext(ctx, `SELECT name FROM llm_providers WHERE id=$1`, providerID).Scan(&owner); err == nil && owner == legacyName {
				var taken int
				if err := con.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_profiles WHERE name=$1 AND id<>$2`, model, l.id).Scan(&taken); err == nil && taken == 0 {
					_, _ = con.ExecContext(ctx, `UPDATE llm_profiles SET name=$1 WHERE id=$2`, model, l.id)
				}
			}
		}
	}
	return nil
}

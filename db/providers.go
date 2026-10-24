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
	// 端点级限速：一个 Key 一份额度，所以按供应商而不是按模型计。
	RatePerSecond float64 `json:"rate_per_second"`
	RatePerMinute float64 `json:"rate_per_minute"`
	// 网关会话头：同一条网关链路对所有模型一致，故也放在这一层。
	SessionHeaderKey string `json:"session_header_key,omitempty"`
	// ModelCount / Models 是读路径的附加信息：列表接口一次带回该供应商下的模型，
	// 前端据此渲染「供应商卡片 + 模型行」，不必再发 N 个请求。
	ModelCount int           `json:"model_count"`
	Models     []*LLMProfile `json:"models,omitempty"`
	// KeyError 非空 = 存库的 Key 读不出来（主密钥丢失或换过）。列表接口把它带
	// 出去让界面直说原因；真正要发请求的读取方（ProviderByID）会把它升级成错误。
	// 不这样做的话，一条坏 Key 会让整个设置页 500——连改回正确 Key 的入口都没有。
	KeyError string `json:"key_error,omitempty"`
}

var (
	ErrProviderNotFound = errors.New("供应商不存在")
	ErrProviderNotEmpty = errors.New("该供应商下还有模型，先删除或移走这些模型")
	ErrProviderNameUsed = errors.New("供应商名称已被占用")
)

const providerSelect = `SELECT v.id,v.name,v.format,COALESCE(v.base_url,''),COALESCE(v.proxy,''),COALESCE(v.api_key,''),
COALESCE(v.api_key_hint,''),
v.rate_per_second,v.rate_per_minute,COALESCE(v.session_header_key,'')
FROM llm_providers v`

func scanProvider(sc interface{ Scan(...any) error }, v *LLMProvider) error {
	var stored string
	if err := sc.Scan(&v.ID, &v.Name, &v.Format, &v.BaseURL, &v.Proxy, &stored,
		&v.APIKeyHint,
		&v.RatePerSecond, &v.RatePerMinute, &v.SessionHeaderKey); err != nil {
		return err
	}
	if credentialEncrypted(stored) {
		plain, err := decryptCredential(stored)
		if err != nil {
			// 记在行上交给调用方决定：列表页要能显示这一行并让人改它，发请求的
			// 路径则必须硬失败（空 Key 发出去只会换来一个离原因很远的 401）。
			v.KeyError = fmt.Sprintf("provider %d: %v", v.ID, err)
			stored = ""
		} else {
			stored = plain
		}
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
	if v.KeyError != "" {
		// 读不出 Key 就不能拿去发请求：这里的失败要响，不能退化成"没配 Key"。
		return nil, errors.New(v.KeyError)
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
//   - 端点身份变了只影响「请求发去哪」，窗口与输出上限仍按用户手填的值走。
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

	prevFormat, prevBase := "", ""
	if v.ID > 0 {
		// 读失败不阻断保存：与 SaveProfile 的 staleCaps 处理同一口径。
		_ = d.QueryRow(`SELECT format,COALESCE(base_url,'') FROM llm_providers WHERE id=$1`, v.ID).
			Scan(&prevFormat, &prevBase)
	}

	var id int64
	if v.ID == 0 {
		err = d.QueryRow(`INSERT INTO llm_providers(name,format,base_url,proxy,api_key,api_key_hint,rate_per_second,rate_per_minute,session_header_key)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$7,$8,$9) RETURNING id`,
			v.Name, v.Format, v.BaseURL, v.Proxy, inlineKey, inlineHint,
			v.RatePerSecond, v.RatePerMinute, v.SessionHeaderKey).Scan(&id)
		if err != nil {
			return 0, err
		}
	} else {
		id = v.ID
		// Key 留空 = 保持原样（前端编辑时不回显明文，空输入必须是「不改」）；
		// 给了新 Key 就整体替换，连同 hint 一起重算。
		_, err = d.Exec(`UPDATE llm_providers SET
name=$1,format=$2,base_url=NULLIF($3,''),proxy=NULLIF($4,''),
api_key      = CASE WHEN $5 <> '' THEN NULLIF($5,'') ELSE api_key END,
api_key_hint = CASE WHEN $5 <> '' THEN NULLIF($6,'') ELSE api_key_hint END,
rate_per_second=$7,rate_per_minute=$8,session_header_key=$9
WHERE id=$10`,
			v.Name, v.Format, v.BaseURL, v.Proxy, stored, hint,
			v.RatePerSecond, v.RatePerMinute, v.SessionHeaderKey, id)
		if err != nil {
			return 0, err
		}
	}
	v.ID = id
	if err := d.mirrorProviderToProfiles(id); err != nil {
		return id, err
	}
	if prevFormat != "" && (prevFormat != v.Format || prevBase != v.BaseURL) {
		// 能力探测已移除：端点变更不再需要作废任何快照。判断保留，因为
		// prevFormat/prevBase 仍是「这次保存把端点改动了」的唯一落点。
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
api_key=(SELECT v.api_key FROM llm_providers v WHERE v.id=$1),
api_key_hint=(SELECT v.api_key_hint FROM llm_providers v WHERE v.id=$1)
WHERE provider_id=$1`, providerID)
	return err
}

// DeleteProvider removes an empty provider. Providers with models are refused:
// deleting them would cascade into task chains, agent bindings and conversations,
// and "silently unhook 3 tasks" is not a good default for a settings page.
// DeleteProvider 删除一家供应商，连同它名下的全部模型（级联）。
//
// 旧实现是「名下还有模型就 409，让用户先逐个删干净」：一个供应商开五个模型
// 就要点六次，而且中间任意一步失败还得从头再来。现在一次事务做完三件事：
//
//  1. 引用这些模型的任务把 llm_chain_revision +1，唤醒后重算故障转移链；
//  2. 删掉模型行（老库的 llm_profiles.provider_id 是后补的列、没有
//     ON DELETE CASCADE，所以必须显式删；新库 schema 已声明级联，两条路径
//     结果一致）。task_llm_profiles / agent_llm_fallbacks / llm_profile_health
//     由各自的 ON DELETE CASCADE 收尾，tasks / agents / conversations 的
//     引用列由 ON DELETE SET NULL 收尾——这些都是建表时就有的，老库同样生效；
//  3. 如果删掉的模型里有「当前激活」的那条，把激活位交给剩下的最优模型；
//     一条都不剩就留空（界面显示未配置，而不是继续跑一个已经删掉的身份）。
//
// 返回被一起删掉的模型数量，供调用方在提示里说清「顺带删了几条」。
func (d *DB) DeleteProvider(id int64) (int, error) {
	if id <= 0 {
		return 0, ErrProviderNotFound
	}
	tx, err := d.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM llm_providers WHERE id=$1`, id).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, ErrProviderNotFound
	}
	var wasDefault int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM llm_profiles WHERE provider_id=$1 AND is_default`, id).Scan(&wasDefault); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE tasks SET llm_chain_revision=llm_chain_revision+1
WHERE llm_profile_id IN (SELECT id FROM llm_profiles WHERE provider_id=$1)
   OR active_llm_profile_id IN (SELECT id FROM llm_profiles WHERE provider_id=$1)
   OR id IN (SELECT x.task_id FROM task_llm_profiles x
             JOIN llm_profiles p ON p.id=x.profile_id WHERE p.provider_id=$1)`, id); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM llm_profiles WHERE provider_id=$1`, id)
	if err != nil {
		return 0, err
	}
	models, _ := res.RowsAffected()

	res, err = tx.Exec(`DELETE FROM llm_providers WHERE id=$1`, id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrProviderNotFound
	}
	if wasDefault > 0 {
		if err := promoteDefaultProfileTx(tx); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(models), nil
}

// promoteDefaultProfileTx 在「激活模型刚被删掉」之后把激活位交给下一个可用模型。
//
// 排序与轮询链一致（priority DESC, id ASC）：用户没显式选的时候，接管的最优
// 候选就该是链首。一条模型都不剩时什么也不做——没有默认模型是合法状态，
// 界面据此提示「去配置一个」，而不是让进程继续持有一个已删除身份的副本。
func promoteDefaultProfileTx(tx *sql.Tx) error {
	var hasDefault int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM llm_profiles WHERE is_default`).Scan(&hasDefault); err != nil {
		return err
	}
	if hasDefault > 0 {
		return nil
	}
	_, err := tx.Exec(`UPDATE llm_profiles SET is_default=true
WHERE id=(SELECT id FROM llm_profiles ORDER BY priority DESC, id ASC LIMIT 1)`)
	return err
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
	// APIKey 非空 = 换一把 Key（明文）。
	APIKey string
}

// empty 报告这份补丁是否什么都没说，避免无谓的供应商读写。
func (p EndpointPatch) empty() bool {
	return p.Format == nil && p.BaseURL == nil && p.Proxy == nil && p.SessionHeaderKey == nil &&
		p.RatePerSecond == nil && p.RatePerMinute == nil && p.APIKey == ""
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
// 端点随之改变，但它的大小仍然只由用户手填的窗口/输出上限决定。
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
	return nil
}

// ---------- 一次性回填 ----------

// sqlRunner 是 *sql.Conn 与 *sql.Tx 的公共子集，回填在开库阶段用前者。
type sqlRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// foldCredentialsIntoProviders 把「独立凭据」里的 Key 折叠回供应商行，然后解绑。
//
// 凭据池是重构过程中的中间形态：Key 曾经被抽成可复用的独立实体，结果界面上多出
// 一个「凭证」标签页，用户要先建凭据、再去供应商里挑，才能填一把 Key。现在收回
// 到一个事实来源——Key 就存在供应商行上（api_key / api_key_hint）。
//
// 这一步只搬数据，不改语义：绑定过的供应商，其 Key 原样（仍是密文，不加解密）
// 从 credentials 行复制过来，随后 credential_id 置空。幂等——折过一遍之后
// credential_id 恒为 NULL，再跑就是空操作。
//
// 表与列本身不删：SQLite 给已有表删列要重建整张表，在有数据的现场库上得不偿失，
// 而留下的空壳不影响任何读写路径。
func foldCredentialsIntoProviders(ctx context.Context, con sqlRunner) (int64, error) {
	var tables int
	if err := con.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='credentials'`).Scan(&tables); err != nil {
		return 0, err
	}
	if tables == 0 {
		return 0, nil
	}
	res, err := con.ExecContext(ctx, `UPDATE llm_providers SET
api_key      = COALESCE((SELECT c.api_key      FROM credentials c WHERE c.id=llm_providers.credential_id), api_key),
api_key_hint = COALESCE((SELECT c.api_key_hint FROM credentials c WHERE c.id=llm_providers.credential_id), api_key_hint),
credential_id = NULL
WHERE credential_id IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("fold provider credentials: %w", err)
	}
	n, _ := res.RowsAffected()
	// 模型行上的历史列同样折一遍：读路径已经不看它，但让数据自洽，
	// 任何只平铺 llm_profiles 的外部脚本才不会读到「绑了凭据却没 Key」。
	if _, err := con.ExecContext(ctx, `UPDATE llm_profiles SET
api_key      = COALESCE((SELECT c.api_key      FROM credentials c WHERE c.id=llm_profiles.credential_id), api_key),
api_key_hint = COALESCE((SELECT c.api_key_hint FROM credentials c WHERE c.id=llm_profiles.credential_id), api_key_hint),
credential_id = NULL
WHERE credential_id IS NOT NULL`); err != nil {
		return n, fmt.Errorf("fold profile credentials: %w", err)
	}
	return n, nil
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

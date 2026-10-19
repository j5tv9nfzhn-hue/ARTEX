package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ---------- LLM profiles ----------

type LLMProfile struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Format        string  `json:"format"`
	BaseURL       string  `json:"base_url,omitempty"`
	Proxy         string  `json:"proxy,omitempty"` // LLM 出站代理(http/https/socks5);空=用环境变量
	Model         string  `json:"model"`
	APIKey        string  `json:"-"` // never serialized to UI
	APIKeyHint    string  `json:"api_key_hint,omitempty"`
	RatePerSecond float64 `json:"rate_per_second"`
	RatePerMinute float64 `json:"rate_per_minute"`
	// ContextWindowK is the model's context window in K tokens, used to size
	// compaction thresholds. 0 = use a 200K default; capped at 1000 (1M).
	ContextWindowK int `json:"context_window_k"`
	// ThinkingType 独立控制思考「开关」(thinking.type):"" = 不发送(默认);
	// "disabled" = 显式关闭; "enabled" = 开启. 与 ReasoningEffort 解耦.
	ThinkingType string `json:"thinking_type"`
	// ReasoningEffort 独立控制思考「强度」:"" = 不发送(默认);
	// "low"/"medium"/"high"/"xhigh"/"max" = 对应强度. 见 agent.Config.NewProvider.
	ReasoningEffort string `json:"reasoning_effort"`
	IsDefault       bool   `json:"is_default"`
	// Priority orders the failover chain: higher goes first. The ACTIVE profile
	// (IsDefault) always heads the chain regardless of this value.
	Priority int `json:"priority"`
	// PoolExclude=true keeps this profile out of the failover chain — it stays
	// usable when an agent/task binds it explicitly, it just never gets picked up
	// as a fallback target.
	PoolExclude bool `json:"pool_exclude"`
	// Streaming selects the wire protocol: true (default) = streaming (SSE);
	// false = real non-streaming (stream:false, single JSON response via
	// Provider.Complete). Non-streaming sidesteps flaky gateway SSE at the cost of
	// live in-run progress. Maps to agent.Config.Stream.
	Streaming bool `json:"streaming"`
	// MaxTokens caps a single reply's output in tokens. 0 = send no cap and let
	// the endpoint's own default apply (the historical behaviour). Unlike
	// ContextWindowK — the model's total capacity, used locally to size compaction
	// — this value travels with every request.
	MaxTokens int `json:"max_tokens"`
	// MaxTokensField picks the request key carrying MaxTokens, for format
	// "openai" only: "" = max_tokens (default); "max_completion_tokens" = the
	// newer key, which OpenAI's reasoning models require and whose budget covers
	// reasoning tokens plus visible output. Ignored by anthropic and
	// openai-responses, which name the field themselves.
	MaxTokensField string `json:"max_tokens_field"`
	// SessionHeaderKey, when non-empty, names a custom HTTP header sent on every
	// request built from this profile; its value is the current run's session id
	// (chat conversation / worker intent). For gateways that key prompt caching
	// or sticky routing off a session-id header. "" = not sent. Maps to
	// agent.Config.SessionHeaderKey.
	SessionHeaderKey string `json:"session_header_key"`
	// Retry overrides this profile's share of the retry ladder. Zero value =
	// inherit the global policy (LLMRetryPolicy), so an untouched profile behaves
	// exactly as before. See RetryOverride.
	Retry RetryOverride `json:"retry"`
	// InputModalities 声明该配置能接收哪些输入模态,逗号分隔的 text/image/audio/
	// pdf。SaveProfile 内统一归一(补 text、规范顺序、丢弃未知取值),故库里只有
	// 一种规范形态;"" = 未声明 → 按模型名保守推断(InferInputModalities)。
	// 运行时路由读 EffectiveInputModalities():配置不支持的附件必须显式报错,
	// 绝不静默丢弃、也不硬塞进纯文本请求。见 db/modalities.go。
	InputModalities string `json:"input_modalities"`
	// CredentialID 绑定凭据(计划 46–47)。nil = 未绑定/不修改绑定;非空指向
	// credentials 行,此时 Key 以凭据为准。见 db/credentials.go 与 SaveProfile。
	CredentialID *int64 `json:"credential_id"`
	// CredentialName 是绑定凭据的展示名(只读,由读路径回填)。
	CredentialName string `json:"credential_name,omitempty"`
}

// RetryOverride is one profile's optional override of the three retry layers
// that are per-endpoint: 建连(connect) / 空响应(empty) / 同 provider 安全窗口
// (stream). Each rule's zero value means "inherit the global policy"; see
// RetryRule for the -1 / 0 / >0 semantics.
type RetryOverride struct {
	Connect RetryRule `json:"connect"`
	Empty   RetryRule `json:"empty"`
	Stream  RetryRule `json:"stream"`
}

// profileCols is the read column list (hint variant, no api key) shared by the
// list query; profileColsKey is the same with api_key for the single-row loads.
const profileRetryCols = `COALESCE(retry_connect_attempts,0),COALESCE(retry_connect_interval_ms,0),COALESCE(retry_empty_attempts,0),COALESCE(retry_empty_interval_ms,0),COALESCE(retry_stream_attempts,0),COALESCE(retry_stream_interval_ms,0)`

// profileModalityCols 挂在两个读列清单末尾;input_modalities 是补旧库列,
// 老库由 openSQLite 的 sqliteColumnAdds 补上,COALESCE 只是与既有列风格一致。
const profileModalityCols = `COALESCE(input_modalities,'')`

// profileCredentialCols 是凭据实体化(计划 46–47)追加的尾列:绑定的凭据 id
// (NULL→0)与凭据展示名。
const profileCredentialCols = `COALESCE(credential_id,0),COALESCE((SELECT c.name FROM credentials c WHERE c.id=llm_profiles.credential_id),'')`

// profileKeyExpr / profileHintExpr 决定「有效 Key / 有效 hint」的来源:绑定了凭据
// 就取凭据行,否则沿用 profile 自己的内联列——旧库、旧配置的行为逐字不变。用
// 相关子查询而不是 JOIN,是为了让既有列清单与 scan 顺序只需尾部追加,所有查询的
// FROM 仍是裸的 llm_profiles。
const profileKeyExpr = `CASE WHEN credential_id IS NULL THEN COALESCE(api_key,'') ELSE COALESCE((SELECT c.api_key FROM credentials c WHERE c.id=llm_profiles.credential_id),'') END`
const profileHintExpr = `CASE WHEN credential_id IS NULL THEN COALESCE(api_key_hint,'') ELSE COALESCE((SELECT c.api_key_hint FROM credentials c WHERE c.id=llm_profiles.credential_id),'') END`

const profileCols = `id,name,format,COALESCE(base_url,''),COALESCE(proxy,''),model,` + profileHintExpr + `,rate_per_second,rate_per_minute,context_window_k,COALESCE(reasoning_effort,''),is_default,priority,pool_exclude,COALESCE(thinking_type,''),COALESCE(streaming,true),COALESCE(max_tokens,0),COALESCE(max_tokens_field,''),COALESCE(session_header_key,''),` + profileRetryCols + `,` + profileModalityCols + `,` + profileCredentialCols
const profileColsKey = `id,name,format,COALESCE(base_url,''),COALESCE(proxy,''),model,` + profileKeyExpr + `,rate_per_second,rate_per_minute,context_window_k,COALESCE(reasoning_effort,''),is_default,priority,pool_exclude,COALESCE(thinking_type,''),COALESCE(streaming,true),COALESCE(max_tokens,0),COALESCE(max_tokens_field,''),COALESCE(session_header_key,''),` + profileRetryCols + `,` + profileModalityCols + `,` + profileCredentialCols

// scanProfile reads one row in the profileCols / profileColsKey column order. The
// 7th column lands in APIKeyHint or APIKey depending on which list the caller used.
//
// The 7th column is decrypted here when it carries the credential ciphertext
// prefix. Doing it in this one funnel means every read path -- ActiveProfile,
// ProfileByID, PoolProfiles -- gets the plaintext without each having to
// remember, and a future read path added elsewhere gets it by construction.
// APIKeyHint never carries the prefix (it is computed from the plaintext before
// saving), so running it on both is harmless. 绑定的凭据行存的同样是密文,故这
// 一步对「来自凭据」与「内联」两种来源是同一条路径。
func scanProfile(sc interface{ Scan(...any) error }, into *string, p *LLMProfile) error {
	var stored string
	var credentialID sql.NullInt64
	if err := sc.Scan(&p.ID, &p.Name, &p.Format, &p.BaseURL, &p.Proxy, &p.Model, &stored,
		&p.RatePerSecond, &p.RatePerMinute, &p.ContextWindowK, &p.ReasoningEffort, &p.IsDefault, &p.Priority, &p.PoolExclude, &p.ThinkingType, &p.Streaming,
		&p.MaxTokens, &p.MaxTokensField, &p.SessionHeaderKey,
		&p.Retry.Connect.Attempts, &p.Retry.Connect.IntervalMS,
		&p.Retry.Empty.Attempts, &p.Retry.Empty.IntervalMS,
		&p.Retry.Stream.Attempts, &p.Retry.Stream.IntervalMS,
		&p.InputModalities, &credentialID, &p.CredentialName); err != nil {
		return err
	}
	if credentialID.Valid && credentialID.Int64 > 0 {
		id := credentialID.Int64
		p.CredentialID = &id
	}
	if credentialEncrypted(stored) {
		plain, err := decryptCredential(stored)
		if err != nil {
			return fmt.Errorf("profile %d: %w", p.ID, err)
		}
		stored = plain
	}
	*into = stored
	return nil
}

func (d *DB) ListProfiles() ([]*LLMProfile, error) {
	rows, err := d.Query(`SELECT ` + profileCols + ` FROM llm_profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LLMProfile
	for rows.Next() {
		var p LLMProfile
		if err := scanProfile(rows, &p.APIKeyHint, &p); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// ActiveProfile returns the default (active) profile with its api key, or nil.
func (d *DB) ActiveProfile() (*LLMProfile, error) {
	var p LLMProfile
	err := scanProfile(d.QueryRow(`SELECT `+profileColsKey+` FROM llm_profiles WHERE is_default LIMIT 1`), &p.APIKey, &p)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// ProfileByID returns one profile with its api key by id, or nil if not found.
// Used to run a task on a specific (non-default) LLM profile.
func (d *DB) ProfileByID(id int64) (*LLMProfile, error) {
	var p LLMProfile
	err := scanProfile(d.QueryRow(`SELECT `+profileColsKey+` FROM llm_profiles WHERE id=$1`, id), &p.APIKey, &p)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// PoolProfiles returns the failover chain in run order, api keys included: the
// active profile first, then every other usable profile that isn't excluded, by
// priority DESC (id ASC to stay stable). Profiles without an effective api key
// can't serve a request, so they never enter the chain ("effective" = the bound
// credential when there is one, else the inline column). The ordering IS the
// policy — callers walk the slice front to back.
func (d *DB) PoolProfiles() ([]*LLMProfile, error) {
	rows, err := d.Query(`SELECT ` + profileColsKey + ` FROM llm_profiles
WHERE ` + profileKeyExpr + ` <> '' AND (is_default OR NOT pool_exclude)
ORDER BY is_default DESC, priority DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LLMProfile
	for rows.Next() {
		var p LLMProfile
		if err := scanProfile(rows, &p.APIKey, &p); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// SaveProfile inserts (id==0) or updates a profile. Empty apiKey on update keeps existing.
//
// credential_id 语义(凭据实体化,计划 46–47):
//   - nil = 不动绑定(新建=不绑定)。旧客户端不传该字段 → 行为与从前逐字一致。
//   - 0   = 显式解绑,回到「内联 Key」模式(内联列原样保留)。
//   - >0  = 绑定该凭据:key 以凭据行为准,并清空本表的内联 api_key/api_key_hint,
//     免得留下「改的是 A、用的是 B」的旧副本(请求里同时带 api_key 时忽略之)。
func (d *DB) SaveProfile(p *LLMProfile) (int64, error) {
	// 输入模态在唯一写入口归一:补 text、规范顺序、丢弃未知取值,库里永远只有
	// 一种规范形态;"" = 未声明,保持 "" 交给运行时按模型名推断。
	p.InputModalities = NormalizeModalities(p.InputModalities)
	bindID := int64(0)
	if p.CredentialID != nil {
		bindID = *p.CredentialID
	}
	if bindID > 0 {
		// 绑定的凭据必须真实存在:否则这条配置会静默变成「无 Key」,错误离原因很远。
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM credentials WHERE id=$1`, bindID).Scan(&n); err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, ErrCredentialNotFound
		}
	}
	hint := p.APIKeyHint
	if len(p.APIKey) >= 4 {
		hint = "…" + p.APIKey[len(p.APIKey)-4:]
	}
	r := p.Retry.Clamped()
	// hint 必须由明文推导，绝不能由密文推导——"…9f2a"是用户唯一认得出的部分，
	// 显示 base64 尾巴既无用又是新的（小）泄露面。
	stored, err := encryptCredential(p.APIKey)
	if err != nil {
		return 0, err
	}
	// 绑定态的内联列一律写 NULL;非绑定态按参数写。
	inlineKey, inlineHint := stored, hint
	var credentialArg any
	if bindID > 0 {
		inlineKey, inlineHint, credentialArg = "", "", bindID
	}
	if p.ID == 0 {
		var id int64
		err := d.QueryRow(`INSERT INTO llm_profiles(name,format,base_url,proxy,model,api_key,api_key_hint,rate_per_second,rate_per_minute,context_window_k,reasoning_effort,priority,pool_exclude,thinking_type,streaming,max_tokens,max_tokens_field,session_header_key,retry_connect_attempts,retry_connect_interval_ms,retry_empty_attempts,retry_empty_interval_ms,retry_stream_attempts,retry_stream_interval_ms,input_modalities,credential_id)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5,NULLIF($6,''),NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26) RETURNING id`,
			p.Name, p.Format, p.BaseURL, p.Proxy, p.Model, inlineKey, inlineHint, p.RatePerSecond, p.RatePerMinute, p.ContextWindowK, p.ReasoningEffort, p.Priority, p.PoolExclude, p.ThinkingType, p.Streaming, p.MaxTokens, p.MaxTokensField, p.SessionHeaderKey,
			r.Connect.Attempts, r.Connect.IntervalMS, r.Empty.Attempts, r.Empty.IntervalMS, r.Stream.Attempts, r.Stream.IntervalMS, p.InputModalities, credentialArg).Scan(&id)
		return id, err
	}
	// 一条语句覆盖三种绑定状态:内联列只在「提供了新 key」或「绑定凭据」时改写,
	// 未表态时原样保留(空 key 保留旧 key 的既有契约不变)。
	credMode := 0
	if p.CredentialID != nil {
		if bindID > 0 {
			credMode = 1
		} else {
			credMode = 2
		}
	}
	_, err = d.Exec(`UPDATE llm_profiles SET
name=$1,format=$2,base_url=NULLIF($3,''),proxy=NULLIF($4,''),model=$5,
api_key      = CASE WHEN $26 = 1 THEN NULL WHEN $6 <> '' THEN NULLIF($6,'') ELSE api_key END,
api_key_hint = CASE WHEN $26 = 1 THEN NULL WHEN $6 <> '' THEN NULLIF($7,'') ELSE api_key_hint END,
rate_per_second=$8,rate_per_minute=$9,context_window_k=$10,reasoning_effort=$11,priority=$12,pool_exclude=$13,thinking_type=$14,streaming=$15,max_tokens=$16,max_tokens_field=$17,session_header_key=$18,
retry_connect_attempts=$19,retry_connect_interval_ms=$20,retry_empty_attempts=$21,retry_empty_interval_ms=$22,retry_stream_attempts=$23,retry_stream_interval_ms=$24,input_modalities=$25,
credential_id = CASE WHEN $26 = 1 THEN $27 WHEN $26 = 2 THEN NULL ELSE credential_id END
WHERE id=$28`,
		p.Name, p.Format, p.BaseURL, p.Proxy, p.Model, stored, hint, p.RatePerSecond, p.RatePerMinute, p.ContextWindowK, p.ReasoningEffort, p.Priority, p.PoolExclude, p.ThinkingType, p.Streaming, p.MaxTokens, p.MaxTokensField, p.SessionHeaderKey,
		r.Connect.Attempts, r.Connect.IntervalMS, r.Empty.Attempts, r.Empty.IntervalMS, r.Stream.Attempts, r.Stream.IntervalMS, p.InputModalities,
		credMode, bindID, p.ID)
	return p.ID, err
}

var (
	ErrActiveLLMProfileDelete      = errors.New("cannot delete the active LLM profile; activate another profile first")
	ErrLLMProfileReferencesChanged = errors.New("LLM profile references changed while deleting; retry the request")
	ErrLLMProfileNotFound          = errors.New("LLM profile not found")
)

func (d *DB) DeleteProfile(id int64) error {
	return d.DeleteProfileContext(context.Background(), id)
}

// DeleteProfileContext removes a non-default profile while preserving the task
// failover cursor. Reference changes are retried because a task, agent, or
// conversation may start pointing at the profile between the initial scan and
// the profile row lock. A bound prevents a continuously changing workload from
// keeping an HTTP request alive forever.
func (d *DB) DeleteProfileContext(ctx context.Context, id int64) error {
	const (
		maxAttempts = 8
		maxDuration = 15 * time.Second
	)
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		retry, err := d.deleteProfile(ctx, id)
		if err != nil || !retry {
			return err
		}
	}
	return fmt.Errorf("%w: profile %d", ErrLLMProfileReferencesChanged, id)
}

func (d *DB) deleteProfile(ctx context.Context, id int64) (bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// Reference setters lock their task/agent/conversation row before an
	// llm_profiles row can be locked by a foreign-key check. Keep deletion in the
	// same order: the old profile -> child order deadlocked with a setter's child
	// -> profile order. Sorting also gives concurrent profile deletions a stable
	// order when their chains overlap multiple tasks.
	// 行锁片段按引擎取：PG 保留 FOR UPDATE [OF t]，SQLite 没有行锁、返回空串，
	// 而锁要防的东西（WAL 下的库级写锁互斥）由 SQLite 自己提供，见 dialect_locking.go。
	lockedTasks, err := lockProfileReferenceRows(ctx, tx, `SELECT t.id
FROM tasks t
WHERE t.llm_profile_id=$1
   OR t.active_llm_profile_id=$1
   OR EXISTS (
       SELECT 1 FROM task_llm_profiles x
       WHERE x.task_id=t.id AND x.profile_id=$1
   )
ORDER BY t.id`+sqlForUpdate("t"), id)
	if err != nil {
		return false, err
	}
	lockedAgents, err := lockProfileReferenceRows(ctx, tx, `SELECT id FROM agents
WHERE llm_profile_id=$1
ORDER BY id`+sqlForUpdate(), id)
	if err != nil {
		return false, err
	}
	lockedConversations, err := lockProfileReferenceRows(ctx, tx, `SELECT id FROM conversations
WHERE llm_profile_id=$1
ORDER BY id`+sqlForUpdate(), id)
	if err != nil {
		return false, err
	}

	var isDefault bool
	if err := tx.QueryRowContext(ctx, `SELECT is_default FROM llm_profiles WHERE id=$1`+forUpdateClause(false), id).Scan(&isDefault); err != nil {
		if err == sql.ErrNoRows {
			return false, ErrLLMProfileNotFound
		}
		return false, err
	}
	if isDefault {
		return false, ErrActiveLLMProfileDelete
	}

	type affectedTask struct {
		id        int64
		position  int
		wasActive bool
	}
	// A task may have committed a new reference after the first statement took
	// its snapshot but before this transaction acquired the profile lock. The
	// profile lock now prevents further references; retry if that committed task
	// was not part of the task-first lock set. Never acquire a new task lock while
	// holding the profile lock, because that would recreate the inversion.
	rows, err := tx.QueryContext(ctx, `SELECT ref_kind, ref_id FROM (
	SELECT CAST('task' AS TEXT) AS ref_kind, t.id AS ref_id
FROM tasks t
WHERE t.llm_profile_id=$1
   OR t.active_llm_profile_id=$1
   OR EXISTS (
       SELECT 1 FROM task_llm_profiles x
       WHERE x.task_id=t.id AND x.profile_id=$1
   )
	UNION ALL
	SELECT 'agent', a.id FROM agents a WHERE a.llm_profile_id=$1
	UNION ALL
	SELECT 'conversation', c.id FROM conversations c WHERE c.llm_profile_id=$1
) refs
ORDER BY ref_kind, ref_id`, id)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var (
			kind  string
			rowID int64
		)
		if err := rows.Scan(&kind, &rowID); err != nil {
			rows.Close()
			return false, err
		}
		locked := false
		switch kind {
		case "task":
			_, locked = lockedTasks[rowID]
		case "agent":
			_, locked = lockedAgents[rowID]
		case "conversation":
			_, locked = lockedConversations[rowID]
		}
		if !locked {
			rows.Close()
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT x.task_id, x.position, COALESCE(t.active_llm_profile_id=$1, false)
FROM task_llm_profiles x
JOIN tasks t ON t.id=x.task_id
WHERE x.profile_id=$1
ORDER BY x.task_id`, id)
	if err != nil {
		return false, err
	}
	var affected []affectedTask
	for rows.Next() {
		var task affectedTask
		if err := rows.Scan(&task.id, &task.position, &task.wasActive); err != nil {
			rows.Close()
			return false, err
		}
		affected = append(affected, task)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_profiles WHERE id=$1`, id); err != nil {
		return false, err
	}
	// Only a successor after the deleted cursor is eligible. If there is none,
	// clear the explicit chain so the task falls back to its Agent/global provider;
	// profiles before a manually selected cursor must never be revived.
	for _, task := range affected {
		if !task.wasActive {
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET llm_chain_revision=llm_chain_revision+1 WHERE id=$1`, task.id); err != nil {
				return false, err
			}
			continue
		}
		var next int64
		err := tx.QueryRowContext(ctx, `SELECT profile_id FROM task_llm_profiles
WHERE task_id=$1 AND position>$2 AND status='ready'
ORDER BY position
LIMIT 1`, task.id, task.position).Scan(&next)
		if err != nil && err != sql.ErrNoRows {
			return false, err
		}
		if err == sql.ErrNoRows {
			if _, err := tx.ExecContext(ctx, `DELETE FROM task_llm_profiles WHERE task_id=$1`, task.id); err != nil {
				return false, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tasks
SET active_llm_profile_id=NULL, llm_profile_id=NULL, llm_chain_revision=llm_chain_revision+1
WHERE id=$1`, task.id); err != nil {
				return false, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks
SET active_llm_profile_id=$2, llm_profile_id=$2, llm_chain_revision=llm_chain_revision+1
WHERE id=$1`, task.id, next); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}

func lockProfileReferenceRows(ctx context.Context, tx *sql.Tx, query string, profileID int64) (map[int64]struct{}, error) {
	rows, err := tx.QueryContext(ctx, query, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	locked := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		locked[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return locked, nil
}

// SetActiveProfile makes one profile the global default (single-default invariant).
func (d *DB) SetActiveProfile(id int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE llm_profiles SET is_default=false WHERE is_default`); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE llm_profiles SET is_default=true WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrLLMProfileNotFound
	}
	return tx.Commit()
}

// ---------- Agents / prompts ----------

type Agent struct {
	ID               int64  `json:"id"`
	Key              string `json:"key"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Role             string `json:"role"`
	Builtin          bool   `json:"builtin"`
	Enabled          bool   `json:"enabled"`
	LLMProfileID     *int64 `json:"llm_profile_id"`    // 绑定的 LLM 配置;nil=跟随任务/会话 pin,再回退全局激活
	MaxTurns         int    `json:"max_turns"`         // 单次运行最大轮次;0=不限制
	RunSecs          int    `json:"run_seconds"`       // worker 单次运行墙钟上限(秒);0=不限制
	WebSearch        bool   `json:"web_search"`        // 是否启用网络搜索(受系统全局开关门控)
	InteractiveShell bool   `json:"interactive_shell"` // 是否启用交互式 shell(持久 PTY 会话工具族)
	WrapupPrompt     string `json:"wrapup_prompt"`     // 收尾提示词(超时/步数耗尽时的 settlement 提示);空=用代码内置默认
	WrapupMaxTurns   int    `json:"wrapup_max_turns"`  // 收尾阶段自身的轮数预算;0=用代码内置默认(按 agent)
	// 任务级超时收尾词(与 per-run 两套;仅 worker/planner 用);空/0=用代码内置默认。
	TaskTimeoutWrapupPrompt   string `json:"task_timeout_wrapup_prompt"`
	TaskTimeoutWrapupMaxTurns int    `json:"task_timeout_wrapup_max_turns"`
	// P3 触发后处理策略(仅自定义 agent 有意义):
	// TriggerRunMode  serial|parallel — 串行排队 / 每次触发各自并发一个会话
	// TriggerMergeMode by_task|all|none — 仅 serial 用:同任务合并 / 全部合并 / 不合并
	// TriggerMaxParallel — 仅 parallel 用的每 agent 并发上限;0=不限
	TriggerRunMode     string `json:"trigger_run_mode"`
	TriggerMergeMode   string `json:"trigger_merge_mode"`
	TriggerMaxParallel int    `json:"trigger_max_parallel"`
}

const agentCols = `id,key,name,COALESCE(description,''),role,builtin,enabled,COALESCE(max_turns,0),COALESCE(run_seconds,600),COALESCE(web_search,false),COALESCE(interactive_shell,false),COALESCE(wrapup_prompt,''),COALESCE(wrapup_max_turns,0),COALESCE(task_timeout_wrapup_prompt,''),COALESCE(task_timeout_wrapup_max_turns,0),COALESCE(trigger_run_mode,'serial'),COALESCE(trigger_merge_mode,'all'),COALESCE(trigger_max_parallel,5),llm_profile_id`

func scanAgent(sc interface{ Scan(...any) error }) (*Agent, error) {
	var a Agent
	var prof sql.NullInt64 // llm_profile_id 可空:未绑定时为 NULL
	err := sc.Scan(&a.ID, &a.Key, &a.Name, &a.Description, &a.Role, &a.Builtin, &a.Enabled, &a.MaxTurns, &a.RunSecs, &a.WebSearch, &a.InteractiveShell, &a.WrapupPrompt, &a.WrapupMaxTurns, &a.TaskTimeoutWrapupPrompt, &a.TaskTimeoutWrapupMaxTurns, &a.TriggerRunMode, &a.TriggerMergeMode, &a.TriggerMaxParallel, &prof)
	if err == nil && prof.Valid {
		v := prof.Int64
		a.LLMProfileID = &v
	}
	return &a, err
}

func (d *DB) ListAgents() ([]*Agent, error) {
	rows, err := d.Query(`SELECT ` + agentCols + ` FROM agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) GetAgentByKey(key string) (*Agent, error) {
	a, err := scanAgent(d.QueryRow(`SELECT `+agentCols+` FROM agents WHERE key=$1`, key))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// AgentBindingCounts returns per-agent binding counts in a few grouped queries
// (NO N+1): visible MCP servers and skills keyed by agent id, and bound tools
// keyed by agent key (tools.agents is a JSONB array of agent keys). Missing keys
// mean zero. Used to show "MCP N · Skill N · 工具 N" on the agent cards.
func (d *DB) AgentBindingCounts() (mcp map[int64]int, skill map[int64]int, tools map[string]int, err error) {
	mcp, skill, tools = map[int64]int{}, map[int64]int{}, map[string]int{}
	byID := func(q string, into map[int64]int) error {
		rows, e := d.Query(q)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var n int
			if e := rows.Scan(&id, &n); e != nil {
				return e
			}
			into[id] = n
		}
		return rows.Err()
	}
	if err = byID(`SELECT agent_id, count(DISTINCT resource_id) FROM agent_visibility WHERE resource_kind='mcp' AND enabled GROUP BY agent_id`, mcp); err != nil {
		return
	}
	if err = byID(`SELECT agent_id, count(*) FROM agent_skill_visibility WHERE enabled GROUP BY agent_id`, skill); err != nil {
		return
	}
	rows, e := d.Query(`SELECT elem, count(*) FROM tools, jsonb_array_elements_text(agents) AS elem GROUP BY elem`)
	if e != nil {
		err = e
		return
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if e := rows.Scan(&k, &n); e != nil {
			err = e
			return
		}
		tools[k] = n
	}
	err = rows.Err()
	return
}

// CreateAgent inserts a custom (builtin=false) conversational agent with role
// 'assistant'. Returns the new row. Callers validate key/name upstream; the DB
// enforces key charset + uniqueness and the role check constraint.
func (d *DB) CreateAgent(key, name, description string) (*Agent, error) {
	a, err := scanAgent(d.QueryRow(`
INSERT INTO agents(key, name, description, role, builtin, enabled)
VALUES ($1, $2, NULLIF($3,''), 'assistant', false, true)
RETURNING `+agentCols, key, name, description))
	if err != nil {
		return nil, err
	}
	return a, nil
}

// UpdateAgentMeta updates a custom agent's display name + description. Built-in
// agents are left untouched (guarded by the caller / the builtin flag).
func (d *DB) UpdateAgentMeta(key, name, description string) error {
	_, err := d.Exec(`UPDATE agents SET name=$2, description=NULLIF($3,'') WHERE key=$1 AND builtin=false`, key, name, description)
	return err
}

// DeleteAgent removes a custom agent. Built-in agents are protected by the
// builtin=false guard. agent_prompts / agent_prompt_vars / visibility rows cascade
// via FK; tools.agents bindings for the key are cleaned by the caller.
func (d *DB) DeleteAgent(key string) error {
	_, err := d.Exec(`DELETE FROM agents WHERE key=$1 AND builtin=false`, key)
	return err
}

// SetAgentMaxTurns updates an agent's max_turns (0 = unlimited).
func (d *DB) SetAgentMaxTurns(key string, maxTurns int) error {
	if maxTurns < 0 {
		maxTurns = 0
	}
	_, err := d.Exec(`UPDATE agents SET max_turns=$1 WHERE key=$2`, maxTurns, key)
	return err
}

// SetAgentLLMProfile binds an agent to a specific LLM profile (id != nil), or clears
// the binding (id == nil) so the agent follows the task/conversation pin, else the
// global active profile. Precedence at runtime: agent binding → task/conv pin → active.
func (d *DB) SetAgentLLMProfile(key string, id *int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// DeleteProfile locks reference rows before the profile row. Keep the same
	// order here so a concurrent rebind cannot form a child/profile deadlock.
	// 行锁子句按引擎取：PG 保留 FOR UPDATE，SQLite 返回空串（见 dialect_locking.go）。
	var agentID int64
	if err := tx.QueryRow(`SELECT id FROM agents WHERE key=$1`+forUpdateClause(false), key).Scan(&agentID); err != nil {
		if err == sql.ErrNoRows {
			// Preserve the previous UPDATE semantics: an unknown key is a no-op.
			return tx.Commit()
		}
		return err
	}
	if err := lockLLMProfileForReference(tx, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE agents SET llm_profile_id=$1 WHERE id=$2`, id, agentID); err != nil {
		return err
	}
	// 链首不得同时出现在备用链里(#139/#144):绑定新主模型时把该 id 从尾部剔除,
	// 让「链首 = llm_profile_id」在两条写路径(本函数 / ReplaceAgentLLMFallbacks)上
	// 都严格成立,而不是等运行时再跳过。
	if id != nil {
		if _, err := tx.Exec(`DELETE FROM agent_llm_fallbacks WHERE agent_id=$1 AND profile_id=$2`, agentID, *id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// lockLLMProfileForReference makes the profile side of the shared lock-order
// protocol explicit. Callers must already own the referencing child row.
func lockLLMProfileForReference(tx *sql.Tx, profileID *int64) error {
	if profileID == nil {
		return nil
	}
	var lockedID int64
	if err := tx.QueryRow(`SELECT id FROM llm_profiles WHERE id=$1`+forKeyShareClause(), *profileID).Scan(&lockedID); err != nil {
		if err == sql.ErrNoRows {
			return ErrLLMProfileNotFound
		}
		return err
	}
	return nil
}

// AgentLLMFallbacks returns the agent's saved fallback tail in position order
// (链首不在这里——链首就是 agents.llm_profile_id). Unknown key → empty list.
func (d *DB) AgentLLMFallbacks(agentKey string) ([]int64, error) {
	rows, err := d.Query(`SELECT f.profile_id
FROM agent_llm_fallbacks f
JOIN agents a ON a.id = f.agent_id
WHERE a.key=$1
ORDER BY f.position`, agentKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ReplaceAgentLLMFallbacks atomically replaces an agent's fallback tail。链首
// (agents.llm_profile_id)不入表:调用方传进来的链首 id 会被静默跳过,重复 id 也
// 只保留第一次出现的位置。空列表 = 清空备用链(角色回落到「绑定独占/跟随」的原有行为)。
//
// 行锁顺序与 SetAgentLLMProfile / DeleteProfile 一致(先 agent 行,再 profile 引用),
// 避免并发重新绑定形成 child↔profile 死锁;未知 key 沿用 UPDATE 的 no-op 语义。
func (d *DB) ReplaceAgentLLMFallbacks(agentKey string, profileIDs []int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		agentID int64
		headID  sql.NullInt64
	)
	if err := tx.QueryRow(`SELECT id, llm_profile_id FROM agents WHERE key=$1`+forUpdateClause(false), agentKey).
		Scan(&agentID, &headID); err != nil {
		if err == sql.ErrNoRows {
			return tx.Commit() // 未知 key = no-op，与 SetAgentLLMProfile 一致
		}
		return err
	}
	if _, err := tx.Exec(`DELETE FROM agent_llm_fallbacks WHERE agent_id=$1`, agentID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	position := 0
	for _, id := range profileIDs {
		if id <= 0 || seen[id] || (headID.Valid && id == headID.Int64) {
			continue
		}
		seen[id] = true
		if err := lockLLMProfileForReference(tx, &id); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO agent_llm_fallbacks(agent_id, profile_id, position) VALUES($1,$2,$3)`,
			agentID, id, position); err != nil {
			return err
		}
		position++
	}
	return tx.Commit()
}

// SetAgentWebSearch toggles whether an agent uses network search (still gated by
// the global web-search master switch + backend/key config).
func (d *DB) SetAgentWebSearch(key string, on bool) error {
	_, err := d.Exec(`UPDATE agents SET web_search=$1 WHERE key=$2`, on, key)
	return err
}

// SetAgentInteractiveShell toggles whether an agent gets the interactive shell
// (持久 PTY 会话) tool family + Bash 提示词联动(见 docs/交互式shell设计.md §14.2).
func (d *DB) SetAgentInteractiveShell(key string, on bool) error {
	_, err := d.Exec(`UPDATE agents SET interactive_shell=$1 WHERE key=$2`, on, key)
	return err
}

// SetAgentWrapupPrompt stores an agent's wrap-up (settlement) prompt. Empty string
// means "use the code built-in default" — resolved at runtime by resolveWrapup.
func (d *DB) SetAgentWrapupPrompt(key, prompt string) error {
	_, err := d.Exec(`UPDATE agents SET wrapup_prompt=$1 WHERE key=$2`, prompt, key)
	return err
}

// SetAgentWrapupMaxTurns stores the wrap-up phase's own turn budget. 0 means "use
// the code built-in default" — resolved at runtime by resolveWrapupTurns.
func (d *DB) SetAgentWrapupMaxTurns(key string, n int) error {
	_, err := d.Exec(`UPDATE agents SET wrapup_max_turns=$1 WHERE key=$2`, n, key)
	return err
}

// SetAgentTaskTimeoutWrapup stores an agent's task-timeout wrap-up prompt (empty =
// use code built-in default; only worker/planner have one) and its turn budget
// (0 = default). Resolved at runtime by resolveTaskTimeoutWrapup / …Turns.
func (d *DB) SetAgentTaskTimeoutWrapup(key, prompt string, maxTurns int) error {
	_, err := d.Exec(`UPDATE agents SET task_timeout_wrapup_prompt=$1, task_timeout_wrapup_max_turns=$2 WHERE key=$3`, prompt, maxTurns, key)
	return err
}

// SetAgentRunSeconds updates an agent's run_seconds wall-clock budget (0 = unlimited).
func (d *DB) SetAgentRunSeconds(key string, runSecs int) error {
	if runSecs < 0 {
		runSecs = 0
	}
	_, err := d.Exec(`UPDATE agents SET run_seconds=$1 WHERE key=$2`, runSecs, key)
	return err
}

// SetAgentTriggerBehavior stores an agent's P3 trigger post-processing策略:
// runMode(serial|parallel) / mergeMode(by_task|all|none) / maxParallel(parallel 用,0=不限)。
// 枚举做白名单校验,非法值回落默认,避免脏数据把调度 pump 带偏。
func (d *DB) SetAgentTriggerBehavior(key, runMode, mergeMode string, maxParallel int) error {
	switch runMode {
	case "serial", "parallel":
	default:
		runMode = "serial"
	}
	switch mergeMode {
	case "by_task", "all", "none":
	default:
		mergeMode = "by_task"
	}
	if maxParallel < 0 {
		maxParallel = 0
	}
	_, err := d.Exec(`UPDATE agents SET trigger_run_mode=$1, trigger_merge_mode=$2, trigger_max_parallel=$3 WHERE key=$4`,
		runMode, mergeMode, maxParallel, key)
	return err
}

type PromptVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Example     string `json:"example"`
	Source      string `json:"source"`
}

func (d *DB) PromptVars(agentID int64) ([]PromptVar, error) {
	rows, err := d.Query(`SELECT var_name,COALESCE(description,''),COALESCE(example,''),source FROM agent_prompt_vars WHERE agent_id=$1 ORDER BY var_name`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PromptVar{}
	for rows.Next() {
		var v PromptVar
		if err := rows.Scan(&v.Name, &v.Description, &v.Example, &v.Source); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CurrentPrompt returns the agent's active template text ("" if none set yet).
func (d *DB) CurrentPrompt(agentID int64) (string, error) {
	var tmpl sql.NullString
	err := d.QueryRow(`SELECT p.template_text FROM agents a JOIN agent_prompts p ON p.id=a.current_prompt_id WHERE a.id=$1`, agentID).Scan(&tmpl)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return tmpl.String, err
}

// SeedPromptIfEmpty writes the code-default template as the agent's first prompt
// version ONLY when it has none yet (current_prompt_id IS NULL). Mirrors
// SeedTool's first-insert-only philosophy: a user's edited prompt is never
// clobbered on restart. Idempotent — a no-op once any version exists.
func (d *DB) SeedPromptIfEmpty(agentID int64, tmpl string) error {
	var cur sql.NullInt64
	if err := d.QueryRow(`SELECT current_prompt_id FROM agents WHERE id=$1`, agentID).Scan(&cur); err != nil {
		return err
	}
	if cur.Valid {
		return nil // already seeded or user-edited → leave it
	}
	_, err := d.SavePrompt(agentID, tmpl, "内置默认", "system")
	return err
}

// ResetPromptToDefault appends the code-default template as a new version and
// points current at it — the explicit "恢复为内置默认" action.
func (d *DB) ResetPromptToDefault(agentID int64, tmpl string) (int, error) {
	return d.SavePrompt(agentID, tmpl, "恢复为内置默认", "system")
}

// SavePrompt appends a new version and points current_prompt_id at it.
func (d *DB) SavePrompt(agentID int64, template, note, by string) (int, error) {
	tx, err := d.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var ver int
	if err := tx.QueryRow(`SELECT COALESCE(max(version),0)+1 FROM agent_prompts WHERE agent_id=$1`, agentID).Scan(&ver); err != nil {
		return 0, err
	}
	var pid int64
	if err := tx.QueryRow(`INSERT INTO agent_prompts(agent_id,version,template_text,note,updated_by) VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,'')) RETURNING id`,
		agentID, ver, template, note, by).Scan(&pid); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE agents SET current_prompt_id=$1 WHERE id=$2`, pid, agentID); err != nil {
		return 0, err
	}
	return ver, tx.Commit()
}

type PromptVersion struct {
	Version   int       `json:"version"`
	Template  string    `json:"template_text"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"ts"`
}

func (d *DB) ListPromptVersions(agentID int64) ([]PromptVersion, error) {
	rows, err := d.Query(`SELECT version,template_text,COALESCE(note,''),created_at FROM agent_prompts WHERE agent_id=$1 ORDER BY version DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PromptVersion{}
	for rows.Next() {
		var v PromptVersion
		if err := rows.Scan(&v.Version, &v.Template, &v.Note, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------- MCP servers ----------

type MCPServer struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Transport string          `json:"transport"`
	Command   string          `json:"command,omitempty"`
	Args      json.RawMessage `json:"args"`
	Env       json.RawMessage `json:"env"`
	URL       string          `json:"url,omitempty"`
	Enabled   bool            `json:"enabled"`
	Insecure  bool            `json:"insecure"`        // http: skip TLS cert verification (self-signed servers, issue #108)
	Tools     []string        `json:"tools,omitempty"` // cached tool names (mcp_tools_cache)
}

func (d *DB) ListMCP() ([]*MCPServer, error) {
	rows, err := d.Query(`SELECT id,name,transport,COALESCE(command,''),args,env,COALESCE(url,''),enabled,insecure FROM mcp_servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []*MCPServer
	for rows.Next() {
		var m MCPServer
		var args, env []byte
		if err := rows.Scan(&m.ID, &m.Name, &m.Transport, &m.Command, &args, &env, &m.URL, &m.Enabled, &m.Insecure); err != nil {
			rows.Close()
			return nil, err
		}
		m.Args, m.Env = json.RawMessage(args), json.RawMessage(env)
		out = append(out, &m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close() // free the connection before the per-server tool-cache queries below
	// Attach each server's cached tool names (best-effort; empty until discovered).
	for _, m := range out {
		m.Tools, _ = d.MCPToolNames(m.ID)
	}
	return out, nil
}

// MCPToolNames returns the cached tool names for a server (empty until discovered).
func (d *DB) MCPToolNames(serverID int64) ([]string, error) {
	rows, err := d.Query(`SELECT tool_name FROM mcp_tools_cache WHERE server_id=$1 ORDER BY tool_name`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MCPTool is one cached tool of an MCP server (name + description).
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// MCPToolsDetailed returns the cached tools (name + description) for a server.
func (d *DB) MCPToolsDetailed(serverID int64) ([]MCPTool, error) {
	rows, err := d.Query(`SELECT tool_name, COALESCE(description,'') FROM mcp_tools_cache WHERE server_id=$1 ORDER BY tool_name`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPTool
	for rows.Next() {
		var t MCPTool
		if err := rows.Scan(&t.Name, &t.Description); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveMCPTools replaces the cached tool list for a server (called after discovery).
func (d *DB) SaveMCPTools(serverID int64, tools []MCPTool) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM mcp_tools_cache WHERE server_id=$1`, serverID); err != nil {
		return err
	}
	for _, t := range tools {
		if _, err := tx.Exec(`INSERT INTO mcp_tools_cache(server_id, tool_name, description) VALUES ($1,$2,$3)
ON CONFLICT (server_id, tool_name) DO UPDATE SET description=EXCLUDED.description`, serverID, t.Name, t.Description); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) SaveMCP(m *MCPServer) (int64, error) {
	args, env := string(m.Args), string(m.Env)
	if args == "" {
		args = "[]"
	}
	if env == "" {
		env = "{}"
	}
	// 写入口唯一漏斗：敏感 env/header 值落地即密文（计划 #49，见 db/mcp_env.go）。
	encEnv, err := encryptMCPEnvSecrets(json.RawMessage(env))
	if err != nil {
		return 0, err
	}
	env = string(encEnv)
	if m.ID == 0 {
		var id int64
		err := d.QueryRow(`INSERT INTO mcp_servers(name,transport,command,args,env,url,enabled,insecure) VALUES ($1,$2,NULLIF($3,''),$4,$5,NULLIF($6,''),$7,$8) RETURNING id`,
			m.Name, m.Transport, m.Command, args, env, m.URL, m.Enabled, m.Insecure).Scan(&id)
		return id, err
	}
	_, err = d.Exec(`UPDATE mcp_servers SET name=$1,transport=$2,command=NULLIF($3,''),args=$4,env=$5,url=NULLIF($6,''),enabled=$7,insecure=$8 WHERE id=$9`,
		m.Name, m.Transport, m.Command, args, env, m.URL, m.Enabled, m.Insecure, m.ID)
	return m.ID, err
}

func (d *DB) DeleteMCP(id int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM agent_visibility WHERE resource_kind='mcp' AND resource_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM mcp_servers WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------- Skill visibility (agent × skill_name) ----------

// AgentSkillNames returns the skill directory names visible to an agent.
func (d *DB) AgentSkillNames(agentID int64) ([]string, error) {
	rows, err := d.Query(`SELECT skill_name FROM agent_skill_visibility WHERE agent_id=$1 AND enabled ORDER BY skill_name`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// SkillAgents returns the agent IDs that can see a skill.
func (d *DB) SkillAgents(skillName string) ([]int64, error) {
	rows, err := d.Query(`SELECT agent_id FROM agent_skill_visibility WHERE skill_name=$1 AND enabled`, skillName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetAgentSkillVisibility replaces all skill visibility for an agent.
func (d *DB) SetAgentSkillVisibility(agentID int64, names []string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM agent_skill_visibility WHERE agent_id=$1`, agentID); err != nil {
		return err
	}
	for _, name := range names {
		if _, err := tx.Exec(`INSERT INTO agent_skill_visibility(agent_id,skill_name,enabled) VALUES ($1,$2,true)
ON CONFLICT (agent_id,skill_name) DO UPDATE SET enabled=true`, agentID, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ToggleSkillVisibility sets one (agent, skill_name) visibility on/off.
func (d *DB) ToggleSkillVisibility(agentID int64, skillName string, on bool) error {
	if on {
		_, err := d.Exec(`INSERT INTO agent_skill_visibility(agent_id,skill_name,enabled) VALUES ($1,$2,true)
ON CONFLICT (agent_id,skill_name) DO UPDATE SET enabled=true`, agentID, skillName)
		return err
	}
	_, err := d.Exec(`DELETE FROM agent_skill_visibility WHERE agent_id=$1 AND skill_name=$2`, agentID, skillName)
	return err
}

// RenameSkillRefs 把技能旧名的所有引用迁移到新名：agent_skill_visibility /
// skill_proposals / skill_usage 三张表都按 skill 名关联，改名不迁移等于静默断链
// （可见性丢失、提议与调用统计归到不存在的技能下）。单事务，失败整体回滚。
func (d *DB) RenameSkillRefs(oldName, newName string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`UPDATE agent_skill_visibility SET skill_name=$1 WHERE skill_name=$2`,
		`UPDATE skill_proposals SET skill=$1 WHERE skill=$2`,
		`UPDATE skill_usage SET skill=$1 WHERE skill=$2`,
	} {
		if _, err := tx.Exec(q, newName, oldName); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RenameMCPRefs 迁移 MCP 服务器改名带来的名字引用：mcp_tools_cache 的工具全名
// （mcp__<服务名>__<工具名>）与 agent_triggers.tool_names（JSON 文本）都嵌着旧
// 服务名。返回触发器与工具缓存各自迁移的行数。
func (d *DB) RenameMCPRefs(serverID int64, oldName, newName string) (triggers, tools int, err error) {
	oldPrefix := "mcp__" + oldName + "__"
	newPrefix := "mcp__" + newName + "__"
	tx, err := d.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE mcp_tools_cache SET tool_name=REPLACE(tool_name,$1,$2) WHERE server_id=$3 AND tool_name LIKE $1 || '%'`,
		oldPrefix, newPrefix, serverID)
	if err != nil {
		return 0, 0, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		tools = int(n)
	}
	res, err = tx.Exec(`UPDATE agent_triggers SET tool_names=REPLACE(tool_names,$1,$2) WHERE tool_names LIKE '%' || $1 || '%'`,
		oldPrefix, newPrefix)
	if err != nil {
		return 0, 0, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		triggers = int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return triggers, tools, nil
}

// DeleteSkillVisibility removes all visibility rows for a skill (called on skill delete).
func (d *DB) DeleteSkillVisibility(skillName string) error {
	_, err := d.Exec(`DELETE FROM agent_skill_visibility WHERE skill_name=$1`, skillName)
	return err
}

// ---------- Visibility (agent × mcp) ----------

// AgentVisible returns the resource ids of a kind visible to an agent.
func (d *DB) AgentVisible(agentID int64, kind string) ([]int64, error) {
	rows, err := d.Query(`SELECT resource_id FROM agent_visibility WHERE agent_id=$1 AND resource_kind=$2 AND enabled`, agentID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ResourceAgents returns the agent ids that can see a resource.
func (d *DB) ResourceAgents(kind string, resourceID int64) ([]int64, error) {
	rows, err := d.Query(`SELECT agent_id FROM agent_visibility WHERE resource_kind=$1 AND resource_id=$2 AND enabled`, kind, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetAgentVisibilityKind replaces the full set of visible resources of a kind for
// an agent (agent-side bulk write). Bidirectional with the resource-side view —
// both read/write the same agent_visibility rows.
func (d *DB) SetAgentVisibilityKind(agentID int64, kind string, resourceIDs []int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM agent_visibility WHERE agent_id=$1 AND resource_kind=$2`, agentID, kind); err != nil {
		return err
	}
	for _, rid := range resourceIDs {
		if _, err := tx.Exec(`INSERT INTO agent_visibility(agent_id,resource_kind,resource_id,enabled) VALUES ($1,$2,$3,true)
ON CONFLICT (agent_id,resource_kind,resource_id,mcp_tool_name) DO UPDATE SET enabled=true`, agentID, kind, rid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ToggleVisibility sets one (agent, kind, resource) visibility on/off (idempotent).
func (d *DB) ToggleVisibility(agentID int64, kind string, resourceID int64, on bool) error {
	if on {
		_, err := d.Exec(`INSERT INTO agent_visibility(agent_id,resource_kind,resource_id,enabled) VALUES ($1,$2,$3,true)
ON CONFLICT (agent_id,resource_kind,resource_id,mcp_tool_name) DO UPDATE SET enabled=true`, agentID, kind, resourceID)
		return err
	}
	_, err := d.Exec(`DELETE FROM agent_visibility WHERE agent_id=$1 AND resource_kind=$2 AND resource_id=$3`, agentID, kind, resourceID)
	return err
}

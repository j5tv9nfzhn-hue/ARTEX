package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const retentionSetting = "data_retention_policy_v1"
const retentionPlanTTL = 15 * time.Minute

var (
	ErrRetentionDisabled = errors.New("数据保留策略未启用人工执行")
	ErrRetentionStale    = errors.New("清理预览已过期、已使用、策略已变化或凭据无效；请重新预览")
)

// Enabled arms MANUAL execution only. No timer/worker calls the cleanup API.
// Zero days means keep indefinitely. Every selected row is rechecked at execute.
type RetentionPolicy struct {
	Enabled          bool `json:"enabled"`
	UsageDays        int  `json:"usage_days"`
	LogDays          int  `json:"log_days"`
	NotificationDays int  `json:"notification_days"`
	MaxRowsPerTable  int  `json:"max_rows_per_table"`
}

func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{UsageDays: 90, LogDays: 30, NotificationDays: 90, MaxRowsPerTable: 100}
}

func (p RetentionPolicy) Validate() error {
	for _, v := range []struct {
		name      string
		days, min int
	}{{"usage_days", p.UsageDays, 30}, {"log_days", p.LogDays, 7}, {"notification_days", p.NotificationDays, 30}} {
		if v.days != 0 && (v.days < v.min || v.days > 3650) {
			return fmt.Errorf("%s 必须为 0（永久保留）或 %d–3650 天", v.name, v.min)
		}
	}
	if p.MaxRowsPerTable < 1 || p.MaxRowsPerTable > 500 {
		return errors.New("max_rows_per_table 必须为 1–500")
	}
	return nil
}

type retentionQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readRetentionPolicy(ctx context.Context, q retentionQueryer) (RetentionPolicy, error) {
	p := DefaultRetentionPolicy()
	var raw string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", retentionSetting).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return p, fmt.Errorf("读取保留策略：%w", err)
	}
	return p, p.Validate()
}
func (d *DB) RetentionPolicy(ctx context.Context) (RetentionPolicy, error) {
	return readRetentionPolicy(ctx, d)
}

func retentionJSON(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }
func retentionDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// The initial audit INSERT acquires SQLite's writer reservation before reading
// mutable policy/candidates. Audit and effect commit together; errors roll back.
func retentionAuditTx(ctx context.Context, tx *sql.Tx, action, plan string, detail any) error {
	raw, err := retentionJSON(detail)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO retention_audit(action,plan_hash,actor,detail) VALUES($1,$2,'authenticated_operator',$3)", action, plan, raw)
	return err
}

func (d *DB) SaveRetentionPolicy(ctx context.Context, p RetentionPolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	raw, err := retentionJSON(p)
	if err != nil {
		return err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err = retentionAuditTx(ctx, tx, "policy_saved", "", p); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2)
 ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=strftime('%Y-%m-%d %H:%M:%f+00:00','now')`, retentionSetting, raw)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE retention_plans SET state='invalidated' WHERE state='preview'"); err != nil {
		return err
	}
	return tx.Commit()
}

type retentionSelection struct {
	Table  string  `json:"table"`
	Cutoff string  `json:"cutoff"`
	IDs    []int64 `json:"ids"`
}
type RetentionCount struct {
	Table    string `json:"table"`
	Selected int    `json:"selected"`
	More     bool   `json:"more_candidates"`
	Deleted  int64  `json:"deleted"`
	Skipped  int64  `json:"skipped"`
}
type RetentionPreview struct {
	Token            string           `json:"confirmation_token"`
	ExpiresAt        time.Time        `json:"expires_at"`
	Policy           RetentionPolicy  `json:"policy"`
	Counts           []RetentionCount `json:"counts"`
	AutomaticCleanup bool             `json:"automatic_cleanup"`
}
type RetentionExecution struct {
	Counts    []RetentionCount `json:"counts"`
	Committed bool             `json:"committed"`
}

func retentionDays(p RetentionPolicy, table string) (int, error) {
	switch table {
	case "llm_usage":
		return p.UsageDays, nil
	case "server_logs":
		return p.LogDays, nil
	case "notification_deliveries", "notification_events":
		return p.NotificationDays, nil
	default:
		return 0, errors.New("未知清理表，拒绝执行")
	}
}
func retentionIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// Actual inbound FKs are protected even when their declared delete rule cascades
// or sets NULL. Unknown/composite target columns conservatively protect the whole
// target table. Metadata-derived identifiers are quoted, never supplied by HTTP.
func retentionReferenceGuard(ctx context.Context, tx *sql.Tx, table string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.name,fk."from",COALESCE(fk."to",'')
 FROM sqlite_schema AS m JOIN pragma_foreign_key_list(m.name) AS fk
 WHERE m.type='table' AND fk."table"=$1`, table)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var clauses []string
	for rows.Next() {
		var child, from, to string
		if err = rows.Scan(&child, &from, &to); err != nil {
			return "", err
		}
		if to != "id" && to != "" {
			return " AND 0=1", nil
		}
		clauses = append(clauses, " AND NOT EXISTS (SELECT 1 FROM "+retentionIdent(child)+" AS retention_child WHERE retention_child."+retentionIdent(from)+"=t.id)")
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return strings.Join(clauses, ""), nil
}

// Notification snapshots are logical references in addition to SQL FKs.
// Malformed/unknown identities are retained. Existing tasks, findings, assets,
// approvals and conversations protect the event and its delivery history.
const retentionOrphanEvent = `e.fanned_out=1
 AND typeof(e.finding_id)='integer' AND e.finding_id>=0
 AND NOT EXISTS (SELECT 1 FROM findings f WHERE f.id=e.finding_id)
 AND CASE WHEN json_valid(e.snapshot) THEN (
  json_type(e.snapshot,'$.task_id')='integer'
  AND NOT EXISTS (SELECT 1 FROM tasks x WHERE x.id=json_extract(e.snapshot,'$.task_id'))
  AND COALESCE(json_type(e.snapshot,'$.asset_ids'),'array') IN ('array','null')
  AND NOT EXISTS (SELECT 1 FROM json_each(e.snapshot,'$.asset_ids') a WHERE json_type(e.snapshot,'$.asset_ids')='array' AND (a.type<>'integer' OR a.value<1))
  AND NOT EXISTS (SELECT 1 FROM json_each(e.snapshot,'$.asset_ids') a JOIN assets x ON x.id=CAST(a.value AS INTEGER))
  AND COALESCE(json_type(e.snapshot,'$.approval_id'),'integer')='integer'
  AND NOT EXISTS (SELECT 1 FROM intercept_pending x WHERE x.id=json_extract(e.snapshot,'$.approval_id'))
  AND COALESCE(json_type(e.snapshot,'$.conversation_id'),'integer')='integer'
  AND NOT EXISTS (SELECT 1 FROM conversations x WHERE x.id=json_extract(e.snapshot,'$.conversation_id'))
 ) ELSE 0 END`

func retentionPredicate(table string) (string, error) {
	switch table {
	case "llm_usage":
		// Never reduce task token budgets, historical task totals or exploration data.
		return "julianday(t.ts)<julianday($1) AND COALESCE(t.task_id,'')='' AND COALESCE(t.exploration_id,0)=0", nil
	case "server_logs":
		return `julianday(t.created_at)<julianday($1) AND lower(t.level) IN ('debug','info')
   AND lower(t.tag) NOT LIKE '%auth%' AND lower(t.tag) NOT LIKE '%audit%'
   AND lower(t.tag) NOT LIKE '%security%' AND lower(t.tag) NOT LIKE '%intercept%'
   AND lower(t.tag) NOT LIKE '%approval%' AND lower(t.tag) NOT LIKE '%retention%'`, nil
	case "notification_deliveries":
		return `julianday(t.created_at)<julianday($1) AND t.state IN ('sent','skipped')
   AND julianday(t.sent_at)<julianday($1)
   AND NOT EXISTS (SELECT 1 FROM notification_deliveries sibling WHERE sibling.event_id=t.event_id AND sibling.state NOT IN ('sent','skipped'))
   AND (t.batch_id IS NULL OR NOT EXISTS (SELECT 1 FROM notification_deliveries sibling WHERE sibling.batch_id=t.batch_id AND sibling.state NOT IN ('sent','skipped')))
   AND EXISTS (SELECT 1 FROM notification_events e WHERE e.id=t.event_id AND ` + retentionOrphanEvent + `)`, nil
	case "notification_events":
		return `julianday(t.created_at)<julianday($1)
   AND EXISTS (SELECT 1 FROM notification_events e WHERE e.id=t.id AND ` + retentionOrphanEvent + `)
   AND NOT EXISTS (SELECT 1 FROM notification_deliveries d WHERE d.event_id=t.id)`, nil
	}
	return "", errors.New("未知清理表，拒绝执行")
}

func (d *DB) CreateRetentionPreview(ctx context.Context) (*RetentionPreview, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(nonce[:])
	planHash := retentionDigest(token)
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err = retentionAuditTx(ctx, tx, "preview_started", planHash, struct {
		Version int `json:"version"`
	}{1}); err != nil {
		return nil, err
	}
	p, err := readRetentionPolicy(ctx, tx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	result := &RetentionPreview{Token: token, ExpiresAt: now.Add(retentionPlanTTL), Policy: p, Counts: []RetentionCount{}}
	selections := []retentionSelection{}
	for _, table := range []string{"llm_usage", "server_logs", "notification_deliveries", "notification_events"} {
		days, err := retentionDays(p, table)
		if err != nil {
			return nil, err
		}
		count := RetentionCount{Table: table}
		if days == 0 {
			result.Counts = append(result.Counts, count)
			continue
		}
		cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
		pred, err := retentionPredicate(table)
		if err != nil {
			return nil, err
		}
		refs, err := retentionReferenceGuard(ctx, tx, table)
		if err != nil {
			return nil, err
		}
		rows, err := tx.QueryContext(ctx, "SELECT t.id FROM "+retentionIdent(table)+" AS t WHERE "+pred+refs+" ORDER BY t.id LIMIT $2", cutoff, p.MaxRowsPerTable+1)
		if err != nil {
			return nil, err
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(ids) > p.MaxRowsPerTable {
			count.More = true
			ids = ids[:p.MaxRowsPerTable]
		}
		count.Selected = len(ids)
		result.Counts = append(result.Counts, count)
		selections = append(selections, retentionSelection{Table: table, Cutoff: cutoff, IDs: ids})
	}
	policyJSON, err := retentionJSON(p)
	if err != nil {
		return nil, err
	}
	selectionJSON, err := retentionJSON(selections)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO retention_plans(token_hash,policy_json,selections_json,state,expires_at)
 VALUES($1,$2,$3,'preview',$4)`, planHash, policyJSON, selectionJSON, result.ExpiresAt.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	if err = retentionAuditTx(ctx, tx, "preview_created", planHash, result.Counts); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// ExecuteRetention only deletes IDs from a single, live preview. New rows are
// never added to its scope. A writer lock protects reference rechecks against
// concurrent writers; per-row predicates additionally retain newly protected
// rows. No cascaded deletion, filesystem deletion, VACUUM or background cleanup.
func (d *DB) ExecuteRetention(ctx context.Context, token string) (*RetentionExecution, error) {
	if len(token) != 64 {
		return nil, ErrRetentionStale
	}
	if _, err := hex.DecodeString(token); err != nil {
		return nil, ErrRetentionStale
	}
	hash := retentionDigest(token)
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err = retentionAuditTx(ctx, tx, "execute_started", hash, map[string]int{"version": 1}); err != nil {
		return nil, err
	}
	p, err := readRetentionPolicy(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, ErrRetentionDisabled
	}
	var storedPolicy, rawItems string
	err = tx.QueryRowContext(ctx, `UPDATE retention_plans SET state='executing'
 WHERE token_hash=$1 AND state='preview' AND julianday(expires_at)>julianday('now')
 RETURNING policy_json,selections_json`, hash).Scan(&storedPolicy, &rawItems)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRetentionStale
	}
	if err != nil {
		return nil, err
	}
	currentPolicy, err := retentionJSON(p)
	if err != nil {
		return nil, err
	}
	if currentPolicy != storedPolicy {
		return nil, ErrRetentionStale
	}
	var selections []retentionSelection
	if len(rawItems) > 128*1024 || json.Unmarshal([]byte(rawItems), &selections) != nil || len(selections) > 4 {
		return nil, ErrRetentionStale
	}
	seenTables := map[string]bool{}
	result := &RetentionExecution{Counts: []RetentionCount{}}
	for _, item := range selections {
		days, err := retentionDays(p, item.Table)
		if err != nil {
			return nil, err
		}
		if days == 0 || seenTables[item.Table] || len(item.IDs) > p.MaxRowsPerTable {
			return nil, ErrRetentionStale
		}
		seenTables[item.Table] = true
		cutoff, err := time.Parse(time.RFC3339, item.Cutoff)
		if err != nil || cutoff.After(time.Now().UTC().Add(-time.Duration(days)*24*time.Hour)) {
			return nil, ErrRetentionStale
		}
		pred, err := retentionPredicate(item.Table)
		if err != nil {
			return nil, err
		}
		refs, err := retentionReferenceGuard(ctx, tx, item.Table)
		if err != nil {
			return nil, err
		}
		stmt, err := tx.PrepareContext(ctx, "DELETE FROM "+retentionIdent(item.Table)+" AS t WHERE t.id=$2 AND "+pred+refs)
		if err != nil {
			return nil, err
		}
		count := RetentionCount{Table: item.Table, Selected: len(item.IDs)}
		seenIDs := map[int64]bool{}
		for _, id := range item.IDs {
			if id < 1 || seenIDs[id] {
				stmt.Close()
				return nil, ErrRetentionStale
			}
			seenIDs[id] = true
			rs, err := stmt.ExecContext(ctx, item.Cutoff, id)
			if err != nil {
				stmt.Close()
				return nil, err
			}
			n, err := rs.RowsAffected()
			if err != nil {
				stmt.Close()
				return nil, err
			}
			count.Deleted += n
		}
		stmt.Close()
		count.Skipped = int64(count.Selected) - count.Deleted
		result.Counts = append(result.Counts, count)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE retention_plans SET state='used',used_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE token_hash=$1", hash); err != nil {
		return nil, err
	}
	if err = retentionAuditTx(ctx, tx, "execute_committed", hash, result.Counts); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	// 删除只把页还回 freelist；把腾出的页真正交回文件系统。这是最佳努力：
	// 失败只记日志，绝不能因为它回滚已经提交的删除。
	d.ReclaimFreePages()
	result.Committed = true
	return result, nil
}

type RetentionAuditEntry struct {
	ID       int64           `json:"id"`
	At       string          `json:"at"`
	Action   string          `json:"action"`
	PlanHash string          `json:"plan_hash"`
	Actor    string          `json:"actor"`
	Detail   json.RawMessage `json:"detail"`
}

func (d *DB) RetentionAudit(ctx context.Context) ([]RetentionAuditEntry, error) {
	rows, err := d.QueryContext(ctx, "SELECT id,created_at,action,plan_hash,actor,detail FROM retention_audit ORDER BY id DESC LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RetentionAuditEntry{}
	for rows.Next() {
		var item RetentionAuditEntry
		var detail string
		if err = rows.Scan(&item.ID, &item.At, &item.Action, &item.PlanHash, &item.Actor, &detail); err != nil {
			return nil, err
		}
		item.Detail = json.RawMessage(detail)
		out = append(out, item)
	}
	return out, rows.Err()
}

// Failed requests are recorded separately after the main transaction rolls back.
// If the database itself is unavailable the caller must log this audit failure.
func (d *DB) RecordRetentionFailure(ctx context.Context, action string) error {
	_, err := d.ExecContext(ctx, "INSERT INTO retention_audit(action,plan_hash,actor,detail) VALUES($1,'','authenticated_operator','{\"failed\":true}')", action+"_failed")
	return err
}

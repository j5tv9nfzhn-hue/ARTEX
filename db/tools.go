package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Tool is one row of the built-in tool catalog. key + handler live in code; this
// row carries only the page-editable surface: description, parameter schema
// (structure read-only, per-param description/default editable), agent binding,
// and the on/off switch. See schema_sqlite.sql §H and agent/toolcatalog.go.
type Tool struct {
	Key         string          `json:"key"`
	System      bool            `json:"system"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	Agents      []string        `json:"agents"`
	Enabled     bool            `json:"enabled"`
	Kind        string          `json:"kind"`     // builtin | command | script | http
	Exec        json.RawMessage `json:"exec"`     // 自定义工具执行规格(kind!=builtin)
	Deferred    bool            `json:"deferred"` // schema 延迟(走 SearchExtraTools/ExecuteExtraTool)
	Calls       int             `json:"calls"`    // runtime ledger aggregate; not stored in tools
}

const toolCols = `key, system, description, schema, agents, enabled, kind, exec, deferred`

// toolsAddAgentSQL / toolsRemoveAgentSQL 把 agents 数组的增删收敛到一处。
//
//	PG:     jsonb 运算符 `|| to_jsonb(...)`（追加）、`-`（删全部同值成员）、
//	        `?`（成员存在性）
//	SQLite: artex_json_add / artex_json_remove UDF + json_each 存在性子查询
//
// 三者语义逐点对齐：追加发生在「不含该成员」时（NOT EXISTS 与 NOT(?) 同义），
// 删除清掉所有同值成员（与 jsonb `-` 一致），存在性判断按字符串值比较。
func toolsAddAgentSQL() string {
	return `UPDATE tools SET agents = artex_json_add(agents, $1) WHERE key=$2 AND NOT EXISTS (SELECT 1 FROM json_each(agents) a WHERE a.value = $1)`
}

func toolsRemoveAgentSQL(withKey bool) string {
	if withKey {
		return `UPDATE tools SET agents = artex_json_remove(agents, $1) WHERE key=$2 AND EXISTS (SELECT 1 FROM json_each(agents) a WHERE a.value = $1)`
	}
	return `UPDATE tools SET agents = artex_json_remove(agents, $1) WHERE EXISTS (SELECT 1 FROM json_each(agents) a WHERE a.value = $1)`
}

// SeedTool inserts a built-in tool's code-defined defaults ONCE. ON CONFLICT DO
// NOTHING: an existing row (possibly edited in the UI) is never overwritten on
// startup — that's what keeps page edits from being wiped every restart. Use
// UpsertToolForce for an explicit "reset to code default".
func (d *DB) SeedTool(key, desc string, schema, agents json.RawMessage) error {
	if len(schema) == 0 {
		schema = json.RawMessage("{}")
	}
	if len(agents) == 0 {
		agents = json.RawMessage("[]")
	}
	_, err := d.Exec(`
INSERT INTO tools(key, system, description, schema, agents, enabled)
VALUES ($1, true, $2, $3, $4, true)
ON CONFLICT (key) DO NOTHING`, key, desc, schema, agents)
	return err
}

// AddAgentToToolBinding adds an agent key to the given tools' `agents` arrays if
// not already present (idempotent). Used to give the built-in Auto agent its
// default toolset on existing DBs.
func (d *DB) AddAgentToToolBinding(agentKey string, keys []string) error {
	for _, k := range keys {
		if _, err := d.Exec(toolsAddAgentSQL(), agentKey, k); err != nil {
			return err
		}
	}
	return nil
}

// RemoveAgentFromToolBindings strips an agent key from every tool's `agents`
// JSONB array — called when a custom agent is deleted so no tool keeps a dangling
// binding. Uses jsonb `-` (remove array element) guarded by `?` (membership).
func (d *DB) RemoveAgentFromToolBindings(agentKey string) error {
	_, err := d.Exec(toolsRemoveAgentSQL(false), agentKey)
	return err
}

// RemoveAgentFromTool strips one agent key from a SINGLE tool's `agents` array —
// used by one-time migrations that change a tool's default binding on existing DBs
// (SeedTool is first-insert-only, so a changed default never reaches a seeded row).
func (d *DB) RemoveAgentFromTool(agentKey, toolKey string) error {
	_, err := d.Exec(toolsRemoveAgentSQL(true), agentKey, toolKey)
	return err
}

// UpsertToolForce overwrites a tool row with the given code-default values (used by
// the per-tool "reset" action). It resets description/schema/agents and re-enables
// the tool, but keeps system=true.
func (d *DB) UpsertToolForce(key, desc string, schema, agents json.RawMessage) error {
	if len(schema) == 0 {
		schema = json.RawMessage("{}")
	}
	if len(agents) == 0 {
		agents = json.RawMessage("[]")
	}
	_, err := d.Exec(`
INSERT INTO tools(key, system, description, schema, agents, enabled)
VALUES ($1, true, $2, $3, $4, true)
ON CONFLICT (key) DO UPDATE
  SET description = EXCLUDED.description,
      schema      = EXCLUDED.schema,
      agents      = EXCLUDED.agents,
      enabled     = true`, key, desc, schema, agents)
	return err
}

// RefreshToolDefaults updates a system tool's model-facing description + schema to the
// code defaults, PRESERVING the user's agent binding + enabled flag. Used by one-time
// migrations to propagate a code schema change (SeedTool is first-insert-only, so a new
// parameter added in code otherwise never reaches an already-seeded row). No-op for
// custom tools or unknown keys.
// PurgeNonOfficialTools 删除所有不在白名单内的系统内置工具行（第六阶段工具面精简）。
// 只处理 system=true 的内置工具，用户自定义工具（system=false）不受影响。
// 返回被删除的行数。
func (d *DB) PurgeNonOfficialTools(keep []string) (int64, error) {
	if len(keep) == 0 {
		return 0, nil
	}
	ph := make([]string, 0, len(keep))
	args := make([]any, 0, len(keep))
	for i, k := range keep {
		ph = append(ph, fmt.Sprintf("$%d", i+1))
		args = append(args, k)
	}
	q := fmt.Sprintf(`DELETE FROM tools WHERE system AND key NOT IN (%s)`, strings.Join(ph, ","))
	res, err := d.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
func (d *DB) RefreshToolDefaults(key, desc string, schema json.RawMessage) error {
	if len(schema) == 0 {
		schema = json.RawMessage("{}")
	}
	_, err := d.Exec(`UPDATE tools SET description=$2, schema=$3, updated_at=now() WHERE key=$1 AND system`, key, desc, schema)
	return err
}

func scanTool(rows interface{ Scan(...any) error }) (*Tool, error) {
	var t Tool
	// schema / agents / exec 是 JSON 列。pgx 交 []byte、SQLite TEXT 列交
	// string、json.RawMessage 绑定写入的又是 BLOB、builtin 行的 exec 还可能是
	// NULL——直接扫进 json.RawMessage 会在 string/nil 上命中 database/sql 的
	// 盲区（详见 sqlite_json_scan.go），统一经 jsonValue 落点再赋回。
	var schemaCol, agentsCol, execCol jsonValue
	if err := rows.Scan(&t.Key, &t.System, &t.Description, &schemaCol, &agentsCol, &t.Enabled, &t.Kind, &execCol, &t.Deferred); err != nil {
		return nil, err
	}
	t.Schema = schemaCol.RawMessage
	t.Exec = execCol.RawMessage
	if agents := agentsCol.RawMessage; len(agents) > 0 {
		_ = json.Unmarshal(agents, &t.Agents)
	}
	if t.Agents == nil {
		t.Agents = []string{}
	}
	return &t, nil
}

// ListTools returns the whole tool catalog, ordered by key.
func (d *DB) ListTools() ([]*Tool, error) {
	rows, err := d.Query(`SELECT ` + toolCols + ` FROM tools ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tool
	for rows.Next() {
		t, err := scanTool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTool fetches one tool row (nil if absent).
func (d *DB) GetTool(key string) (*Tool, error) {
	row := d.QueryRow(`SELECT `+toolCols+` FROM tools WHERE key=$1`, key)
	t, err := scanTool(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

// CreateCustomTool inserts a user-defined tool (system=false) with an execution
// spec. Fails if the key already exists.
func (d *DB) CreateCustomTool(t *Tool) error {
	schema := t.Schema
	if len(schema) == 0 {
		schema = json.RawMessage("{}")
	}
	exec := t.Exec
	if len(exec) == 0 {
		exec = json.RawMessage("{}")
	}
	agents, _ := json.Marshal(t.Agents)
	if len(agents) == 0 {
		agents = json.RawMessage("[]")
	}
	_, err := d.Exec(`
INSERT INTO tools(key, system, description, schema, agents, enabled, kind, exec, deferred)
VALUES ($1, false, $2, $3, $4, $5, $6, $7, $8)`,
		t.Key, t.Description, schema, agents, t.Enabled, t.Kind, exec, t.Deferred)
	return err
}

// UpdateCustomTool updates a custom tool's editable fields (kind/exec/deferred +
// desc/schema/agents/enabled). Only touches system=false rows.
func (d *DB) UpdateCustomTool(t *Tool) error {
	schema := t.Schema
	if len(schema) == 0 {
		schema = json.RawMessage("{}")
	}
	exec := t.Exec
	if len(exec) == 0 {
		exec = json.RawMessage("{}")
	}
	agents, _ := json.Marshal(t.Agents)
	if len(agents) == 0 {
		agents = json.RawMessage("[]")
	}
	_, err := d.Exec(`
UPDATE tools SET description=$2, schema=$3, agents=$4, enabled=$5, kind=$6, exec=$7, deferred=$8
WHERE key=$1 AND system=false`,
		t.Key, t.Description, schema, agents, t.Enabled, t.Kind, exec, t.Deferred)
	return err
}

// DeleteCustomTool removes a custom tool (system=false only; built-ins protected).
func (d *DB) DeleteCustomTool(key string) error {
	_, err := d.Exec(`DELETE FROM tools WHERE key=$1 AND system=false`, key)
	return err
}

// ListCustomTools returns only the user-defined (system=false) tools.
func (d *DB) ListCustomTools() ([]*Tool, error) {
	rows, err := d.Query(`SELECT ` + toolCols + ` FROM tools WHERE system=false ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tool
	for rows.Next() {
		t, err := scanTool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTool saves the page-editable fields. key is never changed (it is welded to
// the Go handler). system tools: the caller must keep the schema structure — only
// per-param description/default and the agent binding are meant to move.
func (d *DB) UpdateTool(key, desc string, schema, agents json.RawMessage, enabled bool) error {
	if len(schema) == 0 {
		schema = json.RawMessage("{}")
	}
	if len(agents) == 0 {
		agents = json.RawMessage("[]")
	}
	_, err := d.Exec(`
UPDATE tools SET description=$2, schema=$3, agents=$4, enabled=$5 WHERE key=$1`,
		key, desc, schema, agents, enabled)
	return err
}

package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// REOperation is an operator-reviewed mapping to a tool from a live MCP catalog.
// There are no inferred provider command names or arbitrary per-query tool calls.
type REOperation struct {
	Tool        string            `json:"tool"`
	Input       map[string]any    `json:"input"`
	RowsPath    string            `json:"rows_path,omitempty"`
	Columns     map[string]string `json:"columns,omitempty"`
	AddressKind string            `json:"address_kind,omitempty"`
}
type REProviderConfig struct {
	Mode             string                 `json:"mode"`
	ReviewedStatic   bool                   `json:"reviewed_static"`
	SessionKeyPath   string                 `json:"session_key_path,omitempty"`
	IdentityHashPath string                 `json:"identity_hash_path,omitempty"`
	Operations       map[string]REOperation `json:"operations"`
}
type RESample struct {
	SHA256    string          `json:"sha256"`
	Name      string          `json:"name"`
	Size      int64           `json:"size"`
	Metadata  json.RawMessage `json:"metadata"`
	CreatedAt string          `json:"created_at"`
}
type REProvider struct {
	ID          int64            `json:"id"`
	MCPID       int64            `json:"mcp_id"`
	Name        string           `json:"name"`
	Kind        string           `json:"kind"`
	Config      REProviderConfig `json:"config"`
	Catalog     json.RawMessage  `json:"catalog"`
	CatalogHash string           `json:"catalog_hash"`
	Discovery   string           `json:"discovery"`
	Message     string           `json:"message"`
	UpdatedAt   string           `json:"updated_at"`
}
type RESession struct {
	ID         int64           `json:"id"`
	TaskID     int64           `json:"task_id"`
	SampleSHA  string          `json:"sample_sha"`
	ProviderID int64           `json:"provider_id"`
	State      string          `json:"state"`
	Message    string          `json:"message"`
	Snapshot   json.RawMessage `json:"snapshot"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}
type REEvidence struct {
	ID          int64           `json:"id"`
	SessionID   int64           `json:"session_id"`
	SampleSHA   string          `json:"sample_sha"`
	Operation   string          `json:"operation"`
	Tool        string          `json:"tool"`
	Input       json.RawMessage `json:"input"`
	Output      string          `json:"output,omitempty"`
	OutputSHA   string          `json:"output_sha"`
	RetainedSHA string          `json:"retained_sha"`
	Diagnostic  string          `json:"diagnostic"`
	IsError     bool            `json:"is_error"`
	Truncated   bool            `json:"truncated"`
	CreatedAt   string          `json:"created_at"`
}
type REEntity struct {
	ID          int64           `json:"id"`
	SessionID   int64           `json:"session_id"`
	EvidenceID  int64           `json:"evidence_id"`
	Kind        string          `json:"kind"`
	Identity    string          `json:"identity"`
	Name        string          `json:"name"`
	Module      string          `json:"module"`
	Address     string          `json:"address"`
	AddressKind string          `json:"address_kind"`
	Payload     json.RawMessage `json:"payload"`
}

func (d *DB) RERegisterSample(taskID int64, sample RESample) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO re_samples(sha256,name,size,metadata) VALUES($1,$2,$3,$4) ON CONFLICT(sha256) DO NOTHING`, sample.SHA256, sample.Name, sample.Size, string(sample.Metadata)); err != nil {
		return err
	}
	var eligible bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL AND archived_at IS NULL) AND NOT EXISTS(SELECT 1 FROM task_archives WHERE task_id=$1 AND state NOT IN ('archive_failed','delete_failed'))`, taskID).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return fmt.Errorf("任务已归档、删除或有归档操作，不能关联样本")
	}
	if _, err = tx.Exec(`INSERT INTO re_task_samples(task_id,sample_sha) VALUES($1,$2) ON CONFLICT DO NOTHING`, taskID, sample.SHA256); err != nil {
		return err
	}
	return tx.Commit()
}
func (d *DB) RESamples(taskID int64) ([]RESample, error) {
	rows, err := d.Query(`SELECT s.sha256,s.name,s.size,s.metadata,s.created_at FROM re_samples s WHERE $1=0 OR EXISTS(SELECT 1 FROM re_task_samples t WHERE t.sample_sha=s.sha256 AND t.task_id=$1) ORDER BY s.created_at DESC,s.sha256 LIMIT 100`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RESample, 0)
	for rows.Next() {
		var v RESample
		var raw string
		if err = rows.Scan(&v.SHA256, &v.Name, &v.Size, &raw, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Metadata = json.RawMessage(raw)
		out = append(out, v)
	}
	return out, rows.Err()
}
func (d *DB) REHasSample(taskID int64, sha string) bool {
	var yes bool
	err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM re_task_samples WHERE task_id=$1 AND sample_sha=$2)`, taskID, sha).Scan(&yes)
	return err == nil && yes
}
func (d *DB) REProviders() ([]REProvider, error) {
	rows, err := d.Query(`SELECT id,mcp_id,name,kind,config,catalog,catalog_hash,discovery,message,updated_at FROM re_providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]REProvider, 0)
	for rows.Next() {
		var v REProvider
		var raw, cat string
		if err = rows.Scan(&v.ID, &v.MCPID, &v.Name, &v.Kind, &raw, &cat, &v.CatalogHash, &v.Discovery, &v.Message, &v.UpdatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v.Config); err != nil {
			return nil, err
		}
		v.Catalog = json.RawMessage(cat)
		out = append(out, v)
	}
	return out, rows.Err()
}
func (d *DB) REProvider(id int64) (*REProvider, error) {
	ps, err := d.REProviders()
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.ID == id {
			return &p, nil
		}
	}
	return nil, sql.ErrNoRows
}
func (d *DB) REMCPManaged(id int64) (bool, error) {
	var yes bool
	err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM re_providers WHERE mcp_id=$1)`, id).Scan(&yes)
	return yes, err
}
func (d *DB) RESaveProvider(p REProvider) (int64, error) {
	raw, err := json.Marshal(p.Config)
	if err != nil {
		return 0, err
	}
	if p.ID == 0 {
		var id int64
		err = d.QueryRow(`INSERT INTO re_providers(mcp_id,name,kind,config) VALUES($1,$2,$3,$4) RETURNING id`, p.MCPID, p.Name, p.Kind, string(raw)).Scan(&id)
		return id, err
	}
	// A profile cannot change underneath a live or uncertain provider lease.
	result, err := d.Exec(`UPDATE re_providers SET name=$2,kind=$3,config=$4,discovery='unverified',catalog='[]',catalog_hash='',message='',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=$1 AND mcp_id=$5 AND NOT EXISTS(SELECT 1 FROM re_sessions WHERE provider_id=$1 AND state IN ('opening','ready','busy','closing','interrupted'))`, p.ID, p.Name, p.Kind, string(raw), p.MCPID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, fmt.Errorf("Provider 不存在、MCP 关联不可修改或会话仍持有租约")
	}
	return p.ID, nil
}
func (d *DB) RECatalog(id int64, catalog, hash, state, message string) error {
	_, err := d.Exec(`UPDATE re_providers SET catalog=$2,catalog_hash=$3,discovery=$4,message=$5,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=$1`, id, catalog, hash, state, message)
	return err
}
func (d *DB) RECreateSession(taskID int64, sha string, p REProvider) (*RESession, error) {
	snapshot, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var id int64
	err = d.QueryRow(`INSERT INTO re_sessions(task_id,sample_sha,provider_id,state,snapshot) VALUES($1,$2,$3,'opening',$4) RETURNING id`, taskID, sha, p.ID, string(snapshot)).Scan(&id)
	if err != nil {
		return nil, err
	}
	return d.RESession(taskID, id)
}

const reSessionCols = `id,task_id,sample_sha,provider_id,state,message,snapshot,created_at,updated_at`

func reScanSession(s interface{ Scan(...any) error }) (*RESession, error) {
	var v RESession
	var raw string
	err := s.Scan(&v.ID, &v.TaskID, &v.SampleSHA, &v.ProviderID, &v.State, &v.Message, &raw, &v.CreatedAt, &v.UpdatedAt)
	v.Snapshot = json.RawMessage(raw)
	return &v, err
}
func (d *DB) RESession(taskID, id int64) (*RESession, error) {
	return reScanSession(d.QueryRow(`SELECT `+reSessionCols+` FROM re_sessions WHERE task_id=$1 AND id=$2`, taskID, id))
}
func (d *DB) RESessions(taskID int64) ([]RESession, error) {
	rows, err := d.Query(`SELECT `+reSessionCols+` FROM re_sessions WHERE task_id=$1 ORDER BY id DESC LIMIT 100`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RESession, 0)
	for rows.Next() {
		v, err := reScanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}
func (d *DB) RETransition(id int64, from, to, message string) error {
	result, err := d.Exec(`UPDATE re_sessions SET state=$3,message=$4,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=$1 AND state=$2`, id, from, to, message)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("会话状态已改变，请刷新")
	}
	return nil
}

// REAppendResult commits evidence and normalized rows only while the caller still
// owns the operation state. Cancellation/release cannot be undone by a late reply.
func (d *DB) REAppendResult(session RESession, expected, next string, e REEvidence, entities []REEntity) (int64, error) {
	tx, err := d.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE re_sessions SET state=$3,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=$1 AND state=$2`, session.ID, expected, next)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, fmt.Errorf("会话已取消或租约改变，丢弃迟到结果")
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO re_evidence(session_id,sample_sha,operation,tool,input,output,output_sha,retained_sha,diagnostic,is_error,truncated) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, session.ID, session.SampleSHA, e.Operation, e.Tool, string(e.Input), e.Output, e.OutputSHA, e.RetainedSHA, e.Diagnostic, e.IsError, e.Truncated).Scan(&id)
	if err != nil {
		return 0, err
	}
	for _, v := range entities {
		_, err = tx.Exec(`INSERT INTO re_entities(session_id,evidence_id,kind,identity,name,module,address,address_kind,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(session_id,kind,identity) DO UPDATE SET evidence_id=excluded.evidence_id,name=excluded.name,module=excluded.module,address=excluded.address,address_kind=excluded.address_kind,payload=excluded.payload`, session.ID, id, v.Kind, v.Identity, v.Name, v.Module, v.Address, v.AddressKind, string(v.Payload))
		if err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}
func (d *DB) REEvidence(taskID, id int64) (*REEvidence, error) {
	var v REEvidence
	var raw string
	err := d.QueryRow(`SELECT e.id,e.session_id,e.sample_sha,e.operation,e.tool,e.input,e.output,e.output_sha,e.retained_sha,e.diagnostic,e.is_error,e.truncated,e.created_at FROM re_evidence e JOIN re_sessions s ON s.id=e.session_id WHERE s.task_id=$1 AND e.id=$2`, taskID, id).Scan(&v.ID, &v.SessionID, &v.SampleSHA, &v.Operation, &v.Tool, &raw, &v.Output, &v.OutputSHA, &v.RetainedSHA, &v.Diagnostic, &v.IsError, &v.Truncated, &v.CreatedAt)
	v.Input = json.RawMessage(raw)
	return &v, err
}
func (d *DB) REEvidenceList(taskID, sessionID, before int64) ([]REEvidence, error) {
	rows, err := d.Query(`SELECT e.id,e.session_id,e.sample_sha,e.operation,e.tool,e.input,e.output_sha,e.retained_sha,e.diagnostic,e.is_error,e.truncated,e.created_at FROM re_evidence e JOIN re_sessions s ON s.id=e.session_id WHERE s.task_id=$1 AND s.id=$2 AND ($3=0 OR e.id<$3) ORDER BY e.id DESC LIMIT 101`, taskID, sessionID, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]REEvidence, 0)
	for rows.Next() {
		var v REEvidence
		var raw string
		if err = rows.Scan(&v.ID, &v.SessionID, &v.SampleSHA, &v.Operation, &v.Tool, &raw, &v.OutputSHA, &v.RetainedSHA, &v.Diagnostic, &v.IsError, &v.Truncated, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Input = json.RawMessage(raw)
		out = append(out, v)
	}
	return out, rows.Err()
}
func (d *DB) REEntities(taskID, sessionID, before int64, kind string) ([]REEntity, error) {
	rows, err := d.Query(`SELECT e.id,e.session_id,e.evidence_id,e.kind,e.identity,e.name,e.module,e.address,e.address_kind,e.payload FROM re_entities e JOIN re_sessions s ON s.id=e.session_id WHERE s.task_id=$1 AND s.id=$2 AND e.kind=$3 AND ($4=0 OR e.id<$4) ORDER BY e.id DESC LIMIT 101`, taskID, sessionID, kind, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]REEntity, 0)
	for rows.Next() {
		var v REEntity
		var raw string
		if err = rows.Scan(&v.ID, &v.SessionID, &v.EvidenceID, &v.Kind, &v.Identity, &v.Name, &v.Module, &v.Address, &v.AddressKind, &raw); err != nil {
			return nil, err
		}
		v.Payload = json.RawMessage(raw)
		out = append(out, v)
	}
	return out, rows.Err()
}

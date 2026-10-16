package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Autumn-27/artex/sidequestion"
	"github.com/google/uuid"
)

var ErrSideBusy = errors.New("当前会话已有旁路问题正在回答")
var ErrSideParentGone = errors.New("旁路父会话已删除或归档")

// Lock the real parent before the side session, also covering soft task/intent
// deletion. A delayed checkpoint cannot recreate data after archive cleanup.
//
// 行锁子句按引擎取：PG 保留 FOR SHARE，SQLite 没有行锁、返回空串（见 dialect_locking.go）。
// 这里要挡的是「父行在本事务写入旁路会话期间被并发删掉/归档」，而 SQLite 的写事务从
// 第一条写语句起独占库文件，并发的删除只能整个排在本事务之前或之后，不存在插入窗口。
func lockSideParent(ctx context.Context, tx *sql.Tx, p sidequestion.Parent) error {
	var id int64
	var err error
	if p.ConversationID > 0 {
		err = tx.QueryRowContext(ctx, `SELECT id FROM conversations WHERE id=$1`+forShareClause(), p.ConversationID).Scan(&id)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE id=$1 AND exploration_id=$2 AND deleted_at IS NULL AND archived_at IS NULL`+forShareClause(), p.TaskID, p.ExplorationID).Scan(&id)
		if err == nil && p.IntentID > 0 {
			err = tx.QueryRowContext(ctx, `SELECT id FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent' AND state<>'stopped'`+forShareClause(), p.IntentID, p.ExplorationID).Scan(&id)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSideParentGone
	}
	return err
}

func nullableSideID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func (d *DB) SaveSideSnapshot(ctx context.Context, s sidequestion.Snapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockSideParent(ctx, tx, s.Parent); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO side_question_sessions(session_key,conversation_id,task_id,exploration_id,intent_id,run_id,version,snapshot)
VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(session_key) DO UPDATE SET run_id=EXCLUDED.run_id,version=EXCLUDED.version,snapshot=EXCLUDED.snapshot
WHERE (side_question_sessions.run_id,side_question_sessions.version)<(EXCLUDED.run_id,EXCLUDED.version)`,
		s.Parent.Key(), nullableSideID(s.Parent.ConversationID), nullableSideID(s.Parent.TaskID), nullableSideID(s.Parent.ExplorationID), nullableSideID(s.Parent.IntentID), s.RunID, s.Version, string(jsonbClean(b)))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) SideSnapshot(ctx context.Context, key string) (*sidequestion.Snapshot, error) {
	var b []byte
	err := d.QueryRowContext(ctx, `SELECT snapshot FROM side_question_sessions WHERE session_key=$1`, key).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s sidequestion.Snapshot
	err = json.Unmarshal(b, &s)
	return &s, err
}

const sideCols = `id,ordinal,session_key,generation,client_id,question,answer,status,error,model,snapshot_at,created_at,sequence,usage,context_info`

func (d *DB) ExistingSideRequest(ctx context.Context, key, client string) (*sidequestion.Exchange, error) {
	e, err := scanSide(d.QueryRowContext(ctx, `SELECT `+sideCols+` FROM side_question_requests WHERE session_key=$1 AND client_id=$2 AND generation=(SELECT generation FROM side_question_sessions WHERE session_key=$1)`, key, client))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &e, err
}

func scanSide(row interface{ Scan(...any) error }) (sidequestion.Exchange, error) {
	var e sidequestion.Exchange
	var model, usage, info []byte
	err := row.Scan(&e.ID, &e.Ordinal, &e.SessionKey, &e.Generation, &e.ClientID, &e.Question, &e.Answer, &e.Status, &e.Error, &model, &e.SnapshotAt, &e.CreatedAt, &e.Sequence, &usage, &info)
	if err != nil {
		return e, err
	}
	if err = json.Unmarshal(model, &e.Model); err != nil {
		return e, err
	}
	if err = json.Unmarshal(info, &e.Context); err != nil {
		return e, err
	}
	err = json.Unmarshal(usage, &e.Usage)
	return e, err
}

func (d *DB) SideRequest(ctx context.Context, id string) (*sidequestion.Exchange, error) {
	e, err := scanSide(d.QueryRowContext(ctx, `SELECT `+sideCols+` FROM side_question_requests WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &e, err
}

func (d *DB) CurrentSideRequest(ctx context.Context, key string) (*sidequestion.Exchange, error) {
	e, err := scanSide(d.QueryRowContext(ctx, `SELECT `+sideCols+` FROM side_question_requests WHERE session_key=$1 AND status='running'`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &e, err
}

func (d *DB) SideHistory(ctx context.Context, key string, before int64, limit int) ([]sidequestion.Exchange, error) {
	// The cursor filter uses "before==0 means no filter". PostgreSQL needs the
	// explicit ::bigint so pgx can type the parameter against both uses in the
	// OR; SQLite would parse the whole `$2::bigint` as a parameter *named*
	// "2::bigint" and report "missing named argument" -- exactly the failure
	// this cast is meant to avoid there. `before` is bound as an integer, so
	// the bare comparison is already well-typed.
	cursor := "($2=0 OR ordinal<$2)"

	rows, err := d.QueryContext(ctx, `SELECT `+sideCols+` FROM side_question_requests WHERE session_key=$1 AND `+cursor+` ORDER BY ordinal DESC LIMIT $3`, key, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sidequestion.Exchange{}
	for rows.Next() {
		e, err := scanSide(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) SideReplay(ctx context.Context, key string) ([]sidequestion.Exchange, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+sideCols+` FROM side_question_requests WHERE session_key=$1 AND status='completed' ORDER BY ordinal DESC LIMIT 20`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sidequestion.Exchange{}
	for rows.Next() {
		e, err := scanSide(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (d *DB) StartSideRequest(ctx context.Context, s sidequestion.Snapshot, clientID, question string) (*sidequestion.Exchange, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if err = lockSideParent(ctx, tx, s.Parent); err != nil {
		return nil, false, err
	}
	var generation int64
	// 行锁子句按引擎取：PG 保留 FOR UPDATE，SQLite 返回空串（见 dialect_locking.go）。
	// 锁防的是「同一 session_key 的两个 StartSideRequest 读到同一个 generation 后双双
	// 插入 running 行」。SQLite 的单写者保证后到的事务读到的是前一个提交后的
	// generation；即使真撞上，schema 上的 UNIQUE(session_key,generation,client_id)
	// 与 idx_side_request_running 这条部分唯一索引仍会把重复行挡在库外。
	if err = tx.QueryRowContext(ctx, `SELECT generation FROM side_question_sessions WHERE session_key=$1`+forUpdateClause(false), s.Parent.Key()).Scan(&generation); err != nil {
		return nil, false, err
	}
	e, err := scanSide(tx.QueryRowContext(ctx, `SELECT `+sideCols+` FROM side_question_requests WHERE session_key=$1 AND generation=$2 AND client_id=$3`, s.Parent.Key(), generation, clientID))
	if err == nil {
		if e.Question != question {
			return nil, false, fmt.Errorf("同一请求 ID 不能用于不同问题")
		}
		return &e, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var busy bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM side_question_requests WHERE session_key=$1 AND status='running')`, s.Parent.Key()).Scan(&busy); err != nil {
		return nil, false, err
	}
	if busy {
		return nil, false, ErrSideBusy
	}
	model, err := json.Marshal(s.Model)
	if err != nil {
		return nil, false, err
	}
	// ordinal 是分页用的单调序号：PostgreSQL 侧靠 BIGSERIAL 的 nextval 赋值，
	// 但那个列不是主键，schema 转换后成了没有默认值的普通 INTEGER，新行的
	// ordinal 因此是 NULL——而 scanSide 扫进非指针的 int64，直接失败。
	// SQLite 没有序列，所以在事务内取下一个值显式写入。
	//
	// 放在同一事务里是安全的：SQLite 同一时刻只有一个写事务（DSN 带
	// _txlock=immediate，写锁在 BEGIN 就取得），所以 MAX+1 到 INSERT 之间
	// 不会有别的写入插进来。PostgreSQL 分支仍走 BIGSERIAL，SQL 不变。
	ordinal, err := nextSideOrdinal(tx)
	if err != nil {
		return nil, false, err
	}
	e, err = scanSide(tx.QueryRowContext(ctx, `INSERT INTO side_question_requests(id,ordinal,session_key,generation,client_id,question,status,model,snapshot_at) VALUES($1,$2,$3,$4,$5,$6,'running',$7,$8) RETURNING `+sideCols, uuid.NewString(), ordinal, s.Parent.Key(), generation, clientID, question, string(model), s.CapturedAt))
	if err != nil {
		return nil, false, err
	}
	return &e, true, tx.Commit()
}

// nextSideOrdinal returns the next paging ordinal inside the caller's
// transaction. See the call site for why this is computed in Go rather than
// left to a column default.
func nextSideOrdinal(tx *sql.Tx) (int64, error) {
	var max sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(ordinal) FROM side_question_requests`).Scan(&max); err != nil {
		return 0, err
	}
	return max.Int64 + 1, nil
}

// Conditional updates cannot resurrect deleted history or overwrite a terminal
// cancellation with a late provider callback.
func (d *DB) UpdateSideRequest(ctx context.Context, e sidequestion.Exchange) (bool, error) {
	usage, err := json.Marshal(e.Usage)
	if err != nil {
		return false, err
	}
	info, err := json.Marshal(e.Context)
	if err != nil {
		return false, err
	}
	r, err := d.ExecContext(ctx, `UPDATE side_question_requests AS r SET answer=$2,status=$3,error=$4,sequence=$5,usage=$6,context_info=$7
WHERE r.id=$1 AND r.status='running' AND r.sequence<$5 AND EXISTS(SELECT 1 FROM side_question_sessions s WHERE s.session_key=r.session_key AND s.generation=r.generation)`, e.ID, e.Answer, e.Status, e.Error, e.Sequence, string(usage), string(info))
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (d *DB) ClearSideHistory(ctx context.Context, key string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE side_question_sessions SET generation=generation+1,memory='{}' WHERE session_key=$1`, key); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM side_question_requests WHERE session_key=$1`, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) SideMemory(ctx context.Context, e sidequestion.Exchange) (sidequestion.Memory, error) {
	var memory sidequestion.Memory
	var raw []byte
	err := d.QueryRowContext(ctx, `SELECT s.memory FROM side_question_sessions s JOIN side_question_requests r ON r.session_key=s.session_key
WHERE r.id=$1 AND r.generation=s.generation AND s.generation=$2 AND r.status='running'`, e.ID, e.Generation).Scan(&raw)
	if err != nil {
		return memory, err
	}
	err = json.Unmarshal(raw, &memory)
	return memory, err
}

// Unlike SideReplay's UI-era 20-row window, this cursor visits all unsummarized
// successful exchanges, in bounded pages and only before the admitted request.
func (d *DB) SideReplayPage(ctx context.Context, e sidequestion.Exchange, after int64) ([]sidequestion.Exchange, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+sideCols+` FROM side_question_requests WHERE session_key=$1 AND generation=$2
AND status='completed' AND ordinal>$3 AND ordinal<$4 ORDER BY ordinal LIMIT 20`, e.SessionKey, e.Generation, after, e.Ordinal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sidequestion.Exchange
	for rows.Next() {
		item, err := scanSide(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (d *DB) SaveSideMemory(ctx context.Context, e sidequestion.Exchange, memory sidequestion.Memory) error {
	raw, err := json.Marshal(memory)
	if err != nil {
		return err
	}
	result, err := d.ExecContext(ctx, `UPDATE side_question_sessions AS s SET memory=$3 WHERE s.session_key=$1 AND s.generation=$2
AND EXISTS(SELECT 1 FROM side_question_requests r WHERE r.id=$4 AND r.session_key=s.session_key AND r.generation=s.generation AND r.status='running')`, e.SessionKey, e.Generation, string(raw), e.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrSideParentGone
	}
	return err
}

func (d *DB) InterruptSideRequests(ctx context.Context) error {
	_, err := d.ExecContext(ctx, `UPDATE side_question_requests SET status='interrupted',error='服务重启，回答已中断',sequence=sequence+1 WHERE status='running'`)
	return err
}

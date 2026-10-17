package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	TaskArchiveFormatVersion       = 3
	TaskArchiveLegacyFormatVersion = 1
	TaskArchiveLLMRecordsPath      = "database/llm_records.ndjson"
)

func IsTaskArchiveFormatSupported(version int) bool {
	return version >= TaskArchiveLegacyFormatVersion && version <= TaskArchiveFormatVersion
}

const (
	ArchiveQueued = "archive_queued"
	Archiving     = "archiving"
	ArchiveFailed = "archive_failed"
	ArchiveReady  = "ready"
	RestoreQueued = "restore_queued"
	Restoring     = "restoring"
	RestoreFailed = "restore_failed"
	DeleteQueued  = "delete_queued"
	Deleting      = "deleting"
	DeleteFailed  = "delete_failed"
)

var (
	ErrTaskArchiveNotFound       = errors.New("task archive not found")
	ErrTaskArchiveIneligible     = errors.New("task must be paused or terminal before archiving")
	ErrTaskArchiveQueued         = errors.New("queued task must be paused before archiving")
	ErrTaskArchiveDependent      = errors.New("task is inherited by a live task")
	ErrTaskArchiveState          = errors.New("task archive state does not allow this operation")
	ErrTaskArchiveDeleteBlocked  = errors.New("task archive is required by another archive")
	ErrTaskArchiveFormatMismatch = errors.New("task archive format is not supported")
)

// TaskArchive is the compact PostgreSQL record retained while a task is cold.
// Sensitive profile configuration and API keys are intentionally absent.
type TaskArchive struct {
	ID                      int64           `json:"id"`
	TaskID                  int64           `json:"task_id"`
	State                   string          `json:"state"`
	Phase                   string          `json:"phase"`
	Progress                int             `json:"progress"`
	Error                   string          `json:"error,omitempty"`
	Warnings                json.RawMessage `json:"warnings"`
	FormatVersion           int             `json:"format_version"`
	ArchivePath             string          `json:"-"`
	SHA256                  string          `json:"sha256,omitempty"`
	OriginalSize            int64           `json:"original_size"`
	CompressedSize          int64           `json:"compressed_size"`
	TaskName                string          `json:"task_name"`
	TaskDescription         string          `json:"task_description"`
	TaskGoal                string          `json:"task_goal"`
	OriginalStatus          string          `json:"original_status"`
	CategoryIDSnapshot      *int64          `json:"category_id,omitempty"`
	CategoryNameSnapshot    string          `json:"category_name,omitempty"`
	SourceTaskIDs           []int64         `json:"source_task_ids"`
	RemainingTimeoutSeconds int64           `json:"remaining_timeout_seconds"`
	DataCounts              json.RawMessage `json:"data_counts"`
	AggregateStats          json.RawMessage `json:"aggregate_stats"`
	ArchivedAt              *time.Time      `json:"archived_at,omitempty"`
	RequestedAt             time.Time       `json:"requested_at"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

// TaskArchiveBlockers returns one live direct dependent for every source task
// that cannot currently be archived. Dependents already queued for archiving do
// not block their source because the FIFO worker will compact them first.
func (d *DB) TaskArchiveBlockers() (map[int64]int64, error) {
	rows, err := d.Query(`SELECT relation.source_task_id, MIN(child.id)
FROM task_relations relation
JOIN tasks child ON child.id=relation.task_id AND child.deleted_at IS NULL
LEFT JOIN task_archives pending ON pending.task_id=child.id
WHERE pending.id IS NULL OR pending.state NOT IN ('archive_queued','archiving')
GROUP BY relation.source_task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	blockers := map[int64]int64{}
	for rows.Next() {
		var sourceID, dependentID int64
		if err := rows.Scan(&sourceID, &dependentID); err != nil {
			return nil, err
		}
		blockers[sourceID] = dependentID
	}
	return blockers, rows.Err()
}

type TaskArchivePage struct {
	Items []TaskArchive `json:"items"`
	Total int           `json:"total"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
}

// TaskArchiveSnapshot is serialized into manifest.json inside the cold package.
// Small tables remain JSON arrays in Tables. Large v2 tables are streamed to
// package files listed in StreamedTables so their size is not bounded by memory.
type TaskArchiveSnapshot struct {
	FormatVersion     int                        `json:"format_version"`
	CreatedAt         time.Time                  `json:"created_at"`
	TaskID            int64                      `json:"task_id"`
	ExplorationID     int64                      `json:"exploration_id"`
	SourceTaskIDs     []int64                    `json:"source_task_ids"`
	Hosts             []string                   `json:"hosts"`
	ExclusiveHosts    []string                   `json:"exclusive_hosts"`
	ExclusiveAssetIDs []int64                    `json:"exclusive_asset_ids"`
	Tables            map[string]json.RawMessage `json:"tables"`
	StreamedTables    map[string]string          `json:"streamed_tables,omitempty"`
	DataCounts        map[string]int64           `json:"data_counts"`
	AggregateStats    map[string]any             `json:"aggregate_stats"`
}

func scanTaskArchive(sc interface{ Scan(...any) error }) (*TaskArchive, error) {
	var item TaskArchive
	var sources string
	// warnings / data_counts / aggregate_stats 三个 JSON 列必须先落进 jsonValue。
	// SQLite 把 JSON 存在 TEXT 列、交出 string，而 json.RawMessage 在 Go 1.27
	// 上是 jsontext.Value 的别名——database/sql 的 convertAssign 只对精确的
	// *[]byte 写了 string/[]byte 两个分支，具名的 []byte 类型一个都不沾，
	// 于是报 "unsupported Scan ... into type *jsontext.Value"。PG 侧一直没暴露，
	// 是因为 pgx 把 JSONB 交成 []byte，恰好撞在反射兜底上。详见 sqlite_json_scan.go。
	var warnings, dataCounts, aggregateStats jsonValue
	err := sc.Scan(
		&item.ID, &item.TaskID, &item.State, &item.Phase, &item.Progress, &item.Error,
		&warnings, &item.FormatVersion, &item.ArchivePath, &item.SHA256,
		&item.OriginalSize, &item.CompressedSize, &item.TaskName, &item.TaskDescription,
		&item.TaskGoal, &item.OriginalStatus, &item.CategoryIDSnapshot,
		&item.CategoryNameSnapshot, &sources, &item.RemainingTimeoutSeconds,
		&dataCounts, &aggregateStats, &item.ArchivedAt, &item.RequestedAt,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	item.Warnings = warnings.Raw()
	item.DataCounts = dataCounts.Raw()
	item.AggregateStats = aggregateStats.Raw()
	if err := json.Unmarshal([]byte(sources), &item.SourceTaskIDs); err != nil {
		return nil, err
	}
	if len(item.Warnings) == 0 {
		item.Warnings = json.RawMessage("[]")
	}
	if len(item.DataCounts) == 0 {
		item.DataCounts = json.RawMessage("{}")
	}
	if len(item.AggregateStats) == 0 {
		item.AggregateStats = json.RawMessage("{}")
	}
	return &item, nil
}

// taskArchiveCols 是读取列清单。声明成 var 而非 const：source_task_ids 的
// 读法按引擎不同（`::text` vs CAST），而 const 求值期不能调用函数。
var taskArchiveCols = `id, task_id, state, phase, progress, COALESCE(error,''), warnings,
format_version, COALESCE(archive_path,''), COALESCE(sha256,''), original_size,
compressed_size, COALESCE(task_name,''), COALESCE(task_description,''),
COALESCE(task_goal,''), COALESCE(original_status,''), category_id_snapshot,
COALESCE(category_name_snapshot,''), ` + sqlCastText("array_to_json(source_task_ids)") + `,
remaining_timeout_seconds, data_counts, aggregate_stats, archived_at,
requested_at, created_at, updated_at`

func (d *DB) GetTaskArchive(id int64) (*TaskArchive, error) {
	item, err := scanTaskArchive(d.QueryRow(`SELECT `+taskArchiveCols+` FROM task_archives WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func (d *DB) GetTaskArchiveByTask(taskID int64) (*TaskArchive, error) {
	item, err := scanTaskArchive(d.QueryRow(`SELECT `+taskArchiveCols+` FROM task_archives WHERE task_id=$1`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func (d *DB) ListTaskArchives(search, state string, page, size int) (TaskArchivePage, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 20
	}
	search = strings.TrimSpace(search)
	state = strings.TrimSpace(state)
	// task_id::text / ILIKE 都是 PG 写法：裸的 `::` 会被 SQLite 判成
	// unrecognized token ":"，ILIKE 更是没有对应关键字。这里按引擎换成
	// CAST(... AS TEXT) + LIKE（SQLite 的 LIKE 对 ASCII 本就大小写不敏感）。
	where := `WHERE ($1='' OR ` + sqlCastText("task_id") + ` ` + sqlILike() + ` '%'||$1||'%'
  OR task_name ` + sqlILike() + ` '%'||$1||'%'
  OR task_description ` + sqlILike() + ` '%'||$1||'%')
AND ($2='' OR state=$2)`
	var out TaskArchivePage
	out.Page, out.Size = page, size
	if err := d.QueryRow(`SELECT count(*) FROM task_archives `+where, search, state).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := d.Query(`SELECT `+taskArchiveCols+` FROM task_archives `+where+`
ORDER BY COALESCE(archived_at, requested_at) DESC, id DESC LIMIT $3 OFFSET $4`, search, state, size, (page-1)*size)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanTaskArchive(rows)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, *item)
	}
	return out, rows.Err()
}

// QueueTaskArchive validates lifecycle and direct inheritance while holding the
// task row. A failed archive can be explicitly retried through the same API.
func (d *DB) QueueTaskArchive(taskID int64) (*TaskArchive, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var name, description, goal, status, categoryName string
	var categoryID *int64
	var paused, queued bool
	var deadline *time.Time
	err = tx.QueryRow(`SELECT COALESCE(t.name,''), t.description, t.goal, t.status,
 t.category_id, COALESCE(c.name,''), t.paused, t.queued, t.deadline_at
FROM tasks t LEFT JOIN task_categories c ON c.id=t.category_id
WHERE t.id=$1 AND t.deleted_at IS NULL`+sqlForUpdate("t"), taskID).Scan(
		&name, &description, &goal, &status, &categoryID, &categoryName, &paused, &queued, &deadline,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskArchiveNotFound
	}
	if err != nil {
		return nil, err
	}
	if queued {
		return nil, ErrTaskArchiveQueued
	}
	if !paused && !IsTerminal(status) {
		return nil, ErrTaskArchiveIneligible
	}
	var dependent int64
	err = tx.QueryRow(`SELECT child.id FROM task_relations relation
JOIN tasks child ON child.id=relation.task_id AND child.deleted_at IS NULL
LEFT JOIN task_archives pending ON pending.task_id=child.id
WHERE relation.source_task_id=$1
  AND (pending.id IS NULL OR pending.state NOT IN ('archive_queued','archiving'))
LIMIT 1`, taskID).Scan(&dependent)
	if err == nil {
		return nil, fmt.Errorf("%w: task %d", ErrTaskArchiveDependent, dependent)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var sources []int64
	rows, err := tx.Query(`SELECT source_task_id FROM task_relations WHERE task_id=$1 ORDER BY created_at, source_task_id`, taskID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if sources == nil {
		sources = []int64{}
	}
	remaining := int64(0)
	if paused && deadline != nil {
		remaining = int64(time.Until(*deadline).Seconds())
		if remaining < 0 {
			remaining = 0
		}
	}
	_, err = tx.Exec(`INSERT INTO task_archives(
 task_id,state,phase,progress,error,warnings,format_version,task_name,
 task_description,task_goal,original_status,category_id_snapshot,
 category_name_snapshot,source_task_ids,remaining_timeout_seconds,requested_at)
VALUES ($1,$2,'queued',0,'','[]',$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
	ON CONFLICT (task_id) DO UPDATE SET
	 state=CASE WHEN task_archives.state IN ('archive_failed') THEN EXCLUDED.state ELSE task_archives.state END,
	 phase=CASE WHEN task_archives.state IN ('archive_failed') THEN 'queued' ELSE task_archives.phase END,
	 progress=CASE WHEN task_archives.state IN ('archive_failed') THEN 0 ELSE task_archives.progress END,
	 error=CASE WHEN task_archives.state IN ('archive_failed') THEN '' ELSE task_archives.error END,
	 format_version=CASE WHEN task_archives.state IN ('archive_failed') THEN EXCLUDED.format_version ELSE task_archives.format_version END,
	 requested_at=CASE WHEN task_archives.state IN ('archive_failed') THEN now() ELSE task_archives.requested_at END`,
		taskID, ArchiveQueued, TaskArchiveFormatVersion, name, description, goal, status,
		categoryID, categoryName, sqlInt64ArrayArg(sources), remaining)
	if err != nil {
		return nil, err
	}
	item, err := scanTaskArchive(tx.QueryRow(`SELECT `+taskArchiveCols+` FROM task_archives WHERE task_id=$1`, taskID))
	if err != nil {
		return nil, err
	}
	if item.State != ArchiveQueued && item.State != ArchiveFailed {
		return nil, fmt.Errorf("%w: current state %s", ErrTaskArchiveState, item.State)
	}
	return item, tx.Commit()
}

func (d *DB) QueueTaskArchiveRestore(id int64) (*TaskArchive, error) {
	item, err := scanTaskArchive(d.QueryRow(`UPDATE task_archives
SET state=$2, phase='queued', progress=0, error='', requested_at=now()
WHERE id=$1 AND state IN ('ready','restore_failed') RETURNING `+taskArchiveCols, id, RestoreQueued))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskArchiveState
	}
	return item, err
}

func (d *DB) QueueTaskArchiveDelete(id int64) (*TaskArchive, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var taskID int64
	if err := tx.QueryRow(`SELECT task_id FROM task_archives WHERE id=$1 AND state IN ('ready','delete_failed')`+sqlForUpdate(), id).Scan(&taskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskArchiveState
		}
		return nil, err
	}
	var dependent int64
	err = tx.QueryRow(`SELECT task_id FROM task_archives
WHERE id<>$1 AND `+taskIDsAnyExpr("$2", "source_task_ids")+` AND state NOT IN ('delete_queued','deleting') LIMIT 1`, id, taskID).Scan(&dependent)
	if err == nil {
		return nil, fmt.Errorf("%w: task %d", ErrTaskArchiveDeleteBlocked, dependent)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	item, err := scanTaskArchive(tx.QueryRow(`UPDATE task_archives
SET state=$2, phase='queued', progress=0, error='', requested_at=now()
WHERE id=$1 RETURNING `+taskArchiveCols, id, DeleteQueued))
	if err != nil {
		return nil, err
	}
	return item, tx.Commit()
}

// RecoverTaskArchiveJobs keeps restore/delete resumable after an unclean shutdown.
// An interrupted archive requires an explicit retry: automatic startup retries can
// otherwise form a crash loop when the prior process was killed by resource limits.
func (d *DB) RecoverTaskArchiveJobs() error {
	_, err := d.Exec(`UPDATE task_archives SET
	 state=CASE state WHEN 'archiving' THEN 'archive_failed'
	                  WHEN 'restoring' THEN 'restore_queued'
	                  WHEN 'deleting' THEN 'delete_queued' ELSE state END,
	 phase='interrupted',
	 error=CASE WHEN state='archiving' THEN '上次归档进程异常退出，请手动重试' ELSE '' END
	WHERE state IN ('archiving','restoring','deleting')`)
	return err
}

// taskArchiveQueuedStates 是「等待领取」的队列状态集合：ClaimTaskArchiveJob 的
// 空队早退探测与领取语句的 IN 子查询共用同一份，避免两处条件在后续修改中漂移。
const taskArchiveQueuedStates = `state IN ('archive_queued','restore_queued','delete_queued')`

// ClaimTaskArchiveJob claims one persistent FIFO item for the single archive
// worker. It returns nil when the queue is empty.
//
// 抢占用一条 UPDATE ... WHERE id = (SELECT ... ORDER BY ... LIMIT 1) RETURNING ...：
// UPDATE 一开始就拿写锁，子查询在同一条语句里求值，因此抢占是原子的，
// 并发领取者会串行地各领一行或领不到，绝不会两人领到同一行。
// state 的映射由 CASE 就地完成，RETURNING 直接给出占用态后的整行。
// （等价性论证见 dialect_locking.go 文件头。）
func (d *DB) ClaimTaskArchiveJob(ctx context.Context) (*TaskArchive, error) {
	// 空队早退：领取是一条 UPDATE，即便匹配 0 行也会先拿库级写锁；队列为空时
	// 后台 worker 每个轮询周期都白占一次写锁。用只读的 EXISTS 先探测（读不参与
	// 写锁竞争），为空直接返回；探测与领取共用 taskArchiveQueuedStates 条件。
	var hasJob bool
	if err := d.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT id FROM task_archives WHERE `+taskArchiveQueuedStates+`)`).Scan(&hasJob); err != nil {
		return nil, err
	}
	if !hasJob {
		return nil, nil
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	item, err := scanTaskArchive(tx.QueryRowContext(ctx, `UPDATE task_archives
SET state=CASE state
        WHEN 'archive_queued' THEN '`+Archiving+`'
        WHEN 'restore_queued' THEN '`+Restoring+`'
        ELSE '`+Deleting+`' END,
    phase='starting',progress=1,error=''
WHERE id=(SELECT id FROM task_archives
          WHERE `+taskArchiveQueuedStates+`
          ORDER BY requested_at,id LIMIT 1)
RETURNING `+taskArchiveCols))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return item, tx.Commit()
}

func (d *DB) UpdateTaskArchiveProgress(id int64, phase string, progress int) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	_, err := d.Exec(`UPDATE task_archives SET phase=$2,progress=$3 WHERE id=$1`, id, phase, progress)
	return err
}

func (d *DB) AppendTaskArchiveWarning(id int64, warning string) error {
	if strings.TrimSpace(warning) == "" {
		return nil
	}
	// warnings 列是 JSON 数组文本。json_insert 的 '$[#]' 追加下标在末尾追加一个
	// 字符串元素；占位符上不能带 ::text，否则 SQLite 会去找名为 "2::text" 的命名参数。
	_, err := d.Exec(`UPDATE task_archives SET warnings=json_insert(warnings,'$[#]',$2) WHERE id=$1`, id, warning)
	return err
}

func (d *DB) IsTaskArchiveRestored(id int64) (bool, error) {
	var restored bool
	err := d.QueryRow(`SELECT task.deleted_at IS NULL AND task.archived_at IS NULL
FROM task_archives archive JOIN tasks task ON task.id=archive.task_id WHERE archive.id=$1`, id).Scan(&restored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrTaskArchiveNotFound
	}
	return restored, err
}

func (d *DB) FailTaskArchiveJob(id int64, activeState string, cause error) error {
	failed := map[string]string{Archiving: ArchiveFailed, Restoring: RestoreFailed, Deleting: DeleteFailed}[activeState]
	if failed == "" {
		return fmt.Errorf("unknown active archive state %q", activeState)
	}
	message := "unknown archive failure"
	if cause != nil {
		message = cause.Error()
	}
	_, err := d.Exec(`UPDATE task_archives SET state=$2,phase='failed',error=$3 WHERE id=$1`, id, failed, message)
	return err
}

func queryArchiveRows(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, inner string, args ...any) (json.RawMessage, int64, error) {
	// SQLite has no row_to_json, and the `::text` that follows it is a
	// syntax error there on its own ("unrecognized token ':'"). Read the
	// inner query directly and assemble {"col": value} per row off
	// rows.Columns() -- same output shape as row_to_json, and the column
	// list follows the schema instead of being hand-listed.
	rows, err := q.Query(inner, args...)
	if err != nil {
		return nil, 0, err
	}
	objs, err := rowJSONObjects(rows)
	// A SQLite connection holds one active result set at a time, so Close
	// has to happen before the caller issues its next statement.
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, 0, err
	}
	raw, err := marshalSnapshot(objs)
	if err != nil {
		return nil, 0, err
	}
	return json.RawMessage(raw), int64(len(objs)), nil
}

func encodeArchiveRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) (json.RawMessage, int64, error) {
	var output bytes.Buffer
	output.WriteByte('[')
	var count int64
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		if count > 0 {
			output.WriteByte(',')
		}
		output.Write(raw)
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	output.WriteByte(']')
	return json.RawMessage(output.Bytes()), count, nil
}

func writeArchiveRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}, writer io.Writer) (int64, error) {
	var count int64
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		if written, err := writer.Write(raw); err != nil {
			return 0, err
		} else if written != len(raw) {
			return 0, io.ErrShortWrite
		}
		if written, err := io.WriteString(writer, "\n"); err != nil {
			return 0, err
		} else if written != 1 {
			return 0, io.ErrShortWrite
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func streamArchiveRows(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, writer io.Writer, inner string, args ...any) (int64, error) {
	return writeArchiveRowsFromQuery(q, writer, inner, args...)
}

// writeArchiveRowsFromQuery 把 inner 查询的每一行写成一个 JSON 对象。
func writeArchiveRowsFromQuery(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, writer io.Writer, inner string, args ...any) (int64, error) {
	rows, err := q.Query(inner, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	var count int64
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return 0, err
		}
		raw, err := marshalSnapshot(rowJSONObject(cols, vals))
		if err != nil {
			return 0, err
		}
		if written, err := writer.Write(raw); err != nil {
			return 0, err
		} else if written != len(raw) {
			return 0, io.ErrShortWrite
		}
		if written, err := io.WriteString(writer, "\n"); err != nil {
			return 0, err
		} else if written != 1 {
			return 0, io.ErrShortWrite
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func rawRowCount(raw json.RawMessage) int64 {
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil {
		return 0
	}
	return int64(len(rows))
}

func archiveAssetIDsQuery() string {
	// SQLite has no LATERAL, but it does accept a table-valued function
	// whose argument references a table earlier in the FROM clause -- a
	// comma join is enough, no LATERAL keyword. Verified against SQLite
	// 3.45: `FROM findings f, json_each(f.asset_ids) je` correlates fine.
	//
	// The json_valid guard is load-bearing rather than defensive: findings.asset_ids
	// is a plain TEXT column, and rowjson.go documents that a TEXT column cannot be
	// told apart from a JSON column by its declared type. An archive written from a
	// row where that column held non-JSON text would otherwise abort the whole
	// snapshot with "malformed JSON".
	return `SELECT id FROM assets WHERE ` + taskIDsAnyExpr("$1", "task_ids") + `
  UNION SELECT link.asset_id FROM task_asset_links link WHERE link.task_id=$1
  UNION SELECT anchor.asset_id FROM exploration_anchors anchor
  JOIN exploration_nodes node ON node.id=anchor.node_id WHERE node.exploration_id=$2
  UNION SELECT CAST(je.value AS INTEGER) FROM findings finding
  JOIN json_each(CASE WHEN json_valid(finding.asset_ids) THEN finding.asset_ids ELSE '[]' END) je
  WHERE finding.task_id=$1 AND regexp(je.value, '^[0-9]+$')`
}

// SnapshotTaskArchive reads one repeatable PostgreSQL snapshot. Task-owned Agent
// writes are already quiescent at the server barrier; repeatable-read also keeps
// the asset and accounting views mutually consistent during serialization.
func (d *DB) SnapshotTaskArchive(taskID int64) (*TaskArchiveSnapshot, error) {
	return d.snapshotTaskArchive(taskID, nil)
}

// SnapshotTaskArchiveWithLLMRecords streams the heavyweight record history to
// llmRecords while all other task-owned data is read from the same repeatable
// PostgreSQL snapshot.
func (d *DB) SnapshotTaskArchiveWithLLMRecords(taskID int64, llmRecords io.Writer) (*TaskArchiveSnapshot, error) {
	if llmRecords == nil {
		return nil, errors.New("nil LLM record archive writer")
	}
	return d.snapshotTaskArchive(taskID, llmRecords)
}

func (d *DB) snapshotTaskArchive(taskID int64, llmRecords io.Writer) (*TaskArchiveSnapshot, error) {
	tx, err := d.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return nil, err
	}
	var expID int64
	if err := tx.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&expID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskArchiveNotFound
		}
		return nil, err
	}
	tables := make(map[string]json.RawMessage)
	queries := []struct {
		name  string
		query string
		args  []any
	}{
		{"tasks", `SELECT * FROM tasks WHERE id=$1`, []any{taskID}},
		{"explorations", `SELECT * FROM explorations WHERE id=$1`, []any{expID}},
		{"exploration_nodes", `SELECT * FROM exploration_nodes WHERE exploration_id=$1 ORDER BY id`, []any{expID}},
		{"exploration_edges", `SELECT * FROM exploration_edges WHERE exploration_id=$1 ORDER BY src_id,dst_id`, []any{expID}},
		{"exploration_anchors", `SELECT anchor.* FROM exploration_anchors anchor JOIN exploration_nodes node ON node.id=anchor.node_id WHERE node.exploration_id=$1 ORDER BY node_id,asset_id`, []any{expID}},
		{"task_constraints", `SELECT * FROM task_constraints WHERE exploration_id=$1 ORDER BY id`, []any{expID}},
		{"activity", `SELECT * FROM activity WHERE exploration_id=$1 ORDER BY id`, []any{expID}},
		{"task_relations", `SELECT * FROM task_relations WHERE task_id=$1 ORDER BY created_at,source_task_id`, []any{taskID}},
		{"task_asset_links", `SELECT * FROM task_asset_links WHERE task_id=$1 ORDER BY asset_id`, []any{taskID}},
		{"task_llm_profiles", `SELECT * FROM task_llm_profiles WHERE task_id=$1 ORDER BY position`, []any{taskID}},
		{"task_scope", `SELECT * FROM task_scope WHERE task_id=$1 ORDER BY id`, []any{taskID}},
		{"findings", `SELECT * FROM findings WHERE task_id=$1 ORDER BY id`, []any{taskID}},
		{"finding_traffic_bindings", `SELECT b.* FROM finding_traffic_bindings b JOIN findings f ON f.id=b.finding_id WHERE f.task_id=$1 ORDER BY b.finding_id,b.position,b.id`, []any{taskID}},
		{"traffic_evidence_snapshots", `SELECT s.* FROM traffic_evidence_snapshots s WHERE EXISTS(SELECT 1 FROM finding_traffic_bindings b JOIN findings f ON f.id=b.finding_id WHERE b.snapshot_id=s.id AND f.task_id=$1) ORDER BY s.id`, []any{taskID}},
		{"llm_records", `SELECT * FROM llm_records WHERE COALESCE(task_id,'')=$1 ORDER BY id`, []any{strconv.FormatInt(taskID, 10)}},
		{"llm_usage", `SELECT * FROM llm_usage WHERE COALESCE(task_id,'')=$1 OR exploration_id=$2 ORDER BY id`, []any{strconv.FormatInt(taskID, 10), expID}},
		{"skill_usage", `SELECT * FROM skill_usage WHERE task_id=$1 OR exploration_id=$2 ORDER BY id`, []any{taskID, expID}},
		{"tool_usage", `SELECT * FROM tool_usage WHERE task_id=$1 OR exploration_id=$2 ORDER BY id`, []any{taskID, expID}},
		{"intercept_pending", `SELECT * FROM intercept_pending WHERE COALESCE(task_id,'')=$1 ORDER BY id`, []any{strconv.FormatInt(taskID, 10)}},
		{"side_question_sessions", `SELECT * FROM side_question_sessions WHERE task_id=$1 ORDER BY session_key`, []any{taskID}},
		{"side_question_requests", `SELECT r.* FROM side_question_requests r JOIN side_question_sessions s ON s.session_key=r.session_key WHERE s.task_id=$1 ORDER BY r.ordinal`, []any{taskID}},
		{"assets", `SELECT asset.* FROM assets asset WHERE asset.id IN (` + archiveAssetIDsQuery() + `) ORDER BY asset.id`, []any{taskID, expID}},
	}
	counts := make(map[string]int64, len(queries))
	streamedTables := map[string]string{}
	for _, query := range queries {
		if query.name == "llm_records" && llmRecords != nil {
			count, err := streamArchiveRows(tx, llmRecords, query.query, query.args...)
			if err != nil {
				return nil, fmt.Errorf("snapshot %s: %w", query.name, err)
			}
			tables[query.name] = json.RawMessage("[]")
			counts[query.name] = count
			streamedTables[query.name] = TaskArchiveLLMRecordsPath
			continue
		}
		raw, count, err := queryArchiveRows(tx, query.query, query.args...)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", query.name, err)
		}
		tables[query.name] = raw
		counts[query.name] = count
	}

	assetIDs, exclusiveAssetIDs, hosts, exclusiveHosts, err := archiveAssetMetadata(tx, taskID, expID, tables["assets"])
	if err != nil {
		return nil, err
	}
	_ = assetIDs // retained in the assets table payload; only exclusive ids need a side channel.
	var sources []int64
	rows, err := tx.Query(`SELECT source_task_id FROM task_relations WHERE task_id=$1 ORDER BY created_at,source_task_id`, taskID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	stats, err := taskArchiveAggregates(tx, taskID, expID)
	if err != nil {
		return nil, err
	}
	snapshot := &TaskArchiveSnapshot{
		FormatVersion: TaskArchiveFormatVersion, CreatedAt: time.Now().UTC(), TaskID: taskID,
		ExplorationID: expID, SourceTaskIDs: sources, Hosts: hosts, ExclusiveHosts: exclusiveHosts,
		ExclusiveAssetIDs: exclusiveAssetIDs, Tables: tables, StreamedTables: streamedTables,
		DataCounts: counts, AggregateStats: stats,
	}
	return snapshot, tx.Commit()
}

func archiveAssetMetadata(tx *sql.Tx, taskID, expID int64, assetRows json.RawMessage) ([]int64, []int64, []string, []string, error) {
	var rows []map[string]any
	if err := json.Unmarshal(assetRows, &rows); err != nil {
		return nil, nil, nil, nil, err
	}
	allHosts := map[string]struct{}{}
	assetIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		if id, ok := jsonInt64(row["id"]); ok {
			assetIDs = append(assetIDs, id)
		}
		for _, key := range []string{"domain", "ip"} {
			if value, _ := row[key].(string); strings.TrimSpace(value) != "" {
				allHosts[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
			}
		}
		if rawURL, _ := row["url"].(string); rawURL != "" {
			if parsed, err := url.Parse(rawURL); err == nil && parsed.Hostname() != "" {
				allHosts[strings.ToLower(parsed.Hostname())] = struct{}{}
			}
		}
	}
	exclusiveHosts, err := hostsForTaskDeletion(tx, taskID, expID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	rowsID, err := tx.Query(`WITH candidate AS (`+archiveAssetIDsQuery()+`)
SELECT asset.id FROM assets asset JOIN candidate ON candidate.id=asset.id
WHERE asset.company_id IS NULL
AND NOT EXISTS (SELECT 1 FROM tasks task WHERE task.id<>$1 AND task.deleted_at IS NULL AND `+taskIDsAnyExpr("task.id", "asset.task_ids")+`)
AND NOT EXISTS (
 SELECT 1 FROM exploration_anchors anchor JOIN exploration_nodes node ON node.id=anchor.node_id
 JOIN tasks task ON task.exploration_id=node.exploration_id
 WHERE anchor.asset_id=asset.id AND task.id<>$1 AND task.deleted_at IS NULL
) ORDER BY asset.id`, taskID, expID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var exclusiveIDs []int64
	for rowsID.Next() {
		var id int64
		if err := rowsID.Scan(&id); err != nil {
			rowsID.Close()
			return nil, nil, nil, nil, err
		}
		exclusiveIDs = append(exclusiveIDs, id)
	}
	if err := rowsID.Close(); err != nil {
		return nil, nil, nil, nil, err
	}
	hosts := make([]string, 0, len(allHosts))
	for host := range allHosts {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return assetIDs, exclusiveIDs, hosts, exclusiveHosts, nil
}

func taskArchiveAggregates(tx *sql.Tx, taskID, expID int64) (map[string]any, error) {
	stats := map[string]any{}
	var calls, input, output, cacheRead, cacheWrite int64
	if err := tx.QueryRow(`SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),
COALESCE(sum(cache_read),0),COALESCE(sum(cache_write),0)
FROM llm_usage WHERE COALESCE(task_id,'')=$1 OR exploration_id=$2`, strconv.FormatInt(taskID, 10), expID).
		Scan(&calls, &input, &output, &cacheRead, &cacheWrite); err != nil {
		return nil, err
	}
	stats["tokens"] = map[string]int64{"calls": calls, "input_tokens": input, "output_tokens": output, "cache_read_tokens": cacheRead, "cache_write_tokens": cacheWrite}
	for _, item := range []struct {
		name  string
		query string
		args  []any
	}{
		{name: "token_profiles", args: []any{strconv.FormatInt(taskID, 10), expID},
			query: `SELECT COALESCE(json_group_array(json_object(
 'profile_name',x.profile_name,'calls',x.calls,'tasks',x.tasks,
 'input_tokens',x.input_tokens,'output_tokens',x.output_tokens,
 'cache_read_tokens',x.cache_read_tokens,'cache_write_tokens',x.cache_write_tokens)),'[]') FROM (
SELECT COALESCE(profile_name,'') profile_name,count(*) calls,1 tasks,
       COALESCE(sum(input_tokens),0) input_tokens,COALESCE(sum(output_tokens),0) output_tokens,
       COALESCE(sum(cache_read),0) cache_read_tokens,COALESCE(sum(cache_write),0) cache_write_tokens
FROM llm_usage WHERE COALESCE(task_id,'')=$1 OR exploration_id=$2
GROUP BY profile_name ORDER BY sum(input_tokens)+sum(output_tokens) DESC) x`},
		{name: "token_daily", args: []any{strconv.FormatInt(taskID, 10), expID},
			// substr(ts,1,10) is the "YYYY-MM-DD" date prefix: SQLite stores ts as
			// "2006-01-02 15:04:05..." so the first ten bytes are exactly the UTC date.
			query: `SELECT COALESCE(json_group_array(json_object(
 'profile_name',x.profile_name,'date',x.date,
 'input_tokens',x.input_tokens,'output_tokens',x.output_tokens,
 'cache_read_tokens',x.cache_read_tokens)),'[]') FROM (
SELECT COALESCE(profile_name,'') profile_name,substr(ts,1,10) date,
       COALESCE(sum(input_tokens),0) input_tokens,COALESCE(sum(output_tokens),0) output_tokens,
       COALESCE(sum(cache_read),0) cache_read_tokens
FROM llm_usage WHERE COALESCE(task_id,'')=$1 OR exploration_id=$2
GROUP BY profile_name,date ORDER BY date) x`},
		{name: "skills", args: []any{taskID, expID},
			query: `SELECT COALESCE(json_group_object(name,n),'{}') FROM (SELECT skill name,count(*) n FROM skill_usage WHERE (task_id=$1 OR exploration_id=$2) AND found GROUP BY skill) x`},
		{name: "tools", args: []any{taskID, expID},
			query: `SELECT COALESCE(json_group_object(name,n),'{}') FROM (SELECT tool_key name,count(*) n FROM tool_usage WHERE task_id=$1 OR exploration_id=$2 GROUP BY tool_key) x`},
		{name: "findings", args: []any{taskID},
			query: `SELECT COALESCE(json_group_object(name,n),'{}') FROM (SELECT COALESCE(NULLIF(severity,''),'unknown') name,count(*) n FROM findings WHERE task_id=$1 GROUP BY severity) x`},
		{name: "skill_stats", args: []any{taskID, expID},
			// The distinct agent list is a correlated subquery rather than
			// json_group_array(DISTINCT ...): the DISTINCT-in-aggregate form is
			// only valid for single-argument json_group_array and arrived late in
			// SQLite's history, so nesting a plain DISTINCT subquery is the
			// shape that does not depend on the bundled SQLite version.
			// json() around the nested aggregate is not cosmetic. SQLite's JSON functions
			// quote any argument that is not itself already JSON, so a bare
			// json_group_array(...) passed to json_object lands as the *string*
			// "[\"agent-a\"]" instead of an array, and unmarshalling into []string fails.
			// Subquery materialization drops the JSON subtype, so the outer json_object
			// must wrap x.agents with json() again at the argument position — that is
			// what keeps the value inlined as an array instead of a quoted string.
			query: `SELECT COALESCE(json_group_array(json_object(
 'skill',x.skill,'calls',x.calls,'tasks',x.tasks,'agents',json(x.agents),'last_used',x.last_used)),'[]') FROM (
SELECT skill,count(*) calls,1 tasks,
       json((SELECT COALESCE(json_group_array(d.a),'[]') FROM (
          SELECT DISTINCT s2.agent_key a FROM skill_usage s2
          WHERE (s2.task_id=$1 OR s2.exploration_id=$2) AND s2.found
            AND s2.skill=skill_usage.skill AND s2.agent_key IS NOT NULL) d)) agents,
       replace(max(ts),' ','T') last_used
FROM skill_usage WHERE (task_id=$1 OR exploration_id=$2) AND found GROUP BY skill) x`},
		{name: "missing_skill_stats", args: []any{taskID, expID},
			query: `SELECT COALESCE(json_group_array(json_object(
 'skill',x.skill,'calls',x.calls,'tasks',x.tasks,'agents',json(x.agents),'last_used',x.last_used)),'[]') FROM (
SELECT skill,count(*) calls,0 tasks,
       json((SELECT COALESCE(json_group_array(d.a),'[]') FROM (
          SELECT DISTINCT s2.agent_key a FROM skill_usage s2
          WHERE (s2.task_id=$1 OR s2.exploration_id=$2) AND NOT s2.found
            AND s2.skill=skill_usage.skill AND s2.agent_key IS NOT NULL) d)) agents,
       replace(max(ts),' ','T') last_used
FROM skill_usage WHERE (task_id=$1 OR exploration_id=$2) AND NOT found GROUP BY skill) x`},
		{name: "finding_stats", args: []any{taskID},
			// count(*) FILTER (WHERE p) becomes SUM(CASE WHEN p THEN 1 ELSE 0 END).
			// COALESCE matters: with no matching rows SUM yields NULL, and
			// json_object would then emit a null instead of 0.
			query: `SELECT json_object(
 'total',count(*),
 'pending',COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),0),
 'critical',COALESCE(SUM(CASE WHEN severity='critical' THEN 1 ELSE 0 END),0),
 'high',COALESCE(SUM(CASE WHEN severity='high' THEN 1 ELSE 0 END),0),
 'medium',COALESCE(SUM(CASE WHEN severity='medium' THEN 1 ELSE 0 END),0),
 'low',COALESCE(SUM(CASE WHEN severity='low' THEN 1 ELSE 0 END),0),
 'vulnclasses',json(COALESCE((SELECT json_group_array(v) FROM (
    SELECT DISTINCT f2.vulnclass v FROM findings f2 WHERE f2.task_id=$1 AND f2.vulnclass<>'')),'[]')))
FROM findings WHERE task_id=$1`},
	} {
		// jsonValue rather than []byte: SQLite returns the JSON column as a
		// string, and database/sql's convertAssign only has a []byte source
		// branch for an exact *[]byte destination. json.RawMessage is a named
		// []byte type (jsontext.Value alias on Go 1.27), so it falls through
		// to reflection and fails. See db/sqlite_json_scan.go.
		var raw jsonValue
		if err := tx.QueryRow(item.query, item.args...).Scan(&raw); err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal(raw.RawMessage, &value); err != nil {
			return nil, err
		}
		stats[item.name] = value
	}
	return stats, nil
}

func jsonInt64(value any) (int64, bool) {
	switch value := value.(type) {
	case float64:
		return int64(value), value == float64(int64(value))
	case json.Number:
		id, err := value.Int64()
		return id, err == nil
	case string:
		id, err := strconv.ParseInt(value, 10, 64)
		return id, err == nil
	default:
		return 0, false
	}
}

package db

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CommandRecord is a paired tool_use + tool_result from the activity table
// (any tool, not just Bash). Command holds the raw tool input (JSON).
type CommandRecord struct {
	ID        int64     `json:"id"`
	ExpID     int64     `json:"exploration_id"`
	Worker    string    `json:"worker"`
	Tool      string    `json:"tool"`
	Command   string    `json:"command"`
	Output    string    `json:"output"`
	IsError   bool      `json:"is_error"`
	CreatedAt time.Time `json:"created_at"`
}

// commandFilter builds the WHERE clause shared by the tool-execution list and
// its per-tool tally, so the summary always describes exactly the rows the table
// pages through. Returns the clause, its args, and the next placeholder index.
func commandFilter(expID *int64, q string) (string, []any, int) {
	where := `WHERE u.kind = 'tool_use'`
	args := []any{}
	argN := 1

	if expID != nil {
		where += fmt.Sprintf(` AND u.exploration_id = $%d`, argN)
		args = append(args, *expID)
		argN++
	}
	if q != "" {
		where += fmt.Sprintf(` AND (u.tool `+sqlILike()+` $%d OR u.detail `+sqlILike()+` $%d)`, argN, argN)
		args = append(args, "%"+q+"%")
		argN++
	}
	return where, args, argN
}

// ToolStat is one tool's execution tally for the usage summary.
//
// ErrorClasses 把失败按 error_class 拆开（任务 27 之后新增「quota」一类）：
// 只看 errors 总数无法回答"Bash 错误率高是因为写法不对、环境缺依赖，还是额度打满"，
// 而这三个原因对应的修法完全不同。
type ToolStat struct {
	Tool         string         `json:"tool"`
	Total        int            `json:"total"`
	Errors       int            `json:"errors"`
	ErrorClasses map[string]int `json:"error_classes,omitempty"`
}

// ToolStats counts executions grouped by tool under the same filters
// ListCommands takes. Unpaginated on purpose: the tally describes the whole
// filtered set, not the page currently on screen.
//
// 数据源是 tool_usage 账本，不是 activity：activity 会随任务数据一起被清理，
// 而「哪个工具错误率高」恰恰是清理之后还要回答的问题（线上 47,374 次调用的
// 错误信息就是这么丢掉的）。账本刻意不存工具入参，所以 q 拆成两步：
// 先在 activity 里解析「入参命中 q 的工具名」，再回账本按工具名统计——
// 数字始终出自同一份账本，不会因为有没有关键字而换口径。
func (d *DB) ToolStats(expID *int64, q string) ([]ToolStat, error) {
	where := `WHERE 1=1`
	args := []any{}
	argN := 1
	if expID != nil {
		where += fmt.Sprintf(` AND exploration_id = $%d`, argN)
		args = append(args, *expID)
		argN++
	}
	if q != "" {
		names, err := d.toolNamesMatching(q)
		if err != nil {
			return nil, err
		}
		if len(names) == 0 {
			return []ToolStat{}, nil
		}
		ph := make([]string, len(names))
		for i, name := range names {
			ph[i] = fmt.Sprintf("$%d", argN)
			args = append(args, name)
			argN++
		}
		where += ` AND tool_key IN (` + strings.Join(ph, ",") + `)`
	}

	// 一次扫描同时拿到「按工具」和「按工具 × error_class」两维：账本可能有几十万行，
	// 拆成两条查询会让统计弹窗在大库上明显变慢。
	rows, err := d.Query(`
SELECT COALESCE(NULLIF(tool_key,''),'-') AS tool, COUNT(*) AS total,
       SUM(CASE WHEN is_error THEN 1 ELSE 0 END) AS errors,
       COALESCE(error_class,'') AS class
FROM tool_usage
`+where+`
GROUP BY 1, 4
ORDER BY tool ASC, class ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 同一工具的多行（不同 error_class）在 Go 侧合并，顺序沿用 SQL 的 total DESC。
	byTool := map[string]int{}
	out := []ToolStat{}
	for rows.Next() {
		var tool, class string
		var total, errors int
		if err := rows.Scan(&tool, &total, &errors, &class); err != nil {
			return nil, err
		}
		idx, ok := byTool[tool]
		if !ok {
			idx = len(out)
			byTool[tool] = idx
			out = append(out, ToolStat{Tool: tool})
		}
		out[idx].Total += total
		out[idx].Errors += errors
		// class 为空 = 没归因的错误 + 全部成功调用，不单列。
		if errors > 0 && class != "" {
			if out[idx].ErrorClasses == nil {
				out[idx].ErrorClasses = map[string]int{}
			}
			out[idx].ErrorClasses[class] += errors
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// SQL 已按工具名分组，这里按调用量重排（与旧行为一致：多的在前）。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Tool < out[j].Tool
	})
	return out, nil
}

// toolNamesMatching resolves a keyword to the tool names whose recorded input
// contains it. Only activity keeps tool inputs (the ledger deliberately does not),
// so this is the one place the old source still answers a question the ledger cannot.
func (d *DB) toolNamesMatching(q string) ([]string, error) {
	rows, err := d.Query(`SELECT DISTINCT tool FROM activity
WHERE kind='tool_use' AND COALESCE(detail,'') `+sqlILike()+` $1`, "%"+q+"%")
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

// ListCommands returns tool executions (tool_use + paired tool_result) across all
// explorations, with optional filtering and pagination. Covers every tool, not
// just Bash; q matches the tool name or its input.
func (d *DB) ListCommands(expID *int64, q string, page, size int) ([]CommandRecord, int, error) {
	if size <= 0 {
		size = 50
	}
	if page < 0 {
		page = 0
	}
	offset := page * size

	where, args, argN := commandFilter(expID, q)

	// count
	var total int
	countQ := `SELECT COUNT(*) FROM activity u ` + where
	if err := d.QueryRow(countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// data query: join tool_use with its tool_result
	dataQ := `
SELECT u.id, u.exploration_id, COALESCE(u.worker,''), COALESCE(u.tool,''), COALESCE(u.detail,''),
       COALESCE(r.detail,''), COALESCE(r.is_error, false), u.created_at
FROM activity u
LEFT JOIN activity r ON r.tool_use_id = u.tool_use_id AND r.kind = 'tool_result'
` + where + `
ORDER BY u.id DESC
LIMIT $` + fmt.Sprintf("%d", argN) + ` OFFSET $` + fmt.Sprintf("%d", argN+1)

	args = append(args, size, offset)
	rows, err := d.Query(dataQ, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []CommandRecord{}
	for rows.Next() {
		var c CommandRecord
		if err := rows.Scan(&c.ID, &c.ExpID, &c.Worker, &c.Tool, &c.Command, &c.Output, &c.IsError, &c.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// LLMRecord is one recorded LLM API call (request + response).
type LLMRecord struct {
	ID           int64     `json:"id"`
	Ts           time.Time `json:"ts"`
	Model        string    `json:"model"`
	ProfileName  string    `json:"profile_name"`
	SessionID    string    `json:"session_id"`
	TaskID       string    `json:"task_id"`
	Worker       string    `json:"worker"`
	LatencyMs    int       `json:"latency_ms"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CacheRead    int       `json:"cache_read"`
	CacheWrite   int       `json:"cache_write"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	// Cost is the read-time USD estimate computed by the API handler from
	// settings.price_table; db never writes it. 0 (omitempty) = 未配价格表或模型
	// 未命中，序列化时字段直接省略，不破坏存量消费者。
	Cost         float64 `json:"cost,omitempty"`
	RequestBody  string  `json:"request_body,omitempty"`
	ResponseBody string  `json:"response_body,omitempty"`
	// RawRequest / RawResponse are the untouched HTTP bodies exchanged with the
	// provider — the request as buildBody() sent it (full tool schemas included)
	// and the raw SSE frames. RequestBody/ResponseBody above are the normalized
	// view, which drops tool schemas and tool_use blocks entirely. Empty for
	// records written before this was added, or when the call never reached HTTP.
	RawRequest  string `json:"raw_request,omitempty"`
	RawResponse string `json:"raw_response,omitempty"`
}

const llmRecordsSchema = `
CREATE TABLE IF NOT EXISTS llm_records (
    id            @SERIAL@ PRIMARY KEY,
    ts            @TS@ DEFAULT @NOW@,
    model         TEXT,
    profile_name  TEXT,
    session_id    TEXT,
    task_id       TEXT,
    worker        TEXT,
    latency_ms    INTEGER,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    cache_read    INTEGER,
    cache_write   INTEGER,
    status        TEXT,
    error         TEXT,
    request_body  TEXT,
    response_body TEXT,
    raw_request   TEXT,
    raw_response  TEXT
);
CREATE INDEX IF NOT EXISTS idx_llm_records_ts ON llm_records(ts);
CREATE INDEX IF NOT EXISTS idx_llm_records_session ON llm_records(session_id);
`

// llmRecordsMigrate adds new columns to existing tables.
const llmRecordsMigrate = `
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS task_id TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS worker TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS profile_name TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS raw_request TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS raw_response TEXT;
`

// llmRecordsMigrateSQL 返回实际要执行的迁移语句。
//
// SQLite 没有 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`，这五条在那边一律是
// 语法错误。但它们在那边也没有存在理由：这五条补的列（task_id / worker /
// profile_name / raw_request / raw_response）已经全部写在 llmRecordsSchema 的
// 建表语句里，而 llm_records 在 SQLite 侧只由这一个建表语句创建，不存在"老表
// 缺列"的中间状态。与 coordinateWithSchemaMigration 在 SQLite 侧短路是同一条
// 论证。
func llmRecordsMigrateSQL() string {
	return ""
}

// EnsureLLMRecordsTable creates the llm_records table if it does not exist.
func (d *DB) EnsureLLMRecordsTable() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(pgTypeSQL(llmRecordsSchema)); err != nil {
		return err
	}
	// Empty on SQLite; an empty statement is a syntax error there, so skip it
	// rather than executing a no-op.
	if migrate := llmRecordsMigrateSQL(); migrate != "" {
		if _, err := tx.Exec(migrate); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// InsertLLMRecord stores one LLM call record.
func (d *DB) InsertLLMRecord(r *LLMRecord) error {
	_, err := d.Exec(`
INSERT INTO llm_records(model, profile_name, session_id, task_id, worker, latency_ms, input_tokens, output_tokens, cache_read, cache_write, status, error, request_body, response_body, raw_request, raw_response)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		r.Model, nullIfEmpty(r.ProfileName), r.SessionID, nullIfEmpty(r.TaskID), nullIfEmpty(r.Worker),
		r.LatencyMs, r.InputTokens, r.OutputTokens, r.CacheRead, r.CacheWrite,
		r.Status, nullIfEmpty(r.Error), nullIfEmpty(r.RequestBody), nullIfEmpty(r.ResponseBody),
		nullIfEmpty(r.RawRequest), nullIfEmpty(r.RawResponse))
	return err
}

// llmRecordsFilter builds the WHERE clause shared by the record list and its
// supplementary per-model aggregation, so 费用汇总与列表永远按同一组过滤条件
// 计算(两个查询必须同步改动，否则"全量费用"与屏幕上翻到的记录对不上)。
func llmRecordsFilter(model, session, task string) (where string, args []any, argN int) {
	where = `WHERE true`
	argN = 1
	if model != "" {
		where += fmt.Sprintf(` AND model = $%d`, argN)
		args = append(args, model)
		argN++
	}
	if session != "" {
		where += fmt.Sprintf(` AND session_id `+sqlILike()+` $%d`, argN)
		args = append(args, "%"+session+"%")
		argN++
	}
	if task != "" {
		where += fmt.Sprintf(` AND COALESCE(task_id,'') = $%d`, argN)
		args = append(args, task)
		argN++
	}
	return where, args, argN
}

// ListLLMRecords returns paginated LLM records with optional filters.
func (d *DB) ListLLMRecords(model, session, task string, page, size int) ([]LLMRecord, int, error) {
	if size <= 0 {
		size = 50
	}
	if page < 0 {
		page = 0
	}
	offset := page * size

	where, args, argN := llmRecordsFilter(model, session, task)

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM llm_records `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	dataQ := `SELECT id, ts, COALESCE(model,''), COALESCE(profile_name,''), COALESCE(session_id,''), COALESCE(task_id,''), COALESCE(worker,''),
		COALESCE(latency_ms,0), COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(cache_read,0), COALESCE(cache_write,0),
		COALESCE(status,''), COALESCE(error,'')
		FROM llm_records ` + where + ` ORDER BY id DESC LIMIT $` + fmt.Sprintf("%d", argN) + ` OFFSET $` + fmt.Sprintf("%d", argN+1)
	args = append(args, size, offset)

	rows, err := d.Query(dataQ, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []LLMRecord{}
	for rows.Next() {
		var r LLMRecord
		if err := rows.Scan(&r.ID, &r.Ts, &r.Model, &r.ProfileName, &r.SessionID, &r.TaskID, &r.Worker, &r.LatencyMs,
			&r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheWrite, &r.Status, &r.Error); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ModelTokenTotals is one model's summed token usage over a filtered llm_records
// set — the supplementary aggregation behind the read-time cost estimate: the list
// rows carry per-call cost, while this sums the WHOLE filtered set (not just the
// current page) so the page can show a 全量费用. db 层不含价格概念，Cost 一律由
// API 层按 settings.price_table 现算。
type ModelTokenTotals struct {
	Model        string
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int
}

// LLMRecordModelTotals aggregates the filtered llm_records set per model, under
// exactly the same filters as ListLLMRecords (shared llmRecordsFilter)。API 层仅在
// 配置了价格表时才调用(无价格表不算钱，省掉这次查询)。
func (d *DB) LLMRecordModelTotals(model, session, task string) ([]ModelTokenTotals, error) {
	where, args, _ := llmRecordsFilter(model, session, task)
	rows, err := d.Query(`SELECT COALESCE(NULLIF(model,''),'(unknown)') AS model,
		COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0)
	FROM llm_records `+where+`
	GROUP BY model`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelTokenTotals{}
	for rows.Next() {
		var m ModelTokenTotals
		if err := rows.Scan(&m.Model, &m.InputTokens, &m.OutputTokens, &m.CacheRead, &m.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// TaskModelTokenTotals is one (task_id, model) bucket of the recorded ledger —
// the supplementary dimension for the task picker's cost column (LLMTasks itself
// only carries task_id + count, no model, so it cannot be priced directly).
type TaskModelTokenTotals struct {
	TaskID       string
	Model        string
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int
}

// LLMTaskModelTotals aggregates every recorded task per model. API 层仅在配置了
// 价格表时调用；按 (task_id, model) 分组后逐组计价、在 Go 侧按 task 求和。
func (d *DB) LLMTaskModelTotals() ([]TaskModelTokenTotals, error) {
	rows, err := d.Query(`SELECT COALESCE(task_id,'') AS task_id, COALESCE(NULLIF(model,''),'(unknown)') AS model,
		COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0)
	FROM llm_records
	WHERE COALESCE(task_id,'') <> ''
	GROUP BY task_id, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskModelTokenTotals{}
	for rows.Next() {
		var t TaskModelTokenTotals
		if err := rows.Scan(&t.TaskID, &t.Model, &t.InputTokens, &t.OutputTokens, &t.CacheRead, &t.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ModelTokenStat is one model's aggregated token usage for a task, summed from the
// llm_usage metering ledger (see db/llm_usage.go). Calls is the number of LLM calls
// that hit this model.
type ModelTokenStat struct {
	Model            string `json:"model"`
	Calls            int    `json:"calls"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	// Cost is the read-time USD estimate (settings.price_table); 0 (omitempty) =
	// 未配价格表或模型未命中。
	Cost float64 `json:"cost,omitempty"`
}

// LLMTask is one distinct task with its LLM-record count.
type LLMTask struct {
	TaskID string `json:"task_id"`
	Count  int    `json:"count"`
	// Cost is the read-time USD estimate for this task's recorded calls
	// (settings.price_table); 0 (omitempty) = 未配价格表或模型全部未命中。
	Cost float64 `json:"cost,omitempty"`
}

// LLMTasks returns distinct non-empty task_ids with record counts, most recent
// first — powers the LLM-records page's task picker.
func (d *DB) LLMTasks() ([]LLMTask, error) {
	rows, err := d.Query(`SELECT task_id, COUNT(*) AS n FROM llm_records
WHERE COALESCE(task_id,'') <> '' GROUP BY task_id ORDER BY MAX(id) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMTask
	for rows.Next() {
		var t LLMTask
		if err := rows.Scan(&t.TaskID, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteLLMRecords removes every LLM record for one exact task_id — the same
// match the page's task picker/filter uses. Returns rows deleted.
func (d *DB) DeleteLLMRecords(task string) (int64, error) {
	res, err := d.Exec(`DELETE FROM llm_records WHERE COALESCE(task_id,'') = $1`, task)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetLLMRecord returns a single LLM record with full request/response bodies.
func (d *DB) GetLLMRecord(id int64) (*LLMRecord, error) {
	var r LLMRecord
	var reqBody, respBody, rawReq, rawResp sql.NullString
	err := d.QueryRow(`SELECT id, ts, COALESCE(model,''), COALESCE(profile_name,''), COALESCE(session_id,''), COALESCE(task_id,''), COALESCE(worker,''),
		COALESCE(latency_ms,0), COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(cache_read,0), COALESCE(cache_write,0),
		COALESCE(status,''), COALESCE(error,''), request_body, response_body, raw_request, raw_response
		FROM llm_records WHERE id=$1`, id).
		Scan(&r.ID, &r.Ts, &r.Model, &r.ProfileName, &r.SessionID, &r.TaskID, &r.Worker, &r.LatencyMs,
			&r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheWrite, &r.Status, &r.Error,
			&reqBody, &respBody, &rawReq, &rawResp)
	if err != nil {
		return nil, err
	}
	r.RequestBody = reqBody.String
	r.ResponseBody = respBody.String
	r.RawRequest = rawReq.String
	r.RawResponse = rawResp.String
	return &r, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

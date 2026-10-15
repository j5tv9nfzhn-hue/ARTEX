package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 归档回写的 SQLite 侧实现：替代 PostgreSQL 的 json_populate_record /
// json_populate_recordset。
//
// ── 为什么必须搬到 Go 侧 ────────────────────────────────────────────────────
//
// 归档包里每一张表是以「整行 JSON 数组」存的，回写时靠这两个函数把对象展开成
// 与表结构对齐的一行 INSERT：
//
//	INSERT INTO t SELECT * FROM json_populate_recordset(NULL::t, $1::json)
//
// `NULL::t` 是关键：它取的是表的**复合类型**，所以
//
//   - 列顺序 = 表定义顺序，因此 `SELECT *` 不用显式列名也不会错位；
//   - 归档里缺的列一律落 NULL，**绕过 SQL 默认值**。
//
// 第二条不是可有可无的细节。归档是历史快照：schema 之后新增的 NOT NULL 列在
// 快照里根本不存在，靠默认值兜底与靠 NULL 兜底是两种语义，调用点还专门为
// v3 归档补过 memory / context_info（见 insertArchiveRows 的注释）。
//
// SQLite 侧没有任何等价物，PRAGMA 也拿不到列的默认值表达式去复现这条语义。
// 于是只能在 Go 侧按「表定义顺序 + 缺列补 NULL」重建这条 INSERT。
//
// ── 为什么 PostgreSQL 分支保持原样 ──────────────────────────────────────────
//
// 本机没有可用的 PostgreSQL，所有 PG 分支都没有真机执行验证。PG 侧那段 SQL 是
// 现存且已知的正确实现，没有理由用一份新写的、同样没验证过的 Go 代码去替换它。
// 方言差异在这里的表现形式就是同一个函数里两个分支，与 companySQL 的惯例一致。

// archiveColumn 是回写目标表的一列。
type archiveColumn struct {
	Name string
	// Type 是声明类型，仅在 SQLite 侧用于识别时间列。
	Type string
}

// isArchiveTimestampColumn 判断声明类型是否是时间列。
//
// 靠声明类型而不是靠值形状来认：free text 列里出现 "2026-01-02T03:04:05Z"
// 是完全可能的，按值猜会把普通文本改写成另一种格式，静默损坏内容。
func isArchiveTimestampColumn(declared string) bool {
	switch strings.ToUpper(strings.TrimSpace(declared)) {
	case "TIMESTAMP", "DATETIME", "DATE":
		return true
	}
	return false
}

// archiveTableColumns 返回表的列，按表定义顺序排列。
//
// 用 pragma_table_info 这个表值函数而不是 PRAGMA table_info(t)：前者能把表名
// 作为绑定参数传进去，不必把表名拼进 SQL。表名虽然都来自 insertArchiveRows 的
// 白名单，但保持"表名永远不进 SQL 字符串"这条性质成本为零。
func archiveTableColumns(tx *sql.Tx, table string) ([]archiveColumn, error) {
	rows, err := tx.Query(`SELECT name, type FROM pragma_table_info($1) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []archiveColumn
	for rows.Next() {
		var c archiveColumn
		if err := rows.Scan(&c.Name, &c.Type); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("archive restore: table %q has no columns", table)
	}
	return cols, nil
}

// archiveTimeLayouts 是把归档里的时间文本还原成 time.Time 时依次尝试的布局。
//
// 归档的时间列是 RFC3339Nano（rowjson.jsonSafeValue 的 time.Time 分支）。
// 后几个布局不是多余的：早期归档、或本来就以 SQLite 存储格式落盘的列，值的
// 形状可能已经是驱动格式，解析失败时原样返回即可——不认识的形状宁可不动，
// 也不要猜一个格式改写过去。
var archiveTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// archiveBindValue 把归档 JSON 里的一个值转成可以直接绑定的 driver.Value。
//
// 四类转换，每一类都对应 jsonSafeValue 在导出方向上做过的事的逆运算：
//
//  1. json.Number → int64 / float64。decodeArchiveRows 开了 UseNumber，
//     json.Number 底层是 string，database/sql 的默认转换器不认它。
//  2. 嵌套的 map / slice → JSON 文本。导出时 jsonText 把 JSON 类型的列内联
//     成了 JSON 值，回写时 SQLite 要的是 TEXT，于是重新编码。
//     数组列（assets.task_ids 等）在 SQLite 侧存的正是 JSON 文本，
//     encodeTaskIDArray 的输出形态与这里一致。
//  3. 时间列 → time.Time。**不能**把 RFC3339 字符串原样绑进去：
//     同一列里其它行是 "2026-10-14 18:44:28.123+00:00"（驱动格式，空格分隔），
//     而 'T' 的字节是 0x54，空格是 0x20，于是归档恢复出来的时间戳会在排序里
//     整体排到同一秒的其它行之后，deadline_at / created_at 这类比较直接出错；
//     同时 _texttotime 按驱动格式解析，它也读不出来。
//     交给 time.Time 让驱动按 _time_format 渲染，是唯一能保证与其它行同形的做法。
//  4. 其余原样返回。
func archiveBindValue(col archiveColumn, v any) any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case map[string]any, []any:
		raw, err := marshalSnapshot(t)
		if err != nil {
			return nil
		}
		return string(raw)
	case string:
		if isArchiveTimestampColumn(col.Type) {
			if parsed, ok := parseArchiveTime(t); ok {
				return parsed
			}
		}
		return t
	default:
		return v
	}
}

// parseArchiveTime 尝试用已知的布局解析，失败返回 false 让调用方保留原值。
func parseArchiveTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range archiveTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// archiveTimeArg 是 restoreTaskArchive 里零散 SQL 用的时间参数转换：
// 空串 → nil（让 COALESCE 走 now() 分支），能解析 → time.Time，其余原样。
//
// 返回 time.Time 而不是格式化好的文本，原因与 archiveBindValue 的第 3 条
// 相同：交给驱动按 _time_format 渲染，才能与同一列里其它行保持同一种文本形态。
func archiveTimeArg(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if t, ok := parseArchiveTime(s); ok {
		return t
	}
	return s
}

// archiveInserter 把归档 JSON 行写回一张表。
//
// 逐行 INSERT 而不是拼一条多行 VALUES：一次绑定全部行会撞上 SQLite 的
// SQLITE_MAX_VARIABLE_NUMBER（旧版本 999），而 llm_records 的流式回写动辄几千行。
// 逐行还能把失败的行号带进错误信息——归档损坏时能直接定位到第几行。
type archiveInserter struct {
	table string
	cols  []archiveColumn
	stmt  *sql.Stmt
}

// newArchiveInserter 为 table 准备一条按列名显式赋值的 INSERT 语句。
//
// onConflict 非空时追加对应的冲突子句（task_scope 与 assets 需要 DO NOTHING）。
func newArchiveInserter(tx *sql.Tx, table, onConflict string) (*archiveInserter, error) {
	cols, err := archiveTableColumns(tx, table)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(cols))
	holders := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
		holders[i] = "$" + strconv.Itoa(i+1)
	}
	query := `INSERT INTO ` + table + ` (` + strings.Join(names, ",") + `) VALUES (` +
		strings.Join(holders, ",") + `)` + onConflict
	stmt, err := tx.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &archiveInserter{table: table, cols: cols, stmt: stmt}, nil
}

// Insert 写入一行。行里没有的列按 PG 的 json_populate_record 语义落 NULL。
func (in *archiveInserter) Insert(row map[string]any) error {
	args := make([]any, len(in.cols))
	for i, c := range in.cols {
		args[i] = archiveBindValue(c, row[c.Name])
	}
	_, err := in.stmt.Exec(args...)
	return err
}

// InsertAll 写入多行，第一行失败即返回并带上行号。
func (in *archiveInserter) InsertAll(rows []map[string]any) error {
	for i, row := range rows {
		if err := in.Insert(row); err != nil {
			return fmt.Errorf("restore %s row %d: %w", in.table, i, err)
		}
	}
	return nil
}

func (in *archiveInserter) Close() { in.stmt.Close() }

// insertArchiveRowMaps 是 insertArchiveRows 在 SQLite 侧的等价物。
func insertArchiveRowMaps(tx *sql.Tx, table string, rows []map[string]any, onConflict string) error {
	in, err := newArchiveInserter(tx, table, onConflict)
	if err != nil {
		return err
	}
	defer in.Close()
	return in.InsertAll(rows)
}

// archiveStubUpdate 组装一条「用归档 JSON 对象回填现有行」的 UPDATE。
//
// 存在的理由：PostgreSQL 侧原本靠
//
//	UPDATE explorations current SET description=archived.description, …
//	FROM json_populate_record(NULL::explorations,$2::json) archived
//	WHERE current.id=$1
//
// 完成这件事。SQLite 既没有 UPDATE…FROM，也没有把 JSON 展开成复合类型的函数，
// 所以改成在 Go 侧解码单个对象、逐列绑定——字段到字段的映射关系一字未变，
// 只是执行位置从数据库移到了调用方。
//
// cols 里的每一列按同名键从 row 取值；extra 是需要原样写进 SET 的 SQL 片段，
// 它引用到的占位符按出现顺序对应 extraArgs。两者都为空则不成立——cols 为空
// 且 extra 为空的 UPDATE 没有意义，直接报错而不是发出 `UPDATE t SET  WHERE`。
func archiveStubUpdate(tx *sql.Tx, table string, keyID int64, cols []archiveColumn, row map[string]any, extra string, extraArgs ...any) error {
	if len(cols) == 0 && extra == "" {
		return fmt.Errorf("archive restore: %s stub update has no assignments", table)
	}
	sets := make([]string, 0, len(cols)+1)
	args := make([]any, 0, len(cols)+len(extraArgs)+1)
	for _, c := range cols {
		sets = append(sets, c.Name+" = ?")
		args = append(args, archiveBindValue(c, row[c.Name]))
	}
	if extra != "" {
		sets = append(sets, extra)
		args = append(args, extraArgs...)
	}
	args = append(args, keyID)
	_, err := tx.Exec(`UPDATE `+table+` SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}

package db

import (
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/config"
)

// 本文件集中存放「同一条业务逻辑、两种 SQL 方言」的片段助手。
//
// 背景：项目正从 PostgreSQL 迁移到 SQLite，两边都还在用。迁移期最容易出的
// 三类 SQLite 报错都源于这里：
//
//  1. missing named argument "2::boolean" —— pgx 把参数位置上的 `$2::boolean`
//     整体当成一个名叫 `2::boolean` 的参数；SQLite 的 `$N`/`?` 是纯位置参数，
//     找不到这个名字就报缺参数。凡是占位符后面跟 `::type` 的，两边都要写成
//     光秃秃的 `$2`：值类型本来就由 Go 侧绑定决定，PG 不需要那个转换。
//  2. unrecognized token ":" / near "=" / near "ON" —— 语句里剩下的 `::type`
//     转换或 PG 专有语法。
//  3. near "FOR" —— SQLite 没有行锁，FOR UPDATE 只能按引擎去掉。
//
// 下面的助手一律返回「可以直接拼进语句的片段」，而不是返回整条语句，
// 这样调用点的语句骨架（列名、WHERE 条件、参数顺序）仍然两边共用一眼可读。
//
// 约定：PostgreSQL 分支保持原样，不做任何「顺便优化」。

// isPostgresEngine 报告当前是否走 PostgreSQL。
// 与 db.go 中 advisoryLock 的判断保持一致：只有显式的 postgres 引擎才启用
// PG 专有语法，其余一律按 SQLite 处理。
func isPostgresEngine() bool {
	return config.Engine() == config.EnginePostgres
}

// isSQLiteEngine 是 isPostgresEngine 的取反，语义同 advisoryLock。
func isSQLiteEngine() bool {
	return !isPostgresEngine()
}

// sqlForUpdate 返回行锁子句。
//
//	PG:     " FOR UPDATE"            / " FOR UPDATE OF t"
//	SQLite: ""                        （SQLite 没有行锁）
//
// 语义判断：这几处 FOR UPDATE 一律是「校验—写入」之间的防竞态，不是业务数据
// 的一部分。SQLite 侧由 advisoryLock（no-op，见 db.go）的同一条论证覆盖：
// 一个写事务期间整个库文件被独占，两次并发多表写入不会交错，追加行锁既没有
// 可模拟的对象，也会把本来正确的语句变成 `near "FOR"`。
//
// 残留风险（已在上报材料里记录）：SQLite 事务默认是 deferred 的，只有第一条
// 写语句才真正拿写锁。若某处调用点是「先 SELECT 再写」，理论上仍有窗口。
// 全局解法是给 DSN 加 `_txlock=immediate`（config 包，不在本任务范围内）。
func sqlForUpdate(of ...string) string {
	if isSQLiteEngine() {
		return ""
	}
	if len(of) == 0 {
		return " FOR UPDATE"
	}
	return " FOR UPDATE OF " + strings.Join(of, ",")
}

// sqlLockTableShareRowExclusive 返回 PG 的表级排他锁语句，SQLite 返回空串。
//
// SHARE ROW EXCLUSIVE 用来把「读一批资产归属 → 改归属」这段窗口对并发的
// INSERT/UPDATE/DELETE 关掉。SQLite 的 WAL 写事务本来就是整库写锁，
// 覆盖范围严格大于 SHARE ROW EXCLUSIVE，所以直接不发这条语句。
func sqlLockTableShareRowExclusive(tables ...string) string {
	if isSQLiteEngine() || len(tables) == 0 {
		return ""
	}
	return "LOCK TABLE " + strings.Join(tables, ", ") + " IN SHARE ROW EXCLUSIVE MODE"
}

// sqlSkipLocked 返回抢占队列时的跳过已锁行子句。
//
// 仅用于「挑一行出来」的读语句；调用点必须保证随后在同一事务里把这行改成
// 占用态，否则跳锁就失去了意义。写入类抢占请用单条 UPDATE ... WHERE id =
// (SELECT ... LIMIT 1)，不要用这里的片段。
func sqlSkipLocked() string {
	if isSQLiteEngine() {
		return ""
	}
	return " SKIP LOCKED"
}

// sqlILike 返回大小写不敏感的模糊匹配运算符。
//
// SQLite 的 LIKE 对 ASCII 本来就大小写不敏感（除非显式开 PRAGMA
// case_sensitive_like），与 PG 的 ILIKE 在本项目用到的字段（全小写的任务名、
// 任务描述、任务 ID 数字串）上等价，因此这里不需要额外包一层 lower()。
func sqlILike() string {
	if isSQLiteEngine() {
		return "LIKE"
	}
	return "ILIKE"
}

// sqlNotILike 返回否定模糊匹配的运算符，与 sqlILike 成对。
//
// 单独一个函数而不是让调用点写 "NOT "+sqlILike()：NOT 两侧的空格在调用点
// 很容易漏，而漏掉之后 `NOTLIKE` 是合法 token 却不是任何引擎认识的运算符
// ——编译通过、PG 直接报错。
func sqlNotILike() string {
	if isSQLiteEngine() {
		return "NOT LIKE"
	}
	return "NOT ILIKE"
}

// sqlCastText 返回把列转成文本的写法。
//
// 用途只有一个：让数值列参与字符串比较（任务 ID 的模糊搜索）。
// PG 侧保留 `col::text` 的原样，SQLite 侧必须写成 CAST(col AS TEXT)，
// 裸的 `col::text` 会被 SQLite 报 unrecognized token ":"。
func sqlCastText(col string) string {
	if isSQLiteEngine() {
		return "CAST(" + col + " AS TEXT)"
	}
	return col + "::text"
}

// sqlNowPlusSeconds 返回「当前时间 + n 秒」的表达式，n 由占位符 arg 给出。
//
// PG:      now() + make_interval(secs => $n)
// SQLite:  strftime('%Y-%m-%d %H:%M:%f+00:00','now','+'||$n||' seconds')
//
// SQLite 侧刻意不用 datetime('now')：列默认时间是 strftime 的
// '%Y-%m-%d %H:%M:%f+00:00' 格式，datetime() 少毫秒少时区，同一列里混两种
// 文本格式会直接破坏排序（与 sqlite_compat.go 里 now() 的取舍同源）。
// make_interval 的 `secs => $n::double precision` 命名参数也是 PG 专有写法，
// SQLite 侧只能走字符串拼接。
func sqlNowPlusSeconds(arg string) string {
	if isSQLiteEngine() {
		return "strftime('%Y-%m-%d %H:%M:%f+00:00','now','+'||" + arg + "||' seconds')"
	}
	return "now() + make_interval(secs => " + arg + ")"
}

// sqlArrayRemove 返回「从 <col> 的数组里删掉 <expr>」的方言形式。
//
//	PostgreSQL: array_remove(col, expr)
//	SQLite:     artex_json_remove(col, expr)
//
// SQLite 侧存的是 JSON 数组文本而不是 PG 的 {1,2,3} 字面量，所以函数名与
// 参数顺序都换了；语义（删掉所有等于该值的元素）由该 UDF 保证。
func sqlArrayRemove(col, expr string) string {
	if isSQLiteEngine() {
		return "artex_json_remove(" + col + ", " + expr + ")"
	}
	return "array_remove(" + col + ", " + expr + ")"
}

// sqlArrayAddIfMissing 返回「<col> 已含 <expr> 则原样，否则追加」的表达式。
//
//	PostgreSQL: CASE WHEN expr=ANY(col) THEN col ELSE array_append(col, expr) END
//	SQLite:     CASE WHEN artex_json_contains(col, expr) THEN col
//	                   ELSE artex_json_add(col, expr) END
//
// 为什么要带这个 CASE 而不直接追加：调用点是在恢复归档时把任务重新挂到资产上，
// 同一个任务可能已经被别的恢复路径挂过一次，无条件追加就会产生重复成员。
func sqlArrayAddIfMissing(col, expr string) string {
	if isSQLiteEngine() {
		return "CASE WHEN artex_json_contains(" + col + ", " + expr + ") THEN " + col +
			" ELSE artex_json_add(" + col + ", " + expr + ") END"
	}
	return "CASE WHEN " + expr + "=ANY(" + col + ") THEN " + col +
		" ELSE array_append(" + col + ", " + expr + ") END"
}

// sqlNextSequenceValue 返回取下一个自增序列值的表达式。
//
//	PostgreSQL: nextval(pg_get_serial_sequence('assets','id'))
//	SQLite:     (SELECT seq FROM sqlite_sequence WHERE name='assets')
//
// 这里不能用 max(id)+1 作为首选：并发下两个插入会拿到同一个值。SQLite 侧
// sqlite_sequence 由 AUTOINCREMENT 维护，读它即可，语义与 nextval 一致。
// 外层 COALESCE 是因为 sqlite_sequence 只在该表**首次插入之后**才有行；归档
// 恢复走到这里时表里必然已有行（否则 rowExists 为假），但兜底比"依赖调用点
// 的隐含前提"便宜。
func sqlNextSequenceValue(table string) string {
	if isSQLiteEngine() {
		return "(SELECT COALESCE((SELECT seq + 1 FROM sqlite_sequence WHERE name = '" + table + "')," +
			"(SELECT COALESCE(MAX(id), 0) + 1 FROM " + table + ")))"
	}
	return "nextval(pg_get_serial_sequence('" + table + "','id'))"
}

// sqlNullableTimestampArg 返回「该参数可空、且是时间」的表达式。
//
//	PostgreSQL: $n::timestamptz
//	SQLite:     $n
//
// SQLite 侧的裸 $n 不是省事：调用点此时绑的是 Go 的 time.Time 或 nil，值类型
// 本就确定了，加一个 SQLite 不认识的 `::timestamptz` 只会得到 unrecognized
// token ":"。PG 侧必须留着转换——pgx 靠它把参数类型定成 timestamptz，否则
// COALESCE 的两个分支类型不一致会直接报错。
func sqlNullableTimestampArg(arg string) string {
	if isSQLiteEngine() {
		return arg
	}
	return arg + "::timestamptz"
}

// sqlCoalesceNow 返回 COALESCE(<arg>, now())。
//
// 与 sqlNullableTimestampArg 同理：PG 保留 `::timestamptz` 以便 pgx 推断类型，
// SQLite 侧去掉。调用点绑 nil 时走 now() 分支，绑 time.Time 时走参数分支。
func sqlCoalesceNow(arg string) string {
	return "COALESCE(" + sqlNullableTimestampArg(arg) + ", now())"
}

// pgTypeSQL 把 Go 里拼装的 DDL 中的占位符换成当前引擎的写法。
//
//	@SERIAL@  BIGSERIAL  /  INTEGER
//	@TS@      TIMESTAMPTZ /  TIMESTAMP
//	@NOW@     now()      /  (strftime('%Y-%m-%d %H:%M:%f+00:00','now'))
//
// 存在的理由是这条路径上的三件事都不是「换个类型名」那么简单：
//
//  1. TIMESTAMPTZ 在 SQLite 里不合法，直接 unrecognized token ":"。
//  2. BIGSERIAL 在 SQLite 里虽然能被当成任意类型名接受，但列就不是
//     INTEGER PRIMARY KEY，拿不到 rowid 别名，主键不会自增。
//  3. **SQLite 的列默认值不允许裸函数调用**，`DEFAULT now()` 直接
//     near "("。必须是括号包裹的 (strftime(...))，这也是
//     db/schema_sqlite.sql 里其余表默认时间的写法，两边存下来的文本形态
//     因此一致——同一列里混两种时间格式会直接破坏排序。
func pgTypeSQL(ddl string) string {
	serial, ts, now := "BIGSERIAL", "TIMESTAMPTZ", "now()"
	if isSQLiteEngine() {
		serial, ts = "INTEGER", "TIMESTAMP"
		now = `(strftime('%Y-%m-%d %H:%M:%f+00:00','now'))`
	}
	return strings.NewReplacer("@SERIAL@", serial, "@TS@", ts, "@NOW@", now).Replace(ddl)
}

// sqlDayStart 返回「把时间列截断到当天 00:00:00」的表达式。
//
//	PostgreSQL: date_trunc('day', X)
//	SQLite:     strftime('%Y-%m-%d 00:00:00+00:00', X)
//
// 与 sqlUTCDate 一样依赖 DSN 的 _time_format=sqlite：那个格式以空格分隔、
// 带数字时区后缀，而 SQLite 的 strftime 只按 POSIX 解析日期部分，多余的
// " 12:34:56+00:00" 会被忽略——对截断到天这个用途恰好无害，但若哪天改掉那个
// DSN 参数，这两处会一起开始出错。
func sqlDayStart(col string) string {
	if isSQLiteEngine() {
		return "strftime('%Y-%m-%d 00:00:00+00:00', " + col + ")"
	}
	return "date_trunc('day', " + col + ")"
}

// sqlEpochInt 返回「时间表达式 -> unix 秒（整型）」的写法。
//
//	PostgreSQL: EXTRACT(EPOCH FROM expr)::bigint
//	SQLite:     CAST(strftime('%s', expr) AS INTEGER)
//
// SQLite 没有 EXTRACT(EPOCH FROM ...)；strftime('%s', ...) 返回字符串形态的
// unix 秒，必须显式 CAST 才能被 Scan 进 int64。与 sqlDayStart 同样依赖 DSN 的
// _time_format=sqlite 时间文本形态：strftime 按 POSIX 解析，时区后缀换算成
// UTC 秒，与 Go 的 time.Unix() 对齐。
func sqlEpochInt(expr string) string {
	if isSQLiteEngine() {
		return "CAST(strftime('%s', " + expr + ") AS INTEGER)"
	}
	return "EXTRACT(EPOCH FROM " + expr + ")::bigint"
}

// sqlInt64ArrayArg 返回「可以绑给 id 数组列」的值。
//
//	PostgreSQL: 原始 []int64，交给 pgx 转成 int8[]
//	SQLite:     JSON 数组文本 "[1,2,3]"
//
// SQLite 驱动不认切片——直接绑 []int64 会得到
// "sql: converting argument $N type: unsupported type []int64"。
//
// 编码成 JSON 而不是 PG 的 "{1,2,3}" 字面量，是因为 task_archives.source_task_ids
// 在 SQLite 侧本来就按 JSON 数组文本存：读侧 taskIDsAnyExpr 用
// artex_json_contains 遍历元素。写入格式必须与读侧配对，写成 PG 字面量的话
// 依赖检查会静默失效——artex_json_contains 对非 JSON 文本返回假，任务因此
// 永远可以被归档，尽管它实际上被别的归档依赖着。
func sqlInt64ArrayArg(ids []int64) any {
	if !isSQLiteEngine() {
		return ids
	}
	if len(ids) == 0 {
		return "[]"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

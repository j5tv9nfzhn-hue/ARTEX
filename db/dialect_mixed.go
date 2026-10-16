package db

import (
	"strconv"
	"strings"
)

// 本文件集中存放「可以直接拼进语句的 SQL 片段」助手。
//
// 这些助手原本是 PostgreSQL / SQLite 双分支，每个都带着一段「为什么两边不一样」
// 的论证。项目收敛到 SQLite 之后，分叉消失了，但助手本身保留：调用点因此仍然是
// `col = ANY(...)` 之类的语义表达，而不是散落各处的字面 SQL 片段。每条的注释保留
// 了原 PostgreSQL 写法，作为「这条语句想表达什么」的规范说明。

// sqlILike 返回大小写不敏感的模糊匹配运算符。
//
// SQLite 的 LIKE 对 ASCII 本来就大小写不敏感（除非显式开 PRAGMA
// case_sensitive_like），与本项目用到的字段（全小写的任务名、任务描述、任务 ID
// 数字串）上等价于 PostgreSQL 的 ILIKE，因此不需要额外包一层 lower()。
func sqlILike() string { return "LIKE" }

// sqlNotILike 返回否定模糊匹配的运算符，与 sqlILike 成对。
//
// 单独一个函数而不是让调用点写 "NOT " + sqlILike()：NOT 两侧的空格在调用点很容易
// 漏，而漏掉之后 `NOTLIKE` 是合法 token 却不是任何引擎认识的运算符——编译通过、
// 运行时报错。
func sqlNotILike() string { return "NOT LIKE" }

// sqlCastText 返回把列转成文本的写法。
//
// 用途只有一个：让数值列参与字符串比较（任务 ID 的模糊搜索）。PostgreSQL 的
// `col::text` 在 SQLite 里会报 unrecognized token ":"，必须写 CAST(col AS TEXT)。
func sqlCastText(col string) string { return "CAST(" + col + " AS TEXT)" }

// sqlNowPlusSeconds 返回「当前时间 + n 秒」的表达式，n 由占位符 arg 给出。
//
//	PostgreSQL: now() + make_interval(secs => $n)
//	本实现:     strftime('%Y-%m-%d %H:%M:%f+00:00','now','+'||$n||' seconds')
//
// 刻意不用 datetime('now')：列默认时间是 strftime 的 '%Y-%m-%d %H:%M:%f+00:00'
// 格式，datetime() 少毫秒少时区，同一列里混两种文本格式会直接破坏排序（与
// sqlite_compat.go 里 now() 的取舍同源）。
func sqlNowPlusSeconds(arg string) string {
	return "strftime('%Y-%m-%d %H:%M:%f+00:00','now','+'||" + arg + "||' seconds')"
}

// sqlArrayRemove 返回「从 <col> 的数组里删掉 <expr>」的写法。
//
// PostgreSQL 用 array_remove(col, expr)；SQLite 侧存的是 JSON 数组文本而不是 PG 的
// {1,2,3} 字面量，所以换成 artex_json_remove UDF，语义（删掉所有等于该值的元素）
// 由该 UDF 保证。
func sqlArrayRemove(col, expr string) string {
	return "artex_json_remove(" + col + ", " + expr + ")"
}

// sqlArrayAddIfMissing 返回「<col> 已含 <expr> 则原样，否则追加」的表达式。
//
// 为什么要带这个 CASE 而不直接追加：调用点是在恢复归档时把任务重新挂到资产上，
// 同一个任务可能已经被别的恢复路径挂过一次，无条件追加就会产生重复成员。
func sqlArrayAddIfMissing(col, expr string) string {
	return "CASE WHEN artex_json_contains(" + col + ", " + expr + ") THEN " + col +
		" ELSE artex_json_add(" + col + ", " + expr + ") END"
}

// sqlNextSequenceValue 返回取下一个自增序列值的表达式。
//
// PostgreSQL 用 nextval(pg_get_serial_sequence(table,'id'))。这里不能用 max(id)+1
// 作为首选：并发下两个插入会拿到同一个值。sqlite_sequence 由 AUTOINCREMENT 维护，
// 读它即可，语义与 nextval 一致。外层 COALESCE 是因为 sqlite_sequence 只在该表
// **首次插入之后**才有行；归档恢复走到这里时表里必然已有行（否则 rowExists 为假），
// 但兜底比"依赖调用点的隐含前提"便宜。
func sqlNextSequenceValue(table string) string {
	return "(SELECT COALESCE((SELECT seq + 1 FROM sqlite_sequence WHERE name = '" + table + "')," +
		"(SELECT COALESCE(MAX(id), 0) + 1 FROM " + table + ")))"
}

// sqlCoalesceNow 返回 COALESCE(<arg>, now())。
//
// PostgreSQL 侧原本要写 `$n::timestamptz` 让 pgx 把参数类型定成 timestamptz；
// SQLite 侧的裸 $n 就够了，调用点此时绑的是 Go 的 time.Time 或 nil，值类型本就确定，
// 加一个 SQLite 不认识的 `::timestamptz` 只会得到 unrecognized token ":"。
// 调用点绑 nil 时走 now() 分支，绑 time.Time 时走参数分支。
func sqlCoalesceNow(arg string) string {
	return "COALESCE(" + arg + ", now())"
}

// pgTypeSQL 把 Go 里拼装的 DDL 中的占位符换成 SQLite 写法。
//
//	@SERIAL@  BIGSERIAL   -> INTEGER
//	@TS@      TIMESTAMPTZ -> TIMESTAMP
//	@NOW@     now()       -> (strftime('%Y-%m-%d %H:%M:%f+00:00','now'))
//
// 存在的理由是这条路径上的三件事都不是「换个类型名」那么简单：
//
//  1. TIMESTAMPTZ 在 SQLite 里不合法，直接 unrecognized token ":"。
//  2. BIGSERIAL 在 SQLite 里虽然能被当成任意类型名接受，但列就不是
//     INTEGER PRIMARY KEY，拿不到 rowid 别名，主键不会自增。
//  3. **SQLite 的列默认值不允许裸函数调用**，`DEFAULT now()` 直接 near "("。
//     必须是括号包裹的 (strftime(...))，这也是 db/schema_sqlite.sql 里其余表默认
//     时间的写法，两边存下来的文本形态因此一致——同一列里混两种时间格式会直接
//     破坏排序。
func pgTypeSQL(ddl string) string {
	return strings.NewReplacer(
		"@SERIAL@", "INTEGER",
		"@TS@", "TIMESTAMP",
		"@NOW@", `(strftime('%Y-%m-%d %H:%M:%f+00:00','now'))`,
	).Replace(ddl)
}

// sqlDayStart 返回「把时间列截断到当天 00:00:00」的表达式。
//
// PostgreSQL 是 date_trunc('day', X)。这里与 sqlUTCDate 一样依赖 DSN 的
// _time_format=sqlite：那个格式以空格分隔、带数字时区后缀，而 SQLite 的 strftime
// 只按 POSIX 解析日期部分，多余的 " 12:34:56+00:00" 会被忽略——对截断到天这个用途
// 恰好无害，但若哪天改掉那个 DSN 参数，这两处会一起开始出错。
func sqlDayStart(col string) string {
	return "strftime('%Y-%m-%d 00:00:00+00:00', " + col + ")"
}

// sqlEpochInt 返回「时间表达式 -> unix 秒（整型）」的写法。
//
// PostgreSQL 是 EXTRACT(EPOCH FROM expr)::bigint；SQLite 没有 EXTRACT，而
// strftime('%s', ...) 返回字符串形态的 unix 秒，必须显式 CAST 才能被 Scan 进
// int64。与 sqlDayStart 同样依赖 DSN 的 _time_format=sqlite 时间文本形态：
// strftime 按 POSIX 解析，时区后缀换算成 UTC 秒，与 Go 的 time.Unix() 对齐。
func sqlEpochInt(expr string) string {
	return "CAST(strftime('%s', " + expr + ") AS INTEGER)"
}

// sqlNowMinusDays 返回「当前时间 - n 天」的表达式，n 由占位符 arg 给出。
//
// 调用点原是 PostgreSQL 专有的 `now() - ($1 * interval '1 day')`，这里沿用
// sqlNowPlusSeconds 的 strftime 形状（与列默认值同形，字典序即时间序）。
func sqlNowMinusDays(arg string) string {
	return "strftime('%Y-%m-%d %H:%M:%f+00:00','now','-'||" + arg + "||' days')"
}

// sqlNotBeforeExpr 是 notification_delivery.go sqlNotAfterExpr 的对称版：
// 「时间戳表达式 >= 某个时间表达式」。显式 CAST 左操作数为 TEXT，让比较退回字典序
// （同一时间文本形状下与时间比较等价），理由同 sqlNotAfterExpr。
func sqlNotBeforeExpr(col, tsExpr string) string {
	return "CAST(" + col + " AS TEXT) >= " + tsExpr
}

// sqlInt64ArrayArg 返回「可以绑给 id 数组列」的值。
//
// PostgreSQL 侧直接交原始 []int64 让 pgx 转成 int8[]；SQLite 驱动不认切片——直接绑
// []int64 会得到 "sql: converting argument $N type: unsupported type []int64"。
//
// 编码成 JSON 而不是 PG 的 "{1,2,3}" 字面量，是因为 task_archives.source_task_ids
// 在 SQLite 侧本来就按 JSON 数组文本存：读侧 taskIDsAnyExpr 用 artex_json_contains
// 遍历元素。写入格式必须与读侧配对，写成 PG 字面量的话依赖检查会静默失效——
// artex_json_contains 对非 JSON 文本返回假，任务因此永远可以被归档，尽管它实际上
// 被别的归档依赖着。
func sqlInt64ArrayArg(ids []int64) any {
	if len(ids) == 0 {
		return "[]"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// sqlForUpdate 返回行锁子句，SQLite 侧为空串。
//
// 调用点的 FOR UPDATE 一律是「校验—写入」之间的防竞态，不是业务数据的一部分。
// SQLite 没有行锁：一个写事务期间整个库文件被独占（DSN 的 _txlock=immediate），
// 两次并发多表写入不会交错，这正是行锁要提供的保证，而且是更强的保证（粒度更粗）。
// 追加行锁既没有可模拟的对象，也会把本来正确的语句变成 `near "FOR"`。
func sqlForUpdate(...string) string { return "" }

// sqlLockTableShareRowExclusive 返回 PostgreSQL 的表级排他锁语句，SQLite 侧为空串。
//
// SHARE ROW EXCLUSIVE 用来把「读一批资产归属 → 改归属」这段窗口对并发的
// INSERT/UPDATE/DELETE 关掉。SQLite 的 WAL 写事务本来就是整库写锁，覆盖范围严格
// 大于 SHARE ROW EXCLUSIVE，所以直接不发这条语句。
func sqlLockTableShareRowExclusive(...string) string { return "" }

// sqlSkipLocked 返回抢占队列时的跳过已锁行子句，SQLite 侧为空串。
//
// SQLite 的队列抢占不是「挑一行再改」，而是单条 UPDATE ... WHERE id IN (SELECT …
// LIMIT n) RETURNING：领取和占用标记在同一条语句里原子完成，因此没有 SKIP LOCKED
// 要表达的语义。详见 sqliteClaimUpdate。
func sqlSkipLocked() string { return "" }

// sqlNullableTimestampArg 返回「该参数可空、且是时间」的表达式。
//
// PostgreSQL 需要 `$n::timestamptz` 让 pgx 把参数类型定成 timestamptz，否则
// COALESCE 的两个分支类型不一致会直接报错；SQLite 侧的裸 $n 就够了，调用点此时绑的
// 是 Go 的 time.Time 或 nil，值类型本就确定，加一个 SQLite 不认识的 `::timestamptz`
// 只会得到 unrecognized token ":"。
func sqlNullableTimestampArg(arg string) string { return arg }

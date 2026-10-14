package db

import (
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

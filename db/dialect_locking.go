package db

import (
	"database/sql"
	"fmt"
)

// 本文件集中存放「行锁 / 队列抢占」这一类按引擎分叉的 SQL 片段，与 dialect_mixed.go
// 里的其它方言助手并列。之所以单列，是因为这类片段**不能只看语法差异**，还要逐处
// 判断原代码到底拿锁在防什么——判错语义比写错语法更危险。
//
// ── 为什么 SQLite 不需要 FOR UPDATE ───────────────────────────────────────────
//
// PostgreSQL 的行锁把「读出来 → 判断 → 更新」这段变成原子的：并发事务要么看到旧值、
// 要么被挡在锁外直到本事务提交。SQLite 根本没有行锁这个概念，写上去只会得到
// `SQL logic error: near "FOR": syntax error`。
//
// 本项目里所有 FOR UPDATE 的用途只有两种，去掉它在 SQLite 上都是安全的：
//
//  1. 防竞态：读出一行、按读到的值决定怎么改（校验状态、算下一跳）。
//     WAL 模式下任意时刻只有一个写事务，它从第一条写语句起持有库级写锁直到提交，
//     两个并发写事务无法交错——这正是行锁要提供的保证，而且是更强的保证（粒度更粗）。
//     见 db.go 里 advisoryLock 在 SQLite 下为 no-op 的同一条论证。
//
//  2. 队列抢占：SKIP LOCKED 让每个并发领取者各领到未被占用的行，而不是排队等锁。
//     这一类**不能简单删子句了事**：删掉之后两个领取者会读到同一批 pending 行并各自
//     尝试领取，领取标记（lease / fanned_out）写在各自事务里，第二个事务提交时才发现
//     该行已被占——但它已经把重复的投递建出来了。所以 SQLite 侧改用
//     「更新即领取」：一条 UPDATE ... WHERE id IN (SELECT ... LIMIT n) RETURNING ...，
//     领取和占用标记在同一条语句里原子完成（见 sqliteClaimUpdate）。
//
// ── 为什么「更新即领取」不会变成阻塞等待 / 死锁 ──────────────────────────────
//
// 这条 UPDATE 是事务里的**第一条语句**，SQLite 在执行它的一开始就取写锁（而不是等到
// 第一次写才升级），因此并发的第二个领取者只会在 busy_timeout（config.SQLiteBusyTimeout）
// 上等待，等第一个领取者提交；提交之后它的子查询重新求值，看到的已经是占用态的行，
// 于是自然地领到下一批或领不到。
//
// 换句话说：不是「等锁 → 抢同一行」，而是「等写锁 → 在新快照上重新挑未被占用的行」。
// 与 FOR UPDATE SKIP LOCKED 的可观察结果一致（不重复领、不阻塞整队），差别只在并发度
// 低一档——而 SQLite 本来就只能这样。
//
// 附带的好处：把写锁提到读之前，顺带绕开了 SQLite 延迟事务的经典坑——
// 「先 SELECT 拿到读锁、再写」升级锁失败时，SQLite 为了避免死锁会**直接**返回
// SQLITE_BUSY 且不触发 busy handler 重试（全局解法是给 DSN 加 `_txlock=immediate`，
// 见 db/open_sqlite.go 与 config.SQLiteDSN）。

// sqliteClaimUpdate 组装 SQLite 侧的「更新即领取」语句，用来替代
// SELECT ... FOR UPDATE SKIP LOCKED + UPDATE 两步走：
//
//	UPDATE <table> SET <setSQL>
//	WHERE id IN (<selection> LIMIT $n)
//	RETURNING <returning>
//
//	table        被领取的表
//	selection    候选行 SELECT：**只投影 id 一列**（IN 子查询必须返回单列），
//	             自带 WHERE / ORDER BY，不带 LIMIT，也不带任何锁子句。它与 PG 分支
//	             用的是同一条候选条件，只是不再拼行锁子句、也不再多投影列。
//	selArgs      selection 的参数，**最后一个就是 LIMIT 的值**（与 PG 分支拼
//	             `LIMIT $<len(args)>` 是同一个约定），因此 LIMIT 的占位符就是 $n。
//	returning    RETURNING 列；可以是 id，也可以是被领取行的其它列（SQLite 的
//	             UPDATE … RETURNING 会返回更新后的整行）。
//	setSQL       SET 子句，占位符从 $n+1 起编号。
//	setArgs      SET 子句的参数，顺序对应 setSQL 里的 $n+1、$n+2……
//
// 占位符编号上故意把 SET 的参数排在 selection 之后，selection 里原有的 $1..$n
// 一个都不用改，两个分支的候选条件与参数顺序因此保持逐字一致。
//
// 领取条件（selection 的 WHERE）必须自带「排除已占用行」的判定，例如租约到期判断或
// NOT fanned_out：占用标记由 SET 子句写入，并发的第二个领取者拿到写锁后会重新求值
// 子查询，从而自然跳过已经领走的行。
func sqliteClaimUpdate(table, selection, returning string, selArgs []any, setSQL string, setArgs ...any) (string, []any) {
	n := len(selArgs)
	args := make([]any, 0, n+len(setArgs))
	args = append(args, selArgs...)
	args = append(args, setArgs...)
	return "UPDATE " + table + " SET " + setSQL +
		"\nWHERE id IN (" + selection + " LIMIT $" + fmt.Sprint(n) + ")" +
		"\nRETURNING " + returning, args
}

// sqlTimestampNotAfterNow 返回「该时间戳列是否已到期」的比较表达式，
// 本文件收录它是因为**队列领取条件必须能真的跑起来**：SQLite 分支的领取语句
// 直接照搬了 PG 的 `next_attempt_at <= now()`，而那一句在 SQLite 上根本执行不了
// （报 datatype mismatch (20)，与行锁无关，是移植时就带过来的问题）。
//
// 原因：schema 里这些列声明为 TIMESTAMP，SQLite 判定其亲和性为 NUMERIC；而
// sqlite_compat.go 注册的 now() 返回的是 TEXT。两个操作数亲和性不同、且文本
// 无法转成数字时，SQLite 直接报 DATATYPE_MISMATCH 而不是当作字符串比较。
//
//	PG:      col <= now()
//	SQLite:  CAST(col AS TEXT) <= now()
//
// CAST(col AS TEXT) 给左操作数 TEXT 亲和性，比较退回字典序。这对本项目的列是
// 等价的：写入路径只有两条（列默认值与 now()/sqlNowPlusSeconds），都产出同一形状
// 「2026-10-14 19:16:05.123+00:00」——定宽、UTC、固定 +00:00，字典序即时间序
// （sqlite_compat.go 与 dialect_mixed.go 的 sqlNowPlusSeconds 是同一条论证）。
func sqlTimestampNotAfterNow(col string) string {
	return "CAST(" + col + " AS TEXT) <= now()"
}

// claimIDs 读一条 sqliteClaimUpdate 的 RETURNING id 结果。
// 它与 selectForClaim（PG 分支）配成一对：两条分支给调用方的是同一种东西——领到的
// id 列表，顺序无意义。
func claimIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

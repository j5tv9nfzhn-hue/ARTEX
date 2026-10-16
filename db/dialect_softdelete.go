package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// =====================================================================
// xmax 方言层（PostgreSQL ↔ SQLite）
//
// 文件名沿用 "softdelete" 的叫法，只是为了和并行迁移任务的文件名互相区分；
// 真正收口的是 db/companies.go 中 UpsertCompany 里那一处 `(xmax = 0)`。
//
// ---------------------------------------------------------------------
// xmax 到底在做什么
// ---------------------------------------------------------------------
//
// 原始语句（PostgreSQL）：
//
//	INSERT INTO companies(name, nkey, logo) VALUES ($1,$2,$3)
//	ON CONFLICT (nkey) DO UPDATE SET
//	    name = EXCLUDED.name,
//	    logo = COALESCE(EXCLUDED.logo, companies.logo),
//	    updated_at = now()
//	RETURNING id, (xmax = 0)
//
// xmax 不是 companies 的业务列，而是 PostgreSQL MVCC 的系统列
// (system column)：它记录"当前这一行版本是被哪个事务 ID 改写/锁定的"。
//
//   - INSERT 分支真的插入了一个新的行版本：还没有任何后续事务改写它，
//     所以 xmax = 0。
//   - ON CONFLICT DO UPDATE 分支把已有行改写成了一个新的行版本：这一版的
//     xmax 会被写成当前事务 ID（非 0）。
//
// 因此 `(xmax = 0)` 的含义就是"本次 upsert 走的是插入分支，而不是更新
// 分支"，也就是 UpsertCompany 返回的 created 标志。
//
// 需要特别澄清：这**不是**软删除惯用法。companies 表根本没有 deleted_at
// 之类的墓碑列（db/schema_sqlite.sql:27-34 / db/schema_sqlite.sql:66-73）；
// 本项目的软删除是显式业务列语义——tasks.deleted_at IS NULL
// (db/schema_sqlite.sql:536-537)、exploration_nodes.state='deleted'
// (db/schema_sqlite.sql:184-197)。xmax 只是被借来当"插入/更新"的判定位。
//
// ---------------------------------------------------------------------
// SQLite 侧
// ---------------------------------------------------------------------
//
// SQLite 没有 MVCC，也没有 xmax 这类系统列。它的 RETURNING 返回的是该行
// 变更后的列值，不携带"这一行是被插入的还是被改写的"这一位信息；而
// EXCLUDED 伪表在 RETURNING 子句里不可引用。所以没有任何一个表达式能复刻
// `(xmax = 0)`。
//
// 替代做法是用同一个事务里的两条语句表达同一个判定：
//
//  1. INSERT ... ON CONFLICT (nkey) DO NOTHING RETURNING id
//     冲突与否交给数据库的唯一索引裁决，不做"先 SELECT 再 INSERT"的
//     读改写，因此不会引入 TOCTOU 窗口。有返回行 = 真新建（created=true）。
//  2. 上一条返回 0 行（sql.ErrNoRows）说明 nkey 已被占用 = 命中已有企业，
//     于是执行 UPDATE ... WHERE nkey=$3 RETURNING id（created=false）。
//
// 语义与 PostgreSQL 分支一一对应：
//
//	PG 走 INSERT 分支            <=>  SQLite 第 1 条拿到 id
//	PG 走 DO UPDATE 分支         <=>  SQLite 第 1 条 0 行 + 第 2 条 UPDATE
//	定位冲突行 ON CONFLICT (nkey)  <=>  UPDATE ... WHERE nkey = $3
//	SET name       = EXCLUDED.name  <=>  SET name = $1
//	SET logo       = COALESCE(     <=>  SET logo = COALESCE($2, logo)
//	                  EXCLUDED.logo,
//	                  companies.logo)
//	SET updated_at = now()           <=>  SET updated_at = now()
//
// 三条 SET 逐列对应，logo 的 COALESCE 在两侧都是"入参为空则保留原值"。
//
// 关于顺序与事务的两点取舍：
//   - 语句 1 是写语句，所以事务一开就拿到 SQLite 的写锁，不会出现 WAL 下
//     "先读后升级写锁" 触发 SQLITE_BUSY_SNAPSHOT 的情况。
//   - 事务保证两条语句之间没有别的连接插入/删除同一个 nkey，因此在写锁
//     保护下第 2 条语句必然命中第 1 条刚刚判定为冲突的那一行。
//     (id 被并发删掉) 在这里不可达，真发生也只是把底层错误原样抛出，
//     不会伪装成业务错误。

// upsertCompanyPGSQL 是 PostgreSQL 分支的语句，逐字保持迁移前的写法。
// xmax 在这里必须保留：SQLite 上它不存在，而 PG 上它是唯一能区分
// "插入" 与 "DO UPDATE" 的位。见本文件头部的说明。
const upsertCompanyPGSQL = `
INSERT INTO companies(name, nkey, logo)
VALUES ($1, $2, $3)
ON CONFLICT (nkey) DO UPDATE SET
    name = EXCLUDED.name,
    logo = COALESCE(EXCLUDED.logo, companies.logo),
    updated_at = now()
RETURNING id, (xmax = 0)`

// upsertCompanySQLiteInsert 只负责"抢占"这一行：nkey 已存在时什么都不做，
// 并且不返回任何行。与本文件配套的第 2 条语句见 upsertCompanySQLiteUpdate。
const upsertCompanySQLiteInsert = `
INSERT INTO companies(name, nkey, logo)
VALUES ($1, $2, $3)
ON CONFLICT (nkey) DO NOTHING
RETURNING id`

// upsertCompanySQLiteUpdate 是 xmax <> 0 分支的等价物：把已有企业改写成
// 新值并回传 id。占位符按 (name, logo, nkey) 的自然顺序排：
// COALESCE($2, logo) 对应 PG 侧的 COALESCE(EXCLUDED.logo, companies.logo)，
// 调用方传空 logo 时保留原值。
const upsertCompanySQLiteUpdate = `
UPDATE companies
SET name = $1, logo = COALESCE($2, logo), updated_at = now()
WHERE nkey = $3
RETURNING id`

// upsertCompanyByNkey 按规范化名称 upsert 一家企业，并报告这次调用到底是
// 新建（created=true）还是命中已有企业（created=false）。
//
// PostgreSQL 用单条语句的 xmax 惯用法判断"是插入还是更新"；SQLite 没有 xmax，
// 改用"抢占式 INSERT + UPDATE"的两语句等价写法，对外返回值完全一致。
func upsertCompanyByNkey(d *DB, name, nkey string, logoVal any) (id int64, created bool, err error) {
	tx, err := d.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback() //nolint:errcheck

	// 插入分支：唯一键无冲突，DO NOTHING 不会吞掉本行，所以必然返回一行。
	if scanErr := tx.QueryRow(upsertCompanySQLiteInsert, name, nkey, logoVal).Scan(&id); scanErr == nil {
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return id, true, nil
	} else if !errors.Is(scanErr, sql.ErrNoRows) {
		return 0, false, scanErr
	}

	// 更新分支：DO NOTHING 命中已有 nkey，一条都没写，所以回传 0 行。
	if err := tx.QueryRow(upsertCompanySQLiteUpdate, name, logoVal, nkey).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 写锁保护下不可达；真发生了说明 schema 或并发假设被破坏，
			// 明确报出来而不是伪装成正常结果。
			return 0, false, fmt.Errorf("upsert company %q: conflicting row is gone", nkey)
		}
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return id, false, nil
}

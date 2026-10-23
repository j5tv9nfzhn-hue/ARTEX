package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// legacyDSN 造一个「v2.3.x 形态」的数据库文件：先把完整 schema 建好，再把
// provider_id 这一列连同它的索引一起退回去。
//
// 这样得到的文件与现场升级前的老库同构（llm_providers 已存在、llm_profiles
// 没有 provider_id），而且是真实落到磁盘的文件，Open 走的路径与生产完全一致。
func legacyDSN(t *testing.T) string {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "legacy-2.3.db") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	con, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	for _, stmt := range sqliteStatements(schemaSQLiteSQL) {
		if _, err := con.Exec(stmt); err != nil {
			con.Close()
			t.Fatalf("build fixture schema: %v\n  > %s", err, stmt)
		}
	}
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_llm_profiles_provider`,
		`ALTER TABLE llm_profiles DROP COLUMN provider_id`,
	} {
		if _, err := con.Exec(stmt); err != nil {
			con.Close()
			t.Fatalf("degrade fixture: %v\n  > %s", err, stmt)
		}
	}
	if err := con.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	return dsn
}

// TestOpenUpgradesLegacyLLMProfiles 复现 v2.4.0 首次升级时的现场故障：
//
//	open stores: apply sqlite schema: statement 63/228: SQL logic error:
//	no such column: provider_id (1)
//	  > CREATE INDEX IF NOT EXISTS idx_llm_profiles_provider ON llm_profiles(provider_id);
//
// 老库的 llm_profiles 建在「供应商」改造之前，没有 provider_id 列；schema 每次
// 启动整段重放，而 CREATE TABLE IF NOT EXISTS 对已存在的表是空操作，所以只要
// schema 里有任何 DDL 引用这一列，启动就会当场失败。CI 里的库全是新建的，
// 这类问题只会在真实升级路径上出现——这条用例就是那条路径。
func TestOpenUpgradesLegacyLLMProfiles(t *testing.T) {
	dsn := legacyDSN(t)

	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("老库升级失败（依赖补列的 DDL 又跑到补列前面去了？）：%v", err)
	}

	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('llm_profiles') WHERE name='provider_id'`).Scan(&n); err != nil {
		d.Close()
		t.Fatalf("查列失败：%v", err)
	}
	if n != 1 {
		d.Close()
		t.Fatal("provider_id 列没有被补回来")
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_llm_profiles_provider'`).Scan(&n); err != nil {
		d.Close()
		t.Fatalf("查索引失败：%v", err)
	}
	if n != 1 {
		d.Close()
		t.Fatal("idx_llm_profiles_provider 没有建起来（补列之后的 DDL 没跑）")
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 再开一次：schema 重放 + 补列 + 后置 DDL 都必须幂等，
	// 否则用户第二次启动会直接失败。
	d2, err := Open(dsn)
	if err != nil {
		t.Fatalf("第二次打开失败（迁移不幂等）：%v", err)
	}
	if err := d2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

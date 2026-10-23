package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// schemaSQLiteSQL and sqliteStatements live in db/open_sqlite.go; these tests
// exercise the same artifacts the application uses rather than a copy.

// openSQLiteForSchema opens a fresh database for the translated schema.
//
// The `regexp` compatibility function is installed by db/sqlite_compat.go's
// init(); this helper only selects the pragmas the application must use.
//
// foreign_keys stays OFF here on purpose. The PostgreSQL schema relies on
// declared REFERENCES for integrity; SQLite ignores them unless the pragma is
// set, and turning it on for the schema test would surface ordering problems
// in the DDL rather than problems in the translation. Turning it on is the
// application's decision at Open time, not the schema's.
func openSQLiteForSchema(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.db")
	con, err := sql.Open("sqlite",
		"file:"+path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { con.Close() })
	return con
}

// applySQLiteSchema delegates to the production applier so the tests exercise
// the same code path Open uses, rather than a parallel implementation.
func applySQLiteSchemaForTest(t *testing.T, con *sql.DB) {
	t.Helper()
	conn, err := con.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := applySQLiteSchema(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSchemaApplies(t *testing.T) {
	con := openSQLiteForSchema(t)
	applySQLiteSchemaForTest(t, con)

	// Independent schema inventory: the original 54 tables plus six RE tables
	// and retention_plans / retention_audit. Missing and unexpected tables both fail.
	want := []string{
		"activity",
		"agent_llm_fallbacks",
		"agent_prompt_vars",
		"agent_prompts",
		"agent_skill_visibility",
		"agent_triggers",
		"agent_visibility",
		"agents",
		"asset_intercept_rules",
		"assets",
		"checkpoints",
		"compaction_state",
		"companies",
		"company_scope",
		"conversation_activities",
		"conversations",
		"credentials",
		"exploration_anchors",
		"exploration_edges",
		"exploration_nodes",
		"explorations",
		"finding_retests",
		"finding_verifications",
		"finding_traffic_bindings",
		"findings",
		"intercept_pending",
		"intercept_rules",
		"llm_profile_health",
		"llm_profiles",
		"llm_providers",
		"main_sessions",
		"mcp_servers",
		"mcp_tools_cache",
		"notification_channels",
		"notification_deliveries",
		"notification_events",
		"re_entities",
		"re_evidence",
		"re_providers",
		"re_samples",
		"re_sessions",
		"re_task_samples",
		"retention_audit",
		"retention_plans",
		"scheduler_state",
		"server_logs",
		"settings",
		"side_question_requests",
		"side_question_sessions",
		"skill_usage",
		"task_archives",
		"task_asset_links",
		"task_categories",
		"task_constraints",
		"task_env_context",
		"task_intercept_rules",
		"task_llm_profiles",
		"task_relations",
		"task_scope",
		"task_templates",
		"tasks",
		"tool_usage",
		"tools",
		"traffic_evidence_snapshots",
	}
	rows, err := con.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Errorf("建表数 = %d，预期 %d", len(got), len(want))
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("缺少表 %s", name)
		}
		delete(got, name)
	}
	for name := range got {
		t.Errorf("出现非预期表 %s", name)
	}
}

func TestSQLiteSchemaIsIdempotent(t *testing.T) {
	con := openSQLiteForSchema(t)
	applySQLiteSchemaForTest(t, con)
	// Open applies the schema on every start; a second pass must be a no-op.
	applySQLiteSchemaForTest(t, con)
}

func TestSQLiteSchemaColumnParity(t *testing.T) {
	con := openSQLiteForSchema(t)
	applySQLiteSchemaForTest(t, con)

	// Columns that the PostgreSQL schema only added via a later
	// ALTER TABLE ... ADD COLUMN. The translator folds these into the CREATE
	// TABLE, because SQLite has no ADD COLUMN IF NOT EXISTS and a separate
	// step would not be re-runnable.
	for _, tc := range []struct{ table, column string }{
		{"intercept_pending", "decision_source"},
		{"intercept_pending", "audit"},
		{"explorations", "round_no"},
		{"exploration_nodes", "content_version"},
		{"side_question_sessions", "memory"},
		{"llm_profiles", "retry_connect_attempts"},
		{"llm_profiles", "input_modalities"},
		{"llm_profiles", "provider_id"},
		{"assets", "company_source"},
		{"findings", "evidence_version"},
		// 批次 6 新增列同样折进 CREATE TABLE，老库靠 sqliteColumnAdds 补列。
		{"activity", "reverted_at"},
		{"conversation_activities", "reverted_at"},
		// 任务级 token 预算（0=不限），同样折进 CREATE TABLE + sqliteColumnAdds 补旧库。
		{"tasks", "token_budget"},
	} {
		rows, err := con.Query("PRAGMA table_info(" + tc.table + ")")
		if err != nil {
			t.Fatalf("%s: %v", tc.table, err)
		}
		found := false
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			if name == tc.column {
				found = true
			}
		}
		rows.Close()
		if !found {
			t.Errorf("%s.%s 缺失（ALTER 折叠遗漏）", tc.table, tc.column)
		}
	}
}

func TestSQLiteSchemaPreservesEnumLiterals(t *testing.T) {
	con := openSQLiteForSchema(t)
	applySQLiteSchemaForTest(t, con)

	// The translator rewrites column types (inet -> TEXT). It must not touch
	// string literals: 'cidr' here is a data value, not a type name.
	var src string
	if err := con.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='company_scope'`,
	).Scan(&src); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src, "'cidr'") {
		t.Errorf("company_scope 的 kind 枚举丢失 'cidr'：\n%s", src)
	}
}

func TestSQLiteStatementSplitter(t *testing.T) {
	stmts := sqliteStatements(
		"-- comment\nCREATE TABLE a (id INTEGER PRIMARY KEY);\n" +
			"CREATE TRIGGER t AFTER UPDATE ON a\nBEGIN\n  UPDATE a SET id=id WHERE rowid=NEW.rowid;\nEND;\n" +
			"INSERT INTO a (id) VALUES (1);\n")
	if len(stmts) != 3 {
		t.Fatalf("切分出 %d 条，预期 3：\n%s", len(stmts), strings.Join(stmts, "\n---\n"))
	}
	if !strings.Contains(stmts[1], "END;") {
		t.Errorf("触发器未完整切出：\n%s", stmts[1])
	}
}

// TestSQLiteForeignKeysDeclared documents that the translated schema keeps the
// REFERENCES clauses. SQLite ignores them unless foreign_keys is enabled at
// runtime, so their presence in the DDL is a precondition for the application
// choosing to enable that pragma later.
func TestSQLiteForeignKeysDeclared(t *testing.T) {
	con := openSQLiteForSchema(t)
	applySQLiteSchemaForTest(t, con)

	rows, err := con.Query(`PRAGMA foreign_key_list(intercept_pending)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if n == 0 {
		t.Error("intercept_pending 的 REFERENCES 丢失：SQLite 默认不强制外键，丢失后无法靠运行时补救")
	}
}

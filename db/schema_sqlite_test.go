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
		"file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
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

	var tables int
	if err := con.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`,
	).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	// The PostgreSQL schema declares 49 CREATE TABLE statements.
	if tables != 49 {
		t.Errorf("建表数 = %d，预期 49", tables)
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
		{"assets", "company_source"},
		{"findings", "evidence_version"},
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

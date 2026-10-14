package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Engine selection decides which backend every query goes to, so it is pinned
// here rather than discovered at runtime. The default matters most: during the
// SQLite migration the tree must stay green on PostgreSQL, and flipping the
// default early is the one change that would silently break unrelated packages.

func TestNormalizeEngine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"sqlite", EngineSQLite},
		{"SQLite", EngineSQLite},
		{" sqlite3 ", EngineSQLite},
		{"postgres", EnginePostgres},
		{"postgresql", EnginePostgres},
		{"pg", EnginePostgres},
		{"PGSQL", EnginePostgres},
		{"", ""},
		{"mysql", ""},
		{"sqlite4", ""},
	} {
		if got := NormalizeEngine(tc.in); got != tc.want {
			t.Errorf("NormalizeEngine(%q) = %q，预期 %q", tc.in, got, tc.want)
		}
	}
}

// pointConfigAt redirects the package-level config lookup at a scratch file so
// the precedence test does not depend on -- or disturb -- the developer's real
// configuration.
func pointConfigAt(t *testing.T, path string) {
	t.Helper()
	t.Setenv("ARTEX_CONFIG", path)
}

func TestEnginePrecedence(t *testing.T) {
	t.Setenv(envEngine, "")
	t.Setenv(envSQLitePath, "")

	// Neither source configured: PostgreSQL, so an unfinished SQLite migration
	// cannot turn every skipped test into a failure.
	if got := Engine(); got != EnginePostgres {
		t.Errorf("默认引擎 = %q，预期 %q", got, EnginePostgres)
	}

	// Config file, then environment on top of it.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	write := func(engine string) {
		if err := os.WriteFile(cfg, []byte(`{"database":{"engine":"`+engine+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pointConfigAt(t, cfg)

	write(EngineSQLite)
	if got := Engine(); got != EngineSQLite {
		t.Errorf("配置文件指定 sqlite 时 = %q", got)
	}
	t.Setenv(envEngine, EnginePostgres)
	if got := Engine(); got != EnginePostgres {
		t.Errorf("环境变量应覆盖配置文件，实际 = %q", got)
	}

	// An unrecognised value falls through instead of becoming an engine that
	// does not exist.
	t.Setenv(envEngine, "cassandra")
	if got := Engine(); got != EngineSQLite {
		t.Errorf("非法环境变量应被忽略，实际 = %q", got)
	}
}

func TestSQLiteDSNCarriesRequiredPragmas(t *testing.T) {
	t.Setenv(envSQLitePath, filepath.Join(t.TempDir(), "a.db"))
	dsn := SQLiteDSN()

	// Each of these is load-bearing rather than tuning; see SQLiteDSN's comment.
	for _, want := range []string{
		"file:",
		"journal_mode(WAL)",  // readers must not block behind a writer
		"busy_timeout(5000)", // contended writes wait instead of SQLITE_BUSY
		"foreign_keys(ON)",   // SQLite ignores REFERENCES without this
		"_texttotime=1",      // otherwise every time.Time scan fails
		"_txlock=immediate",  // otherwise read-then-write has a race window
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN 缺少 %q：%s", want, dsn)
		}
	}
}

func TestSQLitePathIsAbsoluteAndCreatesParent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "deeper", "artex.db")
	t.Setenv(envSQLitePath, target)

	got := SQLitePath()
	if !filepath.IsAbs(got) {
		t.Errorf("SQLitePath() = %q，应为绝对路径", got)
	}
	if fi, err := os.Stat(filepath.Dir(got)); err != nil || !fi.IsDir() {
		t.Errorf("父目录未被创建：%v", err)
	}
}

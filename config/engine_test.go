package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The database location is the one piece of storage configuration left, so it is
// pinned here rather than discovered at runtime. These tests also guard the DSN
// contract: every pragma below is load-bearing, and a test that quietly lost one
// would only surface later as a time.Time scan failure or a lock-contention
// flake in an unrelated package.

// pointConfigAt redirects the package-level config lookup at a scratch file so
// the precedence tests do not depend on -- or disturb -- the developer's real
// configuration.
func pointConfigAt(t *testing.T, path string) {
	t.Helper()
	t.Setenv("ARTEX_CONFIG", path)
}

func TestSQLitePathPrecedence(t *testing.T) {
	t.Setenv(envSQLitePath, "")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	pointConfigAt(t, cfg)

	// Nothing configured: the file lands next to the config file.
	if got := SQLitePath(); filepath.Base(got) != sqliteFileName {
		t.Errorf("默认库文件名 = %q，预期 %q", filepath.Base(got), sqliteFileName)
	}

	// Config file, then environment on top of it.
	fromFile := filepath.Join(dir, "from-file.db")
	writeConfig(t, cfg, Config{Database: Database{Path: fromFile}})
	if got := SQLitePath(); got != fromFile {
		t.Errorf("配置文件指定 path 时 = %q，预期 %q", got, fromFile)
	}

	fromEnv := filepath.Join(dir, "from-env.db")
	t.Setenv(envSQLitePath, fromEnv)
	if got := SQLitePath(); got != fromEnv {
		t.Errorf("环境变量应覆盖配置文件，实际 = %q", got)
	}
}

func TestSQLiteDSNCarriesRequiredPragmas(t *testing.T) {
	t.Setenv(envSQLitePath, filepath.Join(t.TempDir(), "a.db"))
	dsn := SQLiteDSN()

	// Each of these is load-bearing rather than tuning; see SQLiteDSN's comment.
	for _, want := range []string{
		"file:",
		"journal_mode(WAL)",   // readers must not block behind a writer
		"busy_timeout(10000)", // contended writes wait instead of SQLITE_BUSY
		"synchronous(NORMAL)", // no per-commit fsync: a commit must not hog the write lock
		"foreign_keys(ON)",    // SQLite ignores REFERENCES without this
		"_texttotime=1",       // otherwise every time.Time scan fails
		"_txlock=immediate",   // otherwise read-then-write has a race window
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN 缺少 %q：%s", want, dsn)
		}
	}
}

// TestSQLiteDSNForMatchesSQLiteDSN pins the two constructors to one contract.
// Tests and tooling use SQLiteDSNFor with their own path; if it ever drifted from
// SQLiteDSN, those callers would silently exercise a different database
// configuration than production.
func TestSQLiteDSNForMatchesSQLiteDSN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same.db")
	t.Setenv(envSQLitePath, path)
	if got, want := SQLiteDSNFor(path), SQLiteDSN(); got != want {
		t.Errorf("SQLiteDSNFor 与 SQLiteDSN 不一致：\n got %q\nwant %q", got, want)
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

// TestSQLitePathSourceNamesOrigin pins the three startup-log labels so an
// operator can tell which source won without reading the code.
func TestSQLitePathSourceNamesOrigin(t *testing.T) {
	t.Setenv(envSQLitePath, "")
	pointConfigAt(t, filepath.Join(t.TempDir(), "nope.json"))
	if got := SQLitePathSource(); !strings.Contains(got, "内置默认") {
		t.Errorf("无配置时来源 = %q，预期内置默认", got)
	}

	t.Setenv(envSQLitePath, filepath.Join(t.TempDir(), "x.db"))
	if got := SQLitePathSource(); !strings.Contains(got, envSQLitePath) {
		t.Errorf("环境变量来源 = %q，应含 %q", got, envSQLitePath)
	}
}

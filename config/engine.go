package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Storage engine selection.
//
// ARTEX runs on SQLite by default and keeps PostgreSQL as a selectable
// alternative. The switch exists so the migration can be exercised end to end
// on both engines without a rebuild, and so an existing PostgreSQL
// deployment keeps working unchanged.
//
// Precedence for every knob here is the same as the rest of the package:
//
//	env  >  config file  >  built-in default
const (
	EnginePostgres = "postgres"
	EngineSQLite   = "sqlite"
)

const (
	envEngine     = "ARTEX_DB_ENGINE"
	envSQLitePath = "ARTEX_SQLITE_PATH"

	// sqliteFileName is the database file created next to the config file when
	// no path is configured.
	sqliteFileName = "artex.db"
)

// NormalizeEngine maps user input onto a known engine id. It returns "" for
// anything unrecognised so callers can fall through to the next configuration
// source instead of failing on a typo in one place but not another.
func NormalizeEngine(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case EngineSQLite, "sqlite3":
		return EngineSQLite
	case EnginePostgres, "postgresql", "pg", "pgsql":
		return EnginePostgres
	}
	return ""
}

// Engine resolves the storage engine with precedence:
//
//	env ARTEX_DB_ENGINE  >  config file (database.engine)  >  EnginePostgres
//
// The default is still PostgreSQL while the SQLite migration is in progress.
// The translated schema is complete and the engine switch works, but the
// query layer is not finished: switching the default early would turn
// currently-skipped tests across db and agent into failures on every build
// without the tree actually being closer to done.
//
// Until every dialect gap is closed, select SQLite explicitly:
//
//	ARTEX_DB_ENGINE=sqlite
//
// Flip this default to EngineSQLite once the migration lands.
func Engine() string {
	if v := NormalizeEngine(os.Getenv(envEngine)); v != "" {
		return v
	}
	if v := NormalizeEngine(Load().Database.Engine); v != "" {
		return v
	}
	return EnginePostgres
}

// EngineSource reports where the engine choice came from, for startup logging.
// It is advisory: callers treat an unrecognised value as "config file" rather
// than branching on it.
func EngineSource() string {
	if NormalizeEngine(os.Getenv(envEngine)) != "" {
		return "环境变量 " + envEngine
	}
	if NormalizeEngine(Load().Database.Engine) != "" {
		return fmt.Sprintf("配置文件 %s (database.engine)", Path())
	}
	return "内置默认"
}

// SQLitePath resolves the on-disk database file with precedence:
//
//	env ARTEX_SQLITE_PATH  >  config file (database.path)  >  <配置目录>/artex.db
//
// The parent directory is created if missing, so pointing this at a fresh
// location works without a separate setup step.
func SQLitePath() string {
	var p string
	if v := strings.TrimSpace(os.Getenv(envSQLitePath)); v != "" {
		p = v
	} else if v := strings.TrimSpace(Load().Database.Path); v != "" {
		p = v
	} else {
		p = filepath.Join(filepath.Dir(Path()), sqliteFileName)
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if dir := filepath.Dir(p); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	return p
}

// SQLiteDSN returns the driver DSN, not a bare path.
//
// The pragmas are part of the connection contract rather than optional tuning:
//
//   - journal_mode(WAL) lets readers proceed while a write is in flight.
//     SQLite's default rollback journal serialises all access behind one
//     writer, which would stall the server's concurrent readers.
//   - busy_timeout(5000) makes a contended write wait instead of failing
//     immediately with SQLITE_BUSY, which the runtime does not retry.
//   - foreign_keys(ON) restores the referential integrity PostgreSQL gave for
//     free; SQLite ignores declared REFERENCES unless this is set.
//
// WAL is a persistent database property, so setting it per connection is
// harmless and keeps a freshly created file correct.
//
// _texttotime is required, not cosmetic. PostgreSQL returns TIMESTAMPTZ
// columns to Go as time.Time; SQLite has no such type and returns the stored
// TEXT. Without this the first Scan into a *time.Time fails with "unsupported
// Scan, storing driver.Value type string". _time_format then pins the
// outbound direction to the same shape, so a value written by Go and a value
// written by a column default are byte-identical.
//
// _txlock=immediate is what makes the read-then-write patterns in this
// codebase correct on SQLite. The default is "deferred": a transaction does
// not take the write lock until its first write, and a leading SELECT runs
// under a read lock with no protection against a concurrent writer. Patterns
// like "read the row, check a version column, then write" -- finding-retest
// deduplication, evidence_version compare-and-set, archive state transitions --
// therefore have a real race window under deferred semantics, and a deferred
// transaction that upgrades to a writer can fail outright with
// SQLITE_BUSY_SNAPSHOT. Immediate takes the write lock at BEGIN, which is
// correct here because this is a single-user desktop application with no
// read-only long transactions competing for the lock; busy_timeout turns the
// resulting contention into a wait rather than an error.
//
// The cost is that read-only transactions also serialise against writers. That
// is the intended trade: correctness of the write paths is worth more than
// read concurrency that this workload does not need.
func SQLiteDSN() string {
	return SQLiteDSNFor(SQLitePath())
}

// SQLiteDSNFor is SQLiteDSN for an explicit file path. Callers that manage
// their own database location (tests, tools, conversions) must go through this
// rather than assembling a DSN themselves: the pragma and format list is a
// single contract, and a second copy that drifts is how a test starts failing
// on time.Time scans or on write-transaction locking while the application it
// is supposed to be exercising works fine.
func SQLiteDSNFor(path string) string {
	return "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_texttotime=1" +
		"&_time_format=" + sqliteTimeFormat +
		"&_txlock=immediate"
}

// sqliteTimeFormat is the on-disk timestamp shape. It is UTC with an explicit
// Z and millisecond precision: sortable lexicographically, round-trips through
// time.Parse, and matches the schema's column defaults.
const sqliteTimeFormat = "sqlite"

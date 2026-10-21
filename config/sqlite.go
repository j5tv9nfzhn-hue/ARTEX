package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SQLite is the only storage engine ARTEX supports. The database is a single
// file created next to the config file, with no server, no service and no
// installation step — which is what makes the product download-and-run.
//
// Everything the engine needs is pinned here rather than discovered at runtime:
// the path, the DSN, and the pragma/format contract that the rest of the
// codebase (and the tests) must agree on. There is no engine switch, because a
// second backend would mean every query has two spellings and only one of them
// is exercised by default.

const (
	// envSQLitePath overrides the database location. Precedence:
	//
	//	env ARTEX_SQLITE_PATH  >  config file (database.path)  >  <配置目录>/artex.db
	envSQLitePath = "ARTEX_SQLITE_PATH"

	// sqliteFileName is the database file created next to the config file when
	// no path is configured.
	sqliteFileName = "artex.db"
)

// SQLiteBusyTimeout 是「等待写锁」的时间预算（DSN 里下发的 busy_timeout pragma）。
//
// 它是并发写入的**下限契约**：SQLite 单写者模型下，写语句遇到库级写锁时会先在
// 这个预算内等待、再报 SQLITE_BUSY。任何环绕写事务的 context 超时都必须比它长，
// 否则一次本来会等到锁的写入会被 ctx 提前打断成 "context deadline exceeded"。
// server 侧的后台持久化统一下限见 server/sqlite_write.go。
//
// 10 秒是「桌面单机 + 慢盘」的取值：正常写入是毫秒级，只有磁盘抖动、杀毒扫描、
// 检查点回写叠加时才会排到秒级；更长的等待对交互式操作已无意义。
const SQLiteBusyTimeout = 10 * time.Second

// SQLitePath resolves the on-disk database file.
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

// SQLitePathSource describes where the database location came from, for startup
// logging. Kept next to SQLitePath so the two cannot drift.
func SQLitePathSource() string {
	if strings.TrimSpace(os.Getenv(envSQLitePath)) != "" {
		return "环境变量 " + envSQLitePath
	}
	if strings.TrimSpace(Load().Database.Path) != "" {
		return "配置文件 " + Path() + " (database.path)"
	}
	return "内置默认（配置目录）"
}

// SQLiteDSN returns the driver DSN, not a bare path.
//
// The pragmas are part of the connection contract rather than optional tuning:
//
//   - journal_mode(WAL) lets readers proceed while a write is in flight.
//     SQLite's default rollback journal serialises all access behind one
//     writer, which would stall the server's concurrent readers.
//
//   - busy_timeout makes a contended write wait instead of failing immediately
//     with SQLITE_BUSY, which the runtime does not retry. The budget is
//     SQLiteBusyTimeout; caller-side deadlines around writes must exceed it or
//     a queued write turns into a spurious context timeout.
//
//   - synchronous(NORMAL) drops the per-commit fsync in WAL mode. Durability
//     still covers an application crash (the WAL survives through the OS
//     cache); only an OS crash or power loss can lose the last commits. The
//     trade turns a commit on a Windows box (fsync, often through antivirus,
//     on the WAL file) from hundreds of milliseconds into an append — and a
//     shorter lock-hold is what keeps the single write lock available to
//     everyone else.
//
//   - foreign_keys(ON) restores referential integrity; SQLite ignores declared
//     REFERENCES unless this is set.
//
//   - auto_vacuum(incremental) 让删除腾出的页可以还给文件系统。它与上面几条
//     不同：在**已经有表**的库上它是静默空操作，只有建库前设置、或随后执行一次
//     VACUUM 才会写进文件头。老库的一次性转换在 db/reclaim.go 的
//     convertSQLiteAutoVacuum，持续回收由 db.DB.ReclaimFreePages 完成；写进 DSN
//     是为了让任何新文件（包括测试与工具直接打开的）从诞生起就带上这层契约。
//
// WAL is a persistent database property, so setting it per connection is
// harmless and keeps a freshly created file correct.
//
// _texttotime is required, not cosmetic. SQLite has no TIMESTAMPTZ type and
// returns the stored TEXT, so without this the first Scan into a *time.Time
// fails with "unsupported Scan, storing driver.Value type string".
// _time_format then pins the outbound direction to the same shape, so a value
// written by Go and a value written by a column default are byte-identical.
//
// _txlock=immediate is what makes the read-then-write patterns in this
// codebase correct. The default is "deferred": a transaction does not take the
// write lock until its first write, and a leading SELECT runs under a read lock
// with no protection against a concurrent writer. Patterns like "read the row,
// check a version column, then write" — finding-retest deduplication,
// evidence_version compare-and-set, archive state transitions — therefore have
// a real race window under deferred semantics, and a deferred transaction that
// upgrades to a writer can fail outright with SQLITE_BUSY_SNAPSHOT. Immediate
// takes the write lock at BEGIN, which is correct here because this is a
// single-user desktop application with no read-only long transactions competing
// for the lock; busy_timeout turns the resulting contention into a wait rather
// than an error.
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
		"?_pragma=busy_timeout(" + strconv.FormatInt(SQLiteBusyTimeout.Milliseconds(), 10) + ")" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=auto_vacuum(incremental)" +
		// 页缓存 32MiB(负值 = KiB):默认 2MB 太小,一次图查询/分页就把缓存冲干净,
		// 于是每次都要重新读页。这是**每条连接**的上限,而连接池是 8 写/3 读,
		// 所以取 32MiB(最坏约 350MB 上限)而不是 64MiB——上限是给病态查询兜底的,
		// 正常工作时每条连接只用到工作集那几 MB。
		"&_pragma=cache_size(-32768)" +
		// 256MiB 只读内存映射:命中映射的读省掉一次从页缓存到用户态的拷贝
		// (SQLite 官方压在 mmap 上的主要收益就是这个)。写仍走常规缓冲。
		"&_pragma=mmap_size(268435456)" +
		// 临时表/临时排序放内存:排序中间结果落盘会在每次大查询时制造新的写锁竞争。
		"&_pragma=temp_store(MEMORY)" +
		"&_texttotime=1" +
		"&_time_format=" + sqliteTimeFormat +
		"&_txlock=immediate"
}

// sqliteTimeFormat is the on-disk timestamp shape. It is UTC with an explicit
// Z and millisecond precision: sortable lexicographically, round-trips through
// time.Parse, and matches the schema's column defaults.
const sqliteTimeFormat = "sqlite"

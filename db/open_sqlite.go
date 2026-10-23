package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/config"

	_ "modernc.org/sqlite"
)

// isSQLiteDSN distinguishes the two drivers from the DSN itself.
//
// Both are driver-owned DSNs rather than raw connection parameters, so the
// scheme is the discriminator: modernc.org/sqlite uses a URI ("file:..."),
// pgx uses a postgres:// URL. An empty DSN is treated as PostgreSQL because
// that is what every existing caller has always passed, and failing later at
// sql.Open gives a clearer error than silently opening the wrong database.
func isSQLiteDSN(dsn string) bool {
	s := strings.TrimSpace(dsn)
	return strings.HasPrefix(strings.ToLower(s), "file:")
}

// OpenConfigured resolves the engine from configuration and opens that
// backend. Callers that do not have a specific DSN in hand should prefer this
// over Open(dsn) so the ARTEX_DB_ENGINE / database.engine switch is honoured in
// exactly one place.
func OpenConfigured() (*DB, error) {
	dsn, _, err := DSN()
	if err != nil {
		return nil, err
	}
	return Open(dsn)
}

// sqliteStatements splits the schema into individual statements.
//
// A plain split on ";" is wrong: the translated triggers have the shape
// CREATE TRIGGER ... BEGIN <stmt>; END; and the inner semicolon would end the
// statement early. Once BEGIN is seen, only END; terminates.
func sqliteStatements(script string) []string {
	var out []string
	var buf strings.Builder
	inTrigger := false
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		buf.WriteString(line)
		buf.WriteString("\n")

		upper := strings.ToUpper(trimmed)
		switch {
		case !inTrigger && strings.HasPrefix(upper, "CREATE TRIGGER"):
			inTrigger = true
		case inTrigger && strings.HasPrefix(upper, "END;"):
			inTrigger = false
		}

		if !inTrigger && strings.HasSuffix(trimmed, ";") {
			if s := strings.TrimSpace(buf.String()); s != "" {
				out = append(out, s)
			}
			buf.Reset()
		}
	}
	if s := strings.TrimSpace(buf.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// applySQLiteSchema runs the translated schema.
//
// Unlike PostgreSQL this needs no advisory lock: the schema is a single
// connection's work and SQLite serialises writers at the file level anyway.
func applySQLiteSchema(ctx context.Context, con *sql.Conn) error {
	stmts := sqliteStatements(schemaSQLiteSQL)
	for i, stmt := range stmts {
		if _, err := con.ExecContext(ctx, stmt); err != nil {
			head := stmt
			if idx := strings.IndexByte(head, '\n'); idx > 0 {
				head = head[:idx]
			}
			return fmt.Errorf("apply sqlite schema: statement %d/%d: %w\n  > %s",
				i+1, len(stmts), err, head)
		}
	}
	return nil
}

// sqliteColumnAdds 是「补旧库」列:它们已经折进 schema 的 CREATE TABLE(见
// schema_sqlite.sql 各处注释,例如 llm_profiles.input_modalities),但老 SQLite
// 存量文件里没有这些列——CREATE TABLE IF NOT EXISTS 对已存在的表是空操作。
//
// SQLite 没有 ADD COLUMN IF NOT EXISTS,而 schema 每次启动都整段重放:不加保护
// 直接 ALTER,第二次启动就会因 duplicate column name 整体失败。故先查
// pragma_table_info,缺了才加,等价于 PG 侧 ADD COLUMN IF NOT EXISTS 的幂等语义。
var sqliteColumnAdds = []struct {
	table  string
	column string
	alter  string
}{
	{"tasks", "analysis_domain", `ALTER TABLE tasks ADD COLUMN analysis_domain TEXT NOT NULL DEFAULT 'web_security' CHECK (analysis_domain IN ('web_security','software_re'))`},
	{
		"llm_profiles",
		"input_modalities",
		`ALTER TABLE llm_profiles ADD COLUMN input_modalities TEXT NOT NULL DEFAULT ''`,
	},
	{
		"activity",
		"reverted_at",
		`ALTER TABLE activity ADD COLUMN reverted_at TIMESTAMP`,
	},
	{
		"conversation_activities",
		"reverted_at",
		`ALTER TABLE conversation_activities ADD COLUMN reverted_at TIMESTAMP`,
	},
	{
		// 凭据实体化(计划 46–47)。可空列,ALTER 时默认 NULL 才允许带 REFERENCES
		// (SQLite 的 ADD COLUMN 限制);指向的 credentials 表由 schema 建在前面。
		"llm_profiles",
		"credential_id",
		`ALTER TABLE llm_profiles ADD COLUMN credential_id BIGINT REFERENCES credentials(id) ON DELETE SET NULL`,
	},
	{
		// 任务级 token 预算(0=不限)。普通带默认值的列,直接 ADD 即可。
		"tasks",
		"token_budget",
		`ALTER TABLE tasks ADD COLUMN token_budget INTEGER NOT NULL DEFAULT 0`,
	},
	{
		// 复核状态机(findings.validation)。老库整体回填 unverified:历史发现没有走过
		// 独立复核,不能默认当成真值。
		"findings",
		"validation",
		`ALTER TABLE findings ADD COLUMN validation TEXT NOT NULL DEFAULT 'unverified'`,
	},
	{
		// 工具错误标记(见 server/tool_usage.go)。历史行没有错误信息可回溯,统一回填
		// 0=未报错——宁可低估,也不要把「不知道」记成错误。
		"tool_usage",
		"is_error",
		`ALTER TABLE tool_usage ADD COLUMN is_error BOOLEAN NOT NULL DEFAULT 0`,
	},
	{
		// 供应商标识(模型挂在供应商下)。可空列才能带 REFERENCES(SQLite 的 ADD
		// COLUMN 限制);回填由 backfillLLMProviders 紧跟其后完成,所以运行期恒非空。
		"llm_profiles",
		"provider_id",
		`ALTER TABLE llm_profiles ADD COLUMN provider_id BIGINT REFERENCES llm_providers(id)`,
	},
}

// sqlitePostColumnDDL 是「依赖 sqliteColumnAdds 补出来的列」的 DDL，必须在补列
// **之后**执行。
//
// 为什么不能写进 schema_sqlite.sql：schema 每次启动整段重放，而 CREATE TABLE
// IF NOT EXISTS 对已存在的表是空操作——老库里 provider_id 这一列还不存在，索引
// 一建就报 `no such column: provider_id`，整个进程起不来（v2.4.0 从 2.3.x 升级
// 时的现场故障）。新库因为 CREATE TABLE 自带这一列看不出来；CI 里的数据库全是
// 新建的，同样发现不了，所以必须由 TestOpenUpgradesLegacyLLMProfiles 这类「先退化成
// 老库形态再打开」的用例兜住。
//
// 以后凡是引用「补出来的列」的索引/视图/触发器，一律加到这里，不要加回 schema 文件。
var sqlitePostColumnDDL = []string{
	`CREATE INDEX IF NOT EXISTS idx_llm_profiles_provider ON llm_profiles(provider_id)`,
}

// applySQLiteColumnAdds 逐条补齐上表缺失的列。pragma_table_info 是表值函数,表名
// 可以作为绑定参数传入,不必拼进 SQL(与 archive_restore_sqlite.go 的用法一致)。
func applySQLiteColumnAdds(ctx context.Context, con *sql.Conn) error {
	for _, col := range sqliteColumnAdds {
		var n int
		if err := con.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info($1) WHERE name=$2`,
			col.table, col.column).Scan(&n); err != nil {
			return fmt.Errorf("check sqlite column %s.%s: %w", col.table, col.column, err)
		}
		if n > 0 {
			continue
		}
		if _, err := con.ExecContext(ctx, col.alter); err != nil {
			return fmt.Errorf("add sqlite column %s.%s: %w", col.table, col.column, err)
		}
	}
	return nil
}

// applySQLitePostColumnDDL 建那些引用补列的索引。IF NOT EXISTS 保证幂等，
// 新库老库走同一条路。
func applySQLitePostColumnDDL(ctx context.Context, con *sql.Conn) error {
	for _, stmt := range sqlitePostColumnDDL {
		if _, err := con.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply sqlite post-column ddl: %w\n  > %s", err, stmt)
		}
	}
	return nil
}

// openSQLite opens the embedded database and initialises it.
//
// The SQLite side deliberately skips two things the PostgreSQL path does:
//
//   - ensureDatabase: SQLite has no separate server and no database to CREATE.
//   - the advisory lock around DDL: SQLite serialises writers on the file, so
//     a session-level lock would be redundant as well as unimplementable.
func openSQLite(dsn string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite (%s): %w", config.Redact(dsn), err)
	}

	// 连接池上限:每条 sqlite 连接都带自己的页缓存(DSN 里 cache_size=-64MiB),无上限
	// 意味着空闲连接越多常驻内存越高。写全部走单写协程(db/writer.go),8 条读连接
	// 足够覆盖页面查询与流式读取。
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(4)

	d := &DB{DB: sqlDB}
	// One connection for schema application and seeding, so the two cannot
	// interleave and so a partially-created file is not left behind by a
	// failed seed.
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	defer conn.Close()

	// 新库的回收契约必须在建表之前落定：auto_vacuum 只有在文件还没有任何表时
	// 才能直接写进头部，建过表再改就必须走一次 VACUUM（见 convertSQLiteAutoVacuum）。
	if err := prepareSQLiteAutoVacuum(context.Background(), conn); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := applySQLiteSchema(context.Background(), conn); err != nil {
		sqlDB.Close()
		return nil, err
	}
	// 补旧库列:老 SQLite 文件缺的列在这里补齐(新库的列已折进 CREATE TABLE,
	// 此步是完整的空操作)。必须在 seedBuiltins 之前——seed 会写 llm_profiles 等表。
	if err := applySQLiteColumnAdds(context.Background(), conn); err != nil {
		sqlDB.Close()
		return nil, err
	}
	// 依赖补列的索引排在补列之后：schema 重放时老库还没有 provider_id，
	// 在那里建索引会让升级当场失败（见 sqlitePostColumnDDL 的注释）。
	if err := applySQLitePostColumnDDL(context.Background(), conn); err != nil {
		sqlDB.Close()
		return nil, err
	}
	// 凭据折叠必须排在回填之前：先把「绑了凭据」的 Key 搬回供应商/模型行，
	// 回填才能按真实 Key 明文把同端点的配置合并成一家，而不是按 credential_id 猜。
	if n, err := foldCredentialsIntoProviders(context.Background(), conn); err != nil {
		sqlDB.Close()
		return nil, err
	} else if n > 0 {
		log.Printf("[db] 凭据已折叠回供应商行：%d 条（旧「凭证」模型已废弃）", n)
	}
	// 供应商回填：把每个存量模型配置收进它自己的供应商行。幂等（只处理
	// provider_id 为空的行），新库是空操作。必须早于 seedBuiltins —— seed 之后的
	// 新配置会自己建供应商，混在一起会让"这条为什么没有供应商"变得无法解释。
	if err := backfillLLMProviders(context.Background(), conn); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("backfill llm providers: %w", err)
	}
	// 老库的一次性转换：历史文件是 auto_vacuum=NONE，删除过的页永远留在文件里
	// （实测有安装 91.5% 的页停在这里）。这一步把它整体重写一次并永久改为
	// incremental，此后每次删除都能自我回收。失败不阻断启动——库仍然可用，
	// 只是这轮没有转换，下次启动会重试。
	if err := convertSQLiteAutoVacuum(context.Background(), conn); err != nil {
		log.Printf("[db] 主库增量回收转换失败（不影响启动）：%v", err)
	}
	if err := d.seedBuiltins(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("seed builtins: %w", err)
	}
	// PRAGMA optimize 让查询规划器在启动时收集一次统计(长期运行的桌面库里表行数
	// 变化很大,默认统计会过期)。失败不影响启动:它只是让后续查询慢一点。
	_, _ = conn.ExecContext(context.Background(), "PRAGMA optimize")

	// 只读连接池：与主池同一个文件，但带 query_only(ON)。读路径(统计/列表/导出)
	// 用它，任何误落到读路径的写语句都会当场报 readonly database，而不是悄悄抢写锁。
	// 内存库不能用：file::memory: 每个连接都是一个新库，第二个池会看到空库。
	if !strings.Contains(dsn, ":memory:") && !strings.Contains(dsn, "mode=memory") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		rd, rerr := sql.Open("sqlite", dsn+sep+"_pragma=query_only(ON)")
		if rerr == nil && rd.Ping() == nil {
			rd.SetMaxOpenConns(3)
			rd.SetMaxIdleConns(3)
			d.rd = rd
		} else if rd != nil {
			rd.Close()
			log.Printf("[db] 只读连接池打开失败（回落到主池）：%v", rerr)
		}
	}
	return d, nil
}

// sqliteMaintenancePragmas is the set the application relies on. It is applied
// per connection through the DSN, but a reader that wants to assert on them
// needs the same list. The busy budget comes from config.SQLiteBusyTimeout so
// this list cannot drift from the DSN it describes.
var sqliteMaintenancePragmas = []string{
	"PRAGMA busy_timeout = " + strconv.FormatInt(config.SQLiteBusyTimeout.Milliseconds(), 10),
	"PRAGMA journal_mode = WAL",
	"PRAGMA synchronous = NORMAL",
	"PRAGMA foreign_keys = ON",
}

// openSQLiteTemp opens a throwaway database for tests and tooling.
//
// The DSN comes from config.SQLiteDSNFor so the pragma and format list stays a
// single contract with the production path. Hand-assembling it here previously
// omitted _texttotime and _txlock, which made anything using this helper fail on
// time.Time scans and run write transactions without immediate locking --
// i.e. it silently tested a different database configuration than production.
func openSQLiteTemp(dir, name string) (*DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return openSQLite(config.SQLiteDSNFor(
		strings.TrimRight(dir, "/\\") + string(os.PathSeparator) + name))
}

package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/Autumn-27/artex/config"

	_ "modernc.org/sqlite"
)

//go:embed schema_sqlite.sql
var schemaSQLiteSQL string

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

	d := &DB{sqlDB}
	// One connection for schema application and seeding, so the two cannot
	// interleave and so a partially-created file is not left behind by a
	// failed seed.
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	defer conn.Close()

	if err := applySQLiteSchema(context.Background(), conn); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := d.seedBuiltins(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("seed builtins: %w", err)
	}
	return d, nil
}

// sqliteMaintenancePragmas is the set the application relies on. It is applied
// per connection through the DSN, but a reader that wants to assert on them
// needs the same list.
var sqliteMaintenancePragmas = []string{
	"PRAGMA busy_timeout = 5000",
	"PRAGMA journal_mode = WAL",
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

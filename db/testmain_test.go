package db

import (
	"database/sql"
	"os"
	"testing"
)

// TestMain serialises the db test suite when it runs against PostgreSQL.
//
// The package holds a PostgreSQL advisory lock (7337741002) for the whole run.
// Packages agent and server hold the same lock, so a parallel `go test ./...`
// serialises across packages on the shared dev DB and avoids cross-package
// cleanup races (e.g. DELETE FROM assets WHERE id > X from one package deleting
// assets created by another).
//
// SQLite needs none of this and cannot provide it: the lock is a server-side
// facility, whereas SQLite's guarantee is structural -- the database file is
// locked for the duration of a write transaction, and busy_timeout makes a
// contended writer wait rather than fail. Attempting the lock would only turn
// a correct no-op into an error and make every test look unavailable.
func TestMain(m *testing.M) {
	if engineIsSQLite() {
		os.Exit(m.Run())
	}

	dsn, _, err := DSN()
	if err != nil {
		// No DB configured — tests that need a database will skip themselves.
		os.Exit(m.Run())
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil || conn.Ping() != nil {
		os.Exit(m.Run())
	}
	defer conn.Close()
	if _, err := conn.Exec(`SELECT pg_advisory_lock(7337741002)`); err != nil {
		os.Exit(m.Run())
	}
	defer conn.Exec(`SELECT pg_advisory_unlock(7337741002)`) //nolint:errcheck
	os.Exit(m.Run())
}

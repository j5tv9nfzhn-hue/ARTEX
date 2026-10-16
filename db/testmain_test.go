package db

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain gives the suite its own database file.
//
// ARTEX is SQLite-only, so the package's tests would otherwise open the real
// database next to the config file — running `go test ./...` would mutate a
// user's data. Pointing ARTEX_SQLITE_PATH at a fresh file under the OS temp dir
// keeps the suite hermetic and makes it reproducible on any machine.
//
// The previous harness held a PostgreSQL advisory lock (7337741002) across the
// whole run to serialise the db, agent and server packages against one shared
// dev database. That coordination is gone with the shared server: each package
// now gets its own file, and SQLite serialises writers on the file itself
// (busy_timeout makes a contended writer wait rather than fail), so there is
// nothing left for a cross-package lock to protect.
//
// An explicitly configured ARTEX_SQLITE_PATH is respected, so a developer can
// still point the suite at a specific file to inspect it afterwards.
func TestMain(m *testing.M) {
	dir := ""
	if os.Getenv(envSQLitePathForTest) == "" {
		var err error
		dir, err = os.MkdirTemp("", "artex-db-test-")
		if err != nil {
			panic(err)
		}
		_ = os.Setenv(envSQLitePathForTest, filepath.Join(dir, "test.db"))
	}
	// os.Exit skips defers, so cleanup has to happen before it rather than
	// through one — otherwise every run leaves a database file in %TEMP%.
	code := m.Run()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}

// envSQLitePathForTest is config's ARTEX_SQLITE_PATH. Duplicated as a literal
// because the constant lives in an unexported config identifier and this file
// must not depend on that name.
const envSQLitePathForTest = "ARTEX_SQLITE_PATH"

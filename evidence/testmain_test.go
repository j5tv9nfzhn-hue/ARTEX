package evidence

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain gives the suite its own database file.
//
// The previous harness required an explicitly configured ARTEX_PG_DSN and held a
// PostgreSQL advisory lock (7337741002) for the whole run. ARTEX is SQLite-only
// now, so the suite needs no configuration at all and no cross-package lock:
// each package gets its own file, and SQLite serialises writers on the file.
func TestMain(m *testing.M) {
	dir := ""
	if os.Getenv(envSQLitePathForTest) == "" {
		var err error
		dir, err = os.MkdirTemp("", "artex-evidence-test-")
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

// envSQLitePathForTest is config's ARTEX_SQLITE_PATH, spelled out so this file
// does not depend on the unexported identifier in the config package.
const envSQLitePathForTest = "ARTEX_SQLITE_PATH"

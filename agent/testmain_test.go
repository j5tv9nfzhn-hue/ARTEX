package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain gives the suite its own database file.
//
// ARTEX is SQLite-only, so without this the package's tests would open the real
// database next to the config file and mutate a user's data. The previous harness
// instead held a PostgreSQL advisory lock (7337741002) to serialise the db, agent
// and server packages against one shared dev database; with a file per package
// there is no shared server left to coordinate.
func TestMain(m *testing.M) {
	dir := ""
	if os.Getenv(envSQLitePathForTest) == "" {
		var err error
		dir, err = os.MkdirTemp("", "artex-agent-test-")
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

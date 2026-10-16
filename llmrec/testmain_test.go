package llmrec

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain gives the suite its own database file.
//
// TestSideUsageRecordedOnceOnConsumerCancellation opens a real store to exercise
// the usage-recording path. ARTEX is SQLite-only, so without this the test would
// open the database next to the config file and mutate a user's data.
func TestMain(m *testing.M) {
	const envSQLitePath = "ARTEX_SQLITE_PATH"
	dir := ""
	if os.Getenv(envSQLitePath) == "" {
		var err error
		dir, err = os.MkdirTemp("", "artex-llmrec-test-")
		if err != nil {
			panic(err)
		}
		_ = os.Setenv(envSQLitePath, filepath.Join(dir, "test.db"))
	}
	// os.Exit skips defers, so cleanup has to happen before it rather than
	// through one — otherwise every run leaves a database file in %TEMP%.
	code := m.Run()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}

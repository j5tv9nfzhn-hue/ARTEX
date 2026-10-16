package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain gives the suite its own database file and disables the background
// loops that would otherwise mutate test fixtures out of band.
//
// ARTEX is SQLite-only, so without the file redirection the package's tests would
// open the real database next to the config file and mutate a user's data. The
// previous harness instead held a PostgreSQL advisory lock (7337741002) to
// serialise db/agent/server against one shared dev database; with a file per
// package there is no shared server left to coordinate.
func TestMain(m *testing.M) {
	// server.New starts the realtime notifier's background delivery loop
	// (notifyTick) unless this env var is set. The notify integration tests
	// drive stepRealtime/stepDigest directly (see newNotifyFixture) and only
	// that fixture disabled the loop per-test. Other tests construct a server
	// with a live context.Background() that is never cancelled, so their
	// notifier goroutine leaks and keeps ticking for the rest of the binary;
	// when it fires inside a later test's window it claims that test's
	// deliveries out of band and corrupts the exact per-delivery attempt
	// accounting. That is what made TestNotifyDeliveriesHistoryAndRetry flake
	// intermittently ("重试耗尽后应为 failed，得到 pending"). No test asserts on the
	// background loop itself, so disable it suite-wide for determinism; the
	// loop's dispatch is covered through direct stepRealtime/stepDigest calls.
	os.Setenv(notifyBackgroundDisabledEnv, "1")

	// Same reasoning for the archive worker: several Servers in one test binary
	// would leak workers that race each other for archive jobs, and under
	// SQLite's single-writer model they would push each other into SQLITE_BUSY.
	// The queue itself is driven directly by the tests (runOneTaskArchiveJob);
	// background polling is only needed in production.
	os.Setenv(taskArchiveWorkerDisabledEnv, "1")

	dir := ""
	if os.Getenv(envSQLitePathForTest) == "" {
		var err error
		dir, err = os.MkdirTemp("", "artex-server-test-")
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

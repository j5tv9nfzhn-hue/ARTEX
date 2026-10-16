package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeConfig marshals a config file so paths with Windows separators survive:
// interpolating a raw `C:\...` path into a JSON string literal produces invalid
// escapes and silently yields a zero Config, which would make these tests pass
// for the wrong reason.
func writeConfig(t *testing.T, path string, cfg Config) {
	t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The startup path no longer has a "database not configured" failure mode: SQLite
// always resolves to a file next to the config, so the only thing left to pin is
// that the resolved location is honoured verbatim, and that a config file which
// simply does not exist is not an error.
func TestSQLitePathFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv(envSQLitePath, "")
	t.Setenv("ARTEX_CONFIG", cfgPath)

	want := filepath.Join(dir, "custom.db")
	writeConfig(t, cfgPath, Config{Database: Database{Path: want}})
	if got := SQLitePath(); got != want {
		t.Fatalf("配置文件的 database.path 未生效：got %q want %q", got, want)
	}

	// Missing config file: falls back to the built-in default beside the config
	// path rather than erroring, so a fresh install boots with no configuration.
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "nope.json"))
	if got := SQLitePath(); filepath.Base(got) != sqliteFileName {
		t.Fatalf("配置文件缺失时应回退默认库文件名：got %q", got)
	}
}

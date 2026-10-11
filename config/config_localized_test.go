package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hasHan 检查中文 Han 字符，用于确认源消息已译为中文。
// 中文应保留，韩文不得残留，ASCII 技术标识不受影响。
func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

func hasHangul(s string) bool {
	for _, r := range s {
		if r >= 0xac00 && r <= 0xd7a3 {
			return true
		}
	}
	return false
}

func assertChinese(t *testing.T, label, s string) {
	t.Helper()
	if hasHangul(s) {
		t.Errorf("%s: 韩文残留: %q", label, s)
	}
	if !hasHan(s) {
		t.Errorf("%s: 未包含中文: %q", label, s)
	}
}

// TestPostgresDSNErrorLocalized pins the install-path startup message to Simplified Chinese.
// When neither ARTEX_PG_DSN nor a config file supplies a database, PostgresDSN
// returns an error that propagates verbatim (db.DSN → server.NewManager → the
// `log.Fatalf("open stores: %v", err)` in cmd/artex/main.go). An operator who
// boots with missing config sees it first, so it must read as Simplified Chinese.
func TestPostgresDSNErrorLocalized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "nope.json"))
	// Pin Simplified Chinese explicitly so ARTEX_LANG in the environment cannot leak in.
	t.Setenv("ARTEX_LANG", "zh")

	_, _, err := PostgresDSN()
	if err == nil {
		t.Fatal("missing config should error")
	}
	assertChinese(t, "startup error", err.Error())

	// The identifiers an operator must act on stay verbatim (not translated).
	for _, want := range []string{"ARTEX_PG_DSN", "database", "dsn", "host/user/dbname"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("startup error should keep identifier %q verbatim: %q", want, err.Error())
		}
	}

	// What the operator actually sees after propagation (no %w wrapping anywhere
	// on the path), recorded so the message can be eyeballed in context.
	t.Logf("open stores: %v", err)
}

// TestPostgresDSNSourceLocalized pins the three source labels (logged at
// server/manager.go:361) to Simplified Chinese. They live in the same function as the
// startup error, so they are localized together to avoid a mixed-language file.
func TestPostgresDSNSourceLocalized(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	// Pin Simplified Chinese explicitly so ARTEX_LANG in the environment cannot leak in.
	t.Setenv("ARTEX_LANG", "zh")

	// env source
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "nope.json"))
	t.Setenv("ARTEX_PG_DSN", "postgres://envwins/x")
	if _, source, err := PostgresDSN(); err != nil {
		t.Fatalf("env source: %v", err)
	} else {
		assertChinese(t, "env source", source)
	}

	// config file dsn source
	t.Setenv("ARTEX_PG_DSN", "")
	if err := os.WriteFile(cfgPath, []byte(`{"database":{"dsn":"postgres://full/dsn"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_CONFIG", cfgPath)
	if _, source, err := PostgresDSN(); err != nil {
		t.Fatalf("dsn source: %v", err)
	} else {
		assertChinese(t, "dsn source", source)
	}

	// config file fields source
	if err := os.WriteFile(cfgPath, []byte(`{"database":{"host":"10.1.2.3","dbname":"d","user":"u"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, source, err := PostgresDSN(); err != nil {
		t.Fatalf("fields source: %v", err)
	} else {
		assertChinese(t, "fields source", source)
	}
}

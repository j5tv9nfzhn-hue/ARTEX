package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalizeLanguageAndChineseDefault(t *testing.T) {
	if DefaultLanguage != "zh" || !reflect.DeepEqual(SupportedLanguages, []string{"zh", "en", "es"}) {
		t.Fatalf("默认及可选语言错误：default=%q supported=%v", DefaultLanguage, SupportedLanguages)
	}
	for input, want := range map[string]string{
		"zh": "zh", " ZH ": "zh", "en": "en", " ES ": "es",
		"ko": "zh", " KO ": "zh", "": "", "fr": "",
	} {
		if got := NormalizeLanguage(input); got != want {
			t.Errorf("NormalizeLanguage(%q)=%q，预期 %q", input, got, want)
		}
	}
}

func TestLanguagePrecedenceAndLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("ARTEX_CONFIG", path)
	t.Setenv("ARTEX_LANG", "")
	if got := Language(); got != "zh" {
		t.Fatalf("无配置时应默认简体中文，实际 %q", got)
	}
	if err := os.WriteFile(path, []byte(`{"language":"es"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Language(); got != "es" {
		t.Fatalf("应使用文件配置，实际 %q", got)
	}
	t.Setenv("ARTEX_LANG", "en")
	if got := Language(); got != "en" {
		t.Fatalf("环境变量应优先于文件配置，实际 %q", got)
	}
	t.Setenv("ARTEX_LANG", "invalid")
	if got := Language(); got != "es" {
		t.Fatalf("无效环境变量不应屏蔽有效文件配置，实际 %q", got)
	}
	t.Setenv("ARTEX_LANG", "ko")
	if got := Language(); got != "zh" {
		t.Fatalf("旧 ko 环境变量应迁移为 zh，实际 %q", got)
	}
	t.Setenv("ARTEX_LANG", "")
	if err := os.WriteFile(path, []byte(`{"language":"ko"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Language(); got != "zh" {
		t.Fatalf("旧 ko 文件配置应迁移为 zh，实际 %q", got)
	}
}

func TestActiveLanguageNormalizesRuntimeSetting(t *testing.T) {
	previous := CurrentLanguage
	t.Cleanup(func() { CurrentLanguage = previous })
	CurrentLanguage = nil
	if got := ActiveLanguage(); got != "zh" {
		t.Fatalf("无运行时配置时应默认 zh，实际 %q", got)
	}
	for input, want := range map[string]string{"ko": "zh", "en": "en", " ES ": "es", "invalid": "zh", "": "zh"} {
		CurrentLanguage = func() string { return input }
		if got := ActiveLanguage(); got != want {
			t.Errorf("ActiveLanguage(%q)=%q，预期 %q", input, got, want)
		}
	}
}

package config

import (
	"os"
	"path/filepath"
	"regexp"
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

// TestStartupLogMessagesLocalized pins the operator-facing startup log templates
// (printed by cmd/artex and server.Manager) to Simplified Chinese. These are the
// first lines an operator reads, so they must not regress to English or Hangul.
func TestStartupLogMessagesLocalized(t *testing.T) {
	t.Setenv("ARTEX_LANG", "zh")
	for _, key := range []string{
		"[config] 配置文件: %s",
		"[config] skill 目录: %s",
		"[db] 数据库配置来源：%s",
	} {
		if _, ok := logCatalog[key]; !ok {
			t.Errorf("日志目录缺少条目 %q", key)
			continue
		}
		assertChinese(t, key, T(key))
	}
}

// TestTLeavesUnknownTemplatesVerbatim documents that T is a passthrough for
// anything not in the catalog: log templates are added lazily, and a missing
// entry must degrade to the source string rather than to an empty line.
func TestTLeavesUnknownTemplatesVerbatim(t *testing.T) {
	const unknown = "[test] 未登记的模板 %s"
	if got := T(unknown); got != unknown {
		t.Errorf("未登记模板应原样返回：got %q want %q", got, unknown)
	}
}

// TestLanguageFallsBackToChinese covers the single supported language: any
// historical or unknown code resolves to zh rather than leaking through.
func TestLanguageFallsBackToChinese(t *testing.T) {
	for _, code := range []string{"zh", " ZH ", "ko", "en", "es", "fr", ""} {
		t.Setenv("ARTEX_LANG", code)
		if got := Language(); got != DefaultLanguage {
			t.Errorf("ARTEX_LANG=%q 时应回退 %q，实际 %q", code, DefaultLanguage, got)
		}
	}
}

// TestCatalogFormatVerbsMatch 验证各语言保留中文源模板的格式动词顺序，
// 避免日志出现 %!s(MISSING) 或参数错位。
func TestCatalogFormatVerbsMatch(t *testing.T) {
	verbs := regexpMustCompile(t, `%[-+# 0-9.]*[a-zA-Z%]`)
	for name, cat := range map[string]map[string]map[string]string{"logCatalog": logCatalog} {
		for source, tr := range cat {
			want := strings.Join(verbs.FindAllString(source, -1), " ")
			for lang, value := range tr {
				if got := strings.Join(verbs.FindAllString(value, -1), " "); got != want {
					t.Errorf("%s[%q][%s] 格式动词 = %q，预期 %q", name, source, lang, got, want)
				}
			}
		}
	}
}

// regexpMustCompile keeps the test's import list to the standard trio the rest of
// this file uses; a compile failure here is a test bug, not a product failure.
func regexpMustCompile(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	return regexp.MustCompile(pattern)
}

// TestConfigFilePathHonoursEnv pins the one remaining startup path knob.
func TestConfigFilePathHonoursEnv(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "explicit.json")
	t.Setenv("ARTEX_CONFIG", want)
	if got := Path(); got != want {
		t.Errorf("ARTEX_CONFIG 未生效：got %q want %q", got, want)
	}
	if _, err := os.Stat(want); err == nil {
		t.Errorf("Path() 不应创建文件：%s", want)
	}
}

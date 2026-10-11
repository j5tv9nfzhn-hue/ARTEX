package config

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestStartupMessageSwitchesLanguage 验证中文默认、旧 ko 别名与 en/es 切换。
// 启动消息使用环境/文件默认语言，不读取尚未可用的数据库设置。
func TestStartupMessageSwitchesLanguage(t *testing.T) {
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_CONFIG", filepath.Join(t.TempDir(), "nope.json"))
	t.Setenv("ARTEX_LANG", "")
	source := mustDSNErr(t)
	assertChinese(t, "默认启动错误", source)

	for _, lang := range []string{"zh", "ko", " ZH ", " KO ", "fr", ""} {
		t.Setenv("ARTEX_LANG", lang)
		if got := mustDSNErr(t); got != source {
			t.Errorf("%q 应使用中文源消息：got=%q source=%q", lang, got, source)
		}
	}

	for _, lang := range []string{"en", "es"} {
		t.Setenv("ARTEX_LANG", lang)
		got := mustDSNErr(t)
		if got == "" || got == source || hasHan(got) || hasHangul(got) {
			t.Errorf("%s 应使用非空译文，不能回退中文或包含韩文：%q", lang, got)
		}
		for _, id := range []string{"ARTEX_PG_DSN", "database", "dsn", "host/user/dbname"} {
			if !containsStr(got, id) {
				t.Errorf("%s 启动错误必须保留标识 %q：%q", lang, id, got)
			}
		}
	}
}

func mustDSNErr(t *testing.T) string {
	t.Helper()
	_, _, err := PostgresDSN()
	if err == nil {
		t.Fatal("缺少配置应返回错误")
	}
	return err.Error()
}

func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}

// TestCatalogFormatVerbsMatch 验证各语言保留中文源模板的格式动词顺序，
// 避免日志出现 %!s(MISSING) 或参数错位。
func TestCatalogFormatVerbsMatch(t *testing.T) {
	verbs := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)
	for name, cat := range map[string]map[string]map[string]string{"cfgCatalog": cfgCatalog, "logCatalog": logCatalog} {
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

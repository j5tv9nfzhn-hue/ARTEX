package config

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestStartupMessageAlwaysChinese 验证启动消息恒为简体中文。
// 本仓库只提供简体中文一种语言，任何语言配置值都回退到中文源消息。
// 启动消息使用环境/文件默认语言，不读取尚未可用的数据库设置。
func TestStartupMessageAlwaysChinese(t *testing.T) {
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_CONFIG", filepath.Join(t.TempDir(), "nope.json"))
	t.Setenv("ARTEX_LANG", "")
	source := mustDSNErr(t)
	assertChinese(t, "默认启动错误", source)

	for _, lang := range []string{"zh", "ko", "en", "es", " ZH ", " KO ", "fr", ""} {
		t.Setenv("ARTEX_LANG", lang)
		if got := mustDSNErr(t); got != source {
			t.Errorf("%q 应回退到中文源消息：got=%q source=%q", lang, got, source)
		}
	}

	// 启动错误必须保留定位问题所需的技术标识，不能被翻译。
	for _, id := range []string{"ARTEX_PG_DSN", "database", "dsn", "host/user/dbname"} {
		if !containsStr(source, id) {
			t.Errorf("启动错误必须保留标识 %q：%q", id, source)
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

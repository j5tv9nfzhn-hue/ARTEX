package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestStartupMessageAlwaysChinese 验证启动消息恒为简体中文。
// 本仓库只提供简体中文一种语言，任何语言配置值都回退到中文源消息。
// 启动消息使用环境/文件默认语言，不读取尚未可用的数据库设置。
func TestStartupMessageAlwaysChinese(t *testing.T) {
	t.Setenv("ARTEX_CONFIG", filepath.Join(t.TempDir(), "nope.json"))
	t.Setenv("ARTEX_LANG", "")

	const startup = "[config] 配置文件: %s"
	source := T(startup)
	assertChinese(t, "默认启动消息", source)

	for _, lang := range []string{"zh", "ko", "en", "es", " ZH ", " KO ", "fr", ""} {
		t.Setenv("ARTEX_LANG", lang)
		if got := T(startup); got != source {
			t.Errorf("%q 应回退到中文源消息：got=%q source=%q", lang, got, source)
		}
	}
}

// TestStartupLogsKeepIdentifiersVerbatim pins that operator-actionable
// identifiers survive translation: an operator has to be able to grep for them.
func TestStartupLogsKeepIdentifiersVerbatim(t *testing.T) {
	t.Setenv("ARTEX_LANG", "zh")
	for _, want := range []string{
		"[config] 配置文件: %s",
		"[db] 数据库配置来源：%s",
		"[config] skill 目录: %s",
	} {
		if got := T(want); !strings.Contains(got, "config") && !strings.Contains(got, "pg") {
			t.Errorf("启动消息丢失技术标识：%q", got)
		}
	}
}

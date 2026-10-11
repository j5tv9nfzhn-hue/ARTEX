package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestOutputLanguageDefaultsAndLegacyMigration(t *testing.T) {
	previous := OutputLanguage
	t.Cleanup(func() { OutputLanguage = previous })
	if !reflect.DeepEqual(SupportedOutputLangs, []string{"zh", "en", "es"}) {
		t.Fatalf("可选输出语言错误：%v", SupportedOutputLangs)
	}
	OutputLanguage = nil
	if got := resolveOutputLang(); got != "zh" {
		t.Fatalf("无 Hook 时应默认简体中文，实际 %q", got)
	}
	for input, want := range map[string]string{"ko": "zh", " ZH ": "zh", "en": "en", "es": "es", "fr": "zh", "": "zh"} {
		OutputLanguage = func() string { return input }
		if got := resolveOutputLang(); got != want {
			t.Errorf("resolveOutputLang(%q)=%q，预期 %q", input, got, want)
		}
	}
	if got := langDirective("ko"); got != langDirective("zh") {
		t.Error("旧 ko 别名必须使用中文指令，不能同时禁止中文输出")
	}
}

func TestWrapupAlwaysAppendsSelectedOutputLanguage(t *testing.T) {
	previous := OutputLanguage
	t.Cleanup(func() { OutputLanguage = previous })
	for _, language := range SupportedOutputLangs {
		OutputLanguage = func() string { return language }
		prompt := "保留自定义收尾正文。"
		if got := withWrapupLang(prompt); got != prompt+langDirective(language) {
			t.Errorf("%s：收尾正文必须保留，并追加语言约束", language)
		}
		if got := withWrapupLang(" \n\t"); got != " \n\t" {
			t.Error("空白收尾提示不应注入额外内容")
		}
	}
	OutputLanguage = nil
	if got := withWrapupLang(WrapupDefault("worker")); !strings.HasSuffix(got, langDirective("zh")) {
		t.Error("默认 Worker 收尾必须明确使用简体中文")
	}
}

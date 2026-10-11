package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestOutputLanguageDefaultsAndLegacyMigration(t *testing.T) {
	previous := OutputLanguage
	t.Cleanup(func() { OutputLanguage = previous })
	if !reflect.DeepEqual(SupportedOutputLangs, []string{"zh"}) {
		t.Fatalf("可选输出语言错误：%v", SupportedOutputLangs)
	}
	OutputLanguage = nil
	if got := resolveOutputLang(); got != "zh" {
		t.Fatalf("无 Hook 时应默认简体中文，实际 %q", got)
	}
	// 只保留简体中文：任何旧语言代码或未知代码都必须回退到简体中文。
	for input, want := range map[string]string{
		" ZH ": "zh", "zh": "zh", "ZH": "zh",
		"ko": "zh", "en": "zh", "es": "zh", "fr": "zh", "": "zh",
	} {
		OutputLanguage = func() string { return input }
		if got := resolveOutputLang(); got != want {
			t.Errorf("resolveOutputLang(%q)=%q，预期 %q", input, got, want)
		}
	}
	// 未知语言代码也必须渲染出中文指令，而不是空串或残留其他语言。
	if got := langDirective("ko"); got != langDirective("zh") {
		t.Error("未知语言代码必须回退到中文指令")
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

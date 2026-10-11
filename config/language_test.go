package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalizeLanguageAndChineseDefault(t *testing.T) {
	if DefaultLanguage != "zh" || !reflect.DeepEqual(SupportedLanguages, []string{"zh"}) {
		t.Fatalf("默认及可选语言错误：default=%q supported=%v", DefaultLanguage, SupportedLanguages)
	}
	// 只有简体中文被接受；旧语言代码与未知代码一律返回空串，交由上层回退。
	for input, want := range map[string]string{
		"zh": "zh", " ZH ": "zh",
		"ko": "", " KO ": "", "en": "", " ES ": "", "es": "",
		"": "", "fr": "",
	} {
		if got := NormalizeLanguage(input); got != want {
			t.Errorf("NormalizeLanguage(%q)=%q，预期 %q", input, got, want)
		}
	}
}

func TestLanguageIgnoresNonChineseConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("ARTEX_CONFIG", path)
	t.Setenv("ARTEX_LANG", "")
	if got := Language(); got != "zh" {
		t.Fatalf("无配置时应默认简体中文，实际 %q", got)
	}
	// 文件配置为简体中文时使用它。
	if err := os.WriteFile(path, []byte(`{"language":"zh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Language(); got != "zh" {
		t.Fatalf("应使用文件配置，实际 %q", got)
	}
	// 旧语言代码不再是受支持的值，必须回退到默认简体中文。
	for _, legacy := range []string{"ko", "en", "es"} {
		if err := os.WriteFile(path, []byte(`{"language":"`+legacy+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := Language(); got != "zh" {
			t.Fatalf("文件配置 %q 应回退为 zh，实际 %q", legacy, got)
		}
	}
	// 环境变量优先级高于文件配置，但同样只接受简体中文。
	t.Setenv("ARTEX_LANG", "zh")
	if got := Language(); got != "zh" {
		t.Fatalf("环境变量应优先于文件配置，实际 %q", got)
	}
	t.Setenv("ARTEX_LANG", "ko")
	if got := Language(); got != "zh" {
		t.Fatalf("环境变量 %q 应回退为 zh，实际 %q", "ko", got)
	}
}

func TestActiveLanguageNormalizesRuntimeSetting(t *testing.T) {
	previous := CurrentLanguage
	t.Cleanup(func() { CurrentLanguage = previous })
	CurrentLanguage = nil
	if got := ActiveLanguage(); got != "zh" {
		t.Fatalf("无运行时配置时应默认 zh，实际 %q", got)
	}
	// 运行时设置同样只接受简体中文，其余取值回退到默认语言。
	for input, want := range map[string]string{
		"zh": "zh", " ZH ": "zh",
		"ko": "zh", "en": "zh", " ES ": "zh", "invalid": "zh", "": "zh",
	} {
		CurrentLanguage = func() string { return input }
		if got := ActiveLanguage(); got != want {
			t.Errorf("ActiveLanguage(%q)=%q，预期 %q", input, got, want)
		}
	}
}
package agent

import "strings"

// OutputLanguage 由服务器接入运行时设置，仅决定用户可见的自然语言。
// 内部推理提示词、工具名、命令、代码、URL 和证据原文保持不变。
var OutputLanguage func() string

const defaultOutputLang = "zh"

var SupportedOutputLangs = []string{"zh", "en", "es"}

// normalizeLang 兼容遗留 ko 配置，并对未知语言回退到简体中文。
func normalizeLang(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "ko" {
		return "zh"
	}
	for _, c := range SupportedOutputLangs {
		if c == code {
			return c
		}
	}
	return defaultOutputLang
}

func resolveOutputLang() string {
	if OutputLanguage != nil {
		if code := OutputLanguage(); code != "" {
			return normalizeLang(code)
		}
	}
	return defaultOutputLang
}

// 使用明确的语言名称，避免模型混淆输出语言。
var langPhrase = map[string]string{
	"zh": "中文（简体中文）",
	"en": "英语（English）",
	"es": "西班牙语（Español）",
}

var langBare = map[string]string{
	"zh": "中文",
	"en": "英语",
	"es": "西班牙语",
}

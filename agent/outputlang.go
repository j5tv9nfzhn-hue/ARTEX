package agent

import "strings"

// OutputLanguage 由服务器接入运行时设置，仅决定用户可见的自然语言。
// 内部推理提示词、工具名、命令、代码、URL 和证据原文保持不变。
var OutputLanguage func() string

const defaultOutputLang = "zh"

// 本仓库只提供简体中文一种面向用户的语言。旧版本写入的 ko/en/es 等配置
// 一律回退到默认值，不再单独处理。
var SupportedOutputLangs = []string{"zh"}

// normalizeLang 归一化语言代码；任何非简体中文的取值都回退到默认语言。
func normalizeLang(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
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
}

var langBare = map[string]string{
	"zh": "中文",
}

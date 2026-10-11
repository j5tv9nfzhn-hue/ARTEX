package intercept

import "github.com/Autumn-27/artex/config"

// 判定标签随输出语言切换；协议枚举 allow / deny / ask 不翻译。
var judgeActionLabels = map[string]map[string]string{
	"zh": {"allow": "允许", "deny": "拦截", "ask": "询问"},
	"en": {"allow": "allow", "deny": "deny", "ask": "ask"},
	"es": {"allow": "permitir", "deny": "bloquear", "ask": "preguntar"},
}

func judgeActionLang() string {
	lang := config.ActiveLanguage()
	if _, ok := judgeActionLabels[lang]; ok {
		return lang
	}
	return "zh"
}

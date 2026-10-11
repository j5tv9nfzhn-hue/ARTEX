package server

import "github.com/Autumn-27/artex/config"

// 后端用户可见消息以简体中文为源文案，按运行时语言查找翻译。
// 对格式化消息，必须先翻译模板，再调用 fmt.Sprintf / fmt.Errorf，
// 以保留格式占位符、参数顺序和错误包装关系。
func trMsg(message string) string {
	if translations, ok := errCatalog[message]; ok {
		if value := translations[config.ActiveLanguage()]; value != "" {
			return value
		}
	}
	return message
}

// logT 翻译面向运维人员的控制台日志模板。
func logT(message string) string { return config.T(message) }

// 运行时数据库设置优先于 ARTEX_LANG、配置文件和默认简体中文。
// 此函数查询数据库，仅在填充或刷新输出语言缓存时调用。
func (s *Server) loadOutputLanguage() string {
	if s.m != nil && s.m.pg != nil {
		if v, ok, _ := s.m.pg.GetSetting(settingOutputLanguage); ok {
			if normalized := config.NormalizeLanguage(v); normalized != "" {
				return normalized
			}
		}
	}
	return config.Language()
}

func (s *Server) outputLanguage() string {
	if v, ok := s.outputLang.Load().(string); ok && v != "" {
		return v
	}
	v := s.loadOutputLanguage()
	s.outputLang.Store(v)
	return v
}

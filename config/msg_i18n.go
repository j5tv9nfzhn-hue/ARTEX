package config

// 数据库连接之前的启动阶段，操作员消息按照 Language() 翻译：
// ARTEX_LANG > 配置文件 > 简体中文。不能在此时读取运行时 settings。
// ARTEX_PG_DSN、database、dsn、host/user/dbname 等操作所需标识保持原文。
// 源消息为中文，zh 直接使用源消息，未知条目也回退到源消息。
var cfgCatalog = map[string]map[string]string{
	"环境变量 ARTEX_PG_DSN": {
		"zh": "环境变量 ARTEX_PG_DSN",
	},
	"配置文件 %s (database.dsn)": {
		"zh": "配置文件 %s (database.dsn)",
	},
	"配置文件 %s (database 字段)": {
		"zh": "配置文件 %s (database 字段)",
	},
	"找不到数据库配置：环境变量 ARTEX_PG_DSN 未设置，配置文件 %s 中也没有 database（dsn 或 host/user/dbname）配置。请创建配置文件或设置环境变量后重试": {
		"zh": "找不到数据库配置：环境变量 ARTEX_PG_DSN 未设置，配置文件 %s 中也没有 database（dsn 或 host/user/dbname）配置。请创建配置文件或设置环境变量后重试",
	},
}

// logCatalog holds operator-facing console log templates (startup + runtime
// diagnostics). Keyed by the Simplified Chinese source template; format verbs are preserved in
// the same positional order so fmt.Printf/log.Printf arguments still line up.
var logCatalog = map[string]map[string]string{
	"[config] 配置文件: %s": {
		"zh": "[config] 配置文件: %s",
	},
	"[config] 配置文件: %s (文件不存在 · 仅使用环境变量 ARTEX_PG_DSN)": {
		"zh": "[config] 配置文件: %s (文件不存在 · 仅使用环境变量 ARTEX_PG_DSN)",
	},
	"[config] skill 目录: %s": {
		"zh": "[config] skill 目录: %s",
	},
	"已暂停": { "zh": "已暂停" },
	"已恢复": { "zh": "已恢复" },
	"[auth] 已将 JWT 密钥从 %s 迁移到 %s（移出可被发现的工作区）": {
		"zh": "[auth] 已将 JWT 密钥从 %s 迁移到 %s（移出可被发现的工作区）",
	},
	"[auth] 已将新的 JWT 密钥写入 %s": {
		"zh": "[auth] 已将新的 JWT 密钥写入 %s",
	},
	"[auth] %s 少于 %d 个字符，已忽略。请使用设置令牌方式初始化": {
		"zh": "[auth] %s 少于 %d 个字符，已忽略。请使用设置令牌方式初始化",
	},
	"[auth] 生成 %s 哈希失败: %v": {
		"zh": "[auth] 生成 %s 哈希失败: %v",
	},
	"[auth] 保存 %s 失败: %v": {
		"zh": "[auth] 保存 %s 失败: %v",
	},
	"[auth] 已通过环境变量 %s 初始化管理员密码（用户名 ARTEX）": {
		"zh": "[auth] 已通过环境变量 %s 初始化管理员密码（用户名 ARTEX）",
	},
	"[auth] 管理员密码尚未设置。设置令牌为环境变量 %s 的值（不输出到日志）": {
		"zh": "[auth] 管理员密码尚未设置。设置令牌为环境变量 %s 的值（不输出到日志）",
	},
	"[auth] 生成设置令牌失败: %v": {
		"zh": "[auth] 生成设置令牌失败: %v",
	},
	"[auth] 管理员密码尚未设置。设置令牌: %s": {
		"zh": "[auth] 管理员密码尚未设置。设置令牌: %s",
	},
	"[auth] 请在首个页面（/setup）的“设置令牌”栏中输入上述值，或将其作为 POST /api/auth/init 请求体中的 setup_token 发送。该令牌不会通过网络传输，只能在此控制台日志中查看": {
		"zh": "[auth] 请在首个页面（/setup）的“设置令牌”栏中输入上述值，或将其作为 POST /api/auth/init 请求体中的 setup_token 发送。该令牌不会通过网络传输，只能在此控制台日志中查看",
	},
	"[auth] 警告：管理员密码尚未设置，但服务器绑定在 %q，其他主机也可以连接。虽然没有设置令牌无法初始化，但在完成初始设置之前请勿将此端口暴露到外部网络（默认 127.0.0.1:8787）": {
		"zh": "[auth] 警告：管理员密码尚未设置，但服务器绑定在 %q，其他主机也可以连接。虽然没有设置令牌无法初始化，但在完成初始设置之前请勿将此端口暴露到外部网络（默认 127.0.0.1:8787）",
	},
	"[auth] 拒绝初始设置：设置令牌不匹配 (client=%s)": {
		"zh": "[auth] 拒绝初始设置：设置令牌不匹配 (client=%s)",
	},
	"[auth] 已初始化管理员密码 (client=%s)": {
		"zh": "[auth] 已初始化管理员密码 (client=%s)",
	},
	"[auth] 拒绝修改密码：当前密码不匹配 (client=%s)": {
		"zh": "[auth] 拒绝修改密码：当前密码不匹配 (client=%s)",
	},
	"[auth] 已修改管理员密码，所有现有会话均已失效 (client=%s)": {
		"zh": "[auth] 已修改管理员密码，所有现有会话均已失效 (client=%s)",
	},
	"[auth] 登录失败 (client=%s)": {
		"zh": "[auth] 登录失败 (client=%s)",
	},
	"[notify] 后台通知发送循环已通过 %s 关闭（仅测试）— 不会发送漏洞 IM 通知": {
		"zh": "[notify] 后台通知发送循环已通过 %s 关闭（仅测试）— 不会发送漏洞 IM 通知",
	},
	"[mcp] 从 %s 发现并缓存了 %d 个工具": {
		"zh": "[mcp] 从 %s 发现并缓存了 %d 个工具",
	},
	"[mcp] 启动时自动发现：读取列表失败：%v": {
		"zh": "[mcp] 启动时自动发现：读取列表失败：%v",
	},
	"[mcp] 启动时自动发现 %s 失败：%v": {
		"zh": "[mcp] 启动时自动发现 %s 失败：%v",
	},
	"[tools] 已将 orchestration/platform 工具 schema 刷新为代码默认值（一次性）": {
		"zh": "[tools] 已将 orchestration/platform 工具 schema 刷新为代码默认值（一次性）",
	},
	"[reporter] 已预配置「%s」agent + finding 触发器": {
		"zh": "[reporter] 已预配置「%s」agent + finding 触发器",
	},
	"[custom-tool] 自动检测到 python 解释器：%s": {
		"zh": "[custom-tool] 自动检测到 python 解释器：%s",
	},
	"[pg] 数据库配置来源：%s": {
		"zh": "[pg] 数据库配置来源：%s",
	},
	"[mcp] browser MCP 抓包代理已连接：%s (CA %s)": {
		"zh": "[mcp] browser MCP 抓包代理已连接：%s (CA %s)",
	},
	"[mcp] 已移除 browser MCP 抓包代理配置": {
		"zh": "[mcp] 已移除 browser MCP 抓包代理配置",
	},
}

// trCfg translates a startup/operator message to the configured default language
// (Language()). Format templates are translated the same way: pass the Simplified Chinese
// template through trCfg before fmt.Sprintf/fmt.Errorf.
func trCfg(ko string) string {
	if m, ok := cfgCatalog[ko]; ok {
		if v, ok2 := m[Language()]; ok2 && v != "" {
			return v
		}
	}
	return ko
}

// T translates an operator-facing console log template to the configured default
// language (Language()). Exposed for cmd and server log.Printf call sites. Format
// verbs are preserved in order, so wrap the template only: log.Printf(config.T(ko), args...).
func T(ko string) string {
	if m, ok := logCatalog[ko]; ok {
		if v, ok2 := m[Language()]; ok2 && v != "" {
			return v
		}
	}
	return ko
}

package agent

import (
	"fmt"
	"sort"
	"strings"

	actool "github.com/Autumn-27/norma/tool"
)

type tacticalRouteRule struct {
	skillName string
	mcpServer string
	title     string
	keywords  []string
	action    string
}

var builtinTacticalRoutes = []tacticalRouteRule{
	{
		skillName: "api-recon",
		title:     "REST/GraphQL API 枚举与未授权测试",
		keywords: []string{
			"api", "swagger", "openapi", "graphql", "actuator", "wsdl", "wadl",
			"接口", "端点", "endpoint", "rest", "v1/", "v2/", "jwt", "bola", "idor",
		},
		action: "优先调用 `Skill({\"name\":\"api-recon\"})` 加载 API 侦察 SOP，并配合 `web_recon` / `http_fuzz` / `http_request` 探测文档端点与越权参数。",
	},
	{
		skillName: "playwright-cli",
		mcpServer: "Playwright",
		title:     "SPA 前端动态渲染、登录态交互与 DOM/XSS 验证",
		keywords: []string{
			"spa", "vue", "react", "angular", "dom", "xss", "前端渲染", "单页应用",
			"浏览器", "playwright", "登录态", "验证码", "滑块", "弹窗", "截图", "headless",
		},
		action: "优先调用 `Skill({\"name\":\"playwright-cli\"})` 加载无头浏览器交互规约；若已挂载 Playwright MCP，可直接通过 `ExecuteExtraTool` 调用 `mcp__Playwright__*` 工具执行页面导航与 DOM 交互。",
	},
	{
		skillName: "scopesentry-mcp",
		mcpServer: "ScopeSentry",
		title:     "ScopeSentry 外部攻击面测绘与全量资产同步",
		keywords: []string{
			"scopesentry", "scope-sentry", "子域名收集", "泛域名", "资产测绘",
			"全量资产", "外网测绘", "外部扫描", "端口扫描任务",
		},
		action: "立即调用 `Skill({\"name\":\"scopesentry-mcp\"})` 解锁并加载 ScopeSentry 联动手册，随后通过 `ExecuteExtraTool` 直接调用 `mcp__ScopeSentry__*` 工具拉取/下发测绘任务。",
	},
	{
		skillName: "web-vuln-tactics",
		title:     "Web 核心漏洞深度利用 (SQLi/SSRF/RCE/SSTI/XXE/上传/越权)",
		keywords: []string{
			"sql", "sqli", "注入", "ssrf", "rce", "命令执行", "ssti", "模板注入",
			"xxe", "上传", "upload", "lfi", "文件包含", "反序列化", "越权", "绕过", "waf",
		},
		action: "优先调用 `Skill({\"name\":\"web-vuln-tactics\"})` 获取 Web 漏洞验证与 WAF 绕过决策树，并用 `tactical_kb({\"mode\":\"vuln_patterns\",\"query\":\"...\"})` 检索载荷与复现模板。",
	},
	{
		skillName: "reverse-engineering",
		title:     "二进制/固件/APK/前端加密逆向与静态源码审计",
		keywords: []string{
			"逆向", "reverse", "elf", "pe", "apk", "固件", "firmware", "反编译",
			"ghidra", "radare2", "objdump", "加壳", "脱壳", "签名算法", "webpack", "混淆", "源码审计", "代码审计",
		},
		action: "优先调用 `Skill({\"name\":\"reverse-engineering\"})` 加载只读静态分析 SOP（动态执行须另行授权），并结合 `Grep` / `Glob` / `codec_crypto` / `tactical_kb` 定位关键校验与加解密逻辑。",
	},
	{
		skillName: "exploit-kb-patterns",
		title:     "Nday/CVE 漏洞快速画像与 PoC 载荷库检索",
		keywords: []string{
			"cve-", "cve", "nday", "1day", "poc", "exp", "shiro", "fastjson",
			"spring", "struts", "log4j", "thinkphp", "weblogic", "oa", "中间件", "组件版本",
		},
		action: "优先调用 `Skill({\"name\":\"exploit-kb-patterns\"})` 与 `tactical_kb({\"mode\":\"vuln_patterns\",\"query\":\"<组件名或CVE>\"})` 检索已知漏洞指纹与 PoC 链。",
	},
	{
		skillName: "expert-triage-pivot",
		title:     "受阻研判、WAF/403 突破、假阳性证伪与内网横向",
		keywords: []string{
			"受阻", "失败", "超时", "403", "404", "拦截", "无回显", "盲注",
			"旁路", "横向", "跳板", "提权", "权限提升", "pivot", "复盘", "卡住",
		},
		action: "立即调用 `Skill({\"name\":\"expert-triage-pivot\"})` 执行专家级原因分诊（WAF 拦截 vs 路由错误 vs 假阳性）并切换攻击路径。",
	},
}

// BuildTacticalRoutingHint inspects the current task/intent/message text and the
// agent's mounted tools/MCPs, returning a deterministic Chinese tactical routing
// directive so the agent proactively triggers matching Skills, tactical_kb, and
// MCP tools on Turn 1 without blind guessing.
func BuildTacticalRoutingHint(taskText string, tools []actool.CoreTool, def DeferredInfo) string {
	trimmed := strings.TrimSpace(taskText)
	if trimmed == "" {
		return ""
	}
	hasSkillTool := false
	hasTacticalKB := false
	for _, t := range tools {
		switch t.Name() {
		case "Skill":
			hasSkillTool = true
		case "tactical_kb":
			hasTacticalKB = true
		}
	}
	hasMCP := len(def.GlobalNames) > 0 || len(def.Deferred) > 0
	if !hasSkillTool && !hasTacticalKB && !hasMCP {
		return ""
	}

	lower := strings.ToLower(trimmed)
	var matched []tacticalRouteRule
	for _, r := range builtinTacticalRoutes {
		for _, kw := range r.keywords {
			if strings.Contains(lower, kw) {
				matched = append(matched, r)
				break
			}
		}
	}

	// Collect active MCP server names from def.GlobalNames / def.Deferred
	activeServers := map[string]bool{}
	for _, name := range def.GlobalNames {
		if srv := extractMCPServerName(name); srv != "" {
			activeServers[srv] = true
		}
	}

	var b strings.Builder
	b.WriteString("<tactical_skill_mcp_routing>\n")
	b.WriteString("【关键词路由建议（不是能力就绪证明）】\n")

	if len(matched) > 0 && hasSkillTool {
		b.WriteString("系统检测到当前任务/指令命中以下专业领域场景，**仅当下列技能出现在当前可见清单时，才调用对应 `Skill`；缺失时明确报告未配置。知识库调用以真实 mode 为准**：\n")
		for i, m := range matched {
			if i >= 3 {
				break // 最多展示前 3 条最相关路由，保持精炼高信噪比
			}
			fmt.Fprintf(&b, "- 🎯 **%s** (`%s`)：%s\n", m.title, m.skillName, m.action)
		}
	} else if hasSkillTool {
		b.WriteString("- 💡 **技能库先行**：当前已挂载 `Skill` 工具，具体技能以可见清单为准。遇到具体渗透/逆向/侦察子方向时，**请先调用 `Skill({\"name\":\"<技能名>\"})` 加载专家 SOP**，切忌盲目硬猜。\n")
	}

	if hasTacticalKB {
		b.WriteString("- 📚 **知识库联动**：遇到特定中间件/框架指纹、报错特征或 WAF 拦截时，主动调用 `tactical_kb({\"mode\":\"vuln_patterns\",\"query\":\"<关键词>\"})` 查阅历史漏洞规律与绕过载荷。\n")
	}

	if len(activeServers) > 0 {
		var srvs []string
		for s := range activeServers {
			srvs = append(srvs, s)
		}
		sort.Strings(srvs)
		fmt.Fprintf(&b, "- 🔌 **已解锁 MCP 外部工具直调**：当前已连接并解锁 MCP 服务 `[%s]`。凡任务涉及对应能力，**无需先调 SearchExtraTools，直接调用 `ExecuteExtraTool({\"tool_name\":\"mcp__<服务名>__<工具名>\",\"params\":{...}})` 一步执行**（详见系统提示词底部【已挂载 MCP 扩展工具速查】）。\n", strings.Join(srvs, ", "))
	}
	b.WriteString("</tactical_skill_mcp_routing>")
	return b.String()
}

func extractMCPServerName(toolName string) string {
	if !strings.HasPrefix(toolName, "mcp__") {
		return ""
	}
	rest := strings.TrimPrefix(toolName, "mcp__")
	if idx := strings.Index(rest, "__"); idx > 0 {
		return rest[:idx]
	}
	return ""
}

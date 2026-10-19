package agent

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

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
		skillName: "human-verification",
		title:     "人机验证、挑战识别与有界免费 PoW 求解",
		keywords:  []string{"captcha", "turnstile", "cloudflare", "人机验证", "验证码", "黄色云", "橙色云", "altcha", "anubis", "pbkdf2", "proof of work"},
		action:    "加载 Skill(human-verification)，先用 human_verification_capabilities 核对可用协议/helper，再识别响应；human_verification_solve 支持明确的 ALTCHA v1/v2 SHA/PBKDF2 与旧版 Anubis，flow 支持端到端及 protocol_only。可选 fingerprint_profile 依赖独立 Go helper，缺失不自动下载；不重复失败策略、不默认转人工。",
	},
	{
		skillName: "ai-gateway-logic",
		title:     "AI 聚合中转站登录后业务逻辑审计（计费/余额/Key 生命周期/租户归属/支付回调）",
		keywords: []string{
			"newapi", "new-api", "oneapi", "one-api", "one-hub", "litellm", "sub2api",
			"ai 中转", "ai中转", "中转站", "聚合网关", "llm gateway", "llm 网关",
			"api 中转", "token 计费", "按 token 计费", "额度充值", "兑换码",
			"/v1/models", "/api/status", "/api/setup", "/api/user/self", "/pricing",
			"remain_quota", "used_quota", "token_name", "api key 计费",
		},
		action: "优先调用 `Skill({\"name\":\"ai-gateway-logic\"})` 加载中转站业务不变量手册；同时用 `tactical_kb({\"mode\":\"logic_invariants\",\"query\":\"ai_gateway\"})` 取不变量清单（INV-01..11：结算与鉴权解耦、定价 fail-closed、数值溢出、余额真值源、Key 失效、租户 scoped、支付验签、本地信任、管理面层级、促销滥用）。这类目标表面漏洞少，重心在登录后的计费与权限链路；每条不变量须给出最小复现与账单/状态前后快照，区分『配置缺失』与『可恶意利用绕过』。",
	},
	{
		skillName: "ai-coding-sandbox-tactics",
		title:     "AI 编程平台、Next.js Server Actions、云沙箱穿透与 Supabase 审计",
		keywords: []string{
			"sandbox", "沙箱", "next.js", "nextjs", "server action", "next-action", "_next/static",
			"supabase", "anon key", "service_role", "rls", "e2b", "vercel", "在线编程", "ai 编程",
			"代码生成", "容器逃逸", "单行编码", "free.ai",
		},
		action: "优先调用 `Skill({\"name\":\"ai-coding-sandbox-tactics\"})` 加载 AI 编程平台与沙箱穿透实战包（自动附带 `references/pitfalls-and-bypasses.md` 单行 Base64 防截断绕过与 `references/attack-chains-and-pocs.md` 攻击推导链）。",
	},
	{
		skillName: "wasm-cloud-saas-tactics",
		title:     "WebAssembly (Go WASM) 逆向、WebSocket 越权、预签名云存储与 AI SaaS 计费绕过",
		keywords: []string{
			"wasm", "webassembly", "wasm_exec", "go wasm", "websocket", "ws://", "wss://",
			"presigned", "预签名", "oss", "s3", "cos", "r2", "bucket", "积分", "配额",
			"credits", "quota", "consume", "计费", "扣费", "ai saas", "notegpt",
		},
		action: "优先调用 `Skill({\"name\":\"wasm-cloud-saas-tactics\"})` 加载 Go WASM 内存钩子取密钥、WebSocket 帧级越权、预签名 Bucket 列举与客户端积分拦截实战包（自动附带 `references/*` 防踩坑与 PoC 手册）。",
	},
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
		action: "优先调用 `Skill({\"name\":\"playwright-cli\"})` 加载无头浏览器交互规约；若已挂载 Playwright MCP，可直接调用 `mcp__Playwright__*` 工具或通过 `ExecuteExtraTool` 执行页面导航与 DOM 交互。",
	},
	{
		skillName: "scopesentry-mcp",
		mcpServer: "ScopeSentry",
		title:     "ScopeSentry 外部攻击面测绘与全量资产同步",
		keywords: []string{
			"scopesentry", "scope-sentry", "子域名收集", "泛域名", "资产测绘",
			"全量资产", "外网测绘", "外部扫描", "端口扫描任务",
		},
		action: "立即调用 `Skill({\"name\":\"scopesentry-mcp\"})` 解锁并加载 ScopeSentry 联动手册，随后直接调用 `mcp__ScopeSentry__*` 工具或通过 `ExecuteExtraTool` 拉取/下发测绘任务。",
	},
	{
		skillName: "rea-mcp",
		mcpServer: "rea",
		title:     "REA 逆向引擎契约会话、能力分级与整包导出",
		keywords: []string{
			"rea", "逆向引擎", "软件逆向", "反编译", "decompile", "调用图",
			"callgraph", "交叉引用", "xref", "函数表", "rva", "多模块",
		},
		action: "优先调用 `Skill({\"name\":\"rea-mcp\"})` 加载 REA 引擎契约手册；software_re 工作台 mode=capabilities 核对 tier，mode=open/query 按 RVA 纪律查询，mode=export 整包导出，mode=quota 查配额。",
	},
	{
		skillName: "wpe64-mcp",
		mcpServer: "wpe64",
		title:     "WPE64 Winsock 抓包、改包与重放（配合 mitmproxy 记录代理）",
		keywords: []string{
			"wpe", "wpe64", "抓包", "改包", "重放", "replay", "winsock",
			"封包", "发包", "packet", "套接字",
		},
		action: "优先调用 `Skill({\"name\":\"wpe64-mcp\"})` 加载封包实验手册；启用 wpe64 MCP 后调用 `mcp__wpe64__*` 抓取/编辑/重放，与 mitmproxy 流量记录按五元组+时间对齐取证。",
	},
	{
		skillName: "mitmproxy-traffic",
		title:     "mitmproxy 流量录制、解密取证与精确重放",
		keywords: []string{
			"mitmproxy", "流量记录", "流量取证", "流量回放", "https 解密",
			"http 代理", "har", "抓包分析",
		},
		action: "优先调用 `Skill({\"name\":\"mitmproxy-traffic\"})` 加载记录代理工作流（任务 -proxy 端口落库）；非 HTTP/自定义协议切换 Skill(wpe64-mcp) 帧层联合作战。",
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

type availableSkillEntry struct {
	name        string
	description string
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
	var skillToolDesc string
	for _, t := range tools {
		switch t.Name() {
		case "Skill":
			hasSkillTool = true
			skillToolDesc = t.Description()
		case "tactical_kb":
			hasTacticalKB = true
		}
	}
	hasMCP := len(def.GlobalNames) > 0 || len(def.Deferred) > 0
	for _, t := range tools {
		if strings.HasPrefix(t.Name(), "mcp__") {
			hasMCP = true
			break
		}
	}
	if !hasSkillTool && !hasTacticalKB && !hasMCP {
		return ""
	}

	availableSkills := parseAvailableSkillsFromToolDesc(skillToolDesc)
	visibleSkillSet := make(map[string]bool, len(availableSkills))
	for _, entry := range availableSkills {
		visibleSkillSet[entry.name] = true
	}

	lower := strings.ToLower(trimmed)
	builtinNames := make(map[string]bool, len(builtinTacticalRoutes))
	var matched []tacticalRouteRule
	for _, r := range builtinTacticalRoutes {
		builtinNames[r.skillName] = true
		if len(visibleSkillSet) > 0 && !visibleSkillSet[r.skillName] {
			continue
		}
		for _, kw := range r.keywords {
			if strings.Contains(lower, kw) {
				matched = append(matched, r)
				break
			}
		}
	}

	// Match distilled case-* skills and custom/report-imported skills from <available_skills>
	dynamicMatched := matchDynamicSkills(lower, availableSkills, builtinNames)
	if len(dynamicMatched) > 0 {
		// Put highest-relevance distilled/custom skills at the front (up to 2), followed by builtin routes
		combined := make([]tacticalRouteRule, 0, len(dynamicMatched)+len(matched))
		combined = append(combined, dynamicMatched...)
		combined = append(combined, matched...)
		matched = combined
	}

	// Collect active MCP server names from def.GlobalNames and gated MCPs from def.Deferred / tools
	activeServers := map[string]bool{}
	for _, name := range def.GlobalNames {
		if srv := extractMCPServerName(name); srv != "" {
			activeServers[srv] = true
		}
	}
	gatedServers := map[string]bool{}
	for _, name := range def.Deferred {
		if srv := extractMCPServerName(name); srv != "" && !activeServers[srv] {
			gatedServers[srv] = true
		}
	}
	for _, t := range tools {
		if srv := extractMCPServerName(t.Name()); srv != "" && !activeServers[srv] {
			gatedServers[srv] = true
		}
	}

	var b strings.Builder
	b.WriteString("<tactical_skill_mcp_routing>\n")
	b.WriteString("【关键词路由建议（不是能力就绪证明）】\n")

	if len(matched) > 0 && hasSkillTool {
		b.WriteString("系统检测到当前任务/指令命中以下专业领域场景，**仅当下列技能出现在当前可见清单时，必须在第一轮优先调用对应 `Skill`（多文件技能包会自动附带加载 `references/*` 防踩坑与攻击链手册）；缺失时明确报告未配置。知识库调用以真实 mode 为准**：\n")
		for i, m := range matched {
			if i >= 4 {
				break // 最多展示前 4 条最相关路由（含沉淀复盘技能与内置专家技能），保持精炼高信噪比
			}
			fmt.Fprintf(&b, "- 🎯 **%s** (`%s`)：%s\n", m.title, m.skillName, m.action)
		}
	} else if hasSkillTool {
		b.WriteString("- 💡 **技能库先行**：当前已挂载 `Skill` 工具，具体技能以可见清单为准。遇到具体渗透/逆向/侦察子方向时，**请先调用 `Skill({\"name\":\"<技能名>\"})` 加载专家 SOP（含配套 `references/*` 防踩坑与 PoC 手册）**，切忌盲目硬猜。\n")
	}

	if hasTacticalKB {
		b.WriteString("- 📚 **知识库联动**：遇到特定中间件/框架指纹、报错特征、逆向样本或 WAF 拦截时，主动调用 `tactical_kb({\"mode\":\"vuln_patterns\"|\"case_skills\"|\"skill_search\",\"query\":\"<关键词>\"})` 查阅历史漏洞规律、沉淀技能与绕过载荷。\n")
		b.WriteString("- 🧮 **业务不变量（logic_invariants）**：目标为 LLM 聚合中转站、计费/配额网关、SaaS 额度系统等『表面无漏洞、风险在登录后资金与权限链路』的业务时，调用 `tactical_kb({\"mode\":\"logic_invariants\",\"query\":\"ai_gateway\"})` 取『必须成立』的断言清单，逐条转成待验证假设；违反须给最小复现与账单/状态前后快照，区分配置缺失与可恶意利用绕过。\n")
	}

	if len(activeServers) > 0 {
		var srvs []string
		for s := range activeServers {
			srvs = append(srvs, s)
		}
		sort.Strings(srvs)
		fmt.Fprintf(&b, "- 🔌 **已解锁 MCP 外部工具直调**：当前已连接并解锁 MCP 服务 `[%s]`。凡任务涉及对应能力，**可直接以工具全名 `mcp__<服务名>__<工具名>({...})` 发起工具调用，或通过 `ExecuteExtraTool({\"tool_name\":\"mcp__<服务名>__<工具名>\",\"params\":{...}})` 一步执行（无需先调 SearchExtraTools）**（详见系统提示词底部【已挂载 MCP 扩展工具速查】）。\n", strings.Join(srvs, ", "))
	}
	if len(gatedServers) > 0 {
		var gsrvs []string
		for s := range gatedServers {
			gsrvs = append(gsrvs, s)
		}
		sort.Strings(gsrvs)
		fmt.Fprintf(&b, "- 🔐 **技能关联 MCP 待解锁**：检测到技能门控 MCP 服务 `[%s]`，调用对应关联 `Skill` 后即可自动解锁并直接调用其工具。\n", strings.Join(gsrvs, ", "))
	}
	b.WriteString("</tactical_skill_mcp_routing>")
	return b.String()
}

func parseAvailableSkillsFromToolDesc(desc string) []availableSkillEntry {
	start := strings.Index(desc, "<available_skills>")
	end := strings.Index(desc, "</available_skills>")
	if start < 0 || end <= start {
		return nil
	}
	block := desc[start+len("<available_skills>") : end]
	lines := strings.Split(block, "\n")
	var out []availableSkillEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		rest := strings.TrimPrefix(line, "- ")
		colon := strings.Index(rest, ":")
		if colon <= 0 {
			continue
		}
		name := strings.TrimSpace(rest[:colon])
		summary := strings.TrimSpace(rest[colon+1:])
		if name != "" {
			out = append(out, availableSkillEntry{name: name, description: summary})
		}
	}
	return out
}

var dynamicSkillStopwords = map[string]bool{
	"case": true, "web": true, "re": true, "skill": true, "test": true, "report": true,
	"web_security": true, "software_re": true,
	"任务复盘经验": true, "围绕": true, "已验证": true, "条路径": true,
	"发现": true, "个漏洞": true, "复测": true, "个样本": true, "含关键词": true,
	"遇到同类目标或同类故障时加载": true, "安全测试": true, "渗透测试": true,
	"含配套": true, "references": true, "防踩坑与攻击链": true, "手册": true, "调用即自动加载": true,
}

func extractDynamicSkillKeywords(name, desc string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || dynamicSkillStopwords[s] || seen[s] {
			return
		}
		if utf8.RuneCountInString(s) < 2 {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	add(name)
	for _, part := range strings.Split(name, "-") {
		if len(part) >= 3 && !isAllDigits(part) {
			add(part)
		}
	}

	// Extract quoted title inside 「...」
	if l := strings.Index(desc, "「"); l >= 0 {
		rest := desc[l+len("「"):]
		if r := strings.Index(rest, "」"); r > 0 {
			quoted := rest[:r]
			add(quoted)
			for _, tok := range splitSkillTokens(quoted) {
				add(tok)
			}
		}
	}

	// Extract explicit keywords after "含关键词"
	if idx := strings.Index(desc, "含关键词"); idx >= 0 {
		rest := strings.TrimSpace(desc[idx+len("含关键词"):])
		if dot := strings.Index(rest, "。"); dot >= 0 {
			rest = rest[:dot]
		}
		for _, kw := range strings.FieldsFunc(rest, func(r rune) bool {
			return r == '、' || r == ',' || r == '，' || r == ';' || r == '；'
		}) {
			add(kw)
			for _, tok := range splitSkillTokens(kw) {
				add(tok)
			}
		}
	}

	for _, tok := range splitSkillTokens(desc) {
		add(tok)
	}
	return out
}

func splitSkillTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) ||
			r == '（' || r == '）' || r == '「' || r == '」' || r == '、' || r == '，' || r == '。' || r == '：' || r == '；' || r == '[' || r == ']'
	})
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func matchDynamicSkills(lowerTask string, available []availableSkillEntry, builtinNames map[string]bool) []tacticalRouteRule {
	type scored struct {
		rule  tacticalRouteRule
		score int
	}
	var matches []scored
	for _, entry := range available {
		if builtinNames[entry.name] {
			continue
		}
		keywords := extractDynamicSkillKeywords(entry.name, entry.description)
		score := 0
		for _, kw := range keywords {
			if strings.Contains(lowerTask, kw) {
				if kw == strings.ToLower(entry.name) || utf8.RuneCountInString(kw) >= 4 {
					score += 2
				} else {
					score += 1
				}
			}
		}
		if score == 0 {
			continue
		}
		title := "平台沉淀/自定义专家技能"
		if strings.HasPrefix(entry.name, "case-") || strings.HasPrefix(entry.name, "merged-") {
			title = "历史任务沉淀复盘经验"
		} else if strings.HasPrefix(entry.name, "report-") {
			title = "外部报告蒸馏实战技能包"
		}
		matches = append(matches, scored{
			score: score,
			rule: tacticalRouteRule{
				skillName: entry.name,
				title:     title,
				action:    fmt.Sprintf("当前任务与已沉淀技能高度相关（%s），优先调用 `Skill({\"name\":%q})` 复用已验证路径、关键证据与止损规则（含配套 `references/*` 手册）。", entry.description, entry.name),
			},
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].rule.skillName < matches[j].rule.skillName
	})
	if len(matches) > 2 {
		matches = matches[:2]
	}
	out := make([]tacticalRouteRule, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.rule)
	}
	return out
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

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/skills"
	"github.com/Autumn-27/norma/skill"
	actool "github.com/Autumn-27/norma/tool"
)

type vulnPatternCard struct {
	Category    string   `json:"category"`
	Title       string   `json:"title"`
	HighYield   []string `json:"high_yield_vectors"`
	FastProbes  []string `json:"fast_probes"`
	StopSignals []string `json:"stop_signals_3_strike"`
	NativeTools []string `json:"recommended_native_tools"`
}

var builtinVulnPatternCards = []vulnPatternCard{
	{
		Category: "bola_idor",
		Title:    "BOLA / BFLA / 多租户隔离穿透（最高赏金产出）",
		HighYield: []string{
			"次级子路由漏检：GET/POST 主资源有鉴权，但 /export、/clone、/archive、/logs、/members、/webhooks、/batch 漏检归属",
			"路径与 Body 复合 ID 错配：URL 路径传自己的 tenant_id/user_id 过网关，JSON Body 内传受害者 id/org_id/project_id",
			"低权限角色垂直越权（BFLA）：普通成员 Token 直接调用管理员专属写接口（PATCH /role、DELETE、/admin/*）",
		},
		FastProbes: []string{
			"使用 http_sequence(mode=\"bola\") 同时传入会话 A（低权限/攻击者）与会话 B（资源归属者）Header，自动对比状态码与 JSON 相似度",
			"将数值 ID 替换为相邻整数，或从公开接口/抓包历史（traffic_search）中提取另一账号 UUID 进行交叉重放",
		},
		StopSignals: []string{
			"若授权范围内的请求持续被拒绝，应停止低收益重复尝试；403/404 不能证明 ORM 全局过滤或其他字段安全，记录实际请求上下文及未验证范围",
		},
		NativeTools: []string{"http_sequence(mode=\"bola\")", "http_request", "extract_data(mode=\"json_path\")"},
	},
	{
		Category: "auth_mfa",
		Title:    "认证状态机、MFA/2FA 步进验证（AAL）与密码重置绕过",
		HighYield: []string{
			"AAL1 半途令牌越权：完成密码登录拿到 aal1 JWT 后不走 /mfa/verify，直接调用业务写接口或次级微服务（排查漏检 aal2 的接口）",
			"备选登录通道绕过 MFA：主登录口强制 MFA，但 /browser-auth/*、OAuth refresh_token、Magic Link 或 API Key 兑换接口直接签发完整权限会话",
			"跨账号凭据错配：用账号 B 的 Bearer Token 提交账号 A 的 password_reset_token / factor_id / challenge_id / email_verification_code",
			"Pre-Account Takeover：先用受害者邮箱普通注册（未验证邮箱），再触发 OAuth 同邮箱合并观察是否保留旧密码",
		},
		FastProbes: []string{
			"调用 codec_crypto(operation=\"jwt_decode\") 解码各阶段 Token，对比 aal、amr、role、scope、email_verified 字段差异",
			"调用 http_sequence(mode=\"replay\") 拿着 aal1 或未验证邮箱的 Token 批量探测核心读写端点",
		},
		StopSignals: []string{
			"若已测路由返回固定 403 Need AAL2，记录已测样本与访问拒绝；拦截来源和其他子服务状态仍需证据，不外推为全局结论",
		},
		NativeTools: []string{"http_sequence(mode=\"chain\"|\"replay\")", "codec_crypto(operation=\"jwt_decode\")", "http_request"},
	},
	{
		Category: "jwt_oauth",
		Title:    "JWT 签名缺陷与 OAuth / SSO 授信劫持",
		HighYield: []string{
			"JWT alg:none 签名剥离、HS256 弱密钥爆破、Header kid 路径遍历（../../dev/null）或 jwk/jku 自嵌入公钥",
			"OAuth redirect_uri 校验绕过（前缀伪造、路径遍历 /callback/../../open-redirect、参数污染）窃取 code/token",
			"OAuth state 参数缺失或固定导致跨站绑定（CSRF 绑定攻击者第三方账号）",
		},
		FastProbes: []string{
			"调用 codec_crypto(operation=\"jwt_none\") 生成无签名 Token 实测 1 次",
			"调用 codec_crypto(operation=\"jwt_crack\") 使用内置高频弱密钥字典离线秒级爆破（零网络请求）",
		},
		StopSignals: []string{
			"当前令牌变体被拒且有限字典未命中时，可基于范围与成本停止该方法；不能由此证明签名实现或其他令牌均安全",
		},
		NativeTools: []string{"codec_crypto(operation=\"jwt_decode\"|\"jwt_none\"|\"jwt_crack\"|\"jwt_sign\")", "http_request"},
	},
	{
		Category: "graphql_api",
		Title:    "GraphQL / OpenAPI 隐藏操作与批量越权",
		HighYield: []string{
			"GraphQL Introspection 暴露内部 Mutation（如 updateRole、impersonateUser、debug*）",
			"GraphQL 关闭内省时通过字段拼写错误建议（Did you mean ...）还原隐藏 Schema，或利用 JSON 数组 Batching 绕过验证码/登录速率限制",
			"Swagger / OpenAPI 文档残留旧版 API（/api/v1 vs /api/v2），旧版缺少新增的鉴权中间件",
		},
		FastProbes: []string{
			"调用 web_recon(mode=\"graphql\", url=\"...\") 一键探测内省并提取全部 Query/Mutation",
			"调用 web_recon(mode=\"openapi\", url=\"...\") 自动拉取并解析全量 REST 端点契约",
		},
		StopSignals: []string{
			"若当前请求未观察到 GraphQL 或 OpenAPI，记录发现范围；必要时在授权范围内改用 web_recon(mode=\"spa_harvest\"|\"route_map\") 从前端 JS 提取路由",
		},
		NativeTools: []string{"web_recon(mode=\"graphql\"|\"openapi\"|\"route_map\")", "http_sequence"},
	},
	{
		Category: "ssrf_cloud",
		Title:    "SSRF 服务端请求伪造、Webhook 透传与云元数据窃取",
		HighYield: []string{
			"自定义 LLM Provider BaseURL / Webhook 测试连接 / 远程头像或文档 URL 导入 / PDF 渲染接口外联内网",
			"更新已保存的第三方集成配置时，仅修改 baseUrl/endpoint 为攻击者地址而不传 apiKey，诱导后端把数据库里原加密保存的 apiKey 发往攻击者服务器",
			"云元数据穿透：169.254.169.254（AWS/OpenStack）、metadata.google.internal（GCP）、100.100.100.200（阿里云）",
		},
		FastProbes: []string{
			"调用 net_probe(mode=\"oast_callback\") 启动本地临时回连监听或使用基准内外网端口测响应时间/报错差分",
			"测试 3 种正交绕过：302 重定向、[::ffff:127.0.0.1] / 十进制 IP、nip.io / sslip.io 泛解析域名",
		},
		StopSignals: []string{
			"若授权范围内的请求持续失败，停止该路径并记录观察；需独立证据区分 Egress 拒绝、客户端请求与服务端请求，不能仅凭失败排除 SSRF",
		},
		NativeTools: []string{"net_probe(mode=\"oast_callback\"|\"port_scan\")", "http_request"},
	},
	{
		Category: "race_toctou",
		Title:    "高并发条件竞争（Race Condition / TOCTOU 双花与配额突破）",
		HighYield: []string{
			"额度/积分/优惠券/免费次数扣减：并发齐射导致单次余额被多次消费",
			"唯一性配额突破：免费账户限制创建 1 个团队/项目/API Key/试用实例，并发 15 请求突破上限",
			"一次性凭证复用：单次有效的重置码、邀请链接、备份验证码在极短窗口内被并发核销多次",
		},
		FastProbes: []string{
			"调用 http_sequence(mode=\"race\", race_count=15, steps=[...]) 通过内存级同步屏障在同一毫秒齐射请求，检查成功响应数是否 > 1",
		},
		StopSignals: []string{
			"若本次有限并发试验仅一次成功，其余返回冲突，记录该轮结果与条件；不能据此断言底层锁机制、普遍原子性或不同负载下结果相同",
		},
		NativeTools: []string{"http_sequence(mode=\"race\")"},
	},
	{
		Category: "js_reverse",
		Title:    "前端混淆 JS、SourceMap 还原与请求签名（sign/nonce/AES）逆向",
		HighYield: []string{
			"生产环境残留 .js.map 文件，直接还原完整前端 TypeScript 源码与注释",
			"前端 axios/fetch 请求拦截器内硬编码 HMAC-SHA256 / AES-CBC / RSA 密钥及待签串拼接逻辑",
			"前端权限树（perm_tree）硬编码角色判断，暴露后端未设防的管理接口",
		},
		FastProbes: []string{
			"调用 web_recon(mode=\"sourcemap\"|\"spa_harvest\"|\"perm_tree\") 收割前端产物",
			"用 Grep 搜索 X-Sign / Hmac / AES / interceptors，用 Read 读局部 50 行，再用 codec_crypto 对照抓包正样本 100% 验算签名",
		},
		StopSignals: []string{
			"未用 codec_crypto 将正样本签名验算到 100% 一致前，严禁盲目发包 Fuzz",
		},
		NativeTools: []string{"web_recon(mode=\"sourcemap\"|\"spa_harvest\"|\"perm_tree\")", "extract_data(mode=\"js_secrets\")", "codec_crypto", "Grep", "Read"},
	},
	{
		Category: "sqli_injection_triage",
		Title:    "参数注入（SQLi / NoSQLi / SSTI / RCE）快速定性与强类型止损",
		HighYield: []string{
			"弱类型动态拼接参数：order_by / sort / filter / search / raw SQL 报表接口",
			"NoSQL 操作符注入：JSON 传 {\"$ne\": null}、{\"$gt\": \"\"}、{\"$regex\": \"^a\"}",
			"模板/表达式注入：邮件模板预览、自定义报表公式、Webhook 载荷模板（{{7*7}}、${7*7}）",
		},
		FastProbes: []string{
			"每个可疑槽位最多发送 3 个正交探针：① 单引号/反斜杠语法破坏对比双引号闭合；② 布尔恒真 vs 恒假或数值运算 7*7；③ 时间延迟探针（用 http_sequence(mode=\"timing\") 自动核验基线防抖）",
		},
		StopSignals: []string{
			"若连续观察到相同拒绝/错误响应，应停止低收益重复尝试，记录状态、请求上下文与证据。422 或固定 400/500 不能证明技术栈、参数化查询或漏洞不存在；解释必须标为推断",
		},
		NativeTools: []string{"http_request", "http_sequence(mode=\"timing\"|\"diff\")"},
	},
}

func (t *ToolSet) tacticalKBTool() actool.CoreTool {
	return readTool(
		"tactical_kb",
		"跨任务战术知识库与专家战法秒级检索工具（零外部进程开销）。支持 5 种模式：\n"+
			"- past_findings：检索本平台历史漏洞记录（逐条返回状态；非误报不等于已确认）（按域名、漏洞类型、关键词匹配，直接复用过往成功攻击链与 PoC）；\n"+
			"- past_assets：跨历史任务检索已探明的目标资产、端口、技术栈指纹与历史 API 端点；\n"+
			"- case_skills：专项检索 AI 自主沉淀的历史任务复盘技能（case-web-* / case-re-* / merged-*），结构化返回已验证路径、漏洞/复测/逆向证据与 3-Strike 止损经验；\n"+
			"- skill_search：按关键词全文检索本地与内置专家技能包（SKILL.md）的章节切片，无需整篇加载即可获取关键步骤；\n"+
			"- vuln_patterns：按漏洞分类（bola_idor / auth_mfa / jwt_oauth / graphql_api / ssrf_cloud / race_toctou / js_reverse / sqli_injection_triage）获取本地静态经验卡片（不构成在线全球知识库或结论证明）、止损建议与推荐原生工具。",
		obj(map[string]any{
			"mode":     str("必填：检索模式，可选 past_findings | past_assets | case_skills | skill_search | vuln_patterns"),
			"query":    str("可选：检索关键词（域名、端点路径、技术栈如 fastapi/supabase/graphql、漏洞词如 mfa/jwt/bola/ssrf、逆向样本或函数名）"),
			"category": str("可选：vuln_patterns 模式下指定分类（bola_idor | auth_mfa | jwt_oauth | graphql_api | ssrf_cloud | race_toctou | js_reverse | sqli_injection_triage），留空返回全部分类概览"),
			"limit":    intp("可选：最大返回条数（默认 8，上限 25）"),
		}, "mode"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Mode     string `json:"mode"`
				Query    string `json:"query"`
				Category string `json:"category"`
				Limit    int    `json:"limit"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			mode := strings.ToLower(strings.TrimSpace(a.Mode))
			q := strings.TrimSpace(a.Query)
			limit := a.Limit
			if limit <= 0 {
				limit = 8
			}
			if limit > 25 {
				limit = 25
			}

			switch mode {
			case "past_findings":
				var d *db.DB
				if t.as != nil {
					d = t.as.DB()
				}
				if d == nil {
					return jsonResult(map[string]any{
						"mode":     mode,
						"findings": []any{},
						"note":     "当前运行上下文未挂载全局数据库，请改用 mode=\"vuln_patterns\" 或 mode=\"skill_search\"",
					})
				}
				rows, total, err := d.ListFindingsPage(db.FindingFilter{
					Query: q,
					Sort:  "severity",
				}, 1, limit)
				if err != nil {
					return actool.Errorf("检索历史漏洞失败: " + err.Error()), nil
				}
				type findingBrief struct {
					ID        int64  `json:"id"`
					TaskID    *int64 `json:"task_id,omitempty"`
					VulnClass string `json:"vuln_class"`
					Name      string `json:"name"`
					Severity  string `json:"severity"`
					Status    string `json:"status"`
					Summary   string `json:"summary"`
					Evidence  string `json:"evidence_snippet"`
				}
				out := make([]findingBrief, 0, len(rows))
				for _, r := range rows {
					if r.Status == "false_positive" {
						continue
					}
					ev := strings.TrimSpace(r.Evidence)
					if len(ev) > 450 {
						ev = clipUTF8(ev, 450) + "...(truncated)"
					}
					out = append(out, findingBrief{
						ID:        r.ID,
						TaskID:    r.TaskID,
						VulnClass: r.VulnClass,
						Name:      r.Name,
						Severity:  r.Severity,
						Status:    r.Status,
						Summary:   r.Summary,
						Evidence:  ev,
					})
				}
				return jsonResult(map[string]any{
					"mode":          mode,
					"query":         q,
					"total_matched": total,
					"returned":      len(out),
					"findings":      out,
				})

			case "past_assets":
				if t.as == nil {
					return jsonResult(map[string]any{
						"mode":   mode,
						"assets": []any{},
						"note":   "AssetStore 未挂载",
					})
				}
				rows, err := t.as.Search(q, limit)
				if err != nil {
					return actool.Errorf("检索历史资产失败: " + err.Error()), nil
				}
				type assetBrief struct {
					ID           int64    `json:"id"`
					Type         string   `json:"type"`
					Domain       string   `json:"domain,omitempty"`
					URL          string   `json:"url,omitempty"`
					IP           string   `json:"ip,omitempty"`
					Port         *int     `json:"port,omitempty"`
					Method       string   `json:"method,omitempty"`
					StatusCode   *int     `json:"status_code,omitempty"`
					PageTitle    string   `json:"page_title,omitempty"`
					Technologies []string `json:"technologies,omitempty"`
					Summary      string   `json:"task_source_summary,omitempty"`
				}
				out := make([]assetBrief, 0, len(rows))
				for _, r := range rows {
					out = append(out, assetBrief{
						ID:           r.ID,
						Type:         r.Type,
						Domain:       r.Domain,
						URL:          r.URL,
						IP:           r.IP,
						Port:         r.Port,
						Method:       r.Method,
						StatusCode:   r.StatusCode,
						PageTitle:    r.PageTitle,
						Technologies: r.Technologies,
						Summary:      r.TaskSourceSummary,
					})
				}
				return jsonResult(map[string]any{
					"mode":     mode,
					"query":    q,
					"returned": len(out),
					"assets":   out,
				})

			case "case_skills":
				return searchDistilledCaseSkills(q, limit)

			case "skill_search":
				return searchBundledAndDiskSkills(q, limit)

			case "vuln_patterns":
				cat := strings.ToLower(strings.TrimSpace(a.Category))
				ql := strings.ToLower(q)
				matched := []vulnPatternCard{}
				for _, c := range builtinVulnPatternCards {
					if cat != "" && c.Category != cat {
						continue
					}
					if ql != "" {
						blob := strings.ToLower(c.Category + " " + c.Title + " " + strings.Join(c.HighYield, " ") + " " + strings.Join(c.FastProbes, " "))
						if !strings.Contains(blob, ql) {
							continue
						}
					}
					matched = append(matched, c)
				}
				totalMatched := len(matched)
				if len(matched) > limit {
					matched = matched[:limit]
				}
				return jsonResult(map[string]any{
					"mode":        mode,
					"category":    cat,
					"query":       q,
					"count":       len(matched),
					"match_count": totalMatched,
					"truncated":   totalMatched > len(matched),
					"source":      "bundled_static_cards",
					"claims":      "heuristics_not_evidence",
					"patterns":    matched,
				})

			default:
				return actool.Errorf(fmt.Sprintf("未知 mode %q，可选值: past_findings | past_assets | case_skills | skill_search | vuln_patterns", a.Mode)), nil
			}
		},
	)
}

func readSkillReferencesText(skillDir string) string {
	if strings.TrimSpace(skillDir) == "" {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join(skillDir, "references"))
	if err != nil || len(entries) == 0 {
		return ""
	}
	var parts []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".md" && ext != ".txt" && ext != ".json" {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(skillDir, "references", e.Name())); err == nil && len(b) > 0 {
			parts = append(parts, "## references/"+e.Name()+"\n"+strings.TrimSpace(string(b)))
		}
	}
	return strings.Join(parts, "\n\n")
}

func searchDistilledCaseSkills(query string, limit int) (actool.Result, error) {
	type caseSkillCard struct {
		Skill             string `json:"skill"`
		Domain            string `json:"domain"`
		Description       string `json:"description"`
		VerifiedPaths     string `json:"verified_paths,omitempty"`
		FindingsRetestsRE string `json:"findings_retests_re,omitempty"`
		PitfallsAndStops  string `json:"pitfalls_and_stop_signals,omitempty"`
		MatchedSection    string `json:"matched_section,omitempty"`
	}
	var results []caseSkillCard
	ql := strings.ToLower(strings.TrimSpace(query))
	skillDir := config.SkillDir()
	if reg, err := skill.LoadDir(skillDir); err == nil && reg != nil {
		for _, s := range reg.List() {
			name := s.Name
			if s.Dir != "" {
				name = filepath.Base(s.Dir)
			}
			refText := readSkillReferencesText(s.Dir)
			isCase := strings.HasPrefix(name, "case-") || strings.HasPrefix(name, "merged-") || strings.HasPrefix(name, "report-") || refText != ""
			if !isCase {
				continue
			}
			body := s.Instructions
			if refText != "" {
				body = body + "\n\n" + refText
			}
			combined := strings.ToLower(name + "\n" + s.Description + "\n" + body)
			if ql != "" && !strings.Contains(combined, ql) {
				continue
			}
			domain := "web_security"
			if strings.HasPrefix(name, "case-re-") || strings.Contains(s.Description, "software_re") {
				domain = "software_re"
			}
			verified := extractMarkdownSection(body, "已验证的有效路径", 450)
			if verified == "" {
				verified = extractMarkdownSection(body, "核心战术推导链", 450)
			}
			var evidenceParts []string
			for _, secTitle := range []string{"已确认漏洞与可利用点", "漏洞复测与修复验证", "逆向样本与静态分析证据", "关键事实与证据", "references/attack-chains-and-pocs.md"} {
				if sec := extractMarkdownSection(body, secTitle, 320); sec != "" {
					evidenceParts = append(evidenceParts, sec)
				}
			}
			pitfalls := extractMarkdownSection(body, "失败、误区与止损", 400)
			if pitfalls == "" {
				pitfalls = extractMarkdownSection(body, "踩坑记录与 3-Strike 止损红线", 400)
			}
			if pitfalls == "" {
				pitfalls = extractMarkdownSection(body, "references/pitfalls-and-bypasses.md", 400)
			}
			var matchedSec string
			if ql != "" {
				for _, sec := range strings.Split(body, "\n## ") {
					if strings.Contains(strings.ToLower(sec), ql) {
						matchedSec = clipUTF8(strings.TrimSpace("## "+sec), 500)
						break
					}
				}
			}
			results = append(results, caseSkillCard{
				Skill:             name,
				Domain:            domain,
				Description:       s.Description,
				VerifiedPaths:     verified,
				FindingsRetestsRE: clipUTF8(strings.Join(evidenceParts, "\n"), 600),
				PitfallsAndStops:  pitfalls,
				MatchedSection:    matchedSec,
			})
			if len(results) >= limit {
				break
			}
		}
	}
	return jsonResult(map[string]any{
		"mode":       "case_skills",
		"query":      query,
		"skill_dir":  os.Getenv("ARTEX_SKILL_DIR"),
		"count":      len(results),
		"case_cards": results,
	})
}

func extractMarkdownSection(body, title string, maxRunes int) string {
	marker := "## " + title
	idx := strings.Index(body, marker)
	if idx < 0 {
		return ""
	}
	rest := body[idx:]
	if next := strings.Index(rest[len(marker):], "\n## "); next >= 0 {
		rest = rest[:len(marker)+next]
	}
	rest = strings.TrimSpace(rest)
	if maxRunes > 0 {
		rest = clipUTF8(rest, maxRunes)
	}
	return rest
}

func searchBundledAndDiskSkills(query string, limit int) (actool.Result, error) {
	type skillSnippet struct {
		Skill       string `json:"skill"`
		Description string `json:"description"`
		Section     string `json:"matched_section"`
	}
	var results []skillSnippet
	seen := map[string]bool{}
	ql := strings.ToLower(strings.TrimSpace(query))

	collectFromContent := func(skillName, desc, body string) {
		if seen[skillName] {
			return
		}
		if ql == "" {
			seen[skillName] = true
			sec := body
			if len(sec) > 500 {
				sec = clipUTF8(sec, 500) + "...(可调用 Skill 工具加载完整技能包及 references 手册)"
			}
			results = append(results, skillSnippet{
				Skill:       skillName,
				Description: desc,
				Section:     sec,
			})
			return
		}
		combined := strings.ToLower(skillName + "\n" + desc + "\n" + body)
		if !strings.Contains(combined, ql) {
			return
		}
		seen[skillName] = true
		sections := strings.Split(body, "\n## ")
		var bestSec string
		for i, sec := range sections {
			if i > 0 {
				sec = "## " + sec
			}
			if strings.Contains(strings.ToLower(sec), ql) {
				bestSec = strings.TrimSpace(sec)
				break
			}
		}
		if bestSec == "" {
			bestSec = strings.TrimSpace(body)
		}
		if len(bestSec) > 900 {
			bestSec = clipUTF8(bestSec, 900) + "...(truncated)"
		}
		results = append(results, skillSnippet{
			Skill:       skillName,
			Description: desc,
			Section:     bestSec,
		})
	}

	// 1. Read from runtime SkillDir on disk (includes user edits, custom skills, and references/*.md)
	skillDir := config.SkillDir()
	if reg, err := skill.LoadDir(skillDir); err == nil && reg != nil {
		for _, s := range reg.List() {
			name := s.Name
			if s.Dir != "" {
				name = filepath.Base(s.Dir)
			}
			fullBody := s.Instructions
			if refText := readSkillReferencesText(s.Dir); refText != "" {
				fullBody += "\n\n" + refText
			}
			collectFromContent(name, s.Description, fullBody)
			if len(results) >= limit {
				break
			}
		}
	}

	// 2. Fallback to embedded skills.Bundled if disk directory was not yet initialized
	if len(results) < limit {
		_ = fs.WalkDir(skills.Bundled, ".", func(rel string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.EqualFold(filepath.Base(rel), "SKILL.md") {
				return nil
			}
			data, rerr := skills.Bundled.ReadFile(rel)
			if rerr != nil {
				return nil
			}
			skillSubDir := filepath.Dir(rel)
			dirName := filepath.Base(skillSubDir)
			raw := string(data)
			desc, body := splitSkillFrontmatter(raw)
			if refEntries, rerr := fs.ReadDir(skills.Bundled, filepath.ToSlash(filepath.Join(skillSubDir, "references"))); rerr == nil {
				for _, re := range refEntries {
					if re.IsDir() {
						continue
					}
					if rb, rerr2 := skills.Bundled.ReadFile(filepath.ToSlash(filepath.Join(skillSubDir, "references", re.Name()))); rerr2 == nil && len(rb) > 0 {
						body += "\n\n## references/" + re.Name() + "\n" + strings.TrimSpace(string(rb))
					}
				}
			}
			collectFromContent(dirName, desc, body)
			return nil
		})
	}

	return jsonResult(map[string]any{
		"mode":      "skill_search",
		"query":     query,
		"skill_dir": os.Getenv("ARTEX_SKILL_DIR"),
		"count":     len(results),
		"skills":    results,
	})
}

func splitSkillFrontmatter(raw string) (desc string, body string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(raw, "---\n") {
		return "", raw
	}
	rest := raw[4:]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return "", raw
	}
	fm := rest[:idx]
	body = strings.TrimSpace(rest[idx+5:])
	for _, line := range strings.Split(fm, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "description" {
			desc = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return desc, body
}

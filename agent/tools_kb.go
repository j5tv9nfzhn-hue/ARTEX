package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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

// logicInvariant 是一条可验证的业务不变量。它不是攻击步骤，而是"该系统
// 在正常运行时必须成立的断言"——Agent 把它转成待验证假设写入探索图，
// 违反时给出可观测证据，而不是凭直觉尝试越权。
type logicInvariant struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Assertion   string   `json:"assertion"`
	ViolationBy []string `json:"observable_violation_signals,omitempty"`
	VerifyWith  []string `json:"minimal_verification_sequence,omitempty"`
	NativeTools []string `json:"recommended_native_tools,omitempty"`
	SafetyNote  string   `json:"safety_note,omitempty"`
}

// logicProfile 是一类业务系统的"必须成立"集合。
type logicProfile struct {
	Target  string           `json:"target"`
	Aliases []string         `json:"aliases"`
	Finger  []string         `json:"fingerprints"`
	Rules   []logicInvariant `json:"invariants"`
}

// builtinLogicProfiles 按目标业务类型组织不变量。当前只有 AI 网关一类；
// 新增类型时追加 profile 即可，检索逻辑不变。
var builtinLogicProfiles = []logicProfile{
	{
		Target:  "ai_gateway",
		Aliases: []string{"ai_gateway", "ai-gateway", "llm_gateway", "中转站", "聚合网关", "newapi", "oneapi", "litellm", "sub2api", "one-hub"},
		Finger: []string{
			"/api/status 返回 version/start_time/system_name",
			"/api/setup 存在且含 root_init 字段（该字段具误导性，不代表已初始化）",
			"/v1/models 与 /pricing 或 /api/pricing 同时存在",
			"前端 React SPA + /static/js/index.<hash>.js",
			"/ui/ 或 /sso/key/generate 或 /key/list（LiteLLM）",
			"响应含 quota / remain_quota / used_quota / token_name",
		},
		Rules: []logicInvariant{
			{
				ID:        "INV-01",
				Title:     "结算与鉴权解耦",
				Assertion: "请求准入时快照用户/配额上下文；结算阶段不得依赖『API Key 仍然存在』。",
				ViolationBy: []string{
					"日志出现 API_KEY_NOT_FOUND / record_usage_failed / quota_not_found，且该请求已正常返回内容",
					"流式请求进行中删除 Key 后，请求完整返回但账单无记录",
				},
				VerifyWith: []string{
					"http_sequence(overlap=true)：发起流式长响应请求",
					"delay_ms 取实测响应耗时的 50%~80%，在该窗口内发送删除该 Key 的请求",
					"等待首请求完成，比对响应完整性 + 账单新增",
				},
				NativeTools: []string{"http_sequence(overlap=true)", "http_request"},
				SafetyNote:  "仅在自有账号执行；删除 Key 会中断后续请求，先确认可重建。",
			},
			{
				ID:        "INV-02",
				Title:     "定价缺失必须 fail-closed",
				Assertion: "计费路径遇到『该模型无定价配置』时必须拒绝请求或挂账告警，不得零成本放行。",
				ViolationBy: []string{
					"日志出现 pricing_missing_record_zero_cost / price_not_found 且请求成功、账单为 0",
				},
				VerifyWith: []string{
					"请求 /v1/models 列出但 /pricing 未覆盖的模型",
					"比对请求前后账单条目",
				},
				NativeTools: []string{"http_request"},
				SafetyNote:  "需区分『运营未配置』与『配置了但被绕过』；前者是配置问题，不得写成漏洞。",
			},
			{
				ID:        "INV-03",
				Title:     "计费数值必须溢出安全",
				Assertion: "所有参与计费的用户可控数值（n / max_tokens / audio duration / 图片数量）必须有上界，且参与乘法前做溢出检查。",
				ViolationBy: []string{
					"超大 max_tokens 或 n 导致余额增加（负扣费翻转）",
					"账单出现负数金额",
					"余额超过累计充值总额",
				},
				VerifyWith: []string{
					"在自有账号上用极小余额，提交边界值（n=2^31-1、max_tokens 极大值）",
					"观察余额与账单变化",
				},
				NativeTools: []string{"http_fuzz", "http_request"},
				SafetyNote:  "禁止在真实付费账户执行；返回 400/422 即视为已防护。",
			},
			{
				ID:        "INV-04",
				Title:     "余额唯一事实源是数据库",
				Assertion: "余额以 DB 为唯一真值；缓存只能读穿透或写失效，不得整体回写快照。",
				ViolationBy: []string{
					"并发扣费后余额回弹",
					"调用 PUT /api/user/self 类接口（只改昵称等无关字段）后余额恢复旧值",
				},
				VerifyWith: []string{
					"记录当前余额",
					"http_sequence 并发发起 N 个消耗请求",
					"全部完成后调用一次『更新用户资料』接口",
					"再次读取余额，比对『初始 - 实际消耗』",
				},
				NativeTools: []string{"http_sequence", "http_request"},
				SafetyNote:  "并发数控制在个位数，避免对目标造成压力。",
			},
			{
				ID:        "INV-05",
				Title:     "负余额/负费用是非法状态",
				Assertion: "余额不得小于 0（或不得低于配置的透支下限）；任何账单金额不得为负。",
				ViolationBy: []string{
					"余额为负",
					"账单条目金额为负",
				},
				VerifyWith: []string{
					"所有涉及数值的用例执行后统一检查该后置条件",
				},
			},
			{
				ID:        "INV-06",
				Title:     "Key 状态变更即时生效",
				Assertion: "删除/禁用 Key 后鉴权缓存必须在有界时间（建议 ≤1s）内失效；在途请求语义需明确定义。",
				ViolationBy: []string{
					"删除 Key 后 5s 仍可用该 Key 成功请求",
					"请求成功但未计费（与 INV-01 交叉）",
				},
				VerifyWith: []string{
					"删除 Key → 立即用旧 Key 请求 → 记录结果",
					"等待 5s 再请求一次",
				},
				NativeTools: []string{"http_request"},
				SafetyNote:  "仅在自有账号执行。",
			},
			{
				ID:        "INV-07",
				Title:     "租户归属必须强制 scoped",
				Assertion: "所有带 user_id / team_id / org_id / key_id 的查询必须强制限定为鉴权主体的可访问范围，不得信任客户端传入的 ID。",
				ViolationBy: []string{
					"低权 Key 可读取他人消费记录",
					"低权 Key 可列出他人 Key",
					"return_full_object=true 返回完整 Key 对象",
				},
				VerifyWith: []string{
					"用 A 租户低权 Key 请求带 B 租户 ID 的查询接口",
				},
				NativeTools: []string{"http_request", "http_sequence(mode=\"bola\")"},
				SafetyNote:  "只在自有双租户测试环境执行；返回 403/404/空即视为已防护。",
			},
			{
				ID:        "INV-08",
				Title:     "支付回调必须完整验签",
				Assertion: "回调验签必须满足：签名密钥非空、算法正确、订单状态前置校验（pending→paid）、金额与订单一致、幂等。",
				ViolationBy: []string{
					"签名密钥为空时验签通过",
					"重复回调导致重复充值",
					"回调金额可篡改",
				},
				VerifyWith: []string{
					"检查配置中签名密钥是否为空",
					"用错误签名提交回调 → 应拒绝",
					"用正确签名重复提交同一事件 → 应幂等",
				},
				NativeTools: []string{"http_request"},
				SafetyNote:  "禁止在真实支付渠道测试；只做静态配置审计 + 自有测试商户号。",
			},
			{
				ID:        "INV-09",
				Title:     "本地信任不得来自客户端 Header",
				Assertion: "判断请求是否来自本地必须基于 socket peer 地址，不得信任 Host / X-Forwarded-For / X-Real-IP。",
				ViolationBy: []string{
					"带 Host: localhost 的未鉴权请求被放行",
				},
				VerifyWith: []string{
					"向目标发送带 Host: localhost 的未鉴权请求",
				},
				NativeTools: []string{"http_request"},
				SafetyNote:  "只读探测；若被放行立即停止，不进一步索取数据。",
			},
			{
				ID:        "INV-10",
				Title:     "管理面必须做角色层级校验",
				Assertion: "管理 API 必须比较操作者与被操作者的角色层级；敏感字段（access_token / secret / password_hash）永不回显。",
				ViolationBy: []string{
					"普通管理员可操作同级或上级账号",
					"响应含明文 access_token / secret",
				},
				VerifyWith: []string{
					"用普通管理员访问用户列表/重置密码/提权接口",
					"检查响应是否含敏感字段",
				},
				NativeTools: []string{"http_request"},
				SafetyNote:  "只读验证；不实际执行提权写操作。",
			},
			{
				ID:        "INV-11",
				Title:     "促销与注册滥用防护",
				Assertion: "免费额度、邀请返利、优惠券兑换需有设备/IP/支付前置、单账号上限、关联图检测。",
				ViolationBy: []string{
					"同一设备/IP 可重复领取",
					"奖励无上限",
				},
				VerifyWith: []string{
					"仅在自有环境构造 2~3 个测试账号验证上限是否生效",
				},
				NativeTools: []string{"http_request"},
				SafetyNote:  "禁止批量注册真实服务；2~3 个测试账号即可判定。",
			},
		},
	},
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
			"- vuln_patterns：按漏洞分类（bola_idor / auth_mfa / jwt_oauth / graphql_api / ssrf_cloud / race_toctou / js_reverse / sqli_injection_triage）获取本地静态经验卡片（不构成在线全球知识库或结论证明）、止损建议与推荐原生工具；\n"+
			"- logic_invariants：按业务系统类型（当前支持 ai_gateway：NewAPI/OneAPI/LiteLLM/sub2api 等 LLM 聚合中转站）返回『该系统必须成立的业务不变量』清单，每条含断言、违反时的可观测信号、最小验证序列与安全约束。用于登录后计费/余额/租户/支付等无表面漏洞的业务逻辑审计。",
		obj(map[string]any{
			"mode":     str("必填：检索模式，可选 past_findings | past_assets | case_skills | skill_search | vuln_patterns | logic_invariants"),
			"query":    str("可选：检索关键词（域名、端点路径、技术栈如 fastapi/supabase/graphql、漏洞词如 mfa/jwt/bola/ssrf、逆向样本或函数名；logic_invariants 模式下为目标类型如 ai_gateway 或别名 newapi/litellm）"),
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
				rows, total, err := searchPastFindingsMulti(d, q, limit)
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
				// 多词 query（"example.com admin 登录"）在资产检索里是按整串字面量
				// 匹配的——整串几乎必然 0 命中。0 命中时按词回退，取第一个有结果的词。
				if len(rows) == 0 && strings.TrimSpace(q) != "" {
					for _, term := range kbQueryTerms(q) {
						if term == "" || strings.EqualFold(term, q) {
							continue
						}
						more, e := t.as.Search(term, limit)
						if e != nil {
							continue
						}
						if len(more) > 0 {
							rows = more
							break
						}
					}
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
				terms := kbQueryTerms(ql)
				// 打分而非整串字面量匹配：原来只拿 category/title/高产出向量/快探
				// 拼一个 blob 做 strings.Contains(ql)，多词 query（"jwt 越权"）和
				// 只在止损信号里出现的词（stop signals）永远检索不到。
				type scoredCard struct {
					card  vulnPatternCard
					score int
				}
				scored := make([]scoredCard, 0, len(builtinVulnPatternCards))
				for _, c := range builtinVulnPatternCards {
					if cat != "" && c.Category != cat {
						continue
					}
					score := 0
					if ql != "" {
						blob := strings.ToLower(c.Category + " " + c.Title + " " +
							strings.Join(c.HighYield, " ") + " " + strings.Join(c.FastProbes, " ") + " " +
							strings.Join(c.StopSignals, " ") + " " + strings.Join(c.NativeTools, " "))
						score = kbMatchScore(blob, ql, terms)
						if score == 0 {
							continue
						}
					}
					scored = append(scored, scoredCard{card: c, score: score})
				}
				sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
				matched := make([]vulnPatternCard, 0, len(scored))
				for _, sc := range scored {
					matched = append(matched, sc.card)
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

			case "logic_invariants":
				return searchLogicInvariants(q, limit)

			default:
				return actool.Errorf(fmt.Sprintf("未知 mode %q，可选值: past_findings | past_assets | case_skills | skill_search | vuln_patterns | logic_invariants", a.Mode)), nil
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

// searchLogicInvariants 按业务系统类型返回"必须成立"的业务不变量清单。
// 与 vuln_patterns 的区别：vuln_patterns 是"怎么打"，这里是"什么必须为真"——
// Agent 应把每条不变量转成待验证假设，用 VerifyWith 的最小序列取证，
// 而不是凭直觉批量试越权。
//
// query 为空时返回全部 profile 的概览（只列 target 与不变量标题），
// 避免一次把几十条断言灌进上下文。
func searchLogicInvariants(query string, limit int) (actool.Result, error) {
	ql := strings.ToLower(strings.TrimSpace(query))

	if ql == "" {
		overview := make([]map[string]any, 0, len(builtinLogicProfiles))
		for _, p := range builtinLogicProfiles {
			titles := make([]string, 0, len(p.Rules))
			for _, r := range p.Rules {
				titles = append(titles, r.ID+" "+r.Title)
			}
			overview = append(overview, map[string]any{
				"target":          p.Target,
				"aliases":         p.Aliases,
				"invariant_count": len(p.Rules),
				"invariants":      titles,
			})
		}
		return jsonResult(map[string]any{
			"mode":     "logic_invariants",
			"query":    "",
			"count":    len(overview),
			"profiles": overview,
			"note":     "指定 query 为目标类型（如 ai_gateway）以获取完整不变量（断言/可观测信号/最小验证序列）",
			"claims":   "must_hold_assertions_not_attack_steps",
		})
	}

	matchedProfiles := make([]logicProfile, 0, 1)
	for _, p := range builtinLogicProfiles {
		hit := strings.Contains(strings.ToLower(p.Target), ql)
		if !hit {
			for _, a := range p.Aliases {
				if strings.Contains(strings.ToLower(a), ql) || strings.Contains(ql, strings.ToLower(a)) {
					hit = true
					break
				}
			}
		}
		if !hit {
			// 指纹文本也参与匹配：Agent 常直接拿观测到的路径当 query。
			for _, f := range p.Finger {
				if strings.Contains(strings.ToLower(f), ql) {
					hit = true
					break
				}
			}
		}
		if hit {
			matchedProfiles = append(matchedProfiles, p)
		}
	}

	if len(matchedProfiles) == 0 {
		known := make([]string, 0, len(builtinLogicProfiles))
		for _, p := range builtinLogicProfiles {
			known = append(known, p.Target)
		}
		return jsonResult(map[string]any{
			"mode":  "logic_invariants",
			"query": query,
			"count": 0,
			"known": known,
			"note":  "未匹配到已知业务类型；可先不带 query 查看概览，或用指纹路径（如 /v1/models）再试",
		})
	}

	type profileOut struct {
		Target string           `json:"target"`
		Finger []string         `json:"fingerprints"`
		Rules  []logicInvariant `json:"invariants"`
	}
	out := make([]profileOut, 0, len(matchedProfiles))
	total := 0
	for _, p := range matchedProfiles {
		rules := p.Rules
		total += len(rules)
		if len(rules) > limit {
			rules = rules[:limit]
		}
		out = append(out, profileOut{Target: p.Target, Finger: p.Finger, Rules: rules})
	}

	return jsonResult(map[string]any{
		"mode":        "logic_invariants",
		"query":       query,
		"count":       len(out),
		"match_count": total,
		"truncated":   total > limit,
		"source":      "bundled_static_assertions",
		"claims":      "must_hold_assertions_not_attack_steps",
		"discipline":  "违反需给出最小复现与账单/状态前后快照；仅观测到一次异常不足以下结论；未配置与配置被绕过必须区分",
		"profiles":    out,
	})
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
	terms := kbQueryTerms(ql)
	type scoredCase struct {
		card  caseSkillCard
		score int
	}
	var scoredCases []scoredCase
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
			score := 0
			if ql != "" {
				score = kbMatchScore(combined, ql, terms)
				if score == 0 {
					continue
				}
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
				// 选「命中词最多」的那一段，而不是第一个字面量命中整串的段。
				bestScore := 0
				for _, sec := range strings.Split(body, "\n## ") {
					if sc := kbMatchScore(strings.ToLower(sec), ql, terms); sc > bestScore {
						bestScore = sc
						matchedSec = clipUTF8(strings.TrimSpace("## "+sec), 500)
					}
				}
			}
			scoredCases = append(scoredCases, scoredCase{
				score: score,
				card: caseSkillCard{
					Skill:             name,
					Domain:            domain,
					Description:       s.Description,
					VerifiedPaths:     verified,
					FindingsRetestsRE: clipUTF8(strings.Join(evidenceParts, "\n"), 600),
					PitfallsAndStops:  pitfalls,
					MatchedSection:    matchedSec,
				},
			})
		}
	}
	// 先收全量再排序截断：原来按目录遍历顺序 break，等于「字典序前 N 个」，
	// 与相关度和新旧都无关。
	sort.SliceStable(scoredCases, func(i, j int) bool { return scoredCases[i].score > scoredCases[j].score })
	for _, sc := range scoredCases {
		if len(results) >= limit {
			break
		}
		results = append(results, sc.card)
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
	terms := kbQueryTerms(ql)
	type scoredSnippet struct {
		snip  skillSnippet
		score int
	}
	var scored []scoredSnippet

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
			scored = append(scored, scoredSnippet{
				snip: skillSnippet{
					Skill:       skillName,
					Description: desc,
					Section:     sec,
				},
			})
			return
		}
		combined := strings.ToLower(skillName + "\n" + desc + "\n" + body)
		score := kbMatchScore(combined, ql, terms)
		if score == 0 {
			return
		}
		seen[skillName] = true
		// 选命中词最多的章节，而不是第一个字面量命中整串的章节。
		sections := strings.Split(body, "\n## ")
		var bestSec string
		bestScore := 0
		for i, sec := range sections {
			if i > 0 {
				sec = "## " + sec
			}
			if sc := kbMatchScore(strings.ToLower(sec), ql, terms); sc > bestScore {
				bestScore = sc
				bestSec = strings.TrimSpace(sec)
			}
		}
		if bestSec == "" {
			bestSec = strings.TrimSpace(body)
		}
		if len(bestSec) > 900 {
			bestSec = clipUTF8(bestSec, 900) + "...(truncated)"
		}
		scored = append(scored, scoredSnippet{
			score: score,
			snip: skillSnippet{
				Skill:       skillName,
				Description: desc,
				Section:     bestSec,
			},
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
		}
	}

	// 2. Fallback to embedded skills.Bundled if disk directory was not yet initialized
	// 注意：结果改为末尾统一排序后，这里必须看 scored 而不是 results。
	if len(scored) < limit {
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

	// 全量收集后再按相关度排序截断（原实现是「目录序前 N 个」）。
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	for _, sc := range scored {
		if len(results) >= limit {
			break
		}
		results = append(results, sc.snip)
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

// ── 检索词切分与打分（知识库/记忆库三链路共用） ─────────────────────────
//
// 原实现的检索一律是「整串 strings.Contains」：
//
//	tactical_kb(mode="case_skills", query="newapi 余额 越权")
//
// 会把 "newapi 余额 越权" 当成一个连续子串去匹配，几乎必然 0 命中；命中率
// 因此只取决于「用户/模型是否恰好只传了一个词」。结果是知识库看起来只有
// 那么几张卡片可用，实际是检索把它们过滤掉了。
//
// 现在把 query 切成若干个原子词（空格/逗号/顿号/分号/竖线），整串仍保留为
// phrase 以奖励精确短语命中；命中多个词的结果得分更高，排序也在打分之后。
func kbQueryTerms(query string) []string {
	std := strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r', ',', '\uFF0C', '\u3001', ';', '\uFF1B', '|', '\uFF5C', '\u3000':
			return ' '
		}
		return r
	}, strings.ToLower(strings.TrimSpace(query)))
	if std == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.Fields(std) {
		if len([]rune(f)) < 2 || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
		if len(out) == 6 { // 上限：避免一句话被切成十几个词后到处乱命中
			break
		}
	}
	return out
}

// kbMatchScore 返回 haystack（已小写）对 query 的相关度：整串短语命中给高分，
// 每个原子词按其长度加权。0 表示未命中。
func kbMatchScore(haystack, phrase string, terms []string) int {
	score := 0
	if phrase != "" && strings.Contains(haystack, phrase) {
		score += 6
	}
	for _, t := range terms {
		if t == "" || !strings.Contains(haystack, t) {
			continue
		}
		n := len([]rune(t))
		switch {
		case n >= 4:
			score += 3
		case n >= 2:
			score += 2
		default:
			score++
		}
	}
	return score
}

// searchPastFindingsMulti 让 past_findings 也支持多词检索。db 层的 Query 是
// 单个 LIKE %q%，多词 query 会退化成 0 命中；这里按词分别检索（最多 4 个词），
// 按「命中词数」合并去重后取前 limit 条。单词 query 走原路径，行为不变。
func searchPastFindingsMulti(d *db.DB, q string, limit int) ([]*db.DBFinding, int, error) {
	terms := kbQueryTerms(q)
	if len(terms) <= 1 {
		return d.ListFindingsPage(db.FindingFilter{Query: q, Sort: "severity"}, 1, limit)
	}
	type hitRow struct {
		row  *db.DBFinding
		hits int
	}
	seen := map[int64]*hitRow{}
	var order []int64
	total := 0
	for i, term := range terms {
		if i >= 4 {
			break
		}
		rows, n, err := d.ListFindingsPage(db.FindingFilter{Query: term, Sort: "severity"}, 1, limit)
		if err != nil {
			return nil, 0, err
		}
		total += n
		for _, r := range rows {
			if r == nil {
				continue
			}
			if h, ok := seen[r.ID]; ok {
				h.hits++
				continue
			}
			seen[r.ID] = &hitRow{row: r, hits: 1}
			order = append(order, r.ID)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		hi, hj := seen[order[i]], seen[order[j]]
		if hi.hits != hj.hits {
			return hi.hits > hj.hits
		}
		return hi.row.ID > hj.row.ID // 同分取较新
	})
	out := make([]*db.DBFinding, 0, len(order))
	for _, id := range order {
		if len(out) >= limit {
			break
		}
		out = append(out, seen[id].row)
	}
	return out, total, nil
}

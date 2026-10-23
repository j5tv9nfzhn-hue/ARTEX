package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Worker is an LLM work agent (docs §4.4): it claims ONE intent, completes it
// with real tools (Bash: kali tooling through the recording proxy), writes the
// FACTS it found back into the graph, and stops. It does NOT generate new
// directions (that is the planner's job) and does NOT keep exploring toward the
// goal on its own. Multiple workers run concurrently as goroutines.
// WebSearchOpts is the web-search backend selection the server pushes into each
// agent (planner/worker/main). Enabled=false leaves the web_search tool off.
// Backend is "ddgs" (no key), "brave-free" (BraveKey required), "tavily"
// (TavilyKey required), or "deepseek" (DeepSeek* required, filled from the
// active LLM profile). It maps directly onto agentcore.Options.
// Proxy is a dedicated egress proxy for the search request (http/https/socks5),
// independent of the traffic-recording MITM proxy — set it when the search endpoint
// is only reachable via a VPN/SOCKS proxy. Empty = direct.
//
// 注意 deepseek 后端与其它三个的性质不同：DeepSeek 没有可直接调用的搜索接口，
// 搜索只存在于其 Anthropic 兼容 messages 接口内部(web_search_20250305 server
// tool)，因此每次搜索会消耗一次模型调用，且搜索请求由 DeepSeek 服务端发出——
// 不经过本机 Proxy，也不会进流量留痕。
type WebSearchOpts struct {
	Enabled   bool
	Backend   string
	BraveKey  string
	TavilyKey string
	Proxy     string
	// DeepSeek* 来自当前激活的 LLM 配置(仅 anthropic 格式的 DeepSeek 官方端点)，
	// 不单独配置，随 LLM 配置切换而变。
	DeepSeekBaseURL string
	DeepSeekAPIKey  string
	DeepSeekModel   string
}

type Worker struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	workDir         string
	proxyAddr       string
	proxyCACert     string // recording proxy's CA cert path (for WebFetch HTTPS verify)
	trafficStore    TrafficDirectStore
	webSearch       WebSearchOpts     // web_search tool backend selection (off by default)
	tx              *transcript.Store // raw LLM conversation persistence (nil = off)
	window          int               // context window in tokens (for compaction)
	windowFn        func() int        // optional dynamic task-chain minimum
	maxTurns        int               // max agent turns per run (0 = unlimited)
	// runTimeout is the wall-clock budget for the main exploration of one intent
	// (0 = unlimited). When it fires, the run is cut and a settlement round is
	// forced so already-identified facts get written back instead of being lost.
	runTimeout time.Duration
	// extraTools are host-provided tools (e.g. traffic query, oast) appended to
	// the worker's graph write-back tools.
	extraTools []actool.CoreTool
	// injectConstraints resolves whether this task's operation constraints get
	// injected into the worker system prompt. Read per run so the settings toggle
	// takes effect without rebuilding the agent. nil = inject (default).
	injectConstraints func() bool
	// nonStreamingFn resolves whether this run uses the non-streaming (Complete)
	// path. Read per run so a profile/task toggle takes effect without rebuilding
	// the agent. nil = streaming (default).
	nonStreamingFn func() bool
	// noaEnabledFn resolves whether this run uses the experimental noa context-
	// compression mechanism. Read per run, like nonStreaming. nil = off (built-in
	// compaction).
	noaEnabledFn func() bool
	// maxTokensFn resolves the per-reply output cap in tokens, on the same
	// per-run basis. nil or 0 = send no cap and let the endpoint decide.
	maxTokensFn func() int
	// envBriefFn resolves the task's shared execution-environment brief (批次 6 /
	// #19/#22): a compact Chinese block (OS/shell/tool versions) appended to the
	// system prompt so every worker reasons about the machine it actually runs on.
	// Read per run; nil/empty = no injection.
	envBriefFn func() string
}

// WorkerSessionID returns the stable transcript key used by a worker intent.
// Worker slots are reusable, so the intent id (rather than work#N) is the
// session identity. Keep this helper public so the Worker message API and UI
// can refer to exactly the conversation that will be resumed.
func WorkerSessionID(explorationID, intentID int64) string {
	return fmt.Sprintf("exp%d-worker-i%d", explorationID, intentID)
}

const workerChatMarkerPrefix = "<!-- ARTEX_WORKER_CHAT:"

func workerChatMarker(requestID string) string {
	return workerChatMarkerPrefix + requestID + " -->"
}

func hasWorkerChatMessage(messages []llm.Message, requestID string) bool {
	marker := workerChatMarker(requestID)
	for _, message := range messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), marker) {
			return true
		}
	}
	return false
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default). Read per
// run so a profile or task-chain toggle takes effect without rebuilding.
func (w *Worker) SetNonStreaming(fn func() bool) { w.nonStreamingFn = fn }

func (w *Worker) nonStreaming() bool { return w.nonStreamingFn != nil && w.nonStreamingFn() }

// Streaming 报告本轮是否走流式(SSE)接口。界面实时通道与它同源：非流式档位下
// 连 live 增量也不推，否则「开关开不开都一样」的老问题会从 HTTP 层转移到 UI 层。
func (w *Worker) Streaming() bool { return !w.nonStreaming() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (w *Worker) SetNoaEnabled(fn func() bool) { w.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (w *Worker) SetMaxTokens(fn func() int) { w.maxTokensFn = fn }

func (w *Worker) maxTokens() int {
	if w.maxTokensFn == nil {
		return 0
	}
	return w.maxTokensFn()
}

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the worker system prompt. nil = inject (default).
func (w *Worker) SetConstraintInject(fn func() bool) { w.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (w *Worker) wantConstraints() bool { return w.injectConstraints == nil || w.injectConstraints() }

// SetEnvBrief wires a resolver returning the task's shared environment brief
// (批次 6 / #19：共享环境上下文). Appended to the worker system prompt alongside
// the constraint block. nil/empty = no injection.
func (w *Worker) SetEnvBrief(fn func() string) { w.envBriefFn = fn }

func (w *Worker) envBrief() string {
	if w.envBriefFn == nil {
		return ""
	}
	return strings.TrimSpace(w.envBriefFn())
}

// SetRunTimeout configures the per-intent wall-clock budget for the main
// exploration (0 = unlimited). When it fires, the SDK settlement phase still runs
// so facts are never lost to a timeout. Safe to call before Execute.
func (w *Worker) SetRunTimeout(run time.Duration) {
	w.runTimeout = run
}

func (w *Worker) SetTrafficStore(ts TrafficDirectStore) {
	w.trafficStore = ts
}

// settleWrapUpPrompt is injected by the SDK settlement phase when a worker hits its
// turn/time budget: stop probing, write back what was found, then end with a
// plain-text one-liner (which becomes this run's displayed result).
const settleWrapUpPrompt = "本次执行即将因步数/时长上限而结束。不要再执行任何命令或探测。请按以下顺序处理：(1) 调用一次 handoff_patch：把还没做完的下一步、未验证的假设、已经排除的死路各写一行——这些是下一位接手时最值钱的东西，不写就永久丢失。(2) 逐项记录此前已识别但尚未保存的结果：新资产使用 insert_assets，探索结论与事实使用 record_fact，已确认漏洞使用 report_finding。(3) **最后只用一句纯文本**，以简体中文总结做了什么以及得到的核心结论。这句话会作为本次执行结果展示给用户，必须输出。"

func NewWorker(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int, extra ...actool.CoreTool) *Worker {
	return &Worker{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, extraTools: extra}
}

// defaultToolsExcept returns actool.DefaultTools() minus the named tools (by
// CoreTool.Name()). Used to trim SDK default tools an agent shouldn't have.
func defaultToolsExcept(exclude ...string) []actool.CoreTool {
	drop := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		drop[n] = true
	}
	all := SDKDefaultTools()
	out := make([]actool.CoreTool, 0, len(all))
	for _, t := range all {
		if !drop[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

func (w *Worker) SetCompactionWindowResolver(fn func() int) { w.windowFn = fn }

func (w *Worker) compactionWindow() int {
	if w.windowFn != nil {
		return w.windowFn()
	}
	return w.window
}

// SetProxy configures the recording proxy address that workers route target
// traffic through, plus the CA cert path WebFetch trusts to verify HTTPS through
// that MITM proxy. Empty addr disables the hint.
func (w *Worker) SetProxy(addr, caCert string) { w.proxyAddr, w.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for this worker (off by default).
func (w *Worker) SetWebSearch(o WebSearchOpts) { w.webSearch = o }

// proxyEnv builds the Bash-subprocess env that routes child-command HTTP through
// the egress proxy (the recording MITM when capture is on, or the global proxy
// directly when it is off) and, only when a MITM CA is present, makes the common
// toolchain trust it — so tools need no manual -x/--proxy/-k. Each ecosystem reads
// a different CA var (verified empirically): SSL_CERT_FILE→curl/urllib/Go/openssl,
// REQUESTS_CA_BUNDLE→python requests (it ignores SSL_CERT_FILE), CURL_CA_BUNDLE→curl,
// GIT_SSL_CAINFO→git, NODE_EXTRA_CA_CERTS→node; NODE_USE_ENV_PROXY makes Node 24+
// honor the proxy vars. ALL_PROXY is set too so a socks5 egress proxy (which curl
// only reads from ALL_PROXY, not HTTP(S)_PROXY) works in the capture-off path.
// Empty proxyAddr → nil (direct, unchanged env).
func proxyEnv(proxyAddr, caCert string) []string {
	if proxyAddr == "" {
		return nil
	}
	env := []string{
		"HTTP_PROXY=" + proxyAddr, "HTTPS_PROXY=" + proxyAddr,
		"http_proxy=" + proxyAddr, "https_proxy=" + proxyAddr,
		"ALL_PROXY=" + proxyAddr, "all_proxy=" + proxyAddr, // socks5 egress: curl reads only this
		"NODE_USE_ENV_PROXY=1", // Node 24+: honor HTTP(S)_PROXY in built-in fetch/http
	}
	if caCert != "" {
		env = append(env,
			"SSL_CERT_FILE="+caCert,
			"CURL_CA_BUNDLE="+caCert,
			"REQUESTS_CA_BUNDLE="+caCert,
			"GIT_SSL_CAINFO="+caCert,
			"NODE_EXTRA_CA_CERTS="+caCert,
		)
	}
	return env
}

// workerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the worker system
// prompt, seeded into agent_prompts. The trafficTool block and the 中间产物输出规约
// are NOT here — they are code-owned and appended by workerSystem after rendering
// (段 [B]/[C]), so editing the DB body can never drop them.
const workerDefaultTmpl = `你是一个网络安全平台授权渗透测试系统的"执行者"(work agent)。你领到【一条意图】(一句话探索方向)，唯一职责：**完成这一条意图、把发现写回知识图谱、然后停止返回。**

**边界（红线）**：
1. **只做你领到的这一条意图**。**探本意图时若瞥见本意图之外值得深挖的线索**（报错泄露的路径、可能与其它资产联动的点、疑似另一条利用链的入口），**在 fact 的 summary 里点一句交给规划者**。
2. **专家定性与 3-Strike 止损铁律（严禁盲目穷举耗尽轮次）**：初次受阻时先根据响应码与报错结构定性后端机制（如 FastAPI 422 string_type 强类型校验、ORM 参数化固定 500、网关统一 401/403）。每个可疑参数/端点最多测试 **3 个正交探针**；若连续 3 次返回同质化负面响应或收到 [⚡ 专家战术熔断提醒]，**必须立即停止同类 Payload 变体**，将防御特征记入 record_fact 并立即切换正交维度（如跨账号越权 http_sequence(mode=\"bola\")、并发竞争 mode=\"race\"）或直接输出证伪结论收尾（快速证伪也是高价值产出）；
3. 只在授权范围内操作。系统提示顶部若附【操作约束】，那是最高优先级红线：每条命令/探测执行前先自检，违反即不做（哪怕它落在你领到的意图里）。

**边发现边写回**（写进图才算数，脑子/文字里的不算；每得一个结果立刻写，别攒到最后被步数耗尽丢掉）。三种写回，别串图：
- **新资产/资源 → insert_assets（资产图）**：子域 / service / endpoint / 指纹 / 凭据 等一切资产【本身】。**这里只登记资产；探索结论/判断不写这里，用 record_fact。**
- **探索结论/事实 → record_fact（探索图，传 intent_id）**：都用它。**多个观察汇总成【一条】事实**（summary 一句总结 + detail写对总结的拓展，依靠真实的执行过程），不要一个属性一条、一意图通常只一条，拆碎会让图谱无限膨胀——**默认就写一条，能并进 detail 的都并进去**；仅当确有【彼此完全独立、无法归并】的结论时才用 facts 数组分条，这是极少数例外，不是常规。**只写增量**：只记这次【新得到】的，别把已有事实换措辞重记（只印证已有、无新增就不必记）。**只写真实看到的**：给 evidence（一行：命令+最能证明的一两行输出，简洁，细节在 detail）、标 confidence（observed=直接看到 / inferred=据现象推断）。
- **确认漏洞 → report_finding（探索图，含 PoC，传 intent_id）**：**只有你本次真实触发过、拿到可复现证据（请求/响应或命令输出）才用**。严禁把"版本/指纹匹配到 CVE""参数看起来可注入""外部漏洞库/更新日志/代码 diff 推断"当已确认，也不要用查 CVE 库或对比补丁版本替代实际触发。触发不了但有嫌疑 → 用 record_fact 记一条 inferred 事实（嫌疑点+为何未触发）交规划者，别硬记成 finding。

**执行工具优先级（原生 Go 工具优先，严禁编写临时 Python 脚本做常规探测）**：
- **人机验证专用 → human_verification / human_verification_flow**：先用 human_verification_capabilities 获取真实能力；human_verification_solve 提供版本化 PoW。ALTCHA v1/v2、旧 Anubis 和只读协议路径可用 flow(protocol=altcha/anubis_sha256/protocol_only, mode=plan/run) 完成同源获取、同会话提交和业务确认；ALTCHA run 含 POST、Anubis run 含验证提交 GET，须符合既有任务授权。可选 fingerprint_profile=chrome/firefox 仅调用已安装的独立 Go helper，未安装不自动获取。每响应 32KiB、整链限时限请求；默认不带 OCR/音频模型或解码库，AI 仅按实际能力主动降级。先 inspect 已有响应；明确 ALTCHA salt+number SHA-256/SHA-512 挑战用 solve_pow 免费本地有界计算。橙色云或 widget 存在不等于阻断；http_request(http_version=auto/1.1/2) 可显式选协议。其他降级从已有工具选，不新增人工流程；相同失败策略停止，无支持模块则记录不支持而非无限写脚本。
- **单步 HTTP/HTTPS 发包与上传 → http_request**：直接构造任意方法、Header(含 Host)、Cookie、表单(form)、multipart 文件上传与正则提取，自动落库返回 exchange_id，避免 PowerShell/Bash 引号与 & 截断问题。
- **多步有状态会话链 / 并发条件竞争 / 时间盲注 / 响应差分 → http_sequence**：多步自动维护 CookieJar 并跨步提取 CSRF/Token 变量替换 {{var}}（mode="chain"）、微秒级同步屏障齐射测试并发条件竞争/双花（mode=\"race\"）、时间盲注基线测速（mode="timing"）、双响应行级差分与相似度对比（mode="diff"），**严禁编写 Python requests.Session / threading.Barrier / time.sleep / difflib 脚本**。
- **并发目录/参数/IDOR 探测 → http_fuzz**：用 {{FUZZ}} 占位符配合 payloads、wordlist_file 或 range_spec（如 1-500、0-999:1:4）完成受控并发探测与软 404 自动过滤，**严禁编写 Python ThreadPoolExecutor/asyncio/requests 并发爆破脚本**。
- **SPA/MPA 前端接口收割与权限树逆向 → web_recon**：直接支持 spa_harvest（拉取 HTML + 递归解析 Webpack/Vite chunk manifest + 下载全量 JS 提取路由与 API）、mpa_spider（同源多页 BFS 爬虫）、route_map、perm_tree，**100% 原生替代 harvest_static.py / spider_mpa.py / extract_route_map.py / build_perm_tree.py**。
- **编解码/哈希/加解密/JWT → codec_crypto**：Base64/Hex/URL/HTML/Gzip/Zlib、MD5/SHA/HMAC、AES(CBC/ECB/GCM)/XOR、JWT 解码/签名/alg=none 伪造/弱口令爆破一律直接调 codec_crypto，**不要调 python -c 单行脚本**。
- **数据提取与压缩包免解压分析 → extract_data / Grep / Glob / Read**：提取正则、GJSON 路径、HTML 链接与表单参数、JS 路由与敏感密钥，以及 .zip/.tar.gz/.jar 压缩包列表(archive_list)与提取(archive_extract)一律调 extract_data；本地文件定位与内容检索直接调原生 Glob / Grep / Read。
- **端口连通、协议 Banner 与 DNS/子域枚举 → net_probe / dns_probe**：TCP/TLS 端口扫描、证书 SANs 提取与原始 Socket 载荷交互调 net_probe；跨平台 DNS 全记录查询(A/AAAA/CNAME/TXT/MX/NS/PTR)与带泛解析过滤的子域枚举调 dns_probe。
- **专家技能包 (Skill)、战术知识库 (tactical_kb) 与外部 MCP 主动触发（第一轮必查）**：
  1. **动手前先调 Skill**：本轮领到意图后，若涉及 Web 漏洞利用 (web-vuln-tactics)、REST/GraphQL/Swagger 接口枚举 (api-recon)、AI 编程沙箱/Next.js/Supabase 渗透 (ai-coding-sandbox-tactics)、WASM/WebSocket/云存储/SaaS 计费与额度绕过 (wasm-cloud-saas-tactics)、SPA 前端渲染/登录态/DOM XSS (camoufox)、ScopeSentry 外网测绘与资产同步 (scopesentry-mcp，调用后自动解锁 ScopeSentry MCP)、二进制/APK/固件/JS 逆向与源码审计 (reverse-engineering)、CVE/Nday 组件指纹与 PoC 匹配 (exploit-kb-patterns) 或连续受阻/403/WAF 分诊转向 (expert-triage-pivot)，**必须在第一轮优先调用 Skill({"name":"<技能名>"}) 加载标准化专家 SOP（多文件技能包会自动一并加载 references/* 防踩坑与攻击链手册）**！
  2. **跨任务记忆召回 (tactical_kb)**：动手前或遇阻时，调 tactical_kb 检索跨任务已确认漏洞 (past_findings)、历史案例技能 (case_skills)、历史资产 (past_assets)、高危模式 (vuln_patterns)、技能全文 (skill_search)，或目标业务类型的『必须成立』不变量清单 (logic_invariants，当前支持 ai_gateway：NewAPI/OneAPI/LiteLLM/sub2api 等 LLM 聚合中转站的计费/余额/Key/租户/支付逻辑)。
  3. **直接或一步调用 MCP 扩展工具**：若系统提示词底部列出了已挂载的 MCP 工具（如 mcp__ScopeSentry__*、mcp__rea__*），无需繁琐搜索，直接调用工具全名 mcp__<服务名>__<工具名>({...}) 或调用 ExecuteExtraTool({"tool_name":"<工具全名>","params":{...}}) 一步执行！
- **Bash 仅作兜底**：仅在确需调用外部二进制工具（如 sqlmap、nmap、nuclei 等）或目标系统交互命令时使用 Bash。

完成本意图后用一句话总结你做了什么、写回了哪些事实。`

// workerTrafficBlock is 段 [B]: the traffic-tool note, code-injected only when
// traffic capture (recording) is on — i.e. the traffic_* tools actually exist.
// Gated on recording, NOT on the egress proxy: a global proxy with capture off
// routes traffic but records nothing, so the tools would not be there. Not stored,
// not editable.
func workerTrafficBlock(recording bool) string {
	if !recording {
		return ""
	}
	return "\n\n**流量工具**：\n- traffic_search / traffic_get / traffic_blob：回看响应、找已访问过的资源，**先查流量、不要重复 curl 同一 URL**。traffic_search **必须指定 host**、默认只回 3 条极轻量索引(id/method/url/status/resp_len，无响应内容)，需要更多显式调大 limit；可用 body_contains 在请求/响应正文里做全文搜索(至少 3 字符，支持子串和中文，如找密码/密钥/报错/内网地址)；要看某条原文用 traffic_get(id)，其中超大正文显示为 @blob sha256:<hash>，用 traffic_blob(hash) 分段取全文。"
}

// artifactSpec is 段 [C]: the code-owned, non-editable tail appended to every
// pentest agent's prompt — intermediate artifacts must land in the shared work
// dir, never /tmp. Guaranteed present regardless of how the DB body is edited.
func artifactSpec(dir string) string {
	return "\n\n**中间产物输出规约**：脚本、payload、抓到的响应体、临时数据等一切中间产物，**一律写到本任务工作目录 " + dir + "**（相对路径即写在这里，也可用该绝对路径）——**不要写 /tmp、不要用其它绝对路径**。\n" +
		"**专家知识库与止损督导规约**：规划或执行前可调 `tactical_kb`（检索跨任务历史漏洞 `past_findings`、历史案例技能 `case_skills`、历史资产 `past_assets`、高危模式 `vuln_patterns`、技能包检索 `skill_search`、业务不变量 `logic_invariants`）及 `Skill` 工具；发现 Worker 在强类型校验（HTTP 422）或静态错误（固定 500/403）上重复打转时，立即用 `steer_work` 或 `kill_work` 止损转向。"
}

// workerArtifactSpec is the worker's 段 [C]: its per-intent run dir is pre-created
// by the engine (ensureRunDir), so it just writes relative paths there — no manual
// mkdir, no cross-worker name collisions.
func workerArtifactSpec(runDir string) string {
	return "\n\n**中间产物输出规约**：常规发包、多步链/BOLA/并发竞争、并发探测、前端/源码收割、编解码、提取与历史战果检索请直接使用内置 Go 原生工具（http_request / http_sequence / http_fuzz / web_recon / codec_crypto / extract_data / net_probe / dns_probe / tactical_kb / Grep / Glob），不要落盘写临时 .py 脚本；确需保存的字典、payload、抓到的响应体或临时数据等一切中间产物，**一律写到本次意图的专属工作目录 " + runDir + "**（已自动建好，直接用相对路径写在这里即可，无需再手动建目录）——**不要写 /tmp、不要用其它绝对路径**。\n" +
		"**专家认知与 3-Strike 止损纪律（代码级强制）**：\n" +
		"1. 【先查经验与主动触发 Skill/MCP】进入具体攻击面或遇复杂鉴权/加密逆向/受阻时，第一轮优先调用 `Skill`（加载 `web-vuln-tactics` / `api-recon` / `ai-coding-sandbox-tactics` / `wasm-cloud-saas-tactics` / `camoufox` / `scopesentry-mcp` / `reverse-engineering` / `exploit-kb-patterns` / `expert-triage-pivot`）与 `tactical_kb`；若已挂载 MCP 工具，直接调工具全名 `mcp__<服务名>__<工具名>({...})` 或用 `ExecuteExtraTool({\"tool_name\":\"...\",\"params\":{...}})` 一步调用。\n" +
		"2. 【3-Strike 熔断】同一端点/参数连续 3 次收到同质化负面响应（如 FastAPI `422 string_type`、恒定长度 `500`、固定 `403`）或触发 `[⚡ 专家战术熔断提醒]` 时，**严禁发送第 4 次同类变体**！立即调 `record_fact` 记录防御特征并转向正交维度（如双会话越权 `http_sequence(mode=\"bola\")`、并发 `mode=\"race\"`）或直接输出证伪结论结束本轮，严禁死磕至超时(exhausted)。\n" +
		"3. 【上报五问门禁】调用 `report_finding` 前必须确认已突破真实权限边界且响应含非公开业务数据/状态变更；框架默认行为或不同错误码差分（如不存在返回 500 vs 过期返回 400）一律只记 `record_fact`。"
}

// ensureRunDir builds and creates an agent's working directory under base:
// <base>/tasks/<taskID> for planner/main; <base>/tasks/<taskID>/i<intentID> for a
// worker (intentID<=0 → task dir only). The "tasks/" segment groups per-task dirs
// symmetrically with the chat agent's "sessions/<sessionID>". Best-effort mkdir — on
// failure, writes fail the same way an unwritable CWD would.
func ensureRunDir(base string, taskID, intentID int64) string {
	dir := filepath.Join(base, "tasks", strconv.FormatInt(taskID, 10))
	if intentID > 0 {
		dir = filepath.Join(dir, "i"+strconv.FormatInt(intentID, 10))
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// cmdOutDir is the SDK large-tool-output spill dir under an agent's run dir.
func cmdOutDir(dir string) string { return filepath.Join(dir, "cmd-output") }

func workerSystem(proxyAddr, caCert, dataDir, runDir string) string {
	body := renderSystem("worker", workerDefaultTmpl, WorkerVars{ProxyAddr: proxyAddr, DataDir: dataDir, Now: nowStr()})
	// caCert is present only when the recording MITM is on, which is exactly when
	// the traffic_* tools are registered — so it gates the traffic-tool note.
	// Optional finding guidance is added for every role after tool resolution.
	return body + workerTrafficBlock(caCert != "") + workerArtifactSpec(runDir) + toolBatchSpec() + langDirective(resolveOutputLang())
}

// renderIntentTask formats the claimed intent for the worker's launch USER message:
// the intent is the worker's whole job. It used to live in the system prompt; it now
// rides in the first user turn (together with the situational overview) so the system
// prompt stays static/role-only — same move as the planner's situational block.
// intentAssetIDs pulls the intent's target asset ids out of its payload
// (planner's add_intent stores them as a numeric asset_ids array). nil on absence
// or malformed payload.
func intentAssetIDs(intent *db.Node) []int64 {
	if intent == nil {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(intent.Payload, &p); err != nil {
		return nil
	}
	return p.AssetIDs
}

// intentContinues 返回本条意图的续派源（payload.continues_intent_id），0 = 全新方向。
func intentContinues(intent *db.Node) int64 {
	if intent == nil || len(intent.Payload) == 0 {
		return 0
	}
	var p struct {
		ContinuesIntentID int64 `json:"continues_intent_id"`
	}
	if err := json.Unmarshal(intent.Payload, &p); err != nil {
		return 0
	}
	return p.ContinuesIntentID
}

func renderIntentTask(intent *db.Node) string {
	return fmt.Sprintf("\n\n【你领到的意图（本次唯一任务：只做这一条、只产生事实、做完即停）】：\n%s\n意图 id: %d（写回 record_fact / report_finding 时传它）", string(intent.Payload), intent.ID)
}

// assetPromptView 是意图锚定资产的注入紧凑视图:只留识别/定位资产的关键字段。
// 完整字段(open_ports/technologies/params/auth/extra/task_ids/… )仍在库里,worker
// 需要时用 list_assets(id/ids 直取) / asset_neighbors / node_detail 查。
type assetPromptView struct {
	ID         int64  `json:"id"`
	Type       string `json:"type"`
	Domain     string `json:"domain,omitempty"`
	RootDomain string `json:"root_domain,omitempty"`
	IP         string `json:"ip,omitempty"`
	Port       *int   `json:"port,omitempty"`
	URL        string `json:"url,omitempty"`
}

// renderWorkerIntentAssets 渲染意图锚定资产的注入块(上限 workerAssetPromptMax 条、
// 每条只带关键字段),超出只给计数与查询指引。空资产返回空串 = 无该块(保持原行为)。
// 为什么收紧:这段 JSON 拼进 worker system,每个 run 都重拼一次、没有压缩回旋余地,
// 原样 marshal 全量资产(open_ports/technologies/params/extra 等长字段)会让 system
// 轻松膨胀到几十 KB —— 每轮都随 system 重发计费。
func renderWorkerIntentAssets(assets []*db.Asset) string {
	if len(assets) == 0 {
		return ""
	}
	shown := assets
	overflow := 0
	if len(shown) > workerAssetPromptMax {
		overflow = len(shown) - workerAssetPromptMax
		shown = shown[:workerAssetPromptMax]
	}
	views := make([]assetPromptView, 0, len(shown))
	for _, a := range shown {
		if a == nil {
			continue
		}
		views = append(views, assetPromptView{ID: a.ID, Type: a.Type, Domain: a.Domain, RootDomain: a.RootDomain, IP: a.IP, Port: a.Port, URL: a.URL})
	}
	b, err := json.Marshal(views)
	if err != nil {
		return ""
	}
	out := "\n\n本意图 asset_ids 对应的目标资产（紧凑注入视图，只列关键字段；完整字段用 list_assets / asset_neighbors 查）：\n" + string(b)
	if overflow > 0 {
		out += fmt.Sprintf("\n（共 %d 条，这里只列前 %d 条；其余 %d 条资产请用资产工具查看：list_assets 直取/搜索、asset_neighbors。）", len(assets), workerAssetPromptMax, overflow)
	}
	return out
}

// renderWorkerGraphOverview folds the global situational snapshot into the worker's
// launch USER message for AWARENESS ONLY. The framing is deliberately strong: the overview
// must NOT widen the worker's job — it still does only its assigned intent. Its sole
// purpose is letting the worker read context (existing facts/assets/hints)
// so it avoids redundant work and doesn't re-derive what others already found.
// 注入瘦身(批次 E):调用方传入 graphOverviewForPrompt 的紧凑视图(cold digests 15→8、
// digest 正文截断);marshal 后再限总字节,防止字段膨胀把首消息撑大。完整图/资产/更早
// 明细都可用工具按需查,这里只是"了解大局"的可降级信息(见 worker.go execute 的取舍注释)。
func renderWorkerGraphOverview(ctx context.Context, data map[string]any) string {
	// coverage 是给规划者判断「哪类测得少 / 要不要扩范围」的信号，与 worker「只做领到的
	// 那条意图、别追未覆盖的点」的职责边界相悖 → 从 worker 视图里剔除。data 是本次 worker
	// 专属的新 map，删键不影响 planner。
	delete(data, "coverage")
	// 第三阶段:同 planner,注入预算随窗口伸缩;窗口未知时退回旧值。
	maxBytes := workerOverviewMaxBytes
	if w := contextWindowFrom(ctx); w > 0 {
		maxBytes = OverviewBudgetBytes(w)
	}
	b := marshalOverviewCapped(data, maxBytes)
	if b == "" {
		return "" // fall back silently: the worker just won't have the global context
	}
	return "\n\n【全局探索态势（只读，帮你把自己这条意图放进大局看）】：\n" +
		"下面是整个任务当前的探索概况（注入紧凑版：列表截为最新一窗，完整内容可用 graph_overview / list_facts / list_assets / node_detail 等工具按需查询）。用途有两个：一是知道别人已发现什么，别重复；二是让你探自己这条意图时，能联想到它和全局的关系。\n" +
		"**发散是好事**：探本意图时尽管深想、多联想。唯一的界线是——别真的动手去执行别的意图（那是别的 worker 的事，由规划者调度）。但凡你联想到有价值的线索（跨资产的联动、疑似另一条利用链的入口、全局层面的可疑点），**务必写进 fact 交规划者**——这是你重要的产出，不是可有可无。宁可多报一条让规划者判断，也别自己咽下去。\n" +
		string(b)
}

// Execute runs one intent. hooks (the per-task Guard) gates every tool call; may
// be nil. emit, if non-nil, receives one ActivityRecord per execution step.
// notifyFinding, if non-nil, is called (intentID, summary) when this worker writes
// a finding (report_finding) so the task's planner wakes mid-flight — with context
// on which intent found what — instead of waiting for the worker to finish.
// Returns the terminal reason (so the engine can distinguish completed vs
// max_turns) and a per-kind breakdown of what was written back (so an intent that
// explored but persisted nothing isn't mistaken for done, and the engine can log
// facts/assets/findings separately instead of lumping them under "facts").
func (w *Worker) Execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string)) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, "", "")
}

// ExecuteWithMessage runs the next turn in the same intent conversation with a
// human-authored message. The HTTP handler does not edit the transcript;
// agentcore records the message as a normal user turn when this Worker starts.
// This keeps Worker continuation identical to the regular agent chat flow.
func (w *Worker) ExecuteWithMessage(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, strings.TrimSpace(requestID), strings.TrimSpace(message))
}

// intentPhase 读意图 payload 里 planner 声明的阶段;读不到就返回空(不限制工具面)。
func intentPhase(n *db.Node) string {
	if n == nil || len(n.Payload) == 0 {
		return ""
	}
	var p struct {
		Phase string `json:"phase"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return normalizeWorkerPhase(p.Phase)
}

func (w *Worker) execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	runDir := ensureRunDir(w.workDir, taskID, intent.ID)
	// 同 planner：态势/digest 注入预算要按窗口推导，opts 与首条 user 消息之前挂上。
	ctx = WithContextWindow(ctx, w.compactionWindow())
	tsx := NewToolSet(ts, name)
	tsx.SetPhase(intentPhase(intent)) // 意图声明了阶段 → worker 只注入该阶段工具面
	tsx.SetFindingRecorder(w.findingRecorder)
	tsx.SetTaskID(taskID)
	tsx.SetProxy(w.proxyAddr, w.proxyCACert)
	tsx.SetWorkDir(runDir)
	tsx.SetTrafficStore(w.trafficStore)
	coverageEnabled := as == nil || as.CoverageEnabled(taskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetOwnerNode(intent.ID)         // assets this worker discovers anchor to its intent → visible to the task
	tsx.SetEnrich(enr)                  // async DNS/HTTP auto-completion for assets this worker writes
	tsx.SetNotifyFinding(notifyFinding) // report_finding 落库时当场唤醒 planner，带上「哪个意图+finding」
	// base = built-in worker tools ∪ host tools (traffic) ∪ default tools (incl. Bash/Glob/Grep);
	// then augment with the agent's visible skills/MCP. During the SDK settlement
	// phase, active probing tools are hidden via Settlement.DisabledTools.
	base := append(tsx.WorkerTools(), w.extraTools...)
	// 保留原生 Go 的 Glob 与 Grep 给 worker，并用 WrapCrossPlatformBash 装饰 Bash 工具
	// （提供进程内 Coreutils 原语、外部安全二进制直调及跨 Worker 全局进程信号量保护）。
	// P3（见 agent/tool_steering.go）：裁掉 SDK 自带的逃生舱工具。
	//   - LS：与保留的 Glob 重复；Sleep：agent 不该靠 sleep 等结果；
	//   - Write/Edit：web 渗透 worker 习惯用它们落 .py 脚本绕开专用工具，这是
	//     「专用工具被通用工具吞掉」的主要来源（http_request 49.4% vs
	//     http_sequence 1.0%）。软逆 worker 需要 Write 落分析产物
	//     （softwareREArtifactSpec），故对 RE 保留。
	// 只裁这几个 SDK 工具，领域工具（http_sequence / http_fuzz / web_recon 等）
	// 一个不动——病根是通用工具吞掉专用工具，不是工具太多。
	trimSDK := []string{"MultiEdit", "LS", "Sleep"}
	if !softwareRE(ts) {
		// apply_patch 与 Write/Edit 是同一份能力，软逆 worker 需要它落分析产物，
		// 其余 worker 一并裁掉（否则等于给它们留了后门）。
		trimSDK = append(trimSDK, "Write", "Edit", "apply_patch")
	}
	defTools := WrapCrossPlatformBash(defaultToolsExcept(trimSDK...), runDir, proxyEnv(w.proxyAddr, w.proxyCACert))
	base = append(base, defTools...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), IntentID: intent.ID})
	isRE := softwareRE(ts)
	tools, def, cleanup := AugmentTools(ctx, "worker", base)
	if isRE {
		tools, def = softwareRETools(tools, def)
	}
	defer cleanup()
	// The run-wide intent is not the current tool action. Do not forward it or
	// inherit a parent run's background into the action reviewer.
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	overview := renderWorkerGraphOverview(ctx, tsx.graphOverviewForPrompt(ctx, workerPromptCaps))
	sysBody := workerSystem(w.proxyAddr, w.proxyCACert, w.workDir, runDir)
	if isRE {
		sysBody = softwareREWorker + softwareREArtifactSpec(runDir) + toolBatchSpec() + langDirective(resolveOutputLang())
	}
	if w.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作约束(若有)注入系统提示,worker 执行时严格遵守
	}
	// 共享环境上下文（批次 6）：整台机器的探测快照（OS/执行器/工具版本），
	// 与 constraintBlock 同一套 system 末尾追加法——同一个任务的所有 worker 看到同一份。
	if brief := w.envBrief(); brief != "" {
		sysBody += "\n\n" + brief
	}
	// 意图块 → 意图锚定资产块 → 启动指令，依次追加到 system 尾部（与 constraintBlock 同一套追加法）。
	// 意图块整体有 8KB 兜底(批次 E):超限时先丢资产块尾部(意图原文在前、必须保住),
	// 完整资产可用 list_assets / asset_neighbors 按需取。
	intentBlock := renderIntentTask(intent)
	if as != nil {
		if ids := intentAssetIDs(intent); len(ids) > 0 {
			if assets, err := as.GetByIDs(ids); err == nil && len(assets) > 0 {
				intentBlock += renderWorkerIntentAssets(assets)
				// 意图明确针对的这些资产 → 自动纳入任务测试范围（与 insertAssets 同一套
				// 保守粒度）。upsertTaskScope 的 ON CONFLICT DO NOTHING + uq_task_scope
				// 唯一索引保证不会重复添加；重跑/重试同样是幂等 no-op。
				// 资产覆盖度功能关闭时不再累积测试范围(分母)。注意:注入瘦身只影响上面
				// 拼进 prompt 的视图,这里仍对【全部】锚定资产累计范围,数据能力不减。
				if coverageEnabled {
					for _, a := range assets {
						_ = as.AddAutoScope(taskID, a.Type, a.Domain, a.URL, a.IP)
					}
				}
			}
		}
	}
	if len(intentBlock) > workerIntentBlockMaxBytes {
		intentBlock = cutUTF8(intentBlock, workerIntentBlockMaxBytes) +
			"\n…（意图块超长已截断；完整资产用 list_assets / asset_neighbors 查。）"
	}
	sysBody += intentBlock
	// 结构化交接：本条意图若是「接着某条没跑完的意图继续」（payload.continues_intent_id），
	// 把那条意图最近的交接单注入 system 尾部。交接单的事实层由服务端机械抽取
	// （跑过什么/写回什么/最后输出/可回读区间），不依赖上一位的文本总结，
	// 因此"没说出口的东西"不会因为换了个子代理就蒸发。
	if src := intentContinues(intent); src > 0 {
		if h, err := ts.IntentHandoff(src); err == nil && h != nil && strings.TrimSpace(h.RenderText) != "" {
			sysBody += h.RenderText
		}
	}
	// 交接纪律：明确要求「边做边写」。等到收尾再补，往往已经没有步数了——
	// 上一任的下一步/假设/负结果就是这样丢的。
	sysBody += "\n\n【交接纪律】你随时可能被步数/时长上限掐断，也可能被别的子代理接手。" +
		"每当确认一个新方向、排除一条死路、或留下未验证的假设，立刻调用 handoff_patch 写下来" +
		"（next_actions / hypotheses / negative_results 各一行，写得越具体越好）。不要等到收尾。"
	if hint := func() string {
		tactical := BuildTacticalRoutingHint(intentBlock, tools, def)
		if isRE {
			if tactical != "" {
				return softwareREHint() + "\n\n" + tactical
			}
			return softwareREHint()
		}
		return tactical
	}(); hint != "" {
		sysBody += "\n\n" + hint
	}
	sysBody += "\n\n开始执行上面这条意图：只做它、只产生事实、assets、finding、做完即停。"
	system, boundary := deferredSystem(sysBody, def)
	// 任务级 deadline(经 ctx 注入)夹逼本 run 的墙钟预算 + 决定收尾词(见 taskclock.go)。
	tc := taskClockFrom(ctx)
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, w.runTimeout)
	settleDisabled := []string{"human_verification_flow", "Bash", "http_request", "http_fuzz", "http_sequence", "web_recon", "net_probe", "dns_probe"}
	settle := wrapupSettlement("worker", settleDisabled)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("worker", settleDisabled, clamped)
	}
	opts := agentcore.Options{
		Provider:        w.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		// WebFetch 走记录代理，其 HTTP 与 curl 一样被留痕；载入代理 CA 让经 MITM
		// 重签的 HTTPS 证书能【正常校验通过】（而非关掉校验）。proxy 空则直连。
		EnableWebFetch: true,
		WebFetchProxy:  w.proxyAddr,
		WebFetchCACert: w.proxyCACert,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立的出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:       w.webSearch.Enabled,
		WebSearchBackend:      w.webSearch.Backend,
		BraveSearchAPIKey:     w.webSearch.BraveKey,
		TavilySearchAPIKey:    w.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: w.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  w.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   w.webSearch.DeepSeekModel,
		WebSearchProxy:        w.webSearch.Proxy,
		// Bash 子命令的 HTTP 默认走记录代理 + 信任其 CA（工具无需 -x/-k）。
		BashEnv:    proxyEnv(w.proxyAddr, w.proxyCACert),
		WorkingDir: runDir,
		MaxTurns:   intentTurnBudget(w.maxTurns), // 与工具额度同量级；额度关闭时不动配置（0 = unlimited）
		// 墙钟预算,轮边界判,不打断半路;0 = 不限。有任务级 deadline 时夹逼到 min(自身预算,
		// 距 deadline 剩余),让本 run 在任务到点时自然进收尾(见 taskclock.go)。
		MaxDuration: maxDur,
		// 命中预算(轮次 OR 时长)→ SDK 跑一轮收尾(隐藏 Bash),把已识别的写回,避免烂尾。
		// clamped(被任务 deadline 夹逼)时用 PromptByReason:因超时=任务到点→任务超时词,
		// 因步数=夹逼窗口内步数先耗尽→回落 per-run 词。非 clamped 维持纯 per-run。
		Settlement: settle,
		// large tool output spills to cmd-output/ with a head + pointer (SDK tool.Capture);
		// full output preserved on disk. 截断上限见 maxToolOutputChars。
		ToolOutputDir: cmdOutDir(runDir),
		// 工具输出进入对话历史的字符上限：超限部分仍全量落盘可重读，头部 12k 足够；
		// 收紧上限直接降低每轮历史重发体积（详见 maxToolOutputChars 的注释）。
		MaxToolOutputChars: ToolOutputChars(w.compactionWindow()),
		Todos:              actool.NewTodoStore(), // 会话级临时待办（TodoWrite），纯规划用，退出即丢
		NonStreaming:       w.nonStreaming(),      // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:          w.maxTokens(),         // 0 = 不单独设限;norma harness 兜底为 32768(harness/query.go maxTokens)
	}
	if hooks != nil { // typed-nil guard: only set when concrete (avoids harness panic)
		opts.Hooks = hooks
	}
	if w.tx != nil { // persist raw LLM conversation; one file per worked intent
		opts.Transcript = w.tx
		opts.SessionID = WorkerSessionID(ts.ID(), intent.ID)
	}
	intentID := intent.ID
	emitWrap := func(r db.Activity) {
		if emit != nil {
			r.NodeID, r.Worker = &intentID, name
			emit(r)
		}
	}
	// 意图 / 启动指令 / 意图锚定资产已随 system prompt 下发（见上方 sysBody 组装）。
	// 这条启动 user 消息只承载【全局态势 overview】——可降级的了解大局信息，压掉无碍。
	// overview 罕见地 marshal 失败为空时，回退一句启动词，避免首轮出现空 user 消息。
	input := overview
	if strings.TrimSpace(input) == "" {
		input = "开始执行 system 里领到的意图：只做它、只产生事实、assets、finding、做完即停。"
	}

	// 实验功能:开启后由 noa 接管上下文压缩(归档集中在 <workDir>/noa/<SessionID> 下,持久)。
	noaSession := WorkerSessionID(ts.ID(), intent.ID)
	// 内置上下文压缩：窗口 × 80% 触发（口径见 compactionConfig）。
	attachCompaction(&opts, w.compactionWindow(), noaSession)
	enableNoa(&opts, w.noaEnabledFn, w.workDir, noaSession, w.compactionWindow(), noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close() // release the session's background-task manager (temp dir + processes)

	// Resume prior conversation if this intent was paused/blocked/exhausted and is
	// being re-run. The transcript ID is deterministic per intent, so if a prior
	// session exists the worker continues from where it left off instead of
	// restarting from scratch.
	alreadyRecorded := false
	if w.tx != nil {
		_ = s.Resume(opts.SessionID)
		alreadyRecorded = requestID != "" && hasWorkerChatMessage(s.Messages(), requestID)
		if len(s.Messages()) > 0 && message == "" {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
			input = "继续执行。"
		} else if len(s.Messages()) > 0 {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
		}
	}
	if message != "" {
		if alreadyRecorded {
			input = "继续执行上一次人工对话输入的新意图。不要重复已经完成的动作。"
		} else if len(s.Messages()) > 0 {
			input = workerChatMarker(requestID) + "\n【人工对话输入的新意图】\n" + message +
				"\n\n请立即按这条人工输入执行，完成后再根据上下文决定原任务是否需要继续。"
		} else {
			input += "\n\n" + workerChatMarker(requestID) + "\n【人工对话输入的新意图】\n" + message +
				"\n\n请优先执行这条人工输入。"
		}
	}

	// Budgets + settlement are owned by the SDK (MaxTurns/MaxDuration + Settlement):
	// on hit it runs a wrap-up turn and finishes with ReasonMaxTurns/ReasonTimeout.
	// MaxDuration now interrupts an in-flight tool at the wall-clock deadline and
	// enters the wrap-up phase on the live ctx, so a run whose tool overran the budget
	// still settles (no external hard-timeout backstop needed). ctx itself carries only
	// pause / planner kill / shutdown, which the engine distinguishes and re-queues/stops.
	ctx = WithContextWindow(ctx, w.compactionWindow())
	// 单意图工具额度（任务 27）：每次 run = 一条意图。额度挂到 ctx 上，由
	// guardedTool 的闸门执行——软提醒 / 硬拒绝 / 强制收尾都在那里。
	budget := newIntentBudget(IntentToolCap())
	ctx = WithIntentBudget(ctx, budget)
	finalText, reason, err := captureRunSession(ctx, s, input, emitWrap)
	wrote := tsx.Writes()
	if budget.Exhausted() || budget.Refused() > 0 {
		// 报告就是本条意图的"交接单正文"：落库后合并进 handoff，人工/下一轮直接可读。
		wrote.Quota = &QuotaOutcome{
			Cap:     budget.cap,
			Calls:   budget.Calls(),
			Refused: budget.Refused(),
			Report:  clipQuotaReport(finalText),
		}
	}
	return reason, wrote, err
}

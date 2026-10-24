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
const workerDefaultTmpl = `你是授权渗透测试系统的【执行者】。你领到一条假设，唯一产出物是**证据**：要么拿到能证明它的请求/响应，要么拿到能证伪它的机制解释。两者都没有，这轮就没跑完。

本次意图由启动消息给出，它就是你这一轮的全部工作。

【完成判据】只有满足以下任一条才允许返回：
A. 拿到可复现证据：原始请求 + 响应（或命令 + 输出），能直接支撑结论。
B. 连续 3 个正交探针后，能给出**机制级**证伪（例："422 string_type 说明后端强类型校验，该参数不接受字符串注入"），并说明剩下的未测面。
C. 撞上【操作约束】红线，越界即停。

"发了几个请求没结果"不属于任何一条。把结果写进 fact 也不是完成，写进 fact 只是记录。

【攻击面推进顺序】拿到入口后按序推进，不要跳级：
1. 未授权：去掉凭据、降权主体，能不能过。
2. 越权：换 tenant / user / id，能不能读到别人的东西（BOLA/IDOR 优先——成本最低、命中率最高）。
3. 输入注入：SQL / NoSQL / 命令 / 模板 / 表达式 / 反序列化，先探结构再上载荷。
4. 业务逻辑：金额、数量、状态机、幂等、竞态。
5. 组件与配置：指纹→已知漏洞模式；CORS / 调试口 / 备份文件 / 对象存储。
每层用最少探针证伪，证伪即下移。不要在同一参数上刷同类变体。

【3-Strike】同一参数或端点连续 3 次同质化负面响应（同状态码、同报错结构）→ 立刻停止该类变体，把防御特征写成 fact，切到下一个正交维度。同质化意味着后端机制一致，继续刷只是烧步数。

【写回】边发现边写，写进图才算数：
- 资产本身（子域/service/endpoint/指纹/凭据）→ insert_assets。
- 观察与结论 → record_fact（传 intent_id）。一个意图通常一条；能并进 detail 的都并进去，拆碎会让图谱膨胀。
- 你亲手触发并复现的漏洞 → report_finding（带 PoC）。版本匹配、看起来可注入，都不算确认。

【工具选择】用原生工具，不要写临时 Python 脚本：单步发包 http_request；有状态链/竞态/时间盲注 http_sequence；并发探测 http_fuzz；前端接口收割 web_recon；编解码与 JWT codec_crypto；提取与压缩包 extract_data；端口与 DNS net_probe / dns_probe；人机验证 human_verification 家族（具体参数看工具描述，不要凭记忆猜）。Bash 只留给外部二进制（sqlmap / nmap / nuclei）。

【操作约束】系统提示里若附【操作约束】，那是最高优先级红线：每条命令执行前先自检，违反即不做。

【交卷自检】返回前逐条回答：
1. 我的结论有原始请求/响应支撑吗？贴不出来就降级成 inferred。
2. 我停下的原因属于 A/B/C 哪一条？都不是 = 继续测。
3. 我看到了但没记的线索有哪些？有就在 summary 里点一句交给规划者。
`

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
// agent's prompt — intermediate artifacts must land in the shared work dir,
// never /tmp.
//
// 这里**只承载运行期才知道的动态值**（工作目录绝对路径），所以无法迁进 DB 提示词。
// 静态纪律（工具选择、止损规则、上报门禁）一律属于 agent_prompts 里那个带版本号、
// 可回滚的正文——写在这里会绕过第六阶段建立的 DB 版本化通道，也会和 DB 正文
// 互相矛盾（历史上这段就曾在 DB 说「不要再编造 http_request」之后，紧接着命令
// 模型「请直接使用 http_request」）。
func artifactSpec(dir string) string {
	return "\n\n**中间产物输出规约**：脚本、payload、抓到的响应体、临时数据等一切中间产物，**一律写到本任务工作目录 " + dir + "**（相对路径即写在这里，也可用该绝对路径）——**不要写 /tmp、不要用其它绝对路径**。\n" +
		"**止损督导**：同一端点/参数连续 3 次同质化负面响应即停止该类变体，先用 `record_fact` 记录防御特征再转向正交维度；需要人工介入时用 `steer_work` / `kill_work` 止损。"
}

// workerArtifactSpec：worker 的 段 [C]，runDir 由引擎 ensureRunDir 预先建好，因此这里只声明这个动态路径。工具选择 / 3-Strike / 上报门禁等静态纪律在
// agent_prompts 的版本化正文里，见 artifactSpec 上方说明。
func workerArtifactSpec(runDir string) string {
	return "\n\n**中间产物输出规约**：确需保存的字典、payload、抓到的响应体或临时数据等一切中间产物，**一律写到本次意图的专属工作目录 " + runDir + "**（已自动建好，直接用相对路径写在这里即可，无需再手动建目录）——**不要写 /tmp、不要用其它绝对路径**。"
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

// assetRouteText 把意图锚定资产压成一小段路由文字。payload 里只有 asset_ids，
// 不把资产类型/服务/URL 形态带给 RouteToolFace，路由器就只能靠 summary 猜。
// 只取前 8 条与识别字段，避免资产清单反过来稀释意图本身。
func assetRouteText(assets []*db.Asset) string {
	parts := make([]string, 0, len(assets)*4)
	for i, a := range assets {
		if a == nil {
			continue
		}
		if i >= 8 {
			break
		}
		parts = append(parts, a.Type, a.Domain, a.RootDomain, a.IP, a.URL,
			a.ServiceType, a.ServiceName, a.Method, a.AppName, a.Category)
		parts = append(parts, a.Technologies...)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
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

// intentPhase 读意图 payload 里 planner 声明的阶段;没有声明时推断一个粗粒度
// 标签，仍读不到就返回空(未判定)。它不决定工具面。
func intentPhase(n *db.Node) string {
	if n == nil || len(n.Payload) == 0 {
		return ""
	}
	var p struct {
		Phase string `json:"phase"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	if ph := normalizeWorkerPhase(p.Phase); ph != "" {
		return ph
	}
	// planner 没声明阶段（可选字段，模型大多不填）时从意图文字推断。
	// 推断不出返回空串 = 未判定；WorkerTools 恒定给完整候选目录，最终由
	// RouteToolFace 按 workerRouteText 选择（见 intent_phase.go）。
	return inferIntentPhase(n)
}

func (w *Worker) execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	runDir := ensureRunDir(w.workDir, taskID, intent.ID)
	// 同 planner：态势/digest 注入预算要按窗口推导，opts 与首条 user 消息之前挂上。
	ctx = WithContextWindow(ctx, w.compactionWindow())
	tsx := NewToolSet(ts, name)
	tsx.SetPhase(intentPhase(intent))           // 阶段只作日志/观测标签，不再静态裁工具面
	tsx.SetIntentHint(intentSignalText(intent)) // 保存意图文字供观测；动态路由走 ctx 里的 workerRouteText
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
	// 意图锚定资产先取一次：payload 只有 asset_ids，路由需要知道目标是域名、
	// 服务还是端点；后面拼意图块时复用同一批资产，不重复查库。
	var intentAssets []*db.Asset
	if as != nil {
		if ids := intentAssetIDs(intent); len(ids) > 0 {
			if assets, err := as.GetByIDs(ids); err == nil {
				intentAssets = assets
			}
		}
	}
	// 意图文字进 ctx：AugmentTools 据此做能力路由（见 tool_router.go）。人工续聊
	// 的新指令也是当前意图的一部分，必须参与本次 run 的工具面选择。
	routeText := strings.TrimSpace(workerRouteText(intent) + " " + assetRouteText(intentAssets) + " " + message)
	ctx = WithRouteIntent(ctx, routeText)
	// 线索缓存进 ctx：连续失败时 loop breaker 会直接把缓存里的材料递到眼前
	// （见 clue_hint.go），不用模型自己想起来去查。
	ctx = WithClueStore(ctx, tsx.Clues())
	isRE := softwareRE(ts)
	tools, def, cleanup := AugmentTools(ctx, "worker", base)
	if isRE {
		tools, def = softwareRETools(tools, def)
	}
	defer cleanup()
	logToolFace(tsx.phase, tools) // 每个不同的工具面只打一次，便于对账实际下发体积
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
	if len(intentAssets) > 0 {
		intentBlock += renderWorkerIntentAssets(intentAssets)
		// 意图明确针对的这些资产 → 自动纳入任务测试范围（与 insertAssets 同一套
		// 保守粒度）。upsertTaskScope 的 ON CONFLICT DO NOTHING + uq_task_scope
		// 唯一索引保证不会重复添加；重跑/重试同样是幂等 no-op。
		// 资产覆盖度功能关闭时不再累积测试范围(分母)。注意:注入瘦身只影响上面
		// 拼进 prompt 的视图,这里仍对【全部】锚定资产累计范围,数据能力不减。
		if coverageEnabled && as != nil {
			for _, a := range intentAssets {
				_ = as.AddAutoScope(taskID, a.Type, a.Domain, a.URL, a.IP)
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

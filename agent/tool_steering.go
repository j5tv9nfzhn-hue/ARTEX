package agent

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"

	"github.com/Autumn-27/artex/intercept"
)

// 工具选择转向（数据驱动）。
//
// 实测某安装 21,459 次工具调用：http_request 占 49.2%，而专为多步会话设计的
// http_sequence 只占 1.0%、web_recon 占 0.5%、binary_inspect 占 0.06%。问题不是
// 工具太多导致选不过来，而是**最通用的那个工具会把专用工具的活全部吞掉**。
//
// 更关键的是频率分布的形状：没有通用替代品的工具（node_detail 6.6%、
// extract_data 5.8%）用得都正常，掉到谷底的恰恰全都有「用 http_request 或 Bash
// 也能凑」的替代路径。所以这里不减少工具数量，而是补上两件此前完全缺失的事：
//
//  1. **决策点的反馈**：同一 host 的连续请求，在下一次调用当场说明该换哪个工具；
//  2. **逃生通道的可见性**：Bash 里写/跑 Python 脚本时当场指出对应的原生工具。
//
// 之所以放在工具结果里而不是提示词里：原规则埋在 14k 字符提示词的中段，属于
// 「Lost in the Middle」召回最差的位置；而工具结果紧贴模型的下一次决策。
//
// ---- 第一版实测反馈与本次修正（2026-10-20）----
//
// 第一版上线后 http_request 占比不降反升（48.3% → 52.9% → 最近 24h 54.1%）。
// 从 tool_usage 的连击分布定位到三个可证伪的缺陷：
//
//  1. `usedSequence || usedFuzz || usedRecon` 一旦任一为真就整体静默。任务 #15 在
//     10:30 用过两次 web_recon，随后 9 分钟内连打 272 次 http_request，一次提示
//     都没有。根因是把「知道这些工具存在」和「知道这一步该用哪个」当成了一件事。
//     → 改为**按能力分别记账**：用过的那个不再提，没碰过的照劝。
//
//  2. 硬拦截的判据是「同 method+host+path 命中 4 次」，而真实连击里路径一直在变
//     （枚举），于是 4 次阈值从未命中，blockedOnce 形同虚设。另外拦一次就永久解除，
//     模型换个参数继续照样没人管。
//     → 判据放宽为「同 host 连续 N 次」，并改成**有预算的冷却**（每 run 最多 2 次，
//     拦截后放行 8 次再评估）。
//
//  3. 提示语从第 3 次起每调用一次说一遍，272 连击意味着 260 段几乎相同的文案，
//     既浪费上下文又会被模型当噪音忽略。
//     → 改为**分级节流**：软提示每 6 次一条，强指令每 20 次一条。
//
// 另外补上「组合链」：只说「该用 http_fuzz」模型可能仍然照旧，但给出一条从侦察到
// 落事实的完整链条，才叫「搭配使用」。

const (
	// httpSteerAfter 起开始软提示：同一 host 连续第 N 次 http_request。
	httpSteerAfter = 3
	// httpStrongAfter 起改为强指令（仍然是执行完再劝，不阻断）。
	httpStrongAfter = 6
	// httpBlockAfter 起允许硬拦截（还需满足预算与冷却条件）。
	httpBlockAfter = 10
	// httpRepeatHits：同一 method+host+path 命中这个次数即判定为「重放」。
	httpRepeatHits = 3
	// httpEnumPaths：同一 host 出现过的不同 path 达到这个数量即判定为「枚举」。
	httpEnumPaths = 6
	// maxTrackedPaths 给 path/key 集合封顶，避免长跑任务里无界增长。
	maxTrackedPaths = 512
	// maxBlocksPerRun 每个 run 的硬拦截预算。拦得太多会把正常验证流程钉死。
	maxBlocksPerRun = 2
	// blockCooldown：拦截后放行这么多次再评估，给模型执行转向的空间。
	blockCooldown = 8
	// softEvery / strongEvery：同类提示的最小间隔（按 hostCalls 计）。
	softEvery   = 6
	strongEvery = 20
	// steerResetGap：距上一次同 host 请求超过这个间隔就重新计数，避免长时间运行里
	// 零散的偶发请求被累加成误报。
	steerResetGap = 3 * time.Minute

	// 原生工具名集中在此，提示语里不手写工具名，避免改名后提示指向不存在的工具。
	nativeHTTPSequence = "http_sequence"
	nativeHTTPFuzz     = "http_fuzz"
	nativeWebRecon     = "web_recon"
)

// 转向提示的边界标记、注入预算与展示剥离。
//
// 这段文案是**给模型的转向指令**，不是命令输出的一部分。第一版每次 Bash 写/跑
// Python 都追加一遍：它随工具结果写进 Activity.Detail，于是界面里每展开一条命令
// 都挂着同一段 <tool_steering> 残留物，长跑任务里模型侧也被同一段字反复冲刷。
// 现在注入与展示分开处理：
//   - 注入侧：同一任务只说 bashSteerBudget 次（见 noteBashPythonSteer）；
//   - 展示侧：capture.go 落库前用 stripSteerNotes 剥掉，界面只剩真正的命令输出。
const (
	steerOpenTag  = "<tool_steering>"
	steerCloseTag = "</tool_steering>"
	// bashSteerBudget 每个任务说几次 Python 转向提示。一次足够——工具清单已经
	// 随结果给过模型，重复只是噪声。
	bashSteerBudget = 1
	// bashSteerStateCap 记账表上限，超过整表清空（与 skillRecall 同口径）。
	bashSteerStateCap = 512
	// steerJSONKey 是 http_* 把提示塞进结果 JSON 时用的字段名。
	steerJSONKey = `"tool_steering"`
)

// httpPattern 是「这一串请求到底在干什么」的判定，决定提示里点名哪个工具。
type httpPattern int

const (
	// patternBurst：路径与接口都没有收敛，只是高频单发。
	patternBurst httpPattern = iota
	// patternEnum：同一 host 下出现大量不同路径——目录/接口枚举。
	patternEnum
	// patternRepeat：同一接口被反复命中——重放/迭代。
	patternRepeat
)

// toolChoiceSteering 是单次 run 内的工具选择计数。每个 ToolSet 一份（= 一次
// worker 执行），run 结束随手回收，不跨任务泄漏。
//
// 并发：ToolSet 的 steer 是值字段，零值必须可用，因此 map 与预算在 ensureInit
// 里惰性建立；所有读写都在 mu 之下。
type toolChoiceSteering struct {
	mu sync.Mutex

	// run 是本 run 的 http_request 总数（跨 host 累计），用来说明整体失控程度。
	run int

	// 以下为「当前 host 窗口」的计数，换 host 或超时即重置。
	lastHost  string
	hostCalls int
	lastAt    time.Time
	paths     map[string]bool // 该 host 出现过的 path
	keyHits   map[string]int  // method+host+path → 命中次数
	maxKeyHit int             // 单接口最高命中次数

	// nextSteerAt 是下一次允许输出提示的 hostCalls 水位（分级节流）。
	nextSteerAt int
	// steers 是当前窗口内已经说过的次数。硬拦截要求「已经说过两次还没改」——
	// 只凭调用次数就拦截，会误伤「逐个验证多条 finding」这类正常单发流程。
	steers int

	// 硬拦截预算与冷却。
	blocksLeft int
	blockCool  int

	// 本 run 是否已经用过专用工具。用过的那个不再劝，没碰过的照劝。
	usedSequence bool
	usedFuzz     bool
	usedRecon    bool
}

// ensureInit 建立零值下缺失的 map 与拦截预算。调用方必须已持有 mu。
func (s *toolChoiceSteering) ensureInit() {
	if s.paths != nil {
		return
	}
	s.paths = make(map[string]bool, 64)
	s.keyHits = make(map[string]int, 64)
	s.blocksLeft = maxBlocksPerRun
}

// resetWindow 丢弃当前 host 的窗口计数。换 host、超时、或模型已经照做换了工具时
// 调用——后者是关键：刚换完工具不该立刻又被同一段文案劝一次。
//
// 不动 blocksLeft：拦截预算属于整个 run，不随窗口刷新。
func (s *toolChoiceSteering) resetWindow() {
	s.hostCalls = 0
	s.maxKeyHit = 0
	s.nextSteerAt = 0
	s.steers = 0
	// 两个 map 都与 host 绑定，直接重建比清空更省事。
	s.paths = make(map[string]bool, 64)
	s.keyHits = make(map[string]int, 64)
}

// markTool records that a purpose-built tool was actually used in this run.
//
// 第一版把「用过」当成「不用再劝」因此整段静默；现在只把对应能力的文案摘掉，
// 并把窗口清零——模型已经照做的证据。
func (s *toolChoiceSteering) markTool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureInit()
	switch name {
	case nativeHTTPSequence:
		s.usedSequence = true
	case nativeHTTPFuzz:
		s.usedFuzz = true
	case nativeWebRecon:
		s.usedRecon = true
	default:
		return
	}
	s.resetWindow()
}

// requestKey derives the repetition identity of one call.
func requestKey(method string, u *url.URL) (host, key string) {
	host = strings.ToLower(u.Host)
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" {
		m = "GET"
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return host, m + " " + host + path
}

// classify 判定这一串请求的形态。顺序有意义：重放优先于枚举——同一接口打很多次
// 同时又探了很多路径时，最该说的是「这是重放」。
func (s *toolChoiceSteering) classify() httpPattern {
	if s.maxKeyHit >= httpRepeatHits {
		return patternRepeat
	}
	if len(s.paths) >= httpEnumPaths {
		return patternEnum
	}
	return patternBurst
}

// noteHTTPRequest records one http_request and returns whether this call should be
// intercepted plus the steering text (empty = nothing to say).
func (s *toolChoiceSteering) noteHTTPRequest(method string, u *url.URL) (block bool, steer string) {
	if u == nil {
		return false, ""
	}
	host, key := requestKey(method, u)
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureInit()

	now := time.Now()
	if s.lastHost != host || (!s.lastAt.IsZero() && now.Sub(s.lastAt) > steerResetGap) {
		s.resetWindow()
	}
	s.lastHost, s.lastAt = host, now
	s.hostCalls++
	s.run++
	if s.blockCool > 0 {
		s.blockCool--
	}
	if len(s.paths) < maxTrackedPaths {
		s.paths[path] = true
	}
	s.keyHits[key]++
	if s.keyHits[key] > s.maxKeyHit {
		s.maxKeyHit = s.keyHits[key]
	}

	// 硬拦截：同 host 连续请求已明显失控，且本 run 还有预算、不在冷却期。
	// 判据从「同 method+host+path 四次」放宽为「同 host 连续 N 次」——272 连击里
	// 路径一直在变，旧判据永远不命中。
	//
	// 另加两道闸，避免误伤：
	//   - steers >= 2：已经劝过两次仍不改，才算「无视」；只凭次数就拦会打断
	//     「逐个验证多条 finding」这类合理的单发流程；
	//   - classify() != patternBurst：必须已有重复或枚举的实证，不能只是「发得多」。
	if s.blocksLeft > 0 && s.blockCool == 0 && s.hostCalls >= httpBlockAfter &&
		s.steers >= 2 && s.classify() != patternBurst {
		s.blocksLeft--
		s.blockCool = blockCooldown
		s.nextSteerAt = s.hostCalls + strongEvery
		return true, s.steerText(host, 2)
	}

	// 分级节流：窗口内只在越过水位时说话，避免长连击里刷屏。
	switch {
	case s.hostCalls >= httpStrongAfter && s.hostCalls >= s.nextSteerAt:
		s.nextSteerAt = s.hostCalls + strongEvery
		s.steers++
		return false, s.steerText(host, 1)
	case s.hostCalls >= httpSteerAfter && s.hostCalls >= s.nextSteerAt:
		s.nextSteerAt = s.hostCalls + softEvery
		s.steers++
		return false, s.steerText(host, 0)
	}
	return false, ""
}

// steerText 生成一次转向提示。
//
// level：0 = 软提示，1 = 强指令，2 = 硬拦截（本次调用不执行）。
//
// 提示由三部分组成：**实测到的形态**（为什么现在就该换）、**本 run 还没用过的
// 工具**（只提没碰过的，用过的绝不再念）、**组合链**（不是孤立工具名，而是从
// 侦察到落事实的完整路径）。
func (s *toolChoiceSteering) steerText(host string, level int) string {
	var b strings.Builder
	switch level {
	case 2:
		b.WriteString("<tool_steering>\n已拦截：本次请求未执行。")
	case 1:
		b.WriteString("<tool_steering>\n⚠️ 强指令：")
	default:
		b.WriteString("<tool_steering>\n")
	}
	fmt.Fprintf(&b, "本 run 累计 %d 次 http_request，当前 host %s 连续 %d 次，涉及 %d 个不同路径，单接口最多重复 %d 次。\n",
		s.run, host, s.hostCalls, len(s.paths), s.maxKeyHit)

	switch s.classify() {
	case patternRepeat:
		fmt.Fprintf(&b, "形态判定：**重放/迭代**（同一接口已命中 %d 次）。改用 `%s`(mode=\"replay\") 读取原始请求、改包重放并自动与上次响应做差分；多步流程用 mode=\"chain\" 维护 Cookie 与变量。\n",
			s.maxKeyHit, nativeHTTPSequence)
	case patternEnum:
		fmt.Fprintf(&b, "形态判定：**目录/接口枚举**（同一 host 下已出现 %d 个不同路径）。改用 `%s` 批量跑：受控并发 + 软 404 基线校准，命中自动入库；逐条手打既慢又没有基线可比。\n",
			len(s.paths), nativeHTTPFuzz)
	default:
		b.WriteString("形态判定：**高频单发、面未收敛**。先回答一句：这些请求里有多少真正验证了新事实？没有新增事实的请求属于重复劳动。\n")
	}

	var avail []string
	if !s.usedSequence {
		avail = append(avail, "`"+nativeHTTPSequence+"`：多步会话 / Cookie / CSRF 回填 / 越权矩阵 / 并发竞态 / 重放改包")
	}
	if !s.usedFuzz {
		avail = append(avail, "`"+nativeHTTPFuzz+"`：参数与路径枚举，受控并发 + 软 404 基线")
	}
	if !s.usedRecon {
		avail = append(avail, "`"+nativeWebRecon+"`：前端 JS / 接口 / 路由 / GraphQL 侦察")
	}
	if len(avail) > 0 {
		b.WriteString("本 run 尚未使用：\n")
		for _, a := range avail {
			b.WriteString("- ")
			b.WriteString(a)
			b.WriteString("\n")
		}
	} else {
		b.WriteString("三个专用工具本 run 都已用过——问题不在「知不知道」，而在当前这一步的粒度。先说明为什么必须单发，再继续。\n")
	}

	b.WriteString("组合链（工具要搭着用，不是单打）：`" + nativeWebRecon + "` 摸清面 → `" + nativeHTTPFuzz +
		"` 批量枚举 → `" + nativeHTTPSequence + "`(mode=\"chain\"/\"bola\") 走多步与越权 → `extract_data` 取数 → " +
		"`record_fact` 落事实 → `report_finding` 出报告。\n")

	switch level {
	case 2:
		fmt.Fprintf(&b, "下一次同类操作请改用上面点名的工具；确需单发时，说明理由并把参数换成与前一次不同的一组（本 run 拦截预算 %d 次，已用 %d）。\n",
			maxBlocksPerRun, maxBlocksPerRun-s.blocksLeft)
	case 1:
		b.WriteString("下一轮**必须**换成上面的工具；继续单发会被拦截。\n")
	}
	b.WriteString("</tool_steering>")
	return b.String()
}

// ---- Bash: Python 脚本识别与转向 ----

var (
	// pyRunRe 命中「直接调用解释器」：python x.py、python3 -c '...'、py -3 等。
	pyRunRe = regexp.MustCompile(`(?i)(^|[;&|]\s*|\bsudo\s+|\benv\s+)(python[0-9.]*|py)\b`)
	// pyFileRe 命中「出现 .py 文件」。
	pyFileRe = regexp.MustCompile(`(?i)\.py\b`)
)

// looksLikePythonScript reports whether a Bash command writes or runs a Python
// script. 只认两种无歧义形态，避免 `grep -r ".py"` 之类的误报。
func looksLikePythonScript(cmd string) bool {
	l := strings.TrimSpace(cmd)
	if l == "" {
		return false
	}
	if pyRunRe.MatchString(l) {
		return true
	}
	if pyFileRe.MatchString(l) {
		return strings.Contains(l, ">") || strings.Contains(l, "tee") || strings.Contains(l, "<<")
	}
	return false
}

// pythonScriptSteer names the native tool that replaces the script the model just
// wrote or ran, based on what the script appears to do.
func pythonScriptSteer(cmd string) string {
	if !looksLikePythonScript(cmd) {
		return ""
	}
	l := strings.ToLower(cmd)
	var hints []string
	if strings.Contains(l, "requests") || strings.Contains(l, "urllib") || strings.Contains(l, "httpx") ||
		strings.Contains(l, "aiohttp") || strings.Contains(l, "session") || strings.Contains(l, "cookie") {
		hints = append(hints, "HTTP 发送与会话 → http_request（单发）或 http_sequence（多步 / Cookie / CSRF / 多身份 / 并发）")
	}
	if strings.Contains(l, "threadpool") || strings.Contains(l, "asyncio") || strings.Contains(l, "concurrent") || strings.Contains(l, "range(") {
		hints = append(hints, "并发与枚举 → http_fuzz（受控并发 + 软 404 基线校准，命中自动入库）")
	}
	if strings.Contains(l, "socket") || strings.Contains(l, "connect(") || strings.Contains(l, "port") {
		hints = append(hints, "端口与服务指纹 → net_probe；DNS → dns_probe")
	}
	if strings.Contains(l, "base64") || strings.Contains(l, "jwt") || strings.Contains(l, "hashlib") ||
		strings.Contains(l, "hmac") || strings.Contains(l, "aes") || strings.Contains(l, "rsa") || strings.Contains(l, "crypto") {
		hints = append(hints, "编解码 / 哈希 / JWT / 加解密 → codec_crypto")
	}
	if strings.Contains(l, "json.") || strings.Contains(l, "re.") || strings.Contains(l, "beautifulsoup") ||
		strings.Contains(l, "bs4") || strings.Contains(l, "etree") || strings.Contains(l, "lxml") {
		hints = append(hints, "结构化取数 → extract_data（json_path / regex / html）")
	}
	if len(hints) == 0 {
		hints = append(hints, "常规探测 → http_request / http_sequence / http_fuzz / web_recon / codec_crypto / extract_data / net_probe / dns_probe")
	}

	var b strings.Builder
	b.WriteString("\n\n<tool_steering>\n")
	b.WriteString("⚠️ 本次用 Bash 写/跑了 Python 脚本。同类能力平台已内置原生 Go 工具：进程内执行、自动写入流量库留证、无引号转义与 & 截断问题。\n")
	for _, h := range hints {
		b.WriteString("- ")
		b.WriteString(h)
		b.WriteString("\n")
	}
	b.WriteString("下一次同类操作请直接调用上述工具；仅当确需外部二进制（sqlmap / nmap / nuclei 等）时才用 Bash。\n")
	b.WriteString("</tool_steering>")
	return b.String()
}

var (
	bashSteerMu   sync.Mutex
	bashSteerSeen = map[string]int{} // 任务 ID → 已说次数
)

// noteBashPythonSteer 是 Bash 工具 Call 的唯一入口：先判形态，再按任务记账。
//
// 无任务上下文（单元测试、独立会话）时不去重——那种场景没有「同一任务」的概念。
func noteBashPythonSteer(ctx context.Context, cmd string) string {
	note := pythonScriptSteer(cmd)
	if note == "" {
		return ""
	}
	taskID := intercept.TaskIDFromContext(ctx)
	if taskID == "" {
		return note
	}
	bashSteerMu.Lock()
	defer bashSteerMu.Unlock()
	if len(bashSteerSeen) > bashSteerStateCap {
		bashSteerSeen = map[string]int{}
	}
	if bashSteerSeen[taskID] >= bashSteerBudget {
		return ""
	}
	bashSteerSeen[taskID]++
	return note
}

// stripSteerNotes 剥掉工具结果里的转向提示，只用于**落库与界面**；模型侧看到的
// 内容不变（剥离发生在 capture 写 Activity 之前）。
//
// 处理两种形态：
//   - <tool_steering>…</tool_steering> 段（Bash 追加的 text block）；
//   - http_* 结果 JSON 里的 "tool_steering" 键（整行删除，并修掉可能的尾逗号）。
//
// 没有闭合标记时按「输出被截断」处理，从标记处切到结尾——宁可少显示，也不让半截
// 标记留在界面上。
func stripSteerNotes(s string) string {
	if s == "" || !strings.Contains(s, "tool_steering") {
		return s
	}
	for {
		start := strings.Index(s, steerOpenTag)
		if start < 0 {
			break
		}
		if rel := strings.Index(s[start:], steerCloseTag); rel >= 0 {
			s = s[:start] + s[start+rel+len(steerCloseTag):]
			continue
		}
		s = s[:start]
	}
	if strings.Contains(s, steerJSONKey) {
		var b strings.Builder
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, steerJSONKey+":") {
				continue
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		s = strings.ReplaceAll(b.String(), ",\n}", "\n}")
		s = strings.ReplaceAll(s, ",\r\n}", "\r\n}")
	}
	return strings.TrimRight(s, "\n")
}

// ---- 原生工具路由 ----

// nativeToolGuide 是「场景 → 原生工具」的固定映射，按本次实际挂载的工具裁剪后渲染。
var nativeToolGuide = []struct {
	name string
	use  string
}{
	{"http_request", "单发一次性请求（一个 payload、一次验证）"},
	{"http_sequence", "多步会话 / Cookie / CSRF 回填 / 越权矩阵 / 并发竞态 / 重放改包"},
	{"http_fuzz", "参数、目录、ID 枚举（受控并发 + 软 404 基线）"},
	{"web_recon", "前端 JS、接口、路由、GraphQL、OpenAPI 侦察"},
	{"codec_crypto", "编解码、哈希、JWT、加解密"},
	{"extract_data", "结构化取数（JSONPath / 正则 / HTML）"},
	{"net_probe", "端口、服务、Banner 探测"},
	{"dns_probe", "DNS 解析、记录、区域传送"},
}

// renderNativeToolRouting lists only the native tools actually mounted for this
// agent, so the hint never names a tool the model cannot call.
//
// 这是此前最大的缺口：tacticalRouteRule 只认 Skill 和 MCP，一旦意图没有命中的
// Skill，整个路由提示里就**没有一句话**告诉模型还有 http_sequence / http_fuzz
// 这些原生工具可用，于是它们连被考虑的机会都没有。
func renderNativeToolRouting(tools []actool.CoreTool) string {
	have := make(map[string]bool, len(tools))
	for _, t := range tools {
		if t != nil {
			have[t.Name()] = true
		}
	}
	var b strings.Builder
	for _, g := range nativeToolGuide {
		if !have[g.name] {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("- 🔧 **原生工具直用（进程内执行、自动入库留证，优先于 Bash/Python 脚本）**：\n")
		}
		fmt.Fprintf(&b, "  - `%s`：%s\n", g.name, g.use)
	}
	if b.Len() == 0 {
		return ""
	}
	b.WriteString("  需要多次请求才能完成同一流程时用 `http_sequence`，**不要用 `http_request` 循环或写 Python 脚本**。\n")
	return b.String()
}

// nativeToolUseMap 供 skill_router 渲染「本场景原生工具」后缀，措辞与
// renderNativeToolRouting 保持一致。
var nativeToolUseMap = func() map[string]string {
	m := make(map[string]string, len(nativeToolGuide))
	for _, g := range nativeToolGuide {
		m[g.name] = g.use
	}
	return m
}()

// nativeToolBadges renders the ∈mounted native tools of a matched tactical route,
// e.g. " ↳ 本场景原生工具：`http_sequence`（多步会话…）、`http_fuzz`（参数枚举…）".
// 未挂载的工具一律不出现——绝不能提示模型去调一个它没有的工具。
func nativeToolBadges(names []string, mounted map[string]bool) string {
	var parts []string
	for _, n := range names {
		if !mounted[n] {
			continue
		}
		use := nativeToolUseMap[n]
		if use == "" {
			parts = append(parts, "`"+n+"`")
			continue
		}
		parts = append(parts, "`"+n+"`（"+use+"）")
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n  ↳ **本场景原生工具**：" + strings.Join(parts, "、")
}

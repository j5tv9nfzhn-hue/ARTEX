package agent

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// 工具选择转向（数据驱动）。
//
// 实测某安装 20,521 次工具调用：http_request 占 49.4%，而专为多步会话设计的
// http_sequence 只占 1.0%、web_recon 占 0.4%、binary_inspect 占 0.05%。问题不是
// 工具太多导致选不过来，而是**最通用的那个工具会把专用工具的活全部吞掉**。
//
// 更关键的是频率分布的形状：没有通用替代品的工具（node_detail 6.7%、
// extract_data 6.0%）用得都正常，掉到谷底的恰恰全都有「用 http_request 或 Bash
// 也能凑」的替代路径。所以这里不减少工具数量，而是补上两件此前完全缺失的事：
//
//  1. **决策点的反馈**：同一 host 的连续请求，在下一次调用当场说明该换 http_sequence；
//  2. **逃生通道的可见性**：Bash 里写/跑 Python 脚本时当场指出对应的原生工具。
//
// 之所以放在工具结果里而不是提示词里：原规则埋在 14k 字符提示词的中段，属于
// 「Lost in the Middle」召回最差的位置；而工具结果紧贴模型的下一次决策。

const (
	// httpSteerAfter 起开始提示：同一 host 连续第 N 次 http_request。
	httpSteerAfter = 3
	// httpStrongAfter 起改为强指令（仍然是执行完再劝，不阻断）。
	httpStrongAfter = 6
	// httpSamePathHit：同 host + 同 path + 同 method 的重复达到这个次数就拦截一次。
	// 这是最无歧义的「本该用 http_sequence」信号——手工重放同一接口。
	httpSamePathHit = 4
	// steerResetGap：距上一次同 host 请求超过这个间隔就重新计数，避免长时间运行里
	// 零散的偶发请求被累加成误报。
	steerResetGap = 3 * time.Minute

	// 原生工具名集中在此，提示语里不手写工具名，避免改名后提示指向不存在的工具。
	nativeHTTPSequence = "http_sequence"
	nativeHTTPFuzz     = "http_fuzz"
	nativeWebRecon     = "web_recon"
)

// toolChoiceSteering 是单次 run 内的工具选择计数。每个 ToolSet 一份（= 一次
// worker 执行），run 结束随手回收，不跨任务泄漏。
type toolChoiceSteering struct {
	mu sync.Mutex

	lastHost string
	sameHost int
	lastKey  string // method + host + path
	sameKey  int
	lastAt   time.Time
	// blockedOnce 保证「同接口重复」的拦截每次运行最多触发一次：拦住一次就够把
	// 模型推回 http_sequence，之后即便它坚持单发也不能被永久钉死。
	blockedOnce bool

	// 本 run 是否已经用过专用工具。用过就不再劝——说明模型已经知道它们存在，
	// 继续提示只会变成噪音。
	usedSequence bool
	usedFuzz     bool
	usedRecon    bool
}

// markTool records that a purpose-built tool was actually used in this run.
func (s *toolChoiceSteering) markTool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch name {
	case "http_sequence":
		s.usedSequence = true
	case "http_fuzz":
		s.usedFuzz = true
	case "web_recon":
		s.usedRecon = true
	}
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

// noteHTTPRequest records one http_request and returns whether this call should be
// intercepted plus the steering text (empty = nothing to say).
func (s *toolChoiceSteering) noteHTTPRequest(method string, u *url.URL) (block bool, steer string) {
	if u == nil {
		return false, ""
	}
	host, key := requestKey(method, u)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if s.lastHost != host || (!s.lastAt.IsZero() && now.Sub(s.lastAt) > steerResetGap) {
		s.sameHost = 0
		s.sameKey = 0
		s.lastKey = ""
		s.blockedOnce = false
	}
	s.lastHost = host
	s.lastAt = now
	s.sameHost++
	if s.lastKey == key {
		s.sameKey++
	} else {
		s.lastKey = key
		s.sameKey = 1
	}

	// 本 run 已经用过任一专用工具：说明模型知道它们存在，继续提示只会变成噪音。
	if s.usedSequence || s.usedFuzz || s.usedRecon {
		return false, ""
	}

	// 同接口反复重放：手工做多步/迭代，正是 http_sequence 该接管的地方。
	if s.sameKey >= httpSamePathHit && !s.blockedOnce {
		s.blockedOnce = true
		return true, fmt.Sprintf(
			"已拦截：对同一接口（%s）的第 %d 次重复请求。\n"+
				"这类重复属于多步/迭代场景，手工逐条重放既丢会话状态也没有意义。请改用 %s：\n"+
				"- mode=\"chain\"：按序执行多请求并自动维护 CookieJar，regex:/json: 提取变量后用 {{var}} 引用（登录→带 Cookie→回填 CSRF 的典型流程）；\n"+
				"- mode=\"replay\"：读取已有 exchange_id 的原始请求，改包后重放并与原响应做差分；\n"+
				"- mode=\"bola\"：多组身份/未授权态自动重放，检测水平与垂直越权；\n"+
				"- mode=\"race\"：内存屏障齐射 N 个并发请求，测双花与竞态。\n"+
				"若确实需要单发（例如先取一个 token），改一下 path 或参数后重试即可——本拦截每次运行只触发一次。",
			key, s.sameKey, nativeHTTPSequence)
	}

	switch {
	case s.sameHost >= httpStrongAfter:
		return false, fmt.Sprintf(
			"⚠️ 强指令：本 run 已连续向 %s 发出 %d 次 http_request，且从未使用过 %s。\n"+
				"接下来的多步流程**必须**改用 %s(mode=\"chain\") 或 %s(mode=\"bola\")，参数/路径枚举改用 %s，前端与接口枚举改用 %s。\n"+
				"不要再手工逐条发请求、也不要为此写 Python 脚本——这些工具在进程内完成且自动写入流量库留证。",
			host, s.sameHost, nativeHTTPSequence,
			nativeHTTPSequence, nativeHTTPSequence, nativeHTTPFuzz, nativeWebRecon)
	case s.sameHost >= httpSteerAfter:
		return false, fmt.Sprintf(
			"<tool_steering>本 run 已连续向 %s 发出 %d 次 http_request。若仍需多个请求才能完成同一流程（登录态、CSRF 回填、多身份对比、并发竞态），"+
				"请立刻改用 %s(mode=\"chain\"/\"bola\"/\"race\")——它自动维护 Cookie 与变量，比手工复制 Set-Cookie 更准；"+
				"参数/目录枚举用 %s；前端与接口枚举用 %s。不要为此写 Python 脚本。</tool_steering>",
			host, s.sameHost, nativeHTTPSequence, nativeHTTPFuzz, nativeWebRecon)
	}
	return false, ""
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

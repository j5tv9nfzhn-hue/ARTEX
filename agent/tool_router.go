package agent

// 能力向量工具路由（第五阶段 · 模块 1）。
//
// ── 为什么要有它 ─────────────────────────────────────────────────────────────
//
// 旧实现把工具面写死成三档（recon / verify / exploit），再按阶段挑一档下发。
// 两个问题：
//
//  1. 三档本身是「硬编码的侦察流程」——假定渗透一定先扫描、再验证、再利用。
//     真实目标的攻击面根本不按这个顺序展开：一个暴露的 Swagger 可能直接给出
//     全部端点，一套 JWT 可能一步越权，反过来一个纯静态站点根本用不上 fuzz。
//  2. 它连自己那点作用都没发挥出来：`AugmentTools` 在 ToolSet 裁完之后又把
//     MCP 工具并回来，实测（2026-10-24 的 artex.db / server_logs）recon 与
//     verify 两个阶段下发的是**同一份 55 个工具**——阶段裁剪被整体抵消。
//
// ── 方案：不写死组合，按「工具自己怎么描述自己」动态路由 ──────────────────────
//
// 每个工具的适用场景不来自人工清单，而是从它自己的
// Name + Description + InputSchema 的属性名与说明 里抽词项构成向量。改一句
// 工具说明，路由立刻跟着变；不存在「第二份需要同步维护的工具清单」。
//
// 打分用 IDF 加权余弦：几乎所有工具说明里都出现的词（请求、参数、可选、http）
// 权重趋近 0；真正区分能力的词（sqli、wordlist、jwt、entropy、race）权重高。
// 于是「扫描端口」自然命中 net_probe / dns_probe，而不会命中 sqli_probe ——
// 这不是因为写了规则，而是因为 sqli_probe 的自我描述里没有「端口」。
//
// 与 Tool Attention（arXiv:2604.21816）的对应关系：
//   - ISO 分数        → 词项向量余弦（离线二进制跑不了 embedding 模型；论文自己的
//                        消融里 TF-IDF 只比 MiniLM 低 8.1pp）
//   - 状态门控        → ToolResolve 的 agent 绑定/禁用先行过滤，再由本层做相关性
//                        选择；协议控制面不参与能力名额竞争
//   - 两阶段惰性加载  → Description 瘦身（tool_lean.go）+ get_tool_help 按需取完整
//                        schema。被摘掉的工具不会被 get_tool_help 重新提升为可调用
//                        工具；需要换面时应由新意图重新路由。
//
// ── 安全阀（比省 token 更重要）──────────────────────────────────────────────
//
//   - 意图文字为空 / 与目录无实质交集 → 原样全量下发，一个都不摘；
//   - 控制面工具（写回、回报、交接、恢复）永远在，但不占 Top-N 名额——少一个
//     worker 有结论也交不出来，那比多花几千 token 严重得多；
//   - Top-N 与最小面只统计非控制面的能力工具：证据只决定「裁到哪」，不该把
//     能力面裁到只剩一个专用工具（它一坏就没有退路）；
//   - ARTEX_TOOL_ROUTE=0 一键关回旧行为（排障用）。

import (
	"context"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	actool "github.com/Autumn-27/norma/tool"
)

// routeIntentKey 把「这次 run 在干什么」从 worker 传到 AugmentTools。
//
// 走 context 而不是给 AugmentTools 加参数：AugmentTools 的签名是给 server 侧
// 的 ToolAugment 钩子用的，加参数会波及每一个调用点；而意图本来就是 run 的
// 属性，放在 ctx 里语义正确。
type routeIntentKey struct{}

// WithRouteIntent 附带本次 run 的意图文字（用于工具路由）。
// 空白意图原样返回（不覆盖已有值），nil ctx 兜底成 Background，避免调用方
// 少传一层 context 时直接 panic。
func WithRouteIntent(ctx context.Context, intent string) context.Context {
	if strings.TrimSpace(intent) == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, routeIntentKey{}, intent)
}

// routeIntentFrom 取出意图文字，没有就是空串（空 = 不路由，全量面）。
func routeIntentFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(routeIntentKey{}).(string); ok {
		return v
	}
	return ""
}

const (
	// toolRouteTopNDefault 是默认每次下发的非控制面能力工具上限。Tool Attention
	// 的模拟里 k=10 已接近饱和；再多，模型的工具选择准确率开始掉；再少，跨面
	// 任务会在中途缺工具。控制面工具另行常驻，不占这 10 个名额。
	toolRouteTopNDefault = 10
	// toolRouteTopNMax 是环境变量也不得突破的能力面上限。
	toolRouteTopNMax = 10
	// toolRouteMinEvidence 是「这次意图真的说到了这个工具」的门槛，单位是
	// 「最高特异性词的当量」：mass = Σ(命中词项 idf / 目录最大 idf)。
	//
	// 为什么门槛不看「命中几个工具」：数量既不能说明匹配好坏，还会把窄意图
	// 误判成路由失败——「对 /login 做 SQL 注入布尔盲注」只该挂 sqli_probe 一个
	// 专用工具，命中 1 个是**正确**结果，不是失败。
	//
	// 为什么不看余弦绝对值：余弦会被中文二元组的跨词垃圾（"户订""单接"）压低，
	// 也会被归一化放大。实测（30 工具目录）：
	//   扫端口      → net_probe mass 3.22
	//   测越权      → http_request mass 1.44
	//   JWT 分析    → codec_crypto mass 4.06
	//   「把今天的天气写成一首五言绝句」→ record_fact mass 0.84（"成一"撞上
	//   record_fact 的"汇总成一条事实"，纯属巧合）
	// 1.2 落在 0.84 与 1.44 之间：真实意图过得去，巧合过不去。
	toolRouteMinEvidence = 1.2
	// toolRouteMinTools 是非控制面能力工具的最小可用面：证据只决定「裁到哪」，
	// 不该裁到只剩一个专用工具——那个工具要是坏了/被 WAF 拦了，worker 连换个
	// 方式试的余地都没有。不足这个数时按余弦次序补齐正分工具，不是随机补。
	toolRouteMinTools = 5
)

// controlPlane 是协议面，不是侦察流程：没有它们，worker 就算测出了东西也交不
// 出来，或者路由漏挂时完全无法自救。刻意保持极小，且按「职责」而非「阶段」归类。
var controlPlane = map[string]string{
	"record_fact":    "写回事实",
	"add_finding":    "写回漏洞",
	"report_finding": "上报漏洞",
	"list_findings":  "查重",
	"cache_clue":     "写线索缓存",
	"query_clues":    "查线索缓存（换分支的唯一依据）",
	"Bash":           "通用恢复手段（路由漏挂时的兜底）",
	"Read":           "读取中间产物",
	// fetch_evidence 是 Planner 唯一的反投毒回溯通道。能力面(Top-N)可以裁掉别的，
	// 这个不行：它一旦被裁，判决被污染时整条链就没有任何纠错手段。
	"fetch_evidence": "反投毒取证通道（受限只读，常驻）",
}

// toolRouteEnabled 读开关，默认开。ARTEX_TOOL_ROUTE=0/false/off 关闭。
func toolRouteEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ARTEX_TOOL_ROUTE"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// toolRouteTopN 读上限，ARTEX_TOOL_ROUTE_TOPN 可覆盖（夹在 5..10）。
func toolRouteTopN() int {
	raw := strings.TrimSpace(os.Getenv("ARTEX_TOOL_ROUTE_TOPN"))
	if raw == "" {
		return toolRouteTopNDefault
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return toolRouteTopNDefault
	}
	if n < toolRouteMinTools {
		n = toolRouteMinTools
	}
	if n > toolRouteTopNMax {
		n = toolRouteTopNMax
	}
	return n
}

// routeTokens 把一个文本切成可比较的词项。
//
//   - 拉丁/数字：按非字母数字切，长度 ≥2 才收（http_request → http, request）
//   - 中日韩：连续汉字取二元组（"端口扫描" → 端口, 口扫, 扫描）——中文没有
//     空格分词，二元组是最省事又能保住局部语义的做法
func routeTokens(s string) []string {
	s = strings.ToLower(s)
	out := make([]string, 0, 96)
	var latin strings.Builder
	var cjk []rune
	flushLatin := func() {
		if latin.Len() >= 2 {
			out = append(out, latin.String())
		}
		latin.Reset()
	}
	flushCJK := func() {
		for i := 0; i+1 < len(cjk); i++ {
			out = append(out, string(cjk[i:i+2]))
		}
		cjk = cjk[:0]
	}
	for _, r := range s {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			flushCJK()
			latin.WriteRune(r)
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r),
			unicode.Is(unicode.Katakana, r), unicode.Is(unicode.Hangul, r):
			flushLatin()
			cjk = append(cjk, r)
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return out
}

// toolRouteDoc 收集一个工具「自我描述」的全部文字。
//
// 名字重复三次是刻意的：工具名是作者对这个能力最凝练的概括（sqli_probe、
// entropy_scan），比描述里的自然语言更可靠，但字数太少，不放大就会被长描述淹没。
func toolRouteDoc(t actool.CoreTool) string {
	var b strings.Builder
	name := t.Name()
	b.WriteString(name + " " + name + " " + name + " ")
	b.WriteString(t.Description())
	if s := t.InputSchema(); s != nil {
		appendSchemaRouteText(&b, s, 0)
	}
	return b.String()
}

// appendSchemaRouteText 递归收集 schema 里真正携带语义的部分：各级 description、
// 属性名与字符串枚举。type/object/properties 这类结构词不写入向量，避免所有工具
// 都被同一批 JSON 样板词稀释。map 遍历顺序随机，但向量化对顺序不敏感。
func appendSchemaRouteText(b *strings.Builder, v any, depth int) {
	if depth > 16 {
		return // 外部 MCP schema 不可信：深度封顶，避免畸形嵌套把路由拖死。
	}
	switch x := v.(type) {
	case map[string]any:
		if d, ok := x["description"].(string); ok {
			b.WriteString(" ")
			b.WriteString(d)
		}
		if props, ok := x["properties"].(map[string]any); ok {
			for k, prop := range props {
				b.WriteString(" ")
				b.WriteString(k)
				appendSchemaRouteText(b, prop, depth+1)
			}
		}
		for _, key := range []string{"items", "additionalProperties", "anyOf", "oneOf", "allOf"} {
			if child, ok := x[key]; ok {
				appendSchemaRouteText(b, child, depth+1)
			}
		}
		if enum, ok := x["enum"].([]any); ok {
			for _, e := range enum {
				if s, ok := e.(string); ok {
					b.WriteString(" ")
					b.WriteString(s)
				}
			}
		}
		if enum, ok := x["enum"].([]string); ok {
			for _, s := range enum {
				b.WriteString(" ")
				b.WriteString(s)
			}
		}
	case []any:
		for _, e := range x {
			appendSchemaRouteText(b, e, depth+1)
		}
	}
}

// routeCatalog 是「一次路由」的目录快照：文档频率与 IDF 只在这批工具上统计。
// 每次都重建，避免跨 run 复用导致 idf 漂移（不同 agent 的工具集合不同）。
type routeCatalog struct {
	idf    map[string]float64
	maxIDF float64
	vecs   []map[string]float64
	docs   []string
}

func newRouteCatalog(tools []actool.CoreTool) *routeCatalog {
	n := len(tools)
	c := &routeCatalog{
		idf:  make(map[string]float64, 512),
		vecs: make([]map[string]float64, n),
		docs: make([]string, n),
	}
	tokenized := make([][]string, n)
	df := make(map[string]int, 512)
	for i, t := range tools {
		doc := toolRouteDoc(t)
		c.docs[i] = doc
		toks := routeTokens(doc)
		tokenized[i] = toks
		seen := make(map[string]struct{}, len(toks))
		for _, tk := range toks {
			if _, ok := seen[tk]; ok {
				continue
			}
			seen[tk] = struct{}{}
			df[tk]++
		}
	}
	for tk, f := range df {
		// 平滑 IDF：出现在所有工具里的词权重 1.0，只出现在一个工具里的词接近
		// log(1+N)。不额外维护停用词表——停用词会自己沉底。
		c.idf[tk] = math.Log((1+float64(n))/(1+float64(f))) + 1
	}
	// 目录里没出现过的词项（查询侧独有）按「最高特异性」计权，而不是 0。
	//
	// 这一条不是细节，是置信度算得对不对的关键：若未登录词项直接取 0，它们在
	// L2 归一化时被整个抹掉——「把今天的天气写成一首五言绝句」十三个词项里
	// 恰好一个「成一」与 record_fact 的「汇总成一条事实」撞上，归一化后这一个
	// 偶然重合就变成余弦 1.0 的「完美匹配」，路由会把工具面裁到只剩 record_fact。
	// 反过来，未登录词项带着高权重留在分母里，「覆盖率」才真的反映「这句话有
	// 多少内容被这个工具接住了」。
	c.maxIDF = math.Log(1+float64(n)) + 1
	for i := range tools {
		c.vecs[i] = c.vectorizeTokens(tokenized[i])
	}
	return c
}

// vectorizeTokens 把词项列表变成 tf-idf 稀疏向量并做 L2 归一化。
func (c *routeCatalog) vectorizeTokens(toks []string) map[string]float64 {
	if len(toks) == 0 {
		return nil
	}
	tf := make(map[string]float64, len(toks))
	for _, tk := range toks {
		tf[tk]++
	}
	vec := make(map[string]float64, len(tf))
	var norm float64
	for tk, f := range tf {
		weight, ok := c.idf[tk]
		if !ok {
			weight = c.maxIDF
		}
		w := (1 + math.Log(f)) * weight
		vec[tk] = w
		norm += w * w
	}
	if norm == 0 {
		return nil
	}
	norm = math.Sqrt(norm)
	for tk := range vec {
		vec[tk] /= norm
	}
	return vec
}

// vectorize 用目录的 idf 给任意文本编码（查询侧）。
func (c *routeCatalog) vectorize(s string) map[string]float64 {
	return c.vectorizeTokens(routeTokens(s))
}

// evidence 统计「查询里有多少特异性词汇落在这个工具的自述里」。
//
// 每个命中词项按 idf/maxIDF 计一个当量：只在一个工具里出现的词记满 1，
// 到处都是的词（请求、参数、可选）接近 0。于是 mass≈3 意思是「意图里有三个
// 只属于少数工具的专有说法出现在这个工具的介绍里」，而 mass≈0.8 大多是
// 一个中文二元组的偶然重合。
func (c *routeCatalog) evidence(q, doc map[string]float64) float64 {
	var m float64
	for tk := range q {
		if _, ok := doc[tk]; !ok {
			continue
		}
		weight := c.idf[tk]
		if weight == 0 {
			weight = c.maxIDF
		}
		m += weight / c.maxIDF
	}
	return m
}

// cosine 计算两个稀疏向量的余弦相似度（两侧都已归一化，点积即余弦）。
func cosine(a, b map[string]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// 遍历较短的一侧。
	if len(a) > len(b) {
		a, b = b, a
	}
	var s float64
	for tk, v := range a {
		if w, ok := b[tk]; ok {
			s += v * w
		}
	}
	return s
}

type routeHit struct {
	idx      int
	name     string
	score    float64 // 余弦：用于排序（已按工具说明长度归一化）
	evidence float64 // 证据质量：用于判定「这条意图是否真的说到了它」
}

// RankTools 返回按相关度降序的工具下标（不裁剪）。导出给测试与诊断用。
func RankTools(intent string, tools []actool.CoreTool) []routeHit {
	if len(tools) == 0 {
		return nil
	}
	cat := newRouteCatalog(tools)
	q := cat.vectorize(intent)
	hits := make([]routeHit, 0, len(tools))
	for i, t := range tools {
		hits = append(hits, routeHit{
			idx:      i,
			name:     t.Name(),
			score:    cosine(q, cat.vecs[i]),
			evidence: cat.evidence(q, cat.vecs[i]),
		})
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return hits[a].name < hits[b].name
	})
	return hits
}

// RouteToolFace 按意图挑出这次真正要下发的工具面。
//
// 返回顺序保持原始顺序（不是相关度顺序）：同一组工具每次都以同样的顺序出现在
// 提示里，prompt cache 的前缀才稳定。
func RouteToolFace(intent string, tools []actool.CoreTool) []actool.CoreTool {
	return routeToolFace(intent, tools, nil)
}

// routeToolFace 是 RouteToolFace 的实现。alwaysKeep 里的名字与控制面一样不占
// 原生 schema 的 Top-N 名额：AugmentTools 用它保住 DeferredInfo 指向的工具——
// 那些工具的 schema 本就被 SDK 扣住、只经 Search/ExecuteExtraTool 惰性加载，
// 路由若把 CoreTool 本体摘掉，DeferredTools 名单与 Skill 解锁都会指向空处。
func routeToolFace(intent string, tools []actool.CoreTool, alwaysKeep map[string]bool) []actool.CoreTool {
	if !toolRouteEnabled() || len(tools) == 0 {
		return tools
	}
	resident := func(name string) bool {
		_, isControl := controlPlane[name]
		return isControl || alwaysKeep[name]
	}
	topN := toolRouteTopN()
	candidateCount := 0
	for _, t := range tools {
		if !resident(t.Name()) {
			candidateCount++
		}
	}
	if candidateCount <= topN {
		return tools // 能力目录本来就小，裁了反而是负收益（省不下多少，却多一次判断）
	}
	intent = strings.TrimSpace(intent)
	if intent == "" {
		return tools // 判不出意图 → 全量，绝不猜
	}

	hits := RankTools(intent, tools)
	keep := make(map[int]bool, topN+len(controlPlane)+len(alwaysKeep))
	// 控制面与 deferred 常驻面永远在：即使证据为 0 也不摘，且不占能力 Top-N 名额。
	for i, t := range tools {
		if resident(t.Name()) {
			keep[i] = true
		}
	}
	selected := 0 // 只统计需要原生下发 schema 的非控制面能力工具
	best := 0.0
	for _, h := range hits {
		if resident(h.name) {
			continue
		}
		if h.evidence > best {
			best = h.evidence
		}
		if selected >= topN {
			continue
		}
		if h.evidence < toolRouteMinEvidence {
			continue
		}
		keep[h.idx] = true
		selected++
	}
	if selected == 0 {
		// 意图与能力目录没有实质交集（闲聊、纯目标名、新领域）。此时裁剪
		// 等于让 worker 闭着眼睛干活，宁可多花几千 token。
		log.Printf("[tools] 意图与工具目录无实质匹配（最高证据 %.2f < %.2f），本次退回全量面：意图=%q",
			best, toolRouteMinEvidence, truncRunesLog(intent, 40))
		return tools
	}
	// 最小可用面：只补非控制面的正分能力工具，补到 toolRouteMinTools。
	if selected < toolRouteMinTools {
		for _, h := range hits {
			if selected >= toolRouteMinTools {
				break
			}
			if resident(h.name) || keep[h.idx] || h.score <= 0 {
				continue
			}
			keep[h.idx] = true
			selected++
		}
	}

	out := make([]actool.CoreTool, 0, len(keep))
	dropped := make([]string, 0, len(tools)-len(keep))
	for i, t := range tools {
		if keep[i] {
			out = append(out, t)
		} else {
			dropped = append(dropped, t.Name())
		}
	}
	log.Printf("[tools] 能力路由 能力=%d/%d 总面=%d/%d 证据=%.2f 意图=%q 摘掉=%s",
		selected, candidateCount, len(out), len(tools), best, truncRunesLog(intent, 40), strings.Join(dropped, ","))
	return out
}

// truncRunesLog 只给日志用：按 rune 截断，避免把中文字符切成半个。
func truncRunesLog(s string, max int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= max {
		return string(rs)
	}
	return string(rs[:max]) + "…"
}

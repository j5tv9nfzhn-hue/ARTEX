package agent

// 上下文预算：把「模型到底有多大窗口」和「到多少 token 该压」这两件事从
// 用户手填的一格输入框里拿出来，改成有默认、有上限、能从真实报错里自愈的策略。
//
// 参考三个开源实现的共识（2026-10 调研）：
//
//	Codex CLI : 窗口来自模型目录（model catalog），不是自由输入；有效窗口再打 95%，
//	            自动压缩上限 = min(用户配置, 窗口×90%)，配置无法突破 90%。
//	OpenCode  : usable = 模型 limit.context − maxOutputTokens（无则 20K 兜底），
//	            用【API 实际回报的 token】判断溢出；溢出后 compact 一次再重试。
//	ZCode     : 阈值 = 窗口 − min(maxOutput, 21K) − 13K 缓冲；200K 窗口约 83% 触发，
//	            128K 窗口约 73% 触发；窗口来自服务端下发或模型元数据。
//
// 三个实现都不允许「窗口」虚高：窗口一虚高，阈值就永远到不了，最后只能等供应商
// 静默截断（或者 413）。ARTEX 之前把 context_window_k 原样当窗口用，声明 1000K
// 直接把阈值抬到 800K，8 天一次都没触发——本文件的 ResolveContextWindowTokens
// 就是修这个：默认按 200K 计，超出上限一律夹到上限并告警，真实上限靠报错学习。

import (
	"errors"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	// defaultContextWindowCapK 是「声明窗口」的可信上限（K token）。
	// 200K 与 ZCode 的 DEFAULT_COMPACT_CONTEXT_WINDOW、ARTEX 自己的缺省窗口一致：
	// 绝大多数 OpenAI 兼容网关的真实窗口落在 128K–256K，声明 1000K 基本是填错了。
	defaultContextWindowCapK = 200

	// 输出侧预留：参考 ZCode 的 min(maxOutput, 21K) + 13K 安全缓冲。
	reserveOutputTokens = 21000
	reserveSafetyTokens = 13000
)

// ContextWindowCapK 返回声明窗口的信任上限。ARTEX_CONTEXT_WINDOW_CAP_K 可覆盖
// （真·1M 窗口的模型请显式设大，否则会被按 200K 压缩——宁可多压几次，不能漏压）。
func ContextWindowCapK() int {
	if raw := strings.TrimSpace(os.Getenv("ARTEX_CONTEXT_WINDOW_CAP_K")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= minWindowK {
			return v
		}
	}
	return defaultContextWindowCapK
}

var windowClampWarned sync.Map

// ResolveContextWindowTokens 把用户声明的窗口（K）解析成实际用于压缩阈值的 token 数。
// clamped=true 表示声明值超过了可信上限，已按上限计算（调用方负责告警一次）。
func ResolveContextWindowTokens(declaredK int) (tokens int, clamped bool) {
	k := declaredK
	if k <= 0 {
		k = defaultWindowK
	}
	if k < minWindowK {
		k = minWindowK
	}
	if limit := ContextWindowCapK(); k > limit {
		k, clamped = limit, true
	}
	if k > maxWindowK {
		k, clamped = maxWindowK, true
	}
	return k * 1000, clamped
}

// MinContextWindowTokens 是可接受的最小窗口——低于它压缩已无从谈起。
func MinContextWindowTokens() int { return minWindowK * 1000 }

// warnWindowClamped 对同一个声明值只告警一次，避免每次装配 agent 都刷屏。
func warnWindowClamped(declaredK, effective int) {
	if _, loaded := windowClampWarned.LoadOrStore(declaredK, true); loaded {
		return
	}
	log.Printf("[compaction] 上下文窗口声明 %dK 超过可信上限 %dK，按 %dK 计算压缩阈值。"+
		"（想用真·大窗口请设 ARTEX_CONTEXT_WINDOW_CAP_K）",
		declaredK, ContextWindowCapK(), effective/1000)
}

// reservedForCompaction 是压缩时必须给「输出 + 摘要 + 下一轮增长」留出的空间。
// 窗口很小时按 1/4 收缩，避免预留把窗口本身吞掉（32K 窗口下 34K 预留会算出负阈值）。
func reservedForCompaction(window int) int {
	if window <= 0 {
		return 0
	}
	r := reserveOutputTokens + reserveSafetyTokens
	if quarter := window / 4; r > quarter {
		r = quarter
	}
	if r < 4000 {
		r = 4000
	}
	return r
}

// CompactionTriggerTokens 返回「用到多少 token 就该压缩」。
//
// 取两条线里更早的一条：
//   - ratio × 窗口（默认 80%，ARTEX_COMPACT_RATIO 可调）——与 Codex 的 90%、
//     OpenCode 的 usable、ZCode 的「窗口 − 34K」落在同一区间；
//   - 窗口 − 预留——保证压缩来得及，不会把输出空间吃掉。
//
// 结果不低于窗口的 1/4（再小就变成每轮都压，反而更慢更贵），但**不高于绝对上限**
// （CompactionTriggerCapTokens，默认 120K）：窗口声明得再大，也不能让会话一路涨到
// 80 万 token 才压——那正是 1000K 虚窗口下 8 天不触发的成因。
//
// 绝对上限放在最后一步，优先级高于 1/4 下限：1M 窗口时 floor(250K) 会顶穿成本线，
// 此时以下限换成本是不可接受的。
func CompactionTriggerTokens(window int) int {
	if window <= 0 {
		window = defaultWindowK * 1000
	}
	target := int(float64(window) * CompactionRatio())
	if soft := window - reservedForCompaction(window); soft < target {
		target = soft
	}
	if floor := window / 4; target < floor {
		target = floor
	}
	if cap := CompactionTriggerCapTokens(); cap > 0 && target > cap {
		target = cap
	}
	return target
}

// ---- 供应商报错里捞真实窗口 ----
//
// 各家措辞不一样，但都会把真实上限写进去。能捞到就直接把它当成这个 profile 的
// 观测上限（比声明值可信）；捞不到就整体降档——总之不能让声明值一直虚高。

var contextOverflowHints = []string{
	"context length", "context_length", "context window", "context size",
	"maximum context", "too many tokens", "prompt is too long", "input is too long",
	"exceeds the context", "reduce the length", "status 413", "exceed_context_size",
}

// IsContextOverflowError 判断是不是「请求超出上下文窗口」类错误。
// 这类错误重试同一份请求必然还失败，必须靠压缩而不是重试。
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, h := range contextOverflowHints {
		if strings.Contains(s, h) {
			return true
		}
	}
	var apiErr interface{ StatusCode() int }
	if errors.As(err, &apiErr) && apiErr.StatusCode() == 413 {
		return true
	}
	return false
}

var contextLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`maximum context length is (\d{4,8})`),
	regexp.MustCompile(`context window of (\d{4,8})`),
	regexp.MustCompile(`context size \((\d{4,8})\)`),
	regexp.MustCompile(`maximum (?:context|of) (\d{4,8}) tokens`),
	regexp.MustCompile(`available context (?:size )?\(?(\d{4,8})\)?`),
	regexp.MustCompile(`max(?:imum)?[_ ]?(?:context[_ ]?)?(?:window|length|size)[":= ]+(\d{4,8})`),
}

// ProviderContextLimit 从错误文本里捞供应商声明的真实窗口；捞不到返回 0。
// 只认 4–8 位整数（1K–99M），避免把状态码、耗时当成窗口。
func ProviderContextLimit(err error) int {
	if err == nil {
		return 0
	}
	s := strings.ToLower(err.Error())
	for _, re := range contextLimitPatterns {
		if m := re.FindStringSubmatch(s); len(m) == 2 {
			if v, e := strconv.Atoi(m[1]); e == nil && v >= 8000 {
				return v
			}
		}
	}
	return 0
}

// ---- 压缩/注入预算（第三阶段：参数重设计） ----
//
// 这一组函数把原先四个写死的常量换成「窗口 × 内容量」推导出来的预算：
//
//	旧                          新
//	MaxTokens: 800              DigestBudgetTokens(inputTokens, window)
//	600 字符/条 digest          DigestInjectRunes(window)
//	maxToolOutputChars = 12000  ToolOutputChars(window)
//	plannerOverviewMaxBytes=16K  OverviewBudgetBytes(window)
//
// 依据（2026-10 核对后的外部实践）：
//   - Hermes Agent：摘要预算 = 内容量×0.20，下限 2000、上限 min(5%窗口, 12K)；触发阈值
//     另有一条绝对上限 threshold_tokens，防止「大窗口骑满、成本失控」；注入侧用 token
//     预算而不是条数预算；先用确定性规则剪旧工具结果，再决定要不要摘要。
//   - OpenCode：单条工具结果上限取「窗口的 1/8」并夹在 2K–16K 字符；最近的工具输出
//     受保护、更旧的才剪；子代理返回父级时按字节预算二次裁剪。
//
// 三条不变量：
//  1. 所有预算都以**夹取后**的窗口为基准（ResolveContextWindowTokens 的结果），
//     声明窗口虚高不会把预算一起放大；
//  2. 摘要永远不长于输入（≤0.6×input），预算是天花板不是目标；
//  3. 截断只裁正文，不裁可回溯指针（id / 落盘路径），见 truncateDigestBodies 与
//     clipToolOutputHeadTail。

const (
	// digestBudgetFloorTokens 是单条冷摘要的下限：够写「结论 / 证据 / 未决」三句。
	// 不采用 Hermes 的 2000——它压的是对话中段（输入动辄几万 token），而 ARTEX 的
	// block 输入通常只有 1–3K token，2000 会让摘要比输入还长。
	digestBudgetFloorTokens = 512
	// digestBudgetCeilTokens 是上限：再长就该拆块，而不是写更长的摘要。
	digestBudgetCeilTokens = 4096
	// digestInputShare 是「摘要 / 输入」的目标比例。Hermes 用 0.20 压对话中段；
	// ARTEX 的节点 summary 本身已经是压缩过的，信息密度更高，故取 0.30。
	digestInputShare = 0.30
	// digestNeverExpand 保证摘要永远不长于输入。
	digestNeverExpand = 0.60

	digestInjectFloorRunes = 800  // 单条注入正文下限（字符）
	digestInjectCeilRunes  = 2000 // 单条注入正文上限（字符）
	digestTotalFloorRunes  = 6000 // 全部 digest 注入总量下限
	digestTotalCeilRunes   = 24000

	toolOutputFloorChars = 4000
	toolOutputCeilChars  = 24000

	overviewFloorBytes = 24000
	overviewCeilBytes  = 64000

	// injectionSharePercent 是「每轮注入总量占窗口」的硬上限（overview + digests）。
	injectionSharePercent = 12
)

// clampInt 把 v 夹到 [lo, hi]；lo > hi 时以 lo 为准（不同窗口下的极端取值）。
func clampInt(v, lo, hi int) int {
	if lo > hi {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// 说明：文本 token 估算复用 maxtokens.go 的 estimateTextTokens（按 ASCII/宽字符分别
// 计权），不在这里另立一套口径——两套估算打架会让预算函数的单测与线上表现对不上。

// DigestBudgetTokens 计算一条冷摘要该给多少输出 token。
func DigestBudgetTokens(inputTokens, windowTokens int) int {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	if inputTokens <= 0 {
		return digestBudgetFloorTokens
	}
	budget := int(float64(inputTokens) * digestInputShare)
	ceiling := digestBudgetCeilTokens
	if pct := int(float64(windowTokens) * 0.05); pct < ceiling {
		ceiling = pct
	}
	budget = clampInt(budget, digestBudgetFloorTokens, ceiling)
	// 永远不长于输入：小 block 上这条优先于下限——摘要比原文还长毫无意义。
	// 绝对地板 32 token 只为「输入小到没有摘要价值」时别退化成 0 长度调用。
	capByInput := int(float64(inputTokens) * digestNeverExpand)
	if capByInput < 32 {
		capByInput = 32
	}
	if budget > capByInput {
		budget = capByInput
	}
	if budget < 32 {
		budget = 32
	}
	return budget
}

// DigestInjectRunes 是注入侧**单条** digest 正文的字符上限。
func DigestInjectRunes(windowTokens int) int {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	// 窗口 / 50 ≈ 128K → 2560，夹到 800–2000。
	return clampInt(windowTokens/50, digestInjectFloorRunes, digestInjectCeilRunes)
}

// DigestTotalRunes 是注入侧**全部** digest 正文的总字符预算。
func DigestTotalRunes(windowTokens int) int {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	// 0.06 × 窗口 × 4 字符/token ≈ 窗口 × 0.24。
	return clampInt(windowTokens*6/25, digestTotalFloorRunes, digestTotalCeilRunes)
}

// ToolOutputChars 是单条工具输出进入对话历史的字符上限。
//
// 语义与 OpenCode 的「窗口的 1/8」一致，并夹在 4K–24K：小窗口不能让一条输出吃掉
// 半个上下文，大窗口也没必要无限放宽（超出的部分原本就落盘可重读）。
func ToolOutputChars(windowTokens int) int {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	return clampInt(windowTokens/8, toolOutputFloorChars, toolOutputCeilChars)
}

// OverviewBudgetBytes 是 planner/worker 每轮态势注入的字节预算。
func OverviewBudgetBytes(windowTokens int) int {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	return clampInt(windowTokens*2/5, overviewFloorBytes, overviewCeilBytes)
}

// InjectionBudgetRunes 是「每轮注入总量」的硬上限（含 overview 与 digests）。
//
// 这是一条守恒式：每加一个注入面，就从同一个池子里取，不允许各自定上限然后一起把
// 窗口吃满。超出时由调用方按固定优先级降级（先 digest 正文 → 再旧 facts → 绝不丢
// open_intents / finding_list）。
func InjectionBudgetRunes(windowTokens int) int {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	return clampInt(windowTokens*injectionSharePercent/100*4, 12000, 96000)
}

// CompactionTriggerCapTokens 是触发线的绝对上限：窗口再大也不许把触发点推到这之上。
//
// Hermes 的 threshold_tokens 就是这条线；ARTEX 之前把 1000K 声明窗口直接当触发基准，
// 阈值被抬到 800K，8 天一次都没压过。ARTEX_COMPACT_CAP_TOKENS 可覆盖（0=不设上限）。
func CompactionTriggerCapTokens() int {
	raw := strings.TrimSpace(os.Getenv("ARTEX_COMPACT_CAP_TOKENS"))
	if raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			return v
		}
	}
	return 120000
}

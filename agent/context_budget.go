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
// 结果不低于窗口的 1/4（再小就变成每轮都压，反而更慢更贵）。
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

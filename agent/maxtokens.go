package agent

// 输出上限钳制（max_tokens clamping）——把「请求想要多少输出」夹进「端点真能给的范围」。
//
// 为什么要单开一层：max_tokens 是请求侧的旋钮，但它同时受三条服务侧事实约束，
// 任何一条被忽略都会在真实网关上炸，而且是两种完全相反的炸法：
//
//	配太大 → 请求直接被拒：Anthropic 3.7/4 起 prompt+max_tokens 超出窗口不再自动
//	         下调，直接 400；vLLM/SGLang 也是超了 max_model_len 就拒绝、不裁剪。
//	         线上更常见的是「配置 64K、模型只吃 8K」这种能力错配，一样 400。
//	钳太狠 → 回答被截成半句甚至空白：思考型模型先把 max_tokens 烧在 thinking 上，
//	         正文一个 token 都轮不到（finish=length、content 为空）。opencode 曾经
//	         把整条链路上限写死 32K（#29363/#20078/#1735），kilocode 则因为估算偏差
//	         把 max_tokens 塌到 1024（#14959）——都是同一个错误的两面。
//
// 所以规则只有一条：三层上限取最小，只降不升，逼到墙角就 stand down。
//
//	effective = min(requested, 模型输出上限, 窗口 − 估算输入 − 余量)
//
// 「逼到墙角」指 remaining < floor（floor = min(requested, 4096)）。这时不发那个
// 注定被截断的小上限，而是原样发出去，让 400/413 走既有的上下文溢出自愈路径
// （IsContextOverflowError → 压缩 → 重试）。宁可报「上下文超了」，也不要静默
// 变成「模型不说话」——后者在界面上和「模型挂了」完全无法区分。
//
// 位置只有一个：Config.NewProvider() 的最外层。按 openclaw #127560 的结论，钳制
// 必须落在「最后一个同时持有请求上限和模型容量」的构造点，否则每加一条新调用链
// 就漏一次。这一层同时也覆盖了 SDK 内部的重试/续写路径，它们和普通请求走同一个
// Provider。
//
// 明确不做的事：
//   - 不向上钳：requested 小就保持小，绝不因为「模型能出 64K」就替调用方加码。
//     title/摘要这类内部调用要的就是几百 token，放大只会变慢变贵。
//   - 不把「未配置」补成某个魔法常量。requested=0 保持 0（OpenAI 兼容协议下
//     max_tokens 字段直接省略，由端点用自己的默认值）；写死 32K 会重演 opencode
//     的 bug，写死「模型最大值」则会把 vLLM 的会话卡死（pi #4675）。
//   - 不动压缩阈值。窗口/触发点仍由 ContextWindowK + CompactionTriggerTokens 决定，
//     本文件的窗口只用来算「这一轮还能留多少给输出」。

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Autumn-27/norma/llm"
)

const (
	// clampFloorTokens 是钳制的下限：被窗口逼到比它还小的时候不再钳，改为 stand down。
	// 4096 与 claudish #441 的下限一致——再小的上限连一段像样的正文都装不下，
	// 发出去只会得到截断的内容，而截断的内容往往比没有内容更难排查。
	clampFloorTokens = 4096
	// clampMinMarginTokens 是余量的下限：窗口里除了「输入 + 输出」，还要装下这一轮
	// 工具结果增长、时间戳/缓存键之类的开销。2048 是 claudish 经验值，够挡住常见抖动。
	clampMinMarginTokens = 2048
	// clampMarginDivisor 让余量随输入规模一起长：长上下文的估算误差绝对值更大，
	// 固定 2048 挡不住 100K 级别输入上 5% 的偏差。
	clampMarginDivisor = 64
)

// maxTokensDecision 是一次钳制的完整决策，保留全部中间量是为了可观测：
// 出了问题要能一眼看出「为什么最后发的是这个数」，而不是只看到一个结果。
type maxTokensDecision struct {
	Requested int    // 调用方想发多少（0 = 不设限）
	Effective int    // 实际发多少（0 = 字段省略）
	Reason    string // unset / no_data / stand_down_overflow / stand_down_headroom / model_max_output / window_headroom
	Window    int    // 本次判定使用的上下文窗口（token）
	EstInput  int    // 估算的输入 token（system + messages + tools）
	Margin    int    // 预留余量
	StandDown bool   // true = 判定为「不该钳」，请求按原样发出
}

// OutputCeiling 返回模型自身的单次输出上限（0 = 未知）。
// 只认供应商上报/内建目录的实测值：用户配置（Config.MaxTokens）由调用方自己落进
// 请求，不在这里再当一次上限——否则「连接测试固定要 8192」这种内部调用会被用户的
// 小配置误伤，推理模型思考到一半就断，测试报「无回复」而实际配置是好的。
func (c Config) OutputCeiling() int {
	if c.MaxOutputDetected > 0 {
		return c.MaxOutputDetected
	}
	return 0
}

// decideMaxTokens 是本文件的全部策略，纯函数（不读配置以外的状态），便于单测。
func (c Config) decideMaxTokens(req llm.CompletionRequest) maxTokensDecision {
	d := maxTokensDecision{Requested: req.MaxTokens, Effective: req.MaxTokens}
	if req.MaxTokens <= 0 {
		// 未设限：保持未设限。OpenAI 兼容协议会省略字段（max_tokens omitempty），
		// Anthropic 适配器自己兜 8192——两者都安全，加码反而危险。
		d.Reason = "unset"
		return d
	}
	window := c.CompactionWindow()
	d.Window = window
	est := estimateRequestTokens(req)
	d.EstInput = est
	if window <= 0 || est <= 0 {
		d.Reason = "no_data"
		return d
	}
	margin := est / clampMarginDivisor
	if margin < clampMinMarginTokens {
		margin = clampMinMarginTokens
	}
	d.Margin = margin
	headroom := window - est - margin
	if headroom <= 0 {
		// 输入本身就装不下：这时候钳 max_tokens 毫无意义（钳到 1 也只是让报错换个说法），
		// 原样发出，交给上下文溢出自愈去压缩。
		d.Reason = "stand_down_overflow"
		d.StandDown = true
		return d
	}

	eff := req.MaxTokens
	reason := ""
	if capTokens := c.OutputCeiling(); capTokens > 0 && eff > capTokens {
		eff = capTokens
		reason = "model_max_output"
	}
	if eff > headroom {
		eff = headroom
		if reason != "" {
			reason += "+window_headroom"
		} else {
			reason = "window_headroom"
		}
	}
	if eff >= req.MaxTokens {
		d.Reason = "within_limits" // 两条上限都没碰到：不动
		return d
	}
	// 下限保护：只对被「窗口」逼下来的时候生效。被模型能力逼下来的是真上限
	// （比如 2K 输出的老模型），照发；被窗口逼到 4K 以下则说明这一轮本来就装不下，
	// 发个小值只会把「溢出」变成「空回复」。
	if floor := min(req.MaxTokens, clampFloorTokens); eff < floor && strings.Contains(reason, "window_headroom") {
		d.Reason = "stand_down_headroom"
		d.StandDown = true
		return d
	}
	d.Effective = eff
	d.Reason = reason
	return d
}

// clampMaxTokens 是 decideMaxTokens 的落地形式：只改需要改的那一个字段。
func (c Config) clampMaxTokens(req llm.CompletionRequest) llm.CompletionRequest {
	d := c.decideMaxTokens(req)
	if d.StandDown {
		noteMaxTokensDecision(c.Model, d)
		return req
	}
	if d.Effective != d.Requested {
		req.MaxTokens = d.Effective
		noteMaxTokensDecision(c.Model, d)
	}
	return req
}

// maxTokensLogged 保证同一种决策只刷一行日志。provider 会随配置变化重建，
// 用全局表去重比挂在实例上更稳（每次重建都不会重新刷一遍）。
var maxTokensLogged sync.Map

// noteMaxTokensDecision 把钳制结果写进日志，供「为什么回复变短了」这类问题定位。
// 只记「改变了的」与「判定过但不改的」，正常请求（无钳制）一行都不写。
func noteMaxTokensDecision(model string, d maxTokensDecision) {
	if d.Reason == "unset" || d.Reason == "no_data" || d.Reason == "within_limits" {
		return
	}
	key := fmt.Sprintf("%s|%d|%d|%s", model, d.Requested, d.Effective, d.Reason)
	if _, loaded := maxTokensLogged.LoadOrStore(key, true); loaded {
		return
	}
	if d.StandDown {
		log.Printf("[llm] max_tokens 未钳制（%s）：请求 %d，估算输入 %d + 余量 %d 已触及窗口 %d，交给上下文溢出自愈",
			d.Reason, d.Requested, d.EstInput, d.Margin, d.Window)
		return
	}
	log.Printf("[llm] max_tokens 钳制（%s）：请求 %d → 实际 %d（窗口 %d，估算输入 %d，余量 %d）",
		d.Reason, d.Requested, d.Effective, d.Window, d.EstInput, d.Margin)
}

// estimateRequestTokens 估算一次请求的输入 token 数。
//
// 只用于钳制，不用于计费/展示，所以取「宁可高估」的算法：低估会让 max_tokens 偏大，
// 直接把请求顶出窗口（就是我们要防的那个 400）；高估只会让输出上限小一点，而
// 有 floor 与 stand-down 兜着，不会变成空回复。
//
// 口径：ASCII 4 字符 ≈ 1 token（英文/代码/JSON 的经验值）；非 ASCII 按 1 字符 ≈
// 1 token（中文在主流分词器上约 0.6–1.5 token/字，取 1 属于偏安全的一侧）；
// 每消息/每块再加少量固定开销（role、分隔符、JSON 结构）。
func estimateRequestTokens(req llm.CompletionRequest) int {
	total := 0
	for _, s := range req.System {
		total += estimateTextTokens(s) + 4
	}
	for _, m := range req.Messages {
		total += 4
		for _, b := range m.Content {
			total += estimateBlockTokens(b)
		}
	}
	// 工具定义是每轮都要重发的固定成本，且 schema 常常比正文还长，必须算进去。
	for _, t := range req.Tools {
		total += estimateTextTokens(t.Name) + estimateTextTokens(t.Description) + 8
		if t.InputSchema != nil {
			if raw, err := json.Marshal(t.InputSchema); err == nil {
				total += estimateTextTokens(string(raw))
			}
		}
	}
	return total
}

// estimateBlockTokens 逐块估算，口径与 norma compaction.EstimateTokens 对齐，
// 只把「按字符数除以 4」换成区分 ASCII/非 ASCII 的更保守版本。
func estimateBlockTokens(b llm.ContentBlock) int {
	switch b.Type {
	case llm.BlockText:
		return estimateTextTokens(b.Text) + 3
	case llm.BlockThinking:
		return estimateTextTokens(b.Thinking) + 3
	case llm.BlockToolUse:
		return estimateTextTokens(b.Name) + estimateTextTokens(string(b.Input)) + 8
	case llm.BlockToolResult:
		n := 8
		for _, cb := range b.Content {
			n += estimateBlockTokens(cb)
		}
		return n
	default:
		return estimateTextTokens(b.Text) + 3
	}
}

// estimateTextTokens 见 estimateRequestTokens 的口径说明。
func estimateTextTokens(s string) int {
	if s == "" {
		return 0
	}
	ascii, wide := 0, 0
	for _, r := range s {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			wide++
		}
	}
	return (ascii+3)/4 + wide
}

// clampProvider 是最外层的 Provider 包装：进到内层之前把 MaxTokens 改好。
// 内层依次是 思考拆分 / 工具入参截流 / 真实 provider，它们都不看 MaxTokens，
// 所以放最外层与放最内层效果等价，但最外层能覆盖未来新增的包装层。
type clampProvider struct {
	inner llm.Provider
	cfg   Config
}

func (p *clampProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return p.inner.Stream(ctx, p.cfg.clampMaxTokens(req))
}

func (p *clampProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.inner.Complete(ctx, p.cfg.clampMaxTokens(req))
}

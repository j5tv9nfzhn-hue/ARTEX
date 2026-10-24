package agent

// 上下文压缩的可观测装配：把 norma 内置 compaction 包一层。压缩真的发生 / 摘要失败时
// 各留一行日志。
//
// 为什么需要这一层：SDK 的 AutoCompact 有三条完全静默的 "什么都不做" 路径 ——
// summarize 为 nil、连续 3 次摘要失败后 tripped 永久关闭、可摘要区间为空 —— 全都只
// return 不打日志。线上看到的现象就是 "上下文早就顶到窗口了，压缩却没发生"，
// 而日志里一条线索都没有。这一层不改任何阈值与行为，只把证据补上：
//
//   - 摘要失败：前两次报错，第三次起改用本地兜底摘要（SDK 因此不会关掉自动压缩）；
//   - 压缩发生：报输入规模、等效窗口与触发点，便于和 UI 的占用率对账。
//
// 注意：wrapper 只实现 harness.Compactor 的三个方法，**不实现** ContextView。
// 内置压缩是 "就地改写消息数组" 的语义，不能被当成投影型上下文管理器
// （agentcore/noa_off_test.go 专门盯着这条）。

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
)

// observedCompactor 在保留内置压缩全部行为的前提下加日志。
type observedCompactor struct {
	label string
	cfg   compaction.Config
	inner *compaction.Compactor
}

func newObservedCompactor(label string, cfg *compaction.Config, summary compaction.Summarizer) *observedCompactor {
	return &observedCompactor{label: label, cfg: *cfg, inner: compaction.New(*cfg, summary)}
}

func (c *observedCompactor) Pre(ctx context.Context, msgs []llm.Message, lastInputTokens int) []llm.Message {
	// 第三阶段：先做无 LLM 预剪（旧工具输出），再交给 SDK 判断要不要摘要。
	// 顺序不能反——先摘要会让模型为即将被丢掉的日志付一次调用费。
	if pruned, n := pruneToolOutputs(msgs, c.cfg.ContextWindow, compactionTrigger(&c.cfg)); n > 0 {
		log.Printf("[compaction] %s: 预剪 %d 条旧工具输出（无 LLM，可逆：重跑命令即可）", c.label, n)
		msgs = pruned
	}
	out := c.inner.Pre(ctx, msgs, lastInputTokens)
	c.report(msgs, out, lastInputTokens, "自动")
	return out
}

func (c *observedCompactor) Reactive(ctx context.Context, msgs []llm.Message) ([]llm.Message, bool) {
	out, ok := c.inner.Reactive(ctx, msgs)
	c.report(msgs, out, 0, "溢出兜底")
	return out, ok
}

func (c *observedCompactor) IsOverflow(err error) bool { return c.inner.IsOverflow(err) }

// report 只在 "多出一条压缩边界" 时打日志 —— 那是压缩真的发生过的唯一证据。
func (c *observedCompactor) report(before, after []llm.Message, lastInputTokens int, trigger string) {
	if c == nil || len(after) <= len(before) {
		return
	}
	if llm.LastBoundaryIndex(after) <= llm.LastBoundaryIndex(before) {
		return
	}
	log.Printf("[compaction] %s: 已压缩（%s）输入=%d tok → API 视图估算 %d tok（等效窗口 %d，触发点 %d）",
		c.label, trigger, lastInputTokens,
		compaction.EstimateTokens(llm.MessagesForAPI(after)),
		c.cfg.ContextWindow, compactionTrigger(&c.cfg))
}

const (
	// compactionSummaryFailBudget 是「连续多少次模型摘要失败后改用本地兜底摘要」。
	// 与 SDK 的 maxAutoCompactFails(=3) 对齐：SDK 收到第 3 个错误就会把这个会话的
	// 自动压缩永久关掉，此后上下文只涨不压，直到溢出把这次运行打死——「压缩自己停了」
	// 就是这条路径。所以第 3 次起我们不再给 SDK 递错误，而是自己兜一段本地摘要，
	// 压缩照常发生，等模型恢复再切回模型摘要。
	compactionSummaryFailBudget = 3
	// compactionSummaryRetryAfter 是兜底模式下重新尝试模型摘要的间隔。
	compactionSummaryRetryAfter = 10 * time.Minute
)

// observedSummarizer 记录摘要调用的成败，并在模型摘要连续失败时用本地兜底摘要顶上。
// base 为 nil 时保持 nil（AutoCompact 关闭），不要替调用方把闭区间打开。
func observedSummarizer(label string, base compaction.Summarizer) compaction.Summarizer {
	if base == nil {
		return nil
	}
	var mu sync.Mutex
	fails := 0
	var skipTill time.Time
	return func(ctx context.Context, msgs []llm.Message) (string, error) {
		mu.Lock()
		cooling := !skipTill.IsZero() && time.Now().Before(skipTill)
		mu.Unlock()
		if !cooling {
			summary, err := base(ctx, msgs)
			if err == nil {
				mu.Lock()
				fails, skipTill = 0, time.Time{}
				mu.Unlock()
				log.Printf("[compaction] %s: 摘要完成 %d 条 → %d 字", label, len(msgs), len([]rune(summary)))
				return summary, nil
			}
			mu.Lock()
			fails++
			n := fails
			if n >= compactionSummaryFailBudget {
				skipTill = time.Now().Add(compactionSummaryRetryAfter)
			}
			mu.Unlock()
			if n < compactionSummaryFailBudget {
				log.Printf("[compaction] %s: 摘要失败（第 %d/%d 次）: %v", label, n, compactionSummaryFailBudget, err)
				return summary, err
			}
			log.Printf("[compaction] %s: 摘要连续失败 %d 次，改为本地兜底摘要，自动压缩不关闭（%s 后重试模型）: %v",
				label, n, compactionSummaryRetryAfter, err)
		}
		return fallbackSummary(msgs), nil
	}
}

// fallbackSummary 在模型摘要不可用时用本地规则生成折叠区间的占位摘要。
//
// 折叠必须留下「摘要」这个位置，否则 SDK 会把它记成一次失败，三次之后永久停掉
// 自动压缩。本地摘要保留每轮的工具名、命令/参数首行与结果首行——足够接着干活，
// 并且显式标明它来自本地规则，避免模型把脚手架当成已核实的事实。
func fallbackSummary(msgs []llm.Message) string {
	var b strings.Builder
	b.WriteString("【自动摘要暂不可用：以下是折叠区间内的关键行，由本地规则提取，不是模型总结】\n")
	lines := 0
	for _, m := range msgs {
		if lines >= 80 {
			break
		}
		for _, blk := range m.Content {
			if lines >= 80 {
				break
			}
			switch blk.Type {
			case llm.BlockToolUse:
				b.WriteString("- 调用 " + blk.Name + " " + clipSummaryLine(string(blk.Input)) + "\n")
				lines++
			case llm.BlockToolResult:
				for _, cb := range blk.Content {
					if lines >= 80 {
						break
					}
					if t := clipSummaryLine(cb.Text); t != "" {
						b.WriteString("- 结果 " + t + "\n")
						lines++
					}
				}
			case llm.BlockThinking:
				if t := clipSummaryLine(blk.Thinking); t != "" {
					b.WriteString("- 思考 " + t + "\n")
					lines++
				}
			default:
				if t := clipSummaryLine(blk.Text); t != "" {
					b.WriteString("- " + t + "\n")
					lines++
				}
			}
		}
	}
	b.WriteString("（需要细节时用证据台账或工具重跑取回，不要凭这段占位摘要下结论。）")
	return b.String()
}

// clipSummaryLine 取首行并截断，保证兜底摘要不会自己长成一个新的大上下文。
func clipSummaryLine(s string) string {
	s = strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

// attachCompaction 装配内置上下文压缩，等价于 agentcore.NewSession 里那句
//
//	compaction.New(*opts.Compaction, compaction.ProviderSummarizer(opts.Provider, opts.MaxTokens, opts.NonStreaming))
//
// 只是多了一层观测。必须在 opts.Provider / opts.MaxTokens / opts.NonStreaming 就位之后
// 调用；noa 若在之后启用会直接覆盖 opts.Compactor（实验功能优先，符合设计）。
func attachCompaction(opts *agentcore.Options, window int, label string) {
	cfg := compactionConfig(window)
	opts.Compaction = nil // 只留一个上下文管理器，避免 agentcore 报 "两者同时设置"
	opts.Compactor = newObservedCompactor(label, cfg,
		observedSummarizer(label, compaction.ProviderSummarizer(opts.Provider, opts.MaxTokens, opts.NonStreaming)))
}

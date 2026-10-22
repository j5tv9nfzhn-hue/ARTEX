package agent

import (
	"context"
	"iter"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
)

// toolInputEvery 是工具入参帧的节流窗口。与文本帧同一个节奏（约 25 帧/秒）：
// 参数通常几十到几千字符，40ms 一帧既看得出「在打字」，又不会把 SSE 刷爆。
const toolInputEvery = 40 * time.Millisecond

// wrapToolStream 给 Provider 套一层「工具入参截流」，让界面在工具真正执行之前
// 就能逐字看到模型正在生成的参数。
//
// 为什么在这一层：norma 的 llm 层本来就在逐帧发 tool_input_delta（Anthropic 的
// input_json_delta、OpenAI 的 function.arguments 都是按 SSE chunk 出来的），是
// harness 把它攒到 tool_use 结束才一次性吐一整块。ARTEX 自己的 Provider 装饰链
// （llmrec、限流都在同一层）位于 harness 之下、SSE 之上，正好截得到原始增量——
// 因此不必 fork SDK，norma 升级也不会把这里改掉。
//
// 只发两种帧，都是「累计值」而不是增量，界面拿到直接整体替换：
//
//	tool_start {ToolUseID, Tool}         一次调用的参数开始流
//	tool_input {ToolUseID, Tool, Text}   截至目前的完整参数 JSON
//
// 已知边界：OpenAI 的并行工具调用会在同一条消息里按 index 交错下发参数，而
// llm.StreamEvent 不带 index（Accumulator 也是同一个限制），所以交错时后一段
// 参数会被挂到最近一次 tool_start 上。串行调用（含 Anthropic 全部情况、以及
// 绝大多数实际请求）不受影响；落库口径与最终参数始终以 harness 定稿的
// tool_use 为准，实时帧只影响「生成中」的观感。
func wrapToolStream(inner llm.Provider) llm.Provider {
	if inner == nil {
		return nil
	}
	return &toolStreamProvider{inner: inner}
}

type toolStreamProvider struct{ inner llm.Provider }

func (p *toolStreamProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.inner.Complete(ctx, req)
}

func (p *toolStreamProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		var (
			toolID, toolName string
			input            strings.Builder
			lastEmit         time.Time
		)
		reset := func() {
			toolID, toolName = "", ""
			input.Reset()
			lastEmit = time.Time{}
		}
		for ev, err := range p.inner.Stream(ctx, req) {
			if err == nil {
				switch ev.Type {
				case llm.SEMessageStart:
					// 新一轮模型输出：清掉上一轮的名字与参数缓冲。同 provider 重试
					// 会重放整条流，也会从这里重新开始，因此不会串台。
					reset()
				case llm.SEToolUseStart:
					reset()
					toolID, toolName = ev.ToolID, ev.ToolName
					emitLive(ctx, LiveDelta{Kind: "tool_start", Tool: toolName, ToolUseID: toolID})
				case llm.SEToolInputJSON:
					input.WriteString(ev.Text)
					if now := time.Now(); now.Sub(lastEmit) >= toolInputEvery {
						lastEmit = now
						emitLive(ctx, LiveDelta{Kind: "tool_input", Tool: toolName, ToolUseID: toolID, Text: input.String()})
					}
				}
			}
			if !yield(ev, err) {
				return
			}
			if err != nil {
				return
			}
		}
	}
}

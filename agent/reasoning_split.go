package agent

// ---------- 内联思考拆分（content 里的推理标签）----------
//
// 为什么需要这一层：norma 只把「显式字段」里的推理当思考 —— OpenAI 系看
// reasoning_content / reasoning / reasoning_text（llm/openai.go 的 pickReasoning），
// Anthropic 系看 thinking 块。可现实里的网关不总是这么规矩：
//
//   - 有的网关不认 reasoning_content，把上游的 <think>…</think> 原样透传进 content；
//   - 有的网关按请求负载均衡到不同上游实现，同一条会话的两次请求可能一次带
//     reasoning_content、一次把推理写进 content —— 于是「思考变正文」看起来是
//     概率性的：同一个模型、同一条会话，有时正常、有时不正常。
//
// 只要推理落在 content 里，SDK 就只有 KindText 可发；capture 把它落成
// activity.kind="text"（落库侧 addDelta 已按 kind 断句，不会把 thinking 记成
// text）；前端所有对话页都按 kind 渲染 —— chat 页把 kind="text" 当成最终正文
// 整段展开，折叠的 ThinkingRow 根本不会出现。这是「思考过程被显示为正文」的
// 唯一通路，所以修在最早的一层：Provider 装饰链（与 toolstream.go 同层，
// 位于 harness 之下、SSE 之上），一次性覆盖 chat / 任务会话 / 侧问等全部页面。
// 唯一例外是 Anthropic 格式：它回放 thinking 块必须带 signature，而拆出来的思考块
// 没有签名，下一轮会被 400 拒掉——装配处（provider.go 的 NewProvider）因此跳过它。
//
// 拆分规则刻意保守，避免误伤正文里的代码示例：
//   - 只在「消息开头（允许前导空白）」识别起始标签；正文一旦开始就不再识别；
//   - 标签必须整体匹配（<think> / <thinking> / <reasoning> / <thought>）；
//   - 标签可能被 SSE 切在两个 chunk 里，所以尾部会留一小段 holdback；
//   - 流结束时仍在标签内（没有闭合标签）→ 已收内容按思考收尾：模型把整段推理
//     内联时就是这个形状，按正文渲染反而是错的。

import (
	"context"
	"iter"
	"strings"

	"github.com/Autumn-27/norma/llm"
)

// 只认这几个成对标签。加新标签时同步更新 web 侧的 REASONING_OPEN_RE
// （web/src/components/transcript.tsx），两边判断必须一致，否则历史行与新数据
// 在界面上的归类会不一致。
var (
	inlineThinkOpens  = []string{"<think>", "<thinking>", "<reasoning>", "<thought>"}
	inlineThinkCloses = []string{"<｜end▁of▁thinking｜>", "</thinking>", "</reasoning>", "</thought>", "</think>"}
)

// inlineThinkPrefixLen 返回 s 末尾有多少字节可能是某个标签的开头。SSE 会把标签
// 切在两个 chunk 之间，这段尾巴必须扣住，不能当正文发出去。
func inlineThinkPrefixLen(s string, tags []string) int {
	best := 0
	for _, tag := range tags {
		limit := len(tag)
		if limit > len(s)+1 {
			limit = len(s) + 1
		}
		for n := 1; n < limit; n++ {
			if n > len(s) {
				break
			}
			if n > best && strings.HasSuffix(s, tag[:n]) {
				best = n
			}
		}
	}
	return best
}

// inlineThinkIndex 返回 s 中最早出现的标签及其下标（没有则 -1）。
func inlineThinkIndex(s string, tags []string) (int, string) {
	best, hit := -1, ""
	for _, tag := range tags {
		if i := strings.Index(s, tag); i >= 0 && (best < 0 || i < best) {
			best, hit = i, tag
		}
	}
	return best, hit
}

// inlineThinkMatch 判断 s 是否以某个起始标签开头。
func inlineThinkMatch(s string) (string, bool) {
	for _, tag := range inlineThinkOpens {
		if strings.HasPrefix(s, tag) {
			return tag, true
		}
	}
	return "", false
}

// inlineThinkIsPrefix 判断 s 是否是某个起始标签的「还没收全」的前缀。
func inlineThinkIsPrefix(s string) bool {
	for _, tag := range inlineThinkOpens {
		if len(s) < len(tag) && strings.HasPrefix(tag, s) {
			return true
		}
	}
	return false
}

// inlineThinkSplitter 是一个逐 chunk 的状态机：输入正文增量，输出
// ("thinking" | "text", 片段)。状态只跨 chunk 存在，一个模型请求一份。
type inlineThinkSplitter struct {
	inThink bool   // 当前在推理标签内
	started bool   // 正文已经开始：之后不再识别起始标签
	matched bool   // 至少拆分过一次（用来判断是否需要改写 message）
	buf     string // 尚未判定归属的尾巴
}

func (s *inlineThinkSplitter) feed(delta string, emit func(kind, text string)) {
	s.buf += delta
	for {
		if s.inThink {
			if idx, tag := inlineThinkIndex(s.buf, inlineThinkCloses); idx >= 0 {
				if idx > 0 {
					emit("thinking", s.buf[:idx])
				}
				s.buf = s.buf[idx+len(tag):]
				s.inThink = false
				continue
			}
			keep := inlineThinkPrefixLen(s.buf, inlineThinkCloses)
			if n := len(s.buf) - keep; n > 0 {
				emit("thinking", s.buf[:n])
				s.buf = s.buf[n:]
			}
			return
		}
		if s.started {
			if s.buf != "" {
				emit("text", s.buf)
				s.buf = ""
			}
			return
		}
		trimmed := strings.TrimLeft(s.buf, " \t\r\n")
		if trimmed == "" {
			return // 只有空白：可能还在等起始标签
		}
		if tag, ok := inlineThinkMatch(trimmed); ok {
			s.matched = true
			s.inThink = true
			s.buf = trimmed[len(tag):]
			continue
		}
		if inlineThinkIsPrefix(trimmed) {
			return // 标签还没收全，继续攒
		}
		// 不是标签：整段当正文（含前导空白），此后不再识别起始标签。
		emit("text", s.buf)
		s.buf = ""
		s.started = true
		return
	}
}

// flush 收尾：把扣住的尾巴吐出去。仍在标签内时按思考收尾（见文件头注释）。
func (s *inlineThinkSplitter) flush(emit func(kind, text string)) {
	if s.buf == "" {
		return
	}
	if s.inThink {
		emit("thinking", s.buf)
	} else {
		emit("text", s.buf)
	}
	s.buf = ""
}

// splitInlineThinking 给 Provider 套一层「内联思考拆分」。幂等：重复包装不会叠加。
func splitInlineThinking(inner llm.Provider) llm.Provider {
	if inner == nil {
		return nil
	}
	if _, ok := inner.(*inlineThinkingProvider); ok {
		return inner
	}
	return &inlineThinkingProvider{inner: inner}
}

type inlineThinkingProvider struct{ inner llm.Provider }

func (p *inlineThinkingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	msg, stop, usage, err := p.inner.Complete(ctx, req)
	if err != nil {
		return msg, stop, usage, err
	}
	return splitInlineThinkingMessage(msg), stop, usage, nil
}

func (p *inlineThinkingProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		var sp inlineThinkSplitter
		stopped := false
		push := func(kind, text string) {
			if stopped || text == "" {
				return
			}
			ev := llm.StreamEvent{Type: llm.SETextDelta, Text: text}
			if kind == "thinking" {
				ev.Type = llm.SEThinkingDelta
			}
			if !yield(ev, nil) {
				stopped = true
			}
		}
		for ev, err := range p.inner.Stream(ctx, req) {
			if err != nil {
				sp.flush(push) // 出错前把已收到的尾巴交出去，别吞内容
				yield(ev, err)
				return
			}
			switch ev.Type {
			case llm.SEMessageStart:
				// 新一轮模型输出（含 provider 内部重试重放的整条流）：状态归零。
				sp = inlineThinkSplitter{}
			case llm.SETextDelta:
				sp.feed(ev.Text, push)
				if stopped {
					return
				}
				continue
			}
			if !yield(ev, err) {
				return
			}
		}
		sp.flush(push)
	}
}

// splitInlineThinkingMessage 是 Complete（非流式）路径的同一条规则。
// 没有命中内联推理时原样返回，绝不改动 message 的形状。
func splitInlineThinkingMessage(msg llm.Message) llm.Message {
	var sp inlineThinkSplitter
	var think, text strings.Builder
	out := make([]llm.ContentBlock, 0, len(msg.Content)+1)
	flushThink := func() {
		if think.Len() > 0 {
			out = append(out, llm.ContentBlock{Type: llm.BlockThinking, Thinking: think.String()})
			think.Reset()
		}
	}
	flushText := func() {
		if text.Len() > 0 {
			out = append(out, llm.TextBlock(text.String()))
			text.Reset()
		}
	}
	piece := func(kind, s string) {
		if kind == "thinking" {
			flushText()
			think.WriteString(s)
			return
		}
		flushThink()
		text.WriteString(s)
	}
	for _, b := range msg.Content {
		switch b.Type {
		case llm.BlockText:
			sp.feed(b.Text, piece)
		case llm.BlockThinking:
			// 显式 thinking 块本身就是思考：先收尾，再并进同一条思考。
			sp.flush(piece)
			flushText()
			think.WriteString(b.Thinking)
		default:
			sp.flush(piece)
			flushThink()
			flushText()
			out = append(out, b)
		}
	}
	sp.flush(piece)
	flushThink()
	flushText()
	if !sp.matched {
		return msg
	}
	msg.Content = out
	return msg
}

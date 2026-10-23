package agent

import (
	"context"
	"iter"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// inlineScriptProvider 只回放预置事件，用来驱动 inlineThinkingProvider。
type inlineScriptProvider struct {
	events []llm.StreamEvent
	msg    llm.Message
}

func (p inlineScriptProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.msg, "end_turn", llm.Usage{}, nil
}

func (p inlineScriptProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		for _, ev := range p.events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// inlinePiece 把连续同类的片段合并成一条，方便断言「思考整段 / 正文整段」。
type inlinePiece struct {
	kind string
	text string
}

func drainInlineStream(t *testing.T, prov llm.Provider) []inlinePiece {
	t.Helper()
	var out []inlinePiece
	for ev, err := range prov.Stream(context.Background(), llm.CompletionRequest{}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		var kind string
		switch ev.Type {
		case llm.SETextDelta:
			kind = "text"
		case llm.SEThinkingDelta:
			kind = "thinking"
		default:
			continue
		}
		if n := len(out); n > 0 && out[n-1].kind == kind {
			out[n-1].text += ev.Text
			continue
		}
		out = append(out, inlinePiece{kind: kind, text: ev.Text})
	}
	return out
}

func TestInlineThinkingSplitsContentTags(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   []inlinePiece
	}{
		{
			name:   "整段内联思考",
			chunks: []string{"<think>先看目录", "，再读 README", "</think>结论如下"},
			want:   []inlinePiece{{"thinking", "先看目录，再读 README"}, {"text", "结论如下"}},
		},
		{
			name:   "标签被 SSE 切在两个 chunk 之间",
			chunks: []string{"<thi", "nking>推理", "</think", "ing>正文"},
			want:   []inlinePiece{{"thinking", "推理"}, {"text", "正文"}},
		},
		{
			name:   "没有标签时原样透传",
			chunks: []string{"普通", "正文"},
			want:   []inlinePiece{{"text", "普通正文"}},
		},
		{
			name:   "未闭合标签按思考收尾",
			chunks: []string{"<think>推理没写完"},
			want:   []inlinePiece{{"thinking", "推理没写完"}},
		},
		{
			name:   "正文里出现的标签不识别",
			chunks: []string{"举例说 <think> 这种标签", "不会折叠"},
			want:   []inlinePiece{{"text", "举例说 <think> 这种标签不会折叠"}},
		},
		{
			name:   "正文开始后的第二个标签不识别",
			chunks: []string{"<think>a</think>b<think>c"},
			want:   []inlinePiece{{"thinking", "a"}, {"text", "b<think>c"}},
		},
		{
			name:   "前导空白不影响识别",
			chunks: []string{"\n\n<thinking>想想</thinking>答案"},
			want:   []inlinePiece{{"thinking", "想想"}, {"text", "答案"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			events := []llm.StreamEvent{{Type: llm.SEMessageStart}}
			for _, chunk := range c.chunks {
				events = append(events, llm.StreamEvent{Type: llm.SETextDelta, Text: chunk})
			}
			events = append(events, llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn"})
			got := drainInlineStream(t, splitInlineThinking(inlineScriptProvider{events: events}))
			if len(got) != len(c.want) {
				t.Fatalf("pieces = %+v, want %+v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("piece %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// 原生 thinking 事件（reasoning_content / Anthropic thinking 块）必须原样保留，
// 顺序也不变：拆分层只处理 content，不重排已有事件。
func TestInlineThinkingKeepsNativeThinking(t *testing.T) {
	prov := splitInlineThinking(inlineScriptProvider{events: []llm.StreamEvent{
		{Type: llm.SEMessageStart},
		{Type: llm.SEThinkingDelta, Text: "原生推理"},
		{Type: llm.SETextDelta, Text: "正文"},
		{Type: llm.SEMessageDelta, StopReason: "end_turn"},
	}})
	got := drainInlineStream(t, prov)
	want := []inlinePiece{{"thinking", "原生推理"}, {"text", "正文"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("pieces = %+v, want %+v", got, want)
	}
}

func TestInlineThinkingNonStreaming(t *testing.T) {
	cases := []struct {
		name  string
		in    []llm.ContentBlock
		kinds []string
		texts []string
	}{
		{
			name:  "正文里整段内联思考被拆开",
			in:    []llm.ContentBlock{{Type: llm.BlockText, Text: "<think>推理</think>正文"}},
			kinds: []string{"thinking", "text"},
			texts: []string{"推理", "正文"},
		},
		{
			name: "显式 thinking 块与内联思考合并",
			in: []llm.ContentBlock{
				{Type: llm.BlockThinking, Thinking: "原生"},
				{Type: llm.BlockText, Text: "<think>内联</think>正文"},
			},
			kinds: []string{"thinking", "text"},
			texts: []string{"原生内联", "正文"},
		},
		{
			name:  "没有标签时原样返回",
			in:    []llm.ContentBlock{{Type: llm.BlockText, Text: "正文 <think> 只是举例"}},
			kinds: []string{"text"},
			texts: []string{"正文 <think> 只是举例"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitInlineThinkingMessage(llm.Message{Role: llm.RoleAssistant, Content: c.in})
			if len(got.Content) != len(c.kinds) {
				t.Fatalf("blocks = %+v, want kinds %v", got.Content, c.kinds)
			}
			for i := range c.kinds {
				block := got.Content[i]
				kind := ""
				switch block.Type {
				case llm.BlockThinking:
					kind = "thinking"
				case llm.BlockText:
					kind = "text"
				}
				text := block.Text
				if kind == "thinking" {
					text = block.Thinking
				}
				if kind != c.kinds[i] || text != c.texts[i] {
					t.Fatalf("block %d = %s/%q, want %s/%q", i, kind, text, c.kinds[i], c.texts[i])
				}
			}
		})
	}
}

// 幂等：重复包装不会叠出两层拆分器（否则同一个标签会被拆两次）。
func TestSplitInlineThinkingIsIdempotent(t *testing.T) {
	inner := inlineScriptProvider{}
	once := splitInlineThinking(inner)
	if twice := splitInlineThinking(once); twice != once {
		t.Fatal("re-wrapping must return the same provider")
	}
	if splitInlineThinking(nil) != nil {
		t.Fatal("nil provider must stay nil")
	}
}

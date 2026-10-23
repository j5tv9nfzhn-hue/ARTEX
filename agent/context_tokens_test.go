package agent

import (
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// 容量环的分子必须是"最近一轮请求的输入规模"，不是本次 run 的累计值，
// 也不能把已经含在 InputTokens 里的 cache 再算一遍。三种错法都会让占用率虚高，
// 表现就是"环早就 80% 了，压缩却不触发"（压缩器按真实值判断）。
func TestContextTokensUsed(t *testing.T) {
	cases := []struct {
		name string
		prev llm.Usage
		cur  llm.Usage
		want int
	}{
		{
			name: "首轮：prev 为 0，整轮都是新上下文",
			cur:  llm.Usage{InputTokens: 42000, OutputTokens: 800, CacheReadTokens: 38000, CacheWriteTokens: 2000},
			want: 42800,
		},
		{
			name: "第二轮：累计值相减，cache 不再叠加",
			prev: llm.Usage{InputTokens: 42000, OutputTokens: 800},
			cur:  llm.Usage{InputTokens: 90000, OutputTokens: 2100, CacheReadTokens: 86000, CacheWriteTokens: 2000},
			want: 49300,
		},
		{
			name: "高缓存命中：InputTokens 已含 cache，不能加出两倍",
			cur:  llm.Usage{InputTokens: 160000, OutputTokens: 0, CacheReadTokens: 150000, CacheWriteTokens: 0},
			want: 160000,
		},
		{
			name: "网关不按累计上报：相减为负时退回当前绝对量",
			prev: llm.Usage{InputTokens: 1000, OutputTokens: 100},
			cur:  llm.Usage{InputTokens: 200, OutputTokens: 50},
			want: 250,
		},
		{
			name: "空用量：0 而不是负数",
			want: 0,
		},
	}
	for _, c := range cases {
		if got := contextTokensUsed(c.prev, c.cur); got != c.want {
			t.Errorf("%s：got %d，want %d", c.name, got, c.want)
		}
	}
}

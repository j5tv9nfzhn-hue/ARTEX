package agent

// 无 LLM 预剪（第三阶段，对应 OpenCode 的 compaction.prune / Hermes 的第一阶段）。
//
// 为什么在「摘要」之前还要多一道剪枝：
//   - 摘要要花一次模型调用（贵、慢，且有失败率）；而**旧工具输出**是历史里最没有
//     保留价值的部分——它们往往几十 KB 的重复日志，结论早就被 consolidated 进
//     digest / fact 里了。
//   - OpenCode 的做法是：保护最近 40,000 token 的工具输出，更旧的清掉，且「能回收
//     ≥20,000 才值得动手」（issue #14825：希望两次压缩触发之前都先 prune）。
//   - Hermes 的第一阶段同样是「确定性规则先剪旧工具结果」，剪完才决定要不要叫模型。
//
// 三条不变量：
//  1. 只剪 tool_result 正文，**不动**消息结构、不动 tool_use_id、不动 user/assistant
//     文本——配对关系一破，供应商直接 400；
//  2. 最近 pruneProtectTailTokens（并至少保留 pruneMinTurns 轮）原文不动；
//  3. 剪过的块留标记，永不重复剪（重复剪会把首尾也吃掉）。
//
// 为什么要「可回收 ≥20,000 字符」这个门槛：剪枝会破坏前缀缓存（prompt caching），
// 剪掉几百字符换来一次全量重算，是净亏。只有剪得多才值得。

import (
	"fmt"
	"strings"

	"github.com/Autumn-27/norma/llm"
)

const (
	// pruneReclaimThresholdChars 是可回收字符数门槛：不到这个量不剪。
	pruneReclaimThresholdChars = 20000
	// pruneProtectTailTokens 是尾部受保护原文的 token 预算（OpenCode 同值）。
	pruneProtectTailTokens = 40000
	// pruneMinTurns 是无论如何都要保护的最近轮数。
	pruneMinTurns = 2
	// pruneMinBodyRunes 是单条工具输出「值得剪」的最小长度。
	pruneMinBodyRunes = 2000
	// pruneKeepHeadRunes / pruneKeepTailRunes 是剪枝后保留的首尾长度：命令回显在
	// 开头、最终状态在结尾，中间才是可丢的重复日志。
	pruneKeepHeadRunes = 400
	pruneKeepTailRunes = 150
)

// pruneMarker 标记「这条输出被剪过」。再次遇到时直接跳过。
const pruneMarker = "【已剪枝】"

// pruneToolOutputs 返回剪枝后的消息副本与被剪块数。
//
// window 用来决定尾部保护预算（窗口的 25%，夹 8K–40K）；trigger 是压缩触发点，
// 只在「已经很接近触发」时才有意义，这里仅用于日志对账。
func pruneToolOutputs(msgs []llm.Message, window, trigger int) ([]llm.Message, int) {
	if len(msgs) < 4 {
		return msgs, 0
	}
	protect := pruneProtectTailTokens
	if window > 0 {
		if q := window / 4; q < protect {
			protect = q
		}
	}
	if protect < 8000 {
		protect = 8000
	}

	// 从后往前走：累计 token 一旦超过保护预算，之前的部分才是候选。
	// 同时统计「从后数第几轮」，保证最近 pruneMinTurns 轮不动。
	acc := 0
	cut := 0 // 下标 < cut 的部分是候选区
	turns := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			turns++
		}
		if acc >= protect || turns > pruneMinTurns {
			cut = i + 1
			break
		}
		acc += estimateTextTokens(messageTextForEstimtate(msgs[i]))
	}
	if turns <= pruneMinTurns {
		return msgs, 0
	}

	// 先估算可回收量，不到门槛就一个字都不改。
	reclaim := 0
	for i := 0; i < cut; i++ {
		for _, b := range msgs[i].Content {
			if b.Type != llm.BlockToolResult {
				continue
			}
			for _, sub := range b.Content {
				if sub.Type != llm.BlockText || runeLen(sub.Text) < pruneMinBodyRunes {
					continue
				}
				if strings.Contains(sub.Text, pruneMarker) {
					continue
				}
				reclaim += len(sub.Text) - pruneKeepHeadRunes - pruneKeepTailRunes
			}
		}
	}
	if reclaim < pruneReclaimThresholdChars {
		return msgs, 0
	}

	out := make([]llm.Message, len(msgs))
	copy(out, msgs)
	pruned := 0
	for i := 0; i < cut; i++ {
		var content []llm.ContentBlock
		changed := false
		for bi, b := range out[i].Content {
			if b.Type != llm.BlockToolResult || len(b.Content) == 0 {
				continue
			}
			var subs []llm.ContentBlock
			for si, sub := range b.Content {
				if sub.Type != llm.BlockText || runeLen(sub.Text) < pruneMinBodyRunes || strings.Contains(sub.Text, pruneMarker) {
					continue
				}
				if subs == nil {
					subs = make([]llm.ContentBlock, len(b.Content))
					copy(subs, b.Content)
				}
				subs[si].Text = clipRunes(sub.Text, pruneKeepHeadRunes, pruneKeepTailRunes) +
					fmt.Sprintf("\n\n%s 该工具输出（原始 %d 字符）已在较早轮次被剪枝：结论已在上文的摘要/事实里；需要原文就重新执行这条命令（或读落盘输出）。", pruneMarker, runeLen(sub.Text))
				pruned++
			}
			if subs != nil {
				if content == nil {
					content = make([]llm.ContentBlock, len(out[i].Content))
					copy(content, out[i].Content)
				}
				content[bi].Content = subs
				changed = true
			}
		}
		if changed {
			out[i].Content = content
		}
	}
	return out, pruned
}

// messageTextForEstimtate 把一条消息的可计费文本拼起来（正文 + 工具结果正文 +
// 工具入参）。名字保留历史拼写，避免无关改动。
func messageTextForEstimtate(m llm.Message) string {
	var b strings.Builder
	for _, blk := range m.Content {
		switch blk.Type {
		case llm.BlockText:
			b.WriteString(blk.Text)
		case llm.BlockToolResult:
			for _, sub := range blk.Content {
				if sub.Type == llm.BlockText {
					b.WriteString(sub.Text)
				}
			}
		case llm.BlockToolUse:
			b.Write(blk.Input)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

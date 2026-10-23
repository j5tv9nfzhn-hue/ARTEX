package agent

// 工具输出截断（第三阶段：head 70 / tail 30）。
//
// 背景：SDK 的 tool.Capture 超限时是**头尾各一半**（或落盘后只留头部 + 指针）。
// 两种都不理想：
//   - 一半一半：结论/命令回显通常在开头，最终状态（退出码、最后一屏报错、跑完的
//     统计行）在结尾；中间那段恰恰是信息密度最低的重复日志。真实事故见 OpenCode
//     PR #104——每条工具结果先截 2000 字符再喂摘要，后半段与最终状态全被藏掉。
//   - 只留头部 + 落盘指针：模型往往不会去读那个文件，于是「最后跑挂了没」它看不见。
//
// 这里改成 head 70% / tail 30%，并且**超限时仍然落盘**（全量可回读）。落盘失败才
// 退化成纯截断。
//
// 语义边界：
//   - 只在超过 tc.MaxOutputChars 时动手；上限本身由 ToolOutputChars(window) 推导；
//   - 已经带 <persisted-output> 或 SDK 截断标记的原样返回（避免二次截断把指针切掉）；
//   - 按 rune 切，不切出半个 UTF-8 字符。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
)

// outputTruncateHeadShare 是保留头部占上限的比例（其余给尾部）。
const outputTruncateHeadShare = 70

var outputSpillSeq atomic.Int64

// captureToolOutput 把一条工具输出裁到 tc.MaxOutputChars 以内，保留 head 70% +
// tail 30%，全量落盘到 tc.OutputDir。
func captureToolOutput(tc *tool.ToolContext, s string) string {
	max := 0
	dir := ""
	if tc != nil {
		max = tc.MaxOutputChars
		dir = tc.OutputDir
	}
	if max <= 0 {
		max = defaultMaxToolOutputChars
	}
	if len(s) <= max {
		return s
	}
	if alreadyClipped(s) {
		return s
	}
	ref := ""
	if dir != "" {
		if path, err := spillToolOutput(dir, s); err == nil {
			ref = path
			if tc != nil && tc.WorkingDir != "" {
				if rel, e := filepath.Rel(tc.WorkingDir, path); e == nil && !strings.HasPrefix(rel, "..") {
					ref = rel
				}
			}
		}
	}
	head := max * outputTruncateHeadShare / 100
	tail := max - head
	body := clipRunes(s, head, tail)
	var note string
	if ref != "" {
		note = fmt.Sprintf("\n\n... <persisted-output>[输出 %d 字节过长，已保留开头 %d / 结尾 %d 字符。完整输出已落盘：%s（可用 Read/Grep 按需读）]</persisted-output>",
			len(s), head, tail, ref)
	} else {
		note = fmt.Sprintf("\n\n... <persisted-output>[输出 %d 字节过长，已保留开头 %d / 结尾 %d 字符；中间 %d 字节省略]</persisted-output>",
			len(s), head, tail, len(s)-head-tail)
	}
	return body + note
}

// clipRunes 保留前 head 个 rune 与后 tail 个 rune，中间插一句省略说明。
func clipRunes(s string, head, tail int) string {
	if head <= 0 && tail <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= head+tail {
		return s
	}
	end := len(r) - tail
	if tail <= 0 {
		end = len(r)
	}
	return string(r[:head]) + fmt.Sprintf("\n\n……（中略 %d 字符）……\n\n", end-head) + string(r[end:])
}

// alreadyClipped 判断这条输出是否已经被截断/落盘过（自己或 SDK 做的）。
func alreadyClipped(s string) bool {
	return strings.Contains(s, "<persisted-output>") || strings.Contains(s, "characters truncated]")
}

// spillToolOutput 把全量输出写到 dir/output-<ts>-<seq>.txt，返回绝对路径。
// 命名与 SDK 的 tool.Capture 同一目录、不同前缀，互不覆盖。
func spillToolOutput(dir, s string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("output-%d-%d.txt", time.Now().Unix(), outputSpillSeq.Add(1))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// clippedResult 对结果里的长文本做一次统一裁剪（只动成功结果；错误信息本来就短，
// 且被截断的错误会误导模型）。
func clippedResult(tc *tool.ToolContext, res tool.Result) tool.Result {
	if res.IsError || len(res.Content) == 0 {
		return res
	}
	for i, b := range res.Content {
		if b.Type != llm.BlockText || b.Text == "" {
			continue
		}
		res.Content[i].Text = captureToolOutput(tc, b.Text)
	}
	return res
}

// runeLen 是 utf8 字符数（截断预算统一按字符算，中文一字符一单位）。
func runeLen(s string) int { return utf8.RuneCountInString(s) }

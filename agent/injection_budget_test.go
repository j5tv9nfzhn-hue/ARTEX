package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

// 摘要预算是「输入量 × 0.30」，夹在 [512, min(5%窗口, 4096)]，且永不超过输入的 0.6。
// 钉这条是为了防两个回退：① 又变回写死的 800；② 小 block 上摘要比原文还长。
func TestDigestBudgetTokens(t *testing.T) {
	cases := []struct {
		input, window int
		min, max      int
	}{
		{300, 128000, 64, 180},       // 极小 block：预算被 0.6×input 压到 180
		{2000, 128000, 600, 600},     // 0.3×2000=600，未触上限
		{8000, 128000, 2400, 2400},   // 0.3×8000=2400
		{50000, 128000, 4096, 4096},  // 触 4096 上限（5%×128K=6400 > 4096）
		{50000, 32000, 1600, 1600},   // 小窗口：5%×32K=1600 先到
		{100000, 200000, 4096, 4096}, // 大窗口仍是 4096 硬顶
		{0, 128000, 512, 512},        // 无输入 → 下限
		{2000, 0, 600, 600},          // 窗口未知 → 默认 200K
	}
	for _, c := range cases {
		got := DigestBudgetTokens(c.input, c.window)
		if got < c.min || got > c.max {
			t.Errorf("input=%d window=%d：预算 %d，期望 [%d,%d]", c.input, c.window, got, c.min, c.max)
		}
	}
	// 永不大于输入：这条比下限优先。
	for _, in := range []int{100, 200, 400, 800} {
		if got := DigestBudgetTokens(in, 128000); got > int(float64(in)*digestNeverExpand)+1 {
			t.Errorf("input=%d：摘要预算 %d 超过输入的 60%%", in, got)
		}
	}
	// 与旧值对照：原来写死 800，2K 的 block 现在给 600（更少），8K 的给 2400（更多）。
	if DigestBudgetTokens(8000, 128000) == 800 {
		t.Error("8K 输入的预算仍是 800——固定值没有被替换")
	}
}

// 注入侧预算随窗口单调、且都有硬上下限。
func TestInjectionBudgets(t *testing.T) {
	if got := DigestInjectRunes(128000); got != 2000 {
		t.Errorf("128K 窗口单条 digest 注入 = %d，期望 2000（窗口/50 命中上限）", got)
	}
	if got := DigestInjectRunes(32000); got != 800 {
		t.Errorf("32K 窗口单条 digest 注入 = %d，期望 800（下限）", got)
	}
	if got := ToolOutputChars(128000); got != 16000 {
		t.Errorf("128K 窗口工具输出上限 = %d，期望 16000（窗口/8）", got)
	}
	if got := ToolOutputChars(1000000); got != 24000 {
		t.Errorf("大窗口工具输出上限 = %d，期望 24000（硬顶）", got)
	}
	if got := ToolOutputChars(8000); got != 4000 {
		t.Errorf("小窗口工具输出上限 = %d，期望 4000（地板）", got)
	}
	if got := OverviewBudgetBytes(128000); got != 51200 {
		t.Errorf("128K 窗口态势注入 = %d，期望 51200（0.4×窗口）", got)
	}
	if got := OverviewBudgetBytes(32000); got != overviewFloorBytes {
		t.Errorf("32K 窗口态势注入 = %d，期望 %d", got, overviewFloorBytes)
	}
	// 单调不减 + 夹取。
	prev := 0
	for _, w := range []int{32000, 64000, 128000, 200000, 1000000} {
		v := InjectionBudgetRunes(w)
		if v < prev {
			t.Errorf("窗口 %d：注入池 %d 小于更小窗口的 %d", w, v, prev)
		}
		prev = v
	}
	if InjectionBudgetRunes(1000000) != 96000 {
		t.Errorf("1M 窗口注入池应被夹到 96000")
	}
}

// digest 注入的两级预算：单条上限之外还有总量上限，超了按比例收紧（从原文重算，
// 不是对已截断的再截一次）。
func TestTruncateDigestBodiesRespectsTotalBudget(t *testing.T) {
	mk := func(n int) []map[string]any {
		list := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			list = append(list, map[string]any{"id": i + 1, "body": strings.Repeat("结论", 3000)}) // 6000 字符
		}
		return list
	}
	data := map[string]any{"cold_digests": mk(8)}
	perItem, total := 2000, 6000
	truncateDigestBodies(data, perItem, total)

	sum := 0
	for _, d := range data["cold_digests"].([]map[string]any) {
		body := d["body"].(string)
		sum += runeLen(body)
		if runeLen(body) > perItem {
			t.Fatalf("单条超过上限：%d > %d", runeLen(body), perItem)
		}
		if !strings.Contains(body, "expand_digest") {
			t.Fatal("被截断的正文缺少可回溯提示")
		}
	}
	if sum > total {
		t.Fatalf("总量 %d 超过预算 %d", sum, total)
	}
	if sum < total/2 {
		t.Fatalf("总量 %d 被收得过狠（预算 %d）", sum, total)
	}

	// 预算充足时不该动。
	data2 := map[string]any{"cold_digests": []map[string]any{{"id": 1, "body": "短结论"}}}
	truncateDigestBodies(data2, 2000, 6000)
	if got := data2["cold_digests"].([]map[string]any)[0]["body"]; got != "短结论" {
		t.Fatalf("短正文被改写了：%v", got)
	}
}

// 预剪：只剪「尾部保护窗之外」的旧工具输出，结构（tool_use_id / 块数）必须原样。
func TestPruneToolOutputs(t *testing.T) {
	big := strings.Repeat("x", 20000)
	var msgs []llm.Message
	for i := 0; i < 6; i++ {
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "call-" + string(rune('a'+i)), Name: "Bash", Input: []byte(`{"command":"echo hi"}`)},
		}})
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{
			llm.ToolResultText("call-"+string(rune('a'+i)), big, false),
		}})
	}
	out, pruned := pruneToolOutputs(msgs, 128000, 96000)
	if pruned == 0 {
		t.Fatal("20K×6 的旧工具输出应当触发预剪")
	}
	if len(out) != len(msgs) {
		t.Fatalf("消息条数变了：%d → %d", len(msgs), len(out))
	}
	// 最后两条（最近一轮）必须原样。
	for _, i := range []int{len(out) - 2, len(out) - 1} {
		if got := out[i].Content[0]; got.Type == llm.BlockToolResult && got.Content[0].Text != big {
			t.Fatalf("最近的输出被剪了：msg %d", i)
		}
	}
	// 剪过的块：留下标记 + 首尾，且 id 不变。
	first := out[1].Content[0]
	if first.ToolUseID != "call-a" {
		t.Fatalf("tool_use_id 被改坏：%q", first.ToolUseID)
	}
	body := first.Content[0].Text
	if !strings.Contains(body, pruneMarker) {
		t.Fatal("剪枝后缺少标记")
	}
	if runeLen(body) > 2000 {
		t.Fatalf("剪枝后仍然过长：%d", runeLen(body))
	}
	// 幂等：再跑一次不会继续剪（标记已在）。
	if _, again := pruneToolOutputs(out, 128000, 96000); again != 0 {
		t.Fatalf("重复剪枝：%d", again)
	}

	// 小输出不剪（省一次前缀缓存重算）。
	var small []llm.Message
	for i := 0; i < 6; i++ {
		small = append(small, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{
			llm.ToolResultText("c", strings.Repeat("y", 300), false),
		}}, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("ok")}})
	}
	if _, n := pruneToolOutputs(small, 128000, 96000); n != 0 {
		t.Fatalf("小输出被剪了：%d", n)
	}
}

// 工具输出裁剪：head 70 / tail 30，超限落盘，且不重复裁。
func TestCaptureToolOutputHeadTail(t *testing.T) {
	tc := &actool.ToolContext{MaxOutputChars: 1000}
	head := strings.Repeat("H", 5000)
	tail := strings.Repeat("T", 5000)
	out := captureToolOutput(tc, head+tail)

	if !strings.Contains(out, "<persisted-output>") {
		t.Fatal("超限输出缺少落盘标记")
	}
	if !strings.HasPrefix(out, "HHH") {
		t.Error("开头被丢了")
	}
	if !strings.Contains(out, "TTT") {
		t.Error("结尾被丢了——尾部的最终状态正是最需要保留的部分")
	}
	// 头:尾 ≈ 7:3（允许中间说明行的少量偏差）。
	nh := strings.Count(out, "H")
	nt := strings.Count(out, "T")
	if nh < nt {
		t.Errorf("头 %d 少于尾 %d：没有按 70/30 切", nh, nt)
	}
	if ratio := float64(nh) / float64(nh+nt); ratio < 0.65 || ratio > 0.75 {
		t.Errorf("头占比 %.2f，期望约 0.70", ratio)
	}

	// 已经裁过的（带 SDK 标记）原样返回。
	pre := "abc characters truncated] xyz"
	if got := captureToolOutput(tc, pre+strings.Repeat("z", 5000)); got != pre+strings.Repeat("z", 5000) {
		t.Error("对已裁剪的输出做了二次裁剪")
	}
	// 未超限原样返回。
	if got := captureToolOutput(tc, "short"); got != "short" {
		t.Error("短输出被改写")
	}
}

// 触发点绝对上限可被环境变量关闭（真·大窗口）。
func TestCompactionTriggerCapEnv(t *testing.T) {
	t.Setenv("ARTEX_COMPACT_CAP_TOKENS", "0")
	if got := CompactionTriggerTokens(1000000); got != 800000 {
		t.Errorf("关闭上限后 1M 窗口触发点 = %d，期望 800000", got)
	}
	t.Setenv("ARTEX_COMPACT_CAP_TOKENS", "60000")
	if got := CompactionTriggerTokens(128000); got != 60000 {
		t.Errorf("上限 60K 时 128K 窗口触发点 = %d，期望 60000", got)
	}
}

// 态势注入的字节预算必须随窗口变化：32K 窗口给 24KB 地板，200K 窗口给 64KB 上限。
// 这条把 contextWindowFrom(ctx) → OverviewBudgetBytes → marshalOverviewCapped 串起来，
// 防止「函数改了但调用方没传窗口」这种半截修复。
func TestOverviewBudgetScalesWithWindow(t *testing.T) {
	data := map[string]any{}
	list := make([]map[string]any, 0, 400)
	for i := 0; i < 400; i++ {
		list = append(list, map[string]any{"id": i, "summary": strings.Repeat("态势描述", 20)})
	}
	data["open_intents"] = list

	small := renderGraphOverview(WithContextWindow(context.Background(), 32000), data)
	big := renderGraphOverview(WithContextWindow(context.Background(), 200000), data)
	none := renderGraphOverview(context.Background(), data)

	if len(small) > overviewFloorBytes+512 {
		t.Errorf("32K 窗口注入 %d 字节，超过地板 %d", len(small), overviewFloorBytes)
	}
	if len(big) > overviewCeilBytes+512 {
		t.Errorf("200K 窗口注入 %d 字节，超过上限 %d", len(big), overviewCeilBytes)
	}
	if len(big) <= len(small) {
		t.Errorf("大窗口注入 %d 字节没有比小窗口 %d 字节更多——窗口没传到预算里", len(big), len(small))
	}
	if len(none) > 16<<10+512 {
		t.Errorf("窗口未知时应退回旧的 16KB 上限，实际 %d", len(none))
	}
}

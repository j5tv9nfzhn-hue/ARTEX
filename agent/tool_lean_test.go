package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestToolSchemaSlimmingMeetsBudget 是 P3 的验收断言：三套内置工具面在装配后
// 「交给模型的字节数」都必须下降 40% 以上（含 SDK 通用工具，因为它们在真实请求里
// 同样占体积）。数值直接打到失败信息里，回归时能看出差多少。
func TestToolSchemaSlimmingMeetsBudget(t *testing.T) {
	if !leanEnabled() {
		t.Skip("瘦身被环境变量关闭")
	}
	for _, s := range ToolSchemaSizes() {
		if s.Before == 0 {
			t.Fatalf("%s: 没有量到任何字节", s.Agent)
		}
		if got := s.SavedPercent(); got < 40 {
			t.Errorf("%s 工具面只省了 %.1f%%（%d → %d 字节），低于验收线 40%%",
				s.Agent, got, s.Before, s.After)
		}
	}
}

func TestLeanTextCutsOnBoundaryAndMarksTruncation(t *testing.T) {
	short := "读取目标页面"
	if got := leanText(short, leanTopDescRunes); got != short {
		t.Fatalf("短说明不应被改动: %q", got)
	}
	long := "发起一次 HTTP 请求。支持代理、自定义方法与任意头；响应体超限时截断并在结果里标记 body_truncated，完整内容落盘可用 file 参数重读。"
	got := leanText(long, 24)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("被截断的说明必须带省略号: %q", got)
	}
	if n := len([]rune(got)); n > 25 {
		t.Fatalf("截断后仍有 %d 字: %q", n, got)
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(got, "…")) {
		t.Fatalf("截断必须取自原文前缀: %q", got)
	}
	// 边界优先：第一句正好在 max 以内时，应停在句号后而不是硬截。
	sentence := "只读工具。它会返回完整结构，不要用它改任何状态。"
	if got := leanText(sentence, 12); got != "只读工具。…" {
		t.Fatalf("应停在句子边界: %q", got)
	}
	if got := leanText("", 10); got != "" {
		t.Fatalf("空串应保持空: %q", got)
	}
}

// TestLeanSchemaKeepsStructure 钉住「只压说明、不动结构」：类型/枚举/必填/默认值
// 必须逐字节保留，否则模型会按错的结构填参数。
func TestLeanSchemaKeepsStructure(t *testing.T) {
	in := map[string]any{
		"type":     "object",
		"required": []any{"url", "method"},
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": strings.Repeat("很长的入参说明", 20)},
			"method": map[string]any{
				"type":        "string",
				"enum":        []any{"GET", "POST"},
				"default":     "GET",
				"description": "请求方法" + strings.Repeat("，可选", 30),
			},
			"steps": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": strings.Repeat("嵌套字段说明", 10)},
					},
				},
			},
		},
	}
	out := leanSchemaMap(in)
	props := out["properties"].(map[string]any)
	if _, ok := out["required"]; !ok {
		t.Fatal("required 丢失")
	}
	method := props["method"].(map[string]any)
	if method["default"] != "GET" {
		t.Fatalf("default 被改动: %v", method["default"])
	}
	if enum, _ := method["enum"].([]any); len(enum) != 2 {
		t.Fatalf("enum 被改动: %v", method["enum"])
	}
	if d := method["description"].(string); len([]rune(d)) > leanParamDescRunes+1 {
		t.Fatalf("顶层参数说明未被压短: %q", d)
	}
	steps := props["steps"].(map[string]any)
	nested := steps["items"].(map[string]any)["properties"].(map[string]any)["path"].(map[string]any)
	if d := nested["description"].(string); len([]rune(d)) > leanNestedDescRunes+1 {
		t.Fatalf("嵌套参数说明未被压短: %q", d)
	}
	// 原始 map 不能被就地改动（瘦身必须是拷贝）。
	if d := in["properties"].(map[string]any)["url"].(map[string]any)["description"].(string); len([]rune(d)) < 50 {
		t.Fatal("原始 schema 被就地修改了")
	}
}

// TestLeanToolListRegistersFullDocs 保证「瘦身不丢信息」：完整说明进存档，
// get_tool_help 能原样取回。
func TestLeanToolListRegistersFullDocs(t *testing.T) {
	ts := NewToolSet(nil, "")
	base := ts.MainAgentTools()
	leaned := leanToolList(base)

	if got, want := len(leaned), len(base)+1; got != want {
		t.Fatalf("工具数 = %d, 期望 %d（原集合 + get_tool_help）", got, want)
	}
	last := leaned[len(leaned)-1]
	if last.Name() != ToolHelpToolName {
		t.Fatalf("最后一个工具应是 %s，实际 %s", ToolHelpToolName, last.Name())
	}
	// 顶层说明被压短，但存档里仍是全文。
	for _, tool := range base {
		if len([]rune(tool.Description())) <= leanTopDescRunes {
			continue
		}
		var short string
		for _, lt := range leaned {
			if lt.Name() == tool.Name() {
				short = lt.Description()
			}
		}
		if short == "" {
			t.Fatalf("%s 丢了", tool.Name())
		}
		if len([]rune(short)) > leanTopDescRunes+1 {
			t.Fatalf("%s 的说明没被压短: %q", tool.Name(), short)
		}
		res, err := last.Call(context.Background(), json.RawMessage(`{"name":"`+tool.Name()+`"}`), nil)
		if err != nil {
			t.Fatalf("调用 %s 失败: %v", ToolHelpToolName, err)
		}
		if !strings.Contains(res.Flatten(), tool.Description()) {
			t.Fatalf("%s 的完整说明没有出现在 %s 的输出里", tool.Name(), ToolHelpToolName)
		}
		if !strings.Contains(res.Flatten(), tool.Name()) {
			t.Fatal("输出里没有工具名")
		}
	}
}

// TestToolHelpUnknownNameListsOptions 让「查错名字」可自愈：返回错误并列出现有名。
func TestToolHelpUnknownNameListsOptions(t *testing.T) {
	ts := NewToolSet(nil, "")
	leanToolList(ts.PlannerTools())
	res, err := NewToolHelpTool().Call(context.Background(), json.RawMessage(`{"name":"nope_not_a_tool"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("未知工具名应返回错误结果")
	}
	text := res.Flatten()
	if !strings.Contains(text, "nope_not_a_tool") || !strings.Contains(text, "add_intent") {
		t.Fatalf("错误信息应同时给出请求名与可选工具: %q", text)
	}
}

// TestAugmentToolsSlimsAndAppendsHelp 走真实装配链路：AugmentTools 之后模型看到
// 的是瘦身说明 + 常驻 get_tool_help；关掉开关后逐字节回到原样。
func TestAugmentToolsSlimsAndAppendsHelp(t *testing.T) {
	ts := NewToolSet(nil, "")
	base := ts.MainAgentTools()

	out, _, cleanup := AugmentTools(context.Background(), "mainagent", base)
	defer cleanup()
	help := ""
	for _, tool := range out {
		if tool.Name() == ToolHelpToolName {
			help = tool.Description()
		}
	}
	if help == "" {
		t.Fatal("装配后缺少常驻的 get_tool_help")
	}

	t.Setenv(leanDisableEnv, "0")
	off, _, cleanup2 := AugmentTools(context.Background(), "mainagent", base)
	defer cleanup2()
	for _, tool := range off {
		if tool.Name() == ToolHelpToolName {
			t.Fatal("关闭瘦身后不应注入 get_tool_help")
		}
	}
	if leanEnabled() {
		t.Fatal("环境变量应关闭瘦身")
	}
}

// TestPhaseToolFaceIsNarrowerThanFull 把 P3 的两条并起来看效果：先按阶段裁面
// （WorkerTools 的 recon/verify/exploit），再瘦身。任何阶段的最终体积都应显著小于
// 「不声明阶段」的原始全量——这正是 planner 提示里「写 phase 更省 token」那句话的
// 可验证版本。
func TestPhaseToolFaceIsNarrowerThanFull(t *testing.T) {
	ts := NewToolSet(nil, "")
	ts.SetPhase("")
	full := schemaPayloadBytes(ts.WorkerTools())
	if full <= 0 {
		t.Fatal("全量工具面为空")
	}
	for _, phase := range []string{"recon", "verify", "exploit"} {
		ts.SetPhase(phase)
		subset := ts.WorkerTools()
		leaned := leanToolList(subset)
		if got, want := len(leaned), len(subset)+1; got != want {
			t.Fatalf("%s: 瘦身后工具数 = %d, 期望 %d", phase, got, want)
		}
		after := 0
		for _, tool := range leaned {
			after += schemaOneBytes(tool)
		}
		ratio := float64(after) / float64(full)
		if ratio > 0.7 {
			t.Errorf("%s 的工具面是「全量未瘦身」的 %.0f%%，阶段裁剪没起作用", phase, ratio*100)
		}
	}
	ts.SetPhase("") // 还原，避免影响同包其它测试
}

package agent

import (
	"strings"
	"testing"
	"unicode"
)

// A2：wrap-up / settlement 提示词中文化。
// 运行或任务达到步数、时间预算后，settlement 阶段注入这些常量，
// 直接指导用户可见的最终总结。因此必须使用中文、不得残留韩文，
// 同时保留工具标识和“一句纯文本”等指令含义。
// 数据库初始 wrapup_prompt / task_timeout_wrapup_prompt 为空，
// db/db.go 的 builtin Agent INSERT 不填写这些列，空值回退到以下常量。
func TestWrapupPromptsLocalizedToChinese(t *testing.T) {
	all := map[string]string{
		"settleWrapUpPrompt":        settleWrapUpPrompt,
		"plannerWrapUpDefault":      plannerWrapUpDefault,
		"mainAgentWrapUpDefault":    mainAgentWrapUpDefault,
		"genericWrapUpDefault":      genericWrapUpDefault,
		"workerTaskTimeoutDefault":  workerTaskTimeoutDefault,
		"plannerTaskTimeoutDefault": plannerTaskTimeoutDefault,
	}

	hasScript := func(s string, table *unicode.RangeTable) bool {
		for _, r := range s {
			if unicode.Is(table, r) {
				return true
			}
		}
		return false
	}

	for name, p := range all {
		if !hasScript(p, unicode.Han) {
			t.Errorf("%s：未包含汉字，尚未完成中文化", name)
		}
		// 工具名保持 ASCII 协议标识；自然语言应包含汉字，不能残留韩文。
		if hasScript(p, unicode.Hangul) {
			t.Errorf("%s：仍有韩文残留，翻译未完成：%q", name, p)
		}
	}

	// 工具名是标识符，必须原样保留。Worker 的 per-run 与 task-timeout
	// 使用 record_fact 记录结论、report_finding 记录漏洞，最后输出一句纯文本总结。
	mustContain := func(name, p string, subs ...string) {
		for _, s := range subs {
			if !strings.Contains(p, s) {
				t.Errorf("%s：必须保留指令含义 %q", name, s)
			}
		}
	}
	mustContain("settleWrapUpPrompt", settleWrapUpPrompt,
		"insert_assets", "record_fact", "report_finding", "一句", "纯文本")
	mustContain("workerTaskTimeoutDefault", workerTaskTimeoutDefault,
		"insert_assets", "record_fact", "report_finding", "一句", "纯文本")
	mustContain("genericWrapUpDefault", genericWrapUpDefault, "一句", "纯文本")
	mustContain("mainAgentWrapUpDefault", mainAgentWrapUpDefault, "一句", "纯文本")
	// Planner 不输出总结句，只做判断并结束，保留意图、目标和 Todo 工具。
	mustContain("plannerWrapUpDefault", plannerWrapUpDefault, "add_intent", "prove_goal", "TodoWrite")
	mustContain("plannerTaskTimeoutDefault", plannerTaskTimeoutDefault, "prove_goal")
}

// per-run 与 task-timeout 的含义不同：前者仅结束本轮，后者结束整个任务。
// 尤其不能混淆 Planner 的两条收尾路径及其常量映射。
func TestWrapupDefaultsRouting(t *testing.T) {
	if WrapupDefault("worker") != settleWrapUpPrompt {
		t.Error("worker per-run 默认值不是 settleWrapUpPrompt")
	}
	if WrapupDefault("planner") != plannerWrapUpDefault {
		t.Error("planner per-run 默认值不是 plannerWrapUpDefault")
	}
	if WrapupDefault("mainagent") != mainAgentWrapUpDefault {
		t.Error("mainagent per-run 默认值不是 mainAgentWrapUpDefault")
	}
	// 未注册角色（自定义 Agent）回退到 generic。
	if WrapupDefault("unknown-agent") != genericWrapUpDefault {
		t.Error("未知角色未回退到 genericWrapUpDefault")
	}
	// task-timeout 仅为 Worker 和 Planner 提供专用文案，其他角色返回空字符串，调用方使用 per-run。
	if TaskTimeoutWrapupDefault("worker") != workerTaskTimeoutDefault {
		t.Error("worker task-timeout 默认值不是 workerTaskTimeoutDefault")
	}
	if TaskTimeoutWrapupDefault("planner") != plannerTaskTimeoutDefault {
		t.Error("planner task-timeout 默认值不是 plannerTaskTimeoutDefault")
	}
	if TaskTimeoutWrapupDefault("mainagent") != "" {
		t.Error("mainagent 不应有 task-timeout 文案（应为空字符串）")
	}
	// 两条路径文案相同意味着收尾语义区分丢失。
	if workerTaskTimeoutDefault == settleWrapUpPrompt {
		t.Error("worker 的 per-run 和 task-timeout 文案相同")
	}
	if plannerTaskTimeoutDefault == plannerWrapUpDefault {
		t.Error("planner 的 per-run 和 task-timeout 文案相同")
	}
}

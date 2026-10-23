// Package llmprobe 从供应商那里问出「这个模型到底有多大窗口、能吐多少、
// 支持哪些模态、支持哪几档推理强度」，并把这些事实变成可存储、可展示、
// 可供压缩策略使用的能力描述。
//
// 为什么需要一个独立包：能力是模型的属性，不是用户的输入。把它挂在
// llm_profiles.context_window_k 一格输入框上，填错一个数字（线上真实发生过：
// 声明 1000K、实际 128K）就会让压缩永远不触发，剩下的交给供应商静默截断。
//
// 本包的三个来源，按可信度从高到低：
//
//  1. provider —— 供应商自己的模型列表/元数据端点（OpenRouter 的
//     context_length、vLLM 的 max_model_len、Anthropic 的 max_input_tokens、
//     Gemini 的 inputTokenLimit、Ollama /api/show、LM Studio /api/v0/models、
//     llama.cpp /props）。这是唯一「不会错」的来源。
//  2. catalog  —— 内建模型目录（capability catalog），按模型名匹配。离线可用，
//     但会随厂商更新而过期，只做兜底。
//  3. defaults —— 什么都没有时的保守默认（200K / 8K 输出 / 纯文本）。
//
// 参考实现（2026-10 调研）：models.dev 的 limit.context/limit.output、
// modalities、reasoning_options；OpenRouter 的 /api/v1/models；Codex 的
// model catalog；OpenCode 的 models.dev 拉取；Zed/Cline 的模型选择器。
// 四家的共同点是：**窗口优先取服务端上报，其次查本地目录，绝不拿用户手填
// 当真相**。
package llmprobe

import (
	"strings"
	"time"
)

// Source 标注这份能力描述是从哪来的，UI 会把它显示在数值旁边。
const (
	SourceProvider = "provider" // 供应商端点直接上报
	SourceLocal    = "local"    // 本机推理进程（Ollama / LM Studio / llama.cpp）
	SourceCatalog  = "catalog"  // 内建目录按模型名匹配
	SourceDefaults = "defaults" // 保守默认值
	SourceManual   = "manual"   // 用户手工覆盖
)

// Capabilities 是一个模型的可观测能力快照。
//
// 所有字段都是「未知即零值」：ContextTokens=0 表示没探测到，而不是 0 窗口。
// 这样上层可以用 0 决定是否回落到目录/默认值，不会把缺失当成事实。
type Capabilities struct {
	// ContextTokens 是模型的总上下文窗口（输入 + 输出共享），单位 token。
	ContextTokens int `json:"context_tokens,omitempty"`
	// MaxOutputTokens 是单次回复的输出上限。0 = 供应商没报。
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// InputModalities 是能接收的输入模态，沿用 db 包的词汇：
	// text / image / audio / pdf。
	InputModalities []string `json:"input_modalities,omitempty"`
	// OutputModalities 是能产出的模态，一般只有 text，多模态生成模型会有 image。
	OutputModalities []string `json:"output_modalities,omitempty"`
	// Reasoning 表示这是不是思考型模型（能不能开 thinking）。
	Reasoning bool `json:"reasoning"`
	// ReasoningEfforts 是支持的推理强度档位，如 low/medium/high。
	// 空 = 支持思考但没报档位（前端只显示「支持」，不显示档位选择）。
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	// Tools 表示是否支持函数/工具调用。ARTEX 全靠工具干活，这条只是展示用。
	Tools bool `json:"tools"`
	// Source 见上面的常量；Model 是这份描述对应的模型 id。
	Source string `json:"source,omitempty"`
	Model  string `json:"model,omitempty"`
	// ProbedAt 是探测时间（RFC3339），前端据此显示「3 分钟前检测」。
	ProbedAt string `json:"probed_at,omitempty"`
	// Notes 记录探测过程中的降级原因（例如「端点返回 404，已用内建目录」），
	// 让「为什么显示这个数」可追溯，而不是一个来源不明的数字。
	Notes []string `json:"notes,omitempty"`
}

// Known 判断这份快照是否包含任何来自外部的事实（而不是空壳）。
func (c Capabilities) Known() bool {
	return c.ContextTokens > 0 || c.MaxOutputTokens > 0 || len(c.InputModalities) > 0 || c.Reasoning
}

// ContextK 把窗口换算成 K，四舍五入。UI 与 llm_profiles.context_window_k 同一单位。
func (c Capabilities) ContextK() int {
	if c.ContextTokens <= 0 {
		return 0
	}
	return (c.ContextTokens + 500) / 1000
}

// clone 复制一份，避免 Merge 时把调用方持有的切片改掉。
func (c Capabilities) clone() Capabilities {
	out := c
	out.InputModalities = append([]string(nil), c.InputModalities...)
	out.OutputModalities = append([]string(nil), c.OutputModalities...)
	out.ReasoningEfforts = append([]string(nil), c.ReasoningEfforts...)
	out.Notes = append([]string(nil), c.Notes...)
	return out
}

// Merge 用 over 补齐 base 的空字段（over 优先，但不覆盖已有值——除非 over 有值）。
// 语义：越靠前的来源越可信，调用方按 可信度从高到低 依次 Merge。
func Merge(base, over Capabilities) Capabilities {
	out := base.clone()
	if over.ContextTokens > 0 {
		out.ContextTokens = over.ContextTokens
	}
	if over.MaxOutputTokens > 0 {
		out.MaxOutputTokens = over.MaxOutputTokens
	}
	if len(over.InputModalities) > 0 {
		out.InputModalities = append([]string(nil), over.InputModalities...)
	}
	if len(over.OutputModalities) > 0 {
		out.OutputModalities = append([]string(nil), over.OutputModalities...)
	}
	if over.Reasoning {
		out.Reasoning = true
	}
	if len(over.ReasoningEfforts) > 0 {
		out.ReasoningEfforts = append([]string(nil), over.ReasoningEfforts...)
	}
	if over.Tools {
		out.Tools = true
	}
	if over.Source != "" {
		out.Source = over.Source
	}
	if over.Model != "" {
		out.Model = over.Model
	}
	if over.ProbedAt != "" {
		out.ProbedAt = over.ProbedAt
	}
	out.Notes = append(out.Notes, over.Notes...)
	return out
}

// Stamp 打上探测时间与模型名，并去重 Notes。
func (c Capabilities) Stamp(model string) Capabilities {
	out := c.clone()
	out.Model = model
	out.ProbedAt = time.Now().Format(time.RFC3339)
	out.Notes = dedupeStrings(out.Notes)
	return out
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// NormalizeModalities 把各家不同的模态词汇归一成 db 包使用的四个值。
// 未知模态（video、embedding 等）直接丢弃：ARTEX 只会因为这些值去路由附件，
// 保留一个它不认识的词没有意义。
func NormalizeModalities(in []string) []string {
	order := []string{"text", "image", "audio", "pdf"}
	seen := map[string]bool{}
	for _, m := range in {
		switch strings.ToLower(strings.TrimSpace(m)) {
		case "text", "input_text", "output_text":
			seen["text"] = true
		case "image", "input_image", "vision", "vlm", "image_url":
			seen["image"] = true
		case "audio", "input_audio", "speech":
			seen["audio"] = true
		case "pdf", "file", "document", "input_file":
			seen["pdf"] = true
		}
	}
	var out []string
	for _, m := range order {
		if seen[m] {
			out = append(out, m)
		}
	}
	return out
}

package llmprobe

import "strings"

// 内建模型目录：离线兜底。
//
// 为什么不把这里做成大表：厂商每个月都在改窗口，写死的表一定会过期，过期后
// 它比「不知道」更危险（会给出一个看起来很确定的错值）。所以规则只覆盖
// 公开资料长期稳定的主流系列，并且 Source 标成 catalog——UI 上会显示成
// 「目录推测」，用户一眼能看出这不是探测结果。
//
// 探测链的顺序是 provider > catalog > defaults：只要供应商端点报了数，
// 这里的值就完全不参与计算。

type catalogRule struct {
	or   []string // 命中任意一个子串即可
	all  []string // 必须全部命中（用于「系列 + 变体」这种精确规则）
	none []string // 命中任意一个即否决
	caps Capabilities
}

// 目录规则从上到下匹配，先命中先返回，所以更具体的变体要排在系列前面。
var catalogRules = []catalogRule{
	// ---- Anthropic ----
	{
		or:   []string{"claude"},
		caps: Capabilities{ContextTokens: 200_000, MaxOutputTokens: 32_000, InputModalities: []string{"text", "image", "pdf"}, Reasoning: true, ReasoningEfforts: []string{"low", "medium", "high"}, Tools: true},
	},
	// ---- OpenAI 推理系 ----
	{
		or:   []string{"gpt-5"},
		caps: Capabilities{ContextTokens: 400_000, MaxOutputTokens: 128_000, InputModalities: []string{"text", "image"}, Reasoning: true, ReasoningEfforts: []string{"minimal", "low", "medium", "high"}, Tools: true},
	},
	{
		or:   []string{"gpt-6"},
		caps: Capabilities{ContextTokens: 1_050_000, MaxOutputTokens: 128_000, InputModalities: []string{"text", "image"}, Reasoning: true, ReasoningEfforts: []string{"low", "medium", "high"}, Tools: true},
	},
	{
		or:   []string{"o1-", "o3-", "o4-", "/o1", "/o3", "/o4"},
		caps: Capabilities{ContextTokens: 200_000, MaxOutputTokens: 100_000, InputModalities: []string{"text", "image"}, Reasoning: true, ReasoningEfforts: []string{"low", "medium", "high"}, Tools: true},
	},
	{
		or:   []string{"gpt-4o", "gpt-4.1", "gpt-4-turbo"},
		caps: Capabilities{ContextTokens: 128_000, MaxOutputTokens: 16_384, InputModalities: []string{"text", "image"}, Tools: true},
	},
	// ---- Google ----
	{
		or:   []string{"gemini"},
		caps: Capabilities{ContextTokens: 1_048_576, MaxOutputTokens: 65_536, InputModalities: []string{"text", "image", "audio", "pdf"}, Reasoning: true, ReasoningEfforts: []string{"low", "high"}, Tools: true},
	},
	// ---- Qwen：先 VL 变体（多模态），再纯文本系列 ----
	{
		all:  []string{"qwen", "vl"},
		caps: Capabilities{ContextTokens: 131_072, MaxOutputTokens: 32_768, InputModalities: []string{"text", "image"}, Tools: true},
	},
	{
		or:   []string{"qwen"},
		caps: Capabilities{ContextTokens: 131_072, MaxOutputTokens: 32_768, InputModalities: []string{"text"}, Tools: true},
	},
	// ---- 智谱 GLM ----
	{
		or:   []string{"glm"},
		caps: Capabilities{ContextTokens: 131_072, MaxOutputTokens: 32_768, InputModalities: []string{"text"}, Reasoning: true, ReasoningEfforts: []string{"low", "medium", "high"}, Tools: true},
	},
	// ---- DeepSeek ----
	{
		or:   []string{"deepseek"},
		caps: Capabilities{ContextTokens: 131_072, MaxOutputTokens: 32_768, InputModalities: []string{"text"}, Reasoning: true, ReasoningEfforts: []string{"low", "medium", "high"}, Tools: true},
	},
	// ---- Moonshot ----
	{
		or:   []string{"kimi"},
		caps: Capabilities{ContextTokens: 262_144, MaxOutputTokens: 32_768, InputModalities: []string{"text"}, Tools: true},
	},
	// ---- MiniMax ----
	{
		or:   []string{"minimax"},
		caps: Capabilities{ContextTokens: 204_800, MaxOutputTokens: 32_768, InputModalities: []string{"text"}, Reasoning: true, Tools: true},
	},
	// ---- xAI ----
	{
		or:   []string{"grok"},
		caps: Capabilities{ContextTokens: 131_072, MaxOutputTokens: 32_768, InputModalities: []string{"text", "image"}, Reasoning: true, Tools: true},
	},
	// ---- Meta / Mistral ----
	{
		or:   []string{"llama"},
		caps: Capabilities{ContextTokens: 131_072, MaxOutputTokens: 8_192, InputModalities: []string{"text"}, Tools: true},
	},
	{
		or:   []string{"mistral", "magistral", "codestral"},
		caps: Capabilities{ContextTokens: 131_072, MaxOutputTokens: 32_768, InputModalities: []string{"text"}, Tools: true},
	},
	// ---- 本地常见小模型 ----
	{
		or:   []string{"gemma"},
		caps: Capabilities{ContextTokens: 131_072, MaxOutputTokens: 8_192, InputModalities: []string{"text"}},
	},
}

// FromCatalog 按模型名匹配内建目录；没有命中返回 ok=false。
//
// 只做子串匹配，不做正则：模型 id 的写法太野（带厂商前缀、带 :free 后缀、
// 带 @quant 标签），正则只会更脆。
func FromCatalog(model string) (Capabilities, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return Capabilities{}, false
	}
	for _, rule := range catalogRules {
		if len(rule.or) > 0 {
			hit := false
			for _, s := range rule.or {
				if strings.Contains(name, s) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		if len(rule.all) > 0 {
			hit := true
			for _, s := range rule.all {
				if !strings.Contains(name, s) {
					hit = false
					break
				}
			}
			if !hit {
				continue
			}
		}
		excluded := false
		for _, s := range rule.none {
			if strings.Contains(name, s) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		caps := rule.caps.clone()
		caps.Source = SourceCatalog
		caps.Model = model
		return caps, true
	}
	return Capabilities{}, false
}

// Defaults 是什么都不知道时的保守假设：200K 窗口、8K 输出、纯文本。
// 与 agent.ResolveContextWindowTokens 的默认值保持一致，避免两处各说各话。
func Defaults() Capabilities {
	return Capabilities{
		ContextTokens:    200_000,
		MaxOutputTokens:  8_192,
		InputModalities:  []string{"text"},
		OutputModalities: []string{"text"},
		Source:           SourceDefaults,
	}
}

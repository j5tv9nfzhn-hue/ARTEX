package llmprobe

import (
	"encoding/json"
	"strconv"
	"strings"
)

// 各家模型列表对同一个概念用的键名完全不一样，而且「训练上限」和「当前服务
// 上限」经常同时出现（llama.cpp 的 n_ctx_train vs n_ctx、LM Studio 的
// max_context_length vs loaded_context_length）。读错一个就会让压缩阈值偏差
// 十几倍，所以这里按「正在服务的窗口优先」排了序，并且在整个响应树里找，
// 而不是假设它长在第几层。
var contextKeys = []string{
	"loaded_context_length", // LM Studio：当前实例实际服务的窗口
	"n_ctx",                 // llama.cpp /props.default_generation_settings.n_ctx
	"max_model_len",         // vLLM
	"max_input_tokens",      // Anthropic /v1/models
	"context_length",        // OpenRouter / Ollama 风格
	"context_window",        // Groq / 通用 OpenAI 兼容
	"max_context_length",    // LM Studio / Mistral（多为模型训练上限）
	"inputTokenLimit",       // Gemini models.list
	"n_ctx_train",           // llama.cpp meta：训练窗口，不是服务窗口
	"context_size",
}

var outputKeys = []string{
	"max_completion_tokens", // OpenRouter top_provider / 通用
	"max_output_tokens",     // 通用
	"outputTokenLimit",      // Gemini
	"max_output_length",
	"max_tokens", // 目录里常作输出上限，放最后
}

// 合法窗口的区间：1K 以下多半是解析错了（比如把 n_embd 当成 n_ctx），
// 10M 以上同样可疑。
const (
	minSaneTokens = 1024
	maxSaneTokens = 10_000_000
)

// ExtractModelCaps 从模型列表/元数据响应里提取某个模型的能力。
//
// modelID 为空时取第一条；非空但没有任何条目匹配时返回 ok=false——
// **宁可什么都不返回，也不能把隔壁模型的窗口安到当前模型头上**，
// 那正是「压缩阈值莫名其妙」的经典成因。
func ExtractModelCaps(body []byte, modelID string) (Capabilities, bool) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return Capabilities{}, false
	}
	entry, ok := pickEntry(root, modelID)
	if !ok {
		return Capabilities{}, false
	}
	return capsFromEntry(entry)
}

// pickEntry 在几种常见响应外形里找出「这条模型」的对象。
func pickEntry(root any, modelID string) (map[string]any, bool) {
	var list []any
	switch v := root.(type) {
	case []any:
		list = v
	case map[string]any:
		// {"data":[…]} / {"models":[…]} / {"items":[…]}；都不是就把整个对象当条目
		// （llama.cpp /props、Ollama /api/show 都只有单个对象）。
		for _, key := range []string{"data", "models", "items", "results"} {
			if arr, ok := v[key].([]any); ok {
				list = arr
				break
			}
		}
		if list == nil {
			return v, true
		}
	default:
		return nil, false
	}

	want := strings.ToLower(strings.TrimSpace(modelID))
	// 依次放宽：完全相同 → 去掉 models/ 前缀后相同 → 包含。
	for _, strict := range []bool{true, false} {
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := entryID(m)
			if id == "" {
				continue
			}
			if matches(id, want, strict) {
				return m, true
			}
		}
	}
	// 没有指定模型（或列表只有一条）时才允许退而求其次；否则宁可不给。
	if want == "" || len(list) == 1 {
		if m, ok := list[0].(map[string]any); ok {
			return m, true
		}
	}
	return nil, false
}

// entryID 取条目里能当模型标识的字段。
func entryID(m map[string]any) string {
	for _, k := range []string{"id", "model", "name", "model_id"} {
		if s, ok := m[k].(string); ok && s != "" {
			return strings.ToLower(strings.TrimSpace(s))
		}
	}
	return ""
}

// matches 比较模型 id。strict=true 时要求完全相等（允许 models/ 前缀差异），
// strict=false 时退化成包含匹配，兼容 "qwen3.8-flash-next" vs
// "Qwen/Qwen3.8-Flash-Next" 这类带前缀/大小写差异的写法。
func matches(id, want string, strict bool) bool {
	if want == "" {
		return false
	}
	id = strings.TrimPrefix(id, "models/")
	want = strings.TrimPrefix(want, "models/")
	if id == want {
		return true
	}
	if strict {
		return false
	}
	return strings.Contains(id, want) || strings.Contains(want, id)
}

// capsFromEntry 把一条模型记录翻成能力描述。
func capsFromEntry(m map[string]any) (Capabilities, bool) {
	var c Capabilities
	if n := numberByPriority(m, contextKeys); n > 0 && n >= minSaneTokens && n <= maxSaneTokens {
		c.ContextTokens = int(n)
	}
	if n := numberByPriority(m, outputKeys); n > 0 && n <= maxSaneTokens {
		c.MaxOutputTokens = int(n)
	}

	in, out := modalitiesOf(m)
	c.InputModalities = in
	c.OutputModalities = out
	c.Reasoning, c.ReasoningEfforts = reasoningOf(m)
	c.Tools = toolsOf(m)

	return c, c.Known()
}

// ---------- 通用取值 ----------

// numberByPriority 在整个对象树里按 keys 的先后顺序找第一个出现的数字。
//
// 用「键的优先级」而不是「深度优先级」：OpenRouter 把窗口放在 top-level，
// Anthropic 放在条目里，LM Studio 的 max_context_length 和
// loaded_context_length 又在同一层——只有按语义排序才稳。
func numberByPriority(root map[string]any, keys []string) float64 {
	found := map[string]float64{}
	walkNumber(root, found, 0)
	for _, k := range keys {
		if v, ok := found[k]; ok && v > 0 {
			return v
		}
	}
	return 0
}

func walkNumber(v any, out map[string]float64, depth int) {
	if depth > 8 {
		return
	}
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if _, exists := out[k]; !exists {
				if n, ok := asNumber(val); ok {
					out[k] = n
					// Ollama /api/show 把窗口写在 "<arch>.context_length" 这类带前缀的
					// 键里（qwen3.context_length）。补一份去掉前缀的别名，后面的
					// 优先级查表才能命中。
					if i := strings.LastIndex(k, "."); i >= 0 && i+1 < len(k) {
						if sub := k[i+1:]; sub != "" {
							if _, dup := out[sub]; !dup {
								out[sub] = n
							}
						}
					}
				}
			}
			switch val.(type) {
			case map[string]any, []any:
				walkNumber(val, out, depth+1)
			}
		}
	case []any:
		for _, item := range t {
			switch item.(type) {
			case map[string]any, []any:
				walkNumber(item, out, depth+1)
			}
		}
	}
}

// asNumber 同时接受 JSON 数字与数字字符串（不少网关把限制写成 "131072"）。
func asNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

func findValue(v any, key string, depth int) (any, bool) {
	if depth > 8 {
		return nil, false
	}
	switch t := v.(type) {
	case map[string]any:
		if val, ok := t[key]; ok {
			return val, true
		}
		for _, val := range t {
			if got, ok := findValue(val, key, depth+1); ok {
				return got, true
			}
		}
	case []any:
		for _, item := range t {
			if got, ok := findValue(item, key, depth+1); ok {
				return got, true
			}
		}
	}
	return nil, false
}

func stringsOf(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		if s, ok := v.(string); ok && s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---------- 模态 / 推理 / 工具 ----------

func modalitiesOf(m map[string]any) (in, out []string) {
	// 1) 扁平写法：input_modalities / output_modalities
	if v, ok := findValue(m, "input_modalities", 0); ok {
		in = NormalizeModalities(stringsOf(v))
	}
	if v, ok := findValue(m, "output_modalities", 0); ok {
		out = NormalizeModalities(stringsOf(v))
	}
	// 2) models.dev 写法：modalities: {input: [...], output: [...]}
	if v, ok := findValue(m, "modalities", 0); ok {
		if mm, ok := v.(map[string]any); ok {
			if len(in) == 0 {
				in = NormalizeModalities(stringsOf(mm["input"]))
			}
			if len(out) == 0 {
				out = NormalizeModalities(stringsOf(mm["output"]))
			}
		}
	}
	// 3) 本地推理进程：LM Studio type=vlm、Ollama capabilities 含 vision
	if t, ok := m["type"].(string); ok && strings.EqualFold(t, "vlm") {
		in = NormalizeModalities(append(in, "image"))
	}
	if caps := capabilitiesArray(m); len(caps) > 0 {
		in = NormalizeModalities(append(in, caps...))
	}
	// text 是所有对话模型的隐含输入；各家只报 image/audio 时不能把 text 丢掉，
	// 否则上层路由会以为这个模型读不了文本。
	in = NormalizeModalities(append([]string{"text"}, in...))
	if len(in) == 0 {
		in = []string{"text"}
	}
	return in, out
}

// capabilitiesArray 读 Ollama /api/show 的 capabilities: ["completion","vision","tools"]。
// 注意它和 OpenRouter 的 capabilities 对象重名，这里只认数组形式。
func capabilitiesArray(m map[string]any) []string {
	if v, ok := findValue(m, "capabilities", 0); ok {
		if arr, ok := v.([]any); ok {
			out := make([]string, 0, len(arr))
			for _, item := range arr {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return nil
}

func reasoningOf(m map[string]any) (bool, []string) {
	var efforts []string

	// OpenRouter：reasoning: {supported_efforts: [...], mandatory: bool}
	if v, ok := findValue(m, "reasoning", 0); ok {
		switch r := v.(type) {
		case bool:
			if r {
				return true, nil
			}
		case map[string]any:
			efforts = append(efforts, stringsOf(r["supported_efforts"])...)
			if sup, ok := r["supported"].(bool); ok && sup {
				return true, dedupeStrings(efforts)
			}
			if mand, ok := r["mandatory"].(bool); ok && mand {
				return true, dedupeStrings(efforts)
			}
		}
	}
	// models.dev：reasoning_options: [{type, values:[...]}] 或 reasoning: true
	if v, ok := findValue(m, "supported_efforts", 0); ok {
		efforts = append(efforts, stringsOf(v)...)
	}
	if v, ok := findValue(m, "reasoning_options", 0); ok {
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				if mm, ok := item.(map[string]any); ok {
					efforts = append(efforts, stringsOf(mm["values"])...)
					efforts = append(efforts, stringsOf(mm["options"])...)
				}
			}
		}
	}
	// supported_parameters 里出现 reasoning / reasoning_effort 也是强信号。
	for _, p := range stringsOf(findValueDefault(m, "supported_parameters")) {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "reasoning", "reasoning_effort", "include_reasoning", "thinking":
			return true, dedupeStrings(efforts)
		}
	}
	// Ollama capabilities 里含 thinking；Gemini 有 thinking: true。
	if v, ok := findValue(m, "thinking", 0); ok {
		if b, ok := v.(bool); ok && b {
			return true, dedupeStrings(efforts)
		}
	}
	for _, c := range capabilitiesArray(m) {
		if strings.EqualFold(c, "thinking") {
			return true, dedupeStrings(efforts)
		}
	}
	return false, dedupeStrings(efforts)
}

func toolsOf(m map[string]any) bool {
	for _, p := range stringsOf(findValueDefault(m, "supported_parameters")) {
		if strings.EqualFold(strings.TrimSpace(p), "tools") || strings.EqualFold(strings.TrimSpace(p), "tool_choice") {
			return true
		}
	}
	for _, c := range capabilitiesArray(m) {
		if strings.EqualFold(c, "tools") {
			return true
		}
	}
	if v, ok := findValue(m, "tool_call", 0); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// findValueDefault 是 findValue 的「返回零值」简写，用于可缺省字段。
func findValueDefault(m map[string]any, key string) any {
	if v, ok := findValue(m, key, 0); ok {
		return v
	}
	return nil
}

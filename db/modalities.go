package db

import (
	"strings"
	"unicode"
)

// ---------- 输入模态(input modalities, 计划.md 75–96) ----------
//
// input_modalities 是每个 LLM 配置对「能接收哪些输入模态」的声明,逗号分隔,
// 取值 text/image/audio/pdf(顺序无关,读写前由本文件归一)。语义:
//
//	""            = 未声明 → 按模型名启发式推断(InferInputModalities,保守:
//	                只有已知的多模态模型才会推断出 image;不确定即纯文本)。
//	"text,image"  = 显式声明,以用户勾选为准;text 始终隐含。
//
// 运行时按 EffectiveInputModalities 判路由:遇到配置无法接收的附件必须显式报错,
// 绝不静默丢弃,也不把附件硬塞进纯文本请求(闸门在 server/modalities.go)。

const (
	ModalityText  = "text"
	ModalityImage = "image"
	ModalityAudio = "audio"
	ModalityPDF   = "pdf"
)

// modalityOrder 是归一化的规范顺序:读写、展示、报错都按它排列,保证同一份声明
// 在任何路径上写出的字符串完全一致(可比较、可断言)。
var modalityOrder = []string{ModalityText, ModalityImage, ModalityAudio, ModalityPDF}

// ParseModalities 把声明串拆成规范顺序、去重后的已知模态列表。分隔符接受逗号、
// 中文逗号、分号与空白(前端复选框、手改 DB、导入配置都不会写坏);未知取值一律
// 丢弃——宁可当作未声明走保守推断,也不把垃圾值带进能力判断。
func ParseModalities(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, tok := range strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '，', ';', '；':
			return true
		}
		return unicode.IsSpace(r)
	}) {
		switch m := strings.ToLower(strings.TrimSpace(tok)); m {
		case ModalityText, ModalityImage, ModalityAudio, ModalityPDF:
			seen[m] = true
		}
	}
	var out []string
	for _, m := range modalityOrder {
		if seen[m] {
			out = append(out, m)
		}
	}
	return out
}

// NormalizeModalities 把一份声明归一成写入 DB 的规范串:"" 保持 ""(未声明),
// 其余取 ParseModalities 的结果并补上 text——任何能读这条消息的模型都能读文本,
// 所以即便调用方只声明 image,存下来的也是 "text,image"。
func NormalizeModalities(raw string) string {
	mods := ParseModalities(raw)
	if len(mods) == 0 {
		return ""
	}
	if mods[0] != ModalityText {
		mods = append([]string{ModalityText}, mods...)
	}
	return strings.Join(mods, ",")
}

// visionModelHints 是「已知支持图片输入」的模型名片段(全小写,子串匹配,含
// provider 前缀的写法如 openai/gpt-4o 也能命中)。保守原则:只收确信的——宁可
// 漏判(推断成纯文本,用户可在配置里手动勾选),不可误判(把纯文本模型当成能
// 看图,附件路由就失去意义)。新增多模态模型时在这里加片段即可。
var visionModelHints = []string{
	// OpenAI:GPT-4o 起的旗舰与新模型基本都带视觉;gpt-4-turbo 也支持(裸
	// gpt-4 不支持,故不能只匹配 "gpt-4")。
	"gpt-4o", "gpt-4.1", "gpt-4.5", "gpt-5", "gpt-4-turbo", "chatgpt",
	// OpenAI o 系列(o1/o3/o4)支持图片输入;前缀匹配,兼容 o3-mini 等变体。
	"o1-", "o3-", "o4-",
	// Anthropic:Claude 3 及之后全系支持图片(claude-2 不支持,故不匹配裸 claude-)。
	"claude-3", "claude-4", "claude-sonnet", "claude-opus", "claude-haiku",
	// Google:Gemini 1.5 及之后全系原生多模态。
	"gemini-1.5", "gemini-2", "gemini-3",
	// 通义千问 / 智谱:VL(视觉语言)与 omni 系列。
	"qwen-vl", "qwen2-vl", "qwen2.5-vl", "qwen3-vl", "qvq-", "qwen-omni",
	"glm-4v", "glm-4.1v", "glm-4.5v", "glm-4.6v",
	// 开源视觉模型与其它厂商的 -vision 命名(grok-2-vision、llava、pixtral…)。
	"llava", "internvl", "minicpm-v", "pixtral", "deepseek-vl",
	"grok-2-vision", "grok-4", "vision-", "-vision",
}

// exactVisionModels 是按整名匹配的视觉模型:短片段会误伤,单独列出。
var exactVisionModels = map[string]bool{
	"o1": true, "o3": true, "o4": true,
}

// InferInputModalities 在配置未声明时按模型名推断能力。保守:不确定 → 纯文本。
// 只推断 text(+image 命中已知视觉模型);音频/PDF 的命名没有可靠共识,一律不
// 推断,需要时由用户在配置里显式勾选声明。
func InferInputModalities(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ModalityText
	}
	for _, hint := range visionModelHints {
		if strings.Contains(m, hint) {
			return ModalityText + "," + ModalityImage
		}
	}
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	if exactVisionModels[m] {
		return ModalityText + "," + ModalityImage
	}
	return ModalityText
}

// EffectiveModalities 合并「声明 + 推断」:非空声明优先(先归一),空声明按模型名
// 推断。返回值一定非空且含 text,可直接喂给 SupportsModality。
func EffectiveModalities(declared, model string) string {
	if decl := NormalizeModalities(declared); decl != "" {
		return decl
	}
	return InferInputModalities(model)
}

// EffectiveInputModalities 是本配置运行时实际生效的输入模态(声明优先,否则按
// 模型名推断)。它就是「运行时可读的能力元数据」(计划 75–96);批量 4 的按角色
// 路由可直接消费它,而不是自己解析声明串。
func (p *LLMProfile) EffectiveInputModalities() string {
	if p == nil {
		return ModalityText
	}
	return EffectiveModalities(p.InputModalities, p.Model)
}

// SupportsModality 判断一份已生效的模态声明(EffectiveInputModalities 的输出,
// 或任何声明串)是否包含指定模态。text 是兜底:没有哪个能读消息的模型不支持
// 文本。注意 ""(未声明)在这里等价于「什么都没有声明」,调用方应先经过
// EffectiveInputModalities 推断,不要直接拿空串来判断。
func SupportsModality(effective, modality string) bool {
	if modality == "" || modality == ModalityText {
		return true
	}
	for _, m := range ParseModalities(effective) {
		if m == modality {
			return true
		}
	}
	return false
}

// Supports 判断本配置实际生效的能力里是否包含某模态(声明优先,未声明按模型名
// 推断)。批量 4 的按角色路由消费这一族方法。
func (p *LLMProfile) Supports(modality string) bool {
	return SupportsModality(p.EffectiveInputModalities(), modality)
}

// SupportsImage / SupportsAudio / SupportsPDF 是常用的具体模态读口。
func (p *LLMProfile) SupportsImage() bool { return p.Supports(ModalityImage) }
func (p *LLMProfile) SupportsAudio() bool { return p.Supports(ModalityAudio) }
func (p *LLMProfile) SupportsPDF() bool   { return p.Supports(ModalityPDF) }

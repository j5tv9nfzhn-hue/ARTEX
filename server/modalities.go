package server

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// ---------- 输入模态闸门(计划.md 75–96) ----------
//
// 用户消息带附件时,附件以「方式1」进入:文件落在会话/任务工作目录,消息里带
// 绝对路径清单(composeAgentMessage),由模型自己用 Read/Bash 打开。某个配置
// 能不能接收这类附件,取决于该配置声明(或按模型名推断)的输入模态
// (db.LLMProfile.EffectiveInputModalities)。
//
// 运行时的硬规则:
//   - 支持 → 照常走方式1,附件正常附加;
//   - 不支持 → 显式报错(本文件),绝不静默丢弃附件、也绝不把附件硬塞进只发
//     文本的请求——用户看到的必须是「这个配置接不了这张图」,而不是附件凭空
//     消失,也不是请求在服务端莫名其妙地失败。
//
// 闸门放在两个用户消息入口「持久化用户轮次之前」:server.chat(任务主 Agent)
// 与 pgSendConversationMessage(会话)。只在这轮真的会交给 LLM 时才拦(规则模式
// /无可用模型时原样放行)。调度器/重测等系统轮次不带附件,不经过这里。

// ErrModalityUnsupported 是闸门的哨兵错误;*modalityUnsupportedError 实现了
// Unwrap,调用方用 errors.Is(err, ErrModalityUnsupported) 判定,不必依赖中文文案。
var ErrModalityUnsupported = errors.New("LLM profile does not declare support for this input modality")

// modalityUnsupportedError 一次报出所有「附件用到、但目标配置未声明支持」的模态,
// 而不是遇到第一个就返回——用户改一次配置就该看到全部冲突。文案面向最终用户,
// 包含:哪个配置、缺哪种模态、涉及哪些附件、去哪里改。
type modalityUnsupportedError struct {
	profileName string   // 目标 LLM 配置名;空 = 用「全局设置」兜底
	modalities  []string // 缺的模态,中文标签(图片/音频/PDF)
	files       []string // 触发拦截的附件文件名
}

func (e *modalityUnsupportedError) Error() string {
	mods := strings.Join(e.modalities, "、")
	files := e.files
	if len(files) > 5 {
		files = append(append([]string{}, files[:5]...), fmt.Sprintf("等共 %d 个文件", len(e.files)))
	}
	name := e.profileName
	if name == "" {
		name = sourceNameGlobalConfig
	}
	return fmt.Sprintf(
		"当前模型配置未声明支持该输入模态：配置「%s」未声明支持%s输入（附件：%s）。"+
			"请在 系统 → LLM 设置 中编辑该配置并勾选「%s」，或改用支持它的模型配置；"+
			"系统不会把附件塞进纯文本请求，也不会静默忽略它。",
		name, mods, strings.Join(files, "、"), mods)
}

// Unwrap 把闸门错误归到哨兵上,让 errors.Is(err, ErrModalityUnsupported) 成立。
func (e *modalityUnsupportedError) Unwrap() error { return ErrModalityUnsupported }

// attachmentModality 按扩展名把附件归到一个受闸门管制的输入模态;返回 "" 表示
// 该文件不受管制——文本/代码/表格等文档走方式1 的路径清单对所有配置都成立。
// 扩展名列表宁可少收:漏分类只是少了保护(该拦的没拦,与旧行为一致),误分类
// 会拦下本来完全能处理的普通文件。
func attachmentModality(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".avif", ".heic", ".heif", ".svg":
		return db.ModalityImage
	case ".mp3", ".wav", ".flac", ".m4a", ".aac", ".ogg", ".oga", ".opus":
		return db.ModalityAudio
	case ".pdf":
		return db.ModalityPDF
	}
	return ""
}

// modalityLabel 是模态的中文标签,用于错误文案(与前端复选框、系统设置术语一致)。
func modalityLabel(modality string) string {
	switch modality {
	case db.ModalityImage:
		return "图片"
	case db.ModalityAudio:
		return "音频"
	case db.ModalityPDF:
		return "PDF"
	}
	return modality
}

// checkTurnAttachments 是一轮用户消息的输入模态预检:逐个附件查目标配置的
// 生效模态(声明优先,未声明按模型名推断)。全部可接收返回 nil;否则返回一个
// 包含全部冲突模态与附件的 *modalityUnsupportedError。
//
// effective 传 db.LLMProfile.EffectiveInputModalities();纯环境变量配置没有
// DB 记录,传 db.InferInputModalities(cfg.Model)。profileName 只用于错误文案。
func checkTurnAttachments(profileName, effective string, atts []chatAttachment) error {
	if len(atts) == 0 {
		return nil
	}
	var missing []string
	var files []string
	seen := map[string]bool{}
	for _, a := range atts {
		modality := attachmentModality(a.Name)
		if modality == "" || db.SupportsModality(effective, modality) {
			continue
		}
		if !seen[modality] {
			seen[modality] = true
			missing = append(missing, modalityLabel(modality))
		}
		files = append(files, a.Name)
	}
	if len(missing) == 0 {
		return nil
	}
	return &modalityUnsupportedError{profileName: profileName, modalities: missing, files: files}
}

// conversationTurnModalities 解析一段会话这一轮实际会用的配置名与生效输入模态。
// 优先级与 resolveChatAgent 一致:agent 绑定 / 会话指定(且有 Key,能真正发请求)
// → 全局激活配置 → 环境变量配置(无 DB 记录,按模型名推断)。调用点在这之前已
// 把 resolveChatAgent == nil 的请求以 503 拒绝,故末级兜底几乎不会命中。
func (s *Server) conversationTurnModalities(c *db.Conversation) (string, string) {
	if s.m != nil && s.m.pg != nil {
		if eff := s.effectiveProfileForAgent(c.AgentKey, c.LLMProfileID); eff != nil {
			if p, err := s.m.pg.ProfileByID(*eff); err == nil && p != nil && p.APIKey != "" {
				return p.Name, p.EffectiveInputModalities()
			}
		}
		if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil && p.APIKey != "" {
			return p.Name, p.EffectiveInputModalities()
		}
	}
	return s.envProfileModalities()
}

// taskChatModalities 解析任务主 Agent 对话这一轮实际会用的配置名与生效输入模态。
// resolveTaskRoleLLM 与运行时 taskLLMRuntime.current 是同一优先级链(角色绑定 →
// 任务链 → 全局),且不额外建 provider。调用点已确认 taskRuntimeAvailable,故基本
// 都能命中真实配置;ProfileID 为空 = 环境变量配置,按模型名推断。
func (s *Server) taskChatModalities(t *Task) (string, string) {
	resolved, err := s.resolveTaskRoleLLM(t, "mainagent")
	if err != nil {
		// 解析失败(数据库故障)不掩盖附件:宁可按纯文本拦截并显式报错,
		// 也不冒险把未知能力的配置当成支持。
		return "", db.ModalityText
	}
	if resolved.ProfileID != nil && s.m != nil && s.m.pg != nil {
		if p, perr := s.m.pg.ProfileByID(*resolved.ProfileID); perr == nil && p != nil {
			return p.Name, p.EffectiveInputModalities()
		}
	}
	return resolved.Name, db.InferInputModalities(resolved.Model)
}

// envProfileModalities 是没有任何 DB 配置可用时的末级兜底:用环境变量配置的
// 模型名推断。环境配置未启用 → 纯文本,任何图片/音频/PDF 附件都会被显式拦截
// (这种情况下 resolveChatAgent 早已 503,正常不会走到)。
func (s *Server) envProfileModalities() (string, string) {
	s.cfgMu.Lock()
	on, name, cfg := s.llmOn, s.llmProf, s.llmCfg
	s.cfgMu.Unlock()
	if !on {
		return name, db.ModalityText
	}
	return name, db.InferInputModalities(cfg.Model)
}

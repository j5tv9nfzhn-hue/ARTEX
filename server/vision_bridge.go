package server

// ---------- 视觉桥的接入层 ----------
//
// agent.DescribeImage 只负责「把一张图发给模型、拿回转写」。本文件负责策略：
//   - 什么时候做（开关、有没有图片附件）；
//   - 用哪个配置做（会话/任务各自的解析链，与既有模态闸门完全一致）；
//   - 做失败怎么办（绝不静默：转写失败要写进消息、也要在活动流里留一条错误卡）。
//
// 为什么要「先转写再交给 agent」：norma SDK 的消息模型没有 image 块，
// agent 拿到的永远只能是文本。所以图片必须先变成文本，这是唯一的通路。

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

const (
	// settingVisionBridgeMode 控制视觉桥：auto=自动转写(默认) / off=关闭。
	// 关闭后回到旧行为：消息里只有附件路径，模型看不到图。
	settingVisionBridgeMode = "vision_bridge_mode"
	visionBridgeAuto        = "auto"
	visionBridgeOff         = "off"
	// visionTurnDeadline 是一轮里所有图片转写的总时限。图片再多也不让一次
	// 发送无限期挂着——超时的那些按失败处理并明确告知。
	visionTurnDeadline = 90 * time.Second
	// visionMaxParallel 限制并发，避免一次贴 10 张图把上游打爆。
	visionMaxParallel = 4
	// visionInjectMaxChars 是「注入消息」的转写上限。转写会进入对话历史，之后
	// 每一轮都会随上下文重发一次——12K 字符 ≈ 每轮多 3–4K token。所以注入只带
	// 前 4000 字，全文留在 <原图>.vision.txt，模型需要细看时可以 Read。
	visionInjectMaxChars = 4000
)

// VisionBridgeMode 返回视觉桥策略。默认 auto：只要配置声明了图片输入就转写。
// 非法值回落 auto（关掉一个已经声明支持图片的配置的转写，只会让模型看不见图）。
func (m *Manager) VisionBridgeMode() string {
	if m == nil || m.pg == nil {
		return visionBridgeAuto
	}
	v, ok, err := m.pg.GetSetting(settingVisionBridgeMode)
	if err != nil || !ok {
		return visionBridgeAuto
	}
	if strings.TrimSpace(v) == visionBridgeOff {
		return visionBridgeOff
	}
	return visionBridgeAuto
}

// visionConfigOf 把 DB 里的 LLM 配置转成 agent.Config。与 loadLLMConfig 保持
// 同样的口径（代理、会话头都带上），否则视觉请求会绕过用户配置的出口代理。
func visionConfigOf(p *db.LLMProfile) agent.Config {
	cfg := agent.ConfigFrom(p.Format, p.Model, p.BaseURL, p.APIKey, p.Proxy)
	cfg.SessionHeaderKey = p.SessionHeaderKey
	return cfg
}

// visionConfigForConversation 解析一段会话这一轮实际会用的 LLM 配置，
// 优先级与 conversationTurnModalities / resolveChatAgent 完全一致。
func (s *Server) visionConfigForConversation(c *db.Conversation) (agent.Config, string, bool) {
	if s.m == nil || s.m.pg == nil || c == nil {
		return agent.Config{}, "", false
	}
	if eff := s.effectiveProfileForAgent(c.AgentKey, c.LLMProfileID); eff != nil {
		if p, err := s.m.pg.ProfileByID(*eff); err == nil && p != nil && p.APIKey != "" {
			return visionConfigOf(p), p.Name, true
		}
	}
	if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil && p.APIKey != "" {
		return visionConfigOf(p), p.Name, true
	}
	return agent.Config{}, "", false
}

// visionConfigForTask 解析任务主 Agent 这一轮实际会用的 LLM 配置，
// 与 taskChatModalities 同一条优先级链（角色绑定 → 任务链 → 全局）。
func (s *Server) visionConfigForTask(t *Task) (agent.Config, string, bool) {
	if s.m == nil || s.m.pg == nil || t == nil {
		return agent.Config{}, "", false
	}
	if resolved, err := s.resolveTaskRoleLLM(t, "mainagent"); err == nil && resolved.ProfileID != nil {
		if p, perr := s.m.pg.ProfileByID(*resolved.ProfileID); perr == nil && p != nil && p.APIKey != "" {
			return visionConfigOf(p), p.Name, true
		}
	}
	if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil && p.APIKey != "" {
		return visionConfigOf(p), p.Name, true
	}
	return agent.Config{}, "", false
}

// hasImageAttachment 判断这轮附件里是否真的有图片。纯文本/代码/表格附件走
// 原来的「路径清单 + Read」路径，不必付一次视觉请求的钱。
func hasImageAttachment(atts []chatAttachment) bool {
	for _, a := range atts {
		if attachmentModality(a.Name) == db.ModalityImage {
			return true
		}
	}
	return false
}

// truncateRunes 按字符（不是字节）截断，避免把中文/emoji 切坏。
func truncateRunes(s string, max int) (string, bool) {
	r := []rune(s)
	if len(r) <= max {
		return s, false
	}
	return string(r[:max]), true
}

// visionWarning 是一张图转写失败的原因，会写进消息与活动流两处。
type visionWarning struct {
	Name   string
	Reason string
}

// describeImages 对附件里的图片做视觉转写。
//
// 只处理被 modality 闸门归为 image 的文件——闸门已经保证目标配置声明了图片
// 输入，所以这里不必再判断能力；没声明的早在入口 400 掉了。
func (s *Server) describeImages(ctx context.Context, cfg agent.Config, atts []chatAttachment, baseDir string) ([]agent.VisionText, []visionWarning) {
	var imgs []chatAttachment
	for _, a := range atts {
		if attachmentModality(a.Name) == db.ModalityImage {
			imgs = append(imgs, a)
		}
	}
	if len(imgs) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, visionTurnDeadline)
	defer cancel()

	notes := make([]agent.VisionText, len(imgs))
	warns := make([]*visionWarning, len(imgs))
	sem := make(chan struct{}, visionMaxParallel)
	var wg sync.WaitGroup
	for i, a := range imgs {
		wg.Add(1)
		go func(i int, a chatAttachment) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			abs := filepath.Join(baseDir, filepath.FromSlash(a.Path))
			vt, err := agent.DescribeImage(ctx, cfg, abs)
			if err != nil {
				warns[i] = &visionWarning{Name: a.Name, Reason: err.Error()}
				return
			}
			if vt.Name == "" {
				vt.Name = a.Name
			}
			notes[i] = vt
		}(i, a)
	}
	wg.Wait()

	outNotes := make([]agent.VisionText, 0, len(imgs))
	var outWarns []visionWarning
	for i := range imgs {
		if warns[i] != nil {
			outWarns = append(outWarns, *warns[i])
			continue
		}
		outNotes = append(outNotes, notes[i])
	}
	return outNotes, outWarns
}

// visionSection 把转写结果拼成一段注入文本。两段分开写：
// 成功的一段告诉模型「这是转写，原图可复核」；失败的一段明确告诉模型
// 「这些图的内容未知，别根据文件名猜」——避免把缺失当默认。
func visionSection(notes []agent.VisionText, warns []visionWarning) string {
	if len(notes) == 0 && len(warns) == 0 {
		return ""
	}
	var b strings.Builder
	if len(notes) > 0 {
		b.WriteString("\n\n【图片转写】（视觉模型已读取原图并逐字转写；原图仍在下列路径，需要复核时可用 Read 打开）")
		for _, n := range notes {
			text, cut := truncateRunes(n.Text, visionInjectMaxChars)
			fmt.Fprintf(&b, "\n=== %s ===\n%s", n.Name, text)
			if cut && n.CachePath != "" {
				fmt.Fprintf(&b, "\n…（转写过长已截断，完整全文见 %s）", n.CachePath)
			}
		}
	}
	if len(warns) > 0 {
		b.WriteString("\n\n【图片转写失败】（以下图片的内容未知；不要根据文件名或扩展名猜测其中写了什么，必要时请向用户确认）")
		for _, w := range warns {
			fmt.Fprintf(&b, "\n- %s：%s", w.Name, w.Reason)
		}
	}
	return b.String()
}

// visionMessageForConversation 是会话轮的接入点：在 agent 真正开跑之前（后台
// goroutine 内）完成转写，并把失败写成错误活动——用户能在时间线里看到，
// 而不是只把提示藏在模型上下文里。
func (s *Server) visionMessageForConversation(ctx context.Context, c *db.Conversation, atts []chatAttachment, baseDir, msg string) string {
	if s.m.VisionBridgeMode() == visionBridgeOff || !hasImageAttachment(atts) {
		return msg
	}
	cfg, profileName, ok := s.visionConfigForConversation(c)
	if !ok {
		return msg
	}
	if hasImageAttachment(atts) {
		_, _ = s.m.pg.AppendConvActivity(c.ID, db.Activity{
			Worker:  "system",
			Kind:    "text",
			Summary: fmt.Sprintf("正在把图片转写成文本（视觉模型：%s）…", profileName),
		})
	}
	notes, warns := s.describeImages(ctx, cfg, atts, baseDir)
	for _, w := range warns {
		_, _ = s.m.pg.AppendConvActivity(c.ID, db.Activity{
			Worker:  "system",
			Kind:    "text",
			IsError: true,
			Summary: fmt.Sprintf("图片未转写：%s（%s；配置「%s」）。本轮模型看不到这张图的内容。", w.Name, w.Reason, profileName),
		})
	}
	return msg + visionSection(notes, warns)
}

// visionMessageForTask 是任务主 Agent 轮的接入点，逻辑与会话侧一致；
// 失败同样发一条错误活动，并且带上配置名便于排查。
func (s *Server) visionMessageForTask(ctx context.Context, t *Task, atts []chatAttachment, baseDir, msg string, emit func(db.Activity)) string {
	if s.m.VisionBridgeMode() == visionBridgeOff || !hasImageAttachment(atts) {
		return msg
	}
	cfg, profileName, ok := s.visionConfigForTask(t)
	if !ok {
		return msg
	}
	if emit != nil {
		emit(db.Activity{
			Worker:  "mainagent",
			Kind:    "text",
			Summary: fmt.Sprintf("正在把图片转写成文本（视觉模型：%s）…", profileName),
		})
	}
	notes, warns := s.describeImages(ctx, cfg, atts, baseDir)
	for _, w := range warns {
		if emit != nil {
			emit(db.Activity{
				Worker:  "mainagent",
				Kind:    "text",
				IsError: true,
				Summary: fmt.Sprintf("图片未转写：%s（%s；配置「%s」）。本轮模型看不到这张图的内容。", w.Name, w.Reason, profileName),
			})
		}
	}
	return msg + visionSection(notes, warns)
}

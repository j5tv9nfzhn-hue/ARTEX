package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// 会话级 Token 预算：把「任务级、收尾后暂停」的预算闸，下沉到控制台聊天 / Harness
// 共同使用的会话执行路径（server/conversations.go runConversationTurn）。
//
// 为什么是默认开启、而不是一个开关：
//   - 预警类功能要求用户预判一个他从没量过的数字，真实场景里没人配；
//   - 预算要在**突破之前**就有人负责，否则只是账单的事后注解。
//
// 所以这里给一个足够宽松的默认值（40 万 token，约十几到几十轮），
// 用户什么都不做也有刹车；要关掉就显式设 0 或 mode=off。
const (
	// settingDefaultConvTokenBudget 是会话默认 token 预算（0=不限）。
	settingDefaultConvTokenBudget = "default_conversation_token_budget"
	// settingConvBudgetMode 决定越线后的行为：enforce=终止本轮 / warn=只显示不拦（默认）/ off=关闭。
	settingConvBudgetMode = "conversation_budget_mode"

	// convBudgetDefaultTokens 是未配置时的兜底预算。
	convBudgetDefaultTokens = 400000

	convBudgetModeOff     = "off"
	convBudgetModeWarn    = "warn"
	convBudgetModeEnforce = "enforce"
)

// DefaultConvTokenBudget 返回会话默认 token 预算。未配置 → 兜底 40 万；
// 显式配置 0 → 不限（沿用任务预算的语义）；非法值 → 兜底。
func (m *Manager) DefaultConvTokenBudget() int {
	if m == nil || m.pg == nil {
		return convBudgetDefaultTokens
	}
	v, ok, err := m.pg.GetSetting(settingDefaultConvTokenBudget)
	if err != nil {
		return convBudgetDefaultTokens
	}
	if !ok || strings.TrimSpace(v) == "" {
		return convBudgetDefaultTokens
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return convBudgetDefaultTokens
	}
	return n
}

// ConvBudgetMode 返回越线行为。**默认 warn（只显示、不拦截）**：
// 零配置的用户不会因为升级后多出一个默认上限而被无声掐断——那是典型的负优化。
// 只有「显式填了预算」或「显式把 mode 设成 enforce」才真正执行；
// 非法值回落 warn，宁可少刹车也不要误杀。
func (m *Manager) ConvBudgetMode() string {
	if m == nil || m.pg == nil {
		return convBudgetModeWarn
	}
	v, ok, err := m.pg.GetSetting(settingConvBudgetMode)
	if err != nil || !ok {
		// 没写 mode：只有显式设过预算的人才自动进入 enforce——
		//「你填了数」本身就是「你要管」的表达。
		if _, budgetSet, _ := m.pg.GetSetting(settingDefaultConvTokenBudget); budgetSet {
			return convBudgetModeEnforce
		}
		return convBudgetModeWarn
	}
	switch strings.TrimSpace(v) {
	case convBudgetModeOff:
		return convBudgetModeOff
	case convBudgetModeWarn:
		return convBudgetModeWarn
	case convBudgetModeEnforce:
		return convBudgetModeEnforce
	default:
		return convBudgetModeWarn
	}
}

// convBudgetView 是给前端的只读快照。控制台聊天页与 Harness 共用同一份字段，
// 避免两边各算一套阈值。
type convBudgetView struct {
	Limit     int64  `json:"limit"`
	Used      int64  `json:"used"`
	Remaining int64  `json:"remaining"`
	Mode      string `json:"mode"`
	Tier      string `json:"tier"`
	Exhausted bool   `json:"exhausted"`
}

// convBudgetView 汇总一条会话的历史用量（kind=result 的 token 四列）。
// 读库失败按 0 处理：预算视图坏掉不能把聊天页一起打崩。
func (s *Server) convBudgetView(c *db.Conversation) convBudgetView {
	limit := int64(s.m.DefaultConvTokenBudget())
	mode := s.m.ConvBudgetMode()
	var used int64
	if s.m != nil && s.m.pg != nil && c != nil {
		if n, err := s.m.pg.ConvTokenTotal(c.ID); err == nil {
			used = n
		}
	}
	remaining := int64(0)
	if limit > 0 {
		remaining = limit - used
		if remaining < 0 {
			remaining = 0
		}
	}
	return convBudgetView{
		Limit:     limit,
		Used:      used,
		Remaining: remaining,
		Mode:      mode,
		Tier:      agent.ConvBudgetTier(used, limit, mode),
		Exhausted: limit > 0 && mode == convBudgetModeEnforce && used >= limit,
	}
}

// convBudgetInfo 是 GET /api/conversations/{id}/budget。
func (s *Server) convBudgetInfo(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.convBudgetView(c))
}

// emitConvBudgetStop 在预算越线、本轮被终止后写一条结构化终止记录。
// 它不是普通报错：前端按 metadata.reason 渲染成「预算用尽」卡片，
// 时间线里保留已完成步骤与检查点，用户提高预算后可接着跑。
func (s *Server) emitConvBudgetStop(pg *db.DB, convID, used, limit int64) {
	if pg == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"reason":      "budget_exhausted",
		"used":        used,
		"limit":       limit,
		"remaining":   0,
		"resumable":   true,
		"next_action": "提高 default_conversation_token_budget，或换更省的模型后重发；本轮已完成步骤与检查点已保留",
	})
	// 与任务侧 AbortPausedByTokenBudget 的留痕一致：Worker=system + Kind=text，
	// 不置 IsError——预算用尽是「预期内的收尾」，不是执行失败，避免前端渲染成报错气泡。
	_, _ = pg.AppendConvActivity(convID, db.Activity{
		Worker:   "system",
		Kind:     "text",
		Summary:  fmt.Sprintf("本轮已收尾：会话 Token 预算用尽（已用 %d / 预算 %d）", used, limit),
		Detail:   string(payload),
		Metadata: json.RawMessage(payload),
	})
}

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
)

// 手机端总控台的聚合接口。
//
// 设计约束（见 mobile/README.md 的「五块功能（范围）」与「已落地」）：
//   - 只做"看全局与结论"，不复制 PC 的编辑能力；
//   - 口径全部复用现有实现（stats / tasks / llm_usage），不新造业务逻辑；
//   - 首屏只发一个请求（/api/mobile/overview），其余屏各自一个。
//
// 鉴权沿用 requireAuth：这些路由注册在同一个 mux 上，自动受 JWT 保护。

// mobileAlertSink 让内存里的 UI 通知同时落库，手机端收件箱才能回溯与统计未读。
// 未装载时保持原来的纯内存行为，不影响桌面端。
var mobileAlertSink atomic.Pointer[func(UIEvent)]

// setMobileAlertSink 在服务启动时装载一次。
func setMobileAlertSink(fn func(UIEvent)) {
	mobileAlertSink.Store(&fn)
}

// persistUIEvent 尽力而为地落库，绝不阻塞通知分发。
func persistUIEvent(ev UIEvent) {
	fn := mobileAlertSink.Load()
	if fn == nil {
		return
	}
	(*fn)(ev)
}

// mobileTaskBrief 是总览里够用就好的任务摘要，完整字段仍走 /api/tasks。
type mobileTaskBrief struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Goal      string `json:"goal,omitempty"`
	Paused    bool   `json:"paused"`
	Queued    bool   `json:"queued"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

// mobileTokenTotals 是三个口径的 token 合计（与 web 用量面板同源）。
type mobileTokenTotals struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	CacheRead int `json:"cache_read"`
}

type mobileUsageDay struct {
	Date      string `json:"date"`
	Input     int    `json:"input"`
	Output    int    `json:"output"`
	CacheRead int    `json:"cache_read"`
}

type mobileProfileUsage struct {
	Profile   string `json:"profile"`
	Input     int    `json:"input"`
	Output    int    `json:"output"`
	CacheRead int    `json:"cache_read"`
}

// mobileOverviewDTO 是总览屏的一次性载荷：手机端首屏只发这一个请求。
type mobileOverviewDTO struct {
	ServerTime    int64             `json:"server_time"`
	EngineMode    string            `json:"engine_mode"`
	LLMReady      bool              `json:"llm_ready"`
	ActiveTask    *mobileTaskBrief  `json:"active_task,omitempty"`
	Running       int               `json:"running"`
	Queued        int               `json:"queued"`
	Paused        int               `json:"paused"`
	AlertsUnread  int               `json:"alerts_unread"`
	AlertsByLevel map[string]int    `json:"alerts_by_level"`
	TokensToday   mobileTokenTotals `json:"tokens_today"`
	Assets        int               `json:"assets"`
	Traffic       int               `json:"traffic"`
	Approvals     int               `json:"approvals"`
}

type mobileAlertsDTO struct {
	ServerTime int64              `json:"server_time"`
	Items      []pgdb.MobileAlert `json:"items"`
	Unread     int                `json:"unread"`
	ByLevel    map[string]int     `json:"by_level"`
}

type mobileUsageDTO struct {
	Days      int                  `json:"days"`
	Total     mobileTokenTotals    `json:"total"`
	Daily     []mobileUsageDay     `json:"daily"`
	ByProfile []mobileProfileUsage `json:"by_profile"`
}

// mobileRoutes 注册手机端接口。调用点：Server.Handler()。
func (s *Server) mobileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mobile/overview", s.mobileOverview)
	mux.HandleFunc("GET /api/mobile/alerts", s.mobileListAlerts)
	mux.HandleFunc("POST /api/mobile/alerts/read", s.mobileReadAlerts)
	mux.HandleFunc("POST /api/mobile/alerts/read-all", s.mobileReadAllAlerts)
	mux.HandleFunc("GET /api/mobile/tasks", s.mobileTasks)
	mux.HandleFunc("GET /api/mobile/usage", s.mobileUsage)
	mux.HandleFunc("POST /api/mobile/push/register", s.mobilePushRegister)
	mux.HandleFunc("DELETE /api/mobile/push/register", s.mobilePushDelete)

	pg := s.m.PG()
	if pg == nil {
		return
	}
	// 表结构幂等补齐，失败只影响手机端收件箱，不影响主流程。
	if err := pg.EnsureMobileTables(); err != nil {
		return
	}
	setMobileAlertSink(func(ev UIEvent) {
		_ = pg.InsertMobileAlert(&pgdb.MobileAlert{
			TS:       ev.TS,
			Level:    ev.Level,
			Kind:     ev.Kind,
			Severity: ev.Severity,
			Title:    ev.Title,
			Body:     ev.Body,
			Href:     ev.Href,
			TaskID:   ev.TaskID,
		})
	})
}

// mobileOverview 组装总览屏：状态 + 任务计数 + 未读告警 + 今日 token + 资产与流量。
func (s *Server) mobileOverview(w http.ResponseWriter, r *http.Request) {
	out := mobileOverviewDTO{
		ServerTime:    time.Now().UnixMilli(),
		EngineMode:    "idle",
		AlertsByLevel: map[string]int{},
	}
	if s.engine != nil {
		out.LLMReady = s.engine.Ready()
	}

	running := 0
	for _, t := range s.m.List() {
		if t == nil {
			continue
		}
		if t.Queued {
			out.Queued++
			continue
		}
		if t.Paused {
			out.Paused++
			continue
		}
		running++
	}
	out.Running = running
	if at := s.m.ActiveTask(); at != nil {
		out.EngineMode = "running"
		if at.Paused {
			out.EngineMode = "paused"
		}
		out.ActiveTask = &mobileTaskBrief{
			ID:        at.ID,
			Name:      at.Name,
			Goal:      at.Goal,
			Paused:    at.Paused,
			Queued:    at.Queued,
			CreatedAt: at.CreatedAt,
		}
	}

	if tr := s.m.Traffic(); tr != nil {
		if c, err := tr.Count(); err == nil {
			out.Traffic = c
		}
	}
	if as := s.m.Assets(); as != nil {
		if counts, err := as.CountsByType(); err == nil {
			for _, n := range counts {
				out.Assets += n
			}
		}
	}

	if pg := s.m.PG(); pg != nil {
		if pending, err := pg.ListPendingIntercepts(); err == nil {
			out.Approvals = len(pending)
		}
		if total, byLevel, err := pg.MobileAlertUnread(); err == nil {
			out.AlertsUnread = total
			if byLevel != nil {
				out.AlertsByLevel = byLevel
			}
		}
		if daily, err := pg.UsageDaily(1); err == nil {
			for _, d := range daily {
				out.TokensToday.Input += d.InputTokens
				out.TokensToday.Output += d.OutputTokens
				out.TokensToday.CacheRead += d.CacheReadTokens
			}
		}
	}

	writeJSON(w, http.StatusOK, out)
}

// mobileListAlerts 返回收件箱：默认最近 50 条，未读情况一并带回。
func (s *Server) mobileListAlerts(w http.ResponseWriter, r *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	sinceID, _ := strconv.ParseInt(q.Get("since_id"), 10, 64)
	level := strings.TrimSpace(q.Get("level"))
	unreadOnly := q.Get("unread") == "1" || q.Get("unread") == "true"

	items, err := pg.ListMobileAlerts(limit, sinceID, level, unreadOnly)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list alerts failed")
		return
	}
	out := mobileAlertsDTO{
		ServerTime: time.Now().UnixMilli(),
		Items:      items,
		ByLevel:    map[string]int{},
	}
	if total, byLevel, err := pg.MobileAlertUnread(); err == nil {
		out.Unread = total
		if byLevel != nil {
			out.ByLevel = byLevel
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// mobileReadAlerts 把若干条标记已读。
func (s *Server) mobileReadAlerts(w http.ResponseWriter, r *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := decodeMobileJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := pg.MarkMobileAlertsRead(body.IDs); err != nil {
		writeErr(w, http.StatusInternalServerError, "mark read failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// mobileReadAllAlerts 清空未读。
func (s *Server) mobileReadAllAlerts(w http.ResponseWriter, r *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	if err := pg.MarkAllMobileAlertsRead(); err != nil {
		writeErr(w, http.StatusInternalServerError, "mark read failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// mobileTaskItem 是手机端任务列表的一项：桌面端任务字段 + 一个可用的完成度。
// 完成度按目标(goal)达成比例计算；没有目标时给 -1，界面画灰环而不是假装 0%。
type mobileTaskItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name,omitempty"`
	Goal        string  `json:"goal,omitempty"`
	Description string  `json:"description,omitempty"`
	Paused      bool    `json:"paused"`
	Queued      bool    `json:"queued"`
	Completed   bool    `json:"completed"`
	CreatedAt   int64   `json:"created_at,omitempty"`
	CompletedAt int64   `json:"completed_at,omitempty"`
	GoalsTotal  int     `json:"goals_total"`
	GoalsMet    int     `json:"goals_met"`
	Progress    float64 `json:"progress"`
}

// mobileTasks 返回任务列表与完成度。手机端只做「看进度 + 启停」，
// 因此不返回工具调用、日志与 diff，避免把 PC 的细节搬到小屏上。
func (s *Server) mobileTasks(w http.ResponseWriter, r *http.Request) {
	out := []mobileTaskItem{}
	for _, t := range s.m.List() {
		if t == nil {
			continue
		}
		item := mobileTaskItem{
			ID:          t.ID,
			Name:        t.Name,
			Goal:        t.Goal,
			Description: t.Description,
			Paused:      t.Paused,
			Queued:      t.Queued,
			Completed:   t.CompletedAt > 0,
			CreatedAt:   t.CreatedAt,
			CompletedAt: t.CompletedAt,
			Progress:    -1,
		}
		if t.Store != nil {
			if goals, err := t.Store.ListByKind(pgdb.KindGoal, 10000); err == nil {
				item.GoalsTotal = len(goals)
				for _, g := range goals {
					if g.State == "met" {
						item.GoalsMet++
					}
				}
				if item.GoalsTotal > 0 {
					item.Progress = float64(item.GoalsMet) / float64(item.GoalsTotal)
				}
			}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

// mobileUsage 返回近 N 天的 token 曲线与按配置档的分布（不含金额，金额口径仍以 web 为准）。
func (s *Server) mobileUsage(w http.ResponseWriter, r *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 7
	}
	rows, err := pg.UsageDaily(days)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "usage failed")
		return
	}

	byDate := map[string]*mobileUsageDay{}
	byProfile := map[string]*mobileProfileUsage{}
	out := mobileUsageDTO{Days: days, Daily: []mobileUsageDay{}, ByProfile: []mobileProfileUsage{}}
	for _, row := range rows {
		out.Total.Input += row.InputTokens
		out.Total.Output += row.OutputTokens
		out.Total.CacheRead += row.CacheReadTokens

		day := byDate[row.Date]
		if day == nil {
			day = &mobileUsageDay{Date: row.Date}
			byDate[row.Date] = day
		}
		day.Input += row.InputTokens
		day.Output += row.OutputTokens
		day.CacheRead += row.CacheReadTokens

		prof := byProfile[row.ProfileName]
		if prof == nil {
			prof = &mobileProfileUsage{Profile: row.ProfileName}
			byProfile[row.ProfileName] = prof
		}
		prof.Input += row.InputTokens
		prof.Output += row.OutputTokens
		prof.CacheRead += row.CacheReadTokens
	}

	dates := make([]string, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	for _, d := range dates {
		out.Daily = append(out.Daily, *byDate[d])
	}
	for _, p := range byProfile {
		out.ByProfile = append(out.ByProfile, *p)
	}
	sort.Slice(out.ByProfile, func(i, j int) bool {
		return out.ByProfile[i].Input+out.ByProfile[i].Output > out.ByProfile[j].Input+out.ByProfile[j].Output
	})
	writeJSON(w, http.StatusOK, out)
}

// mobilePushRegister 记录一台设备的推送凭据（APNs / FCM）。
func (s *Server) mobilePushRegister(w http.ResponseWriter, r *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	var body struct {
		Platform string `json:"platform"`
		Token    string `json:"token"`
		Device   string `json:"device"`
	}
	if err := decodeMobileJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	platform := strings.ToLower(strings.TrimSpace(body.Platform))
	token := strings.TrimSpace(body.Token)
	if (platform != "ios" && platform != "android") || token == "" || len(token) > 512 {
		writeErr(w, http.StatusBadRequest, "platform must be ios|android and token required")
		return
	}
	if err := pg.RegisterPushToken(platform, token, strings.TrimSpace(body.Device)); err != nil {
		writeErr(w, http.StatusInternalServerError, "register failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// mobilePushDelete 注销设备（换机或退出登录）。
func (s *Server) mobilePushDelete(w http.ResponseWriter, r *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		var body struct {
			Token string `json:"token"`
		}
		if err := decodeMobileJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		token = strings.TrimSpace(body.Token)
	}
	if token == "" {
		writeErr(w, http.StatusBadRequest, "token required")
		return
	}
	if err := pg.DeletePushToken(token); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// decodeMobileJSON 读取受限长度的 JSON 请求体。
func decodeMobileJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

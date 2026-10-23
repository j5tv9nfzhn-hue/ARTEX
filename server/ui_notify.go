package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// 本文件是**应用内通知**（前端右下角浮层 + 顶部横幅）的唯一事件源。
//
// 与出站推送（db/notification.go + notify/ + server/notifier.go 的 IM/Webhook
// 投递）是两条互不相干的链路：
//   - 出站推送要落库（notification_events）以便重试与审计，语义是「人不在电脑前
//     也要收到」；
//   - 应用内通知只在**当前这个浏览器会话**里有意义，刷新即清空，因此**不落库**，
//     只进内存 ring buffer 并 fan-out 给 SSE 订阅者。
//
// 两者共用同一批「人必须知道」的业务时刻（任务终态、子代理结算、漏洞落库），
// 但互不依赖：出站推送失败不影响浮层，浮层订阅者掉线也不影响推送。
//
// 收敛点（五个，覆盖全部来源）：
//  1. task_notify.go emitTaskStatusEvent     → 任务终态（done/failed/timeout）
//  2. checkpoints.go onIntentSettled         → #N 子代理结算
//  3. conversations.go runConversationTurn   → AI 回复回合结束
//  4. db.OnFindingInserted 钩子              → 漏洞落库（带严重程度）
//  5. intercept.OnApprovalPending 钩子       → 等待人工审批（不点就卡住任务）
const (
	uiNotifyRingCap   = 300 // ring 只用于断线补洞，不对外提供历史列表
	uiNotifyReplayMax = 100 // 一次断线重连最多补发多少条
	uiNotifyPingEvery = 20 * time.Second
)

// UIEvent 是一条应用内通知。字段刻意保持最小：前端只负责渲染与跳转，
// 不做业务判断（level/severity 的取舍在服务端已经定好）。
type UIEvent struct {
	ID        int64  `json:"id"`
	TS        int64  `json:"ts"`    // Unix 毫秒
	Kind      string `json:"kind"`  // task_done|task_failed|task_timeout|subagent_*|chat_*|finding
	Level     string `json:"level"` // success | info | warn | error
	Severity  string `json:"severity,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	TaskTitle string `json:"task_title,omitempty"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	Href      string `json:"href,omitempty"`
}

// uiNotifyHubT 与 logSink 同构：ring + 订阅者集合，持锁非阻塞投递。
// 单个进程只有一个 hub（与 logSink 一样做成包级单例），因此不需要挂到 Server
// 结构体上，Manager 侧的事件收敛点也能直接调用。
type uiNotifyHubT struct {
	mu   sync.Mutex
	ring []UIEvent
	cap  int
	seq  int64
	subs map[chan UIEvent]struct{}
}

var uiNotifyHub = &uiNotifyHubT{cap: uiNotifyRingCap, subs: map[chan UIEvent]struct{}{}}

func (h *uiNotifyHubT) emit(ev UIEvent) {
	if ev.Level == "" {
		ev.Level = "info"
	}
	h.mu.Lock()
	h.seq++
	ev.ID = h.seq
	if ev.TS == 0 {
		ev.TS = time.Now().UnixMilli()
	}
	h.ring = append(h.ring, ev)
	if len(h.ring) > h.cap {
		h.ring = h.ring[len(h.ring)-h.cap:]
	}
	for sub := range h.subs {
		select {
		case sub <- ev:
		default: // 订阅者卡住就丢这条，绝不阻塞事件源（漏一条浮层 → 不影响任务）
		}
	}
	h.mu.Unlock()

	// 手机端收件箱要能回溯，事件同时落库；失败只影响回溯，不影响通知本身。
	go persistUIEvent(ev)
}

// recent 返回 id > since 的事件与当前游标。
func (h *uiNotifyHubT) recent(since int64, limit int) ([]UIEvent, int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cursor := h.seq
	if limit <= 0 || limit > h.cap {
		limit = h.cap
	}
	out := make([]UIEvent, 0, 8)
	for _, ev := range h.ring {
		if ev.ID > since {
			out = append(out, ev)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, cursor
}

// subscribe 返回订阅通道、订阅瞬间的队头 seq 与退订函数。
// 同时返回 head 是为了消除「订阅成功 → 读游标」之间的竞态：新连接若先订阅再
// 去 recent() 取游标，这中间产生的事件 id 会小于游标而被当作旧事件丢掉。
func (h *uiNotifyHubT) subscribe() (<-chan UIEvent, int64, func()) {
	ch := make(chan UIEvent, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	head := h.seq
	h.mu.Unlock()
	var once sync.Once
	return ch, head, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// --- 事件构造：文案与等级都在这里定，调用点只传事实 ---------------------------

func uiSeverityLabel(sev string) string {
	switch sev {
	case "critical":
		return "严重"
	case "high":
		return "高危"
	case "medium":
		return "中危"
	case "low":
		return "低危"
	}
	return "未知"
}

func uiTrim(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if max > 0 && len(s) > max {
		r := []rune(s)
		if len(r) > max {
			return string(r[:max]) + "…"
		}
	}
	return s
}

func uiTaskLabel(id int64, title string) string {
	if s := uiTrim(title, 60); s != "" {
		return s
	}
	return "任务 #" + strconv.FormatInt(id, 10)
}

func uiTaskHref(taskID string) string {
	if taskID == "" || taskID == "0" {
		return ""
	}
	return "/function/tasks/detail?id=" + taskID
}

// emitTaskMilestone 任务终态（planner 判定目标达成 / 失败 / 时限收尾）。
func emitTaskMilestone(taskID int64, taskTitle, status, reason string) {
	ev := UIEvent{
		Kind:      "task_" + status,
		TaskID:    strconv.FormatInt(taskID, 10),
		TaskTitle: uiTrim(taskTitle, 60),
		Href:      uiTaskHref(strconv.FormatInt(taskID, 10)),
	}
	label := uiTaskLabel(taskID, taskTitle)
	switch status {
	case "done":
		ev.Level = "success"
		ev.Title = "任务完成：" + label
		ev.Body = "AI 已判定总目标达成，任务收尾。"
	case "failed":
		ev.Level = "error"
		ev.Title = "任务失败：" + label
	case "timeout":
		ev.Level = "warn"
		ev.Title = "任务超时收尾：" + label
	default:
		ev.Level = "warn"
		ev.Title = "任务状态变化：" + label
	}
	if s := uiTrim(reason, 160); s != "" {
		if ev.Body != "" {
			ev.Body += " " + s
		} else {
			ev.Body = s
		}
	}
	uiNotifyHub.emit(ev)
}

// emitSubagentMilestone #N 子代理（意图）结算。
// 只有 done/blocked/exhausted/stopped 是「一个子代理的结局」，其余状态
// （open/running/paused/deleted）是过程态，不进浮层。
func emitSubagentMilestone(taskID, taskTitle string, intentID int64, state string) {
	var level, suffix string
	switch state {
	case "done":
		level, suffix = "success", "完成"
	case "blocked":
		level, suffix = "warn", "受阻"
	case "exhausted":
		level, suffix = "error", "步数耗尽"
	case "stopped":
		level, suffix = "warn", "被终止"
	default:
		return
	}
	uiNotifyHub.emit(UIEvent{
		Kind:      "subagent_" + state,
		Level:     level,
		TaskID:    taskID,
		TaskTitle: uiTrim(taskTitle, 60),
		Title:     fmt.Sprintf("#%d 子代理%s", intentID, suffix),
		Body:      uiTrim(taskTitle, 60),
		Href:      uiTaskHref(taskID),
	})
}

// emitChatMilestone AI 回复回合结束（聊天 / 复测 / 触发器会话共用同一收敛点）。
func emitChatMilestone(convID int64, convTitle, status, reason string) {
	var level, title string
	switch status {
	case "completed":
		level, title = "success", "AI 回复完成"
	case "stopped":
		level, title = "warn", "AI 回复已停止"
	default:
		level, title = "error", "AI 回复失败"
	}
	ev := UIEvent{
		Kind:  "chat_" + status,
		Level: level,
		Title: title,
		Body:  uiTrim(convTitle, 60),
		Href:  "/chat?c=" + strconv.FormatInt(convID, 10),
	}
	if s := uiTrim(reason, 160); s != "" {
		if ev.Body != "" {
			ev.Body += " · " + s
		} else {
			ev.Body = s
		}
	}
	uiNotifyHub.emit(ev)
}

// emitFindingMilestone 漏洞落库。severity 原样带给前端（前端用 lib/severity.ts
// 的统一色彩契约渲染），level 只决定「是否算必须打断」：critical/high 用 error
// 触发提示音，medium 用 warn，low 用 info。
func emitFindingMilestone(findingID, taskID int64, name, vulnclass, severity, summary, taskTitle string) {
	title := uiTrim(name, 80)
	if title == "" {
		title = uiTrim(vulnclass, 60)
	}
	if title == "" {
		title = "未命名漏洞"
	}
	level := "info"
	sev := strings.ToLower(strings.TrimSpace(severity))
	switch sev {
	case "critical", "high":
		level = "error"
	case "medium":
		level = "warn"
	}
	body := uiTrim(summary, 160)
	if t := uiTrim(taskTitle, 60); t != "" {
		if body != "" {
			body = t + " · " + body
		} else {
			body = t
		}
	}
	tid := ""
	if taskID > 0 {
		tid = strconv.FormatInt(taskID, 10)
	}
	// 点击直接落到该漏洞的详情页（与 findings 列表的链接口径一致），
	// 而不是笼统地回任务页：看到「发现高危漏洞」的人下一步就是看证据。
	href := uiTaskHref(tid)
	if findingID > 0 {
		href = "/function/findings/detail?id=" + strconv.FormatInt(findingID, 10)
	}
	uiNotifyHub.emit(UIEvent{
		Kind:      "finding",
		Level:     level,
		Severity:  sev,
		TaskID:    tid,
		TaskTitle: uiTrim(taskTitle, 60),
		Title:     fmt.Sprintf("[%s] %s", uiSeverityLabel(sev), title),
		Body:      body,
		Href:      href,
	})
}

// emitApprovalPendingMilestone 把「等待人工审批」送进应用内浮层。
//
// 这是唯一一个会真正把任务卡住、等人点一下的事件：审批不处理，worker 就一直
// 停在等待里直到超时。出站推送（notification_events → IM/Webhook）早就有它，
// 但应用内浮层此前没有入口——人在浏览器里反而看不到最该看的那条。
// level 用 error：横幅 + 下行双音，与「任务失败」同级，因为它同样在等一个决定。
func emitApprovalPendingMilestone(convID int64, taskID, toolName string, pendingID int64) {
	title := "等待人工审批"
	if n := uiTrim(toolName, 40); n != "" {
		title += "：" + n
	}
	body := ""
	if tid, err := strconv.ParseInt(taskID, 10, 64); err == nil && tid > 0 {
		body = "任务 #" + strconv.FormatInt(tid, 10)
	}
	if convID > 0 {
		c := "会话 #" + strconv.FormatInt(convID, 10)
		if body != "" {
			body += " · " + c
		} else {
			body = c
		}
	}
	if body == "" {
		body = "审批 #" + strconv.FormatInt(pendingID, 10)
	}
	uiNotifyHub.emit(UIEvent{
		Kind:   "approval_pending",
		Level:  "error",
		TaskID: taskID,
		Title:  title,
		Body:   body,
		Href:   "/system/intercept/approvals",
	})
}

// installUINotifyHooks 把两个「发生在本包之外」的业务时刻接到应用内通知上：
// 漏洞落库（db 包）与等待人工审批（intercept 包）。两个包都不认识 server，
// 因此统一用包级回调（见 db/findings.go、intercept/intercept.go）。
// 其余三个收敛点都是 server 自己的函数，直接调 emit 即可。
func (s *Server) installUINotifyHooks() {
	// 这个钩子是**单槽**的（db 包只有一个函数变量），所以两个互不相干的消费者
	// 必须在这里合流：应用内通知（漏了用户看不到）与自动复核入队（漏了口径为 0）。
	// 顺序无所谓，两者都不阻塞、都不返回错误。
	db.OnFindingInserted = func(id, taskID int64, name, vulnclass, severity, summary string) {
		title := ""
		if s != nil && s.m != nil && taskID > 0 {
			title = s.m.taskTitleFor(taskID)
		}
		emitFindingMilestone(id, taskID, name, vulnclass, severity, summary, title)
		s.enqueueAutoVerification(id, severity)
	}
	// 审批挂起发生在 intercept 包（那里才拿得到 pending id），用回调接进浮层。
	intercept.OnApprovalPending = func(convID int64, taskID, toolName string, pendingID int64) {
		emitApprovalPendingMilestone(convID, taskID, toolName, pendingID)
	}
}

// --- HTTP ---------------------------------------------------------------------

// streamUINotifications 推送应用内通知。
//
// 游标语义（与「刷新即清空」一致）：
//   - 新页面打开时不带 since、也不带 Last-Event-ID → 从**当前队头**开始，
//     历史一条都不补（刷新后浮层是空的）；
//   - EventSource 自动重连会带 Last-Event-ID（本端每条都写了 id: 行）→ 只补
//     掉断开期间漏掉的那几条，不会重复弹窗。
func (s *Server) streamUINotifications(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	since := int64(-1) // -1 = 新连接：从队头开始，不补历史
	if raw := r.URL.Query().Get("since"); raw != "" {
		since = int64(atoiDefault(raw, 0))
	}
	if le := r.Header.Get("Last-Event-ID"); le != "" {
		if n, err := strconv.ParseInt(le, 10, 64); err == nil {
			since = n
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // 反代不缓冲，否则事件积压到断开才到

	ch, head, unsub := uiNotifyHub.subscribe()
	defer unsub()
	send := func(ev UIEvent) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.ID, b)
		flusher.Flush()
	}
	if since >= 0 {
		events, cursor := uiNotifyHub.recent(since, uiNotifyReplayMax)
		for _, ev := range events {
			send(ev)
		}
		if cursor > since {
			since = cursor
		}
	} else {
		since = head
	}
	flusher.Flush()

	ctx := r.Context()
	ping := time.NewTicker(uiNotifyPingEvery)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.ID <= since {
				continue
			}
			since = ev.ID
			send(ev)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// listUINotifications 是 SSE 的降级读口：SSE 断开且浏览器不给 Last-Event-ID
// 时，前端可以按 cursor 拉一次，避免整段静默丢事件。
func (s *Server) listUINotifications(w http.ResponseWriter, r *http.Request) {
	since := int64(atoiDefault(r.URL.Query().Get("since"), 0))
	events, cursor := uiNotifyHub.recent(since, uiNotifyReplayMax)
	if events == nil {
		events = []UIEvent{}
	}
	writeJSON(w, 200, map[string]any{"events": events, "cursor": cursor})
}

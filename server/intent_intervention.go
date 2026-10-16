package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

const maxWorkerMessageBytes = 64 << 10

// 向暂停的 Worker 意图注入人工消息的中文 API 错误，直接展示为 toast。
// Worker 是引擎角色名、request_id 是协议字段，均保持原文。
const (
	errIntentRequestTooLarge   = "请求正文过大"
	errIntentMessageEmpty      = "消息不能为空"
	errIntentMessageTooLong    = "消息不能超过 4000 个字符"
	errIntentBadRequestID      = "request_id 只能包含 1~128 个字符，且仅限英文字母、数字、-、_、.、:"
	errIntentTaskDeleting      = "任务正在删除中，因此无法向 Worker 发送消息"
	errIntentTaskPaused        = "任务已暂停。请先恢复任务，然后再向 Worker 发送消息"
	errIntentTaskQueued        = "无法向处于等待状态的任务的 Worker 发送消息"
	errIntentTaskTerminal      = "无法向已结束任务的 Worker 发送消息"
	errIntentTaskSettling      = "任务正在收尾，无法向 Worker 发送消息"
	errIntentInheritedReadonly = "继承的意图为只读，因此无法向 Worker 发送消息"
	errIntentNotPaused         = "只能向已暂停的 Worker 发送消息。请先暂停"

	errIntentSteerEmpty      = "引导内容不能为空"
	errIntentSteerTooLong    = "引导内容不能超过 4000 个字符"
	errIntentSteerNotRunning = "只能引导正在运行的 Worker 意图"
)

func validWorkerMessageRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

// sendWorkerMessage continues a paused Worker intent with a human-authored message.
// The message is injected as the next turn's input through the same
// resume-from-transcript path the worker uses on a normal resume (ExecuteWithMessage),
// so this reuses the existing pause/resume machinery rather than a bespoke protocol.
// The run happens in a dedicated goroutine outside the worker pool (runDetachedIntent),
// so the message is picked up immediately even when every pool slot is busy — the same
// way the main-agent chat handler starts its run directly. In-memory only: a process
// restart re-runs the intent from its transcript without the message, which is
// acceptable for this rare interrupt-then-continue action.
func (s *Server) sendWorkerMessage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || iid <= 0 {
		writeErr(w, http.StatusBadRequest, "bad intent id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkerMessageBytes)
	var req struct {
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, errIntentRequestTooLarge)
			return
		}
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	message := strings.TrimSpace(req.Message)
	requestID := strings.TrimSpace(req.RequestID)
	if message == "" {
		writeErr(w, http.StatusBadRequest, errIntentMessageEmpty)
		return
	}
	if len([]rune(message)) > 4000 {
		writeErr(w, http.StatusBadRequest, errIntentMessageTooLong)
		return
	}
	if !validWorkerMessageRequestID(requestID) {
		writeErr(w, http.StatusBadRequest, errIntentBadRequestID)
		return
	}

	// Reject non-runnable task lifecycles up front so the caller gets a clear reason
	// instead of a silent no-op. The intent itself must be paused: the UI flow is
	// interrupt (pause) first, then send.
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, http.StatusConflict, errIntentTaskDeleting)
		return
	}
	lifecycle := t.lifecycleSnapshot()
	switch {
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		writeErr(w, http.StatusConflict, errIntentTaskPaused)
		return
	case lifecycle.Queued:
		writeErr(w, http.StatusConflict, errIntentTaskQueued)
		return
	case isTerminalStatus(lifecycle.Status):
		writeErr(w, http.StatusConflict, errIntentTaskTerminal)
		return
	case s.engine.isSettling(t.ID):
		writeErr(w, http.StatusConflict, errIntentTaskSettling)
		return
	}

	node, err := t.Store.GetNode(iid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			writeErr(w, http.StatusConflict, errIntentInheritedReadonly)
			return
		}
		writeErr(w, http.StatusNotFound, "intent not found")
		return
	}
	if node.Kind != db.KindIntent {
		writeErr(w, http.StatusConflict, "node is not an intent")
		return
	}
	if node.State != "paused" {
		writeErr(w, http.StatusConflict, errIntentNotPaused)
		return
	}
	agentMessage, ok := s.prepareChatMentionMessage(w, message)
	if !ok {
		return
	}

	// runDetachedIntent transitions paused->running, emits the user turn and starts a
	// dedicated run. Root the run at s.ctx so a disconnected browser cannot strand it
	// while task pause/delete/shutdown still stop it.
	if err := s.engine.runDetachedIntent(s.ctx, t, iid, requestID, message, agentMessage); err != nil {
		switch {
		case errors.Is(err, db.ErrIntentStateConflict):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":         iid,
		"state":      "running",
		"accepted":   true,
		"request_id": requestID,
	})
}

// steerIntent 把人工纠偏指令递交给一条正在运行的 Worker 意图（不暂停、不重跑）：
// 文本进入引擎的 steer 队列，worker 在下一步工具调用前收到并据此重新规划（见
// Engine.SteerWork / steerHooks）。与 sendWorkerMessage 不同，这条通路不改变意图
// 状态，所以只对 running 的意图有效——它必须投递到内存中正在执行的 work 上。
func (s *Server) steerIntent(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || iid <= 0 {
		writeErr(w, http.StatusBadRequest, "bad intent id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkerMessageBytes)
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, errIntentRequestTooLarge)
			return
		}
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeErr(w, http.StatusBadRequest, errIntentSteerEmpty)
		return
	}
	if len([]rune(text)) > 4000 {
		writeErr(w, http.StatusBadRequest, errIntentSteerTooLong)
		return
	}

	// 生命周期闸与 sendWorkerMessage 完全一致：删除/暂停/排队/终态/收尾期递话
	// 都会落进一个不会被消费的队列（或干脆没有 work），提前给出明确原因。
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, http.StatusConflict, errIntentTaskDeleting)
		return
	}
	lifecycle := t.lifecycleSnapshot()
	switch {
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		writeErr(w, http.StatusConflict, errIntentTaskPaused)
		return
	case lifecycle.Queued:
		writeErr(w, http.StatusConflict, errIntentTaskQueued)
		return
	case isTerminalStatus(lifecycle.Status):
		writeErr(w, http.StatusConflict, errIntentTaskTerminal)
		return
	case s.engine.isSettling(t.ID):
		writeErr(w, http.StatusConflict, errIntentTaskSettling)
		return
	}

	node, err := t.Store.GetNode(iid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			writeErr(w, http.StatusConflict, errIntentInheritedReadonly)
			return
		}
		writeErr(w, http.StatusNotFound, "intent not found")
		return
	}
	if node.Kind != db.KindIntent {
		writeErr(w, http.StatusConflict, "node is not an intent")
		return
	}
	if node.State != "running" {
		writeErr(w, http.StatusConflict, errIntentSteerNotRunning)
		return
	}
	// SteerWork 只投给内存里正在执行的 work；意图刚落库为 running 而 work 尚未
	// 注册（或已在收尾）时它会报错，原样透出给用户重试。
	if err := s.engine.SteerWork(iid, text); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": iid, "state": "running", "accepted": true})
}

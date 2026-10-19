// P1 协议面：Codex app-server 风格的 Thread / Turn / Item / Approval 资源面。
// 全部为薄壳——通过合成请求 + Request.SetPathValue 直接调用既有处理器（同一套
// 生命周期闸、同一套 DTO、同一套错误语义），不复制任何业务逻辑；聚合端点
// （items/tree）只做只读拼装。未启用/未匹配路径一律走原 REST 面，行为零改变。
package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const protoVersion = "artex-rea.p1"

// protoRecorder captures one upstream handler response for re-enveloping.
type protoRecorder struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func (p *protoRecorder) Header() http.Header {
	if p.header == nil {
		p.header = http.Header{}
	}
	return p.header
}

func (p *protoRecorder) WriteHeader(code int) {
	if p.code == 0 {
		p.code = code
	}
}

func (p *protoRecorder) Write(b []byte) (int, error) {
	if p.code == 0 {
		p.code = 200
	}
	return p.buf.Write(b)
}

// protoCall invokes one upstream handler directly with a synthetic request.
// Path values are set explicitly (Go 1.22+ SetPathValue); the facade itself has
// already passed requireAuth at the route edge, so the inner call is trusted.
func (s *Server) protoCall(handler http.HandlerFunc, method, target string, pvs map[string]string, query url.Values, body []byte) (int, []byte) {
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(s.ctx, method, target, rdr)
	if err != nil {
		return 500, []byte(`{"error":"internal dispatch failed"}`)
	}
	for k, v := range pvs {
		req.SetPathValue(k, v)
	}
	rec := &protoRecorder{}
	handler(rec, req)
	if rec.code == 0 {
		rec.code = 200
	}
	return rec.code, rec.buf.Bytes()
}

// protoWrite re-envelopes an upstream JSON payload in the protocol shape.
func protoWrite(w http.ResponseWriter, code int, resource, action, id string, upstream []byte) {
	var data any
	if len(upstream) > 0 {
		if err := json.Unmarshal(upstream, &data); err != nil {
			data = string(upstream)
		}
	}
	writeJSON(w, code, map[string]any{
		"protocol": protoVersion,
		"resource": resource,
		"action":   action,
		"id":       id,
		"data":     data,
	})
}

func protoReadBody(r *http.Request) []byte {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	return b
}

// protoThreadTaskID validates "task-<n>" and returns the numeric part.
func protoThreadTaskID(thread string) (string, bool) {
	id := strings.TrimPrefix(thread, "task-")
	if id == thread || id == "" {
		return "", false
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return "", false
	}
	return id, true
}

func protoPage(q url.Values) (after, limit int) {
	if v := q.Get("after"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			after = n
		}
	}
	limit = 500
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	return after, limit
}

// ---------- Thread ----------

// protoListThreads lists threads (= tasks). Upstream: GET /api/tasks.
func (s *Server) protoListThreads(w http.ResponseWriter, r *http.Request) {
	code, up := s.protoCall(s.listTasks, http.MethodGet, "/api/tasks", nil, r.URL.Query(), nil)
	protoWrite(w, code, "thread", "list", "", up)
}

// protoCreateThread creates a thread (= task). Upstream: POST /api/tasks.
func (s *Server) protoCreateThread(w http.ResponseWriter, r *http.Request) {
	code, up := s.protoCall(s.createTask, http.MethodPost, "/api/tasks", nil, nil, protoReadBody(r))
	protoWrite(w, code, "thread", "create", "", up)
}

// protoGetThread returns one thread snapshot. Upstream: GET /api/tasks/{id}.
func (s *Server) protoGetThread(w http.ResponseWriter, r *http.Request) {
	thread := r.PathValue("id")
	tid, ok := protoThreadTaskID(thread)
	if !ok {
		writeErr(w, 400, "线程标识应为 task-<数字>")
		return
	}
	code, up := s.protoCall(s.getTask, http.MethodGet, "/api/tasks/"+tid,
		map[string]string{"id": tid}, r.URL.Query(), nil)
	protoWrite(w, code, "thread", "get", thread, up)
}

// protoControl runs a thread-level control verb (start/pause/resume/stop…).
// Upstream: POST /api/tasks/{id}/control, payload passthrough.
func (s *Server) protoControl(w http.ResponseWriter, r *http.Request) {
	thread := r.PathValue("id")
	tid, ok := protoThreadTaskID(thread)
	if !ok {
		writeErr(w, 400, "线程标识应为 task-<数字>")
		return
	}
	code, up := s.protoCall(s.control, http.MethodPost, "/api/tasks/"+tid+"/control",
		map[string]string{"id": tid}, nil, protoReadBody(r))
	protoWrite(w, code, "thread", "control", thread, up)
}

// ---------- Turn ----------

// protoCreateTurn creates one turn on a thread.
//
// body: {"kind":"chat"|"intent", "intent_id":N(intent 时必填), "message":"..."}
//
//	或 {"kind":..., "payload":{...上游 DTO 原样...}}（payload 优先，透传）
//
// kind=chat → POST /api/chat（active-task 语义：线程必须是当前激活任务）；
// kind=intent → POST /api/tasks/{id}/intents/{iid}/messages。
func (s *Server) protoCreateTurn(w http.ResponseWriter, r *http.Request) {
	thread := r.PathValue("id")
	tid, ok := protoThreadTaskID(thread)
	if !ok {
		writeErr(w, 400, "线程标识应为 task-<数字>")
		return
	}
	var req struct {
		Kind     string          `json:"kind"`
		IntentID int64           `json:"intent_id"`
		Message  string          `json:"message"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(protoReadBody(r), &req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	switch req.Kind {
	case "chat":
		if act := s.m.ActiveTask(); act == nil || act.ID != tid {
			writeErr(w, 409, "chat 轮次只作用于当前激活任务，请先激活该线程（POST /api/active）")
			return
		}
		body := req.Payload
		if len(body) == 0 {
			b, _ := json.Marshal(map[string]any{"message": req.Message})
			body = b
		}
		code, up := s.protoCall(s.chat, http.MethodPost, "/api/chat", nil, nil, body)
		protoWrite(w, code, "turn", "create", thread, up)
	case "intent":
		if req.IntentID <= 0 {
			writeErr(w, 400, "kind=intent 需要 intent_id")
			return
		}
		iid := strconv.FormatInt(req.IntentID, 10)
		body := req.Payload
		if len(body) == 0 {
			b, _ := json.Marshal(map[string]any{"message": req.Message})
			body = b
		}
		code, up := s.protoCall(s.sendWorkerMessage, http.MethodPost,
			"/api/tasks/"+tid+"/intents/"+iid+"/messages",
			map[string]string{"id": tid, "iid": iid}, nil, body)
		protoWrite(w, code, "turn", "create", thread, up)
	default:
		writeErr(w, 400, "kind 仅支持 chat | intent")
	}
}

// protoSteer delivers a course-correction to a running work.
// body: {"intent_id":N, "text"|"message":"..."} 或 payload 透传。
// Upstream: POST /api/tasks/{id}/intents/{iid}/steer.
func (s *Server) protoSteer(w http.ResponseWriter, r *http.Request) {
	thread := r.PathValue("id")
	tid, ok := protoThreadTaskID(thread)
	if !ok {
		writeErr(w, 400, "线程标识应为 task-<数字>")
		return
	}
	var req struct {
		IntentID int64           `json:"intent_id"`
		Text     string          `json:"text"`
		Message  string          `json:"message"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(protoReadBody(r), &req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.IntentID <= 0 {
		writeErr(w, 400, "需要 intent_id")
		return
	}
	iid := strconv.FormatInt(req.IntentID, 10)
	body := req.Payload
	if len(body) == 0 {
		text := req.Text
		if text == "" {
			text = req.Message
		}
		b, _ := json.Marshal(map[string]any{"text": text})
		body = b
	}
	code, up := s.protoCall(s.steerIntent, http.MethodPost,
		"/api/tasks/"+tid+"/intents/"+iid+"/steer",
		map[string]string{"id": tid, "iid": iid}, nil, body)
	protoWrite(w, code, "turn", "steer", thread, up)
}

// protoWorkControl pauses/resumes/cancels one work.
// Upstream: POST /api/tasks/{id}/intents/{iid}/control, payload passthrough.
func (s *Server) protoWorkControl(w http.ResponseWriter, r *http.Request) {
	thread := r.PathValue("id")
	tid, ok := protoThreadTaskID(thread)
	if !ok {
		writeErr(w, 400, "线程标识应为 task-<数字>")
		return
	}
	iid := r.PathValue("iid")
	if _, err := strconv.ParseInt(iid, 10, 64); err != nil {
		writeErr(w, 400, "iid 应为数字")
		return
	}
	code, up := s.protoCall(s.controlIntent, http.MethodPost,
		"/api/tasks/"+tid+"/intents/"+iid+"/control",
		map[string]string{"id": tid, "iid": iid}, nil, protoReadBody(r))
	protoWrite(w, code, "work", "control", thread, up)
}

// protoKill force-stops one running work (语义 = 引擎 kill_work，即规划者终止面)。
// body: {"intent_id":N}。与 control(cancel) 不同：kill 走 AbortKilledByPlanner
// 终止因，并联动方向门控（hard bump + 冻结派发），trace 记 kill.order。
func (s *Server) protoKill(w http.ResponseWriter, r *http.Request) {
	thread := r.PathValue("id")
	tid, ok := protoThreadTaskID(thread)
	if !ok {
		writeErr(w, 400, "线程标识应为 task-<数字>")
		return
	}
	var req struct {
		IntentID int64 `json:"intent_id"`
	}
	if err := json.Unmarshal(protoReadBody(r), &req); err != nil || req.IntentID <= 0 {
		writeErr(w, 400, "需要 intent_id")
		return
	}
	if s.engine.IsDeleting(tid) {
		writeErr(w, 409, "任务删除中，拒绝操作")
		return
	}
	if err := s.engine.KillWork(req.IntentID); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	protoWrite(w, 200, "work", "kill", thread,
		[]byte(`{"killed":true,"intent_id":`+strconv.FormatInt(req.IntentID, 10)+`}`))
}

// ---------- Item ----------

// protoItems returns the thread's items from both ledgers side by side:
// activity（领域活动账本，query 透传上游）+ trace（P0 语义转录，task 线程）。
func (s *Server) protoItems(w http.ResponseWriter, r *http.Request) {
	thread := r.PathValue("id")
	tid, ok := protoThreadTaskID(thread)
	if !ok {
		writeErr(w, 400, "线程标识应为 task-<数字>")
		return
	}
	q := r.URL.Query()
	code, up := s.protoCall(s.activity, http.MethodGet, "/api/exploration/activity", nil, q, nil)
	var activity any
	if len(up) > 0 {
		if err := json.Unmarshal(up, &activity); err != nil {
			activity = string(up)
		}
	}
	after, limit := protoPage(q)
	trace := map[string]any{"events": []any{}, "next": 0, "total": 0}
	if st := s.engine.TraceLog(); st != nil {
		if events, next, total, err := st.ReadEvents("task-"+tid, after, limit); err == nil {
			trace = map[string]any{"events": events, "next": next, "total": total}
		}
	}
	writeJSON(w, code, map[string]any{
		"protocol": protoVersion,
		"resource": "item",
		"id":       thread,
		"data":     map[string]any{"activity": activity, "trace": trace},
	})
}

// ---------- Approval ----------

// protoListApprovals lists pending approvals（= 拦截审批队列）。Upstream:
// GET /api/intercept/pending，query 透传。
func (s *Server) protoListApprovals(w http.ResponseWriter, r *http.Request) {
	code, up := s.protoCall(s.interceptListPending, http.MethodGet, "/api/intercept/pending", nil, r.URL.Query(), nil)
	protoWrite(w, code, "approval", "list", "", up)
}

// protoGetApproval returns one approval item. Upstream: GET /api/intercept/pending/{id}.
func (s *Server) protoGetApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	code, up := s.protoCall(s.interceptGetOne, http.MethodGet, "/api/intercept/pending/"+id,
		map[string]string{"id": id}, nil, nil)
	protoWrite(w, code, "approval", "get", id, up)
}

// protoDecideApproval decides one approval（批准/拒绝，payload 原样透传上游 DTO）。
// Upstream: POST /api/intercept/pending/{id}/decide.
func (s *Server) protoDecideApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	code, up := s.protoCall(s.interceptDecide, http.MethodPost, "/api/intercept/pending/"+id+"/decide",
		map[string]string{"id": id}, nil, protoReadBody(r))
	protoWrite(w, code, "approval", "decide", id, up)
}

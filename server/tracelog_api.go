// P0 会话语义转录的只读回放 API（纯新增路由，不碰任何写路径）：
//
//	GET /api/traces                — 线程列表（任务线/意图线/主 Agent 线）
//	GET /api/traces/{thread}       — 事件时间线（行偏移游标分页，支持回放）
package server

import (
	"net/http"
	"strconv"
)

// apiTraceList lists trace threads, newest activity first.
func (s *Server) apiTraceList(w http.ResponseWriter, r *http.Request) {
	st := s.engine.TraceLog()
	if st == nil {
		writeJSON(w, 200, map[string]any{"threads": []any{}})
		return
	}
	writeJSON(w, 200, map[string]any{"threads": st.ListThreads()})
}

// apiTraceEvents streams one thread's semantic events from ?after= (line
// offset) with ?limit= (default 500, max 5000). Replay = keep calling with
// the returned next cursor until next == total.
func (s *Server) apiTraceEvents(w http.ResponseWriter, r *http.Request) {
	st := s.engine.TraceLog()
	if st == nil {
		writeErr(w, 404, "转录未启用")
		return
	}
	thread := r.PathValue("thread")
	if thread == "" {
		writeErr(w, 400, "缺少线程标识")
		return
	}
	after := 0
	if v := r.URL.Query().Get("after"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			after = n
		}
	}
	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	events, next, total, err := st.ReadEvents(thread, after, limit)
	if err != nil {
		writeErr(w, 404, "转录线程不存在")
		return
	}
	writeJSON(w, 200, map[string]any{
		"thread": thread,
		"events": events,
		"next":   next,
		"total":  total,
	})
}

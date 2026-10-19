// P2 会话树/自省面：把既有检查点（可回滚时间线）、主 Agent 会话分段与 P0 语义
// 转录线程只读拼装为一棵会话树；外加 P1 hooks 插桩链自省。纯读侧聚合，不写任何
// 状态、不改变任何既有流程。
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// jsonRaw decodes an upstream JSON payload for re-enveloping (fallback: raw string).
func jsonRaw(b []byte) any {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b)
	}
	return v
}

// protoTree composes the session tree for one thread:
//
//	{
//	  "checkpoints":  <GET /api/tasks/{id}/checkpoints 上游响应>,
//	  "main_sessions": <GET /api/exploration/main-sessions 上游响应>,
//	  "trace_threads": [<task 线程 + 本任务 exp 相关意图/主 Agent 线程>]
//	}
//
// 回滚语义仍在既有端点：POST /api/tasks/{id}/checkpoints/{cid}/revert 与
// POST /api/tasks/{id}/messages/{seq}/rollback（树只负责“看见”）。
func (s *Server) protoTree(w http.ResponseWriter, r *http.Request) {
	thread := r.PathValue("id")
	tid, ok := protoThreadTaskID(thread)
	if !ok {
		writeErr(w, 400, "线程标识应为 task-<数字>")
		return
	}
	_, cp := s.protoCall(s.taskCheckpoints, "GET", "/api/tasks/"+tid+"/checkpoints",
		map[string]string{"id": tid}, nil, nil)
	_, ms := s.protoCall(s.mainSessions, "GET", "/api/exploration/main-sessions",
		nil, r.URL.Query(), nil)

	var checkpoints, mains any
	if len(cp) > 0 {
		checkpoints = jsonRaw(cp)
	}
	if len(ms) > 0 {
		mains = jsonRaw(ms)
	}

	// 任务相关 trace 线程：task-<id> + exp<ExpID>-worker-i* / exp<ExpID>-main*。
	prefixes := []string{"task-" + tid}
	s.m.mu.Lock()
	t := s.m.tasks[tid]
	s.m.mu.Unlock()
	if t != nil {
		exp := strconv.FormatInt(t.ExpID, 10)
		prefixes = append(prefixes, "exp"+exp+"-worker-", "exp"+exp+"-main")
	}
	threads := []any{}
	if st := s.engine.TraceLog(); st != nil {
		for _, th := range st.ListThreads() {
			for _, p := range prefixes {
				if strings.HasPrefix(th.Thread, p) {
					threads = append(threads, th)
					break
				}
			}
		}
	}

	writeJSON(w, 200, map[string]any{
		"protocol":      protoVersion,
		"resource":      "session_tree",
		"id":            thread,
		"data":          map[string]any{
			"checkpoints":   checkpoints,
			"main_sessions": mains,
			"trace_threads": threads,
		},
	})
}

// protoHooks lists the P1 hook-chain registrations (空链 = 未有任何扩展挂载)。
func (s *Server) protoHooks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"protocol": protoVersion,
		"resource": "hooks",
		"data":     map[string]any{"slots": listHookSlots()},
	})
}

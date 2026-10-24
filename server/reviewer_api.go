package server

// Reviewer 判决台账的只读接口。
//
// 前端任务面板要有一个「回顾者」分区，但判决一直只落库不给前端看：
// 用户在任务页看到的只有 主/规划/审计/Worker 四栏，Reviewer 判了什么完全不可见。
//
// 这里只读不写，也没有权限外的能力：拿到的是已经过 agent.SanitizeReviewerText
// 清洗的产物（raw_bytes 只是长度），原始报文从不进这张表。

import "net/http"

// reviewerVerdictLimit 是单次返回的判决条数上限。台账按 id 倒序，取最近的一批即可。
const reviewerVerdictLimit = 200

// taskReviewerVerdicts 返回本任务的 Reviewer 判决（GET /api/tasks/{id}/reviewer-verdicts）。
func (s *Server) taskReviewerVerdicts(w http.ResponseWriter, r *http.Request) {
	task, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	if s.m == nil || s.m.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"verdicts": []any{}})
		return
	}
	taskID, err := parseTaskID(task.ID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid task id")
		return
	}
	// 判决方法挂在 *db.ExplorationStore 上，但它们只看入参 taskID，
	// 不读 store 自身的 expID，因此这里传 0 与传任务 expID 等价。
	rows, err := s.m.pg.Exploration(0).ListReviewerVerdicts(taskID, reviewerVerdictLimit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verdicts": rows})
}

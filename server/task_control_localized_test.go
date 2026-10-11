package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 任务、意图控制 API 中文化回归测试，复用 assertChineseError、decodeErrorField。
// 固定十类错误；意图控制首先 GetNode，需要数据库，因此其中六类只检查常量。
// 任务控制与批量响应则通过下面的真实函数与 HTTP 测试检查。
func TestTaskControlErrorConstantsLocalized(t *testing.T) {
	cases := map[string]string{
		"task_deleting":          errTaskCtrlDeleting,
		"task_terminal_pause":    errTaskCtrlTerminalPause,
		"task_already_paused":    errTaskCtrlAlreadyPaused,
		"batch_size":             errTaskCtrlBatchSizeFmt,
		"intent_inherited":       errIntentCtrlInheritedReadonly,
		"intent_only_running":    errIntentCtrlOnlyRunningPause,
		"intent_only_paused":     errIntentCtrlOnlyPausedResume,
		"intent_state_conflict":  errIntentCtrlStateConflictFmt,
		"intent_only_deletable":  errIntentCtrlOnlyDeletable,
		"intent_reason_required": errIntentCtrlReasonRequired,
	}
	for label, msg := range cases {
		assertChineseError(t, label, msg)
	}
}

// TestTaskControlApplyResponsesLocalized 真实执行三条失败路径：终态、已暂停、删除屏障。
// 只读取任务映射与生命周期 Status、Paused，空 Engine 即可，无需数据库。
// 成功暂停需要 ApplyTaskPause 数据库操作，不在本测试范围。
func TestTaskControlApplyResponsesLocalized(t *testing.T) {
	// 终态任务：isTerminalStatus(done)。
	terminalServer := func() (*Server, *Task) {
		tk := &Task{ID: "1", Status: "done"}
		return &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}, tk
	}
	// 运行中的任务已暂停：lifecycle.Paused。
	pausedServer := func() (*Server, *Task) {
		tk := &Task{ID: "1", Status: "running", Paused: true}
		return &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}, tk
	}
	// 任务已建立删除屏障：IsDeleting。
	deletingServer := func() (*Server, *Task) {
		tk := &Task{ID: "1", Status: "running"}
		s := &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}
		s.engine.deleting.Store("1", true)
		return s, tk
	}

	cases := []struct {
		name  string
		setup func() (*Server, *Task)
		want  string
	}{
		{"terminal", terminalServer, errTaskCtrlTerminalPause},
		{"already-paused", pausedServer, errTaskCtrlAlreadyPaused},
		{"deleting", deletingServer, errTaskCtrlDeleting},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, tk := c.setup()
			_, err := s.applyTaskControl(tk, "pause")
			if err == nil {
				t.Fatalf("预期返回错误，实际为 nil")
			}
			if err.Error() != c.want {
				t.Fatalf("返回错误 = %q，预期 = %q", err.Error(), c.want)
			}
			assertChineseError(t, c.name, err.Error())
		})
	}
}

// TestControlTasksBatchResponsesLocalized 检查真实 HTTP 批量响应。
// 空 task_ids 在访问 s.m 前返回 400；终态任务的 batch pause 返回 200，
// 并在 items[].error 保留 applyTaskControl 的错误。
func TestControlTasksBatchResponsesLocalized(t *testing.T) {
	t.Run("size-error", func(t *testing.T) {
		s := &Server{}
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/control",
			strings.NewReader(`{"task_ids":[],"action":"pause"}`))
		rec := httptest.NewRecorder()
		s.controlTasksBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d，预期 = 400（正文 %q）", rec.Code, rec.Body.String())
		}
		got := decodeErrorField(t, rec.Body.Bytes())
		want := fmt.Sprintf(errTaskCtrlBatchSizeFmt, maxBatchControlIDs)
		if got != want {
			t.Fatalf("响应文案 = %q，预期 = %q", got, want)
		}
		if !strings.Contains(got, "100") {
			t.Fatalf("文案未包含数量上限 100：%q", got)
		}
		assertChineseError(t, "size-error", got)
	})

	t.Run("item-error-surfaces", func(t *testing.T) {
		tk := &Task{ID: "1", Status: "done"}
		s := &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/control",
			strings.NewReader(`{"task_ids":["1"],"action":"pause"}`))
		rec := httptest.NewRecorder()
		s.controlTasksBatch(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，预期 = 200（正文 %q）", rec.Code, rec.Body.String())
		}
		var out struct {
			Items []struct {
				ID    string `json:"id"`
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析响应 JSON 失败：%v（正文 %q）", err, rec.Body.String())
		}
		if len(out.Items) != 1 {
			t.Fatalf("items 数量 = %d，预期 = 1", len(out.Items))
		}
		if out.Items[0].OK || out.Items[0].Error != errTaskCtrlTerminalPause {
			t.Fatalf("items[0] = %+v，预期 error = %q", out.Items[0], errTaskCtrlTerminalPause)
		}
		assertChineseError(t, "item-error", out.Items[0].Error)
	})
}

// TestIntentStateConflictWrapsSentinel 验证中文状态冲突消息仍用 %w 包装
// db.ErrIntentStateConflict，保持 errors.Is 识别，paused 枚举也必须保留。
func TestIntentStateConflictWrapsSentinel(t *testing.T) {
	err := fmt.Errorf(errIntentCtrlStateConflictFmt, db.ErrIntentStateConflict)
	if !errors.Is(err, db.ErrIntentStateConflict) {
		t.Fatalf("errors.Is(.., ErrIntentStateConflict) = false，%%w 包装失效：%q", err.Error())
	}
	if !strings.Contains(err.Error(), "paused") {
		t.Fatalf("文案未保留枚举 paused：%q", err.Error())
	}
	assertChineseError(t, "state-conflict", err.Error())
}

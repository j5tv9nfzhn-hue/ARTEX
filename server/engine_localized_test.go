package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 任务控制、Worker 干预的中文错误回归测试，复用 assertChineseError。
// ControlWork 的四类错误由 applyIntentControl → controlIntent 返回 writeErr 409；
// runDetachedIntent 的两类错误经 sendWorkerMessage 返回，均为用户路径。
// SteerWork、KillWork 和 transitionIntentState 的模型结果、日志不在此测试范围。
// TestControlWorkNoRunningWorkErrorLocalized 用 NewEngine(nil) 检查 work 映射的 run==nil 分支，
// 不需要数据库；同时验证 %w 包装 errWorkControlConflict 的 errors.Is 关系。
func TestControlWorkNoRunningWorkErrorLocalized(t *testing.T) {
	e := NewEngine(nil)
	err := e.ControlWork(context.Background(), 42, "pause")
	if err == nil {
		t.Fatal("没有运行中的 work，ControlWork 却返回 nil")
	}
	if !errors.Is(err, errWorkControlConflict) {
		t.Fatalf("errors.Is(err, errWorkControlConflict) = false, err=%v", err)
	}
	assertChineseError(t, "control_work_no_running", err.Error())
}

// TestEngineUserFacingErrorsLocalized 固定依赖引擎、数据库或 goroutine 时序的其他模板。
// 用代表性参数填充格式字符串后检查中文消息。
func TestEngineUserFacingErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg string
	}{
		{"work_control_busy", fmt.Errorf(errWorkControlBusyFmt, errWorkControlConflict, int64(42), "pause").Error()},
		{"work_control_wait", fmt.Errorf(errWorkControlWaitFmt, int64(42), "pause", context.DeadlineExceeded).Error()},
		{"detached_worker_not_ready", errDetachedWorkerNotReady},
		{"detached_state_conflict", fmt.Errorf(errIntentCtrlStateConflictFmt, db.ErrIntentStateConflict).Error()},
	} {
		assertChineseError(t, c.label, c.msg)
	}
}

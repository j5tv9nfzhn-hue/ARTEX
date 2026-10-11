package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 自更新端点用户消息中文化回归测试。
// update-card 直接显示 progress.message、reason、writeErr，
// 复用同包 assertChineseError 和 decodeErrorField。

func TestUpdateMessageConstantsLocalized(t *testing.T) {
	assertChineseError(t, "updateMsgPreparing", updateMsgPreparing)
	assertChineseError(t, "updateMsgFailed", updateMsgFailed)
	assertChineseError(t, "updateMsgStaged", updateMsgStaged)
	assertChineseError(t, "updateErrInProgress", updateErrInProgress)
	assertChineseError(t, "updateErrRollbackInProgress", updateErrRollbackInProgress)

	// 填充 %q、%s 后检查模板，保留参数顺序并禁止韩文。
	notRelease := fmt.Sprintf(updateErrNotReleaseFmt, "v0.0.0-dev")
	assertChineseError(t, "updateErrNotReleaseFmt", notRelease)
	if !strings.Contains(notRelease, "v0.0.0-dev") {
		t.Fatalf("updateErrNotReleaseFmt：版本占位符未替换：%q", notRelease)
	}
	latest := fmt.Sprintf(updateErrAlreadyLatestFmt, "v1.2.3")
	assertChineseError(t, "updateErrAlreadyLatestFmt", latest)
	if !strings.Contains(latest, "v1.2.3") {
		t.Fatalf("updateErrAlreadyLatestFmt：版本占位符未替换：%q", latest)
	}
}

// 真实执行 begin、finish 检查 SSE 中文进度。
// 使用局部实例避免污染全局 updHub，无需数据库和网络。
func TestUpdateProgressMessagesLocalized(t *testing.T) {
	h := &updateHub{subs: map[chan updateProgress]struct{}{}}

	if !h.begin("v1.2.3") {
		t.Fatal("首次调用 begin 应为 true")
	}
	cur, running := h.snapshot()
	if !running {
		t.Fatal("begin 后应为 running")
	}
	if cur.Message != updateMsgPreparing {
		t.Fatalf("准备中文案不匹配：%q", cur.Message)
	}
	if h.begin("v1.2.4") {
		t.Fatal("进行中时 begin 应为 false")
	}

	h.finish(errors.New("下载失败"))
	cur, running = h.snapshot()
	if running {
		t.Fatal("finish 后应清除 running")
	}
	if cur.Message != updateMsgFailed {
		t.Fatalf("失败文案不匹配：%q", cur.Message)
	}

	h.finish(nil)
	cur, _ = h.snapshot()
	if cur.Message != updateMsgStaged {
		t.Fatalf("准备就绪文案不匹配：%q", cur.Message)
	}
}

// 通过真实 HTTP 检查更新进行中时 updateRollback 返回的 409 中文拒绝原因。
func TestUpdateRollbackInProgressLocalized(t *testing.T) {
	updHub.mu.Lock()
	prev := updHub.running
	updHub.running = true
	updHub.mu.Unlock()
	defer func() {
		updHub.mu.Lock()
		updHub.running = prev
		updHub.mu.Unlock()
	}()

	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/update/rollback", nil)
	rec := httptest.NewRecorder()
	s.updateRollback(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("预期状态码 409，实际 %d（正文 %s）", rec.Code, rec.Body.Bytes())
	}
	msg := decodeErrorField(t, rec.Body.Bytes())
	assertChineseError(t, "updateRollback 进行中 409", msg)
	if msg != updateErrRollbackInProgress {
		t.Fatalf("回滚拒绝文案不匹配：%q", msg)
	}
}

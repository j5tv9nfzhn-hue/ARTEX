package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 聊天附件上传 API 的中文化回归测试，复用 assertChineseError 与 decodeErrorField。
// TestChatUploadErrorConstantsLocalized 固定七类常量；三个失败前缀后接原始 err.Error()，
// 要求中文包装且无韩文残留，不修改底层错误。
func TestChatUploadErrorConstantsLocalized(t *testing.T) {
	cases := map[string]string{
		"scope_invalid": errChatUploadScopeInvalid,
		"bad_id":        errChatUploadBadID,
		"task_deleting": errChatUploadTaskDeleting,
		"mkdir":         errChatUploadMkdir,
		"parse":         errChatUploadParse,
		"no_file":       errChatUploadNoFile,
		"save_failed":   errChatUploadSaveFailed,
	}
	for label, msg := range cases {
		assertChineseError(t, label, msg)
	}
}

// TestChatUploadHandlerResponsesLocalized 检查不访问数据库的真实 HTTP 错误路径。
// scope、id 在 Manager 访问之前验证；scope=session 的 no-file 只需要临时目录。
// task-deleting 使用 engine.deleting 删除屏障返回 409。
func TestChatUploadHandlerResponsesLocalized(t *testing.T) {
	// multipart 正文有字段但没有 file：ParseMultipartForm 成功，文件数为 0。
	noFileBody := func() (*bytes.Buffer, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("other", "x")
		_ = mw.Close()
		return &buf, mw.FormDataContentType()
	}

	t.Run("scope-invalid", func(t *testing.T) {
		s := &Server{}
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=bogus&id=x", nil)
		rec := httptest.NewRecorder()
		s.chatUpload(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d，预期 = 400（正文 %q）", rec.Code, rec.Body.String())
		}
		if got := decodeErrorField(t, rec.Body.Bytes()); got != errChatUploadScopeInvalid {
			t.Fatalf("响应文案 = %q，预期 = %q", got, errChatUploadScopeInvalid)
		}
		assertChineseError(t, "scope-invalid", errChatUploadScopeInvalid)
	})

	t.Run("bad-id", func(t *testing.T) {
		s := &Server{}
		// scope=session，不是任务；在 Manager 访问前由 id 验证返回错误。
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=session&id=../evil", nil)
		rec := httptest.NewRecorder()
		s.chatUpload(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d，预期 = 400（正文 %q）", rec.Code, rec.Body.String())
		}
		if got := decodeErrorField(t, rec.Body.Bytes()); got != errChatUploadBadID {
			t.Fatalf("响应文案 = %q，预期 = %q", got, errChatUploadBadID)
		}
		assertChineseError(t, "bad-id", errChatUploadBadID)
	})

	t.Run("no-file", func(t *testing.T) {
		s := &Server{m: &Manager{dir: t.TempDir()}}
		body, ctype := noFileBody()
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=session&id=sess1", body)
		req.Header.Set("Content-Type", ctype)
		rec := httptest.NewRecorder()
		s.chatUpload(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d，预期 = 400（正文 %q）", rec.Code, rec.Body.String())
		}
		if got := decodeErrorField(t, rec.Body.Bytes()); got != errChatUploadNoFile {
			t.Fatalf("响应文案 = %q，预期 = %q", got, errChatUploadNoFile)
		}
		assertChineseError(t, "no-file", errChatUploadNoFile)
	})

	t.Run("task-deleting", func(t *testing.T) {
		s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
		s.engine.deleting.Store("t1", true)
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=task&id=t1", nil)
		rec := httptest.NewRecorder()
		s.chatUpload(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("状态码 = %d，预期 = 409（正文 %q）", rec.Code, rec.Body.String())
		}
		if got := decodeErrorField(t, rec.Body.Bytes()); got != errChatUploadTaskDeleting {
			t.Fatalf("响应文案 = %q，预期 = %q", got, errChatUploadTaskDeleting)
		}
		assertChineseError(t, "task-deleting", errChatUploadTaskDeleting)
	})
}

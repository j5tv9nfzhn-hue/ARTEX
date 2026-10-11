package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// TestTaskTemplateErrorConstantsLocalized 验证任务模板错误常量包含中文、不含韩文。
// 格式模板使用实际参数渲染后检查，确保占位符仍可正确使用。
func TestTaskTemplateErrorConstantsLocalized(t *testing.T) {
	assertChineseError(t, "request-too-large", errTaskTemplateRequestTooLarge)
	assertChineseError(t, "name-conflict", errTaskTemplateNameConflict)
	assertChineseError(t, "rule-invalid", errTaskTemplateRuleInvalid)
	assertChineseError(t, "no-fields", errTaskTemplateNoFields)
	assertChineseError(t, "field-too-long",
		fmt.Sprintf(errTaskTemplateFieldTooLongFmt, "name", db.MaxTaskTemplateNameRunes))
}

// TestTaskTemplateResponsesLocalized drives the real response paths that do not
// touch the database: the body decoder rejects an oversized request, the field
// validator rejects an over-length name, and writeTaskTemplateErr maps the
// name-conflict sentinel to the Simplified Chinese 409 body.
func TestTaskTemplateResponsesLocalized(t *testing.T) {
	// decodeTaskTemplateRequest — MaxBytesReader errors on an oversized body
	// before any DB access, so the 413 message is produced on its own.
	body := `{"name":"` + strings.Repeat("a", maxTaskTemplateRequestBytes+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/task-templates", strings.NewReader(body))
	rec := httptest.NewRecorder()
	var decoded taskTemplateRequest
	if _, ok := decodeTaskTemplateRequest(rec, req, &decoded); ok {
		t.Fatalf("too-large：解码不应通过 (status=%d)", rec.Code)
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too-large status=%d, want 413", rec.Code)
	}
	assertChineseError(t, "too-large", decodeErrorField(t, rec.Body.Bytes()))

	// validateTaskTemplateRequest — a name past the rune limit returns the
	// Simplified Chinese field-too-long error (surfaced via writeErr in the handlers).
	longName := strings.Repeat("中", db.MaxTaskTemplateNameRunes+1)
	err := validateTaskTemplateRequest(taskTemplateRequest{Name: &longName})
	if err == nil {
		t.Fatal("name-too-long：验证不应通过")
	}
	assertChineseError(t, "name-too-long", err.Error())

	// writeTaskTemplateErr — the name-conflict sentinel maps to the Simplified Chinese 409.
	rec = httptest.NewRecorder()
	writeTaskTemplateErr(rec, db.ErrTaskTemplateNameConflict)
	if rec.Code != http.StatusConflict {
		t.Fatalf("name-conflict status=%d, want 409", rec.Code)
	}
	assertChineseError(t, "name-conflict-response", decodeErrorField(t, rec.Body.Bytes()))
}

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 任务元数据 API 中文化回归测试，复用 assertChineseError。
// TestTaskMetadataErrorConstantsLocalized 检查四类错误常量。
func TestTaskMetadataErrorConstantsLocalized(t *testing.T) {
	cases := map[string]string{
		"errTaskMetaRequestTooLarge": errTaskMetaRequestTooLarge,
		"errTaskMetaNoFields":        errTaskMetaNoFields,
		"errTaskMetaNameEmpty":       errTaskMetaNameEmpty,
		"errTaskMetaNameTooLongFmt":  fmt.Sprintf(errTaskMetaNameTooLongFmt, maxTaskNameRunes),
	}
	for label, msg := range cases {
		assertChineseError(t, label, msg)
	}
}

// TestUpdateTaskMetadataResponsesLocalized 检查正文超限、缺少字段、名称空白、名称超长
// 四条真实 HTTP 路径；仅读任务映射和请求，在 UpdateTaskMetadata 数据库调用前返回。
func TestUpdateTaskMetadataResponsesLocalized(t *testing.T) {
	s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}}

	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/tasks/t1/metadata", strings.NewReader(body))
		req.SetPathValue("id", "t1")
		rec := httptest.NewRecorder()
		s.updateTaskMetadata(rec, req)
		return rec
	}
	errBody := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		var resp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析响应 JSON 失败：%v（正文 %q）", err, rec.Body.String())
		}
		return resp.Error
	}

	cases := []struct {
		name string
		body string
		code int
		want string
	}{
		// 超过 16KB 限制的有效 JSON，MaxBytesReader 在读取中返回超限。
		{"request_too_large", `{"name":"` + strings.Repeat("a", maxTaskMetadataRequestSize+1024) + `"}`, http.StatusRequestEntityTooLarge, errTaskMetaRequestTooLarge},
		// name、pinned 都缺失，没有可修改字段。
		{"no_fields", `{}`, http.StatusBadRequest, errTaskMetaNoFields},
		{"name_empty", `{"name":"  "}`, http.StatusBadRequest, errTaskMetaNameEmpty},
		{"name_too_long", `{"name":"` + strings.Repeat("中", maxTaskNameRunes+1) + `"}`, http.StatusBadRequest, fmt.Sprintf(errTaskMetaNameTooLongFmt, maxTaskNameRunes)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := call(c.body)
			if rec.Code != c.code {
				t.Fatalf("状态码 = %d，预期 = %d（正文 %q）", rec.Code, c.code, rec.Body.String())
			}
			if got := errBody(t, rec); got != c.want {
				t.Fatalf("响应文案 = %q，预期 = %q", got, c.want)
			}
			assertChineseError(t, c.name, errBody(t, rec))
		})
	}
}

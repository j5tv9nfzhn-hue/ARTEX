package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServerMgmtConstantsLocalized 验证管理接口消息使用中文。
// assertChineseError 要求含 Han、不含 Hangul；与同包响应解码 helper 复用。
func TestServerMgmtConstantsLocalized(t *testing.T) {
	cases := map[string]string{
		"errMgmtPGUnavailable":      errMgmtPGUnavailable,
		"errMgmtTaskIDInvalid":      errMgmtTaskIDInvalid,
		"errMgmtTaskDeleting":       errMgmtTaskDeleting,
		"errMgmtTaskHasAgents":      errMgmtTaskHasAgents,
		"errMgmtAgentKeyFormat":     errMgmtAgentKeyFormat,
		"errMgmtNameEmpty":          errMgmtNameEmpty,
		"errMgmtAgentKeyExists":     errMgmtAgentKeyExists,
		"errMgmtBuiltinNoEditMeta":  errMgmtBuiltinNoEditMeta,
		"errMgmtBuiltinNoDelete":    errMgmtBuiltinNoDelete,
		"errMgmtLLMProfileIDFormat": errMgmtLLMProfileIDFormat,
		"errMgmtLLMProfileInvalid":  errMgmtLLMProfileInvalid,
		"errMgmtNoBuiltinPrompt":    errMgmtNoBuiltinPrompt,
		"errMgmtToolNotFound":       errMgmtToolNotFound,
		"errMgmtNotBuiltinTool":     errMgmtNotBuiltinTool,
		"errMgmtMCPNotFound":        errMgmtMCPNotFound,
		"errMgmtToolDiscoverFail":   errMgmtToolDiscoverFail,
		"errMgmtSkillNameInvalid":   errMgmtSkillNameInvalid,
		"errMgmtSkillNoFile":        errMgmtSkillNoFile,
		"errMgmtSkillNoMDInZip":     errMgmtSkillNoMDInZip,
		"errMgmtSkillReadMDFail":    errMgmtSkillReadMDFail,
		"errMgmtSkillNamePre":       errMgmtSkillNamePre,
		"errMgmtSkillNamePost":      errMgmtSkillNamePost,
		"errMgmtSkillExistsPre":     errMgmtSkillExistsPre,
		"errMgmtSkillExistsPost":    errMgmtSkillExistsPost,
		"errMgmtSkillZipBadPath":    errMgmtSkillZipBadPath,
		"errMgmtSkillZipTooMany":    errMgmtSkillZipTooMany,
		"errMgmtSkillFileTooLarge":  errMgmtSkillFileTooLarge,
		"errMgmtSkillZipTooLarge":   errMgmtSkillZipTooLarge,
		"errMgmtSkillNoMDAfter":     errMgmtSkillNoMDAfter,
		"errMgmtSkillInstallFail":   errMgmtSkillInstallFail,
		"errMgmtLLMActiveDelete":    errMgmtLLMActiveDelete,
		"errMgmtLLMRefChanged":      errMgmtLLMRefChanged,
		"errMgmtLLMRefTimeout":      errMgmtLLMRefTimeout,
		"errMgmtNoAPIKey":           errMgmtNoAPIKey,
		"errMgmtBuildReqFail":       errMgmtBuildReqFail,
		"errMgmtReqFail":            errMgmtReqFail,
		"errMgmtParseRespFail":      errMgmtParseRespFail,
		"errMgmtNoModelList":        errMgmtNoModelList,
		"errMgmtTmplSyntax":         errMgmtTmplSyntax,
		"errMgmtTmplVarPre":         errMgmtTmplVarPre,
		"errMgmtTmplVarPost":        errMgmtTmplVarPost,
		// 使用真实格式化结果检查中文包装，保留格式参数与顺序。
		"errMgmtAPIReturned": fmt.Sprintf(errMgmtAPIReturned, 404, "detail"),
	}
	for label, msg := range cases {
		assertChineseError(t, label, msg)
	}
}

// TestServerMgmtAPIReturnedFormat guards that the model-list probe format string
// keeps its status-code and body placeholders (a %d→wrong-verb edit would drop
// them).
func TestServerMgmtAPIReturnedFormat(t *testing.T) {
	msg := fmt.Sprintf(errMgmtAPIReturned, 404, "boom-body")
	assertChineseError(t, "errMgmtAPIReturned", msg)
	if !strings.Contains(msg, "404") || !strings.Contains(msg, "boom-body") {
		t.Fatalf("格式字符串应保留状态码和正文：%q", msg)
	}
}

// TestServerMgmtSkillExistsSentinel 固定跨栈“已存在”标记，
// 前端 system/skills/page.tsx 的 includes 判断据此进入覆盖确认，
// 文案偏移会静默禁用覆盖流程。
func TestServerMgmtSkillExistsSentinel(t *testing.T) {
	msg := errMgmtSkillExistsPre + "my-skill" + errMgmtSkillExistsPost
	assertChineseError(t, "skillExists", msg)
	if !strings.Contains(msg, "已存在") {
		t.Fatalf("缺少前端同步标记“已存在”：%q", msg)
	}
	if !strings.Contains(msg, "my-skill") {
		t.Fatalf("消息应包含 Skill 名称：%q", msg)
	}
}

// TestServerMgmtPGGate503Localized exercises the real shared DB gate every
// management handler passes through: with no PostgreSQL handle, pg() writes 503
// and returns nil. The 503 body must be Simplified Chinese.
func TestServerMgmtPGGate503Localized(t *testing.T) {
	s := &Server{m: &Manager{}} // pg == nil
	rec := httptest.NewRecorder()
	if got := s.pg(rec); got != nil {
		t.Fatal("未配置数据库句柄时 pg() 应返回 nil")
	}
	if rec.Code != 503 {
		t.Fatalf("预期状态码 503，实际为 %d", rec.Code)
	}
	assertChineseError(t, "pg.503", decodeErrorField(t, rec.Body.Bytes()))
}

// TestServerMgmtDeleteTaskBadIDLocalized drives pgDeleteTask through its first
// guard (non-numeric id → 400) without any DB or engine.
func TestServerMgmtDeleteTaskBadIDLocalized(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/abc", nil)
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	s.pgDeleteTask(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("预期状态码 400，实际为 %d（正文 %s）", rec.Code, rec.Body.Bytes())
	}
	got := decodeErrorField(t, rec.Body.Bytes())
	assertChineseError(t, "deleteTask.badID", got)
	if got != errMgmtTaskIDInvalid {
		t.Fatalf("应与 errMgmtTaskIDInvalid 一致：%q", got)
	}
}

// TestServerMgmtListModelsNoKeyLocalized drives pgListModels with no api key and
// no profile id, so it returns {"ok":false,"error":...} before touching the DB.
func TestServerMgmtListModelsNoKeyLocalized(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/llm/models", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	s.pgListModels(rec, req)
	if rec.Code != 200 {
		t.Fatalf("预期状态码 200，实际为 %d", rec.Code)
	}
	got := decodeErrorField(t, rec.Body.Bytes())
	assertChineseError(t, "listModels.noKey", got)
	if got != errMgmtNoAPIKey {
		t.Fatalf("应与 errMgmtNoAPIKey 一致：%q", got)
	}
}

// TestServerMgmtValidateTemplateLocalized exercises the prompt-template editor
// validator (pure function, no DB): both the parse-error and the
// disallowed-variable branches must return Simplified Chinese.
func TestServerMgmtValidateTemplateLocalized(t *testing.T) {
	if msg := validateTemplate("{{", nil); msg == "" {
		t.Fatal("损坏的模板应返回错误文案")
	} else {
		assertChineseError(t, "validateTemplate.syntax", msg)
	}
	msg := validateTemplate("{{.Bogus}}", nil)
	if msg == "" {
		t.Fatal("允许列表之外的变量应返回错误文案")
	}
	assertChineseError(t, "validateTemplate.var", msg)
	if !strings.Contains(msg, "Bogus") {
		t.Fatalf("消息应包含变量名称：%q", msg)
	}
}

// TestServerMgmtGlobalPromptVarsLocalized pins the template-variable catalog help
// text (rendered in the prompt editor's variable palette) as Simplified Chinese.
func TestServerMgmtGlobalPromptVarsLocalized(t *testing.T) {
	if len(globalPromptVars) == 0 {
		t.Fatal("globalPromptVars 为空")
	}
	for _, v := range globalPromptVars {
		assertChineseError(t, "globalPromptVar."+v.Name, v.Description)
	}
}

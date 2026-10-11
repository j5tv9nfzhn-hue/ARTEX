package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 对话 API 中文错误回归测试，复用 assertChineseError。
// TestConversationErrorConstantsLocalized 固定十二类错误常量；处理器先经过数据库门禁，
// 因此无数据库时不直接调用，包含 %d 的模板通过真实参数格式化后检查。
func TestConversationErrorConstantsLocalized(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"request_too_large", convErrRequestTooLarge},
		{"agent_key_empty", convErrAgentKeyEmpty},
		{"agent_key_too_long", fmt.Sprintf(convErrAgentKeyTooLong, maxConversationAgentKeyRunes)},
		{"agent_not_found", convErrAgentNotFound},
		{"llm_profile", convErrLLMProfile},
		{"title_too_long", fmt.Sprintf(convErrTitleTooLong, maxConversationTitleRunes)},
		{"title_or_pinned", convErrTitleOrPinned},
		{"title_empty", convErrTitleEmpty},
		{"bad_conv_id", convErrBadConvID},
		{"ids_count", fmt.Sprintf(convErrIDsCount, maxConversationDeleteBatch)},
		{"message_empty", convErrMessageEmpty},
		{"busy", convErrBusy},
	}
	for _, c := range cases {
		assertChineseError(t, c.name, c.msg)
	}
}

// TestDecodeConversationRequestTooLargeLocalized 检查超长请求的 HTTP 413 中文正文。
// decodeConversationRequest 不经过 Server 数据库，可直接验证常量到响应的连线。
func TestDecodeConversationRequestTooLargeLocalized(t *testing.T) {
	// 超过 64KB 限制的有效 JSON；MaxBytesReader 在读取时返回超限。
	body := `{"title":"` + strings.Repeat("a", maxConversationRequestBytes+1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(body))
	rec := httptest.NewRecorder()
	var dst struct {
		Title string `json:"title"`
	}
	if decodeConversationRequest(rec, req, &dst) {
		t.Fatal("正文超出上限，decodeConversationRequest 却返回 true")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d，预期 = %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应 JSON 失败：%v（正文=%q）", err, rec.Body.String())
	}
	if resp.Error != convErrRequestTooLarge {
		t.Fatalf("响应 error = %q，预期 = %q", resp.Error, convErrRequestTooLarge)
	}
	assertChineseError(t, "decode.too_large.response", resp.Error)
}

// TestConversationDefaultTitlesLocalized 检查两个中文默认标题不同。
// convDefaultTitle 同时用于创建与自动标题标记，必须保持一致。
func TestConversationDefaultTitlesLocalized(t *testing.T) {
	assertChineseError(t, "default_title", convDefaultTitle)
	assertChineseError(t, "attachment_title", convAttachmentTitle)
	if convDefaultTitle == convAttachmentTitle {
		t.Fatal("默认标题与附件默认标题不应相同")
	}
}

// TestIsDefaultConversationTitle 固定自动标题判断：空标题、创建默认值可以自动生成，
// 用户自定义标题不能覆盖；共享同一常量避免默认值与比较条件偏移。
func TestIsDefaultConversationTitle(t *testing.T) {
	if !isDefaultConversationTitle("") {
		t.Fatal("空标题应自动生成标题")
	}
	if !isDefaultConversationTitle(convDefaultTitle) {
		t.Fatalf("默认值 %q 应自动生成标题", convDefaultTitle)
	}
	if isDefaultConversationTitle("用户自定义标题") {
		t.Fatal("用户自定义标题不应重新自动生成")
	}
}

// TestConversationRetestReasonsLocalized 检查三类复测终止原因使用中文，
// 它们写入 finding_retests.error 并直接显示于 item.error。
func TestConversationRetestReasonsLocalized(t *testing.T) {
	assertChineseError(t, "retest_failed_to_start", convRetestFailedToStart)
	assertChineseError(t, "retest_status_read_failed", convRetestStatusReadFailed)
	assertChineseError(t, "retest_stopped_or_closed", convRetestStoppedOrClosed)
}

// TestTranscriptErrorSummaryLocalized 固定活动错误包装：聊天轮次无 label，
// 任务主 Agent 使用“主 Agent”，外层使用中文格式，内部错误原文保留。
func TestTranscriptErrorSummaryLocalized(t *testing.T) {
	cases := []struct {
		name  string
		label string
		errm  string
		want  string
	}{
		{"chat_turn", "", "connection reset", "（错误：connection reset）"},
		{"main_agent", "主 Agent", "connection reset", "（主 Agent 错误：connection reset）"},
	}
	for _, c := range cases {
		got := transcriptErrorSummary(c.label, c.errm)
		if got != c.want {
			t.Fatalf("%s：transcriptErrorSummary = %q，预期 = %q", c.name, got, c.want)
		}
		if !strings.Contains(got, c.errm) {
			t.Fatalf("%s：未保留错误原文：%q", c.name, got)
		}
		// 验证中文括号包装；中文全角标点允许使用，不再套用旧的 ASCII 标点限制。
		if !strings.HasPrefix(got, "（") || !strings.HasSuffix(got, "）") {
			t.Fatalf("%s：标点格式不符：%q", c.name, got)
		}
		// 错误包装必须包含汉字、不含韩文；原始错误固定为 ASCII，不纳入包装语言断言。
		wrapper := strings.ReplaceAll(got, c.errm, "")
		assertChineseError(t, c.name+".wrapper", wrapper)
	}
}

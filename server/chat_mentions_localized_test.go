package server

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestChatMentionErrorsLocalized 检查 HTTP @提及错误使用中文且不含韩文。
// 协议标签、模型输入快照标题和截断标记不在此测试中改动。
func TestChatMentionErrorsLocalized(t *testing.T) {
	// Fixed user-facing literals.
	for label, msg := range map[string]string{
		"badID":       errChatMentionBadID,
		"tooMany":     errChatMentionTooMany,
		"badSearch":   errChatMentionBadSearch,
		"dataUnavail": errChatMentionDataUnavail,
		"tooLarge":    errChatMentionTooLarge,
	} {
		assertChineseError(t, label, msg)
	}

	// parseChatMentions — a non-positive id inside a valid wire token.
	if _, err := parseChatMentions("@[漏洞#0]"); err == nil {
		t.Fatal("无效引用 ID 不应通过验证")
	} else {
		assertChineseError(t, "parse.badID", err.Error())
	}

	// parseChatMentions — exceeding the per-message mention cap (11 distinct ids).
	var b strings.Builder
	for i := 1; i <= 11; i++ {
		fmt.Fprintf(&b, "@[漏洞#%d] ", i)
	}
	if _, err := parseChatMentions(b.String()); err == nil {
		t.Fatal("超出引用数量上限不应通过验证")
	} else {
		assertChineseError(t, "parse.tooMany", err.Error())
	}

	// composeChatMentionMessage — a mention is present but the database is nil.
	if _, err := composeChatMentionMessage(nil, "@[漏洞#1]"); err == nil {
		t.Fatal("无数据库时引用解析不应成功")
	} else {
		assertChineseError(t, "compose.dataUnavail", err.Error())
	}

	// searchChatMentions — invalid kind and over-long query are rejected before
	// the database handle is touched, so &Server{} is enough to reach the branch.
	for _, q := range []string{"kind=unsupported", "q=" + url.QueryEscape(strings.Repeat("字", 201))} {
		w := httptest.NewRecorder()
		(&Server{}).searchChatMentions(w, httptest.NewRequest("GET", "/api/chat/mentions?"+q, nil))
		if w.Code != 400 {
			t.Fatalf("预期验证失败 (400)，实际返回 %d (%s)", w.Code, q)
		}
		assertChineseError(t, "search."+q, decodeErrorField(t, w.Body.Bytes()))
	}

	// The display label map mirrors the UI mention kinds, and the not-found
	// template resolves to a fully Simplified Chinese message for a known kind.
	for _, kind := range []string{"finding", "asset", "company", "endpoint", "ip", "app", "root_domain", "subdomain", "service"} {
		if chatMentionKindLabel[kind] == "" {
			t.Fatalf("缺少类型标签：%s", kind)
		}
	}
	assertChineseError(t, "notFound", fmt.Sprintf(errChatMentionNotFoundFmt, chatMentionKindLabel["finding"], 7))
}

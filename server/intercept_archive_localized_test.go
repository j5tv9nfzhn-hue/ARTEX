package server

import (
	"net/url"
	"testing"
	"unicode"
)

// assertChineseError 要求消息非空、包含汉字且不含韩文。
// 协议字段名和枚举值等 ASCII 标识保留原样。
func assertChineseError(t *testing.T, label, msg string) {
	t.Helper()
	if msg == "" {
		t.Fatalf("%s：消息为空", label)
	}
	hasHan := false
	for _, r := range msg {
		if unicode.Is(unicode.Hangul, r) {
			t.Fatalf("%s：仍有韩文残留：%q", label, msg)
		}
		if unicode.Is(unicode.Han, r) {
			hasHan = true
		}
	}
	if !hasHan {
		t.Fatalf("%s：未包含中文：%q", label, msg)
	}
}

// TestInterceptArchiveErrorsLocalized guards the F3 first bundle: every
// user-facing error response produced by the pure validators in intercept.go and
// task_archives.go must be Simplified Chinese (Han present, no Hangul). If anyone
// reverts one of these literals to Korean, this test fails.
func TestInterceptArchiveErrorsLocalized(t *testing.T) {
	// intercept.go: interceptFilterParams — query-string validation.
	if _, err := interceptFilterParams(url.Values{"status": {"bogus"}}); err == nil {
		t.Fatal("status 验证不应通过")
	} else {
		assertChineseError(t, "filter.status", err.Error())
	}
	if _, err := interceptFilterParams(url.Values{"decision_source": {"bogus"}}); err == nil {
		t.Fatal("decision_source 验证不应通过")
	} else {
		assertChineseError(t, "filter.decision_source", err.Error())
	}

	// intercept.go: validateInterceptRuleReq — each branch isolated by making the
	// earlier fields valid.
	ruleCases := []struct {
		label string
		req   interceptRuleReq
	}{
		{"rule.name", interceptRuleReq{Name: ""}},
		{"rule.match_target", interceptRuleReq{Name: "r", MatchTarget: "bad"}},
		{"rule.match_type", interceptRuleReq{Name: "r", MatchTarget: "tool_name", MatchType: "bad"}},
		{"rule.pattern_empty", interceptRuleReq{Name: "r", MatchTarget: "tool_name", MatchType: "string", Pattern: ""}},
		{"rule.action", interceptRuleReq{Name: "r", MatchTarget: "tool_name", MatchType: "string", Pattern: "p", Action: "bad"}},
		{"rule.regex", interceptRuleReq{Name: "r", MatchTarget: "tool_name", MatchType: "regex", Pattern: "(", Action: "allow"}},
	}
	for _, tc := range ruleCases {
		err := validateInterceptRuleReq(tc.req)
		if err == nil {
			t.Fatalf("%s：验证不应通过", tc.label)
		}
		assertChineseError(t, tc.label, err.Error())
	}

	// task_archives.go: normalizeArchiveIDs — batch id validation.
	if _, err := normalizeArchiveIDs(nil); err == nil {
		t.Fatal("空 archive_ids 验证不应通过")
	} else {
		assertChineseError(t, "archive_ids.empty", err.Error())
	}
	tooMany := make([]int64, 101)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	if _, err := normalizeArchiveIDs(tooMany); err == nil {
		t.Fatal("101 个 archive_ids 验证不应通过")
	} else {
		assertChineseError(t, "archive_ids.too_many", err.Error())
	}
	if _, err := normalizeArchiveIDs([]int64{0}); err == nil {
		t.Fatal("archive id 为 0 不应通过验证")
	} else {
		assertChineseError(t, "archive_ids.nonpositive", err.Error())
	}

	// task_archives.go: validateArchivePath — package path validation.
	if err := validateArchivePath("/tmp/artex-data", ""); err == nil {
		t.Fatal("空归档路径验证不应通过")
	} else {
		assertChineseError(t, "archive_path.empty", err.Error())
	}
	if err := validateArchivePath("/tmp/artex-data", "/etc/passwd"); err == nil {
		t.Fatal("管理目录之外的路径验证不应通过")
	} else {
		assertChineseError(t, "archive_path.outside", err.Error())
	}
}

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestSyncScopeSentryConstantsLocalized 验证同步消息常量使用中文。
// HTTP 错误与结果 warnings/errors 数组均不得残留韩文。
func TestSyncScopeSentryConstantsLocalized(t *testing.T) {
	cases := []struct {
		label string
		msg   string
	}{
		{"errSSDataSourceMissingFmt", errSSDataSourceMissingFmt},
		{"errSSDataSourceNoURLFmt", errSSDataSourceNoURLFmt},
		{"errSSListProjectsPrefix", errSSListProjectsPrefix},
		{"errSSParseProjectsPrefix", errSSParseProjectsPrefix},
		{"errSSListTasksPrefix", errSSListTasksPrefix},
		{"errSSParseTasksPrefix", errSSParseTasksPrefix},
		{"errSSDimension", errSSDimension},
		{"errSSTargetsEmpty", errSSTargetsEmpty},
		{"warnSSProjectMetaFmt", warnSSProjectMetaFmt},
		{"warnSSCompanyCreateFmt", warnSSCompanyCreateFmt},
		{"warnSSUnknownAssetPrefix", warnSSUnknownAssetPrefix},
		{"errSSFetchFmt", errSSFetchFmt},
		{"warnSSTruncatedFmt", warnSSTruncatedFmt},
		{"errSSParseSubdomainPrefix", errSSParseSubdomainPrefix},
		{"errSSParseAppPrefix", errSSParseAppPrefix},
		{"errSSParseServicePrefix", errSSParseServicePrefix},
	}
	for _, c := range cases {
		assertChineseError(t, c.label, c.msg)
	}

	// dimension/targets validation keep their JSON field names and enum values.
	if !strings.Contains(errSSDimension, "project") || !strings.Contains(errSSDimension, "task") {
		t.Fatalf("errSSDimension 丢失枚举值：%q", errSSDimension)
	}
}

// TestSyncScopeSentryFormattedLocalized exercises the exact Sprintf/Errorf format
// strings used by the sync handler so a verb mismatch or a reverted literal is
// caught by running the real format operation (not just inspecting the constant).
func TestSyncScopeSentryFormattedLocalized(t *testing.T) {
	ds := fmt.Errorf(errSSDataSourceMissingFmt, scopeSentryMCPName).Error()
	assertChineseError(t, "scopeSentryClient/missing", ds)
	if !strings.Contains(ds, "ScopeSentry") {
		t.Fatalf("数据源名称丢失：%q", ds)
	}

	noURL := fmt.Errorf(errSSDataSourceNoURLFmt, scopeSentryMCPName).Error()
	assertChineseError(t, "scopeSentryClient/noURL", noURL)
	if !strings.Contains(noURL, "URL") {
		t.Fatalf("URL 标识丢失：%q", noURL)
	}

	meta := fmt.Sprintf(warnSSProjectMetaFmt, "proj-1", errors.New("boom"))
	assertChineseError(t, "warnSSProjectMetaFmt", meta)
	if !strings.Contains(meta, "proj-1") || !strings.Contains(meta, "boom") {
		t.Fatalf("项目或源错误未替换：%q", meta)
	}

	company := fmt.Sprintf(warnSSCompanyCreateFmt, "ACME", errors.New("boom"))
	assertChineseError(t, "warnSSCompanyCreateFmt", company)
	if !strings.Contains(company, "ACME") {
		t.Fatalf("公司名称未替换：%q", company)
	}

	fetch := fmt.Sprintf(errSSFetchFmt, "subdomain", "t1", errors.New("boom"))
	assertChineseError(t, "errSSFetchFmt", fetch)
	if !strings.Contains(fetch, "subdomain") || !strings.Contains(fetch, "t1") {
		t.Fatalf("资产类型或目标未替换：%q", fetch)
	}

	truncated := fmt.Sprintf(warnSSTruncatedFmt, "app", "t1", syncMaxPerType)
	assertChineseError(t, "warnSSTruncatedFmt", truncated)
	if !strings.Contains(truncated, "5000") {
		t.Fatalf("数量上限未替换：%q", truncated)
	}
}

// TestSyncScopeSentryIngestParseErrorsLocalized runs ssIngest on malformed JSON for
// each asset kind. The unmarshal failure returns before any store access, so this is
// a real code path (nil AssetStore never dereferenced) that pins the parse-error
// strings to Simplified Chinese.
func TestSyncScopeSentryIngestParseErrorsLocalized(t *testing.T) {
	s := &Server{}
	bad := json.RawMessage("not json")
	for _, kind := range []string{"subdomain", "app", "service"} {
		got := s.ssIngest(nil, kind, bad, map[string]int{})
		if got == "" {
			t.Fatalf("%s：JSON 损坏，但错误字符串为空", kind)
		}
		assertChineseError(t, "ssIngest/"+kind, got)
		if !strings.Contains(got, kind) {
			t.Fatalf("%s：资产类型前缀丢失：%q", kind, got)
		}
	}
}

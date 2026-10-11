package selfupdate

// 自更新用户可见进度与错误的中文化回归测试。
// updateHub 将 Stage 回调写入 SSE progress.message，并将 Stage、FetchLatest、
// Rollback 错误显示为 progress.error、writeErr 正文或 boot_notice。
// 因此更新卡片所显示的文本必须包含汉字且不含韩文；底层诊断原文保留。

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func hasHangul(s string) bool {
	for _, r := range s {
		if r >= 0xAC00 && r <= 0xD7A3 {
			return true
		}
	}
	return false
}

func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// assertChineseError 要求错误存在、包含汉字且没有韩文。
// 通过 %w 包装的标准库英文错误允许保留原文。
func assertChineseError(t *testing.T, label string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s：预期返回错误，实际为 nil", label)
	}
	msg := err.Error()
	if !hasHan(msg) {
		t.Errorf("%s：未包含中文：%q", label, msg)
	}
	if hasHangul(msg) {
		t.Errorf("%s：仍有韩文残留：%q", label, msg)
	}
}

// rtFunc 向 http.Client 注入模拟响应，不实际连接 GitHub。
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stubClient(status int, body string) *http.Client {
	return &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
}

// TestCheckURLErrorsLocalized 在现有拒绝非 GitHub URL 测试之外，
// 检查两条拒绝路径的错误消息使用中文。
func TestCheckURLErrorsLocalized(t *testing.T) {
	nonHTTPS := checkURL(mustParse(t, "http://api.github.com/x"))
	assertChineseError(t, "non-https", nonHTTPS)
	if !strings.Contains(nonHTTPS.Error(), "HTTPS") {
		t.Errorf("non-https 错误缺少 HTTPS 标识：%q", nonHTTPS.Error())
	}

	nonGitHub := checkURL(mustParse(t, "https://evil.example.com/x"))
	assertChineseError(t, "non-github-host", nonGitHub)
	if !strings.Contains(nonGitHub.Error(), "GitHub") {
		t.Errorf("non-github 错误缺少 GitHub 标识：%q", nonGitHub.Error())
	}
}

// TestFetchLatestErrorsLocalized 检查限流、未发布、状态异常、解析失败和缺失 tag 的中文错误；
// 使用模拟 transport，不发送网络请求。
func TestFetchLatestErrorsLocalized(t *testing.T) {
	ctx := context.Background()

	_, errRate := FetchLatest(ctx, stubClient(http.StatusForbidden, ""))
	assertChineseError(t, "rate-limited", errRate)
	if !strings.Contains(errRate.Error(), "60") {
		t.Errorf("限流错误缺少 60 标识：%q", errRate.Error())
	}

	_, errNF := FetchLatest(ctx, stubClient(http.StatusNotFound, ""))
	assertChineseError(t, "not-found", errNF)

	_, errStatus := FetchLatest(ctx, stubClient(http.StatusInternalServerError, ""))
	assertChineseError(t, "bad-status", errStatus)
	if !strings.Contains(errStatus.Error(), "GitHub") {
		t.Errorf("状态错误缺少 GitHub 标识：%q", errStatus.Error())
	}

	_, errJSON := FetchLatest(ctx, stubClient(http.StatusOK, "{not json"))
	assertChineseError(t, "bad-json", errJSON)

	_, errTag := FetchLatest(ctx, stubClient(http.StatusOK, "{}"))
	assertChineseError(t, "missing-tag", errTag)
	if !strings.Contains(errTag.Error(), "tag") {
		t.Errorf("缺少 tag 的错误未包含 tag 标识：%q", errTag.Error())
	}
}

// TestStageMissingAssetLocalized 验证发布包中缺少当前平台资源时，Stage 在下载前返回中文错误。
// checkWritable 可能先失败，但也必须是中文；本测试无网络请求。
func TestStageMissingAssetLocalized(t *testing.T) {
	rel := &Release{TagName: "v9.9.9"} // Assets 为空。
	err := Stage(context.Background(), &http.Client{}, rel, "0.0.1", nil)
	assertChineseError(t, "stage-missing-asset", err)
}

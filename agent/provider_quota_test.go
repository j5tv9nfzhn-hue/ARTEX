package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestIsQuotaExhaustedMessage(t *testing.T) {
	t.Parallel()
	positive := []string{
		`{"error":{"code":"insufficient_quota"}}`,
		`RESOURCE_EXHAUSTED`,
		`You exceeded your current quota, please check your plan and billing details.`,
		`billing_not_active`,
		`credit balance is too low`,
		`账户余额不足，请充值`,
	}
	for _, message := range positive {
		if !IsQuotaExhaustedMessage(message) {
			t.Errorf("expected quota classification for %q", message)
		}
	}

	negative := []string{
		`status 429: rate limit exceeded`,
		`RESOURCE_EXHAUSTED: rate limit exceeded`,
		`too many requests per minute`,
		`quota exceeded for quota metric GenerateRequestsPerMinutePerProjectPerBaseModel`,
		`RESOURCE_EXHAUSTED: TPM quota exceeded`,
		`tokens per minute quota exceeded`,
		`rate_limit_exceeded: requests per second`,
		`status 401: invalid api key`,
		`status 401: insufficient_quota`,
		`HTTP 403: billing_hard_limit_reached`,
		`status 500: internal server error`,
		`status=503: insufficient_quota`,
		`context length exceeded`,
	}
	for _, message := range negative {
		if IsQuotaExhaustedMessage(message) {
			t.Errorf("unexpected quota classification for %q", message)
		}
	}
}

func TestQuotaAwareTransportOnlyNormalizesExplicitQuota429(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
	}{
		{name: "quota", status: http.StatusTooManyRequests, body: `{"code":"insufficient_quota"}`, wantStatus: http.StatusPaymentRequired},
		{name: "ordinary rate limit", status: http.StatusTooManyRequests, body: `{"message":"rate limit exceeded"}`, wantStatus: http.StatusTooManyRequests},
		{name: "server error", status: http.StatusInternalServerError, body: `insufficient_quota`, wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := quotaAwareTransport{base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tt.status,
					Status:     http.StatusText(tt.status),
					Body:       io.NopCloser(strings.NewReader(tt.body)),
					Header:     make(http.Header),
				}, nil
			})}
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status=%d, want %d", resp.StatusCode, tt.wantStatus)
			}
			gotBody, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotBody) != tt.body {
				t.Fatalf("body=%q, want %q", gotBody, tt.body)
			}
		})
	}
}

// 429 正文必须带上限读取：异常网关可以用一个任意大的错误正文作答，而原先无界的
// io.ReadAll 会把它整个读进内存。超大正文只应被截断，额度判定与 402 归一化不变。
func TestQuotaAwareTransportBounds429BodyRead(t *testing.T) {
	t.Parallel()
	// 真实额度错误把标记放在正文开头，后面跟大量填充——填充远超上限。
	huge := `{"error":{"code":"insufficient_quota"}}` + strings.Repeat("x", 4<<20)
	transport := quotaAwareTransport{base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     http.StatusText(http.StatusTooManyRequests),
			Body:       io.NopCloser(strings.NewReader(huge)),
			Header:     make(http.Header),
		}, nil
	})}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// 标记在受限读取范围内 → 仍归一化为 402。
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status=%d, want 402 (bounded read must still detect quota)", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxErrorBodyBytes {
		t.Fatalf("buffered %d bytes, want exactly the %d-byte cap", len(got), maxErrorBodyBytes)
	}
	if !strings.HasPrefix(string(got), `{"error":{"code":"insufficient_quota"}}`) {
		t.Fatalf("truncation lost the head of the body: %q", string(got[:64]))
	}
	// 替换后的正文与其声明长度必须自洽，避免下游按 ContentLength 读到一半挂住。
	if resp.ContentLength != int64(len(got)) {
		t.Fatalf("ContentLength=%d, want %d", resp.ContentLength, len(got))
	}
}

// 客户端必须有响应头等待上限，且【不能】有整次交换超时——后者会把长流式回复截断。
func TestQuotaAwareHTTPClientBoundsHeaderWaitButNotWholeExchange(t *testing.T) {
	t.Parallel()
	client, err := quotaAwareHTTPClient("", "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if client.Timeout != 0 {
		t.Fatalf("client.Timeout=%v；它会截断 SSE 流式回复，必须保持未设置", client.Timeout)
	}
	qt, ok := client.Transport.(quotaAwareTransport)
	if !ok {
		t.Fatalf("transport=%T, want quotaAwareTransport", client.Transport)
	}
	base, ok := qt.base.(*http.Transport)
	if !ok {
		t.Fatalf("base transport=%T, want *http.Transport", qt.base)
	}
	if base.ResponseHeaderTimeout != responseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout=%v, want %v", base.ResponseHeaderTimeout, responseHeaderTimeout)
	}
	if responseHeaderTimeout <= 0 {
		t.Fatal("responseHeaderTimeout 必须为正，否则等于没有上限")
	}
	// 建连超时仍由 DefaultTransport 提供（确认没有被 clone 掉）。
	if base.DialContext == nil {
		t.Fatal("DialContext 为空：建连阶段失去了超时")
	}
}

func TestProviderDoesNotRetryExplicitQuota429(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`)
	}))
	defer upstream.Close()

	provider, err := ConfigFrom("openai", "test-model", upstream.URL, "test-key", "").NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	var streamErr error
	for _, err := range provider.Stream(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{llm.UserText("ping")},
	}) {
		if err != nil {
			streamErr = err
		}
	}
	if streamErr == nil || !IsQuotaExhaustedMessage(streamErr.Error()) {
		t.Fatalf("expected explicit quota error, got %v", streamErr)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("explicit quota request retried %d times, want exactly one request", got)
	}
}

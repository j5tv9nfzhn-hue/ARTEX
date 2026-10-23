package llmtest

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// probeRecorder 记录一次测试服务器收到的请求。handler 与测试主体在不同
// goroutine，用互斥量建立同步。
type probeRecorder struct {
	mu     sync.Mutex
	hits   int
	path   string
	header http.Header
	body   []byte
}

func (p *probeRecorder) capture(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hits++
	p.path = r.URL.Path
	p.header = r.Header.Clone()
	p.body = body
}

func (p *probeRecorder) snapshot() (int, string, http.Header, []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits, p.path, p.header, p.body
}

// completionJSON 返回各协议的合法补全响应体（model 与请求不同，用于验证回显）。
func completionJSON(f Format) string {
	switch f {
	case FormatOpenAI:
		return `{"id":"c1","object":"chat.completion","model":"echo-model","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`
	case FormatOpenAIResponses:
		return `{"id":"r1","object":"response","status":"completed","model":"echo-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"pong"}]}]}`
	default:
		return `{"id":"m1","type":"message","role":"assistant","model":"echo-model","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn"}`
	}
}

// TestProbeRequestIsMinimalPerFormat 固化 #61–63 与 #67–73 的回归断言：
//   - 探测恰好发一个请求，路径与协议匹配；
//   - 请求体不含 tools、tool_choice、system、stream（及其它执行栈字段），
//     且只含预期的最小字段；
//   - 认证头按协议发送；配置了会话头名时带上一次性会话头；
//   - 成功判据（#74）成立时返回回显 model 与回复文本。
func TestProbeRequestIsMinimalPerFormat(t *testing.T) {
	cases := []struct {
		name     string
		format   Format
		wantPath string
	}{
		{"openai", FormatOpenAI, "/chat/completions"},
		{"anthropic", FormatAnthropic, "/v1/messages"},
		{"openai-responses", FormatOpenAIResponses, "/responses"},
	}
	// 独立探测允许出现的字段上限：model、一条 user 消息、该协议必填的
	// max_tokens、以及该 profile 显式配置的思考参数。多出来的都应被挡下。
	allowed := map[string]bool{
		"model": true, "messages": true, "input": true, "max_tokens": true,
		"thinking": true, "reasoning_effort": true, "output_config": true,
		"reasoning": true, "store": true,
	}
	forbidden := []string{
		"tools", "tool_choice", "system", "stream", "stream_options",
		"instructions", "previous_response_id", "conversation", "response_format",
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &probeRecorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.capture(r)
				w.Header().Set("content-type", "application/json")
				_, _ = io.WriteString(w, completionJSON(c.format))
			}))
			defer srv.Close()

			res, err := Probe(context.Background(), Config{
				Format:           c.format,
				BaseURL:          srv.URL,
				APIKey:           "sk-test",
				Model:            "probe-model",
				ThinkingType:     "enabled",
				ReasoningEffort:  "low",
				SessionHeaderKey: "x-session-id",
			})
			if err != nil {
				t.Fatalf("探测应成功：%v", err)
			}
			hits, gotPath, header, body := rec.snapshot()
			if hits != 1 {
				t.Fatalf("探测应恰好发出 1 个请求（不重试、不拉模型列表），实际 %d", hits)
			}
			if gotPath != c.wantPath {
				t.Fatalf("请求路径 = %q，预期 = %q", gotPath, c.wantPath)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatalf("请求体不是 JSON：%v（%s）", err, body)
			}
			compact := strings.ReplaceAll(string(body), " ", "")
			for _, key := range forbidden {
				if _, ok := fields[key]; ok {
					t.Fatalf("探测请求不得携带 %q：%s", key, body)
				}
				if strings.Contains(compact, `"`+key+`"`) {
					t.Fatalf("探测请求不得出现 %q：%s", key, body)
				}
			}
			for key := range fields {
				if !allowed[key] {
					t.Fatalf("探测请求出现未预期字段 %q：%s", key, body)
				}
			}
			if !strings.Contains(compact, `"role":"user"`) || !strings.Contains(compact, "ping") {
				t.Fatalf("探测请求应只带一条 user 消息：%s", body)
			}
			switch c.format {
			case FormatAnthropic:
				if header.Get("x-api-key") != "sk-test" {
					t.Fatalf("anthropic 协议应带 x-api-key：%v", header)
				}
				if header.Get("anthropic-version") == "" {
					t.Fatalf("anthropic 协议应带 anthropic-version：%v", header)
				}
			default:
				if header.Get("authorization") != "Bearer sk-test" {
					t.Fatalf("应带 Bearer 认证头：%v", header)
				}
			}
			if got := header.Get("x-session-id"); !strings.HasPrefix(got, "conntest-") {
				t.Fatalf("配置了会话头名时应带一次性会话 id，实际 = %q", got)
			}
			if res.Model != "echo-model" {
				t.Fatalf("应回显供应商返回的 model，实际 = %q", res.Model)
			}
			if res.Reply != "pong" {
				t.Fatalf("回复 = %q，预期 = pong", res.Reply)
			}
			if res.Status != http.StatusOK {
				t.Fatalf("状态码 = %d，预期 = 200", res.Status)
			}
			if res.Latency <= 0 {
				t.Fatalf("应记录耗时，实际 = %v", res.Latency)
			}
		})
	}
}

// TestProbeHTTPErrorPassesThrough 固化 #41–42：非 2xx 诊断必须包含 HTTP 状态码
// 与供应商错误原文，不做吞并或改写。
func TestProbeHTTPErrorPassesThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	_, err := Probe(context.Background(), Config{
		Format: FormatOpenAI, BaseURL: srv.URL, APIKey: "bad", Model: "m",
	})
	if err == nil {
		t.Fatal("401 应判定为失败")
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("错误类型应为 *Error，实际 %T", err)
	}
	if pe.Stage != StageHTTP || pe.Status != http.StatusUnauthorized {
		t.Fatalf("Stage/Status = %s/%d，预期 http/401", pe.Stage, pe.Status)
	}
	msg := err.Error()
	for _, want := range []string{"401", "Incorrect API key provided", "invalid_request_error"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("诊断应包含 %q，实际 = %s", want, msg)
		}
	}
}

// TestProbeSuccessCriteria 固化 #74 的判据：2xx + 合法 JSON + 该协议的补全结构
// + 至少一段内容；缺一不可，且诊断可读。
func TestProbeSuccessCriteria(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"html-not-json", `<html>502 Bad Gateway</html>`, "无法解析响应 JSON"},
		{"json-missing-choices", `{"ok":true}`, "缺少合法补全结构"},
		{"error-object-with-200", `{"error":{"message":"quota exceeded"}}`, "quota exceeded"},
		{"empty-completion", `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`, "无回复内容"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", "application/json")
				_, _ = io.WriteString(w, c.body)
			}))
			defer srv.Close()

			_, err := Probe(context.Background(), Config{
				Format: FormatOpenAI, BaseURL: srv.URL, APIKey: "k", Model: "m",
			})
			if err == nil {
				t.Fatalf("响应 %s 应判定为失败", c.body)
			}
			var pe *Error
			if !errors.As(err, &pe) || pe.Stage != StageProtocol || pe.Status != http.StatusOK {
				t.Fatalf("应为 protocol/200 诊断，实际 = %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("诊断应包含 %q，实际 = %s", c.want, err)
			}
		})
	}
}

// TestProbeTimeout 固化 #74 的超时语义：探测受 Timeout 约束，超时返回可读诊断。
func TestProbeTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	start := time.Now()
	_, err := Probe(context.Background(), Config{
		Format: FormatOpenAI, BaseURL: srv.URL, APIKey: "k", Model: "m",
		Timeout: 150 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("超时应判定为失败")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("诊断应说明超时：%v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("探测应在超时上限附近返回，实际耗时 %v", elapsed)
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Stage != StageTransport {
		t.Fatalf("超时应归类为 transport，实际 = %v", err)
	}
}

// TestProbeDefaultTimeoutPinned 固化默认超时 = 60s。
//
// 这条是「连接测试别再把慢模型误判成坏配置」的回归钉：带推理的模型单发补全
// 20–50s 是常态，上限回落到 10s 会让好配置直接被判死。
func TestProbeDefaultTimeoutPinned(t *testing.T) {
	if DefaultTimeout != 60*time.Second {
		t.Fatalf("默认超时 = %v，预期 60s", DefaultTimeout)
	}
}

// TestProbeRejectsBadConfig 校验前置校验：缺 Key、代理地址不合法都在发包前失败。
func TestProbeRejectsBadConfig(t *testing.T) {
	if _, err := Probe(context.Background(), Config{Format: FormatOpenAI, Model: "m"}); err == nil ||
		!strings.Contains(err.Error(), "未提供 API Key") {
		t.Fatalf("缺 API Key 应返回明确诊断，实际 = %v", err)
	}
	if _, err := Probe(context.Background(), Config{
		Format: FormatOpenAI, APIKey: "k", Model: "m", Proxy: "127.0.0.1:8080",
	}); err == nil || !strings.Contains(err.Error(), "代理地址缺少协议") {
		t.Fatalf("缺协议头的代理应返回明确诊断，实际 = %v", err)
	}
}

// TestProbeSourceDependsOnStdlibOnly 机械固化 #64–66：测试连接探测必须与
// Agent Engine、工具注册表、双图数据库、压缩机制、录制器彻底解耦。做法是
// 解析本包所有非测试源文件的 import，要求全部来自 Go 标准库（路径首段不含
// "."，与 goimports 的判定一致）。
func TestProbeSourceDependsOnStdlibOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败：%v", name, err)
		}
		for _, imp := range file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s 的 import %s 不合法：%v", name, imp.Path.Value, err)
			}
			head := p
			if i := strings.Index(p, "/"); i >= 0 {
				head = p[:i]
			}
			if strings.Contains(head, ".") {
				t.Fatalf("%s 依赖了非标准库包 %q：探测路径不得依赖 Agent Engine/工具注册表/双图数据库/压缩机制（#64–66）", name, p)
			}
		}
	}
	if checked == 0 {
		t.Fatal("没有检查到任何非测试源文件")
	}
}

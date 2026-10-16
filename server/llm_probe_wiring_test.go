package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestTestLLMWiresIndependentProbe 固化批次 2 的接线与硬约束（#54–74）：/api/llm/test
// 必须走 llmtest 的独立轻量探测——单次最小补全请求，不带 tools / tool_choice /
// system / stream，不经过 Agent 执行栈，也不需要数据库即可完成。
func TestTestLLMWiresIndependentProbe(t *testing.T) {
	var mu sync.Mutex
	var hits int
	var body []byte
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits++
		body = raw
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"echo-model","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	}))
	defer llm.Close()

	// m 未接数据库：探测路径不应触碰 DB（真的碰了会在 pg == nil 处暴露）。
	s := &Server{m: &Manager{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/llm/test", strings.NewReader(fmt.Sprintf(
		`{"provider":"openai","model":"probe-model","base_url":%q,"api_key":"sk-test","streaming":true}`, llm.URL)))
	s.testLLM(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，预期 200（正文 %s）", rec.Code, rec.Body.String())
	}
	var out struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Model   string `json:"model"`
		Reply   string `json:"reply"`
		Latency int64  `json:"latency_ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.Bytes())
	}
	if !out.OK {
		t.Fatalf("测试连接应成功：%+v", out)
	}
	if out.Model != "echo-model" || out.Reply != "pong" {
		t.Fatalf("应回传供应商回显的模型与回复：%+v", out)
	}
	mu.Lock()
	defer mu.Unlock()
	// 恰好一个请求：既没有重试，也没有顺带拉模型列表（#61–63）。
	if hits != 1 {
		t.Fatalf("测试连接应恰好发 1 个请求，实际 %d", hits)
	}
	compact := strings.ReplaceAll(string(body), " ", "")
	for _, forbidden := range []string{`"tools"`, `"tool_choice"`, `"system"`, `"stream"`, `"stream_options"`} {
		if strings.Contains(compact, forbidden) {
			t.Fatalf("测试连接请求不得携带 %s：%s", forbidden, body)
		}
	}
}

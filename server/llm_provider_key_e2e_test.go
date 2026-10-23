package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 「测试连接」必须真的把供应商的 Key 发到线上。
//
// 上一个用例（TestResolveLLMKeyByProvider）只验到 Key 解析这一层。但现场那次
// 故障里，真正断掉的地方可能更靠外：JSON 字段没接上、handler 没把 provider_id
// 传下去、或者探针根本没用解析出来的 Key。这类问题单测「解析函数」永远抓不到。
//
// 所以这里从 HTTP handler 进、到假上游的 Authorization 头出，整条链走一遍：
//
//	POST /api/llm/providers  → 落库（Key 加密）
//	POST /api/llm/profiles   → 建模型（不带 Key，Key 归供应商）
//	POST /api/llm/test       → 只给 provider_id，不给 api_key
//	假上游                     → 断言收到的 Bearer 就是供应商那把
func TestTestLLMUsesProviderKeyEndToEnd(t *testing.T) {
	const wantKey = "sk-e2e-provider-key-424242"

	var gotAuth string
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-e2e","choices":[{"message":{"role":"assistant","content":"OK"}}]}`))
	}))
	defer upstream.Close()

	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)

	do := func(h http.HandlerFunc, body string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是 JSON（%d）：%s", rec.Code, rec.Body.String())
		}
		return out
	}

	// ① 建供应商：Key 只在这一步出现，之后所有请求都不再带它。
	savedProvider := do(s.pgSaveProvider, `{"name":"e2e","format":"openai","base_url":"`+upstream.URL+`/v1","api_key":"`+wantKey+`"}`)
	pid := toInt(savedProvider["id"])
	if pid == 0 {
		t.Fatalf("建供应商失败：%v", savedProvider)
	}

	// ② 在其下建模型：payload 里没有任何 Key 字段。
	savedProfile := do(s.pgSaveProfile, `{"name":"e2e-model","model":"gpt-e2e","provider_id":`+itoa(pid)+`}`)
	mid := toInt(savedProfile["id"])
	if mid == 0 {
		t.Fatalf("建模型失败：%v", savedProfile)
	}

	// ③ 只给 provider_id 测连接：这正是「刚建好供应商、模型还没建全」时的形状。
	res := do(s.testLLM, `{"provider":"openai","model":"gpt-e2e","base_url":"`+upstream.URL+`/v1","provider_id":`+itoa(pid)+`}`)
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("按供应商测连接失败：%v", res)
	}
	if gotAuth != "Bearer "+wantKey {
		t.Fatalf("上游收到的 Authorization=%q，期望 %q —— 供应商的 Key 没有真正发出去", gotAuth, "Bearer "+wantKey)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("上游收到的路径=%q", gotPath)
	}
	if src, _ := res["key_source"].(string); !strings.Contains(src, "供应商") {
		t.Fatalf("key_source 应说明来自供应商，实际 %q", src)
	}

	// ④ 按模型测连接：读路径是 COALESCE(供应商, 模型)，必须打到同一把 Key。
	gotAuth = ""
	res = do(s.testLLM, `{"provider":"openai","model":"gpt-e2e","base_url":"`+upstream.URL+`/v1","profile_id":`+itoa(mid)+`}`)
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("按模型测连接失败：%v", res)
	}
	if gotAuth != "Bearer "+wantKey {
		t.Fatalf("按模型测连接时上游收到 %q，期望 %q", gotAuth, "Bearer "+wantKey)
	}

	// ⑤ 换一把只存在于表单里的 Key：表单必须压过库里存的那把。
	const typed = "sk-typed-in-form-777"
	gotAuth = ""
	res = do(s.testLLM, `{"provider":"openai","model":"gpt-e2e","base_url":"`+upstream.URL+`/v1","api_key":"`+typed+`","provider_id":`+itoa(pid)+`}`)
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("表单 Key 测连接失败：%v", res)
	}
	if gotAuth != "Bearer "+typed {
		t.Fatalf("表单 Key 未优先：上游收到 %q", gotAuth)
	}
}

// 服务端把 id 统一序列化成字符串（超长 id 不丢精度），所以两种形态都要认。
func toInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		var n int64
		_, _ = fmt.Sscan(x, &n)
		return n
	}
	return 0
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

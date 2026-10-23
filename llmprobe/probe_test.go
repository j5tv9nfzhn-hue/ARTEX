package llmprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 各家模型列表的真实外形（截取关键字段），用来钉住解析口径。
// 这些样本都取自公开文档，改解析规则时先改这里。
var extractCases = []struct {
	name      string
	body      string
	model     string
	wantCtx   int
	wantOut   int
	wantModal []string
	wantReas  bool
}{
	{
		name: "openrouter",
		body: `{"data":[{"id":"anthropic/claude-4.5-sonnet","context_length":200000,
			"architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"]},
			"top_provider":{"context_length":200000,"max_completion_tokens":32000},
			"supported_parameters":["tools","reasoning"],"reasoning":{"supported_efforts":["low","medium","high"]}}]}`,
		model:     "anthropic/claude-4.5-sonnet",
		wantCtx:   200000,
		wantOut:   32000,
		wantModal: []string{"text", "image", "pdf"},
		wantReas:  true,
	},
	{
		name:    "vllm",
		body:    `{"object":"list","data":[{"id":"qwen3-32b","object":"model","max_model_len":32768}]}`,
		model:   "qwen3-32b",
		wantCtx: 32768,
	},
	{
		name:    "lmstudio",
		body:    `{"object":"list","data":[{"id":"qwen2-vl-7b","type":"vlm","state":"loaded","max_context_length":32768,"loaded_context_length":8192}]}`,
		model:   "qwen2-vl-7b",
		wantCtx: 8192, // 服务中的窗口优先于训练上限
		wantModal: []string{
			"text", "image",
		},
	},
	{
		name:    "llamacpp_props",
		body:    `{"default_generation_settings":{"n_ctx":16384},"total_slots":1,"model_path":"x.gguf"}`,
		model:   "",
		wantCtx: 16384,
	},
	{
		name:    "llamacpp_models",
		body:    `{"data":[{"id":"local","object":"model","meta":{"n_ctx_train":262144,"n_embd":4096}}]}`,
		model:   "local",
		wantCtx: 262144,
	},
	{
		name:    "ollama_show",
		body:    `{"details":{"family":"qwen3"},"capabilities":["completion","tools","thinking","vision"],"model_info":{"qwen3.context_length":131072}}`,
		model:   "qwen3:8b",
		wantCtx: 131072,
		wantModal: []string{
			"text", "image",
		},
		wantReas: true,
	},
	{
		name:     "gemini",
		body:     `{"models":[{"name":"models/gemini-2.5-pro","inputTokenLimit":1048576,"outputTokenLimit":65536,"thinking":true}]}`,
		model:    "gemini-2.5-pro",
		wantCtx:  1048576,
		wantOut:  65536,
		wantReas: true,
	},
	{
		name:    "anthropic",
		body:    `{"data":[{"id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5","max_input_tokens":200000,"max_output_tokens":64000}]}`,
		model:   "claude-sonnet-4-5",
		wantCtx: 200000,
		wantOut: 64000,
	},
	{
		name:    "numeric_string",
		body:    `{"data":[{"id":"gw-model","context_length":"131072","max_output_tokens":"8192"}]}`,
		model:   "gw-model",
		wantCtx: 131072,
		wantOut: 8192,
	},
}

func TestExtractModelCaps(t *testing.T) {
	for _, c := range extractCases {
		t.Run(c.name, func(t *testing.T) {
			caps, ok := ExtractModelCaps([]byte(c.body), c.model)
			if !ok {
				t.Fatalf("未解析出能力")
			}
			if caps.ContextTokens != c.wantCtx {
				t.Errorf("context = %d, want %d", caps.ContextTokens, c.wantCtx)
			}
			if caps.MaxOutputTokens != c.wantOut {
				t.Errorf("output = %d, want %d", caps.MaxOutputTokens, c.wantOut)
			}
			if c.wantModal != nil && strings.Join(caps.InputModalities, ",") != strings.Join(c.wantModal, ",") {
				t.Errorf("modalities = %v, want %v", caps.InputModalities, c.wantModal)
			}
			if caps.Reasoning != c.wantReas {
				t.Errorf("reasoning = %v, want %v", caps.Reasoning, c.wantReas)
			}
		})
	}
}

// 列表里没有目标模型时不能张冠李戴——宁可返回 false。
func TestExtractModelCapsRejectsUnknownModel(t *testing.T) {
	body := `{"data":[{"id":"a","context_length":8192},{"id":"b","context_length":4096}]}`
	if _, ok := ExtractModelCaps([]byte(body), "c"); ok {
		t.Fatal("不存在的模型不应解析出别人的能力")
	}
}

func TestFromCatalog(t *testing.T) {
	cases := map[string]int{
		"claude-sonnet-4-5":       200000,
		"gpt-5.2":                 400000,
		"gemini-2.5-pro":          1048576,
		"qwen3.8-flash-next":      131072,
		"Qwen/Qwen3-VL-30B":       131072,
		"glm-5.3-flash":           131072,
		"deepseek-v4.1-flash":     131072,
		"kimi-k2.6":               262144,
		"minimax-m3.1-flash:free": 204800,
	}
	for model, want := range cases {
		caps, ok := FromCatalog(model)
		if !ok {
			t.Errorf("%s：目录未命中", model)
			continue
		}
		if caps.ContextTokens != want {
			t.Errorf("%s：context = %d, want %d", model, caps.ContextTokens, want)
		}
	}
	if _, ok := FromCatalog("mimo-v2.6-flash"); ok {
		t.Errorf("未知模型不应命中目录")
	}
}

// Merge 的语义：实测值优先，目录只补空缺。
func TestMergePrefersProbed(t *testing.T) {
	base := Capabilities{ContextTokens: 200000, MaxOutputTokens: 32000, Source: SourceCatalog}
	over := Capabilities{ContextTokens: 131072, Source: SourceProvider}
	got := Merge(base, over)
	if got.ContextTokens != 131072 {
		t.Errorf("context = %d, want 131072（实测优先）", got.ContextTokens)
	}
	if got.MaxOutputTokens != 32000 {
		t.Errorf("output = %d, want 32000（目录补缺）", got.MaxOutputTokens)
	}
	if got.Source != SourceProvider {
		t.Errorf("source = %s, want provider", got.Source)
	}
}

// 端到端：httptest 起一个假网关，验证 Probe 走 HTTP 后能取到能力，
// 并在端点不可用时退到目录而不是报错。
func TestProbeOverHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" && r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen3-32b","max_model_len":65536}]}`))
	}))
	defer srv.Close()

	caps, err := Probe(context.Background(), Config{
		Format:  "openai",
		BaseURL: srv.URL,
		Model:   "qwen3-32b",
		APIKey:  "k",
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if caps.ContextTokens != 65536 || caps.Source != SourceProvider {
		t.Fatalf("caps = %+v, want 65536/provider", caps)
	}
}

func TestProbeFallsBackToCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	caps, err := Probe(context.Background(), Config{Format: "openai", BaseURL: srv.URL, Model: "glm-5.3-flash"})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if caps.Source != SourceCatalog || caps.ContextTokens != 131072 {
		t.Fatalf("caps = %+v, want catalog/131072", caps)
	}
	if len(caps.Notes) == 0 {
		t.Fatal("降级时应留下说明")
	}
}

// 本机地址才追加原生端点；公网地址不该去探 127.0.0.1 那套接口。
func TestEndpointsLocalOnly(t *testing.T) {
	remote := Endpoints("openai", "https://api.example.com/v1", "m")
	for _, ep := range remote {
		if strings.Contains(ep.URL, "api/v0/models") || strings.HasSuffix(ep.URL, "/props") {
			t.Fatalf("公网地址不应探测本机端点：%s", ep.URL)
		}
	}
	local := Endpoints("openai", "http://127.0.0.1:1234/v1", "m")
	var hasLM, hasProps bool
	for _, ep := range local {
		if strings.Contains(ep.URL, "api/v0/models") {
			hasLM = true
		}
		if strings.HasSuffix(ep.URL, "/props") {
			hasProps = true
		}
	}
	if !hasLM || !hasProps {
		t.Fatalf("本机地址应追加 LM Studio / llama.cpp 端点，got %+v", local)
	}
}

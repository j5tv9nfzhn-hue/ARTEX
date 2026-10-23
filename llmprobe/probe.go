package llmprobe

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config 是一次能力探测的输入。字段与 llm_profiles 一一对应，
// 便于 server 直接把一条 profile 传进来。
type Config struct {
	Format  string // anthropic | openai | openai-responses
	BaseURL string
	Model   string
	APIKey  string
	Proxy   string
	Timeout time.Duration
}

// Endpoint 是一个待尝试的元数据端点。
type Endpoint struct {
	Name    string // 日志/Notes 用，例如 "openai /v1/models"
	Method  string
	URL     string
	Headers map[string]string
	Body    string
}

const (
	// defaultTimeout 是整次探测的总预算。元数据端点都是小请求，
	// 给 12s 足够；超时就退到目录，不让「探测」拖慢设置页。
	defaultTimeout = 12 * time.Second
	// maxBody 防止某个网关把整个模型仓库塞回来。
	maxBody = 4 << 20
)

// Probe 按 profile 的协议与地址，依次尝试供应商的元数据端点，取第一个
// 能给出「这个模型」能力的结果；拿不到就退到内建目录，再不行给默认值。
//
// 永远不返回错误语义的失败：能力探测是增强信息，失败不该拦住保存配置。
// 唯一例外是 ctx 被取消（调用方主动放弃）。
func Probe(ctx context.Context, cfg Config) (Capabilities, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := newClient(cfg.Proxy)
	var notes []string
	var provider, partial Capabilities

	for _, ep := range Endpoints(cfg.Format, cfg.BaseURL, cfg.Model) {
		body, status, err := fetch(ctx, client, ep, cfg.APIKey)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s：%v", ep.Name, err))
			continue
		}
		if status < 200 || status >= 300 {
			// 404/401 很常见（很多网关根本不提供模型元数据），只记一条简短的。
			notes = append(notes, fmt.Sprintf("%s：HTTP %d", ep.Name, status))
			continue
		}
		caps, ok := ExtractModelCaps(body, cfg.Model)
		if !ok {
			notes = append(notes, fmt.Sprintf("%s：响应里没有 %q 的能力字段", ep.Name, cfg.Model))
			continue
		}
		// 只有模态/推理信息、没有窗口的响应不算「探到了」：继续问下一个端点
		// （本机推理进程的 /api/show、/props 往往在后面），但先把这份留着补空。
		if caps.ContextTokens > 0 || caps.MaxOutputTokens > 0 {
			caps.Source = SourceProvider
			provider = caps
			break
		}
		if !partial.Known() {
			partial = caps
		}
		notes = append(notes, fmt.Sprintf("%s：只给了模态/推理信息，没有窗口", ep.Name))
	}

	cat, hasCat := FromCatalog(cfg.Model)
	// Merge(base, over)：over（供应商实测）优先，目录只补空缺。
	// 优先级：端点实测窗口 > 端点零散信息 > 内建目录。
	out := Merge(Merge(cat, partial), provider)
	if !out.Known() && !hasCat {
		out = Defaults()
	}
	if out.ContextTokens == 0 {
		// 连目录都没给出窗口：按默认 200K，同时留痕，避免"看起来像探测到了"。
		out.ContextTokens = Defaults().ContextTokens
		notes = append(notes, "没有任何来源给出上下文窗口，按 200K 保守估算")
	}
	if out.Source == "" {
		out.Source = SourceDefaults
	}
	out.Notes = append(out.Notes, notes...)
	if ctx.Err() != nil && !out.Known() {
		return out.Stamp(cfg.Model), ctx.Err()
	}
	return out.Stamp(cfg.Model), nil
}

// Endpoints 列出该协议/地址下值得尝试的元数据端点，按优先级排序。
//
// 顺序是有讲究的：先问「协议自己的模型列表」，再问本机推理进程的原生接口
// （Ollama / LM Studio / llama.cpp 的窗口只在他们自己的 API 里），最后才是
// 通用的 OpenAI 兼容路径。
func Endpoints(format, baseURL, model string) []Endpoint {
	f := strings.ToLower(strings.TrimSpace(format))
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	var eps []Endpoint
	switch f {
	case "anthropic":
		if base == "" {
			base = "https://api.anthropic.com"
		}
		eps = append(eps, Endpoint{Name: "anthropic /v1/models", Method: http.MethodGet, URL: anthropicModelsURL(base)})
		// Anthropic 兼容网关（如 DeepSeek 的 /anthropic 路径）常把模型列表放在
		// OpenAI 路径上，剥掉 /anthropic 再试一次。
		if root := strings.TrimRight(strings.TrimSuffix(base, "/anthropic"), "/"); root != base {
			eps = append(eps, Endpoint{Name: "openai /models（兼容路径）", Method: http.MethodGet, URL: root + "/models"})
			eps = append(eps, Endpoint{Name: "openai /v1/models（兼容路径）", Method: http.MethodGet, URL: root + "/v1/models"})
		}
	default: // openai / openai-responses
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		eps = append(eps, Endpoint{Name: "openai /models", Method: http.MethodGet, URL: base + "/models"})
		if !strings.HasSuffix(base, "/v1") {
			eps = append(eps, Endpoint{Name: "openai /v1/models", Method: http.MethodGet, URL: base + "/v1/models"})
		}
	}

	if isLocalURL(base) {
		root := rootOf(base)
		eps = append(eps,
			// LM Studio：/api/v0/models 同时给 max_context_length 与 loaded_context_length
			Endpoint{Name: "lmstudio /api/v0/models", Method: http.MethodGet, URL: root + "/api/v0/models"},
			// llama.cpp：/props 的 default_generation_settings.n_ctx 才是真正服务的窗口
			Endpoint{Name: "llama.cpp /props", Method: http.MethodGet, URL: root + "/props"},
			// Ollama：/api/show 要 POST 且要模型名
			Endpoint{
				Name:    "ollama /api/show",
				Method:  http.MethodPost,
				URL:     root + "/api/show",
				Body:    `{"model":` + quoteJSON(model) + `}`,
				Headers: map[string]string{"Content-Type": "application/json"},
			},
		)
	}
	return eps
}

func anthropicModelsURL(base string) string {
	if strings.HasSuffix(base, "/v1") {
		return base + "/models"
	}
	return base + "/v1/models"
}

// rootOf 去掉路径里的 /v1、/api 之类后缀，用于访问同一台机器的原生接口。
func rootOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	u.Path = ""
	u.RawQuery = ""
	return strings.TrimRight(u.String(), "/")
}

func isLocalURL(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || host == "::1" || host == "0.0.0.0" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

func newClient(proxy string) *http.Client {
	transport := &http.Transport{}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			transport.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{Transport: transport}
}

func fetch(ctx context.Context, client *http.Client, ep Endpoint, apiKey string) ([]byte, int, error) {
	method := ep.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if ep.Body != "" {
		body = strings.NewReader(ep.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, ep.URL, body)
	if err != nil {
		return nil, 0, err
	}
	// 认证头按端点选：Anthropic 用 x-api-key，其余用 Bearer。
	if strings.Contains(ep.Name, "anthropic") {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range ep.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// quoteJSON 只处理模型名里出现的引号/反斜杠，够用且不引入依赖。
func quoteJSON(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

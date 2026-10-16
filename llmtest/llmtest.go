// Package llmtest 实现「测试连接」的独立轻量探测（docs/开发总清单.md 批次 2）。
//
// 它只做一件事：按 LLM profile 的 format / base_url / api_key / model 发出一次
// 最小、单发、无状态、非流式的补全请求，验证端点、密钥与模型是否真的可用，
// 并把 HTTP 状态码与供应商原文带回给调用方。
//
// 依赖清单（有意固化，#64–66；由 llmtest_test.go 的导入守卫机械校验）：
//   - 只依赖 Go 标准库：context、crypto/rand、encoding/hex、encoding/json、
//     errors、fmt、io、log、net/http、net/url、os、strings、time。
//   - 不依赖 agent（Agent Engine）、norma（agentcore/llm/compaction/transcript/
//     permission）、llmrec（LLM 录制器）、llmpool、db（双图数据库）、server。
//
// 因此探测路径天然满足以下硬约束（#54–73）：
//   - 请求体只有 model + 一条 user 消息（外加该 profile 的思考参数与该协议的
//     必填项）：不带 tools、不带 tool_choice、不带 system、不带 stream——请求
//     结构体上没有这些字段，从类型上就不可能带上；
//   - 不写会话历史、不注册工具 schema、不触发上下文压缩、不创建 worker、
//     不经过完整 Agent 执行栈（没有 Provider/Session/Hook/Transcript）；
//   - 不产生 LLM 录制，也不计入会话与压缩的任何状态。
//
// 与「拉取模型」彻底分离（#61–63）：本包只发补全请求，没有任何模型列表逻辑；
// HTTP 侧 /api/llm/test 与 /api/llm/models 是两个互不触发的端点。
//
// 成功判据（#74）：HTTP 2xx + 响应体是合法 JSON + 含该协议的补全结构
// （openai: choices；anthropic: type=message/content/stop_reason；responses:
// output/status），且至少有一段内容（文本、思考、拒绝或工具调用）；空补全与
// 非补全响应都算失败。失败诊断内联 HTTP 状态码与供应商原文（#41–42）。
package llmtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultTimeout 是单次探测的总时限：默认 10s（#74）。
const DefaultTimeout = 10 * time.Second

// probeMaxTokens 只用于 anthropic 协议：Messages API 的 max_tokens 是必填项，
// 给一个够短回复用的固定小预算即可。openai / openai-responses 不发送输出上限
// 字段——它们是可选项，而推理模型（o 系列 / GPT-5）收到 max_tokens 会直接
// 报 unsupported_parameter，探测不该因此误报失败。
const probeMaxTokens = 256

// probePrompt 是探测请求唯一的内容：没有 system 提示词（#69），也不指定回答；
// 任何能返回补全结构的回复都算连通。
const probePrompt = "ping"

// maxResponseBytes 单次探测最多读取 4 MiB 响应体。
const maxResponseBytes = 4 << 20

// diagBodyMax 是错误诊断与日志中保留的供应商原文上限（按 rune 截断）。
const diagBodyMax = 2048

// Format 选择线上协议，取值与 LLM profile 的 provider 字段一致。
type Format string

const (
	FormatAnthropic       Format = "anthropic"
	FormatOpenAI          Format = "openai"
	FormatOpenAIResponses Format = "openai-responses"
)

// normalize 与 agent.ConfigFrom 保持同一套回落规则：空 / 未知一律按 anthropic。
func (f Format) normalize() Format {
	switch Format(strings.TrimSpace(string(f))) {
	case FormatOpenAI:
		return FormatOpenAI
	case FormatOpenAIResponses:
		return FormatOpenAIResponses
	default:
		return FormatAnthropic
	}
}

// Config 是一次探测的输入。字段刻意只保留「请求怎么发」所必需的值：没有
// Tools、System、Stream、Transcript、Compaction、Worker 等执行栈字段——那些在
// 本类型上就不存在，这是 #67–73 的第一道回归防线。
type Config struct {
	Format  Format
	BaseURL string
	APIKey  string
	Model   string
	// Proxy 仅影响本次探测：http/https/socks5（可带账号密码）。空 = 直连，不回退
	// *_PROXY 环境变量（与 agent.Config.Proxy 的约定一致）。
	Proxy string
	// ThinkingType / ReasoningEffort 可选：与生产请求保持同一套思考参数，让
	// 「拒绝该字段的端点」在测试时就暴露，而不是等会话里才 400。空 = 不发送。
	ThinkingType    string
	ReasoningEffort string
	// SessionHeaderKey 可选：非空时探测请求带上该头，值为一次性随机 session id。
	// 某些端点（如 opencode zen）强制要求会话头，缺失即 400。
	SessionHeaderKey string
	// Timeout 单次探测的总时限；0 = DefaultTimeout（10s）。
	Timeout time.Duration
}

// Result 是一次成功探测的结果。
type Result struct {
	Latency time.Duration // 从发出请求到读完响应体的耗时
	Status  int           // HTTP 状态码（恒为 2xx）
	Model   string        // 供应商回显的 model；未回显时回落为请求的 model
	Reply   string        // 从补全结构中抽取的文本；可能为空（仅有思考/工具调用时）
}

// Stage 标记探测失败发生在哪一步。
type Stage string

const (
	StageConfig    Stage = "config"    // 配置或请求构造阶段
	StageTransport Stage = "transport" // 网络/超时：没有拿到 HTTP 响应
	StageHTTP      Stage = "http"      // 拿到了非 2xx 响应
	StageProtocol  Stage = "protocol"  // 2xx 但不是合法补全
)

// Error 是一次可读的探测失败诊断。Message 是可直接展示给用户的简体中文说明，
// 已内联 HTTP 状态码与供应商原文；Body 保留截断后的原始响应体，供日志或后续
// 批次程序化处理（#41–42）。
type Error struct {
	Stage   Stage
	Status  int    // HTTP 状态码；未收到响应时为 0
	Body    string // 供应商原始响应体（截断后）；无响应时为空
	Message string
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// ---- 请求体：结构体字段清单本身就是约束，不存在可不带的字段就发不出去 ----

// probeMessage 是各协议共用的最小消息：只有 role/content。
type probeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// probeThinking 是 thinking.type 开关（openai 与 anthropic 同形）。
type probeThinking struct {
	Type string `json:"type"`
}

// openaiProbeRequest 是 Chat Completions 的最小请求体：没有 tools / tool_choice /
// system / stream 字段。
type openaiProbeRequest struct {
	Model           string         `json:"model"`
	Messages        []probeMessage `json:"messages"`
	Thinking        *probeThinking `json:"thinking,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

// anthropicTextBlock / anthropicProbeMessage 与 norma 生产请求同形：content 是
// 文本块数组。没有 system / tools / stream 字段。
type anthropicTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicProbeMessage struct {
	Role    string               `json:"role"`
	Content []anthropicTextBlock `json:"content"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort"`
}

type anthropicProbeRequest struct {
	Model        string                  `json:"model"`
	Messages     []anthropicProbeMessage `json:"messages"`
	MaxTokens    int                     `json:"max_tokens"`
	Thinking     *probeThinking          `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig  `json:"output_config,omitempty"`
}

// responsesProbeRequest 是 Responses API 的最小请求体：没有 tools / instructions
// (system) / stream / previous_response_id 字段；store=false 保证供应商侧不留
// 任何会话记录（#71–73）。
type responsesProbeRequest struct {
	Model     string              `json:"model"`
	Input     []probeMessage      `json:"input"`
	Store     bool                `json:"store"`
	Reasoning *responsesReasoning `json:"reasoning,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort"`
}

// buildBody 组装各协议的最小请求体。返回的字节序列里不可能出现 tools、
// tool_choice、system、stream——上面的结构体没有这些字段。
func buildBody(f Format, model string, cfg Config) ([]byte, error) {
	switch f {
	case FormatOpenAI:
		req := openaiProbeRequest{
			Model:    model,
			Messages: []probeMessage{{Role: "user", Content: probePrompt}},
		}
		if t := strings.TrimSpace(cfg.ThinkingType); t != "" {
			req.Thinking = &probeThinking{Type: t}
		}
		req.ReasoningEffort = strings.TrimSpace(cfg.ReasoningEffort)
		return json.Marshal(req)
	case FormatOpenAIResponses:
		req := responsesProbeRequest{
			Model: model,
			Input: []probeMessage{{Role: "user", Content: probePrompt}},
			Store: false,
		}
		// 与 norma 的 Responses 适配器一致：compaction 的 "disabled" 抑制推理强度。
		if e := strings.TrimSpace(cfg.ReasoningEffort); e != "" && strings.TrimSpace(cfg.ThinkingType) != "disabled" {
			req.Reasoning = &responsesReasoning{Effort: e}
		}
		return json.Marshal(req)
	default:
		req := anthropicProbeRequest{
			Model:     model,
			Messages:  []anthropicProbeMessage{{Role: "user", Content: []anthropicTextBlock{{Type: "text", Text: probePrompt}}}},
			MaxTokens: probeMaxTokens,
		}
		if t := strings.TrimSpace(cfg.ThinkingType); t != "" {
			req.Thinking = &probeThinking{Type: t}
		}
		if e := strings.TrimSpace(cfg.ReasoningEffort); e != "" {
			req.OutputConfig = &anthropicOutputConfig{Effort: e}
		}
		return json.Marshal(req)
	}
}

// defaultModel 与 agent.ConfigFrom 的默认模型保持一致。
func defaultModel(f Format) string {
	switch f {
	case FormatOpenAI:
		return "gpt-4o"
	case FormatOpenAIResponses:
		return "gpt-5"
	default:
		return "claude-opus-4-8"
	}
}

// endpoint 返回探测的完整 URL，并按 agent.ConfigFrom 的同一套规则兜底与归一化：
// 容忍用户把完整端点 URL 粘进 base_url；base_url 为空时回落到标准环境变量或
// 官方端点（与 norma llm.NewProvider 一致）。
func (c Config) endpoint(f Format) string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	switch f {
	case FormatOpenAI:
		base = strings.TrimRight(strings.TrimSuffix(base, "/chat/completions"), "/")
		if base == "" {
			base = envOr("OPENAI_BASE_URL", "https://api.openai.com/v1")
		}
		return base + "/chat/completions"
	case FormatOpenAIResponses:
		base = strings.TrimRight(strings.TrimSuffix(base, "/responses"), "/")
		if base == "" {
			base = envOr("OPENAI_BASE_URL", "https://api.openai.com/v1")
		}
		return base + "/responses"
	default:
		base = strings.TrimRight(strings.TrimSuffix(base, "/v1/messages"), "/")
		if base == "" {
			base = envOr("ANTHROPIC_BASE_URL", "https://api.anthropic.com")
		}
		return base + "/v1/messages"
	}
}

// headers 返回认证与内容头；非流式探测固定 accept: application/json。
func (c Config) headers(f Format) map[string]string {
	h := map[string]string{
		"content-type": "application/json",
		"accept":       "application/json",
	}
	if f == FormatAnthropic {
		h["x-api-key"] = c.APIKey
		h["anthropic-version"] = "2023-06-01"
		return h
	}
	h["authorization"] = "Bearer " + c.APIKey
	return h
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// newHTTPClient 构造本次探测专用的客户端。留空 = 直连（不读 *_PROXY 环境变量）。
func newHTTPClient(proxy string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		transport.Proxy = nil
		return &http.Client{Transport: transport}, nil
	}
	// "host:port" 这类缺协议的地址会被 url.Parse 先按 path 解析失败
	// （first path segment in URL cannot contain colon），按最常见的缺协议
	// 输入归类，给出可操作的提示而不是笼统的解析错误。
	if !strings.Contains(proxy, "://") {
		return nil, errors.New("代理地址缺少协议（请使用 http://、https:// 或 socks5://）")
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return nil, fmt.Errorf("代理地址无法解析：%v", err)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	case "":
		return nil, errors.New("代理地址缺少协议（请使用 http://、https:// 或 socks5://）")
	default:
		return nil, fmt.Errorf("不支持的代理协议 %q（请使用 http、https 或 socks5）", u.Scheme)
	}
	transport.Proxy = http.ProxyURL(u)
	return &http.Client{Transport: transport}, nil
}

// newSessionID 生成一次性会话 id（conntest-<16 字节随机十六进制>）：形态与生产
// 会话 id 一致，某些端点强制要求会话头，缺失即 400。
func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("conntest-%x", time.Now().UnixNano())
	}
	return "conntest-" + hex.EncodeToString(b)
}

// Probe 发出一次最小探测。整个探测受 ctx 与 cfg.Timeout 双重约束；单发、不重试。
// 调用方拿到 error 时，err.Error() 已包含可直接阅读的诊断（含 HTTP 状态码与
// 供应商原文）。
func Probe(ctx context.Context, cfg Config) (Result, error) {
	f := cfg.Format.normalize()
	if strings.TrimSpace(cfg.APIKey) == "" {
		return Result{}, &Error{Stage: StageConfig, Message: "未提供 API Key"}
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = defaultModel(f)
	}
	endpoint := cfg.endpoint(f)
	body, err := buildBody(f, model, cfg)
	if err != nil {
		return Result{}, &Error{Stage: StageConfig, Message: "无法构造请求： " + err.Error(), cause: err}
	}
	client, err := newHTTPClient(cfg.Proxy)
	if err != nil {
		return Result{}, &Error{Stage: StageConfig, Message: err.Error(), cause: err}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, &Error{Stage: StageConfig, Message: "无法构造请求： " + err.Error(), cause: err}
	}
	for k, v := range cfg.headers(f) {
		req.Header.Set(k, v)
	}
	if key := strings.TrimSpace(cfg.SessionHeaderKey); key != "" {
		req.Header.Set(key, newSessionID())
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		logProbeNoResponse(f, model, endpoint, err)
		return Result{}, transportError(ctx, err, timeout)
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	_ = resp.Body.Close()
	latency := time.Since(start)
	status := resp.StatusCode
	logProbe(f, model, endpoint, status, raw)
	if readErr != nil && len(raw) == 0 {
		return Result{}, &Error{
			Stage:   StageTransport,
			Status:  status,
			Message: fmt.Sprintf("读取响应失败（HTTP %d）：%v", status, readErr),
			cause:   readErr,
		}
	}
	if status < 200 || status > 299 {
		return Result{}, httpError(status, raw)
	}
	return parseCompletion(f, model, status, latency, raw)
}

// transportError 归类「没有拿到 HTTP 响应」的失败：超时 / 取消 / 网络与代理错误。
func transportError(ctx context.Context, err error, timeout time.Duration) *Error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{
			Stage:   StageTransport,
			Message: fmt.Sprintf("连接测试超时（上限 %s）：%v", timeout, err),
			cause:   err,
		}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Stage: StageTransport, Message: "连接测试已取消：" + err.Error(), cause: err}
	}
	return &Error{Stage: StageTransport, Message: "请求失败（未收到 HTTP 响应）：" + err.Error(), cause: err}
}

// httpError 把非 2xx 响应做成可读诊断：状态码 + 供应商错误原文透传（#41–42）。
func httpError(status int, raw []byte) *Error {
	body := clipDiag(string(raw))
	msg := fmt.Sprintf("接口返回 HTTP %d", status)
	if text := providerErrText(raw); text != "" && text != body {
		msg += "：" + text + "（响应原文：" + body + "）"
	} else {
		msg += "：" + body
	}
	return &Error{Stage: StageHTTP, Status: status, Body: body, Message: msg}
}

// parseCompletion 按协议解析 2xx 响应体，并落实 #74 的成功判据。
func parseCompletion(f Format, model string, status int, latency time.Duration, raw []byte) (Result, error) {
	var (
		res Result
		err error
	)
	switch f {
	case FormatOpenAI:
		res, err = parseOpenAI(model, status, raw)
	case FormatOpenAIResponses:
		res, err = parseResponses(model, status, raw)
	default:
		res, err = parseAnthropic(model, status, raw)
	}
	if err != nil {
		return Result{}, err
	}
	res.Latency = latency
	res.Status = status
	return res, nil
}

// parseOpenAI 校验 Chat Completions 结构（choices 数组非空），抽取回复文本。
func parseOpenAI(model string, status int, raw []byte) (Result, error) {
	var r struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          string            `json:"content"`
				ReasoningContent string            `json:"reasoning_content"`
				Reasoning        string            `json:"reasoning"`
				ReasoningText    string            `json:"reasoning_text"`
				Refusal          string            `json:"refusal"`
				ToolCalls        []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Result{}, jsonError(status, err, raw)
	}
	if text := errorEnvelope(raw); text != "" {
		return Result{}, bodyError(status, text, raw)
	}
	if len(r.Choices) == 0 {
		return Result{}, protocolError(status, FormatOpenAI, "缺少 choices 数组", raw)
	}
	msg := r.Choices[0].Message
	reply := firstNonEmpty(
		strings.TrimSpace(msg.Content),
		strings.TrimSpace(msg.Refusal),
		strings.TrimSpace(msg.ReasoningContent),
		strings.TrimSpace(msg.Reasoning),
		strings.TrimSpace(msg.ReasoningText),
	)
	if reply == "" && len(msg.ToolCalls) == 0 {
		return Result{}, emptyCompletion(status, raw)
	}
	return Result{Model: firstNonEmpty(strings.TrimSpace(r.Model), model), Reply: reply}, nil
}

// parseAnthropic 校验 Messages 结构（type=message / content / stop_reason 任一），
// 抽取 text 块；没有 text 时回落思考块。
func parseAnthropic(model string, status int, raw []byte) (Result, error) {
	var r struct {
		Type    string `json:"type"`
		Model   string `json:"model"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Result{}, jsonError(status, err, raw)
	}
	if text := errorEnvelope(raw); text != "" {
		return Result{}, bodyError(status, text, raw)
	}
	if r.Type != "message" && r.StopReason == "" && len(r.Content) == 0 {
		return Result{}, protocolError(status, FormatAnthropic, "缺少 type=message / content / stop_reason", raw)
	}
	var text, thinking strings.Builder
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "thinking":
			thinking.WriteString(b.Thinking)
		}
	}
	reply := firstNonEmpty(strings.TrimSpace(text.String()), strings.TrimSpace(thinking.String()))
	if reply == "" {
		return Result{}, emptyCompletion(status, raw)
	}
	return Result{Model: firstNonEmpty(strings.TrimSpace(r.Model), model), Reply: reply}, nil
}

// parseResponses 校验 Responses 结构（object/type/status/output 任一），抽取
// output_text；没有文本时回落 reasoning 摘要。
func parseResponses(model string, status int, raw []byte) (Result, error) {
	var r struct {
		Type   string `json:"type"`
		Object string `json:"object"`
		Status string `json:"status"`
		Model  string `json:"model"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Summary []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"summary"`
			CallID string `json:"call_id"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Result{}, jsonError(status, err, raw)
	}
	if text := errorEnvelope(raw); text != "" {
		return Result{}, bodyError(status, text, raw)
	}
	if r.Object != "response" && r.Type != "response" && r.Status == "" && len(r.Output) == 0 {
		return Result{}, protocolError(status, FormatOpenAIResponses, "缺少 object=response / status / output", raw)
	}
	var text, thinking strings.Builder
	hasCall := false
	for _, item := range r.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" || c.Text != "" {
					text.WriteString(c.Text)
				}
			}
		case "reasoning":
			for _, s := range item.Summary {
				thinking.WriteString(s.Text)
			}
			for _, c := range item.Content {
				if c.Type == "reasoning_text" {
					thinking.WriteString(c.Text)
				}
			}
		case "function_call":
			hasCall = true
		}
	}
	reply := firstNonEmpty(strings.TrimSpace(text.String()), strings.TrimSpace(thinking.String()))
	if reply == "" && !hasCall {
		return Result{}, emptyCompletion(status, raw)
	}
	return Result{Model: firstNonEmpty(strings.TrimSpace(r.Model), model), Reply: reply}, nil
}

// jsonError 是「2xx 但不是 JSON」的诊断：状态码 + 解析错误 + 原文。
func jsonError(status int, err error, raw []byte) *Error {
	body := clipDiag(string(raw))
	return &Error{
		Stage:   StageProtocol,
		Status:  status,
		Body:    body,
		Message: fmt.Sprintf("无法解析响应 JSON（HTTP %d）：%v（响应原文：%s）", status, err, body),
		cause:   err,
	}
}

// protocolError 是「2xx 但不是该协议的补全结构」的诊断。
func protocolError(status int, f Format, why string, raw []byte) *Error {
	body := clipDiag(string(raw))
	return &Error{
		Stage:   StageProtocol,
		Status:  status,
		Body:    body,
		Message: fmt.Sprintf("响应缺少合法补全结构（HTTP %d，%s 协议，%s）：%s", status, string(f), why, body),
	}
}

// bodyError 是「2xx 但响应体是供应商错误对象」的诊断。
func bodyError(status int, text string, raw []byte) *Error {
	body := clipDiag(string(raw))
	return &Error{
		Stage:   StageProtocol,
		Status:  status,
		Body:    body,
		Message: fmt.Sprintf("接口返回 HTTP %d：%s（响应原文：%s）", status, text, body),
	}
}

// emptyCompletion 是「结构合法但模型没有产出任何内容」的诊断。请求通了不代表
// 会话里能跑：思考把预算烧光、安全策略吞掉正文等情况都会落在这里。
func emptyCompletion(status int, raw []byte) *Error {
	return &Error{
		Stage:   StageProtocol,
		Status:  status,
		Body:    clipDiag(string(raw)),
		Message: fmt.Sprintf("模型无回复内容（请求已通，但未返回任何文本；响应原文：%s）", clipDiag(string(raw))),
	}
}

// errorEnvelope 提取响应体里的供应商错误对象（部分网关用 200 回错误）。
func errorEnvelope(raw []byte) string {
	var m struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Error == nil {
		return ""
	}
	if s := strings.TrimSpace(m.Error.Message); s != "" {
		return s
	}
	return strings.TrimSpace(m.Error.Type)
}

// providerErrText 从供应商的错误响应体里取最可读的一句话（用于非 2xx 诊断）；
// 取不到时返回 ""，调用方回落到原始响应体。
func providerErrText(raw []byte) string {
	var m struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if len(m.Error) > 0 {
		var e struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(m.Error, &e) == nil {
			if s := strings.TrimSpace(e.Message); s != "" {
				return s
			}
			if s := strings.TrimSpace(e.Type); s != "" {
				return s
			}
			if s := strings.TrimSpace(e.Code); s != "" {
				return s
			}
		}
	}
	if s := strings.TrimSpace(m.Message); s != "" {
		return s
	}
	return strings.TrimSpace(m.Detail)
}

// clipDiag 把供应商原文裁剪到诊断可用的长度；空体给出明确占位。
func clipDiag(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(空响应体)"
	}
	r := []rune(s)
	if len(r) <= diagBodyMax {
		return s
	}
	return string(r[:diagBodyMax]) + fmt.Sprintf("…（已截断，共 %d 字符）", len(r))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// logProbe 把每次探测的 HTTP 状态与响应原文写进服务端日志，保证「点了测试」在
// 日志里有可诊断的原始记录（与旧实现的 logTestConnection 同目的，但不经过
// 录制器，也不会把探测细节混进会话记录）。
func logProbe(f Format, model, endpoint string, status int, raw []byte) {
	log.Printf("[llm-test] %s / %s @ %s — HTTP %d\n响应体: %s", f, model, endpoint, status, clipDiag(string(raw)))
}

func logProbeNoResponse(f Format, model, endpoint string, err error) {
	log.Printf("[llm-test] %s / %s @ %s — 未收到 HTTP 响应:%v", f, model, endpoint, err)
}

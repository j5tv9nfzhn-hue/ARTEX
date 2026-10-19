// Package artexsdk 是 ARTEX REST 协议面（/api/protocol/* 信封面 + /api/publish、/api/options…原生面）的
// 官方 Go 客户端。只依赖标准库；只读/显式方法两分：非 Options 方法不做隐式下发，
// 广播类指令（如 kill）必须显式调用对应方法。服务端协议见 server/protocol_api.go。
package artexsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ProtoVersion 与服务端 protocol_api.go 的 protoVersion 对齐。
const ProtoVersion = "v1"

// Envelope 是所有协议端点的统一响应信封。
type Envelope struct {
	Protocol string          `json:"protocol"`
	Resource string          `json:"resource"`
	Action   string          `json:"action"`
	ID       string          `json:"id"`
	Data     json.RawMessage `json:"data"`
}

// Client 是协议面客户端。零值不可用，须经 New 构造。
type Client struct {
	base    string
	http    *http.Client
	key     string
	ua      string
	headers map[string]string
}

// Option 是客户端构造选项。
type Option func(*Client)

// WithHTTPClient 覆盖底层 http.Client。
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithAPIKey 附带 X-API-Key 请求头（与服务端鉴权对齐）。
func WithAPIKey(k string) Option { return func(c *Client) { c.key = k } }

// WithUserAgent 覆盖 User-Agent。
func WithUserAgent(ua string) Option { return func(c *Client) { c.ua = ua } }

// WithHeader 追加固定请求头。
func WithHeader(k, v string) Option {
	return func(c *Client) {
		if c.headers == nil {
			c.headers = map[string]string{}
		}
		c.headers[k] = v
	}
}

// New 构造客户端。base 形如 http://127.0.0.1:8080。
func New(base string, opts ...Option) (*Client, error) {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return nil, fmt.Errorf("artexsdk: empty base url")
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("artexsdk: bad base url: %w", err)
	}
	c := &Client{base: base, http: http.DefaultClient, ua: "artexsdk/1"}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// do 发送一次协议请求并解信封。
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*Envelope, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("artexsdk: encode body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("X-API-Key", c.key)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("artexsdk: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		return nil, fmt.Errorf("artexsdk: decode envelope: %w", err)
	}
	if resp.StatusCode >= 400 {
		var oe struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(env.Data, &oe) == nil && oe.Error != "" {
			msg = oe.Error
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg, Env: &env}
	}
	if env.Protocol != "" && env.Protocol != ProtoVersion {
		return nil, fmt.Errorf("artexsdk: protocol mismatch: server=%s client=%s", env.Protocol, ProtoVersion)
	}
	return &env, nil
}

// doNative 调用平台原生端点（无协议信封），返回原始 JSON。
func (c *Client) doNative(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	return c.doRaw(ctx, method, path, query, body)
}

// doRaw 发请求并返回原始响应体（HTTP>=400 时尝试解析 error 字段）。
func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("artexsdk: encode body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("X-API-Key", c.key)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(raw))
		var oe struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &oe) == nil && oe.Error != "" {
			msg = oe.Error
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	return json.RawMessage(raw), nil
}

// APIError 是服务端返回的错误信封。
type APIError struct {
	Status  int
	Message string
	Env     *Envelope
}

func (e *APIError) Error() string {
	return fmt.Sprintf("artexsdk: HTTP %d: %s", e.Status, e.Message)
}

// ---------- Thread ----------

// Thread 是线程快照（data 载荷原样保留，强类型字段按需读取）。
type Thread struct {
	ID       string          `json:"id"`
	TaskID   int64           `json:"task_id"`
	State    string          `json:"state"`
	Active   bool            `json:"active"`
	Priority string          `json:"priority"`
	Raw      json.RawMessage `json:"-"`
}

// ListThreads 列出线程。GET /api/threads?after=&limit=。
func (c *Client) ListThreads(ctx context.Context, after int, limit int) ([]Thread, string, error) {
	q := url.Values{}
	if after > 0 {
		q.Set("after", strconv.Itoa(after))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	env, err := c.do(ctx, http.MethodGet, "/api/protocol/threads", q, nil)
	if err != nil {
		return nil, "", err
	}
	var payload struct {
		Threads    []json.RawMessage `json:"threads"`
		NextCursor string            `json:"next_cursor"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		return nil, "", fmt.Errorf("artexsdk: decode threads: %w", err)
	}
	out := make([]Thread, 0, len(payload.Threads))
	for _, one := range payload.Threads {
		var t Thread
		if err := json.Unmarshal(one, &t); err != nil {
			return nil, "", fmt.Errorf("artexsdk: decode thread: %w", err)
		}
		t.Raw = one
		out = append(out, t)
	}
	return out, payload.NextCursor, nil
}

// CreateThread 创建线程。POST /api/threads。body 为上游 createTask DTO 透传。
func (c *Client) CreateThread(ctx context.Context, body any) (*Envelope, error) {
	return c.do(ctx, http.MethodPost, "/api/protocol/threads", nil, body)
}

// GetThread 取单线程。GET /api/threads/{id}。
func (c *Client) GetThread(ctx context.Context, id string) (*Envelope, error) {
	return c.do(ctx, http.MethodGet, "/api/protocol/threads/"+url.PathEscape(id), nil, nil)
}

// ControlThread 线程级控制。POST /api/threads/{id}/control，payload 透传。
func (c *Client) ControlThread(ctx context.Context, id string, payload any) (*Envelope, error) {
	return c.do(ctx, http.MethodPost, "/api/protocol/threads/"+url.PathEscape(id)+"/control", nil, payload)
}

// ---------- Turn / Work ----------

// CreateTurn 创建一轮对话。POST /api/threads/{id}/turns：
// kind=chat（须为激活任务）或 kind=intent（落到指定意图）。
func (c *Client) CreateTurn(ctx context.Context, id string, body any) (*Envelope, error) {
	return c.do(ctx, http.MethodPost, "/api/protocol/threads/"+url.PathEscape(id)+"/turns", nil, body)
}

// Steer 取消当前轮并改道。POST /api/threads/{id}/steer。
func (c *Client) Steer(ctx context.Context, id string, body any) (*Envelope, error) {
	return c.do(ctx, http.MethodPost, "/api/protocol/threads/"+url.PathEscape(id)+"/steer", nil, body)
}

// WorkControl 意图级控制。POST /api/threads/{id}/work/{iid}/control，payload 透传。
func (c *Client) WorkControl(ctx context.Context, id string, iid int64, payload any) (*Envelope, error) {
	return c.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/protocol/threads/%s/work/%d/control", url.PathEscape(id), iid), nil, payload)
}

// Kill 紧急停止（engine 唯一停止入口 Engine.KillWork）。POST /api/threads/{id}/kill。
func (c *Client) Kill(ctx context.Context, id string) (*Envelope, error) {
	return c.do(ctx, http.MethodPost, "/api/protocol/threads/"+url.PathEscape(id)+"/kill", nil, nil)
}

// ---------- Items / Approvals ----------

// ListThreadItems 列出线程可消费项。GET /api/protocol/threads/{id}/items?after=&limit=&kind=。
func (c *Client) ListThreadItems(ctx context.Context, id string, after, limit int, kind string) (*Envelope, error) {
	q := url.Values{}
	if after > 0 {
		q.Set("after", strconv.Itoa(after))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	return c.do(ctx, http.MethodGet, "/api/protocol/threads/"+url.PathEscape(id)+"/items", q, nil)
}

// GetThreadTree 取会话树（checkpoints + main_sessions + trace_threads）。
// GET /api/protocol/threads/{id}/tree。
func (c *Client) GetThreadTree(ctx context.Context, id string) (*Envelope, error) {
	return c.do(ctx, http.MethodGet, "/api/protocol/threads/"+url.PathEscape(id)+"/tree", nil, nil)
}

// Hooks 取钩子编排配置。GET /api/protocol/hooks。
func (c *Client) Hooks(ctx context.Context) (*Envelope, error) {
	return c.do(ctx, http.MethodGet, "/api/protocol/hooks", nil, nil)
}

// OpenAPI 取协议 OpenAPI 文档。GET /api/openapi.json（原生 JSON，无信封）。
func (c *Client) OpenAPI(ctx context.Context) (json.RawMessage, error) {
	return c.doRaw(ctx, http.MethodGet, "/api/openapi.json", nil, nil)
}

// Publish 发布可消费项。POST /api/publish，kind ∈ plan|text|handoff|deliverable|stop，
// idempotency_key 可选（重发同键按第一条生效）。
func (c *Client) Publish(ctx context.Context, kind, text, idem string) (json.RawMessage, error) {
	body := map[string]any{"kind": kind, "text": text}
	if idem != "" {
		body["idempotency_key"] = idem
	}
	return c.doNative(ctx, http.MethodPost, "/api/publish", nil, body)
}

// ListApprovals 列出待审批。GET /api/approvals。
func (c *Client) ListApprovals(ctx context.Context) (*Envelope, error) {
	return c.do(ctx, http.MethodGet, "/api/protocol/approvals", nil, nil)
}

// GetApproval 取单审批。GET /api/approvals/{id}。
func (c *Client) GetApproval(ctx context.Context, id string) (*Envelope, error) {
	return c.do(ctx, http.MethodGet, "/api/protocol/approvals/"+url.PathEscape(id), nil, nil)
}

// DecideApproval 审批裁决（payload 透传上游 DTO）。POST /api/approvals/{id}/decide。
func (c *Client) DecideApproval(ctx context.Context, id string, payload any) (*Envelope, error) {
	return c.do(ctx, http.MethodPost, "/api/protocol/approvals/"+url.PathEscape(id), nil, payload)
}

// ---------- Options（唯一显式下发面）----------

// GetOptions 取当前运行时配置。GET /api/options。
func (c *Client) GetOptions(ctx context.Context) (json.RawMessage, error) {
	return c.doNative(ctx, http.MethodGet, "/api/options", nil, nil)
}

// UpdateOptions 显式下发配置（部分字段）。PUT /api/options。
func (c *Client) UpdateOptions(ctx context.Context, fields map[string]any) (json.RawMessage, error) {
	return c.doNative(ctx, http.MethodPut, "/api/options", nil, fields)
}

// GetModelProfile 取档位。GET /api/options/model-profile。
func (c *Client) GetModelProfile(ctx context.Context) (json.RawMessage, error) {
	return c.doNative(ctx, http.MethodGet, "/api/options/model-profile", nil, nil)
}

// SetModelProfile 下发档位。PUT /api/options/model-profile，value 为档位名。
func (c *Client) SetModelProfile(ctx context.Context, value string) (json.RawMessage, error) {
	return c.doNative(ctx, http.MethodPut, "/api/options/model-profile", nil, map[string]any{"value": value})
}

// GetApprovalPolicy 取审批档。GET /api/options/approval-policy。
func (c *Client) GetApprovalPolicy(ctx context.Context) (json.RawMessage, error) {
	return c.doNative(ctx, http.MethodGet, "/api/options/approval-policy", nil, nil)
}

// SetApprovalPolicy 下发审批档。PUT /api/options/approval-policy。
func (c *Client) SetApprovalPolicy(ctx context.Context, value string) (json.RawMessage, error) {
	return c.doNative(ctx, http.MethodPut, "/api/options/approval-policy", nil, map[string]any{"value": value})
}

// SetHookConfig 下发钩子配置。PUT /api/options/hooks。
func (c *Client) SetHookConfig(ctx context.Context, cfg any) (json.RawMessage, error) {
	return c.doNative(ctx, http.MethodPut, "/api/options/hooks", nil, cfg)
}

// SetBudgetConfig 下发预算配置。PUT /api/options/budget。
func (c *Client) SetBudgetConfig(ctx context.Context, cfg any) (json.RawMessage, error) {
	return c.doNative(ctx, http.MethodPut, "/api/options/budget", nil, cfg)
}

// GetRTK 取 RTK 运行时状态。GET /api/options/rtk。
func (c *Client) GetRTK(ctx context.Context) (json.RawMessage, error) {
	return c.doNative(ctx, http.MethodGet, "/api/options/rtk", nil, nil)
}

// SetRTK 下发 RTK 开关。PUT /api/options/rtk。
func (c *Client) SetRTK(ctx context.Context, cfg any) (json.RawMessage, error) {
	return c.doNative(ctx, http.MethodPut, "/api/options/rtk", nil, cfg)
}

// ---------- 发布流 / 导出 ----------

// PublishStream 打开发布流 SSE：GET /api/publish/stream。
// 返回的 body 由调用方关闭；按 SSE 帧逐行读取。
func (c *Client) PublishStream(ctx context.Context) (io.ReadCloser, error) {
	u := c.base + "/api/publish/stream"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "text/event-stream")
	if c.key != "" {
		req.Header.Set("X-API-Key", c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("artexsdk: stream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp.Body, nil
}

// ExportTranscript 导出转录归档流。GET /api/export/transcript。
func (c *Client) ExportTranscript(ctx context.Context) (io.ReadCloser, error) {
	u := c.base + "/api/export/transcript"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	if c.key != "" {
		req.Header.Set("X-API-Key", c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("artexsdk: export HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp.Body, nil
}

// PollExport 轮询导出进度。GET /api/export/{id}。
func (c *Client) PollExport(ctx context.Context, id string) (json.RawMessage, error) {
	return c.doNative(ctx, http.MethodGet, "/api/export/"+url.PathEscape(id), nil, nil)
}

// Stream 便捷逐行回调（每读一行调用 fn；fn 返回 false 即停）。
func (c *Client) Stream(ctx context.Context, path string, fn func(line []byte) bool) error {
	u := c.base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "text/event-stream")
	if c.key != "" {
		req.Header.Set("X-API-Key", c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("artexsdk: stream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		for {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				break
			}
			line := buf[:i]
			buf = buf[i+1:]
			if !fn(line) {
				return nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// DefaultTimeout 是默认请求超时建议值（调用方自建 context 时参考）。
const DefaultTimeout = 30 * time.Second

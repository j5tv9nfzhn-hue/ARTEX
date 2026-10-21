package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// Server 是一个无状态的 MCP 服务器：每次 Handle 都是独立请求，
// 不在内存里保存会话，因此 stdio 与 HTTP 可以共享同一实例。
type Server struct {
	cfg Config

	tools  []Tool
	byName map[string]Tool

	tiers map[Tier]bool
	allow map[string]bool
	deny  map[string]bool
}

// New 构造服务器。cfg.Tools 里 Tier 不在 cfg.Tiers 中的工具会被过滤掉；
// 重复的工具名保留第一个（后一个视为配置错误被忽略）。
func New(cfg Config) *Server {
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "artex"
	}
	if strings.TrimSpace(cfg.Version) == "" {
		cfg.Version = "dev"
	}
	s := &Server{
		cfg:    cfg,
		byName: map[string]Tool{},
		tiers:  map[Tier]bool{},
		allow:  map[string]bool{},
		deny:   map[string]bool{},
	}
	for _, t := range cfg.Tiers {
		s.tiers[t] = true
	}
	if len(cfg.Tiers) == 0 {
		s.tiers[L0] = true
	}
	for _, n := range cfg.AllowTools {
		if n = strings.TrimSpace(n); n != "" {
			s.allow[n] = true
		}
	}
	for _, n := range cfg.DenyTools {
		if n = strings.TrimSpace(n); n != "" {
			s.deny[n] = true
		}
	}
	for _, t := range cfg.Tools {
		if strings.TrimSpace(t.Name) == "" || t.Handler == nil {
			continue
		}
		if _, dup := s.byName[t.Name]; dup {
			continue
		}
		s.tools = append(s.tools, t)
		s.byName[t.Name] = t
	}
	sort.Slice(s.tools, func(i, j int) bool { return s.tools[i].Name < s.tools[j].Name })
	return s
}

// Visible 判断某个工具是否在当前配置下对外可见。
func (s *Server) Visible(name string) bool {
	t, ok := s.byName[name]
	if !ok {
		return false
	}
	return s.visible(t)
}

func (s *Server) visible(t Tool) bool {
	if !s.tiers[t.Tier] {
		return false
	}
	if s.deny[t.Name] {
		return false
	}
	if len(s.allow) > 0 && !s.allow[t.Name] {
		return false
	}
	return true
}

// Tools 返回当前可见工具（按名字排序），供界面展示与测试断言。
func (s *Server) Tools() []Tool {
	out := make([]Tool, 0, len(s.tools))
	for _, t := range s.tools {
		if s.visible(t) {
			out = append(out, t)
		}
	}
	return out
}

// HandleFrame 处理一帧原始 JSON（单条或批量），返回可直接序列化的响应；
// 全是通知时返回 nil。stdio 与 HTTP 都调用它，保证两种传输行为一致。
func (s *Server) HandleFrame(ctx context.Context, frame []byte) any {
	frame = bytes.TrimSpace(frame)
	if len(frame) == 0 {
		return nil
	}
	if frame[0] == '[' {
		var reqs []Request
		if err := json.Unmarshal(frame, &reqs); err != nil {
			return errorResponse(nil, CodeParseError, "invalid JSON: "+err.Error())
		}
		out := make([]Response, 0, len(reqs))
		for _, req := range reqs {
			if resp := s.handleOne(ctx, req); resp != nil {
				out = append(out, *resp)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	var req Request
	if err := json.Unmarshal(frame, &req); err != nil {
		return errorResponse(nil, CodeParseError, "invalid JSON: "+err.Error())
	}
	resp := s.handleOne(ctx, req)
	if resp == nil {
		return nil
	}
	return resp
}

// handleOne 处理单条请求；通知（无 id）返回 nil。
func (s *Server) handleOne(ctx context.Context, req Request) *Response {
	if strings.TrimSpace(req.Method) == "" {
		// 客户端回给服务器的 response（我们没有服务端发起的请求）或垃圾帧：忽略。
		return nil
	}
	if !hasID(req.ID) {
		// 通知不需要结果，但仍允许它触发副作用（目前只有 initialized / cancelled）。
		_, _ = s.Handle(ctx, req.Method, req.Params)
		return nil
	}
	result, rerr := s.Handle(ctx, req.Method, req.Params)
	if rerr != nil {
		return &Response{JSONRPC: "2.0", ID: req.ID, Error: rerr}
	}
	return &Response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

// Handle 分发一个 JSON-RPC 方法。返回 (result, nil) 或 (nil, rpcError)。
func (s *Server) Handle(ctx context.Context, method string, params json.RawMessage) (any, *Error) {
	switch method {
	case "initialize":
		return s.initialize(params), nil
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.toolList()}, nil
	case "tools/call":
		return s.callTool(ctx, params)
	case "resources/list":
		if s.cfg.ReadResource == nil {
			return nil, &Error{Code: CodeMethodNotFound, Message: "resources not supported"}
		}
		return map[string]any{"resources": s.resourceList()}, nil
	case "resources/read":
		return s.readResource(ctx, params)
	case "prompts/list":
		if s.cfg.GetPrompt == nil {
			return nil, &Error{Code: CodeMethodNotFound, Message: "prompts not supported"}
		}
		return map[string]any{"prompts": s.promptList()}, nil
	case "prompts/get":
		return s.getPrompt(ctx, params)
	}
	return nil, &Error{Code: CodeMethodNotFound, Message: "method not found: " + method}
}

// initialize 结果。协议版本按客户端声明协商，未知版本回我们的首选版本，
// 由客户端决定是否继续（规范允许服务端返回自己支持的最高版本）。
func (s *Server) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := ProtocolVersion
	if supportedProtocols[strings.TrimSpace(p.ProtocolVersion)] {
		version = strings.TrimSpace(p.ProtocolVersion)
	}
	caps := map[string]any{
		"tools": map[string]any{"listChanged": false},
	}
	if s.cfg.ReadResource != nil {
		caps["resources"] = map[string]any{"subscribe": false, "listChanged": false}
	}
	if s.cfg.GetPrompt != nil {
		caps["prompts"] = map[string]any{"listChanged": false}
	}
	out := map[string]any{
		"protocolVersion": version,
		"capabilities":    caps,
		"serverInfo": map[string]any{
			"name":    s.cfg.Name,
			"title":   s.cfg.Title,
			"version": s.cfg.Version,
		},
	}
	if s.cfg.Instructions != "" {
		out["instructions"] = s.cfg.Instructions
	}
	return out
}

// mcpTool 是 tools/list 里单个工具的形状。
type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func (s *Server) toolList() []mcpTool {
	out := make([]mcpTool, 0, len(s.tools))
	for _, t := range s.Tools() {
		schema := t.Schema
		if len(schema) == 0 {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, mcpTool{
			Name:        t.Name,
			Title:       t.Title,
			Description: t.Description,
			InputSchema: schema,
			Annotations: map[string]any{"readOnlyHint": t.ReadOnly},
		})
	}
	return out
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *Error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: "invalid tools/call params: " + err.Error()}
	}
	t, ok := s.byName[p.Name]
	if !ok || !s.visible(t) {
		return nil, &Error{Code: CodeInvalidParams, Message: "unknown tool: " + p.Name}
	}
	args := bytes.TrimSpace(p.Arguments)
	if len(args) == 0 || bytes.Equal(args, []byte("null")) {
		args = json.RawMessage(`{}`)
	}
	cctx := ctx
	if s.cfg.ToolTimeout > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, s.cfg.ToolTimeout)
		defer cancel()
	}
	start := time.Now()
	text, err := t.Handler(cctx, args)
	s.audit(t, args, time.Since(start), err)
	if err != nil {
		// 工具执行失败按规范放进 result.isError，让模型看得见原因，
		// 而不是当成协议错误（那会让客户端直接断开）。
		msg := err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "工具执行超时: " + t.Name
		} else if errors.Is(err, context.Canceled) {
			msg = "工具调用已取消: " + t.Name
		}
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": msg}},
			"isError": true,
		}, nil
	}
	if strings.TrimSpace(text) == "" {
		text = "（工具没有返回内容）"
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}, nil
}

func (s *Server) audit(t Tool, args json.RawMessage, d time.Duration, err error) {
	if s.cfg.Audit == nil {
		return
	}
	s.cfg.Audit(AuditEvent{Tool: t.Name, Tier: t.Tier, Args: args, Duration: d, Err: err})
}

func (s *Server) resourceList() []map[string]any {
	out := make([]map[string]any, 0, len(s.cfg.Resources))
	for _, r := range s.cfg.Resources {
		item := map[string]any{"uri": r.URI, "name": r.Name}
		if r.Title != "" {
			item["title"] = r.Title
		}
		if r.Description != "" {
			item["description"] = r.Description
		}
		if r.MIMEType != "" {
			item["mimeType"] = r.MIMEType
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) readResource(ctx context.Context, params json.RawMessage) (any, *Error) {
	if s.cfg.ReadResource == nil {
		return nil, &Error{Code: CodeMethodNotFound, Message: "resources not supported"}
	}
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: "invalid resources/read params"}
	}
	mime, text, err := s.cfg.ReadResource(ctx, p.URI)
	if err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
	}
	if mime == "" {
		mime = "application/json"
	}
	return map[string]any{
		"contents": []map[string]any{{"uri": p.URI, "mimeType": mime, "text": text}},
	}, nil
}

func (s *Server) promptList() []map[string]any {
	out := make([]map[string]any, 0, len(s.cfg.Prompts))
	for _, p := range s.cfg.Prompts {
		item := map[string]any{"name": p.Name}
		if p.Title != "" {
			item["title"] = p.Title
		}
		if p.Description != "" {
			item["description"] = p.Description
		}
		if len(p.Arguments) > 0 {
			args := make([]map[string]any, 0, len(p.Arguments))
			for _, a := range p.Arguments {
				m := map[string]any{"name": a.Name, "required": a.Required}
				if a.Description != "" {
					m["description"] = a.Description
				}
				args = append(args, m)
			}
			item["arguments"] = args
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) getPrompt(ctx context.Context, params json.RawMessage) (any, *Error) {
	if s.cfg.GetPrompt == nil {
		return nil, &Error{Code: CodeMethodNotFound, Message: "prompts not supported"}
	}
	var p struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: "invalid prompts/get params"}
	}
	text, err := s.cfg.GetPrompt(ctx, p.Name, p.Arguments)
	if err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
	}
	desc := ""
	for _, pr := range s.cfg.Prompts {
		if pr.Name == p.Name {
			desc = pr.Description
			break
		}
	}
	return map[string]any{
		"description": desc,
		"messages": []map[string]any{{
			"role":    "user",
			"content": map[string]any{"type": "text", "text": text},
		}},
	}, nil
}

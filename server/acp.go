// acp.go — Agent Client Protocol（ACP）对接层（P2：ACP 协议对接）。
// 纯增量：协议状态机 + stdio JSON-RPC 传输 + 会话/审批/取消状态机；
// 引擎对接经 AcpRunner 函数适配器注入（AttachRunner），未注入时 prompt 返回
// 明确错误（fail-secure），既有执行路径零改动，可零成本回滚。
//
// 协议形态（对齐 agentclientprotocol.com 的核心面）：
//
//	请求：initialize / session/new / session/prompt / session/cancel /
//	      session/request_permission 的响应（permission response）
//	通知：session/update（thought/tool_call/tool_call_update/agent_message_chunk/
//	      plan/agent_thought_chunk）、session/request_permission
//	能力协商：fs.readTextFile / fs.writeTextFile（登记位；文件面权限仍走 G2 出范围）。
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// AcpProtocolVersion 是协议版本标识。
const AcpProtocolVersion = "acp/1"

// ---------- JSON-RPC 信封 ----------

type acpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type acpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type acpNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// ---------- 会话状态机 ----------

// AcpSessionState 是会话生命周期状态。
type AcpSessionState string

const (
	AcpSessionIdle    AcpSessionState = "idle"
	AcpSessionBusy    AcpSessionState = "busy"
	AcpSessionStopped AcpSessionState = "stopped"
)

// AcpSession 是单个 ACP 会话（读写加锁）。
type AcpSession struct {
	mu        sync.Mutex
	ID        string
	Cwd       string
	state     AcpSessionState
	cancel    context.CancelCauseFunc
	lastRound int64
}

// State 返回当前状态。
func (s *AcpSession) State() AcpSessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// ---------- Runner / 权限适配器 ----------

// AcpEvent 是一次会话事件（转成 session/update 通知）。
type AcpEvent struct {
	Kind    string // thought | tool_call | tool_call_update | agent_message_chunk | plan | agent_thought_chunk
	Content string
	ToolID  string
	Meta    map[string]any
}

// AcpRunner 是引擎对接面：跑一轮 prompt 并以 onEvent 回传事件流；Cancel 须可打断。
// 用函数结构体实现注入（如把 prompt 排进既有队列/对话通路），本文件不绑定引擎内部 API。
type AcpRunner interface {
	RunPrompt(ctx context.Context, sessionID, prompt string, onEvent func(AcpEvent)) error
	Cancel(sessionID string) error
}

// AcpFuncRunner 把函数适配成 AcpRunner。
type AcpFuncRunner struct {
	RunFn    func(ctx context.Context, sessionID, prompt string, onEvent func(AcpEvent)) error
	CancelFn func(sessionID string) error
}

func (f *AcpFuncRunner) RunPrompt(ctx context.Context, sessionID, prompt string, onEvent func(AcpEvent)) error {
	if f.RunFn == nil {
		return fmt.Errorf("ACP runner 未注入引擎对接函数")
	}
	return f.RunFn(ctx, sessionID, prompt, onEvent)
}

func (f *AcpFuncRunner) Cancel(sessionID string) error {
	if f.CancelFn == nil {
		return nil
	}
	return f.CancelFn(sessionID)
}

// AcpPermissionRequest 是 session/request_permission 的请求体。
type AcpPermissionRequest struct {
	SessionID string   `json:"session_id"`
	ToolCall  string   `json:"tool_call"`
	Title     string   `json:"title"`
	Reason    string   `json:"reason"`
	Options   []string `json:"options"` // allow_once | allow_always | reject_once | reject_always
}

// AcpPermissionDecision 是权限裁决。
type AcpPermissionDecision struct {
	Outcome string `json:"outcome"` // allow_once | allow_always | reject_once | reject_always
}

// AcpPermissionHandler 是审批对接面（可接到既有审批治理；未注入 = fail-secure 拒绝）。
type AcpPermissionHandler func(ctx context.Context, req AcpPermissionRequest) (AcpPermissionDecision, error)

// ---------- 服务器 ----------

// AcpServer 是 ACP 协议服务器（传输无关）。
type AcpServer struct {
	mu       sync.RWMutex
	runner   AcpRunner
	perm     AcpPermissionHandler
	sessions map[string]*AcpSession
	nextID   atomic.Int64
}

// SharedAcp 是进程级 ACP 服务器。
var SharedAcp = NewAcpServer()

// NewAcpServer 构造空状态机。
func NewAcpServer() *AcpServer {
	return &AcpServer{sessions: map[string]*AcpSession{}}
}

// AttachRunner 注入引擎对接函数（任务级 prompt 通路）。
func (a *AcpServer) AttachRunner(r AcpRunner) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.runner = r
}

// AttachPermissionHandler 注入审批对接函数（接到既有审批治理）。
func (a *AcpServer) AttachPermissionHandler(h AcpPermissionHandler) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.perm = h
}

// Session 返回既有会话。
func (a *AcpServer) Session(id string) (*AcpSession, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s, ok := a.sessions[id]
	return s, ok
}

// Handle 处理一条请求/通知，返回响应（通知返回 nil）与事件期间发出的通知。
// notify 回调可为 nil（此时通知被丢弃，测试用收集器注入）。
func (a *AcpServer) Handle(ctx context.Context, raw []byte, notify func(acpNotification)) (*acpResponse, error) {
	if notify == nil {
		notify = func(acpNotification) {}
	}
	var req acpRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("bad json-rpc frame: %w", err)
	}
	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		return a.onInitialize(req, isNotification)
	case "session/new":
		return a.onSessionNew(req, isNotification)
	case "session/prompt":
		return a.onSessionPrompt(ctx, req, isNotification, notify)
	case "session/cancel":
		return a.onSessionCancel(req, isNotification)
	case "session/request_permission":
		// 客户端回传的权限裁决：登记到审批对接面（若已注入）。
		return a.onPermissionResponse(req, isNotification)
	default:
		if isNotification {
			return nil, nil
		}
		return errResp(req.ID, -32601, "unknown method: "+req.Method), nil
	}
}

func errResp(id json.RawMessage, code int, msg string) *acpResponse {
	return &acpResponse{JSONRPC: "2.0", ID: id, Error: &acpError{Code: code, Message: msg}}
}

func okResp(id json.RawMessage, result any) *acpResponse {
	return &acpResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func (a *AcpServer) onInitialize(req acpRequest, note bool) (*acpResponse, error) {
	result := map[string]any{
		"protocol_version": AcpProtocolVersion,
		"agent_capabilities": map[string]any{
			"prompt_capabilities": map[string]any{
				"image":            false,
				"audio":            false,
				"embedded_context": true,
			},
			"fs": map[string]any{
				"text_document": false, // 文件面读写属 G2 权限引擎，出范围：登记能力但不启用
			},
		},
		"auth_methods": []any{},
	}
	if note {
		return nil, nil
	}
	return okResp(req.ID, result), nil
}

func (a *AcpServer) onSessionNew(req acpRequest, note bool) (*acpResponse, error) {
	var p struct {
		Cwd string `json:"cwd"`
	}
	_ = json.Unmarshal(req.Params, &p)
	id := fmt.Sprintf("acp-%d", a.nextID.Add(1))
	s := &AcpSession{ID: id, Cwd: p.Cwd, state: AcpSessionIdle}
	a.mu.Lock()
	a.sessions[id] = s
	a.mu.Unlock()
	if note {
		return nil, nil
	}
	return okResp(req.ID, map[string]any{"session_id": id, "state": string(AcpSessionIdle)}), nil
}

func (a *AcpServer) onSessionPrompt(ctx context.Context, req acpRequest, note bool, notify func(acpNotification)) (*acpResponse, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Prompt    []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Content string `json:"content"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(req.ID, -32602, "bad params: "+err.Error()), nil
	}
	text := ""
	for _, chunk := range p.Prompt {
		if chunk.Text != "" {
			text += chunk.Text
		} else if chunk.Content != "" {
			text += chunk.Content
		}
	}
	if text == "" {
		return errResp(req.ID, -32602, "prompt 必填"), nil
	}

	a.mu.RLock()
	s, ok := a.sessions[p.SessionID]
	runner := a.runner
	a.mu.RUnlock()
	if !ok {
		return errResp(req.ID, -32601, "session not found: "+p.SessionID), nil
	}

	s.mu.Lock()
	if s.state == AcpSessionBusy {
		s.mu.Unlock()
		return errResp(req.ID, -32000, "session busy；请先 session/cancel"), nil
	}
	if s.state == AcpSessionStopped {
		s.mu.Unlock()
		return errResp(req.ID, -32000, "session stopped"), nil
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	s.state = AcpSessionBusy
	s.cancel = cancel
	s.lastRound = time.Now().UnixNano()
	s.mu.Unlock()

	if runner == nil {
		s.mu.Lock()
		s.state = AcpSessionIdle
		s.cancel = nil
		s.mu.Unlock()
		return errResp(req.ID, -32000, "ACP runner 未注入引擎对接函数（AttachRunner）"), nil
	}

	// 事件流 → session/update 通知（同 goroutine 顺序发出）。
	onEvent := func(ev AcpEvent) {
		notify(acpNotification{JSONRPC: "2.0", Method: "session/update", Params: map[string]any{
			"session_id": s.ID,
			"update":     map[string]any{"kind": ev.Kind, "content": ev.Content, "tool_call_id": ev.ToolID, "meta": ev.Meta},
		}})
	}

	runErr := runner.RunPrompt(runCtx, s.ID, text, onEvent)

	s.mu.Lock()
	s.state = AcpSessionIdle
	s.cancel = nil
	s.mu.Unlock()

	if runErr != nil && runCtx.Err() == nil {
		return errResp(req.ID, -32000, "prompt 执行失败: "+runErr.Error()), nil
	}
	return okResp(req.ID, map[string]any{"stop_reason": "end_turn"}), nil
}

func (a *AcpServer) onSessionCancel(req acpRequest, note bool) (*acpResponse, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(req.Params, &p)
	a.mu.RLock()
	s, ok := a.sessions[p.SessionID]
	runner := a.runner
	a.mu.RUnlock()
	if !ok {
		return errResp(req.ID, -32601, "session not found: "+p.SessionID), nil
	}
	s.mu.Lock()
	cancel := s.cancel
	s.state = AcpSessionIdle
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel(context.Canceled)
	}
	if runner != nil {
		_ = runner.Cancel(p.SessionID)
	}
	if note {
		return nil, nil
	}
	return okResp(req.ID, map[string]any{"cancelled": true}), nil
}

func (a *AcpServer) onPermissionResponse(req acpRequest, note bool) (*acpResponse, error) {
	var p struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
		Outcome   string `json:"outcome"`
	}
	_ = json.Unmarshal(req.Params, &p)
	// 裁决落地：目前登记为响应确认；对接既有审批治理时由 AttachPermissionHandler 消费。
	a.mu.RLock()
	perm := a.perm
	a.mu.RUnlock()
	if perm != nil {
		_, _ = perm(context.Background(), AcpPermissionRequest{
			SessionID: p.SessionID, ToolCall: p.RequestID, Title: "acp.permission", Reason: p.Outcome,
			Options: []string{p.Outcome},
		})
	}
	if note {
		return nil, nil
	}
	return okResp(req.ID, map[string]any{"ack": true}), nil
}

// RequestPermission 发起一次 session/request_permission 通知并等裁决（阻塞至 ctx 结束或 decide 被调用）。
// 服务端主动请求权限时使用；测试中与 Handle 并发配合。
func (a *AcpServer) RequestPermission(ctx context.Context, notify func(acpNotification), req AcpPermissionRequest, decide chan<- AcpPermissionDecision) {
	notify(acpNotification{JSONRPC: "2.0", Method: "session/request_permission", Params: req})
	a.mu.RLock()
	perm := a.perm
	a.mu.RUnlock()
	if perm == nil {
		// fail-secure：无审批对接 = 默认拒绝。
		select {
		case decide <- AcpPermissionDecision{Outcome: "reject_once"}:
		case <-ctx.Done():
		}
		return
	}
	d, err := perm(ctx, req)
	if err != nil {
		d = AcpPermissionDecision{Outcome: "reject_once"}
	}
	select {
	case decide <- d:
	case <-ctx.Done():
	}
}

// ---------- stdio 传输 ----------

// Serve 以 JSON-RPC over stream 服务（stdio 场景：in=os.Stdin, out=os.Stdout）。
// 一行一报文；bufio.Scanner 默认 64K 行上限，这里扩到 1MB。
func (a *AcpServer) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	enc := json.NewEncoder(out)
	var writeMu sync.Mutex
	notify := func(n acpNotification) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = enc.Encode(n)
	}
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		resp, err := a.Handle(ctx, line, notify)
		if err != nil {
			// 帧解码失败：回 JSON-RPC parse error，不中断服务。
			writeMu.Lock()
			_ = enc.Encode(acpResponse{JSONRPC: "2.0", Error: &acpError{Code: -32700, Message: err.Error()}})
			writeMu.Unlock()
			continue
		}
		if resp != nil {
			writeMu.Lock()
			_ = enc.Encode(resp)
			writeMu.Unlock()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	return sc.Err()
}

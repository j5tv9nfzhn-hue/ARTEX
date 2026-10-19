package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu        sync.Mutex
	rounds    []string
	events    int
	cancelled []string
	block     chan struct{}
}

func (f *fakeRunner) RunPrompt(ctx context.Context, sessionID, prompt string, onEvent func(AcpEvent)) error {
	f.mu.Lock()
	f.rounds = append(f.rounds, prompt)
	f.mu.Unlock()
	onEvent(AcpEvent{Kind: "agent_message_chunk", Content: "echo:" + prompt})
	onEvent(AcpEvent{Kind: "plan", Content: "step-1"})
	f.mu.Lock()
	f.events += 2
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (f *fakeRunner) Cancel(sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, sessionID)
	return nil
}

func mustFrame(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAcpInitializeAndSessionFlow(t *testing.T) {
	a := NewAcpServer()
	fr := &fakeRunner{}
	a.AttachRunner(fr)
	ctx := context.Background()

	var notes []acpNotification
	collect := func(n acpNotification) { notes = append(notes, n) }

	resp, err := a.Handle(ctx, mustFrame(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocol_version": AcpProtocolVersion},
	}), collect)
	if err != nil || resp == nil || resp.Error != nil {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}

	resp, err = a.Handle(ctx, mustFrame(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/new",
		"params": map[string]any{"cwd": "C:/work"},
	}), collect)
	if err != nil || resp.Error != nil {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	raw, _ := json.Marshal(resp.Result)
	var created struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(raw, &created)
	if created.SessionID == "" {
		t.Fatal("no session id")
	}

	resp, err = a.Handle(ctx, mustFrame(t, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
		"params": map[string]any{
			"session_id": created.SessionID,
			"prompt":     []map[string]any{{"type": "text", "text": "你好"}},
		},
	}), collect)
	if err != nil || resp.Error != nil {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if len(fr.rounds) != 1 || fr.rounds[0] != "你好" {
		t.Fatalf("rounds=%v", fr.rounds)
	}
	if len(notes) != 2 {
		t.Fatalf("notes=%d", len(notes))
	}
	for _, n := range notes {
		if n.Method != "session/update" {
			t.Fatalf("note=%+v", n)
		}
	}

	s, ok := a.Session(created.SessionID)
	if !ok || s.State() != AcpSessionIdle {
		t.Fatalf("state=%v", s.State())
	}
}

func TestAcpBusyRejectAndCancel(t *testing.T) {
	a := NewAcpServer()
	fr := &fakeRunner{block: make(chan struct{})}
	a.AttachRunner(fr)
	ctx := context.Background()

	resp, _ := a.Handle(ctx, mustFrame(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "session/new", "params": map[string]any{},
	}), nil)
	raw, _ := json.Marshal(resp.Result)
	var created struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(raw, &created)

	done := make(chan *acpResponse, 1)
	go func() {
		r, _ := a.Handle(ctx, mustFrame(t, map[string]any{
			"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
			"params": map[string]any{"session_id": created.SessionID,
				"prompt": []map[string]any{{"type": "text", "text": "run"}}},
		}), nil)
		done <- r
	}()

	// 等到 busy 再打第二发
	deadline := time.Now().Add(2 * time.Second)
	for {
		if s, _ := a.Session(created.SessionID); s != nil && s.State() == AcpSessionBusy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session never busy")
		}
		time.Sleep(5 * time.Millisecond)
	}

	resp2, _ := a.Handle(ctx, mustFrame(t, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
		"params": map[string]any{"session_id": created.SessionID,
			"prompt": []map[string]any{{"type": "text", "text": "dup"}}},
	}), nil)
	if resp2 == nil || resp2.Error == nil || !strings.Contains(resp2.Error.Message, "busy") {
		t.Fatalf("expected busy error, got %+v", resp2)
	}

	resp3, _ := a.Handle(ctx, mustFrame(t, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "session/cancel",
		"params": map[string]any{"session_id": created.SessionID},
	}), nil)
	if resp3.Error != nil {
		t.Fatalf("cancel err: %+v", resp3.Error)
	}
	select {
	case r := <-done:
		if r == nil || r.Error != nil {
			t.Fatalf("prompt after cancel should end via ctx, got %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prompt did not finish")
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.cancelled) != 1 {
		t.Fatalf("cancelled=%v", fr.cancelled)
	}
}

func TestAcpRunnerNotAttachedFailSecure(t *testing.T) {
	a := NewAcpServer()
	ctx := context.Background()
	resp, _ := a.Handle(ctx, mustFrame(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "session/new", "params": map[string]any{},
	}), nil)
	raw, _ := json.Marshal(resp.Result)
	var created struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(raw, &created)

	resp2, _ := a.Handle(ctx, mustFrame(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
		"params": map[string]any{"session_id": created.SessionID,
			"prompt": []map[string]any{{"type": "text", "text": "x"}}},
	}), nil)
	if resp2.Error == nil || !strings.Contains(resp2.Error.Message, "未注入") {
		t.Fatalf("resp=%+v", resp2)
	}
}

func TestAcpPermissionFailSecure(t *testing.T) {
	a := NewAcpServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	decide := make(chan AcpPermissionDecision, 1)
	go a.RequestPermission(ctx, func(acpNotification) {}, AcpPermissionRequest{ToolCall: "t1"}, decide)
	d := <-decide
	if d.Outcome != "reject_once" {
		t.Fatalf("decision=%+v", d)
	}

	// 注入 handler 后走 handler 裁决
	a.AttachPermissionHandler(func(ctx context.Context, req AcpPermissionRequest) (AcpPermissionDecision, error) {
		return AcpPermissionDecision{Outcome: "allow_once"}, nil
	})
	decide2 := make(chan AcpPermissionDecision, 1)
	go a.RequestPermission(ctx, func(acpNotification) {}, AcpPermissionRequest{ToolCall: "t2"}, decide2)
	d2 := <-decide2
	if d2.Outcome != "allow_once" {
		t.Fatalf("decision=%+v", d2)
	}
}

func TestAcpServeStream(t *testing.T) {
	a := NewAcpServer()
	a.AttachRunner(&fakeRunner{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}` + "\n")
	var out bytes.Buffer
	if err := a.Serve(ctx, in, &out); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		// reader EOF → Serve 正常返回
		if err.Error() != "" && !strings.Contains(err.Error(), "EOF") {
			t.Logf("serve returned: %v", err)
		}
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("out=%q", out.String())
	}
	var r1 map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &r1); err != nil {
		t.Fatal(err)
	}
	if r1["jsonrpc"] != "2.0" {
		t.Fatalf("frame=%v", r1)
	}
}

func TestAcpUnknownMethod(t *testing.T) {
	a := NewAcpServer()
	resp, _ := a.Handle(context.Background(), mustFrame(t, map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "no/such",
	}), nil)
	if resp == nil || resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("resp=%+v", resp)
	}
}

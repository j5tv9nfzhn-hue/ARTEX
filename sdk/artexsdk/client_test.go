package artexsdk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func envJSON(resource, action, id string, data any) []byte {
	b, _ := json.Marshal(map[string]any{
		"protocol": ProtoVersion, "resource": resource, "action": action, "id": id, "data": data,
	})
	return b
}

func TestEnvelopeAndListThreads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/protocol/threads" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("after") != "3" || r.URL.Query().Get("limit") != "10" {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		if r.Header.Get("X-API-Key") != "k1" {
			t.Errorf("missing api key header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(envJSON("threads", "list", "", map[string]any{
			"threads": []map[string]any{
				{"id": "task-1", "state": "planning", "active": true},
				{"id": "task-2", "state": "paused"},
			},
			"next_cursor": "2",
		}))
	}))
	defer srv.Close()

	c, err := New(srv.URL, WithAPIKey("k1"))
	if err != nil {
		t.Fatal(err)
	}
	threads, cursor, err := c.ListThreads(context.Background(), 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 || threads[0].ID != "task-1" || !threads[0].Active || cursor != "2" {
		t.Fatalf("threads=%+v cursor=%q", threads, cursor)
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		w.Write(envJSON("thread", "create", "", map[string]any{"error": "bad json: x"}))
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	_, err := c.CreateThread(context.Background(), map[string]any{"title": "x"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 || !strings.Contains(ae.Message, "bad json") {
		t.Fatalf("err=%v", err)
	}
}

func TestOptionsNativeRaw(t *testing.T) {
	var putSeen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/options":
			w.Write([]byte(`{"speed_level":2}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/options/model-profile":
			putSeen = true
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["value"] != "deep" {
				t.Errorf("body=%v", body)
			}
			w.Write([]byte(`{"value":"deep"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	raw, err := c.GetOptions(context.Background())
	if err != nil || !strings.Contains(string(raw), "speed_level") {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
	if _, err := c.SetModelProfile(context.Background(), "deep"); err != nil {
		t.Fatal(err)
	}
	if !putSeen {
		t.Fatal("model-profile PUT not sent")
	}
}

func TestPublishAndKill(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/publish":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["kind"] != "plan" || body["idempotency_key"] != "idem-1" {
				t.Errorf("body=%v", body)
			}
			w.Write([]byte(`{"id":"item-9"}`))
		case "/api/protocol/threads/task-7/kill":
			w.Write(envJSON("thread", "kill", "task-7", map[string]any{"killed": true}))
		case "/api/protocol/threads/task-7/tree":
			w.Write(envJSON("thread", "tree", "task-7", map[string]any{"checkpoints": []any{}}))
		case "/api/protocol/approvals":
			w.Write(envJSON("approval", "list", "", map[string]any{"items": []any{}}))
		case "/api/protocol/approvals/ap-1":
			w.Write(envJSON("approval", "decide", "ap-1", map[string]any{"decided": true}))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	if _, err := c.Publish(context.Background(), "plan", "执行计划", "idem-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Kill(context.Background(), "task-7"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetThreadTree(context.Background(), "task-7"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListApprovals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DecideApproval(context.Background(), "ap-1", map[string]any{"decision": "approve"}); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"protocol":"v9","resource":"x","action":"y","id":"","data":{}}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	if _, err := c.GetThread(context.Background(), "task-1"); err == nil || !strings.Contains(err.Error(), "protocol mismatch") {
		t.Fatalf("err=%v", err)
	}
}

func TestStreamLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/publish/stream" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: one\n\ndata: two\n\n")
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lines []string
	err := c.Stream(ctx, "/api/publish/stream", func(line []byte) bool {
		lines = append(lines, string(line))
		return len(lines) < 4
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 4 {
		t.Fatalf("lines=%v", lines)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error for empty base")
	}
	if _, err := New("http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
}

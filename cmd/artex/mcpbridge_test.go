package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeConsole 模拟控制台的 /mcp：认 server/discover、tools/list、tools/call。
func fakeConsole(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"invalid token"}}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "server/discover":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"artex","version":"test"},"protocolVersions":["2026-07-28"]}}`))
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{},{}]}}`))
		case "tools/call":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":"bridge-1","result":{"content":[{"type":"text","text":"from-console:%s"}]}}`, req.Params.Name)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
}

func TestConsoleL3BridgeForwards(t *testing.T) {
	srv := fakeConsole(t, "tok")
	defer srv.Close()

	bridge := consoleL3Bridge("tok", srv.URL)
	out, err := bridge(context.Background(), "task_status", json.RawMessage(`{"id":1}`))
	if err != nil {
		t.Fatalf("转发失败: %v", err)
	}
	if !strings.Contains(out, "from-console:task_status") {
		t.Fatalf("结果没有透传: %q", out)
	}
}

func TestConsoleL3BridgeExplainsWhenConsoleDown(t *testing.T) {
	srv := fakeConsole(t, "tok")
	url := srv.URL
	srv.Close() // 控制台「没在跑」

	bridge := consoleL3Bridge("tok", url)
	_, err := bridge(context.Background(), "task_create", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("控制台不在时应当报错")
	}
	msg := err.Error()
	// 报错必须说清「为什么」和「怎么办」，而不是一句 connection refused。
	for _, want := range []string{"需要 ARTEX 控制台", "headless"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息缺少 %q: %s", want, msg)
		}
	}
}

func TestConsoleL3BridgeRejectsWrongToken(t *testing.T) {
	srv := fakeConsole(t, "right-token")
	defer srv.Close()

	bridge := consoleL3Bridge("wrong-token", srv.URL)
	_, err := bridge(context.Background(), "task_status", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("token 不匹配时应当报错")
	}
	if !strings.Contains(err.Error(), "token") && !strings.Contains(err.Error(), "401") {
		t.Errorf("错误信息应提到 token：%v", err)
	}
}

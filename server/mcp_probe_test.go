package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/mcpserver"
)

// 探活与附着的端到端验收：真实 Handler + 真实工具清单 + 真实 Bearer 校验。
//
// 这里不复用 httptest 的假服务端，就是要证明「照着 /mcp 的真实行为探活」
// 是成立的——否则 S1 只是又一个自己跟自己玩的单测。

func probeTestHandler(t *testing.T) http.Handler {
	t.Helper()
	s := newMCPTestServer(t)
	return s.buildMCPServer().HTTPHandler(mcpserver.HTTPConfig{
		Authorize: func(r *http.Request) error {
			if strings.TrimSpace(r.Header.Get("Authorization")) != "Bearer probe-token" {
				return errors.New("invalid token")
			}
			return nil
		},
		MaxBody: 8 << 20,
	})
}

// TestMCPProbeAgainstRealServer：status 能报出「活着 + 名称 + 协议版本 + 工具数」。
func TestMCPProbeAgainstRealServer(t *testing.T) {
	ts := httptest.NewServer(probeTestHandler(t))
	defer ts.Close()

	info := mcpserver.Probe(context.Background(),
		mcpserver.Endpoint{URL: ts.URL, Token: "probe-token"}, 3*time.Second)
	if !info.Alive {
		t.Fatalf("应对真实服务端探活成功: %+v", info)
	}
	if info.Name != "artex" {
		t.Fatalf("serverInfo.name 应为 artex: %+v", info)
	}
	if info.Tools != 11 {
		t.Fatalf("默认档位应有 11 个工具，实际 %d", info.Tools)
	}
	if len(info.Protocols) == 0 || info.Protocols[0] != mcpserver.ProtocolVersion {
		t.Fatalf("协议版本应含 %s: %v", mcpserver.ProtocolVersion, info.Protocols)
	}
	if info.URL != ts.URL+"/mcp" {
		t.Fatalf("地址应被规范化为 /mcp: %s", info.URL)
	}
}

// TestMCPProbeRejectsBadToken：错 token 必须判死，不能因为「端口通」就当作可用。
func TestMCPProbeRejectsBadToken(t *testing.T) {
	ts := httptest.NewServer(probeTestHandler(t))
	defer ts.Close()

	info := mcpserver.Probe(context.Background(),
		mcpserver.Endpoint{URL: ts.URL, Token: "wrong-token"}, 3*time.Second)
	if info.Alive {
		t.Fatalf("错 token 不该判活: %+v", info)
	}
	if !strings.Contains(info.Error, "401") {
		t.Fatalf("错误应为 401: %q", info.Error)
	}
}

// TestMCPAttachMatchesLocal：附着后拿到的工具清单必须与本地 stdio 完全一致。
func TestMCPAttachMatchesLocal(t *testing.T) {
	s := newMCPTestServer(t)
	local := mcpToolNames(s.buildMCPServer())

	h := s.buildMCPServer().HTTPHandler(mcpserver.HTTPConfig{
		Authorize: func(r *http.Request) error {
			if strings.TrimSpace(r.Header.Get("Authorization")) != "Bearer probe-token" {
				return errors.New("invalid token")
			}
			return nil
		},
		MaxBody: 8 << 20,
	})
	ts := httptest.NewServer(h)
	defer ts.Close()

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := mcpserver.ProxyStdio(context.Background(), in, &out,
		mcpserver.Endpoint{URL: ts.URL, Token: "probe-token"}); err != nil {
		t.Fatalf("附着转发失败: %v", err)
	}

	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("回帧不是 JSON: %v (%q)", err, out.String())
	}
	got := make([]string, 0, len(resp.Result.Tools))
	for _, tl := range resp.Result.Tools {
		got = append(got, tl.Name)
	}
	if strings.Join(got, ",") != strings.Join(local, ",") {
		t.Fatalf("附着的工具清单与本地不一致\n本地: %v\n附着: %v", local, got)
	}
}

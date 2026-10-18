package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestCompatibilityTransportPreservesBaseAndCachesClones(t *testing.T) {
	base := &http.Transport{}
	if got := compatibilityTransport(base, ""); got != base {
		t.Fatal("legacy transport was replaced")
	}
	for _, v := range []string{"auto", "1.1", "2"} {
		got := compatibilityTransport(base, v)
		if got == base || got.Protocols == nil {
			t.Fatalf("missing clone for %s", v)
		}
		if got.Protocols.HTTP1() != (v != "2") || got.Protocols.HTTP2() != (v != "1.1") {
			t.Fatalf("protocol settings %s", v)
		}
		if compatibilityTransport(base, v) != got {
			t.Fatalf("clone not cached for %s", v)
		}
	}
	if base.Protocols != nil || base.ForceAttemptHTTP2 {
		t.Fatal("shared base mutated")
	}
}

func TestHTTPRequestRejectsUnsupportedProtocolsBeforeNetwork(t *testing.T) {
	tool := NewToolSet(nil, "").httpRequestTool()
	for _, in := range []string{
		`{"url":"https://example.invalid/","http_version":"fake-chrome"}`,
		`{"url":"http://example.invalid/","http_version":"2"}`,
	} {
		r, err := tool.Call(context.Background(), json.RawMessage(in), nil)
		if err != nil || !r.IsError {
			t.Fatalf("protocol input not refused: %s, %+v %v", in, r, err)
		}
	}
}

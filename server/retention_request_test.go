package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetentionMutationRequiresJSONAndSameHostOrigin(t *testing.T) {
	for _, item := range []struct {
		body, kind, origin string
		ok                 bool
	}{
		{`{}`, "application/json", "https://artex.example", true},
		{`{}`, "application/json", "", true},
		{`{}`, "text/plain", "", false},
		{`{}`, "application/json", "https://other.example", false},
		{`null`, "application/json", "", false},
		{`{} {}`, "application/json", "", false},
		{`{"unknown":1}`, "application/json", "", false},
	} {
		req := httptest.NewRequest(http.MethodPost, "https://artex.example/api/retention/preview", strings.NewReader(item.body))
		req.Header.Set("Content-Type", item.kind)
		if item.origin != "" {
			req.Header.Set("Origin", item.origin)
		}
		recorder := httptest.NewRecorder()
		var dst struct{}
		if ok := decodeRetentionJSON(recorder, req, &dst); ok != item.ok {
			t.Fatalf("unexpected result for %q / %q / %q", item.body, item.kind, item.origin)
		}
	}

	// Local dev proxy (loopback peer + loopback Host + loopback Origin) is allowed,
	// while non-loopback peers or external origins are rejected.
	devReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/retention/preview", strings.NewReader(`{}`))
	devReq.RemoteAddr = "127.0.0.1:54321"
	devReq.Header.Set("Content-Type", "application/json")
	devReq.Header.Set("Origin", "http://localhost:5173")
	var devDst struct{}
	if !decodeRetentionJSON(httptest.NewRecorder(), devReq, &devDst) {
		t.Fatal("expected loopback dev proxy request to be permitted")
	}

	spoofReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/retention/preview", strings.NewReader(`{}`))
	spoofReq.RemoteAddr = "203.0.113.9:54321"
	spoofReq.Header.Set("Content-Type", "application/json")
	spoofReq.Header.Set("Origin", "http://localhost:5173")
	if decodeRetentionJSON(httptest.NewRecorder(), spoofReq, &devDst) {
		t.Fatal("non-loopback peer must not bypass exact Host match")
	}
}

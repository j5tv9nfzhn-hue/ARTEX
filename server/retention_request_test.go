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
}

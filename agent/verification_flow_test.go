package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func clearVerificationProxyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(k, "")
	}
}

func callVerificationFlow(t *testing.T, tool actool.CoreTool, a verificationFlowInput) verificationFlowResult {
	t.Helper()
	b, _ := json.Marshal(a)
	result, err := tool.Call(context.Background(), b, nil)
	if err != nil || result.IsError {
		t.Fatalf("flow error %v: %s", err, result.Flatten())
	}
	if len(result.Flatten()) > 4096 {
		t.Fatal("flow leaked large intermediate output")
	}
	var out verificationFlowResult
	if json.Unmarshal([]byte(result.Flatten()), &out) != nil {
		t.Fatal(result.Flatten())
	}
	return out
}

func TestVerificationFlowSameSessionEndToEndAndNoPostReplay(t *testing.T) {
	clearVerificationProxyEnv(t)
	var hits, posts atomic.Int32
	c := verificationFixture("SHA-256", 7)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/target":
			if v, e := r.Cookie("verified"); e == nil && v.Value == "yes" {
				fmt.Fprint(w, "business-ok")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "fixture-session", Path: "/", HttpOnly: true})
			fmt.Fprint(w, `<html><input name="csrf" value="nonce123"><altcha-widget></altcha-widget></html>`)
		case "/challenge":
			if v, e := r.Cookie("session"); e != nil || v.Value != "fixture-session" {
				http.Error(w, "no session", 403)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(c)
		case "/submit":
			posts.Add(1)
			if r.Method != "POST" || r.ParseForm() != nil || r.Form.Get("csrf") != "nonce123" {
				http.Error(w, "bad form", 400)
				return
			}
			if v, e := r.Cookie("session"); e != nil || v.Value != "fixture-session" {
				http.Error(w, "lost session", 403)
				return
			}
			raw, e := base64.StdEncoding.DecodeString(r.Form.Get("altcha"))
			var payload struct {
				Number    int64  `json:"number"`
				Signature string `json:"signature"`
			}
			if e != nil || json.Unmarshal(raw, &payload) != nil || payload.Number != 7 || payload.Signature != c.Signature {
				http.Error(w, "bad proof", 400)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "verified", Value: "yes", Path: "/"})
			fmt.Fprint(w, `{"submitted":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a := verificationFlowInput{Mode: "run", TargetURL: server.URL + "/target", ChallengeURL: server.URL + "/challenge", SubmitURL: server.URL + "/submit", SuccessContains: "business-ok", CSRFField: "csrf", CSRFRegex: `name="csrf" value="([^"]+)"`}
	tool := NewToolSet(nil, "").verificationFlowTool()
	out := callVerificationFlow(t, tool, a)
	if out.State != "business_confirmed" || !out.BusinessConfirmed || !out.SubmissionAttempted || out.Requests != 4 || hits.Load() != 4 || posts.Load() != 1 {
		t.Fatalf("flow %+v hits=%d posts=%d", out, hits.Load(), posts.Load())
	}
	a.TimeoutMS = 12000
	cached := callVerificationFlow(t, tool, a)
	if !cached.Cached || hits.Load() != 4 {
		t.Fatalf("identical flow repeated: %+v hits=%d", cached, hits.Load())
	}
	a.HTTPVersion = "1.1"
	blocked := callVerificationFlow(t, tool, a)
	if blocked.State != "submission_already_attempted" || hits.Load() != 4 || posts.Load() != 1 {
		t.Fatalf("POST replay allowed %+v", blocked)
	}
}

func TestVerificationFlowDoesNotFollowCrossOriginRedirect(t *testing.T) {
	clearVerificationProxyEnv(t)
	var outside atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { outside.Add(1); fmt.Fprint(w, "unexpected") }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			http.Redirect(w, r, other.URL, http.StatusFound)
			return
		}
		http.Error(w, "unsupported", 404)
	}))
	defer server.Close()
	out := callVerificationFlow(t, NewToolSet(nil, "").verificationFlowTool(), verificationFlowInput{Mode: "run", TargetURL: server.URL + "/target", ChallengeURL: server.URL + "/challenge", SubmitURL: server.URL + "/submit", SuccessContains: "business-ok"})
	if outside.Load() != 0 || out.BusinessConfirmed || out.SubmissionAttempted || out.State != "unsupported_challenge" {
		t.Fatalf("scope escape %+v outside=%d", out, outside.Load())
	}
}

func TestVerificationFlowStopsOnResponseCap(t *testing.T) {
	clearVerificationProxyEnv(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, strings.Repeat("x", verificationFlowCapture+1))
	}))
	defer server.Close()
	out := callVerificationFlow(t, NewToolSet(nil, "").verificationFlowTool(), verificationFlowInput{Mode: "run", TargetURL: server.URL, ChallengeURL: server.URL + "/challenge", SubmitURL: server.URL + "/submit", SuccessContains: "x"})
	if out.State != "response_limit_exceeded" || out.BusinessConfirmed || out.SubmissionAttempted || hits.Load() != 1 {
		t.Fatalf("cap ignored %+v", out)
	}
}

func TestVerificationFlowPlanAndValidation(t *testing.T) {
	a := verificationFlowInput{Mode: "plan", TargetURL: "https://example.invalid/target", ChallengeURL: "https://example.invalid/challenge", SubmitURL: "https://example.invalid/submit", SuccessContains: "business-ok"}
	out := callVerificationFlow(t, NewToolSet(nil, "").verificationFlowTool(), a)
	if out.State != "planned" || out.Requests != 0 || out.SubmissionAttempted {
		t.Fatalf("plan %+v", out)
	}
	for _, change := range []func(*verificationFlowInput){
		func(a *verificationFlowInput) { a.SubmitURL = "https://other.invalid/submit" },
		func(a *verificationFlowInput) { a.SuccessContains = "" },
		func(a *verificationFlowInput) { a.Headers = map[string]string{"Host": "other.invalid"} },
		func(a *verificationFlowInput) { a.Form = map[string]string{"data": strings.Repeat("x", 8193)} },
		func(a *verificationFlowInput) { a.CSRFField = "csrf" },
	} {
		bad := a
		change(&bad)
		b, _ := json.Marshal(bad)
		r, e := NewToolSet(nil, "").verificationFlowTool().Call(context.Background(), b, nil)
		if e != nil || !r.IsError {
			t.Fatalf("invalid flow accepted %+v", bad)
		}
	}
}

func TestVerificationFlowEarlySuccessDoesNotSubmit(t *testing.T) {
	clearVerificationProxyEnv(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, "business-ok") }))
	defer server.Close()
	out := callVerificationFlow(t, NewToolSet(nil, "").verificationFlowTool(), verificationFlowInput{Mode: "run", TargetURL: server.URL, ChallengeURL: server.URL + "/challenge", SubmitURL: server.URL + "/submit", SuccessContains: "business-ok"})
	if !out.BusinessConfirmed || out.SubmissionAttempted || hits.Load() != 1 {
		t.Fatalf("unnecessary solve or POST %+v", out)
	}
}

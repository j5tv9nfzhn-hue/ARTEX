package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
	"golang.org/x/crypto/pbkdf2"
)

func verificationProV2Fixture(t *testing.T, algorithm string, n uint32) json.RawMessage {
	t.Helper()
	salt := []byte("salt")
	password := append([]byte("nonce"), make([]byte, 4)...)
	binary.BigEndian.PutUint32(password[len(password)-4:], n)
	newHash, _ := verificationHash(algorithm)
	var key []byte
	if strings.HasPrefix(algorithm, "PBKDF2/") {
		key = pbkdf2.Key(password, salt, 3, 16, newHash)
	} else {
		h := newHash()
		_, _ = h.Write(salt)
		_, _ = h.Write(password)
		key = h.Sum(nil)
		for i := 1; i < 3; i++ {
			h.Reset()
			_, _ = h.Write(key)
			key = h.Sum(nil)
		}
		key = key[:16]
	}
	raw := fmt.Sprintf(`{"parameters":{"algorithm":%q,"nonce":"6e6f6e6365","salt":"73616c74","cost":3,"keyLength":16,"keyPrefix":%q,"data":{"b":2,"a":1}},"signature":"fixture-public-signature"}`, algorithm, hex.EncodeToString(key))
	return json.RawMessage(raw)
}
func TestVerificationProV2VectorsAndPreservesSignedDataOrder(t *testing.T) {
	for _, algorithm := range []string{"SHA-256", "SHA-384", "SHA-512", "PBKDF2/SHA-256", "PBKDF2/SHA-384", "PBKDF2/SHA-512"} {
		t.Run(algorithm, func(t *testing.T) {
			raw := verificationProV2Fixture(t, algorithm, 3)
			raw = json.RawMessage(strings.Replace(string(raw), `"data":{"b":2,"a":1}`, `"data": { "b":2, "a":1 }`, 1))
			r, err := NewToolSet(nil, "").solveVerification(context.Background(), "altcha_v2", raw, 10, time.Second)
			if err != nil || r.State != "proof_computed" || r.Number == nil || *r.Number != 3 || r.BusinessAccepted {
				t.Fatalf("bad v2 vector %+v %v", r, err)
			}
			payload, err := base64.StdEncoding.DecodeString(r.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(payload, raw) {
				t.Fatalf("signed data order changed %s", payload)
			}
			var parsed struct {
				Solution struct {
					Counter    uint32 `json:"counter"`
					DerivedKey string `json:"derivedKey"`
				} `json:"solution"`
			}
			var original verificationV2Challenge
			if json.Unmarshal(payload, &parsed) != nil || json.Unmarshal(raw, &original) != nil || parsed.Solution.Counter != 3 || parsed.Solution.DerivedKey != original.Parameters.KeyPrefix {
				t.Fatal("invalid serialized v2 solution")
			}

		})
	}
}
func TestVerificationProRejectsKDFResourceParameters(t *testing.T) {
	raw := verificationProV2Fixture(t, "PBKDF2/SHA-256", 3)
	for _, replacement := range [][2]string{{`"cost":3`, `"cost":20001`}, {`"keyLength":16`, `"keyLength":1024`}, {`"cost":3`, `"memoryCost":65536,"cost":3`}, {`"PBKDF2/SHA-256"`, `"Argon2id"`}, {`"cost":3`, `"expiresAt":1,"cost":3`}} {
		bad := json.RawMessage(strings.Replace(string(raw), replacement[0], replacement[1], 1))
		if _, err := NewToolSet(nil, "").solveVerification(context.Background(), "altcha_v2", bad, 10, time.Second); err == nil {
			t.Fatalf("over-budget KDF accepted %s", bad)
		}
	}
}
func TestVerificationProLegacyAndNewEntrypointsShareProofBudget(t *testing.T) {
	ts := NewToolSet(nil, "")
	challenge := verificationFixture("SHA-256", 7)
	in, _ := json.Marshal(map[string]any{"mode": "solve_pow", "challenge": challenge, "max_attempts": 1})
	out, err := ts.humanVerificationTool().Call(context.Background(), in, nil)
	if err != nil || out.IsError {
		t.Fatal(err, out.Flatten())
	}
	raw, _ := json.Marshal(challenge)
	r, err := ts.solveVerification(context.Background(), "altcha_v1", raw, 100, time.Second)
	if err != nil || !r.Cached || r.Attempts != 1 || r.State != "budget_exhausted" {
		t.Fatalf("budget reset across entrypoints %+v %v", r, err)
	}
}
func TestVerificationProConstructedFlowsShareState(t *testing.T) {
	clearVerificationProxyEnv(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, "business-ok") }))
	defer server.Close()
	ts := NewToolSet(nil, "")
	a := verificationFlowInput{Mode: "run", Protocol: "protocol_only", TargetURL: server.URL, SuccessContains: "business-ok"}
	first := callVerificationFlow(t, ts.verificationFlowTool(), a)
	second := callVerificationFlow(t, ts.verificationFlowTool(), a)
	if !first.BusinessConfirmed || !second.Cached || hits.Load() != 1 {
		t.Fatalf("flow state reset: %+v %+v", first, second)
	}
}
func TestVerificationProAnubisInlineEndToEnd(t *testing.T) {
	clearVerificationProxyEnv(t)
	var submissions atomic.Int32
	raw := `{"rules":{"algorithm":"fast","difficulty":1},"challenge":{"id":"fixture-id","method":"fast","randomData":"fixture-random-data","difficulty":1}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/target":
			if c, e := r.Cookie("passed"); e == nil && c.Value == "yes" {
				fmt.Fprint(w, "business-ok")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "fixture", Path: "/"})
			fmt.Fprintf(w, `<html><script type="application/json" id="anubis_challenge">%s</script></html>`, raw)
		case "/pass":
			submissions.Add(1)
			q := r.URL.Query()
			sum := sha256.Sum256([]byte("fixture-random-data" + q.Get("nonce")))
			digest := hex.EncodeToString(sum[:])
			c, e := r.Cookie("session")
			if e != nil || c.Value != "fixture" || q.Get("id") != "fixture-id" || digest != q.Get("response") || !strings.HasPrefix(digest, "0") {
				http.Error(w, "invalid", 403)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "passed", Value: "yes", Path: "/"})
			http.Redirect(w, r, "/target", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a := verificationFlowInput{Mode: "run", Protocol: "anubis_sha256", TargetURL: server.URL + "/target", SubmitURL: server.URL + "/pass", SuccessContains: "business-ok"}
	out := callVerificationFlow(t, NewToolSet(nil, "").verificationFlowTool(), a)
	if !out.BusinessConfirmed || !out.SubmissionAttempted || submissions.Load() != 1 || out.Requests > 6 {
		t.Fatalf("Anubis flow %+v submissions=%d", out, submissions.Load())
	}
}
func TestVerificationProAnubisRejectsUnknownMethodsAndMissingSubmit(t *testing.T) {
	raw := json.RawMessage(`{"rules":{"algorithm":"sha256","difficulty":1},"challenge":{"id":"fixture","method":"sha256","randomData":"x","difficulty":1}}`)
	if _, err := NewToolSet(nil, "").solveVerification(context.Background(), "anubis_sha256", raw, 100, time.Second); err == nil {
		t.Fatal("WASM method accepted as legacy")
	}
	_, err := validateVerificationFlow(verificationFlowInput{Protocol: "anubis_sha256", TargetURL: "https://example.invalid/", SuccessContains: "ok"})
	if err == nil {
		t.Fatal("missing submit URL accepted")
	}
}
func TestVerificationProCapabilitiesAndHelperConfiguration(t *testing.T) {
	t.Setenv("ARTEX_VERIFICATION_HTTP_HELPER", "relative-helper")
	if _, err := verificationHelperPath(); err == nil {
		t.Fatal("relative helper accepted")
	}
	r, err := NewToolSet(nil, "").verificationCapabilitiesTool().Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || r.IsError || !strings.Contains(r.Flatten(), "configuration_invalid_or_missing") {
		t.Fatal(err, r.Flatten())
	}
}
func TestVerificationProAnubisSubmissionOriginValidation(t *testing.T) {
	a := verificationFlowInput{Protocol: "anubis_sha256", TargetURL: "https://example.invalid/", SubmitURL: "https://outside.invalid/pass", SuccessContains: "ok"}
	if _, err := validateVerificationFlow(a); err == nil {
		t.Fatal("cross-origin Anubis submission accepted")
	}
	// Ensure query submission safely encodes the original destination, not string concatenation.
	u, _ := url.Parse("https://example.invalid/pass")
	q := u.Query()
	q.Set("redir", "https://example.invalid/a?x=1&y=2")
	u.RawQuery = q.Encode()
	if u.Query().Get("redir") != "https://example.invalid/a?x=1&y=2" {
		t.Fatal("redir encoding")
	}
}
func TestVerificationProCatalogBindings(t *testing.T) {
	for _, name := range []string{"human_verification_solve", "human_verification_capabilities"} {
		found := false
		for _, seed := range BuiltinToolSeeds() {
			if seed.Key == name {
				found = true
				joined := "," + strings.Join(seed.Agents, ",") + ","
				if !strings.Contains(joined, ",worker,") || !strings.Contains(joined, ",pentest,") {
					t.Fatalf("%s bindings %v", name, seed.Agents)
				}
			}
		}
		if !found {
			t.Fatalf("missing %s", name)
		}
	}
	// The new compute/introspection tools are offline; active flow remains subject
	// to the software_re filter after DB injection.
	tools, _ := softwareRETools([]actool.CoreTool{NewToolSet(nil, "").verificationFlowTool()}, DeferredInfo{})
	if len(tools) != 0 {
		t.Fatal("active verification flow leaked into software_re")
	}
}

func TestVerificationProAnubisLegacyAPIAndObjectShareIdentity(t *testing.T) {
	ts := NewToolSet(nil, "")
	object := json.RawMessage(`{"rules":{"algorithm":"fast","difficulty":1},"challenge":{"id":"fixture-id","randomData":"fixture-random-data","method":"fast"}}`)
	api := json.RawMessage(`{"rules":{"algorithm":"fast","difficulty":1},"challenge":"fixture-random-data","id":"fixture-id"}`)
	first, err := ts.solveVerification(context.Background(), "anubis_sha256", object, 100000, time.Second)
	if err != nil || first.State != "proof_computed" {
		t.Fatal(err, first)
	}
	second, err := ts.solveVerification(context.Background(), "anubis_sha256", api, 100000, time.Second)
	if err != nil || !second.Cached || second.ResponseHash != first.ResponseHash {
		t.Fatal(err, second)
	}
}

func TestVerificationProV2FlowEndToEnd(t *testing.T) {
	clearVerificationProxyEnv(t)
	challenge := verificationProV2Fixture(t, "PBKDF2/SHA-256", 3)
	var expected verificationV2Challenge
	if json.Unmarshal(challenge, &expected) != nil {
		t.Fatal("invalid fixture")
	}
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/target":
			if cookie, err := r.Cookie("v2-pass"); err == nil && cookie.Value == "yes" {
				fmt.Fprint(w, "business-ok")
				return
			}
			fmt.Fprint(w, "<html><altcha-widget></altcha-widget></html>")
		case "/challenge":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(challenge)
		case "/submit":
			posts.Add(1)
			if r.Method != "POST" || r.ParseForm() != nil {
				http.Error(w, "bad form", 400)
				return
			}
			raw, err := base64.StdEncoding.DecodeString(r.Form.Get("altcha"))
			var decoded struct {
				Challenge json.RawMessage `json:"challenge"`
				Solution  struct {
					Counter    int64  `json:"counter"`
					DerivedKey string `json:"derivedKey"`
				} `json:"solution"`
			}
			if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded.Solution.Counter != 3 || decoded.Solution.DerivedKey != expected.Parameters.KeyPrefix || !bytes.Equal(decoded.Challenge, challenge) {
				http.Error(w, "bad proof", 403)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "v2-pass", Value: "yes", Path: "/"})
			fmt.Fprint(w, "submitted")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := callVerificationFlow(t, NewToolSet(nil, "").verificationFlowTool(), verificationFlowInput{Mode: "run", TargetURL: server.URL + "/target", ChallengeURL: server.URL + "/challenge", SubmitURL: server.URL + "/submit", SuccessContains: "business-ok"})
	if !out.BusinessConfirmed || posts.Load() != 1 || out.Protocol != "altcha" || out.TransportProvider != "standard_http" {
		t.Fatalf("v2 flow %+v", out)
	}
}

func TestVerificationProV2EnvelopeNoiseCannotResetBudget(t *testing.T) {
	ts := NewToolSet(nil, "")
	raw := verificationProV2Fixture(t, "SHA-256", 3)
	first, err := ts.solveVerification(context.Background(), "altcha_v2", raw, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	altered := json.RawMessage(`{"unrecognized_envelope":true,` + string(raw[1:]))
	second, err := ts.solveVerification(context.Background(), "altcha_v2", altered, 100, time.Second)
	if err != nil || !second.Cached || second.State != first.State || second.Attempts != 1 {
		t.Fatal(err, second)
	}
}

func TestVerificationProAnubisMarkerDoesNotProveBlocking(t *testing.T) {
	plain := inspectVerification(200, nil, "<html><p>Anubis documentation: anubis_challenge business-ok</p></html>")
	if plain.Category == "proof_of_work" || plain.Category == "challenge_page" {
		t.Fatal("marker alone classified as blocking", plain)
	}
	page := `<html><script id="anubis_challenge" type="application/json">{"rules":{"algorithm":"fast","difficulty":1},"challenge":{"id":"fixture","randomData":"fixture"}}</script></html>`
	known := inspectVerification(200, nil, page)
	limited := inspectVerification(429, nil, page)
	if known.Category != "proof_of_work" || limited.Category != "rate_limit" {
		t.Fatal(known, limited)
	}
}

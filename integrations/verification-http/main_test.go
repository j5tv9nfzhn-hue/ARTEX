package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func helperFixture(raw string) request {
	origin, _ := canonicalOrigin(raw)
	return request{Version: ipcVersion, RequestID: 1, Kind: "request", Origin: origin, URL: raw, Method: "GET", Headers: http.Header{"User-Agent": []string{"generic-client"}}, Profile: "chrome", HTTPVersion: "auto", TimeoutMS: 1000}
}
func TestHelperProfileAndBoundedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.UserAgent(), "Chrome") {
			http.Error(w, "inconsistent profile", 400)
			return
		}
		fmt.Fprint(w, strings.Repeat("x", 40000))
	}))
	defer server.Close()
	a := helperFixture(server.URL)
	transport, e := newTransport(a)
	if e != nil {
		t.Fatal(e)
	}
	defer transport.CloseIdleConnections()
	out := exchange(a, transport)
	if out.Error != "" || out.Provider != provider || out.Status != 200 || len(out.Body) != 32769 || !strings.Contains(out.RequestHeaders.Get("User-Agent"), "Chrome") {
		t.Fatalf("bad helper result %+v", out)
	}
}
func TestHelperRejectsUnsafeFrames(t *testing.T) {
	for _, change := range []func(*request){
		func(a *request) { a.Version = 3 },
		func(a *request) { a.URL = "file:///etc/passwd" },
		func(a *request) { a.URL = "https://user:secret@example.invalid/" },
		func(a *request) { a.Body = make([]byte, 24577) },
		func(a *request) { a.Headers = http.Header{"Host": []string{"other.invalid"}} },
		func(a *request) { a.Profile = "arbitrary" },
		func(a *request) { a.TimeoutMS = 6000 },
	} {
		a := helperFixture("https://example.invalid/")
		change(&a)
		if validate(a) == nil {
			t.Fatal("unsafe frame accepted")
		}
	}
}
func TestHelperInvalidFrameProducesOnlyStructuredFailure(t *testing.T) {
	var output bytes.Buffer
	serve(strings.NewReader("not-json\n"), &output)
	var result response
	if json.Unmarshal(bytes.TrimSpace(output.Bytes()), &result) != nil || result.Error != "invalid_frame" {
		t.Fatalf("bad failure %s", output.String())
	}
}
func TestHelperSessionDoesNotChangeConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer server.Close()
	a := helperFixture(server.URL)
	b := a
	b.Profile = "firefox"
	b.RequestID = 2
	first, _ := json.Marshal(a)
	second, _ := json.Marshal(b)
	hello, _ := json.Marshal(request{Version: ipcVersion, Kind: "hello", Origin: a.Origin, Profile: a.Profile, HTTPVersion: a.HTTPVersion})
	input := append(append(hello, '\n'), append(append(first, '\n'), append(second, '\n')...)...)
	var output bytes.Buffer
	serve(bytes.NewReader(input), &output)
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("frame count %d", len(lines))
	}
	var result response
	if json.Unmarshal(lines[2], &result) != nil || result.Error != "configuration_change_rejected" {
		t.Fatalf("configuration changed: %s", lines[2])
	}
}

func TestHelperRejectsCompressedBodyWithoutDecoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("unsafe compression negotiation")
		}
		w.Header().Set("Content-Encoding", "zstd")
		fmt.Fprint(w, "not-decoded")
	}))
	defer server.Close()
	a := helperFixture(server.URL)
	transport, err := newTransport(a)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	result := exchange(a, transport)
	if result.Error != "compressed_response_unsupported" {
		t.Fatal("compressed body decoded or accepted", result)
	}
}

func helperFrames(t *testing.T, frames ...request) []response {
	t.Helper()
	var input, output bytes.Buffer
	encoder := json.NewEncoder(&input)
	for _, frame := range frames {
		if err := encoder.Encode(frame); err != nil {
			t.Fatal(err)
		}
	}
	serve(&input, &output)
	decoder := json.NewDecoder(&output)
	var answers []response
	for decoder.More() {
		var r response
		if err := decoder.Decode(&r); err != nil {
			t.Fatal(err)
		}
		answers = append(answers, r)
	}
	return answers
}
func helperHello(a request) request {
	return request{Version: ipcVersion, Kind: "hello", Origin: a.Origin, Profile: a.Profile, HTTPVersion: a.HTTPVersion, Proxy: a.Proxy, CAPEM: a.CAPEM}
}
func TestHelperHandshakeDoesNotAccessTarget(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, "ok") }))
	defer server.Close()
	a := helperFixture(server.URL)
	answers := helperFrames(t, helperHello(a))
	if len(answers) != 1 || answers[0].Kind != "ready" || hits.Load() != 0 {
		t.Fatal("hello performed target IO", answers, hits.Load())
	}
	answers = helperFrames(t, a)
	if len(answers) != 1 || answers[0].Error != "handshake_required" || hits.Load() != 0 {
		t.Fatal("HTTP request accepted before hello")
	}
}
func TestHelperIndependentlyRejectsCrossOrigin(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, "unexpected") }))
	defer server.Close()
	a := helperFixture("https://example.invalid/")
	hello := helperHello(a)
	a.URL = server.URL
	answers := helperFrames(t, hello, a)
	if len(answers) != 2 || answers[1].Error != "origin_rejected" || hits.Load() != 0 {
		t.Fatal("cross-origin helper request escaped", answers, hits.Load())
	}
}
func TestHelperTLSVerificationAndExplicitCA(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "tls-ok") }))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	a := helperFixture(server.URL)
	transport, err := newTransport(a)
	if err != nil {
		t.Fatal(err)
	}
	rejected := exchange(a, transport)
	transport.CloseIdleConnections()
	if rejected.Error != "transport_failed" {
		t.Fatal("untrusted self-signed certificate accepted")
	}
	a.CAPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	for _, version := range []string{"auto", "2"} {
		a.HTTPVersion = version
		transport, err = newTransport(a)
		if err != nil {
			t.Fatal(err)
		}
		out := exchange(a, transport)
		transport.CloseIdleConnections()
		if out.Error != "" || out.Status != 200 || version == "2" && out.Proto != "HTTP/2.0" {
			t.Fatal("explicit CA / H2 failed", out.Error, out.Proto)
		}
	}
}
func TestHelperExplicitProxyAndNoEnvironmentFallback(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyHits.Add(1); fmt.Fprint(w, "via-proxy") }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "direct") }))
	defer target.Close()
	a := helperFixture(target.URL)
	transport, err := newTransport(a)
	if err != nil {
		t.Fatal(err)
	}
	out := exchange(a, transport)
	transport.CloseIdleConnections()
	if out.Error != "" || string(out.Body) != "direct" || proxyHits.Load() != 0 {
		t.Fatal("inherited ambient proxy")
	}
	a = helperFixture("http://example.invalid/path")
	a.Proxy = proxy.URL
	transport, err = newTransport(a)
	if err != nil {
		t.Fatal(err)
	}
	out = exchange(a, transport)
	transport.CloseIdleConnections()
	if out.Error != "" || string(out.Body) != "via-proxy" || proxyHits.Load() != 1 {
		t.Fatal("explicit proxy ignored")
	}
}

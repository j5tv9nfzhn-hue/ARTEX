package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func verificationIPCFixtureResponse() verificationHelperResponse {
	return verificationHelperResponse{Version: verificationHelperIPCVersion, Kind: "response", Provider: verificationHelperProvider, Status: 200, Proto: "HTTP/1.1", Headers: http.Header{}, RequestHeaders: http.Header{"User-Agent": []string{"fixture/1"}, "Accept-Encoding": []string{"identity"}}, Body: []byte("ok")}
}
func TestVerificationIPCHeadersAndSessionEvidence(t *testing.T) {
	request, _ := http.NewRequest("GET", "https://example.invalid/", nil)
	request.Header.Set("Authorization", "fixture-secret")
	valid := verificationIPCFixtureResponse()
	valid.RequestHeaders.Set("Authorization", "fixture-secret")
	if err := validateVerificationIPCResponse(valid, request, "auto"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*verificationHelperResponse){
		func(r *verificationHelperResponse) { r.Version = 1 },
		func(r *verificationHelperResponse) { r.Proto = "HTTP/3.0" },
		func(r *verificationHelperResponse) { r.Status = 101 },
		func(r *verificationHelperResponse) { r.Headers.Set("X-Test", "bad\r\nInjected: yes") },
		func(r *verificationHelperResponse) { r.Headers["Bad:Name"] = []string{"x"} },
		func(r *verificationHelperResponse) { r.RequestHeaders.Set("Authorization", "changed") },
		func(r *verificationHelperResponse) { r.RequestHeaders.Del("User-Agent") },
		func(r *verificationHelperResponse) { r.Body = make([]byte, verificationFlowCapture+2) },
	} {
		r := valid
		r.Headers = valid.Headers.Clone()
		r.RequestHeaders = valid.RequestHeaders.Clone()
		change(&r)
		if validateVerificationIPCResponse(r, request, "auto") == nil {
			t.Fatal("invalid helper response accepted")
		}
	}
}
func TestVerificationIPCFixedErrorsDoNotEchoHelperSecrets(t *testing.T) {
	request, _ := http.NewRequest("GET", "https://example.invalid/", nil)
	r := verificationIPCFixtureResponse()
	r.Error = "secret-token-should-not-appear"
	err := validateVerificationIPCResponse(r, request, "auto")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatal("untrusted error escaped allowlist")
	}
	r.Error = ""
	if validateVerificationIPCResponse(r, request, "2") == nil {
		t.Fatal("HTTP/1 accepted for forced HTTP/2")
	}
	request.Method = "HEAD"
	if validateVerificationIPCResponse(r, request, "auto") == nil {
		t.Fatal("HEAD body accepted")
	}
}

// This fixture only runs in an explicitly selected test subprocess. It never
// opens network sockets or starts ARTEX. os.Exit keeps test-runner text off IPC.
func TestVerificationIPCFixtureProcess(t *testing.T) {
	mode := os.Getenv("ARTEX_VERIFICATION_IPC_TEST_CASE")
	if mode == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	if !scanner.Scan() {
		os.Exit(2)
	}
	var input verificationHelperRequest
	if json.Unmarshal(scanner.Bytes(), &input) != nil || input.Kind != "hello" || input.URL != "" {
		os.Exit(3)
	}
	version := verificationHelperIPCVersion
	if mode == "wrong-version" {
		version = 1
	}
	if encoder.Encode(verificationHelperResponse{Version: version, Kind: "ready", Provider: verificationHelperProvider}) != nil {
		os.Exit(4)
	}
	if !scanner.Scan() {
		os.Exit(0)
	}
	switch mode {
	case "stall":
		time.Sleep(10 * time.Second)
	case "corrupt":
		_, _ = fmt.Fprint(os.Stdout, "not-json\n")
	case "oversize":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", verificationHelperFrame+1)+"\n")
	default:
		var r verificationHelperRequest
		_ = json.Unmarshal(scanner.Bytes(), &r)
		answer := verificationIPCFixtureResponse()
		answer.RequestID = r.RequestID
		if mode == "wrong-id" {
			answer.RequestID++
		}
		for _, k := range []string{"Cookie", "Authorization"} {
			if v := r.Headers.Get(k); v != "" {
				answer.RequestHeaders.Set(k, v)
			}
		}
		_ = encoder.Encode(answer)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}
func verificationIPCTestTransport(t *testing.T, mode string) (*verificationHelperTransport, *atomic.Int32) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestVerificationIPCFixtureProcess$")
	cmd.Env = append(os.Environ(), "ARTEX_VERIFICATION_IPC_TEST_CASE="+mode)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 500 * time.Millisecond
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		cancel()
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		cancel()
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), verificationHelperFrame)
	released := new(atomic.Int32)
	transport := &verificationHelperTransport{gate: make(chan struct{}, 1), cmd: cmd, in: in, stdout: out, out: scanner, cancel: cancel, release: func() { released.Add(1) }, profile: "chrome", version: "auto", origin: "https://example.invalid"}
	t.Cleanup(transport.Close)
	return transport, released
}
func TestVerificationIPCHandshakeBeforeRequestsAndCloseOnce(t *testing.T) {
	transport, released := verificationIPCTestTransport(t, "ready")
	if err := transport.handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("GET", "https://example.invalid/path", nil)
	request.Header.Set("Cookie", "session=fixture")
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	transport.Close()
	transport.Close()
	if released.Load() != 1 {
		t.Fatal("process slot released more than once")
	}
	if _, err = transport.RoundTrip(request); err == nil {
		t.Fatal("closed helper reused")
	}
}
func TestVerificationIPCRejectsOldHelperBeforeSendingURL(t *testing.T) {
	transport, _ := verificationIPCTestTransport(t, "wrong-version")
	if err := transport.handshake(context.Background()); err == nil || verificationIPCCode(err) != "handshake_failed" {
		t.Fatal("old helper accepted", err)
	}
	if transport.handshaken {
		t.Fatal("invalid helper marked ready")
	}
}
func TestVerificationIPCRequestCancellationPoisonsSession(t *testing.T) {
	transport, _ := verificationIPCTestTransport(t, "stall")
	if err := transport.handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", "https://example.invalid/path", nil)
	start := time.Now()
	_, err := transport.RoundTrip(request)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatal("request deadline did not interrupt IPC", err)
	}
	next, _ := http.NewRequest("GET", "https://example.invalid/", nil)
	if _, err = transport.RoundTrip(next); err == nil {
		t.Fatal("poisoned helper reused")
	}
}
func TestVerificationIPCBadFramesAreTerminal(t *testing.T) {
	for _, mode := range []string{"corrupt", "oversize", "wrong-id"} {
		t.Run(mode, func(t *testing.T) {
			transport, _ := verificationIPCTestTransport(t, mode)
			if err := transport.handshake(context.Background()); err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest("GET", "https://example.invalid/", nil)
			if _, err := transport.RoundTrip(request); err == nil {
				t.Fatal("bad IPC accepted")
			}
			if !transport.failed {
				t.Fatal("bad IPC session not poisoned")
			}
		})
	}
}

func TestVerificationIPCFlowTransportClonesAreNotGlobal(t *testing.T) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	defer base.CloseIdleConnections()
	first, second := verificationBoundedTransport(base), verificationBoundedTransport(base)
	defer first.CloseIdleConnections()
	defer second.CloseIdleConnections()
	if first == second || first == base || first.MaxResponseHeaderBytes != 32768 {
		t.Fatal("flow transport not independently owned")
	}
}

func TestVerificationIPCQueuedCancellationDoesNotKillActiveSession(t *testing.T) {
	transport, _ := verificationIPCTestTransport(t, "ready")
	if err := transport.handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	transport.gate <- struct{}{} // Simulate another request owning the serialized IPC slot.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", "https://example.invalid/", nil)
	_, err := transport.RoundTrip(request)
	<-transport.gate
	if !errors.Is(err, context.DeadlineExceeded) || transport.failed {
		t.Fatal("queued cancellation altered active session", err)
	}
}

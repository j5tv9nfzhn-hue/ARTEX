package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

const verificationHelperFrame = 131072

var verificationHelperSlots = make(chan struct{}, 2)

type verificationHelperRequest struct {
	Version     int         `json:"version"`
	RequestID   int         `json:"request_id"`
	Kind        string      `json:"kind"`
	Origin      string      `json:"origin"`
	URL         string      `json:"url"`
	Method      string      `json:"method"`
	Headers     http.Header `json:"headers"`
	Body        []byte      `json:"body"`
	Proxy       string      `json:"proxy"`
	CAPEM       string      `json:"ca_pem,omitempty"`
	Profile     string      `json:"profile"`
	HTTPVersion string      `json:"http_version"`
	TimeoutMS   int         `json:"timeout_ms"`
}
type verificationHelperResponse struct {
	Version        int         `json:"version"`
	RequestID      int         `json:"request_id"`
	Kind           string      `json:"kind"`
	Provider       string      `json:"provider"`
	Error          string      `json:"error,omitempty"`
	Status         int         `json:"status"`
	Proto          string      `json:"proto"`
	Headers        http.Header `json:"headers"`
	RequestHeaders http.Header `json:"request_headers"`
	Body           []byte      `json:"body"`
}

func verificationHelperBinaryName() string {
	if runtime.GOOS == "windows" {
		return "artex-verification-http.exe"
	}
	return "artex-verification-http"
}

func verificationHelperRegularExecutable(st os.FileInfo) bool {
	if st == nil || !st.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		return false
	}
	return true
}

func verificationHelperPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("ARTEX_VERIFICATION_HTTP_HELPER")); p != "" {
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("helper configuration must be an absolute operator-controlled path")
		}
		st, err := os.Stat(p)
		if err != nil || !verificationHelperRegularExecutable(st) {
			return "", fmt.Errorf("configured helper not found")
		}
		return p, nil
	}
	name := verificationHelperBinaryName()
	if own, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(own), name)
		if st, e := os.Stat(p); e == nil && verificationHelperRegularExecutable(st) {
			return p, nil
		}
	}
	p, err := exec.LookPath(name)
	if err != nil || !filepath.IsAbs(p) {
		return "", fmt.Errorf("optional fingerprint helper not installed; no automatic download or build")
	}
	return p, nil
}

type verificationHelperTransport struct {
	cmd                         *exec.Cmd
	in                          io.WriteCloser
	out                         *bufio.Scanner
	stdout                      io.ReadCloser
	cancel                      context.CancelFunc
	release                     func()
	closeOnce                   sync.Once
	mu                          sync.Mutex
	gate                        chan struct{}
	closed                      bool
	verified                    bool
	handshaken                  bool
	requestID                   int
	failed                      bool
	failureCode                 string
	origin                      string
	profile, version, proxy, ca string
}

func startVerificationHelper(ctx context.Context, profile, version, proxy, caPath, origin string) (*verificationHelperTransport, error) {
	if ctx.Err() != nil {
		return nil, verificationIPCCancel(ctx.Err())
	}
	path, err := verificationHelperPath()
	if err != nil {
		return nil, err
	}
	if profile != "chrome" && profile != "firefox" {
		return nil, fmt.Errorf("unsupported helper profile")
	}
	if version == "" {
		version = "auto"
	}
	if version != "auto" && version != "1.1" && version != "2" {
		return nil, &verificationIPCError{code: "protocol_rejected"}
	}
	if o, e := verificationOrigin(origin); e != nil || o != origin {
		return nil, &verificationIPCError{code: "origin_rejected"}
	}
	ca := ""
	if caPath != "" {
		f, e := os.Open(caPath)
		if e != nil {
			return nil, fmt.Errorf("configured proxy CA unavailable")
		}
		b, e := io.ReadAll(io.LimitReader(f, 16385))
		_ = f.Close()
		if e != nil || len(b) > 16384 {
			return nil, fmt.Errorf("proxy CA exceeds bounds")
		}
		ca = string(b)
	}
	select {
	case verificationHelperSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, verificationIPCCancel(ctx.Err())
	}
	run, cancel := context.WithCancel(ctx)
	release := func() { <-verificationHelperSlots }
	cmd := exec.CommandContext(run, path) // executable is operator configuration, never supplied by page/tool input
	cmd.WaitDelay = 500 * time.Millisecond
	env := []string{}
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(key) {
		case "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "PROGRAMDATA", "APPDATA", "LOCALAPPDATA",
			"PATH", "TEMP", "TMP", "TMPDIR", "HOME", "USERPROFILE", "XDG_RUNTIME_DIR",
			"SSL_CERT_FILE", "SSL_CERT_DIR":
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "GOMEMLIMIT=64MiB", "GOMAXPROCS=1") // soft Go heap target, not an OS RSS guarantee
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		release()
		return nil, fmt.Errorf("helper stdin unavailable")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		cancel()
		release()
		return nil, fmt.Errorf("helper stdout unavailable")
	}
	cmd.Stderr = io.Discard // never turn network errors/credentials into model-facing stderr
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		cancel()
		release()
		return nil, fmt.Errorf("helper failed to start")
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), verificationHelperFrame)
	transport := &verificationHelperTransport{gate: make(chan struct{}, 1), cmd: cmd, in: in, out: scanner, stdout: stdout, cancel: cancel, release: release, profile: profile, version: version, proxy: proxy, ca: ca, origin: origin}
	if err := transport.handshake(ctx); err != nil {
		transport.Close()
		return nil, err
	}
	return transport, nil
}

// Closing both pipe endpoints also unblocks a stalled IPC read/write without
// waiting for the whole flow timeout or trusting the helper's claimed timeout.
func (t *verificationHelperTransport) closePipes() {
	if t.cancel != nil {
		t.cancel()
	}
	if t.in != nil {
		_ = t.in.Close()
	}
	if t.stdout != nil {
		_ = t.stdout.Close()
	}
}
func (t *verificationHelperTransport) Close() {
	t.closeOnce.Do(func() {
		t.closePipes()
		t.mu.Lock()
		defer t.mu.Unlock()
		t.closed = true
		if t.cmd != nil {
			_ = t.cmd.Wait()
		}
		if t.release != nil {
			t.release()
		}
	})
}
func (t *verificationHelperTransport) abortLocked(code string) error {
	t.failed = true
	t.failureCode = code
	t.closePipes()
	return &verificationIPCError{code: code}
}
func (t *verificationHelperTransport) abortContextLocked(err error) error {
	failure := verificationIPCCancel(err)
	_ = t.abortLocked(verificationIPCCode(failure))
	return failure
}
func (t *verificationHelperTransport) status() (bool, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.verified, t.failureCode
}

// Caller holds mu. At most one bounded frame operation can exist per session.
// After interruption this session is poisoned, so a late frame can never be
// mistaken for the response to another request. No automatic replay is allowed.
func (t *verificationHelperTransport) exchangeFrame(ctx context.Context, input verificationHelperRequest) (verificationHelperResponse, error) {
	var result verificationHelperResponse
	if ctx.Err() != nil {
		return result, t.abortContextLocked(ctx.Err())
	}
	frame, err := json.Marshal(input)
	if err != nil || len(frame)+1 > verificationHelperFrame {
		return result, t.abortLocked("request_frame_limit")
	}
	done := make(chan error, 1)
	go func() {
		n, e := t.in.Write(append(frame, '\n'))
		if e != nil || n != len(frame)+1 {
			done <- io.ErrShortWrite
			return
		}
		if !t.out.Scan() {
			done <- io.ErrUnexpectedEOF
			return
		}
		if json.Unmarshal(t.out.Bytes(), &result) != nil {
			done <- io.ErrUnexpectedEOF
			return
		}
		done <- nil
	}()
	select {
	case <-ctx.Done():
		return verificationHelperResponse{}, t.abortContextLocked(ctx.Err())
	case err := <-done:
		if ctx.Err() != nil {
			return verificationHelperResponse{}, t.abortContextLocked(ctx.Err())
		}
		if err != nil {
			return verificationHelperResponse{}, t.abortLocked("ipc_failed")
		}
		return result, nil
	}
}
func (t *verificationHelperTransport) handshake(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.failed || t.handshaken {
		return &verificationIPCError{code: "session_unavailable"}
	}
	ready, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	input := verificationHelperRequest{Version: verificationHelperIPCVersion, Kind: "hello", Origin: t.origin, Profile: t.profile, HTTPVersion: t.version, Proxy: t.proxy, CAPEM: t.ca}
	r, err := t.exchangeFrame(ready, input)
	if err != nil {
		return err
	}
	if r.Version != verificationHelperIPCVersion || r.Kind != "ready" || r.Provider != verificationHelperProvider || r.Error != "" || r.RequestID != 0 || r.Status != 0 || r.Proto != "" || len(r.Body) != 0 || len(r.Headers) != 0 || len(r.RequestHeaders) != 0 {
		return t.abortLocked("handshake_failed")
	}
	t.handshaken = true
	return nil
}
func (t *verificationHelperTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, &verificationIPCError{code: "request_rejected"}
	}
	if request.Body != nil {
		defer request.Body.Close()
	}
	if request.URL == nil {
		return nil, &verificationIPCError{code: "request_rejected"}
	}
	if t.gate != nil {
		select {
		case t.gate <- struct{}{}:
		case <-request.Context().Done():
			return nil, verificationIPCCancel(request.Context().Err())
		}
		defer func() { <-t.gate }()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.failed || !t.handshaken {
		return nil, &verificationIPCError{code: "session_unavailable"}
	}
	origin, err := verificationOrigin(request.URL.String())
	if err != nil || origin != t.origin {
		return nil, &verificationIPCError{code: "origin_rejected"}
	}
	if !verificationIPCHeaders(request.Header, 8192) {
		return nil, &verificationIPCError{code: "header_rejected"}
	}
	var body []byte
	if request.Context().Err() != nil {
		return nil, verificationIPCCancel(request.Context().Err())
	}
	if request.Body != nil {
		body, err = io.ReadAll(io.LimitReader(request.Body, 24577))
		if err != nil || len(body) > 24576 {
			return nil, &verificationIPCError{code: "request_limits"}
		}
	}
	run, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	deadline, _ := run.Deadline()
	timeout := time.Until(deadline).Milliseconds()
	if timeout < 1 {
		return nil, verificationIPCCancel(context.DeadlineExceeded)
	}
	if t.requestID >= 6 {
		return nil, &verificationIPCError{code: "request_budget_exhausted"}
	}
	t.requestID++
	input := verificationHelperRequest{RequestID: t.requestID, Version: verificationHelperIPCVersion, Kind: "request", Origin: t.origin, URL: request.URL.String(), Method: request.Method, Headers: request.Header.Clone(), Body: body, Proxy: t.proxy, CAPEM: t.ca, Profile: t.profile, HTTPVersion: t.version, TimeoutMS: int(timeout)}
	result, err := t.exchangeFrame(run, input)
	if err != nil {
		return nil, err
	}
	if result.RequestID != t.requestID {
		return nil, t.abortLocked("response_sequence_mismatch")
	}
	if err = validateVerificationIPCResponse(result, request, t.version); err != nil {
		return nil, t.abortLocked(verificationIPCCode(err))
	}
	major, minor, _ := http.ParseHTTPVersion(result.Proto)
	request.Header = result.RequestHeaders.Clone()
	t.verified = true
	return &http.Response{StatusCode: result.Status, Status: strconv.Itoa(result.Status) + " " + http.StatusText(result.Status), Proto: result.Proto, ProtoMajor: major, ProtoMinor: minor, Header: result.Headers.Clone(), Body: io.NopCloser(bytes.NewReader(result.Body)), ContentLength: int64(len(result.Body)), Request: request}, nil
}

// VerificationCapabilitiesSnapshot returns a read-only, side-effect-free snapshot
// of the built-in human verification PoW protocols and optional external
// fingerprint helper detection status.
func VerificationCapabilitiesSnapshot() map[string]any {
	_, err := verificationHelperPath()
	state := "not_installed"
	if err != nil && strings.TrimSpace(os.Getenv("ARTEX_VERIFICATION_HTTP_HELPER")) != "" {
		state = "configuration_invalid_or_missing"
	}
	if err == nil {
		state = "detected_not_handshaken"
	}
	return map[string]any{
		"native_protocols": []string{"altcha_v1_sha256_sha512", "altcha_v2_sha2_pbkdf2", "anubis_legacy_fast_slow_sha256"},
		"fingerprint_helper": map[string]any{
			"state":                    state,
			"provider":                 "req/v3@v3.59.0",
			"profiles":                 []string{"chrome", "firefox"},
			"optional_separate_module": true,
			"ipc_version":              verificationHelperIPCVersion,
			"handshake_before_network": true,
			"platform":                 runtime.GOOS + "/" + runtime.GOARCH,
			"binary_name":              verificationHelperBinaryName(),
		},
		"not_supported": []string{"visual_audio_captcha_solver", "argon2_scrypt", "arbitrary_javascript_wasm", "universal_turnstile_recaptcha_hcaptcha"},
		"limits": map[string]any{
			"response_body_bytes":             32768,
			"response_header_bytes":           32768,
			"flow_timeout_ms":                 15000,
			"flow_roundtrips":                 6,
			"proof_cache_entries_per_toolset": 64,
			"flow_cache_entries_per_toolset":  32,
			"helper_processes":                2,
			"proof_max_attempts":              1000000,
			"proof_max_timeout_ms":            5000,
			"v2_max_kdf_work_units":           2000000,
			"helper_go_memory_soft_mib":       64,
		},
		"models_bundled":     false,
		"automatic_download": false,
	}
}

func (t *ToolSet) verificationCapabilitiesTool() actool.CoreTool {
	return readTool("human_verification_capabilities", "列出真实人机验证原生协议、资源上限和可选指纹helper是否检测到；只检查配置/文件，不执行helper、不发包、不下载。检测到文件不等于已通过版本握手或线上验证。", obj(map[string]any{}), func(context.Context, json.RawMessage) (actool.Result, error) {
		out := VerificationCapabilitiesSnapshot()
		b, _ := json.Marshal(out)
		return actool.Text(string(b)), nil
	})
}

// Optional Go helper. This nested module is not part of ARTEX's default build.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/imroc/req/v3"
)

const frameLimit = 131072
const ipcVersion = 2
const provider = "req/v3@v3.59.0"

type request struct {
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
type response struct {
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

func failure(code string) response {
	return response{Version: ipcVersion, Kind: "response", Provider: provider, Error: code}
}
func canonicalOrigin(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || len(raw) > 2048 {
		return "", fmt.Errorf("url_rejected")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}
func validHeaders(h http.Header, limit int) bool {
	if len(h) > 128 {
		return false
	}
	total, pairs := 0, 0
	seen := map[string]bool{}
	for k, values := range h {
		folded := strings.ToLower(k)
		if seen[folded] {
			return false
		}
		seen[folded] = true
		if len(k) == 0 || len(k) > 256 || len(values) > 64 {
			return false
		}
		for _, c := range k {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
				return false
			}
		}
		total += len(k)
		for _, v := range values {
			pairs++
			total += len(v)
			for _, c := range []byte(v) {
				if c < 32 && c != '\t' || c == 127 {
					return false
				}
			}
		}
	}
	return total <= limit && pairs <= 256
}

type sessionConfig struct{ Profile, HTTPVersion, Proxy, CAPEM, Origin string }

func configOf(a request) sessionConfig {
	return sessionConfig{a.Profile, a.HTTPVersion, a.Proxy, a.CAPEM, a.Origin}
}
func validateSession(a request) error {
	if a.Version != ipcVersion {
		return fmt.Errorf("protocol_rejected")
	}
	origin, e := canonicalOrigin(a.Origin)
	if e != nil || origin != a.Origin {
		return fmt.Errorf("origin_rejected")
	}
	if a.Profile != "chrome" && a.Profile != "firefox" {
		return fmt.Errorf("profile_rejected")
	}
	if a.HTTPVersion != "auto" && a.HTTPVersion != "1.1" && a.HTTPVersion != "2" {
		return fmt.Errorf("protocol_rejected")
	}
	if a.HTTPVersion == "2" && !strings.HasPrefix(a.Origin, "https://") {
		return fmt.Errorf("h2_requires_https")
	}
	if len(a.CAPEM) > 16384 {
		return fmt.Errorf("header_or_ca_limit")
	}
	if a.Proxy != "" {
		u, e := url.Parse(a.Proxy)
		if e != nil || u.Host == "" || u.Fragment != "" || u.RawQuery != "" || u.Path != "" && u.Path != "/" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
			return fmt.Errorf("proxy_rejected")
		}
	}
	return nil
}
func validate(a request) error {
	if err := validateSession(a); err != nil {
		return err
	}
	if a.RequestID < 1 || a.RequestID > 6 || a.Kind != "request" || (a.Method != "GET" && a.Method != "POST" && a.Method != "HEAD") || len(a.Body) > 24576 || a.TimeoutMS < 1 || a.TimeoutMS > 5000 {
		return fmt.Errorf("request_limits")
	}
	origin, e := canonicalOrigin(a.URL)
	if e != nil || origin != a.Origin {
		return fmt.Errorf("origin_rejected")
	}
	if !validHeaders(a.Headers, 8192) || len(a.Headers) > 64 {
		return fmt.Errorf("header_rejected")
	}
	for k := range a.Headers {
		switch strings.ToLower(k) {
		case "host", "content-length", "transfer-encoding", "connection", "proxy-authorization", "proxy-connection", "upgrade", "trailer", "te":
			return fmt.Errorf("header_rejected")
		}
	}
	return nil
}

func newTransport(a request) (*req.Transport, error) {
	c := req.C()
	if a.Profile == "chrome" {
		c.ImpersonateChrome()
	} else {
		c.ImpersonateFirefox()
	}
	t := c.GetTransport()
	// Avoid unbounded Brotli/Zstandard windows on untrusted challenge responses.
	t.DisableCompression = true
	t.AutoDecompression = false
	t.SetMaxResponseHeaderBytes(32768)
	t.MaxConnsPerHost = 1
	t.MaxIdleConns = 1
	t.MaxIdleConnsPerHost = 1
	t.IdleConnTimeout = 5 * time.Second
	t.SetTLSHandshakeTimeout(5 * time.Second)
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}
	if a.CAPEM != "" {
		pool, e := x509.SystemCertPool()
		if e != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(a.CAPEM)) {
			return nil, fmt.Errorf("ca_rejected")
		}
		cfg.RootCAs = pool
	}
	t.SetTLSClientConfig(cfg)
	// No inherited proxy environment: the parent explicitly chooses egress.
	t.SetProxy(nil)
	if a.Proxy != "" {
		p, _ := url.Parse(a.Proxy)
		t.SetProxy(http.ProxyURL(p))
	}
	if a.HTTPVersion == "1.1" {
		t.EnableForceHTTP1()
	} else if a.HTTPVersion == "2" {
		t.EnableForceHTTP2()
	}
	return t, nil
}
func exchange(a request, t *req.Transport) response {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(a.TimeoutMS)*time.Millisecond)
	defer cancel()
	r, e := http.NewRequestWithContext(ctx, a.Method, a.URL, bytes.NewReader(a.Body))
	if e != nil {
		return failure("request_rejected")
	}
	r.Header = a.Headers.Clone()
	if r.Header == nil {
		r.Header = make(http.Header)
	}
	// Apply one internally consistent pinned profile; never mix generic Go-tool UA
	// with a browser TLS profile. Authentication/session headers are retained.
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "sec-ch-") {
			delete(r.Header, k)
		}
	}
	for k, vs := range t.Headers {
		if strings.EqualFold(k, "User-Agent") || strings.HasPrefix(strings.ToLower(k), "sec-ch-") || r.Header.Get(k) == "" {
			r.Header[k] = append([]string(nil), vs...)
		}
	}
	// Safety override: no browser/media compression decoder is activated.
	r.Header.Set("Accept-Encoding", "identity")
	out, e := t.RoundTrip(r)
	if e != nil {
		return failure("transport_failed")
	}
	defer out.Body.Close()
	if encoding := out.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return failure("compressed_response_unsupported")
	}
	body, e := io.ReadAll(io.LimitReader(out.Body, 32769))
	if e != nil {
		return failure("response_read_failed")
	}
	actual := r.Header.Clone()
	delete(actual, req.HeaderOderKey)
	delete(actual, req.PseudoHeaderOderKey)
	return response{Version: ipcVersion, RequestID: a.RequestID, Kind: "response", Provider: provider, Status: out.StatusCode, Proto: out.Proto, Headers: out.Header, RequestHeaders: actual, Body: body}
}
func serve(input io.Reader, output io.Writer) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), frameLimit)
	encoder := json.NewEncoder(output)
	var transport *req.Transport
	defer func() {
		if transport != nil {
			transport.CloseIdleConnections()
		}
	}()
	var config sessionConfig
	// One local handshake, then at most six actual RoundTrip commands.
	for n := 0; n < 7 && scanner.Scan(); n++ {
		var a request
		fail := func(code string) { r := failure(code); r.RequestID = a.RequestID; _ = encoder.Encode(r) }
		if json.Unmarshal(scanner.Bytes(), &a) != nil {
			fail("invalid_frame")
			return
		}
		if n == 0 {
			if a.RequestID != 0 || a.Kind != "hello" || a.URL != "" || a.Method != "" || len(a.Body) != 0 || len(a.Headers) != 0 || a.TimeoutMS != 0 {
				fail("handshake_required")
				return
			}
			if err := validateSession(a); err != nil {
				fail(err.Error())
				return
			}
			var err error
			transport, err = newTransport(a)
			if err != nil {
				fail("configuration_failed")
				return
			}
			config = configOf(a)
			if encoder.Encode(response{Version: ipcVersion, Kind: "ready", Provider: provider}) != nil {
				return
			}
			continue
		}
		if a.RequestID != n {
			fail("request_sequence_rejected")
			return
		}
		if err := validate(a); err != nil {
			fail(err.Error())
			return
		}
		if configOf(a) != config {
			fail("configuration_change_rejected")
			return
		}
		answer := exchange(a, transport)
		answer.RequestID = a.RequestID
		frame, err := json.Marshal(answer)
		if err != nil || len(frame)+1 > frameLimit {
			fail("response_frame_limit")
			return
		}
		if _, err = output.Write(append(frame, '\n')); err != nil {
			return
		}
		if answer.Error != "" {
			return
		}
	}
}
func main() { serve(os.Stdin, os.Stdout) }

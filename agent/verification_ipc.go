package agent

import (
	"context"
	"net/http"
	"slices"
	"strings"
)

const verificationHelperIPCVersion = 2
const verificationHelperProvider = "req/v3@v3.59.0"

type verificationIPCError struct {
	code  string
	cause error
}

func (e *verificationIPCError) Error() string { return "verification helper: " + e.code }
func (e *verificationIPCError) Unwrap() error { return e.cause }
func verificationIPCCancel(err error) error {
	code := "request_cancelled"
	if err == context.DeadlineExceeded {
		code = "request_timeout"
	}
	return &verificationIPCError{code: code, cause: err}
}
func verificationIPCCode(err error) string {
	if e, ok := err.(*verificationIPCError); ok {
		return e.code
	}
	return "helper_start_failed"
}

// A local helper is trusted operator code, not a sandbox. Validate its bounded
// contract nevertheless: corrupt output must never become headers or evidence.
func verificationIPCHeaders(h http.Header, limit int) bool {
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
func verificationIPCFailureCode(code string) string {
	switch code {
	case "request_rejected", "request_limits", "url_rejected", "origin_rejected", "header_rejected", "header_or_ca_limit", "proxy_rejected", "profile_rejected", "protocol_rejected", "h2_requires_https", "configuration_failed", "configuration_change_rejected", "handshake_required", "request_sequence_rejected", "invalid_frame", "response_frame_limit", "transport_failed", "compressed_response_unsupported", "response_read_failed":
		return code
	default:
		return "provider_failed"
	}
}
func validateVerificationIPCResponse(r verificationHelperResponse, request *http.Request, version string) error {
	reject := func() error { return &verificationIPCError{code: "invalid_response"} }
	if r.Version != verificationHelperIPCVersion || r.Kind != "response" || r.Provider != verificationHelperProvider {
		return reject()
	}
	if r.Error != "" {
		return &verificationIPCError{code: verificationIPCFailureCode(r.Error)}
	}
	if r.Status < 200 || r.Status > 599 || len(r.Body) > verificationFlowCapture+1 || !verificationIPCHeaders(r.Headers, 32768) || !verificationIPCHeaders(r.RequestHeaders, 16384) {
		return reject()
	}
	major, minor, ok := http.ParseHTTPVersion(r.Proto)
	if !ok || !(major == 1 && (minor == 0 || minor == 1) || major == 2 && minor == 0) || version == "2" && major != 2 || version == "1.1" && major != 1 || major == 2 && request.URL.Scheme != "https" {
		return reject()
	}
	if request.Method == "HEAD" || r.Status == 204 || r.Status == 205 || r.Status == 304 {
		if len(r.Body) != 0 {
			return reject()
		}
	}
	if strings.TrimSpace(r.RequestHeaders.Get("User-Agent")) == "" {
		return reject()
	}
	// Profile overrides are deliberate; all other nonempty caller headers must
	// remain exactly the same, including Cookie and Authorization.
	for k, values := range request.Header {
		low := strings.ToLower(k)
		if low == "user-agent" || low == "accept-encoding" || strings.HasPrefix(low, "sec-ch-") || request.Header.Get(k) == "" {
			continue
		}
		if !slices.Equal(values, r.RequestHeaders.Values(k)) {
			return &verificationIPCError{code: "session_header_changed"}
		}
	}
	if !strings.EqualFold(r.RequestHeaders.Get("Accept-Encoding"), "identity") {
		return &verificationIPCError{code: "invalid_response"}
	}
	return nil
}

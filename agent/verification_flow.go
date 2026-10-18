package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
	"golang.org/x/net/publicsuffix"
)

const verificationFlowCapture = 32768

// Private options keep bounded flow requests on the existing HTTP recording and
// proxy path without changing defaults for unrelated callers.
type verificationHTTPOptions struct {
	ctx         context.Context
	profile     string
	helper      *verificationHelperTransport
	transport   *http.Transport
	failureCode string
	jar         http.CookieJar
	budget      *verificationRequestBudget
}

type verificationRequestBudget struct {
	mu     sync.Mutex
	origin string
	used   int
}

func verificationOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || len(raw) > 2048 {
		return "", fmt.Errorf("invalid HTTP origin")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}

func (b *verificationRequestBudget) count() int { b.mu.Lock(); defer b.mu.Unlock(); return b.used }

type verificationRoundTripper struct {
	base   http.RoundTripper
	budget *verificationRequestBudget
}

func (r verificationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	origin, err := verificationOrigin(req.URL.String())
	if err != nil || origin != r.budget.origin {
		return nil, fmt.Errorf("verification flow cannot leave its origin")
	}
	r.budget.mu.Lock()
	if r.budget.used >= 6 {
		r.budget.mu.Unlock()
		return nil, fmt.Errorf("verification flow request budget exhausted")
	}
	r.budget.used++
	r.budget.mu.Unlock()
	return r.base.RoundTrip(req)
}

// A bounded transport is owned by one flow and released with it; no global
// map retains clones, connections or proxy-specific settings indefinitely.
func verificationBoundedTransport(base *http.Transport) *http.Transport {
	tr := base.Clone()
	tr.MaxResponseHeaderBytes = 32768
	return tr
}

type verificationFlowInput struct {
	Mode               string            `json:"mode"`
	Protocol           string            `json:"protocol"`
	FingerprintProfile string            `json:"fingerprint_profile"`
	TargetURL          string            `json:"target_url"`
	ChallengeURL       string            `json:"challenge_url"`
	SubmitURL          string            `json:"submit_url"`
	Headers            map[string]string `json:"headers"`
	Form               map[string]string `json:"form"`
	PayloadField       string            `json:"payload_field"`
	SuccessContains    string            `json:"success_body_contains"`
	CSRFField          string            `json:"csrf_field"`
	CSRFRegex          string            `json:"csrf_regex"`
	HTTPVersion        string            `json:"http_version"`
	TimeoutMS          int               `json:"timeout_ms"`
}

type verificationFlowResult struct {
	FailureCode                 string   `json:"failure_code,omitempty"`
	Protocol                    string   `json:"protocol,omitempty"`
	RequestedFingerprintProfile string   `json:"requested_fingerprint_profile,omitempty"`
	TransportProvider           string   `json:"transport_provider,omitempty"`
	State                       string   `json:"state"`
	Stage                       string   `json:"stage"`
	Requests                    int      `json:"requests"`
	ExchangeIDs                 []string `json:"exchange_ids"`
	Category                    string   `json:"category,omitempty"`
	SubmissionAttempted         bool     `json:"submission_attempted"`
	BusinessConfirmed           bool     `json:"business_confirmed"`
	Cached                      bool     `json:"cached"`
	Reason                      string   `json:"reason"`
}

type verificationHTTPResponse struct {
	Status     int                    `json:"status"`
	Body       string                 `json:"body"`
	Truncated  bool                   `json:"body_truncated"`
	ExchangeID string                 `json:"exchange_id"`
	Assessment verificationAssessment `json:"human_verification"`
}

func validateVerificationFlow(a verificationFlowInput) (string, error) {
	origin, err := verificationOrigin(a.TargetURL)
	if err != nil {
		return "", err
	}
	if a.Protocol != "" && a.Protocol != "altcha" && a.Protocol != "anubis_sha256" && a.Protocol != "protocol_only" {
		return "", fmt.Errorf("unsupported flow protocol")
	}
	if a.FingerprintProfile != "" && a.FingerprintProfile != "chrome" && a.FingerprintProfile != "firefox" {
		return "", fmt.Errorf("unsupported fingerprint profile")
	}
	if a.Protocol == "anubis_sha256" && a.SubmitURL == "" {
		return "", fmt.Errorf("Anubis submit_url is required")
	}
	for _, raw := range []string{a.ChallengeURL, a.SubmitURL} {
		if raw == "" && (a.Protocol == "protocol_only" || (a.Protocol == "anubis_sha256" && raw == a.ChallengeURL)) {
			continue
		}
		o, e := verificationOrigin(raw)
		if e != nil || o != origin {
			return "", fmt.Errorf("target/challenge/submit must have the same origin and no URL credentials")
		}
	}
	if a.SuccessContains == "" || len(a.SuccessContains) > 256 {
		return "", fmt.Errorf("success_body_contains is required, at most 256 bytes; HTTP 200 alone is not success")
	}
	if a.HTTPVersion != "" && a.HTTPVersion != "auto" && a.HTTPVersion != "1.1" && a.HTTPVersion != "2" {
		return "", fmt.Errorf("unsupported HTTP protocol")
	}
	if a.HTTPVersion == "2" && !strings.HasPrefix(origin, "https://") {
		return "", fmt.Errorf("HTTP/2 requires HTTPS")
	}
	if a.TimeoutMS < 0 || a.TimeoutMS > 15000 {
		return "", fmt.Errorf("timeout_ms must be 0..15000 (0 uses default)")
	}
	if len(a.Headers) > 32 || len(a.Form) > 32 {
		return "", fmt.Errorf("headers/form limited to 32 fields each")
	}
	headerBytes := 0
	for k, v := range a.Headers {
		headerBytes += len(k) + len(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "host", "content-length", "transfer-encoding", "connection", "content-type":
			return "", fmt.Errorf("flow controls Host/framing/content type; these overrides are not accepted")
		}
		if strings.ContainsAny(k+v, "\r\n") {
			return "", fmt.Errorf("header newlines are not accepted")
		}
	}
	if headerBytes > 8192 {
		return "", fmt.Errorf("request headers exceed 8 KiB")
	}
	form := url.Values{}
	for k, v := range a.Form {
		if len(k) > 128 || len(v) > 4096 {
			return "", fmt.Errorf("form field exceeds bounds")
		}
		form.Set(k, v)
	}
	if len(form.Encode()) > 8192 {
		return "", fmt.Errorf("input form exceeds 8 KiB encoded")
	}
	for _, s := range []string{a.PayloadField, a.CSRFField} {
		if len(s) > 128 {
			return "", fmt.Errorf("field name too long")
		}
	}
	if (a.CSRFField == "") != (a.CSRFRegex == "") {
		return "", fmt.Errorf("csrf_field and csrf_regex must be supplied together")
	}
	if a.CSRFField != "" && a.CSRFField == a.PayloadField {
		return "", fmt.Errorf("CSRF and proof fields must differ")
	}
	if len(a.CSRFRegex) > 1024 {
		return "", fmt.Errorf("CSRF expression exceeds 1024 bytes")
	}
	if a.CSRFRegex != "" {
		rx, e := regexp.Compile(a.CSRFRegex)
		if e != nil || rx.NumSubexp() != 1 {
			return "", fmt.Errorf("CSRF expression requires exactly one capture group")
		}
	}
	return origin, nil
}

// Explicit versioned protocol flows. No general JavaScript evaluator, OCR,
// audio decoder, browser/model downloads or paid services. Fingerprint transport
// is delegated only to an explicitly selected, operator-installed Go helper.
func (t *ToolSet) verificationFlowTool() actool.CoreTool {
	s := &t.verificationState
	s.init()
	gate, cache, origins, submitted := s.flowGate, s.flows, s.origins, s.submitted
	schema := obj(map[string]any{
		"mode":                map[string]any{"type": "string", "enum": []string{"plan", "run"}, "description": "plan不发包；run依协议读取/计算/验证提交/确认，验证GET或POST均可能有副作用"},
		"protocol":            map[string]any{"type": "string", "enum": []string{"altcha", "anubis_sha256", "protocol_only"}, "description": "默认altcha；anubis为旧版SHA256原生链；protocol_only仅GET业务校验"},
		"fingerprint_profile": map[string]any{"type": "string", "enum": []string{"chrome", "firefox"}, "description": "可选Go helper配置；不自动下载或构建，省略用现有标准HTTP"},
		"target_url":          str("校验业务结果的完整 HTTP(S) URL"), "challenge_url": str("同源ALTCHA v1/v2或旧Anubis公开JSON端点；Anubis可留空从目标HTML提取"), "submit_url": str("同源验证端点；ALTCHA为POST、旧Anubis为带证明的GET，须在已有授权内"),
		"headers":       map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"form":          map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "业务表单，最多 32 字段/8 KiB；不接受文件"},
		"payload_field": str("PoW 表单字段，默认 altcha"), "success_body_contains": str("明确的预期业务正文片段，必填，不能只依赖状态码"),
		"csrf_field": str("可选同会话 CSRF 表单字段"), "csrf_regex": str("从初始目标正文提取 CSRF，恰好一个捕获组，最多 1024 字节"),
		"http_version": map[string]any{"type": "string", "enum": []string{"auto", "1.1", "2"}},
		"timeout_ms":   map[string]any{"type": "integer", "minimum": 1, "maximum": 15000, "description": "整个链路墙钟预算，默认 10000"},
	}, "mode", "target_url", "success_body_contains")
	return actool.Build(actool.Spec{Name: "human_verification_flow", Description: "免费Go人机验证链：ALTCHA v1/v2 SHA/PBKDF2、旧Anubis SHA256、protocol_only只读访问。同会话/同源/有界求解/业务确认；可选独立req指纹helper，缺失不自动安装。ALTCHA run含POST，Anubis run含验证GET；plan无网络。每链最多6次客户端RoundTrip(含重定向，transport透明重试不逐一计数)、15秒、每响应32KiB；有限缓存与提交去重。不内置或下载OCR/音频/模型/浏览器，不支持的机制由AI按实际能力主动降级。", Schema: schema,
		ReadOnly: func(in json.RawMessage) bool {
			var a verificationFlowInput
			return json.Unmarshal(in, &a) == nil && a.Mode == "plan"
		}, Permissions: allowAllPerm,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			if len(in) > 65536 {
				return actool.Errorf("flow input exceeds 64 KiB"), nil
			}
			var a verificationFlowInput
			if json.Unmarshal(in, &a) != nil {
				return actool.Errorf("invalid flow arguments"), nil
			}
			if a.Protocol == "" {
				a.Protocol = "altcha"
			}
			if a.PayloadField == "" {
				a.PayloadField = "altcha"
			}
			if a.HTTPVersion == "" {
				a.HTTPVersion = "auto"
			}
			origin, err := validateVerificationFlow(a)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if a.Mode == "plan" {
				b, _ := json.Marshal(map[string]any{"state": "planned", "requests": 0, "protocol": a.Protocol, "fingerprint_profile": a.FingerprintProfile, "steps": []string{"read_target", "inspect_supported_protocol", "compute_bounded_proof_if_needed", "submit_if_required", "confirm_business"}, "models_bundled": false})
				return actool.Text(string(b)), nil
			}
			if a.Mode != "run" {
				return actool.Errorf("mode must be plan or run"), nil
			}
			// Changing timeouts does not restart a completed/failed identical flow.
			identity := a
			identity.TimeoutMS = 0
			b, _ := json.Marshal(identity)
			key := sha256.Sum256(b)
			select {
			case gate <- struct{}{}:
			case <-ctx.Done():
				return actool.Errorf("flow wait cancelled"), nil
			}
			defer func() { <-gate }()
			emit := func(r verificationFlowResult) (actool.Result, error) {
				r.Protocol = a.Protocol
				r.RequestedFingerprintProfile = a.FingerprintProfile
				b, _ := json.Marshal(r)
				return actool.Text(string(b)), nil
			}
			if r, ok := cache[key]; ok {
				r.Cached = true
				return emit(r)
			}
			denied := verificationFlowResult{State: "strategy_exhausted", Stage: "preflight", ExchangeIDs: []string{}, Reason: "Per-instance origin/strategy budget exhausted; no network request made."}
			if submitted[origin] && a.Protocol != "protocol_only" {
				denied.State = "submission_already_attempted"
				denied.Reason = "A verification submission was already attempted for this origin; do not blindly repeat a potentially committed operation."
				return emit(denied)
			}
			if origins[origin] >= 2 || len(cache) >= 32 || (origins[origin] == 0 && len(origins) >= 16) {
				return emit(denied)
			}
			if ctx.Err() != nil {
				return actool.Errorf("flow cancelled before start"), nil
			}
			if a.FingerprintProfile != "" {
				if _, err := verificationHelperPath(); err != nil {
					r := verificationFlowResult{State: "provider_unavailable", Stage: "preflight", ExchangeIDs: []string{}, Reason: "Optional fingerprint helper is not installed/configured; no download, build or network request performed."}
					cache[key] = r
					return emit(r)
				}
			}
			origins[origin]++
			timeout := a.TimeoutMS
			if timeout == 0 {
				timeout = 10000
			}
			runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
			defer cancel()
			jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
			headers := make(map[string]string, len(a.Headers))
			for k, v := range a.Headers {
				if strings.EqualFold(k, "Cookie") {
					h := make(http.Header)
					h.Set("Cookie", v)
					req := &http.Request{Header: h}
					cookies := req.Cookies()
					for _, c := range cookies {
						c.Path = "/"
					}
					u, _ := url.Parse(a.TargetURL)
					jar.SetCookies(u, cookies)
				} else {
					headers[k] = v
				}
			}
			budget := &verificationRequestBudget{origin: origin}
			opts := &verificationHTTPOptions{ctx: runCtx, profile: a.FingerprintProfile, jar: jar, budget: budget}
			defer func() {
				if opts.helper != nil {
					opts.helper.Close()
				}
				if opts.transport != nil {
					opts.transport.CloseIdleConnections()
				}
			}()
			requestTool := t.httpRequestToolWithVerification(opts)
			r := verificationFlowResult{State: "request_failed", Stage: "read_target", ExchangeIDs: []string{}, Reason: "No business success is claimed."}
			request := func(method, raw string, form map[string]string) (verificationHTTPResponse, bool) {
				args := map[string]any{"url": raw, "method": method, "headers": headers, "http_version": a.HTTPVersion, "timeout_ms": 5000, "max_body_bytes": verificationFlowCapture, "follow_redirects": true, "max_redirects": 3}
				if form != nil {
					args["form"] = form
				}
				encoded, _ := json.Marshal(args)
				result, e := requestTool.Call(runCtx, encoded, nil)
				r.Requests = budget.count()
				r.FailureCode = opts.failureCode
				if opts.helper != nil {
					verified, code := opts.helper.status()
					r.FailureCode = code
					if verified {
						r.TransportProvider = verificationHelperProvider
					}
				} else if a.FingerprintProfile == "" && r.Requests > 0 {
					r.TransportProvider = "standard_http"
				}
				var out verificationHTTPResponse
				if e != nil || result.IsError || json.Unmarshal([]byte(result.Flatten()), &out) != nil {
					if r.Requests >= 6 {
						r.State = "request_budget_exhausted"
					}
					if runCtx.Err() != nil {
						r.State = "cancelled_or_timed_out"
					}
					return out, false
				}
				if out.ExchangeID != "" {
					r.ExchangeIDs = append(r.ExchangeIDs, out.ExchangeID)
				}
				if out.Truncated {
					r.State = "response_limit_exceeded"
					return out, false
				}
				if out.Assessment.Category == "suspected_fake_verification" {
					r.State = "suspected_fake_verification"
					return out, false
				}
				if out.Status == 429 {
					r.State = "rate_limited"
					return out, false
				}
				return out, true
			}
			finish := func() (actool.Result, error) { cache[key] = r; return emit(r) }
			successful := func(out verificationHTTPResponse) bool {
				return out.Status >= 200 && out.Status < 300 && out.Assessment.Category != "challenge_page" && out.Assessment.Category != "suspected_fake_verification" && out.Assessment.Category != "proof_of_work" && strings.Contains(out.Body, a.SuccessContains)
			}
			first, ok := request("GET", a.TargetURL, nil)
			if !ok {
				return finish()
			}
			if successful(first) {
				r.State = "business_confirmed"
				r.BusinessConfirmed = true
				r.Reason = "Initial response already satisfies the supplied business criterion; no proof or POST needed."
				return finish()
			}
			if a.Protocol == "protocol_only" {
				r.State = "business_unconfirmed"
				r.Category = first.Assessment.Category
				r.Reason = "Protocol-only response did not satisfy the supplied business criterion; no hidden solver or repeated request executed."
				return finish()
			}
			if first.Assessment.Category == "rate_limit" {
				r.State = "rate_limited"
				r.Reason = "Respect Retry-After and the task budget; no solver or retry loop started."
				return finish()
			}
			form := make(map[string]string, len(a.Form)+2)
			for k, v := range a.Form {
				form[k] = v
			}
			if a.CSRFRegex != "" {
				rx, _ := regexp.Compile(a.CSRFRegex)
				m := rx.FindStringSubmatch(first.Body)
				if len(m) != 2 || m[1] == "" || len(m[1]) > 4096 {
					r.State = "csrf_unavailable"
					return finish()
				}
				form[a.CSRFField] = m[1]
			}
			r.Stage = "fetch_challenge"
			var raw json.RawMessage
			protocol := "altcha_v1"
			if a.Protocol == "anubis_sha256" && a.ChallengeURL == "" {
				var err error
				raw, err = extractVerificationAnubis(first.Body)
				if err != nil {
					r.State = "unsupported_challenge"
					return finish()
				}
				protocol = "anubis_sha256"
			} else {
				challenge, ok := request("GET", a.ChallengeURL, nil)
				if !ok {
					return finish()
				}
				if challenge.Status != 200 || challenge.Assessment.Category == "challenge_page" {
					r.State = "unsupported_challenge"
					r.Category = challenge.Assessment.Category
					return finish()
				}
				raw = json.RawMessage(challenge.Body)
				if a.Protocol == "anubis_sha256" {
					protocol = "anubis_sha256"
				} else {
					var shape map[string]json.RawMessage
					if decodeVerificationJSON(raw, &shape) != nil {
						r.State = "unsupported_challenge"
						return finish()
					}
					if _, ok := shape["parameters"]; ok {
						protocol = "altcha_v2"
					}
				}
			}
			r.Stage = "compute_proof"
			proof, err := t.solveVerification(runCtx, protocol, raw, 100000, time.Second)
			if err != nil {
				r.State = "unsupported_or_over_budget_challenge"
				return finish()
			}
			if proof.State != "proof_computed" {
				r.State = "proof_not_computed"
				r.Reason = proof.State
				return finish()
			}
			if protocol == "anubis_sha256" {
				r.Stage = "submit_proof"
				submit, _ := url.Parse(a.SubmitURL)
				q := submit.Query()
				q.Set("id", proof.ChallengeID)
				q.Set("response", proof.ResponseHash)
				q.Set("nonce", fmt.Sprint(*proof.Number))
				q.Set("redir", a.TargetURL)
				q.Set("elapsedTime", fmt.Sprint(proof.ElapsedMS))
				submit.RawQuery = q.Encode()
				if len(submit.String()) > 2048 {
					r.State = "submission_size_limit"
					return finish()
				}
				submitted[origin] = true
				r.SubmissionAttempted = true
				out, ok := request("GET", submit.String(), nil)
				if !ok {
					r.Reason = "Verification submission may have reached the server; no automatic replay."
					return finish()
				}
				if successful(out) {
					r.State = "business_confirmed"
					r.BusinessConfirmed = true
					r.Reason = "Anubis submission/redirect satisfied the supplied business criterion."
					return finish()
				}
				if out.Status >= 400 || out.Assessment.Category == "challenge_page" {
					r.State = "submission_rejected"
					return finish()
				}
				r.Stage = "confirm_business"
				last, ok := request("GET", a.TargetURL, nil)
				if !ok {
					return finish()
				}
				if successful(last) {
					r.State = "business_confirmed"
					r.BusinessConfirmed = true
					r.Reason = "Independent business response confirmed."
				} else {
					r.State = "submitted_unconfirmed"
				}
				return finish()
			}

			form[a.PayloadField] = proof.Payload
			values := url.Values{}
			for k, v := range form {
				values.Set(k, v)
			}
			if len(values.Encode()) > 24576 {
				r.State = "submission_size_limit"
				return finish()
			}
			// Even a transport error can follow a committed POST. Never auto-replay it.
			r.Stage = "submit_proof"
			submitted[origin] = true
			r.SubmissionAttempted = true
			post, ok := request("POST", a.SubmitURL, form)
			if !ok {
				r.Reason = "Submission may have reached the server; automatic replay disabled."
				return finish()
			}
			if successful(post) {
				r.State = "business_confirmed"
				r.BusinessConfirmed = true
				r.Reason = "Submission response satisfies the supplied business criterion."
				return finish()
			}
			if post.Status < 200 || post.Status >= 400 || post.Assessment.Category == "challenge_page" {
				r.State = "submission_rejected"
				return finish()
			}
			r.Stage = "confirm_business"
			last, ok := request("GET", a.TargetURL, nil)
			if !ok {
				return finish()
			}
			if successful(last) {
				r.State = "business_confirmed"
				r.BusinessConfirmed = true
				r.Reason = "Independent follow-up response satisfies the supplied business criterion."
			} else {
				r.State = "submitted_unconfirmed"
				r.Reason = "Proof submitted but expected business content was not observed; do not claim success or replay POST."
			}
			return finish()
		},
	})
}

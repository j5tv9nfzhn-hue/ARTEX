package agent

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

const verificationMaxInput = 256 * 1024

// Assessment is advisory: widget presence is not an access failure, nor does a
// computed proof imply that a protected business operation was accepted.
type verificationAssessment struct {
	Category    string   `json:"category"`
	Vendor      string   `json:"vendor"`
	Confidence  string   `json:"confidence"`
	Evidence    []string `json:"evidence"`
	NextActions []string `json:"next_actions"`
}

func inspectVerification(status int, headers map[string]string, body string) verificationAssessment {
	a := verificationAssessment{Category: "unknown", Vendor: "unknown", Confidence: "low", Evidence: []string{}, NextActions: []string{"use_existing_authorized_tools_only_when_new_evidence_supports_a_different_strategy"}}
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	lower := strings.ToLower(clipUTF8(body, 131072))
	verificationContext := strings.Contains(lower, "captcha") || strings.Contains(lower, "verify") || strings.Contains(lower, "验证")
	terminal := strings.Contains(lower, "powershell") || strings.Contains(lower, "cmd.exe") || strings.Contains(lower, "终端")
	paste := strings.Contains(lower, "paste") || strings.Contains(lower, "粘贴") || strings.Contains(lower, "win + r") || strings.Contains(lower, "windows + r")
	if verificationContext && terminal && paste {
		a.Category = "suspected_fake_verification"
		a.Confidence = "medium"
		a.Evidence = []string{"verification_content_combines_terminal_and_paste_instructions"}
		a.NextActions = []string{"do_not_execute_page_commands", "record_suspected_clickfix_and_continue_unaffected_work"}
		return a
	}
	if strings.EqualFold(strings.TrimSpace(h.Get("cf-mitigated")), "challenge") {
		a.Category = "challenge_page"
		a.Vendor = "cloudflare"
		a.Confidence = "high"
		a.Evidence = []string{"cf-mitigated: challenge"}
		a.NextActions = []string{"check_protocol_and_session_compatibility", "select_available_existing_browser_or_protocol_tools_with_a_finite_strategy_budget", "do_not_repeat_identical_failed_strategy"}
		return a
	}
	action := strings.ToLower(strings.TrimSpace(h.Get("x-amzn-waf-action")))
	if (action == "captcha" && status == 405) || (action == "challenge" && status == 202) {
		a.Category = "challenge_page"
		a.Vendor = "aws_waf"
		a.Confidence = "high"
		a.Evidence = []string{"x-amzn-waf-action_matches_documented_response_status"}
		a.NextActions = []string{"distinguish_captcha_from_challenge", "select_applicable_existing_tools_with_a_finite_strategy_budget"}
		return a
	}
	html := strings.Contains(strings.ToLower(h.Get("Content-Type")), "html") || strings.HasPrefix(strings.TrimSpace(lower), "<")
	if html && strings.Contains(lower, "/cdn-cgi/challenge-platform/") && (strings.Contains(lower, "__cf_chl") || strings.Contains(lower, "just a moment")) {
		a.Category = "challenge_page"
		a.Vendor = "cloudflare"
		a.Confidence = "medium"
		a.Evidence = []string{"multiple_html_challenge_markers_not_vendor_header_alone"}
		a.NextActions = []string{"confirm_expected_business_content", "select_an_evidence_based_compatibility_strategy_not_identical_retries"}
		return a
	}
	if status == 429 {
		a.Category = "rate_limit"
		a.Confidence = "medium"
		a.Evidence = []string{"status_429_not_vendor_identification"}
		a.NextActions = []string{"respect_retry_after_and_task_budget", "do_not_use_solver_as_a_rate_limit_retry_loop"}
		return a
	}
	if html && strings.Contains(lower, "anubis_challenge") {
		if _, err := extractVerificationAnubis(body); err == nil {
			a.Category = "proof_of_work"
			a.Vendor = "anubis"
			a.Confidence = "high"
			a.Evidence = []string{"bounded_supported_anubis_public_json"}
			a.NextActions = []string{"use_only_explicit_legacy_fast_slow_adapter_or_report_unsupported_version"}
			return a
		}
	}
	if status == 401 && h.Get("WWW-Authenticate") != "" {
		a.Category = "auth_required"
		a.Confidence = "high"
		a.Evidence = []string{"status_401_with_www_authenticate"}
		a.NextActions = []string{"use_only_existing_authorized_authentication_context"}
		return a
	}
	if status >= 400 {
		a.Category = "request_denied"
		a.Confidence = "medium"
		a.Evidence = []string{"http_error_without_confirmed_challenge"}
		a.NextActions = []string{"separate_application_rejection_authentication_and_edge_policy"}
		return a
	}
	if html && strings.Contains(lower, "anubis_challenge") {
		a.Category = "verification_candidate"
		a.Vendor = "anubis"
		a.Confidence = "low"
		a.Evidence = []string{"anubis_marker_not_validated_as_supported_challenge"}
		a.NextActions = []string{"continue_readable_business_content", "inspect_actual_public_protocol_before_selecting_any_solver"}
		return a
	}
	if html {
		for _, marker := range []struct{ text, vendor string }{{"cf-turnstile", "cloudflare_turnstile"}, {"g-recaptcha", "google_recaptcha"}, {"h-captcha", "hcaptcha"}, {"altcha-widget", "altcha"}} {
			if strings.Contains(lower, marker.text) {
				a.Category = "embedded_widget"
				a.Vendor = marker.vendor
				a.Confidence = "medium"
				a.Evidence = []string{"html_widget_marker_not_proof_of_blocking"}
				a.NextActions = []string{"continue_readable_business_content", "inspect_only_the_action_that_requires_verification"}
				if marker.vendor == "altcha" {
					a.NextActions = append(a.NextActions, "legacy_solve_pow_is_v1_only_use_human_verification_solve_for_explicit_supported_v1_v2_sha_pbkdf2")
				}
				return a
			}
		}
	}
	return a
}

type verificationPoWChallenge struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
	MaxNumber *int64 `json:"maxNumber,omitempty"`
}

type verificationPoWResult struct {
	ResponseHash     string `json:"response_hash,omitempty"`
	ChallengeID      string `json:"challenge_id,omitempty"`
	State            string `json:"state"`
	Protocol         string `json:"protocol"`
	Attempts         int64  `json:"attempts"`
	ElapsedMS        int64  `json:"elapsed_ms"`
	Number           *int64 `json:"number,omitempty"`
	Payload          string `json:"payload_base64,omitempty"`
	Cached           bool   `json:"cached"`
	BusinessAccepted bool   `json:"business_accepted"`
	Note             string `json:"note"`
}

// This implements the public salt+decimal-number client proof, not a secret
// signature forge or the newer Argon2/Sentinel protocols. The supplied signature
// is carried through unchanged; only the receiving service can accept it.
// Protocol reference: https://github.com/altcha-org/altcha-lib-go (Challenge/Payload).
func validateVerificationPoW(c verificationPoWChallenge) error {
	size := 32
	if c.Algorithm == "SHA-512" {
		size = 64
	} else if c.Algorithm != "SHA-256" {
		return fmt.Errorf("unsupported algorithm: only SHA-256/SHA-512 salt+number challenges are supported")
	}
	raw, err := hex.DecodeString(c.Challenge)
	if err != nil || len(raw) != size {
		return fmt.Errorf("challenge must be the algorithm's full hexadecimal digest")
	}
	if c.Salt == "" || len(c.Salt) > 4096 || c.Signature == "" || len(c.Signature) > 4096 {
		return fmt.Errorf("salt/signature must be nonempty and at most 4096 bytes")
	}
	if c.MaxNumber != nil && (*c.MaxNumber < 0 || *c.MaxNumber > 10000000) {
		return fmt.Errorf("maxNumber must be within 0..10000000")
	}
	if i := strings.IndexByte(c.Salt, '?'); i >= 0 {
		params, err := url.ParseQuery(c.Salt[i+1:])
		if err != nil {
			return fmt.Errorf("malformed salt query")
		}
		if values, exists := params["expires"]; exists {
			if len(values) != 1 {
				return fmt.Errorf("ambiguous challenge expiry")
			}
			expires, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil || expires <= time.Now().Unix() {
				return fmt.Errorf("challenge expiry is invalid or expired")
			}
		}
	}
	return nil
}

func solveVerificationPoW(ctx context.Context, c verificationPoWChallenge, maxAttempts int64, timeout time.Duration) verificationPoWResult {
	r := verificationPoWResult{State: "budget_exhausted", Protocol: "altcha-salt-number-sha2", Note: "Local proof computation only; no network request, HMAC verification, token acceptance or business success is claimed."}
	start := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for n := int64(0); n < maxAttempts; n++ {
		if c.MaxNumber != nil && n > *c.MaxNumber {
			r.State = "no_solution_in_declared_range"
			break
		}
		if err := runCtx.Err(); err != nil {
			r.State = "cancelled"
			if ctx.Err() == nil {
				r.State = "time_budget_exhausted"
			}
			break
		}
		data := []byte(c.Salt + strconv.FormatInt(n, 10))
		var digest string
		if c.Algorithm == "SHA-256" {
			sum := sha256.Sum256(data)
			digest = hex.EncodeToString(sum[:])
		} else {
			sum := sha512.Sum512(data)
			digest = hex.EncodeToString(sum[:])
		}
		r.Attempts++
		if strings.EqualFold(digest, c.Challenge) {
			payload := struct {
				Algorithm string `json:"algorithm"`
				Challenge string `json:"challenge"`
				Number    int64  `json:"number"`
				Salt      string `json:"salt"`
				Signature string `json:"signature"`
			}{c.Algorithm, c.Challenge, n, c.Salt, c.Signature}
			b, _ := json.Marshal(payload)
			r.Number = &n
			r.Payload = base64.StdEncoding.EncodeToString(b)
			r.State = "proof_computed"
			break
		}
	}
	r.ElapsedMS = time.Since(start).Milliseconds()
	return r
}

func (t *ToolSet) humanVerificationTool() actool.CoreTool {
	// Compatibility entry point: proof budget/cache is shared by this ToolSet.
	return readTool("human_verification", "免费原生 Go 人机验证专用工具。mode=inspect 对已有响应分类，不发包，不把橙色云或 widget 当作必然阻断；mode=solve_pow 有界计算明确的 ALTCHA salt+number SHA-256/SHA-512 挑战并返回标准 Base64 payload。同一 ToolSet 的 solver/flow/旧入口共享结果，最多处理 64 个不同挑战。不含视觉/音频打码、Argon2、Cloudflare 指纹突破或网络访问；降级由 AI 使用现有工具选择，无人工交互依赖。",
		obj(map[string]any{
			"mode":    map[string]any{"type": "string", "enum": []string{"inspect", "solve_pow"}},
			"status":  map[string]any{"type": "integer", "minimum": 0, "maximum": 599},
			"headers": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"body":    map[string]any{"type": "string", "description": "已有响应正文，最多 131072 字节，不需要传凭据"},
			"challenge": map[string]any{"type": "object", "description": "公开 PoW 对象：algorithm/challenge/salt/signature，可选 maxNumber；不接受服务端 HMAC 密钥", "properties": map[string]any{
				"algorithm": map[string]any{"type": "string", "enum": []string{"SHA-256", "SHA-512"}},
				"challenge": str("完整十六进制摘要"), "salt": str("原始 salt，保留其公开查询参数"), "signature": str("原始公开签名，不伪造"), "maxNumber": map[string]any{"type": "integer", "minimum": 0, "maximum": 10000000},
			}, "required": []string{"algorithm", "challenge", "salt", "signature"}},
			"max_attempts": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000000, "description": "默认 100000，单次硬上限 1000000；改预算不会重算已缓存挑战"},
			"timeout_ms":   map[string]any{"type": "integer", "minimum": 1, "maximum": 5000, "description": "默认 2000，硬上限 5000"},
		}, "mode"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if len(in) > verificationMaxInput {
				return actool.Errorf("输入超过 256 KiB"), nil
			}
			var a struct {
				Mode        string                   `json:"mode"`
				Status      int                      `json:"status"`
				Headers     map[string]string        `json:"headers"`
				Body        string                   `json:"body"`
				Challenge   verificationPoWChallenge `json:"challenge"`
				MaxAttempts int64                    `json:"max_attempts"`
				TimeoutMS   int                      `json:"timeout_ms"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败"), nil
			}
			if a.Mode == "inspect" {
				if a.Status < 0 || a.Status > 599 || len(a.Body) > 131072 || len(a.Headers) > 128 {
					return actool.Errorf("响应范围超限：status 0..599，body <=128 KiB，headers <=128 项"), nil
				}
				b, _ := json.Marshal(inspectVerification(a.Status, a.Headers, a.Body))
				return actool.Text(string(b)), nil
			}
			if a.Mode != "solve_pow" {
				return actool.Errorf("mode 必须是 inspect 或 solve_pow"), nil
			}
			if err := validateVerificationPoW(a.Challenge); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if a.MaxAttempts == 0 {
				a.MaxAttempts = 100000
			}
			if a.TimeoutMS == 0 {
				a.TimeoutMS = 2000
			}
			if a.MaxAttempts < 1 || a.MaxAttempts > 1000000 || a.TimeoutMS < 1 || a.TimeoutMS > 5000 {
				return actool.Errorf("计算预算超限：max_attempts 1..1000000；timeout_ms 1..5000"), nil
			}
			raw, _ := json.Marshal(a.Challenge)
			r, err := t.solveVerification(ctx, "altcha_v1", raw, a.MaxAttempts, time.Duration(a.TimeoutMS)*time.Millisecond)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			r.Protocol = "altcha-salt-number-sha2"
			b, _ := json.Marshal(r)
			return actool.Text(string(b)), nil
		})
}

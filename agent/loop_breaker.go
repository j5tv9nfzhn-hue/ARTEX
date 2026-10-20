package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

const loopStrikeLimit = 3

type loopSlot struct {
	gate      chan struct{}
	lastSig   string
	streak    int
	blocked   bool
	totalHits int
}

// loopBreaker is local to one AugmentTools invocation, not persistent across
// runs. It gates only http_request/http_fuzz. Bash, MCP, other tools and traffic
// inside one fuzz call are outside its scope: this is not a network sandbox.
// Same-key calls are serialized so concurrent calls cannot pass a tripped gate.
// There is no model-callable reset; a new run starts with a new instance.
type loopBreaker struct {
	mu    sync.Mutex
	slots map[string]*loopSlot
}

func newLoopBreaker() *loopBreaker { return &loopBreaker{slots: make(map[string]*loopSlot)} }

func (lb *loopBreaker) slot(key string) *loopSlot {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	s := lb.slots[key]
	if s == nil {
		s = &loopSlot{gate: make(chan struct{}, 1)}
		lb.slots[key] = s
	}
	return s
}

// before returns a release function or an error result without invoking the tool.
func (lb *loopBreaker) before(ctx context.Context, name string, in json.RawMessage) (func(), *actool.Result) {
	noop := func() {}
	if lb == nil {
		return noop, nil
	}
	key := loopTargetKey(name, in)
	if key == "" {
		return noop, nil
	}
	s := lb.slot(key)
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		res := actool.Errorf("等待同目标执行槽位已取消：" + ctx.Err().Error() +
			"。同一目标同一时刻只允许一次调用，本次在排队期间等待超时（通常是 run 时间预算耗尽，或同目标并发请求太多）。请减少同目标并发、改为顺序执行，或合并成一次 http_fuzz/http_sequence 批量调用。目标标识：" + key)
		return noop, &res
	}
	var once sync.Once
	release := func() { once.Do(func() { <-s.gate }) }
	if err := ctx.Err(); err != nil {
		release()
		res := actool.Errorf("同目标调用已取消：" + err.Error() + "。本次未执行外部调用。目标标识：" + key)
		return noop, &res
	}
	lb.mu.Lock()
	blocked := s.blocked
	streak := s.streak
	lb.mu.Unlock()
	if blocked {
		release()
		res := actool.Errorf(fmt.Sprintf("[执行层止损 / loop_blocked] 本轮同一工具、方法、完整 URL 与请求上下文已连续 %d 次收到相同负反馈。本次未执行外部调用。请记录观察与限制并结束该尝试，或在授权范围内选择不同方法；不要改无关参数绕过止损。新执行轮会重建本轮状态，这不是跨轮安全隔离。目标标识：%s", streak, key))
		return noop, &res
	}
	return release, nil
}

func (lb *loopBreaker) inspect(name string, in json.RawMessage, res actool.Result, callErr error) actool.Result {
	if lb == nil || callErr != nil {
		return res
	}
	key := loopTargetKey(name, in)
	if key == "" {
		return res
	}
	sig, diag := classifyNegativeOutcome(name, res)
	s := lb.slot(key)
	lb.mu.Lock()
	s.totalHits++
	if sig == "" {
		s.streak = 0
		s.lastSig = ""
		lb.mu.Unlock()
		return res
	}
	if s.lastSig == sig {
		s.streak++
	} else {
		s.lastSig = sig
		s.streak = 1
	}
	if s.streak >= loopStrikeLimit {
		s.blocked = true
	}
	blocked, streak := s.blocked, s.streak
	lb.mu.Unlock()
	if !blocked {
		return res
	}
	warning := fmt.Sprintf("\n[执行层止损 / 3-Strike]\n已观察到连续 %d 次相同负反馈；本轮后续同键调用将于执行前拒绝。%s\n这仅说明当前请求上下文与方法未获得有效结果，不能证明后端实现、参数化查询、漏洞不存在或整个目标安全。记录原始状态与证据，将解释标为推断；不要以止损代替目标达成证明。", streak, diag)
	// Append one text block; preserve existing text/non-text blocks, IsError, Extra.
	res.Content = append(append([]llm.ContentBlock(nil), res.Content...), llm.TextBlock(warning))
	return res
}

// Keys preserve literal paths (no replacing numeric/UUID resource identities).
// Full query values and header/cookie/auth/body partition legitimate contexts.
// No inference that differing resource IDs or payloads share an intent is made.
// Only a short opaque digest is exposed, never credentials or payloads.
func loopTargetKey(name string, raw json.RawMessage) string {
	if name != "http_request" && name != "http_fuzz" {
		return ""
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	var rawURL, method string
	if name == "http_fuzz" {
		_ = json.Unmarshal(in["url_template"], &rawURL)
	}
	if rawURL == "" && json.Unmarshal(in["url"], &rawURL) != nil {
		return ""
	}
	_ = json.Unmarshal(in["method"], &method)
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = "GET"
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ""
	}
	// Preserve all declared fields except URL and generic request/size timeouts.
	// Canonical decoding also makes JSON object field order irrelevant.
	contextFields := make(map[string]any)
	for k, v := range in {
		if k == "url" || k == "url_template" || k == "method" || k == "timeout_ms" || k == "max_body_bytes" {
			continue
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(v))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			return ""
		}
		contextFields[k] = value
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	userinfo := ""
	if u.User != nil {
		userinfo = u.User.String()
	}
	identity := []any{name, method, strings.ToLower(u.Scheme), strings.ToLower(u.Host), path, query.Encode(), userinfo, contextFields}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return name + ":" + hex.EncodeToString(sum[:12])
}

func classifyNegativeOutcome(name string, res actool.Result) (string, string) {
	text := res.Flatten()
	if res.IsError {
		sum := sha256.Sum256([]byte(text))
		return "tool_error:" + hex.EncodeToString(sum[:]), "工具返回错误；请区分参数、网络和服务端错误。"
	}
	switch name {
	case "http_request":
		var out struct {
			Status    int               `json:"status"`
			Headers   map[string]string `json:"headers"`
			Body      string            `json:"body"`
			Error     string            `json:"error"`
			Truncated bool              `json:"body_truncated"`
		}
		if json.Unmarshal([]byte(text), &out) != nil {
			return "", ""
		}
		if out.Error != "" {
			sum := sha256.Sum256([]byte(out.Error))
			return "network:" + hex.EncodeToString(sum[:]), "观察到网络错误；不能据此推断服务不存在或被封禁。"
		}
		assessment := inspectVerification(out.Status, out.Headers, out.Body)
		if assessment.Category == "challenge_page" {
			return "challenge:" + assessment.Vendor, "确认当前响应为挑战页，而非业务成功；允许基于新证据选择不同的协议/已有浏览器策略，不默认要求人工，不能重复同一失败策略。"
		}
		if out.Truncated || out.Status < 400 || out.Status > 599 {
			return "", ""
		}
		// Use the full retained body, not a prefix or digit-stripping heuristic.
		sum := sha256.Sum256([]byte(strings.TrimSpace(out.Body)))
		return fmt.Sprintf("http:%d:%x", out.Status, sum), diagnoseHTTPFailure(out.Status)
	case "http_fuzz":
		var out struct {
			Hits   *int `json:"hits_count"`
			Total  *int `json:"total_tested"`
			Errors *int `json:"error_count"`
		}
		if json.Unmarshal([]byte(text), &out) != nil || out.Hits == nil || out.Total == nil || out.Errors == nil {
			return "", ""
		}
		if *out.Total <= 0 || *out.Hits < 0 || *out.Errors < 0 || *out.Hits > *out.Total || *out.Errors > *out.Total {
			return "", ""
		}
		if *out.Hits == 0 {
			if *out.Errors == *out.Total {
				return fmt.Sprintf("fuzz_all_errors:%d", *out.Total), "本轮批量请求全部报错；应检查可达性与参数，不能推断目标安全"
			}
			return fmt.Sprintf("fuzz_no_hits:%d:%d", *out.Total, *out.Errors), "本轮字典未命中仅覆盖本次输入与匹配规则，不证明目标不存在问题"
		}
	}
	return "", ""
}

func diagnoseHTTPFailure(status int) string {
	switch status {
	case 400, 422:
		return "当前输入收到参数/请求拒绝响应；具体校验层与技术栈需要独立证据。"
	case 401, 403:
		return "当前凭据或请求上下文收到访问拒绝；拒绝来源和授权边界尚需核实。"
	case 404:
		return "当前请求收到 404；不能区分路由缺失、访问隐藏或代理返回。"
	case 429:
		return "观察到限流响应；应停止重复请求，并依据授权与服务端指引安排后续。"
	default:
		return fmt.Sprintf("观察到 HTTP %d 错误响应；不能据此证明 ORM、参数化查询或某类问题已被排除。", status)
	}
}

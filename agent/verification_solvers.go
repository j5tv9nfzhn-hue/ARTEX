package agent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// Shared by all native verification entry points attached to this ToolSet.
// The scope is one ToolSet, not a cross-task/global network sandbox.
type verificationState struct {
	once      sync.Once
	proofGate chan struct{}
	proofs    map[[32]byte]verificationPoWResult
	flowGate  chan struct{}
	flows     map[[32]byte]verificationFlowResult
	origins   map[string]int
	submitted map[string]bool
}

func (s *verificationState) init() {
	s.once.Do(func() {
		s.proofGate = make(chan struct{}, 1)
		s.proofs = map[[32]byte]verificationPoWResult{}
		s.flowGate = make(chan struct{}, 1)
		s.flows = map[[32]byte]verificationFlowResult{}
		s.origins = map[string]int{}
		s.submitted = map[string]bool{}
	})
}

func decodeVerificationJSON(raw json.RawMessage, v any) error {
	if len(raw) == 0 || len(raw) > 16384 {
		return fmt.Errorf("challenge must be 1..16384 bytes")
	}
	// Reject pathological nesting before decoding/allocating arbitrary challenge data.
	depth := 0
	quoted := false
	escaped := false
	for _, ch := range raw {
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				quoted = false
			}
			continue
		}
		if ch == '"' {
			quoted = true
			continue
		}
		if ch == '{' || ch == '[' {
			depth++
			if depth > 32 {
				return fmt.Errorf("challenge JSON nesting exceeds 32")
			}
		} else if ch == '}' || ch == ']' {
			depth--
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("invalid challenge JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing challenge input")
	}
	return nil
}

func (t *ToolSet) solveVerification(ctx context.Context, protocol string, raw json.RawMessage, attempts int64, timeout time.Duration) (verificationPoWResult, error) {
	if attempts < 1 || attempts > 1000000 || timeout <= 0 || timeout > 5*time.Second {
		return verificationPoWResult{}, fmt.Errorf("proof budget exceeds supported bounds")
	}
	var identity any
	var legacy verificationPoWChallenge
	var v2 verificationV2Challenge
	var anubis verificationAnubisChallenge
	switch protocol {
	case "altcha_v1":
		if err := decodeVerificationJSON(raw, &legacy); err != nil {
			return verificationPoWResult{}, err
		}
		if err := validateVerificationPoW(legacy); err != nil {
			return verificationPoWResult{}, err
		}
		identity = []string{protocol, legacy.Algorithm, strings.ToLower(legacy.Challenge), legacy.Salt, legacy.Signature}
	case "altcha_v2":
		if err := decodeVerificationJSON(raw, &v2); err != nil {
			return verificationPoWResult{}, err
		}
		if err := validateVerificationV2(v2); err != nil {
			return verificationPoWResult{}, err
		}
		// Unsigned envelope noise and JSON formatting cannot reset a proof budget.
		// Signature binds auxiliary data; the returned payload retains the first raw
		// signed challenge rather than silently rewriting it for a later caller.
		canonical := v2.Parameters
		canonical.Salt = strings.ToLower(canonical.Salt)
		canonical.Nonce = strings.ToLower(canonical.Nonce)
		canonical.KeyPrefix = strings.ToLower(canonical.KeyPrefix)
		identity = []any{protocol, canonical, v2.Signature}
	case "anubis_sha256":
		var err error
		anubis, err = parseVerificationAnubis(raw)
		if err != nil {
			return verificationPoWResult{}, err
		}
		identity = []any{protocol, anubis.Challenge.ID, anubis.Challenge.RandomData, anubis.Challenge.Method, anubis.Challenge.Difficulty}
	default:
		return verificationPoWResult{}, fmt.Errorf("unsupported protocol; no runtime or model will be downloaded")
	}
	encoded, _ := json.Marshal(identity)
	key := sha256.Sum256(encoded)
	s := &t.verificationState
	s.init()
	select {
	case s.proofGate <- struct{}{}:
	case <-ctx.Done():
		return verificationPoWResult{}, ctx.Err()
	}
	defer func() { <-s.proofGate }()
	if ctx.Err() != nil {
		return verificationPoWResult{}, ctx.Err()
	}
	if r, ok := s.proofs[key]; ok {
		r.Cached = true
		return r, nil
	}
	if len(s.proofs) >= 64 {
		return verificationPoWResult{State: "strategy_exhausted", Protocol: protocol, Note: "64 distinct challenges already processed in this ToolSet."}, nil
	}
	var r verificationPoWResult
	switch protocol {
	case "altcha_v1":
		r = solveVerificationPoW(ctx, legacy, attempts, timeout)
	case "altcha_v2":
		r = solveVerificationV2(ctx, raw, v2, attempts, timeout)
	case "anubis_sha256":
		r = solveVerificationAnubis(ctx, anubis, attempts, timeout)
	}
	r.Protocol = protocol
	s.proofs[key] = r
	return r, nil
}

type verificationV2Parameters struct {
	Algorithm   string `json:"algorithm"`
	Nonce       string `json:"nonce"`
	Salt        string `json:"salt"`
	Cost        int    `json:"cost"`
	KeyLength   int    `json:"keyLength"`
	KeyPrefix   string `json:"keyPrefix"`
	MemoryCost  int    `json:"memoryCost"`
	Parallelism int    `json:"parallelism"`
	ExpiresAt   int64  `json:"expiresAt"`
}
type verificationV2Challenge struct {
	Parameters verificationV2Parameters `json:"parameters"`
	Signature  string                   `json:"signature"`
}

func verificationHash(algorithm string) (func() hash.Hash, int) {
	switch strings.TrimPrefix(algorithm, "PBKDF2/") {
	case "SHA-256":
		return sha256.New, 32
	case "SHA-384":
		return sha512.New384, 48
	case "SHA-512":
		return sha512.New, 64
	}
	return nil, 0
}
func validateVerificationV2(c verificationV2Challenge) error {
	p := c.Parameters
	_, size := verificationHash(p.Algorithm)
	if size == 0 {
		return fmt.Errorf("unsupported v2 KDF; only SHA-2/PBKDF2, not Scrypt/Argon2/WASM")
	}
	if p.Cost < 1 || p.Cost > 20000 || p.KeyLength < 1 || p.KeyLength > 64 || p.MemoryCost != 0 || p.Parallelism > 1 || p.Parallelism < 0 {
		return fmt.Errorf("v2 KDF resource parameters rejected before allocation")
	}
	if !strings.HasPrefix(p.Algorithm, "PBKDF2/") && p.KeyLength > size {
		return fmt.Errorf("iterated SHA keyLength exceeds digest size")
	}
	for _, s := range []string{p.Nonce, p.Salt} {
		b, e := hex.DecodeString(s)
		if e != nil || len(b) == 0 || len(b) > 256 {
			return fmt.Errorf("v2 salt/nonce must be bounded hexadecimal")
		}
	}
	if len(c.Signature) == 0 || len(c.Signature) > 4096 {
		return fmt.Errorf("v2 public signature required and bounded")
	}
	if p.KeyPrefix == "" || len(p.KeyPrefix) > p.KeyLength*2 {
		return fmt.Errorf("invalid v2 keyPrefix length")
	}
	prefix := p.KeyPrefix
	if len(prefix)%2 != 0 {
		prefix += "0"
	}
	if _, e := hex.DecodeString(prefix); e != nil {
		return fmt.Errorf("invalid v2 keyPrefix hex")
	}
	if p.ExpiresAt < 0 || (p.ExpiresAt > 0 && p.ExpiresAt <= time.Now().Unix()) {
		return fmt.Errorf("v2 challenge expired or invalid expiry")
	}
	return nil
}

// Interruptible PBKDF2. Salt/nonce/key length/cost are checked before entry.
// RFC 8018: U1=PRF(password,salt||INT32_BE(block)), T=U1 xor ... xor Uc.
func verificationPBKDF2(ctx context.Context, newHash func() hash.Hash, password, salt []byte, cost, keyLen int) ([]byte, error) {
	prf := hmac.New(newHash, password)
	size := prf.Size()
	result := make([]byte, 0, keyLen+size)
	var counter [4]byte
	for block := 1; len(result) < keyLen; block++ {
		binary.BigEndian.PutUint32(counter[:], uint32(block))
		prf.Reset()
		_, _ = prf.Write(salt)
		_, _ = prf.Write(counter[:])
		u := prf.Sum(nil)
		out := append([]byte(nil), u...)
		for n := 1; n < cost; n++ {
			if n%128 == 0 && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			prf.Reset()
			_, _ = prf.Write(u)
			u = prf.Sum(u[:0])
			for i := range out {
				out[i] ^= u[i]
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		result = append(result, out...)
	}
	return result[:keyLen], nil
}
func solveVerificationV2(ctx context.Context, raw json.RawMessage, c verificationV2Challenge, attempts int64, timeout time.Duration) verificationPoWResult {
	r := verificationPoWResult{State: "budget_exhausted", Protocol: "altcha_v2", Note: "Local public KDF computation only; signature and business acceptance are not verified."}
	start := time.Now()
	run, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p := c.Parameters
	salt, _ := hex.DecodeString(p.Salt)
	nonce, _ := hex.DecodeString(p.Nonce)
	newHash, size := verificationHash(p.Algorithm)
	password := make([]byte, len(nonce)+4)
	copy(password, nonce)
	units := int64(p.Cost * ((p.KeyLength + size - 1) / size))
	for n := int64(0); n < attempts; n++ {
		if run.Err() != nil {
			r.State = "cancelled_or_timed_out"
			break
		}
		if (n+1)*units > 2000000 {
			r.State = "work_budget_exhausted"
			break
		}
		if p.ExpiresAt > 0 && time.Now().Unix() >= p.ExpiresAt {
			r.State = "expired"
			break
		}
		binary.BigEndian.PutUint32(password[len(nonce):], uint32(n))
		var key []byte
		var err error
		if strings.HasPrefix(p.Algorithm, "PBKDF2/") {
			key, err = verificationPBKDF2(run, newHash, password, salt, p.Cost, p.KeyLength)
		} else {
			h := newHash()
			_, _ = h.Write(salt)
			_, _ = h.Write(password)
			key = h.Sum(nil)
			for i := 1; i < p.Cost; i++ {
				if i%128 == 0 && run.Err() != nil {
					err = run.Err()
					break
				}
				h.Reset()
				_, _ = h.Write(key)
				key = h.Sum(key[:0])
			}
			key = key[:p.KeyLength]
		}
		r.Attempts++
		if err != nil || run.Err() != nil {
			r.State = "cancelled_or_timed_out"
			break
		}
		derived := hex.EncodeToString(key)
		if strings.HasPrefix(derived, strings.ToLower(p.KeyPrefix)) {
			solution := struct {
				Counter    int64  `json:"counter"`
				DerivedKey string `json:"derivedKey"`
				Time       int64  `json:"time,omitempty"`
			}{n, derived, time.Since(start).Milliseconds()}
			// Keep the public signed challenge verbatim, including data key order,
			// optional zero/null fields and escaping. Only serialize our solution.
			solved, _ := json.Marshal(solution)
			b := append([]byte(`{"challenge":`), raw...)
			b = append(b, []byte(`,"solution":`)...)
			b = append(b, solved...)
			b = append(b, '}')
			r.Payload = base64.StdEncoding.EncodeToString(b)
			r.Number = &n
			r.State = "proof_computed"
			break
		}
	}
	r.ElapsedMS = time.Since(start).Milliseconds()
	return r
}

type verificationAnubisChallenge struct {
	Rules struct {
		Algorithm  string `json:"algorithm"`
		Difficulty int    `json:"difficulty"`
	} `json:"rules"`
	Challenge struct {
		Spent      bool   `json:"spent"`
		ID         string `json:"id"`
		Method     string `json:"method"`
		RandomData string `json:"randomData"`
		Difficulty int    `json:"difficulty"`
	} `json:"challenge"`
}

func validateVerificationAnubis(c *verificationAnubisChallenge) error {
	if c.Challenge.Method == "" {
		c.Challenge.Method = c.Rules.Algorithm
	}
	method := c.Challenge.Method
	if (method != "fast" && method != "slow") || (c.Rules.Algorithm != "" && c.Rules.Algorithm != method) {
		return fmt.Errorf("only explicit legacy Anubis fast/slow SHA-256 methods are supported")
	}
	if c.Challenge.Difficulty == 0 {
		c.Challenge.Difficulty = c.Rules.Difficulty
	}
	d := c.Challenge.Difficulty
	if c.Challenge.Spent || d < 1 || d > 6 || (c.Rules.Difficulty != 0 && c.Rules.Difficulty != d) || len(c.Challenge.ID) == 0 || len(c.Challenge.ID) > 256 || len(c.Challenge.RandomData) == 0 || len(c.Challenge.RandomData) > 4096 {
		return fmt.Errorf("invalid or over-budget Anubis challenge")
	}
	return nil
}
func solveVerificationAnubis(ctx context.Context, c verificationAnubisChallenge, attempts int64, timeout time.Duration) verificationPoWResult {
	r := verificationPoWResult{State: "budget_exhausted", Protocol: "anubis_sha256", Note: "Legacy SHA-256 proof only; no browser telemetry or token acceptance is claimed."}
	start := time.Now()
	run, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	prefix := strings.Repeat("0", c.Challenge.Difficulty)
	for n := int64(0); n < attempts; n++ {
		if run.Err() != nil {
			r.State = "cancelled_or_timed_out"
			break
		}
		sum := sha256.Sum256([]byte(c.Challenge.RandomData + strconv.FormatInt(n, 10)))
		digest := hex.EncodeToString(sum[:])
		r.Attempts++
		if strings.HasPrefix(digest, prefix) {
			r.Number = &n
			r.State = "proof_computed"
			r.ResponseHash = digest
			r.ChallengeID = c.Challenge.ID
			break
		}
	}
	r.ElapsedMS = time.Since(start).Milliseconds()
	return r
}

func (t *ToolSet) verificationSolveTool() actool.CoreTool {
	return readTool("human_verification_solve", "版本化免费 Go PoW：altcha_v1、altcha_v2(SHA-2/PBKDF2，非 Scrypt/Argon2)、anubis_sha256(旧版 fast/slow)。与其他本 ToolSet 入口共享64条有界挑战缓存，拒绝超限KDF后才执行；无网络、模型、媒体解码或自动下载。proof_computed不等于业务通过。",
		obj(map[string]any{"protocol": map[string]any{"type": "string", "enum": []string{"altcha_v1", "altcha_v2", "anubis_sha256"}}, "challenge": map[string]any{"type": "object", "description": "原始公开挑战JSON，不传服务端密钥；最多16KiB"}, "max_attempts": intp("默认100000，1..1000000"), "timeout_ms": intp("默认1000，1..5000")}, "protocol", "challenge"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if len(in) > 32768 {
				return actool.Errorf("solver input exceeds 32 KiB"), nil
			}
			var a struct {
				Protocol  string          `json:"protocol"`
				Challenge json.RawMessage `json:"challenge"`
				Attempts  int64           `json:"max_attempts"`
				Timeout   int             `json:"timeout_ms"`
			}
			if json.Unmarshal(in, &a) != nil {
				return actool.Errorf("invalid solver input"), nil
			}
			if a.Attempts == 0 {
				a.Attempts = 100000
			}
			if a.Timeout == 0 {
				a.Timeout = 1000
			}
			r, err := t.solveVerification(ctx, a.Protocol, a.Challenge, a.Attempts, time.Duration(a.Timeout)*time.Millisecond)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			b, _ := json.Marshal(r)
			return actool.Text(string(b)), nil
		})
}

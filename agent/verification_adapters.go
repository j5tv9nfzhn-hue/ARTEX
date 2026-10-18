package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// Extract only the public JSON challenge. Do not evaluate scripts or treat DOM
// contents as instructions. The caller already bounds the retained response.
func extractVerificationAnubis(body string) (json.RawMessage, error) {
	if len(body) > verificationFlowCapture {
		return nil, fmt.Errorf("challenge page exceeds bounds")
	}
	tokenizer := html.NewTokenizer(strings.NewReader(body))
	found := false
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			if tokenizer.Err() == io.EOF {
				break
			}
			return nil, fmt.Errorf("invalid challenge HTML")
		}
		if kind == html.StartTagToken {
			token := tokenizer.Token()
			found = false
			if token.Data == "script" {
				for _, a := range token.Attr {
					if a.Key == "id" && a.Val == "anubis_challenge" {
						found = true
					}
				}
			}
		} else if found && kind == html.TextToken {
			raw := append(json.RawMessage(nil), tokenizer.Text()...)
			if _, err := parseVerificationAnubis(raw); err != nil {
				return nil, fmt.Errorf("unsupported Anubis challenge JSON")
			}
			return raw, nil
		}
	}
	return nil, fmt.Errorf("no supported inline Anubis challenge found")
}

// v1.22 has both a full challenge object in HTML and a make-challenge API
// envelope whose challenge is a string and whose ID is at the top level.
func parseVerificationAnubis(raw json.RawMessage) (verificationAnubisChallenge, error) {
	var c verificationAnubisChallenge
	var envelope struct {
		Rules     json.RawMessage `json:"rules"`
		Challenge json.RawMessage `json:"challenge"`
		ID        string          `json:"id"`
	}
	if err := decodeVerificationJSON(raw, &envelope); err != nil {
		return c, err
	}
	if len(envelope.Rules) > 0 && json.Unmarshal(envelope.Rules, &c.Rules) != nil {
		return c, fmt.Errorf("invalid Anubis rules")
	}
	if len(envelope.Challenge) == 0 {
		return c, fmt.Errorf("missing Anubis challenge")
	}
	if envelope.Challenge[0] == '"' {
		if json.Unmarshal(envelope.Challenge, &c.Challenge.RandomData) != nil {
			return c, fmt.Errorf("invalid Anubis random data")
		}
		c.Challenge.ID = envelope.ID
	} else if json.Unmarshal(envelope.Challenge, &c.Challenge) != nil {
		return c, fmt.Errorf("invalid Anubis challenge object")
	}
	if envelope.ID != "" && envelope.ID != c.Challenge.ID {
		return c, fmt.Errorf("conflicting Anubis challenge IDs")
	}
	if err := validateVerificationAnubis(&c); err != nil {
		return c, err
	}
	return c, nil
}

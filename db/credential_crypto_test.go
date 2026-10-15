package db

import (
	"errors"
	"strings"
	"testing"
)

// credential tests pin the at-rest encryption contract (计划 #49): plaintext
// never lands in the database once a master key is installed, legacy plaintext
// keeps working, and a ciphertext row is unreadable — loudly — without the key.

func TestCredentialRoundtrip(t *testing.T) {
	SetCredentialKey([]byte("unit-test-master-key"))
	defer SetCredentialKey(nil)

	plain := "sk-live-abcdef123456"
	enc, err := encryptCredential(plain)
	if err != nil {
		t.Fatal(err)
	}
	if enc == plain {
		t.Fatal("encryptCredential returned the plaintext verbatim")
	}
	if !credentialEncrypted(enc) {
		t.Fatalf("ciphertext missing %s prefix: %q", credentialPrefix, enc)
	}
	dec, err := decryptCredential(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != plain {
		t.Fatalf("roundtrip = %q, want %q", dec, plain)
	}

	// GCM with a fresh nonce per call: the same secret must not produce the
	// same ciphertext twice (nonce reuse would leak the keystream).
	enc2, err := encryptCredential(plain)
	if err != nil {
		t.Fatal(err)
	}
	if enc2 == enc {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext")
	}

	// Tamper: flipping the body must fail authentication, never yield garbage.
	raw := strings.TrimPrefix(enc, credentialPrefix)
	tampered := credentialPrefix + "A" + raw[1:]
	if _, err := decryptCredential(tampered); err == nil {
		t.Fatal("tampered ciphertext decrypted without error")
	}
}

func TestCredentialWrongKeyFails(t *testing.T) {
	SetCredentialKey([]byte("key-number-one"))
	enc, err := encryptCredential("sk-secret")
	if err != nil {
		t.Fatal(err)
	}
	SetCredentialKey([]byte("key-number-two"))
	if _, err := decryptCredential(enc); err == nil {
		t.Fatal("decrypt with the wrong master key must fail, not return data")
	}
	// Missing key is its own explicit error, not a silent empty string.
	SetCredentialKey(nil)
	if _, err := decryptCredential(enc); !errors.Is(err, ErrCredentialKeyMissing) {
		t.Fatalf("missing-key decrypt error = %v, want %v", err, ErrCredentialKeyMissing)
	}
}

func TestCredentialNoKeyPassthrough(t *testing.T) {
	SetCredentialKey(nil)

	// No master key = legacy plaintext mode: values store and read verbatim.
	enc, err := encryptCredential("plain-key")
	if err != nil || enc != "plain-key" {
		t.Fatalf("no-key encrypt = %q, %v; want passthrough", enc, err)
	}
	dec, err := decryptCredential("plain-key")
	if err != nil || dec != "plain-key" {
		t.Fatalf("no-key decrypt = %q, %v; want passthrough", dec, err)
	}
	if credentialEncrypted("plain-key") {
		t.Fatal("plaintext flagged as ciphertext")
	}
	// Empty values stay empty on both sides.
	if enc, _ := encryptCredential(""); enc != "" {
		t.Fatalf("empty encrypt = %q", enc)
	}
	if dec, _ := decryptCredential(""); dec != "" {
		t.Fatalf("empty decrypt = %q", dec)
	}
}

func TestSettingsSecretKeysEncryptedAtRest(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("database unavailable (%v) — skipping", err)
	}
	defer d.Close()
	SetCredentialKey([]byte("settings-test-master-key"))
	defer SetCredentialKey(nil)

	if err := d.SetSetting("brave_search_api_key", "bsk-secret-value"); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM settings WHERE key='brave_search_api_key'`)

	var raw string
	if err := d.QueryRow(`SELECT value FROM settings WHERE key=$1`, "brave_search_api_key").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "bsk-secret-value" || !credentialEncrypted(raw) {
		t.Fatalf("brave key stored as %q, want encrypted with %s prefix", raw, credentialPrefix)
	}
	got, ok, err := d.GetSetting("brave_search_api_key")
	if err != nil || !ok || got != "bsk-secret-value" {
		t.Fatalf("GetSetting = %q ok=%v err=%v, want plaintext roundtrip", got, ok, err)
	}

	// Non-secret settings are stored verbatim (no accidental blanket encryption).
	if err := d.SetSetting("llm_pool_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM settings WHERE key='llm_pool_enabled'`)
	if err := d.QueryRow(`SELECT value FROM settings WHERE key=$1`, "llm_pool_enabled").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "true" {
		t.Fatalf("non-secret setting stored as %q, want %q", raw, "true")
	}
}

func TestProfileKeyEncryptedAtRest(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("database unavailable (%v) — skipping", err)
	}
	defer d.Close()
	SetCredentialKey([]byte("profile-test-master-key"))
	defer SetCredentialKey(nil)

	id, err := d.SaveProfile(&LLMProfile{Name: "t-cred-rest", Format: "openai", Model: "m", APIKey: "sk-rest-secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id)

	var raw string
	if err := d.QueryRow(`SELECT api_key FROM llm_profiles WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "sk-rest-secret" || !credentialEncrypted(raw) {
		t.Fatalf("api_key stored as %q, want encrypted with %s prefix", raw, credentialPrefix)
	}

	// Read funnel decrypts; the STORED hint is derived from the plaintext tail
	// (…cret), never from ciphertext — see SaveProfile. (Single-row loads select
	// profileColsKey and do not carry the hint column; ListProfiles does.)
	p, err := d.ProfileByID(id)
	if err != nil || p == nil {
		t.Fatalf("ProfileByID: %+v err=%v", p, err)
	}
	if p.APIKey != "sk-rest-secret" {
		t.Fatalf("ProfileByID key = %q, want plaintext roundtrip", p.APIKey)
	}
	var storedHint string
	if err := d.QueryRow(`SELECT COALESCE(api_key_hint,'') FROM llm_profiles WHERE id=$1`, id).Scan(&storedHint); err != nil {
		t.Fatal(err)
	}
	if storedHint != "…cret" {
		t.Fatalf("stored hint = %q, want …cret (from plaintext, not ciphertext)", storedHint)
	}

	// Blank-key update keeps the stored credential (and keeps it decryptable).
	p.APIKey = ""
	p.Name = "t-cred-rest-renamed"
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	again, err := d.ProfileByID(id)
	if err != nil || again.APIKey != "sk-rest-secret" {
		t.Fatalf("after blank update key = %q err=%v, want kept", again.APIKey, err)
	}

	// Legacy plaintext row (written before the key existed) reads back fine —
	// the lazy-upgrade window promised in credential_crypto.go.
	if _, err := d.Exec(`UPDATE llm_profiles SET api_key='sk-legacy-plain' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	legacy, err := d.ProfileByID(id)
	if err != nil || legacy.APIKey != "sk-legacy-plain" {
		t.Fatalf("legacy plaintext read = %q err=%v, want passthrough", legacy.APIKey, err)
	}
}

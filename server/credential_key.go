package server

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// credentialKeyFilename is the master key for at-rest credential encryption
// (db/credential_crypto.go: LLM profile API keys, web-search keys in settings).
// It lives in keyDir (project base dir, next to the executable) for the same
// reason jwt.key does: dataDir is the browsable workspace root, and a key that
// can be listed/downloaded through the file manager protects nothing. It is
// also deliberately NOT next to the database file it protects — copying one
// file (artex.db) must never be enough to recover plaintext credentials.
const credentialKeyFilename = "credential.key"

// InstallCredentialKey loads (or creates on first run) the credential master
// key and hands it to the db layer. It must run BEFORE the first store open /
// profile read: rows encrypted under this key are unreadable without it. The db
// package owns the cipher but knows nothing about where the key file lives, so
// the composition root installs it from here.
func InstallCredentialKey(keyDir string) error {
	key, err := loadOrCreateCredentialKey(keyDir)
	if err != nil {
		return err
	}
	db.SetCredentialKey(key)
	return nil
}

// loadOrCreateCredentialKey reads the master key from keyDir/credential.key. On
// first run a random token is generated and persisted with 0600. The file may
// hold any token of >=32 characters — SetCredentialKey normalizes it to the 32
// bytes AES-256 needs (via SHA-256, so no entropy is discarded). Same contract
// as loadOrCreateJWTKey.
func loadOrCreateCredentialKey(keyDir string) ([]byte, error) {
	path := filepath.Join(keyDir, credentialKeyFilename)
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	key, err := randomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate credential key: %w", err)
	}
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		return nil, fmt.Errorf("write credential key: %w", err)
	}
	log.Printf(logT("[config] 已将凭据主密钥写入 %s（LLM/搜索 API Key 将加密存储）"), path)
	return []byte(key), nil
}

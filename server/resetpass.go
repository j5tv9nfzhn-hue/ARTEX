package server

import (
	"errors"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"golang.org/x/crypto/bcrypt"
)

// ErrPasswordTooShort is returned by ResetAdminPassword for a password under the
// same length rule the /setup screen and POST /api/auth/init enforce.
var ErrPasswordTooShort = errors.New("密码不足最小长度")

// ResetAdminPassword stores a new bcrypt hash for the fixed ARTEX admin account.
//
// It exists for the offline reset path (artex -reset-password), which has no HTTP
// server to reuse. The invariants — the settings key, bcrypt cost, and the minimum
// length — deliberately live here rather than being duplicated by the caller: this
// is the same trio /setup and ChangePassword use, so a reset can never produce a
// hash the login path would reject.
//
// Every existing session token becomes invalid without a restart. The HS256 key is
// HMAC-SHA256(jwt.key, stored hash) (see signingKey), so changing the hash changes
// the key tokens were signed with.
func ResetAdminPassword(pg *db.DB, password string) error {
	if pg == nil {
		return errors.New("ResetAdminPassword: nil DB")
	}
	if utf8.RuneCountInString(password) < minPasswordLen {
		return ErrPasswordTooShort
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return pg.SetSetting(authPassKey, string(hash))
}

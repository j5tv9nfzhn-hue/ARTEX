package db

import (
	"errors"

	sqlite "modernc.org/sqlite"
)

// SQLite extended result codes. These are fixed by the SQLite ABI (and asserted
// by modernc.org/sqlite's own generated constants), so naming them here is a
// documentation aid rather than a guess.
//
// PostgreSQL exposed these as SQLSTATE strings on *pgconn.PgError; SQLite reports
// them as extended result codes on *sqlite.Error. The call sites below used to
// switch on "23505" / "23503" and now switch on these.
const (
	sqliteConstraintUnique     = 2067 // SQLITE_CONSTRAINT_UNIQUE
	sqliteConstraintPrimaryKey = 1555 // SQLITE_CONSTRAINT_PRIMARYKEY
	sqliteConstraintForeignKey = 787  // SQLITE_CONSTRAINT_FOREIGNKEY
)

// sqliteErrorCode returns the extended result code of err, or 0 when err is not a
// SQLite error.
//
// The primary codes are also matched, because a constraint failure can surface as
// the non-extended SQLITE_CONSTRAINT (19) depending on how the statement failed —
// e.g. a UNIQUE violation raised inside a trigger, which is exactly the shape
// finding_retests uses to force a retry.
func sqliteErrorCode(err error) int {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return 0
	}
	return sqliteErr.Code()
}

// isUniqueViolation reports whether err is a UNIQUE or PRIMARY KEY constraint
// failure — the SQLite equivalents of PostgreSQL's SQLSTATE 23505.
func isUniqueViolation(err error) bool {
	switch sqliteErrorCode(err) {
	case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
		return true
	}
	return false
}

// isForeignKeyViolation reports whether err is a FOREIGN KEY constraint failure —
// the SQLite equivalent of PostgreSQL's SQLSTATE 23503. foreign_keys(ON) is part
// of the DSN contract (see config.SQLiteDSN), so this can actually fire.
func isForeignKeyViolation(err error) bool {
	return sqliteErrorCode(err) == sqliteConstraintForeignKey
}

// ForeignKeyViolation / UniqueViolation expose the constraint classifiers to
// other packages (server/engine.go classifies dropped activity writes with them).
//
// They are thin wrappers rather than direct calls so callers outside this package
// do not have to know which driver's error type carries the code.
func ForeignKeyViolation(err error) bool { return isForeignKeyViolation(err) }

// UniqueViolation reports a UNIQUE / PRIMARY KEY constraint failure.
func UniqueViolation(err error) bool { return isUniqueViolation(err) }

// ConstraintCode names the constraint failure for logs: "fk_violation(787)",
// "unique_violation(2067)", or "" when err carries no constraint code. The numeric
// codes are SQLite's extended result codes; the previous PostgreSQL form reported
// SQLSTATE strings ("23503" / "23505").
func ConstraintCode(err error) string {
	switch sqliteErrorCode(err) {
	case sqliteConstraintForeignKey:
		return "fk_violation(787)"
	case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
		return "unique_violation(2067)"
	}
	return ""
}

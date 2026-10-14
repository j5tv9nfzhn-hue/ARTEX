package db

import (
	"database/sql/driver"
	"regexp"
	"sync"
	"time"

	"modernc.org/sqlite"
)

// SQLite compatibility layer for the translated schema and queries.
//
// PostgreSQL features that have no direct SQLite equivalent are provided here
// rather than by rewriting every call site. Each entry documents what it
// stands in for and why the substitution is sound.

// regexpFunc backs the `regexp(value, pattern)` calls that the schema
// translator emits where the source used PostgreSQL's `~` operator, e.g.
// CHECK (key ~ '^[a-z][a-z0-9_]*$') on agents.key.
//
// Go's regexp is RE2, which is what PostgreSQL's POSIX `~` compiles to
// anyway, so the semantics line up rather than merely approximating.
//
// Registered in init() because modernc.org/sqlite resolves functions when a
// statement is prepared, so registration has to precede the first sql.Open.
var regexpCache sync.Map // pattern string -> *regexp.Regexp

// nowFunc backs PostgreSQL's now().
//
// SQLite has datetime('now') but not now(), and the two are not
// interchangeable by name: queries and seed data call now() directly, so
// without this every seeded row fails with "no such function: now".
//
// The value uses the driver's own write format (_time_format=sqlite), i.e.
// "2006-01-02 15:04:05.999999999-07:00" rendered as
// "2026-10-14 18:44:28.123+00:00". That matters twice: it is what the driver
// parses back into time.Time under _texttotime, and it is what makes a
// now()-written row byte-identical to a column-default row. A hand-rolled
// second format would sort differently inside one timestamp column.
//
// One difference from PostgreSQL is accepted deliberately: PostgreSQL's now()
// is frozen at transaction start, so all rows in a transaction share a
// timestamp. Here it is evaluated per call, which makes ordering within a
// single transaction slightly finer-grained. Nothing depends on rows inside
// one transaction being exactly equal, and freezing it would require
// transaction-awareness the driver does not expose.
func nowFunc() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05.000-07:00")
}

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("regexp", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			value, pattern := argString(args[0]), argString(args[1])
			if pattern == "" {
				return int64(0), nil
			}
			re, err := compileRegexp(pattern)
			if err != nil {
				// SQLite treats a bad pattern as "does not match" rather than
				// aborting the statement; a CHECK constraint should reject the
				// row, not take down the caller.
				return int64(0), nil
			}
			if re.MatchString(value) {
				return int64(1), nil
			}
			return int64(0), nil
		})

	sqlite.MustRegisterDeterministicScalarFunction("now", 0,
		func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
			return nowFunc(), nil
		})

	// CURRENT_TIMESTAMP is a SQLite keyword and cannot be overridden, so the
	// same value is also offered under a non-colliding name for callers that
	// want to be explicit about the format.
	sqlite.MustRegisterDeterministicScalarFunction("artex_now", 0,
		func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
			return nowFunc(), nil
		})
}

func compileRegexp(pattern string) (*regexp.Regexp, error) {
	if v, ok := regexpCache.Load(pattern); ok {
		return v.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexpCache.Store(pattern, re)
	return re, nil
}

func argString(v driver.Value) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}

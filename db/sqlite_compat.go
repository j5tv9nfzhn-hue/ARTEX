package db

import (
	"database/sql/driver"
	"fmt"
	"regexp"
	"strconv"
	"strings"
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

	sqlite.MustRegisterDeterministicScalarFunction("concat_ws", -1, concatWSFunc)
	// GREATEST/LEAST 是 PG 内建：NULL 不参与比较，全部为 NULL 才返回 NULL。
	// SQLite 的 MAX/MIN 多参版只要有一个 NULL 就返回 NULL，语义不同，故自实现。
	sqlite.MustRegisterDeterministicScalarFunction("GREATEST", -1, extremaFunc(true))
	sqlite.MustRegisterDeterministicScalarFunction("LEAST", -1, extremaFunc(false))
	sqlite.MustRegisterDeterministicScalarFunction("left", 2, leftFunc)

	// sqlUTCDate 回应 PG 侧同名 SQL 函数（查询文本里直接调用：TokenDailyAll 的
	// sqlUTCDate("created_at")、UsageDaily 的 sqlUTCDate("ts")）。时间列在 SQLite
	// 侧存的是 _time_format 文本「YYYY-MM-DD ...+00:00」，前 10 字节就是 UTC
	// 当日日期，与 PG 函数 to_char(at time zone 'UTC') 的语义逐字节一致。
	sqlite.MustRegisterDeterministicScalarFunction("sqlUTCDate", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			s := argString(args[0])
			if len(s) < 10 {
				return s, nil
			}
			return s[:10], nil
		})
}

// extremaFunc 实现 PG 的 GREATEST/LEAST：按数值比较，返回原参数值。
// 参与比较的非数值类型无法排序时保守返回 NULL（本项目调用点只喂整型/浮点）。
func extremaFunc(greatest bool) func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
	return func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		var best driver.Value
		var bestF float64
		seen := false
		for _, a := range args {
			if a == nil {
				continue
			}
			var f float64
			switch v := a.(type) {
			case int64:
				f = float64(v)
			case float64:
				f = v
			default:
				return nil, nil
			}
			if !seen || (greatest && f > bestF) || (!greatest && f < bestF) {
				best, bestF, seen = a, f, true
			}
		}
		if !seen {
			return nil, nil
		}
		return best, nil
	}
}

// concatWSFunc backs PostgreSQL's concat_ws(sep, VARIADIC "any").
//
// 语义照抄 PG：NULL 参数被跳过，其余按原样用分隔符连接；全部为 NULL 时返回空串。
// 空串**不**跳过——PG 也不跳过，跳过会让 `concat_ws(' · ', a, ”, b)` 的列数
// 与调用点预期不一致。
//
// 为什么注册 UDF 而不是改写调用点：搜索类的拼接谓词（chat_mentions 的
// "名字/域名/路径任一命中"）在 SQL 里到处都是 concat_ws，逐个改写成 SQLite 的
// 拼接表达式既啰嗦又容易在 NULL 处理上和 PG 产生分叉。注册同名的标量函数让
// 语句在两个引擎上逐字相同——这与 regexp / now 是同一条论证。
func concatWSFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) == 0 {
		return "", nil
	}
	sep := argString(args[0])
	var b strings.Builder
	for _, a := range args[1:] {
		if a == nil {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		switch t := a.(type) {
		case string:
			b.WriteString(t)
		case []byte:
			b.Write(t)
		default:
			b.WriteString(fmt.Sprint(t))
		}
	}
	return b.String(), nil
}

// leftFunc backs PostgreSQL's left(string, n).
//
// SQLite 的 substr(s,1,n) 与之同义；注册 left 只是为了让调用点的 SQL 在两个
// 引擎上逐字相同，而不是到处写 substr(...)。
func leftFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 || args[0] == nil {
		return nil, nil
	}
	s := argString(args[0])
	n := int(toInt64(args[1]))
	if n <= 0 {
		return "", nil
	}
	if len(s) <= n {
		return s, nil
	}
	return s[:n], nil
}

func toInt64(v driver.Value) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
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

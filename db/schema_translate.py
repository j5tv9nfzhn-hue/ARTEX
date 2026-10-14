"""Translate db/schema.sql (PostgreSQL) into db/schema_sqlite.sql.

Design notes
------------
The translation is intentionally mechanical and lossy in exactly three places,
each of which is called out in the generated header:

  1. plpgsql trigger functions are dropped. SQLite has no procedural language,
     so `LANGUAGE plpgsql` bodies cannot run.
  2. `BEFORE UPDATE ... SET NEW.updated_at = now()` has no SQLite equivalent --
     a SQLite trigger cannot assign to NEW. Rewritten as an AFTER UPDATE that
     writes the column back on the same row, which does not recurse because
     recursive_triggers defaults to OFF.
  3. `inet`/`cidr` become TEXT. The source schema wrapped casts in a plpgsql
     helper that returned NULL for unparseable input; in SQLite there is no
     cast, so the column is plain text and the Go side must not cast.

Everything else is type-level and mechanical:

    BIGSERIAL/SERIAL PRIMARY KEY -> INTEGER PRIMARY KEY AUTOINCREMENT
    JSONB / TIMESTAMPTZ / inet / cidr -> TEXT
    DOUBLE PRECISION -> REAL
    now() -> strftime(...) with a sortable UTC ISO-8601 shape

Run:  python schema_translate.py <schema.sql> <out.sql>
"""

import pathlib
import re
import sys

# --- column types -----------------------------------------------------------

TYPE_RULES = [
    (r"\bBIGSERIAL\s+PRIMARY\s+KEY\b", "INTEGER PRIMARY KEY AUTOINCREMENT"),
    (r"\bSERIAL\s+PRIMARY\s+KEY\b", "INTEGER PRIMARY KEY AUTOINCREMENT"),
    (r"\bBIGSERIAL\b", "INTEGER"),
    (r"\bSERIAL\b", "INTEGER"),
    (r"\bJSONB\b", "TEXT"),
    # TIMESTAMPTZ becomes TIMESTAMP, not TEXT. The driver only hands a column
    # back as time.Time when it is declared with a DATETIME-family type
    # (_texttotime), and every scan in this codebase targets *time.Time.
    (r"\bTIMESTAMPTZ\b", "TIMESTAMP"),
    (r"\bDOUBLE\s+PRECISION\b", "REAL"),
    (r"\binet\b", "TEXT"),
    (r"\bcidr\b", "TEXT"),
    # Timestamps the source declared as bare TEXT but that the code scans as
    # time.Time. The column name is the only signal available at DDL level.
    (r"(?im)^(\s+)([a-z_]*(?:_at|_ts|_time|_since|_until))\s+TEXT\b", r"\1\2 TIMESTAMP"),
    (r"(?im)^(\s+)([a-z_]*(?:_at|_ts|_time|_since|_until))\s+TEXT\s+NOT NULL\b", r"\1\2 TIMESTAMP NOT NULL"),
]

# now() -> the driver's canonical on-disk timestamp shape.
#
# modernc.org/sqlite only converts TEXT to time.Time on read (_texttotime) when
# the column is declared DATE/DATETIME/TIME/TIMESTAMP, and its parse layouts are
# "2006-01-02 15:04:05.999999999-07:00" and friends -- space separator, explicit
# numeric offset. Using the driver's own shape for defaults is what keeps a
# column-default write byte-identical to a write that goes through the driver's
# time formatter; a second, hand-rolled format would silently break ordering
# between the two sources.
NOW = "(strftime('%Y-%m-%d %H:%M:%f+00:00','now'))"

# --- statements SQLite cannot express at all -------------------------------
#
# Each entry is (label, compiled pattern). Matched spans are deleted and
# counted so the header can report exactly what was lost. Nothing is silently
# dropped: every removal is attributable.

DROP_PATTERNS = [
    # pg_advisory-based catalog probes: they ask pg_constraint / pg_class what
    # a constraint is, purely to decide whether a later ADD CONSTRAINT is
    # needed. SQLite has no such catalog and no post-hoc ADD CONSTRAINT, so the
    # probe and the guarded ADD go together.
    ("pg_catalog 探测", re.compile(
        r"DO\s+\$\$.*?\$\$;\s*", re.I | re.S)),
    ("ADD/DROP CONSTRAINT", re.compile(
        r"ALTER\s+TABLE\s+[\w.\"]+\s+"
        r"(?:ADD\s+CONSTRAINT|DROP\s+CONSTRAINT).*?;\s*", re.I | re.S)),
    ("ALTER COLUMN", re.compile(
        r"ALTER\s+TABLE\s+[\w.\"]+\s+ALTER\s+COLUMN\s+[\w.\"]+\s+"
        r"SET\s+(?:DEFAULT|NOT\s+NULL|DROP\s+NOT\s+NULL).*?;\s*", re.I | re.S)),
    # Index methods with no SQLite analogue. These index array columns
    # (GIN) or inet ranges (GIST); SQLite indexes neither, so the index is
    # meaningless rather than mistranslatable.
    ("GIN/GIST 索引", re.compile(
        r"CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?[\w]+\s+"
        r"ON\s+[\w.\"]+\s+USING\s+\w+(?:\s*\([^)]*\))?[^;]*;\s*", re.I | re.S)),
    # One-off data migrations that walk a PostgreSQL array column. SQLite has
    # no arrays and no unnest/LATERAL, so the rows these seed have to be
    # migrated in Go by decoding the JSON text instead.
    ("LATERAL/unnest 数组迁移", re.compile(
        r"\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+[\w.\"]+[^;]*?"
        r"\b(?:LATERAL|unnest)\s*\([^;]*?;\s*", re.I | re.S)),
]

# Rewrites (not deletions).
FIX_RULES = [
    # DROP TRIGGER takes no ON <table> in SQLite.
    ("DROP TRIGGER ... ON t", re.compile(
        r"DROP\s+TRIGGER\s+(IF\s+EXISTS\s+)?([\w]+)\s+ON\s+[\w.\"]+\s*;",
        re.I), lambda m: f"DROP TRIGGER IF EXISTS {m.group(2)};"),
    # SQLite has no ADD COLUMN IF NOT EXISTS. Idempotency is re-established at
    # apply time by recording applied migrations, not by the DDL itself.
    ("ADD COLUMN IF NOT EXISTS", re.compile(
        r"ALTER\s+TABLE\s+([\w.\"]+)\s+ADD\s+COLUMN\s+IF\s+NOT\s+EXISTS\s+",
        re.I), lambda m: f"ALTER TABLE {m.group(1)} ADD COLUMN "),
    # PostgreSQL cast syntax. SQLite is dynamically typed, so dropping the
    # cast preserves value semantics everywhere it appears in DDL/seed data.
    ("::type 转换", re.compile(
        r"::\s*(?:regclass|text|jsonb|json|int|bigint|boolean|timestamptz|inet|cidr)\b",
        re.I), lambda m: ""),
    # Covering-index INCLUDE (...) has no SQLite equivalent. SQLite indexes are
    # already covering when the columns are present, and dropping the clause
    # keeps the index's uniqueness/ordering semantics intact.
    ("INDEX ... INCLUDE", re.compile(
        r"\s+INCLUDE\s*\([^)]*\)", re.I), lambda m: " "),
    # PostgreSQL UPDATE <table> <alias>; SQLite requires the AS keyword.
    ("UPDATE <t> <alias>", re.compile(
        r"(?i)\b(UPDATE\s+[\w.\"]+)\s+(\w+)\s+(?=SET\b)"), lambda m: f"{m.group(1)} AS {m.group(2)} "),
    # PostgreSQL regex operators -> a registered regexp() function. Matched
    # only with a bare column on the left so that a '~' inside a string literal
    # is never rewritten. Case-insensitive forms carry RE2's inline (?i).
    ("正则 ~ 运算符", re.compile(
        r"\b(\w+)\s+(!?)~\*?\s+('(?:[^']|'')*')"),
     lambda m: (
         f"(NOT regexp({m.group(1)}, '(?i){m.group(3)[1:-1]}'))"
         if m.group(2) else
         (f"regexp({m.group(1)}, '{m.group(3)[1:-1]}')"
          if m.group(0).find("~*") >= 0
          else f"regexp({m.group(1)}, {m.group(3)})"))),
]


def table_columns(src):
    """Map table name -> declared column names, from CREATE TABLE bodies.

    Used to delete ALTER TABLE ... ADD COLUMN statements that are no-ops on a
    fresh database: the PostgreSQL schema carries an "old install" migration
    column for nearly every table, and those all duplicate a column the CREATE
    already declares.
    """
    out = {}
    pat = re.compile(
        r"CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w.\"]+)\s*\(", re.I)
    for m in pat.finditer(src):
        table = m.group(1)
        i = m.end() - 1
        depth, cols = 0, []
        while i < len(src):
            c = src[i]
            if c == "(":
                depth += 1
            elif c == ")":
                depth -= 1
                if depth == 0:
                    break
            elif depth == 1:
                mm = re.match(r"\s*\"?(\w+)\"?\s+\w", src[i:])
                if mm:
                    cols.append(mm.group(1))
            i += 1
        out[table] = cols
    return out


def strip_plpgsql(src):
    """Remove CREATE [OR REPLACE] FUNCTION ... $$ ... $$ LANGUAGE plpgsql blocks."""
    out = []
    i = 0
    n = len(src)
    pattern = re.compile(
        r"CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\b", re.I
    )
    while i < n:
        m = pattern.search(src, i)
        if not m:
            out.append(src[i:])
            break
        out.append(src[i:m.start()])
        # find the opening dollar-quote
        dq = src.find("$$", m.end())
        if dq < 0:
            # single-quoted body
            dq = src.find("'", m.end())
            close = src.find("'", dq + 1)
            end = src.find(";", close) + 1
        else:
            close = src.find("$$", dq + 2)
            if close < 0:
                end = n
            else:
                # skip "LANGUAGE plpgsql;" (and any trailing options) to the ';'
                semi = src.find(";", close + 2)
                end = semi + 1 if semi >= 0 else close + 2
        i = end
    return "".join(out)


def translate_trigger_body(name, event, table, raw):
    """Rewrite a set_updated_at-style BEFORE UPDATE trigger for SQLite."""
    if event.upper() != "UPDATE":
        return None
    if not re.search(r"\bset_updated_at\s*\(", raw, re.I):
        return None
    return (
        f"CREATE TRIGGER IF NOT EXISTS {name}_updated_at\n"
        f"AFTER UPDATE ON {table}\n"
        "BEGIN\n"
        f"  UPDATE {table} SET updated_at = {NOW} WHERE rowid = NEW.rowid;\n"
        "END;"
    )


def capture_functions(src):
    """Map function name -> body, for triggers that SQLite can express directly.

    A plpgsql body that is nothing but one DML statement translates without
    help: SQLite triggers may run UPDATE/INSERT/DELETE against other tables,
    OLD./NEW. exist with the same names, and now() is supplied as a registered
    function. What does not survive is control flow, RAISE, RETURN QUERY and
    multi-statement bodies, so the caller must check for those.
    """
    out = {}
    # Both spellings occur in this schema: `RETURNS trigger AS $$ ... $$
    # LANGUAGE plpgsql;` and `RETURNS trigger LANGUAGE plpgsql AS $$ ... $$;`.
    # Anchor on `AS $$` and do not require a trailing LANGUAGE clause, so
    # neither form is missed.
    pat = re.compile(
        r"CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+(\w+)\s*\(\)[^$]*?AS\s+\$\$(.*?)\$\$",
        re.I | re.S)
    for m in pat.finditer(src):
        out[m.group(1)] = m.group(2)
    return out


def sqlite_trigger_from_plpgsql(name, when, event, table, body):
    """Transcribe a single-statement plpgsql trigger body into SQLite syntax.

    Returns None when the body has anything SQLite cannot express, so the
    caller reports it instead of emitting something subtly different.
    """
    if re.search(r"\b(RAISE|RETURN\s+QUERY|LOOP|WHILE|FOREACH|EXCEPTION)\b", body, re.I):
        return None
    # Strip the plpgsql wrapper: leading BEGIN, trailing END, and every
    # RETURN OLD/NEW.  RETURN is not always last -- `BEGIN <stmt>; RETURN OLD;
    # END;` puts END after it -- so it has to be removed wherever it appears.
    inner = re.sub(r"^\s*BEGIN\b", "", body, flags=re.I)
    inner = re.sub(r"\bRETURN\s+(OLD|NEW)\s*;", "", inner, flags=re.I)
    inner = re.sub(r"\bEND\b\s*;?\s*$", "", inner, flags=re.I).strip()
    if not inner:
        return None
    # Exactly one statement: a single terminating semicolon at the very end.
    if ";" in inner[:-1] if inner.endswith(";") else ";" in inner:
        return None
    stmt = inner if inner.endswith(";") else inner + ";"
    if not re.match(r"(?is)^\s*(UPDATE|INSERT\s+INTO|DELETE\s+FROM)\b", stmt):
        return None
    return (f"CREATE TRIGGER IF NOT EXISTS {name}\n"
            f"{when} {event} ON {table}\n"
            "BEGIN\n"
            f"  {stmt}\n"
            "END;")


def main():
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    src = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
    functions = capture_functions(src)
    src = strip_plpgsql(src)

    # Deletions and rewrites happen on uncommented SQL.
    removed = []
    for label, pat in DROP_PATTERNS:
        src, n = pat.subn("", src)
        if n:
            removed.append((label, n))

    fixed = []
    for label, pat, fn in FIX_RULES:
        src, n = pat.subn(fn, src)
        if n:
            fixed.append((label, n))

    # Triggers. Deliberately matched loosely (whole statement up to its ';') so
    # that shapes a narrow pattern would miss -- e.g. `AFTER INSERT OR UPDATE OF
    # task_ids` -- are still captured and reported rather than silently emitted
    # as invalid SQLite. Anything not translatable is dropped loudly.
    trigger_re = re.compile(
        r"CREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+(?P<name>\w+)\s+"
        r"(?P<when>BEFORE|AFTER|INSTEAD\s+OF)\s+"
        r"(?P<events>[\w\s]+?)\s+ON\s+(?P<table>[\w.\"]+)\s+"
        r"FOR\s+EACH\s+ROW\s+EXECUTE\s+(?:FUNCTION|PROCEDURE)\s+"
        r"(?P<fn>\w+)\s*\([^)]*\)\s*;",
        re.I | re.S,
    )

    dropped = []
    rewritten = []

    def repl(m):
        d = m.groupdict()
        name, table, fn = d["name"], d["table"], d["fn"]
        events = " ".join(d["events"].split())
        body = m.group(0)
        # Only a plain single-event UPDATE against set_updated_at() has a
        # mechanical SQLite equivalent. A trigger whose plpgsql body is a
        # single DML statement also translates, because SQLite triggers may
        # write to other tables. Everything else (multi-event, column lists,
        # custom plpgsql bodies over array columns) has to move into Go.
        if fn == "set_updated_at" and events.upper() == "UPDATE":
            rewritten.append((name, table))
            return "\n" + translate_trigger_body(name, events, table, body) + "\n"
        direct = None
        if fn in functions:
            direct = sqlite_trigger_from_plpgsql(name, d["when"].upper(), events, table, functions[fn])
        if direct:
            rewritten.append((name, table))
            return "\n" + direct + "\n"
        dropped.append((name, events, table, fn))
        return "\n"

    src = trigger_re.sub(repl, src)

    # now() in DEFAULT clauses
    src = re.sub(r"(?i)default\s+now\(\)", f"DEFAULT {NOW}", src)

    # Column types. Comments AND string literals are lifted out first.
    # Comments matter because prose names types ("safe text->inet conversion").
    # Literals matter more: CHECK (kind IN ('ip','cidr',...)) contains 'cidr'
    # as data, and rewriting it to TEXT would silently corrupt the enum.
    comments = []
    strings = []

    def stash_comment(m):
        comments.append(m.group(0))
        return f"\x00C{len(comments) - 1}\x00"

    def stash_string(m):
        strings.append(m.group(0))
        return f"\x00S{len(strings) - 1}\x00"

    src = re.sub(r"--[^\n]*", stash_comment, src)
    src = re.sub(r"'(?:[^']|'')*'", stash_string, src)

    for pat, rep in TYPE_RULES:
        src = re.sub(pat, rep, src, flags=re.I)

    # Comments and string literals are still stashed here on purpose. A `--`
    # comment inside a CREATE TABLE body can contain a `;` (intercept_pending
    # has one), and the fold below matches table bodies with [^;]*? -- folding
    # while comments are real text truncates the match and silently skips the
    # table. Stashed placeholders contain no semicolons.

    # Fold every ALTER TABLE ... ADD COLUMN into its CREATE TABLE and delete the
    # ALTER. SQLite has no ADD COLUMN IF NOT EXISTS, so a separate migration
    # step is not re-runnable -- but a column folded into CREATE TABLE is
    # covered by IF NOT EXISTS and therefore idempotent for free.
    #
    # This is safe for SQLite specifically because there is no older on-disk
    # install to upgrade: the database is either created by this file or it
    # does not exist yet.
    add_col = re.compile(
        r"ALTER\s+TABLE\s+([\w.\"]+)\s+ADD\s+COLUMN\s+(\w+)([^;]*);\s*", re.I)

    pending, folded, skipped = {}, 0, 0
    keep = []
    pos = 0
    declared = table_columns(src)
    for m in add_col.finditer(src):
        keep.append(src[pos:m.start()])
        table, col, rest = m.group(1), m.group(2), m.group(3).strip()
        if col in declared.get(table, ()):
            # Already part of the CREATE TABLE: the PostgreSQL original used
            # ADD COLUMN IF NOT EXISTS purely to patch older installs.
            skipped += 1
        else:
            cols = pending.setdefault(table, {})
            if col not in cols:
                cols[col] = f"{col} {rest}".rstrip()
                folded += 1
        pos = m.end()
    keep.append(src[pos:])
    src = "".join(keep)

    for table, cols in pending.items():
        additions = ",\n    ".join(cols.values())
        pat = re.compile(
            r"(CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?" + re.escape(table) + r"\s*\()"
            r"([^;]*?)(\s*\)\s*;)", re.I | re.S)

        def repl(m):
            # SQLite requires every column definition to precede any
            # table-level constraint. Appending after a trailing
            # CHECK/UNIQUE/CONSTRAINT clause is a syntax error, so the folded
            # columns are spliced in front of the first such clause.
            lines = m.group(2).rstrip().splitlines()
            insert_at = len(lines)
            for i, line in enumerate(lines):
                if not re.match(r"\s*(CONSTRAINT|CHECK|UNIQUE|PRIMARY\s+KEY|FOREIGN\s+KEY)\b",
                                line, re.I):
                    continue
                # A column definition may carry its own CHECK, continued on the
                # next line (e.g. `status TEXT NOT NULL DEFAULT 'open'` followed
                # by `CHECK (status IN (...))`). Only a line that starts a new
                # definition -- i.e. the previous non-empty line ends with a
                # comma -- is a table-level constraint.
                prev = next((x for x in reversed(lines[:i]) if x.strip()), "")
                if prev.rstrip().endswith(","):
                    insert_at = i
                    break
            block = "    " + additions
            if insert_at == len(lines):
                head = "\n".join(lines).rstrip().rstrip(",")
                return m.group(1) + head + ",\n" + block + m.group(3)
            # More content (a table-level constraint) follows, so the last
            # folded column needs its own trailing comma.
            head = "\n".join(lines[:insert_at]).rstrip().rstrip(",")
            return (m.group(1) + head + ",\n" + block + ","
                    + "\n" + "\n".join(lines[insert_at:]) + m.group(3))

        src, n = pat.subn(repl, src, count=1)
        if n == 0:
            # Column added to a table the schema creates elsewhere (or in Go).
            # Leave it out and let the header record it.
            removed.append((f"{table} 的 ALTER 未找到建表语句", 1))
    if folded:
        removed.append(("ALTER ADD COLUMN 折叠进 CREATE TABLE", folded))
    if skipped:
        removed.append(("ADD COLUMN 新库已含（丢弃）", skipped))

    src = re.sub(r"\x00S(\d+)\x00", lambda m: strings[int(m.group(1))], src)
    src = re.sub(r"\x00C(\d+)\x00", lambda m: comments[int(m.group(1))], src)

    header = [
        "-- ARTEX SQLite schema -- 由 db/schema.sql 自动生成，请勿手改。",
        "-- 重新生成：python db/schema_translate.py db/schema.sql db/schema_sqlite.sql",
        "--",
        "-- 与 PostgreSQL 版的已知差异（有意为之）：",
        "--   1. 无 plpgsql，触发器函数体已丢弃；",
        "--      set_updated_at 形态的 BEFORE UPDATE 改写为 AFTER UPDATE 回写同一行",
        "--      （SQLite 触发器不能给 NEW 赋值；recursive_triggers 默认 OFF 故不递归）。",
        "--   2. inet/cidr 为纯 TEXT，Go 侧不得再做 ::inet 转换。",
        "--   3. 时间戳统一为 TEXT，形状 YYYY-MM-DDTHH:MM:SS.sssZ（UTC，字典序=时间序）。",
        "",
        f"-- 改写触发器 {len(rewritten)} 个，未能表达的 {len(dropped)} 个：",
    ]
    for name, table in rewritten:
        header.append(f"--   重写  {name} -> {table}_updated_at")
    for name, event, table, fn in dropped:
        header.append(f"--   丢弃  {name} ({event} on {table}, 调用 {fn}()) -- 需人工处理")
    header.append("")
    for label, n in removed:
        header.append(f"-- 删除 {n:>3} 条 {label}")
    for label, n in fixed:
        header.append(f"-- 改写 {n:>3} 条 {label}")
    header.append("")

    pathlib.Path(sys.argv[2]).write_text("\n".join(header) + src, encoding="utf-8", newline="\n")
    print(f"wrote {sys.argv[2]}")
    for label, n in removed:
        print(f"  dropped {n:>3}  {label}")
    for label, n in fixed:
        print(f"  rewrote {n:>3}  {label}")
    print(f"  rewritten triggers: {len(rewritten)}")
    print(f"  dropped triggers:   {len(dropped)}")
    for d in dropped:
        print(f"    - {d[0]} ({d[1]} on {d[2]} -> {d[3]}()) -- 需人工处理")
    return 0


if __name__ == "__main__":
    sys.exit(main())
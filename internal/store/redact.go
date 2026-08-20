package store

import (
	"regexp"
	"strings"
)

// Redaction of database credentials out of text bound for a terminal or a log.
//
// WHAT THIS ACTUALLY DEFENDS, corrected after measuring the pinned dependency
// rather than assuming it. An earlier version of this comment asserted that pgx
// quotes `postgres://user:pass@host/db` in its errors and had therefore "put a
// real database password on stdout". THAT IS FALSE for pgx v5.10.0, and the
// claim was never verified before being written down. Measured across
// ParseConfig, pgxpool.New, pool.Begin and Migrate, with URL, keyword and
// quoted-keyword DSNs: pgx MASKS the password as `xxxxxx` in parse errors and
// OMITS it entirely from connect errors, which read
// "failed to connect to `user=leakuser database=nodb`: ... connection refused".
// Password leak: none, on every probe.
//
// So this is PROSPECTIVE hardening, and saying so is the point. What it does buy
// today is real but narrower than a password: pgx parse errors DO quote the full
// URL including host and database ("cannot parse
// `postgres://leakuser:xxxxxx@127.0.0.1:notaport/nodb`"), and per CLAUDE.md a
// hostname can itself be employer-internal detail that must not enter a public
// repo. What it insures against is a future driver version, or any other
// library handed a DSN, formatting one less carefully.
//
// Redaction is a BACKSTOP, not the primary control. The primary control is that
// nothing in this package ever formats a DSN into a message on purpose; this
// catches DSNs that arrive inside somebody else's error string.
var (
	// URL form: postgres:// or postgresql:// through to the next whitespace,
	// backtick, or quote. Deliberately greedy about the tail — over-redacting a
	// database name is free, under-redacting a password is not.
	dsnURLRe = regexp.MustCompile(`(?i)postgres(?:ql)?://[^\s"'` + "`" + `]*`)
	// Keyword form: libpq accepts `host=... password=...`. The value may be
	// SINGLE- OR DOUBLE-QUOTED, which is the only way to express a password
	// containing a space — and an earlier version of this pattern excluded
	// quotes from the value class, so `password='hunter two'` matched NOTHING
	// and passed through intact. The quoted alternatives come first so they win
	// over the bare form.
	dsnKeywordRe = regexp.MustCompile(`(?i)\b(password|pgpassword)\s*=\s*('[^']*'|"[^"]*"|[^\s"'` + "`" + `]+)`)
)

// RedactSecrets removes anything DSN-shaped from s.
//
// The diagnosis is preserved on purpose: an error reduced to "[redacted]" is
// useless, and a useless error message is one somebody deletes the redaction to
// fix — trading the leak straight back.
func RedactSecrets(s string) string {
	if s == "" {
		return s
	}
	out := dsnURLRe.ReplaceAllString(s, "postgres://[redacted]")
	out = dsnKeywordRe.ReplaceAllStringFunc(out, func(m string) string {
		key := m
		if i := strings.IndexByte(m, '='); i >= 0 {
			key = m[:i]
		}
		return key + "=[redacted]"
	})
	return out
}

// RedactError renders err with secrets removed. Returns "" for nil so callers
// can format it unconditionally.
func RedactError(err error) string {
	if err == nil {
		return ""
	}
	return RedactSecrets(err.Error())
}

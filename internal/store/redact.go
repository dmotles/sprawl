package store

import (
	"regexp"
	"strings"
)

// Redaction of database credentials out of text bound for a terminal or a log.
//
// NOT PROSPECTIVE HARDENING — this exists because a test caught a live leak.
// pgx quotes the connection string in its errors ("failed to connect to
// `postgres://user:pass@host:5432/db`: connection refused"), so printing %v of a
// connection failure from `sprawl store doctor` put a real database password on
// stdout. This repo is public and terminal transcripts get pasted into issues.
//
// Redaction is a BACKSTOP, not the primary control. The primary control is that
// nothing in this package ever formats a DSN into a message on purpose; this
// catches the DSNs that arrive inside somebody else's error string.
var (
	// URL form: postgres:// or postgresql:// through to the next whitespace,
	// backtick, or quote. Deliberately greedy about the tail — over-redacting a
	// database name is free, under-redacting a password is not.
	dsnURLRe = regexp.MustCompile(`(?i)postgres(?:ql)?://[^\s"'` + "`" + `]*`)
	// Keyword form: libpq accepts `host=... password=...`, and pgx quotes that
	// too. Redacting only URLs would leave half the leak open.
	dsnKeywordRe = regexp.MustCompile(`(?i)\b(password|pgpassword)\s*=\s*[^\s"'` + "`" + `]+`)
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

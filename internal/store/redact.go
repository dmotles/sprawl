package store

import (
	"regexp"
	"strings"
)

// Redaction of database credentials and database identity out of text bound for
// a terminal or a log.
//
// WHAT THIS ACTUALLY DEFENDS, corrected twice after measuring the pinned
// dependency rather than assuming it.
//
// Round one killed the claim that pgx quotes a live password. It does not, for
// pgx v5.10.0: the password is masked as `xxxxxx` in parse errors and OMITTED
// entirely from connect errors. Password leak: none, on every probe.
//
// Round two (QUM-1281) killed the claim, implied by the shape of the original
// patterns, that the operator's configured DSN form decides what leaks. It does
// not. `pgconn.ConnectError.Error()` renders
//
//	failed to connect to `user=%s database=%s`:
//
// so pgx re-renders CONNECT errors in KEYWORD form no matter which form it was
// handed — a URL-form DSN and a keyword-form DSN produce byte-identical text.
// The URL form appears only in PARSE errors, which are the rarer shape. Before
// the widening below, that made this function a NO-OP on every pgx connect
// error, which is the shape a real outage produces.
//
// Note `host=` is NOT in a connect error. The hostname arrives in the untagged
// tail, in one of two measured grammars:
//
//	hostname resolving error: lookup db.internal.example on 127.0.0.53:53: no such host
//	10.20.30.40:5432 (db.internal.example): dial error: dial tcp ...: connection refused
//
// the first from `net.DNSError.Error()`, the second from pgconn's
// `perDialConnectError.Error()` (`"%s (%s): %s"`). Widening the keyword
// pattern alone would therefore have redacted the user and the database and
// left the hostname — the item CLAUDE.md most clearly forbids in a public repo
// — fully intact. Hence the two anchored tail patterns.
//
// Redaction is a BACKSTOP, not the primary control. The primary control is that
// nothing in this package ever formats a DSN into a message on purpose; this
// catches DSNs that arrive inside somebody else's error string.
//
// THE RULE, so the next widening does not have to reverse-engineer it from the
// tests: a value the OPERATOR SUPPLIED (a keyword value, a DSN component) is
// redacted; a value the FAILURE ITSELF NARRATED (the address actually dialled,
// the resolver actually asked) is preserved. That is why `hostaddr=10.1.2.3` is
// redacted while `127.0.0.53:53` and `10.20.30.40:5432` survive, even though
// all three are IP addresses.
//
// EXPLICIT NON-GOALS, with reasoning, because an unstated non-goal is read as
// protection that is not there:
//
//   - `port=`, and likewise `sslmode`, `connect_timeout`, `application_name`,
//     `options`. A port carries no credential and identifies nothing, while
//     "wrong port" is one of the misconfigurations `store doctor` exists to
//     diagnose. `port=[redacted]` deletes the answer, and an error message that
//     has been made useless is one somebody deletes the redaction to fix —
//     trading the leak straight back.
//   - IP addresses in the failure's own narration (see THE RULE above). An
//     RFC1918 address carries no naming scheme or employer branding, and any
//     pattern broad enough to catch it also eats `127.0.0.53:53` and
//     `127.0.0.1`, which are the diagnosis. This is a judgement, not a claim of
//     zero exposure: deployment topology is on CLAUDE.md's list.
//   - A BARE HOSTNAME in free prose. There is no way to tell a hostname from a
//     filename or a version string without a candidate list, and redacting
//     arbitrary dotted tokens would mangle every error containing either. What
//     IS covered is a hostname in one of the grammars above, anchored to a
//     format string in a pinned dependency; a hostname emitted by some other
//     library in some other shape gets only the keyword and URL coverage.
//   - Unix-socket paths. In the per-dial grammar the "hostname" IS the socket
//     path, so redacting it deletes the whole diagnosis, and a local socket path
//     is not employer-internal detail in the way a hostname is.
//
// An exact alternative was considered and deferred: parse the configured DSN
// with `pgconn.ParseConfig` and string-replace its actual host/user/dbname
// values wherever they occur. That is zero-over-redaction and grammar-agnostic,
// but it requires threading the DSN to every RedactError call site, so it is a
// follow-up rather than part of this change.
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
	//
	// `hostaddr` and `database` precede their prefixes in the alternation:
	// Go's regexp is leftmost-FIRST, so `host|hostaddr` would never reach the
	// longer arm.
	//
	// The bare value class excludes `)` as well as quotes and whitespace,
	// because these keywords appear inside a parenthesised clause far more
	// often than a password does — without it, `dbname=sprawl): timeout`
	// swallows the paren and the colon and the message loses its structure. It
	// deliberately still INCLUDES `,`: libpq multi-host is
	// `host=db1.internal,db2.internal`, and stopping at the comma would redact
	// the first host and leak the second.
	//
	// `(?:pg|ssl)?password` rather than an explicit `password|pgpassword`
	// alternation: `\b` cannot match a keyword sitting behind a word character,
	// so spelling only the two left `sslpassword` — libpq's private-key
	// passphrase, a real secret that pgx does NOT mask — passing through
	// verbatim. (`pghost` is likewise not covered, and is left that way: it is
	// identity, not a credential, and pgx does not emit it.)
	//
	// The FINAL value arm is the unbalanced-quote fallback. The balanced arms
	// come first and win; without a fallback, truncated error text (a log line
	// limit, a `%.100s`) leaves `password='hunter2` matching NOTHING — the exact
	// total-passthrough failure the quoted arms were added to fix, closed for
	// the balanced case and left open for the unbalanced one. It stops at
	// whitespace or `)` so it cannot swallow the rest of the message.
	dsnKeywordRe = regexp.MustCompile(`(?i)\b((?:pg|ssl)?password|hostaddr|host|user|database|dbname)\s*=\s*('[^']*'|"[^"]*"|[^\s"')` + "`" + `]+|['"][^\s)]*)`)
	// pgconn's own resolver-failure prefix. Anchored on that literal text so a
	// SINGLE-LABEL internal hostname is covered too — the dotted pattern below
	// cannot safely match one.
	pgconnResolveRe = regexp.MustCompile(`(?i)(hostname resolving error:\s+lookup\s+)[^\s:]+`)
	// net.DNSError renders "lookup <name> on <server>: <err>", or, when the cgo
	// resolver leaves Server empty, "lookup <name>: <err>". Both delimiters are
	// captured because RE2 has no lookahead.
	//
	// TWO patterns, not one, because the two renderings admit different
	// name classes safely. pgconn joins per-host failures with errors.Join and
	// the "hostname resolving error:" prefix wraps the WHOLE join, so it exists
	// on the FIRST line only — lines 2..N are bare, and a multi-host DSN of
	// SINGLE-LABEL internal names leaked every host but the first until these
	// were split.
	//
	//   - with a server clause, the server is itself `<addr>:<port>` (the
	//     resolver's address), which is specific enough to admit ANY name,
	//     single-label or rooted, without matching prose: "lookup table on
	//     disk: not found" has no `:<port>` after the "on".
	//   - without one, there is nothing to anchor against, so the name must be
	//     DOTTED — a trailing dot is allowed, since a rooted FQDN is legal in a
	//     host= value and a pattern that cannot end on a dot leaks the name
	//     whole.
	dnsLookupServerRe = regexp.MustCompile(`(?i)\b(lookup\s+)[^\s:]+(\s+on\s+[^\s:]+:\d{1,5})`)
	dnsLookupRe       = regexp.MustCompile(`(?i)\b(lookup\s+)[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)+\.?([\s:])`)
	// pgconn's perDialConnectError: "<addr> (<originalHostname>): <cause>".
	//
	// The address prefix is required to be IP-SHAPED — a dotted quad or a
	// bracketed IPv6 literal — rather than merely "a token ending in :<port>".
	// pgconn resolves the host before dialling, so the address it prints is
	// always a literal IP; the looser prefix additionally fired on ordinary
	// `token:digits (token)` prose, measured in review (zone, F3) on
	// "events.sql:12 (syntax)" and "12:30 (UTC)". Those near-misses are now in
	// the negative control. The parenthesised group forbids spaces, which is
	// what keeps this off "connect to 127.0.0.1:5432 (retry 3)" — a separate
	// property from the prefix, and the one the control was accidentally
	// relying on for everything.
	dialHostRe = regexp.MustCompile(`((?:\d{1,3}(?:\.\d{1,3}){3}|\[[0-9a-fA-F:.]+\]):\d{1,5} )\(([^()\s]+)\)`)
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
	out = pgconnResolveRe.ReplaceAllString(out, "${1}[redacted]")
	out = dnsLookupServerRe.ReplaceAllString(out, "${1}[redacted]${2}")
	out = dnsLookupRe.ReplaceAllString(out, "${1}[redacted]${2}")
	out = dialHostRe.ReplaceAllString(out, "${1}([redacted])")
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

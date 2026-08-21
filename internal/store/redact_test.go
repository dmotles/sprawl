package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Redaction of DSNs out of error text.
//
// HONEST ABOUT WHAT PROVOKED THIS. A test did catch `sprawl store doctor`
// printing %v of an error verbatim, which is a real structural exposure — any
// error text containing a DSN reaches stdout. But the error in that test was
// FABRICATED, and the claim originally written here — that pgx puts a live
// password in its error text — is FALSE for the pinned pgx v5.10.0: it masks the
// password as `xxxxxx` in parse errors and omits it from connect errors.
// Measured on eight probes, after a reviewer challenged the claim.
//
// The consequence for these tests is the part that matters: every assertion
// below is pinned to a HAND-WRITTEN string shape, so if a future driver emitted
// a credential in some other form — a `Password:` field, percent-encoded
// userinfo, a struct dump — they would all stay green while the leak reopened.
// TestRedactError_OnARealPgxConnectError exists to close that: it provokes an
// error from the actual dependency, so the instrument tracks the driver rather
// than tracking my model of the driver.

func TestRedactSecrets_StripsAPostgresURL(t *testing.T) {
	in := "failed to connect to `postgres://user:sup3rsecret@db.internal.example:5432/sprawl`: connection refused"
	got := RedactSecrets(in)
	for _, leak := range []string{"sup3rsecret", "user:sup3rsecret", "db.internal.example"} {
		if strings.Contains(got, leak) {
			t.Errorf("RedactSecrets left %q in %q", leak, got)
		}
	}
	// The DIAGNOSIS must survive, or redaction has traded a leak for an
	// unusable error message and someone will delete it.
	if !strings.Contains(got, "connection refused") {
		t.Errorf("RedactSecrets destroyed the actionable part of the message: %q", got)
	}
}

func TestRedactSecrets_HandlesBothURLSchemes(t *testing.T) {
	for _, scheme := range []string{"postgres", "postgresql"} {
		in := fmt.Sprintf("dial %s://u:p@h:5432/db failed", scheme)
		if got := RedactSecrets(in); strings.Contains(got, "u:p@h") {
			t.Errorf("%s:// was not redacted: %q", scheme, got)
		}
	}
}

// TestRedactSecrets_StripsKeywordFormPasswords pins the other DSN spelling.
// libpq accepts `host=... password=...`, and pgx quotes that form too, so
// redacting only URLs would leave half the leak open.
func TestRedactSecrets_StripsKeywordFormPasswords(t *testing.T) {
	in := "failed to connect (host=db.example user=sprawl password=hunter2 dbname=sprawl): timeout"
	got := RedactSecrets(in)
	if strings.Contains(got, "hunter2") {
		t.Errorf("keyword-form password survived redaction: %q", got)
	}
	if !strings.Contains(got, "timeout") {
		t.Errorf("redaction destroyed the diagnosis: %q", got)
	}
}

// TestRedactSecrets_StripsQuotedKeywordPasswords pins the spelling that
// bypassed redaction entirely.
//
// libpq allows a single- or double-quoted keyword value, which is the ONLY way
// to express a password containing a space. The original value character class
// excluded quotes, so `password='hunter two'` matched nothing and passed
// through intact — found by review, not by the tests above, because they only
// ever exercised the bare form.
func TestRedactSecrets_StripsQuotedKeywordPasswords(t *testing.T) {
	cases := map[string]string{
		"single-quoted with a space": `failed (host=db user=sprawl password='hunter two' dbname=sprawl): timeout`,
		"double-quoted":              `failed (host=db user=sprawl password="hunter2" dbname=sprawl): timeout`,
		"single-quoted no space":     `failed (host=db password='hunter2' dbname=sprawl): timeout`,
	}
	for name, in := range cases {
		got := RedactSecrets(in)
		for _, leak := range []string{"hunter two", "hunter2"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s: %q survived redaction in %q", name, leak, got)
			}
		}
		if !strings.Contains(got, "timeout") {
			t.Errorf("%s: redaction destroyed the diagnosis: %q", name, got)
		}
	}
}

// TestRedactError_OnARealPgxConnectError is the only test here that tracks the
// DEPENDENCY rather than my model of it.
//
// Every other assertion in this file is pinned to a hand-written string, so a
// change in how pgx formats errors would leave them all green while a leak
// reopened. This one provokes a genuine error from pgx with a
// credential-bearing DSN and asserts on RedactError of whatever comes back.
//
// It deliberately does NOT assert that pgx leaks anything — measured, it does
// not. It asserts the composition is safe and the diagnosis survives, so if a
// future pgx starts including the password, this test is where it surfaces.
func TestRedactError_OnARealPgxConnectError(t *testing.T) {
	const pw = probePassword
	// Port 1 refuses immediately; connect_timeout keeps this fast.
	dsn := "postgres://leakuser:" + pw + "@127.0.0.1:1/nodb?sslmode=disable&connect_timeout=1"

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		// The DSN above is hardcoded and well-formed, so a parse failure is a
		// bug in this test, not a scenario. Returning here would leave the row
		// green having asserted nothing.
		t.Fatalf("ParseConfig unexpectedly failed on a hardcoded DSN: %v", err)
	}
	defer pool.Close()

	_, err = pool.Begin(context.Background())
	if err == nil {
		t.Skip("port 1 unexpectedly accepted a connection; nothing to redact")
	}
	got := RedactError(err)
	if strings.Contains(got, pw) {
		t.Errorf("CANARY FIRED — pgx now emits the password in a connect error: %q", got)
	}
	// pgx re-renders connect errors in KEYWORD form even though the DSN above
	// is URL form, so dsnURLRe never sees them (QUM-1281). These two survived
	// verbatim while this test was green.
	for _, leak := range []string{"leakuser", "nodb"} {
		if strings.Contains(got, leak) {
			t.Errorf("a real pgx connect error leaked %q through RedactError: %q", leak, got)
		}
	}
	// Anti-vacuity: the error must actually say something, or the assertion
	// above passed over an empty string.
	if got == "" {
		t.Fatal("RedactError of a real pgx failure is empty, so the assertion above proved nothing")
	}
	if !strings.Contains(strings.ToLower(got), "connect") && !strings.Contains(strings.ToLower(got), "refused") {
		t.Errorf("redaction destroyed the diagnosis of a real pgx error: %q", got)
	}
}

// TestRedactSecrets_LeavesOrdinaryTextAlone is the negative control: without it,
// a function returning a constant would satisfy every assertion above.
func TestRedactSecrets_LeavesOrdinaryTextAlone(t *testing.T) {
	for _, in := range []string{
		"relation \"events\" does not exist (SQLSTATE 42P01)",
		"permission denied for table events",
		"",
		"no such file or directory",
		// Shapes the QUM-1281 widening could plausibly over-match. Direction:
		// this control must stay QUIET — each of these must come back
		// byte-identical.
		// Near-misses for the anchored DNS-resolver pattern: prose with the
		// full `lookup <token> on <token>:` shape, and with `on <host>:`.
		"lookup table on disk: not found",
		"ran pg_dump on host2: ok",
		"failed to lookup agent by name",
		// Near-misses for the anchored per-dial `<addr> (<host>)` pattern.
		"see docs/event-log-setup.md (2 files)",
		"connect to 127.0.0.1:5432 (retry 3)",
		// port= is deliberately not a redacted keyword.
		"listening on port=8080",
		// A real caller: cmd/store.go routes filesystem-path errors through
		// RedactError too.
		"open /var/lib/sprawl/spill: permission denied",
	} {
		if got := RedactSecrets(in); got != in {
			t.Errorf("RedactSecrets altered text containing no secret:\n in:  %q\n out: %q", in, got)
		}
	}
}

// TestRedactError_PreservesNilAndRedactsTheRest pins the error-shaped wrapper the
// CLI actually calls.
func TestRedactError_PreservesNilAndRedactsTheRest(t *testing.T) {
	if got := RedactError(nil); got != "" {
		t.Errorf("RedactError(nil) = %q, want empty", got)
	}
	err := fmt.Errorf("wrapped: %w", errors.New("connect to postgres://a:b@c/d failed"))
	if got := RedactError(err); strings.Contains(got, "a:b@c") {
		t.Errorf("RedactError leaked the DSN: %q", got)
	}
}

// TestRedactSecrets_StripsKeywordFormIdentity pins the widening from QUM-1281.
//
// pgx renders CONNECT errors in keyword form regardless of the DSN form it was
// handed, so keyword-form identity — not the URL form — is the shape a real
// outage produces. None of these probes may contain a `postgres://` substring:
// dsnURLRe would swallow them wholesale and the keyword branch would never be
// exercised, which is how a widened regex passes for the wrong reason.
func TestRedactSecrets_StripsKeywordFormIdentity(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		leaks []string
		keeps []string
	}{
		{
			name:  "connect error shape pgx actually emits",
			in:    "failed to connect to `user=leakuser database=leakdb`: dial error: connection refused",
			leaks: []string{"leakuser", "leakdb"},
			// The KEY must survive alongside the diagnosis: an implementation
			// that deletes the whole `user=…` clause passes an absence-only
			// check while destroying the message.
			keeps: []string{"user=[redacted]", "database=[redacted]", "connection refused"},
		},
		{
			name:  "host and dbname keywords",
			in:    "failed (host=db-1.internal.example user=sprawlrole dbname=sprawldb port=5432): timeout",
			leaks: []string{"db-1.internal.example", "sprawlrole", "sprawldb"},
			// port is deliberately NOT redacted: it carries no credential and a
			// wrong port is often the entire diagnosis.
			keeps: []string{"host=[redacted]", "port=5432", "timeout"},
		},
		{
			name:  "hostaddr and database spellings",
			in:    "failed (hostaddr=10.1.2.3 database=sprawldb): timeout",
			leaks: []string{"10.1.2.3", "sprawldb"},
			keeps: []string{"hostaddr=[redacted]", "timeout"},
		},
		{
			name:  "spacing and ordering nobody designed for",
			in:    `failed (dbname = "sprawl db" user = 'sprawl role' host =db-2.internal.example): timeout`,
			leaks: []string{"sprawl db", "sprawl role", "db-2.internal.example"},
			// "): timeout" pins the value class against eating the closing
			// paren on the quoted/spaced path too — PreservesStructuralPunctuation
			// only covers bare values. The bare key tokens pin key survival
			// without over-fitting to a spacing form not yet chosen; each
			// appears in the input only as a key.
			keeps: []string{"dbname", "user", "host", "): timeout"},
		},
		{
			name:  "libpq multi-host must not leak the second host",
			in:    "failed (host=db1.internal.example,db2.internal.example user=sprawlrole): timeout",
			leaks: []string{"db1.internal.example", "db2.internal.example", "sprawlrole"},
			keeps: []string{"host=[redacted]", "): timeout"},
		},
		{
			name:  "repeated keys",
			in:    "failed (host=a.internal.example host=b.internal.example): timeout",
			leaks: []string{"a.internal.example", "b.internal.example"},
			keeps: []string{"host=[redacted] host=[redacted]", "): timeout"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.in)
			if got == "" {
				t.Fatal("RedactSecrets returned empty, so the absence assertions below prove nothing")
			}
			for _, leak := range tc.leaks {
				if strings.Contains(got, leak) {
					t.Errorf("%q survived redaction in %q", leak, got)
				}
			}
			for _, keep := range tc.keeps {
				if !strings.Contains(got, keep) {
					t.Errorf("redaction destroyed %q: %q", keep, got)
				}
			}
		})
	}
}

// TestRedactSecrets_PreservesStructuralPunctuation pins the value class boundary.
//
// The bare-value character class must exclude `)`, or a widened `dbname` arm
// eats the closing paren and the colon that follow it and the message reads
// `dbname=[redacted] timeout`. The pre-existing password tests stay green
// through that mangling because they only check that "timeout" is present.
//
// Positive control run: adding `dbname` to the key alternation WITHOUT adding
// `)` to the value class — the defect this names — makes this test fire.
func TestRedactSecrets_PreservesStructuralPunctuation(t *testing.T) {
	in := "failed (host=db.internal.example user=sprawlrole password=hunter2 dbname=sprawldb): timeout"
	got := RedactSecrets(in)
	if !strings.Contains(got, "): timeout") {
		t.Errorf("redaction mangled the structural punctuation around the last value: %q", got)
	}
}

// TestRedactSecrets_IsIdempotent guards the `[redacted]` marker against being
// re-chewed by a second pass — RedactError output is sometimes wrapped and
// re-redacted further up the stack.
//
// Positive control run: a mutant whose keyword arm APPENDS the marker
// (`return m + " [redacted]"`) rather than replacing the value makes this fire.
// A constant-return mutant does NOT — a constant is trivially idempotent — so
// that mutant is the wrong control for this assertion.
func TestRedactSecrets_IsIdempotent(t *testing.T) {
	for _, in := range []string{
		"failed to connect to `user=leakuser database=leakdb`: connection refused",
		"cannot parse `postgres://leakuser:pw@db.internal.example:5432/sprawl`: invalid port",
		"failed (host=db.internal.example port=5432): timeout",
		"failed: hostname resolving error: lookup db.internal.example on 127.0.0.53:53: no such host",
		"failed: 127.0.0.1:1 (db.internal.example): dial error: connection refused",
	} {
		once := RedactSecrets(in)
		if twice := RedactSecrets(once); twice != once {
			t.Errorf("RedactSecrets is not idempotent:\n in:    %q\n once:  %q\n twice: %q", in, once, twice)
		}
	}
}

// realPgxConnectError provokes a genuine *pgconn.ConnectError from the pinned
// pgx by driving a real pgxpool at synthetic hosts whose resolution is
// controlled by ConnConfig.LookupFunc.
//
// The LookupFunc seam is what makes this hermetic AND real: no egress, no
// dependence on how this host's resolver treats an unknown name (a wildcard or
// captive resolver changes the net.DNSError text), while the error object, its
// formatting and the pgx code path are all the production ones — LookupFunc is
// consumed by pgconn's own buildConnectOneConfigs, not by a test-only branch. A
// hand-written string that "looks like" a pgx error is exactly the substitution
// that let QUM-1279 ship a green test over a broken scenario.
func realPgxConnectError(t *testing.T, hosts string, lookup func(context.Context, string) ([]string, error)) error {
	t.Helper()
	dsn := "postgres://leakuser:" + probePassword + "@" + hosts + ":1/leakdb?sslmode=disable&connect_timeout=1"
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig of a synthetic DSN failed: %v", err)
	}
	cfg.ConnConfig.LookupFunc = lookup
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig failed: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Begin(context.Background()); err == nil {
		t.Fatal("expected the synthetic host to fail to connect, got a live connection")
	}
	return err
}

// probePassword is a canary, not a live assertion: pgx omits the password from
// connect errors entirely (measured, see redact.go), so the checks for it below
// CANNOT fire today. They exist so a future driver that starts including it
// surfaces here. Counting them as live coverage would overstate this file.
const probePassword = "sup3rsecretPROBE"

func dnsErrLookup(server string) func(context.Context, string) ([]string, error) {
	return func(_ context.Context, name string) ([]string, error) {
		return nil, &net.DNSError{Err: "no such host", Name: name, Server: server, IsNotFound: true}
	}
}

// TestRedactError_OnARealPgxDNSFailure is the measured production shape: an
// internal hostname that does not resolve. pgx puts the hostname in the
// RESOLVER's text — `lookup <host> on <server>: no such host` — which is
// outside any keyword or URL context, so widening dsnKeywordRe alone does not
// reach it.
//
// Both net.DNSError renderings are covered. Server is optional: the pure-Go
// resolver populates it, the cgo resolver leaves it empty and the ` on <server>`
// clause disappears — so a pattern anchored on ` on ` alone goes green on a box
// with a non-trivial nsswitch.conf while leaking the hostname in the field.
func TestRedactError_OnARealPgxDNSFailure(t *testing.T) {
	const host = "db-probe.internal.example"
	cases := map[string]struct {
		server string
		keeps  []string
	}{
		"resolver server reported": {server: "127.0.0.53:53", keeps: []string{"127.0.0.53:53", "no such host"}},
		"resolver server empty":    {server: "", keeps: []string{"no such host"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := RedactError(realPgxConnectError(t, host, dnsErrLookup(tc.server)))
			assertRedactedConnectError(t, got, host, tc.keeps)
		})
	}
}

// TestRedactError_OnARealPgxDialFailure covers the other measured tail grammar:
// resolution succeeds, the dial is refused, and pgconn's perDialConnectError
// renders `<addr> (<originalHostname>): <cause>` — the hostname again outside
// any keyword context. IPv6 is a separate row because the address is bracketed
// there, and a pattern anchored on a dotted-quad misses it silently.
func TestRedactError_OnARealPgxDialFailure(t *testing.T) {
	const host = "db-probe.internal.example"
	cases := map[string]struct {
		addr  string
		keeps []string
	}{
		// The structural form, not the bare substring: "127.0.0.1:1" also
		// occurs inside "dial tcp 127.0.0.1:1:", so asserting the substring
		// alone is satisfied by an implementation that eats the whole
		// `<addr> (<host>)` prefix — the over-redaction this keep exists for.
		"ipv4": {addr: "127.0.0.1", keeps: []string{"127.0.0.1:1 (", "refused"}},
		// "dial error" is pgconn's own wrapper text, present on every dial
		// outcome — "refused" is not, on a box without IPv6 loopback.
		"ipv6": {addr: "::1", keeps: []string{"[::1]:1 (", "dial error"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := RedactError(realPgxConnectError(t, host, func(context.Context, string) ([]string, error) {
				return []string{tc.addr}, nil
			}))
			assertRedactedConnectError(t, got, host, tc.keeps)
		})
	}
}

// TestRedactError_OnARealPgxMultiHostFailure drives the joined multi-line shape:
// pgconn wraps per-fallback failures with errors.Join, so the tail is N
// newline-separated `lookup …` lines rather than one. A pattern that is
// accidentally line- or greedy-tail sensitive leaks every host but the first.
func TestRedactError_OnARealPgxMultiHostFailure(t *testing.T) {
	const hostA, hostB = "db-a.internal.example", "db-b.internal.example"
	got := RedactError(realPgxConnectError(t, hostA+","+hostB, dnsErrLookup("127.0.0.53:53")))
	if !strings.Contains(got, "\n") {
		t.Fatalf("expected a joined multi-line pgx error, got a single line: %q", got)
	}
	assertRedactedConnectError(t, got, hostA, []string{"no such host"})
	assertRedactedConnectError(t, got, hostB, nil)
}

func assertRedactedConnectError(t *testing.T, got, host string, keeps []string) {
	t.Helper()
	if got == "" {
		t.Fatal("RedactError of a real pgx failure is empty, so the assertions below prove nothing")
	}
	for _, leak := range []string{host, "leakuser", "leakdb"} {
		if strings.Contains(got, leak) {
			t.Errorf("a real pgx connect error leaked %q through RedactError: %q", leak, got)
		}
	}
	if strings.Contains(got, probePassword) {
		t.Errorf("CANARY FIRED — pgx now emits the password in a connect error: %q", got)
	}
	for _, keep := range keeps {
		if !strings.Contains(got, keep) {
			t.Errorf("redaction destroyed the diagnosis fragment %q: %q", keep, got)
		}
	}
}

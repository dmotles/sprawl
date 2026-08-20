package store

import (
	"context"
	"errors"
	"fmt"
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
	const pw = "sup3rsecretPROBE"
	// Port 1 refuses immediately; connect_timeout keeps this fast.
	dsn := "postgres://leakuser:" + pw + "@127.0.0.1:1/nodb?sslmode=disable&connect_timeout=1"

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		// A parse error is also a real pgx error and an equally valid subject.
		if got := RedactError(err); strings.Contains(got, pw) {
			t.Fatalf("a real pgx parse error leaked the password through RedactError: %q", got)
		}
		return
	}
	defer pool.Close()

	_, err = pool.Begin(context.Background())
	if err == nil {
		t.Skip("port 1 unexpectedly accepted a connection; nothing to redact")
	}
	got := RedactError(err)
	if strings.Contains(got, pw) {
		t.Errorf("a real pgx connect error leaked the password through RedactError: %q", got)
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

package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Redaction of DSNs out of error text.
//
// THIS EXISTS BECAUSE A TEST CAUGHT A REAL LEAK, not because it seemed prudent.
// pgx quotes the connection string in its errors — "failed to connect to
// `postgres://user:pass@host:5432/db`: connection refused" — so `sprawl store
// doctor` printing %v of a connection failure put a live database password on
// stdout. This repo is public and terminal transcripts get pasted into issues.

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

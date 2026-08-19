package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The DSN is a secret. It comes from SPRAWL_DB_DSN or ~/.config/sprawl/secrets.yaml
// and from nowhere else — never .sprawl/config.yaml, which is a TRACKED file in
// a PUBLIC repo. These tests pin the precedence, the 0600 refusal, and (most
// importantly) that no code path ever puts the DSN into a message.

func writeSecrets(t *testing.T, dir, body string, mode os.FileMode) string {
	t.Helper()
	cfgDir := filepath.Join(dir, "sprawl")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(cfgDir, "secrets.yaml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	// WriteFile respects umask, so set the mode explicitly — otherwise the
	// 0644 case silently becomes 0600 on a restrictive umask and the refusal
	// assertion below would be testing the wrong file.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return path
}

func fixedDir(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

const testDSN = "postgres://u:p@db.invalid:5432/sprawl"

func TestResolveDSN_EnvWins(t *testing.T) {
	dir := t.TempDir()
	writeSecrets(t, dir, "db_dsn: postgres://from-file/db\n", 0o600)

	getenv := func(k string) string {
		if k == "SPRAWL_DB_DSN" {
			return testDSN
		}
		return ""
	}
	dsn, source, err := ResolveDSN(getenv, fixedDir(dir))
	if err != nil {
		t.Fatalf("ResolveDSN: %v", err)
	}
	if dsn != testDSN {
		t.Errorf("dsn = %q, want the env value — SPRAWL_DB_DSN must outrank the secrets file", dsn)
	}
	if source != "SPRAWL_DB_DSN" {
		t.Errorf("source = %q, want %q", source, "SPRAWL_DB_DSN")
	}
}

func TestResolveDSN_FallsBackToSecretsFile(t *testing.T) {
	dir := t.TempDir()
	path := writeSecrets(t, dir, "db_dsn: "+testDSN+"\n", 0o600)

	dsn, source, err := ResolveDSN(func(string) string { return "" }, fixedDir(dir))
	if err != nil {
		t.Fatalf("ResolveDSN: %v", err)
	}
	if dsn != testDSN {
		t.Errorf("dsn = %q, want %q", dsn, testDSN)
	}
	if source != path {
		t.Errorf("source = %q, want the secrets path %q", source, path)
	}
}

// TestResolveDSN_SourceNeverLeaksTheSecret is the public-repo hygiene assertion.
//
// `source` exists so `sprawl store doctor` can tell an operator WHERE the DSN
// came from without printing it. If the source string ever carried the DSN
// itself, every diagnostic, every error message, and every log line built from
// it would leak a database credential — in a public repo's issue tracker, in
// pasted terminal output, in a bug report.
func TestResolveDSN_SourceNeverLeaksTheSecret(t *testing.T) {
	dir := t.TempDir()
	writeSecrets(t, dir, "db_dsn: "+testDSN+"\n", 0o600)

	for _, tc := range []struct {
		name   string
		getenv func(string) string
	}{
		{"from env", func(k string) string {
			if k == "SPRAWL_DB_DSN" {
				return testDSN
			}
			return ""
		}},
		{"from file", func(string) string { return "" }},
	} {
		_, source, err := ResolveDSN(tc.getenv, fixedDir(dir))
		if err != nil {
			t.Fatalf("%s: ResolveDSN: %v", tc.name, err)
		}
		if strings.Contains(source, "db.invalid") || strings.Contains(source, "u:p") {
			t.Errorf("%s: source %q contains the DSN — it must name the ORIGIN, never the secret", tc.name, source)
		}
	}
}

// TestResolveDSN_RefusesWorldOrGroupReadableSecrets pins the 0600 requirement.
//
// Not cosmetic on a shared host: this repo's own agents run in sibling
// worktrees under one uid, and the secrets file holds a credential to the
// authoritative cross-host event log.
func TestResolveDSN_RefusesWorldOrGroupReadableSecrets(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o666} {
		dir := t.TempDir()
		writeSecrets(t, dir, "db_dsn: "+testDSN+"\n", mode)

		dsn, _, err := ResolveDSN(func(string) string { return "" }, fixedDir(dir))
		if !errors.Is(err, ErrInsecureSecrets) {
			t.Errorf("mode %#o: got err=%v, want ErrInsecureSecrets", mode, err)
			continue
		}
		if dsn != "" {
			t.Errorf("mode %#o: a refused secrets file must yield no DSN, got %q", mode, dsn)
		}
		if !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %#o: the error must carry a next-action hint naming `chmod 600`; got: %v", mode, err)
		}
		if strings.Contains(err.Error(), "db.invalid") {
			t.Errorf("mode %#o: the error leaks the DSN: %v", mode, err)
		}
	}
}

// TestResolveDSN_Accepts0600 is the negative control for the assertion above:
// without it, a resolver that refused EVERY secrets file would pass it.
func TestResolveDSN_Accepts0600(t *testing.T) {
	dir := t.TempDir()
	writeSecrets(t, dir, "db_dsn: "+testDSN+"\n", 0o600)
	dsn, _, err := ResolveDSN(func(string) string { return "" }, fixedDir(dir))
	if err != nil {
		t.Fatalf("a 0600 secrets file must be accepted; got: %v", err)
	}
	if dsn != testDSN {
		t.Errorf("dsn = %q, want %q", dsn, testDSN)
	}
}

// TestResolveDSN_UnconfiguredIsNotAnError pins that "no DSN anywhere" is a
// normal, quiet state rather than a failure.
//
// The store is opt-in. Every `sprawl` invocation on a machine that has never
// enabled it resolves the DSN, and if that returned an error the CLI would
// either have to swallow it (and swallowing is how a real misconfiguration goes
// unnoticed) or shout at every user who never asked for the feature. The
// "enabled but unconfigured" case IS loud, and it is asserted at the Ledger
// level where the feature flag is known — not here, where it is not.
func TestResolveDSN_UnconfiguredIsNotAnError(t *testing.T) {
	dsn, source, err := ResolveDSN(func(string) string { return "" }, fixedDir(t.TempDir()))
	if err != nil {
		t.Fatalf("no env and no secrets file must not be an error: %v", err)
	}
	if dsn != "" {
		t.Errorf("dsn = %q, want empty", dsn)
	}
	if source != "" {
		t.Errorf("source = %q, want empty", source)
	}
}

func TestResolveDSN_MalformedSecretsFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeSecrets(t, dir, "db_dsn: [unclosed\n", 0o600)
	if _, _, err := ResolveDSN(func(string) string { return "" }, fixedDir(dir)); err == nil {
		t.Error("a malformed secrets file must be an error — silently treating it as unconfigured turns a typo into a disabled store")
	}
}

// TestResolveDSN_EmptyDSNInSecretsFileIsUnconfigured pins that a present-but-empty
// key behaves like absence, so `db_dsn:` with nothing after it does not produce a
// DSN of "" that then fails deep inside pgx with an unhelpful message.
func TestResolveDSN_EmptyDSNInSecretsFileIsUnconfigured(t *testing.T) {
	dir := t.TempDir()
	writeSecrets(t, dir, "db_dsn: \"\"\n", 0o600)
	dsn, source, err := ResolveDSN(func(string) string { return "" }, fixedDir(dir))
	if err != nil {
		t.Fatalf("ResolveDSN: %v", err)
	}
	if dsn != "" || source != "" {
		t.Errorf("an empty db_dsn must read as unconfigured; got dsn=%q source=%q", dsn, source)
	}
}

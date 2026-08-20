package store

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
)

// OpenProcessLedger is the one place that reads the world — the config file, the
// environment, the secrets file, git — and turns it into a Ledger.
//
// It takes every one of those as an injected dependency so all of the branching
// is testable without a repository, a database, or an environment. The
// process-wide singleton on top of it is five lines and holds no logic.

func newProcessDeps(t *testing.T, cfg *config.Config, dsn string) ProcessDeps {
	t.Helper()
	return ProcessDeps{
		SprawlRoot: t.TempDir(),
		LoadConfig: func(string) (*config.Config, error) { return cfg, nil },
		Getenv: func(k string) string {
			if k == EnvDSN {
				return dsn
			}
			return ""
		},
		UserConfigDir: func() (string, error) { return t.TempDir(), nil },
		Git: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch strings.Join(args, " ") {
			case "config --get remote.origin.url":
				return []byte("https://example.invalid/repo\n"), nil
			case "rev-parse HEAD":
				return []byte("0123456789abcdef0123456789abcdef01234567\n"), nil
			}
			return nil, errors.New("unexpected git call: " + strings.Join(args, " "))
		},
		Logger: slog.New(slog.DiscardHandler),
	}
}

// TestOpenProcessLedger_DisabledIsQuiet pins the default path: the flag is off,
// so nothing is read, nothing is opened, and no error is produced.
//
// This runs on every `sprawl` invocation on every host that has never enabled
// the store, so it has to be silent AND cheap — the assertion on git being
// untouched is the cheap part.
func TestOpenProcessLedger_DisabledIsQuiet(t *testing.T) {
	var gitCalled bool
	d := newProcessDeps(t, &config.Config{}, "")
	inner := d.Git
	d.Git = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		gitCalled = true
		return inner(ctx, dir, args...)
	}

	l, err := OpenProcessLedger(context.Background(), d)
	if err != nil {
		t.Fatalf("a disabled store must not error: %v", err)
	}
	if l != nil {
		t.Error("a disabled store must yield a nil Ledger")
	}
	if gitCalled {
		t.Error("the disabled path ran git; it must do no work at all, since it runs on every invocation on every host that never enabled the store")
	}
}

// TestOpenProcessLedger_EnabledWithoutDSNFailsLoudly pins that an enabled store
// with no DSN is a loud misconfiguration rather than a silent disable.
func TestOpenProcessLedger_EnabledWithoutDSNFailsLoudly(t *testing.T) {
	d := newProcessDeps(t, &config.Config{EventLog: "true"}, "")
	_, err := OpenProcessLedger(context.Background(), d)
	if err == nil {
		t.Fatal("an enabled store with no DSN anywhere must fail loudly")
	}
	var hint *HintError
	if !errors.As(err, &hint) {
		t.Fatalf("the failure must carry a next action; got %T: %v", err, err)
	}
}

// TestOpenProcessLedger_ConfigLoadFailureIsReported pins that an unreadable
// config is an error and not "therefore disabled".
//
// Treating it as disabled would mean a corrupt .sprawl/config.yaml silently
// switches the store off on that host alone — a partial-fleet outage with no
// symptom anywhere.
func TestOpenProcessLedger_ConfigLoadFailureIsReported(t *testing.T) {
	d := newProcessDeps(t, nil, "postgres://x/y")
	boom := errors.New("permission denied")
	d.LoadConfig = func(string) (*config.Config, error) { return nil, boom }

	if _, err := OpenProcessLedger(context.Background(), d); !errors.Is(err, boom) {
		t.Errorf("got err=%v, want the config load failure — reading it as `disabled` would silently switch the store off on one host", err)
	}
}

// TestOpenProcessLedger_NoRemoteFallsBackToAProvisionalIdentity pins what the
// plan of record actually asks for: "Project = repo remote URL (unique key;
// TEMP NAME IF UNSET, renameable)".
//
// THIS TEST REPLACES ONE THAT ASSERTED THE OPPOSITE. The first version pinned a
// hard failure on a missing remote, which was my misreading of the design rather
// than the design — and because the test agreed with the implementation, nothing
// in the unit suite could disagree with either. It took the store-degraded e2e
// row, whose sandbox repo has no origin remote, to surface it: no emitter was
// built, so no telemetry spilled, so the AC5 spill assertion failed. Recorded
// rather than quietly swapped, because "assert the intended outcome, not the
// current mechanism" is exactly the rule that was broken.
//
// A fresh repo, a sandbox, and a scratch checkout all legitimately have no
// remote; refusing to record anything there would mean the store cannot be
// enabled until someone pushes.
func TestOpenProcessLedger_NoRemoteFallsBackToAProvisionalIdentity(t *testing.T) {
	root := t.TempDir()
	d := newProcessDeps(t, &config.Config{EventLog: "true"}, "postgres://u@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	d.SprawlRoot = root
	d.Git = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "config --get remote.origin.url" {
			return nil, errors.New("exit status 1")
		}
		return []byte("0123456789abcdef0123456789abcdef01234567\n"), nil
	}

	l, err := OpenProcessLedger(context.Background(), d)
	if err != nil {
		t.Fatalf("a repo with no remote must still be able to enable the store: %v", err)
	}
	if l == nil {
		t.Fatal("no Ledger returned for a repo with no remote, so nothing would ever be recorded there")
	}
	t.Cleanup(l.Close)
	// The DSN points at a refused port, so reaching degraded state proves the
	// function got all the way through identity resolution to Open.
	if l.DegradedError() == nil {
		t.Error("expected a degraded Ledger; a healthy one means this fixture is not exercising the path it claims")
	}
}

// TestProvisionalProjectID_IsStableAndNotRemoteShaped pins the two properties
// the fallback identity needs.
//
// Stable, because an identity that varied per run would make every session on an
// unpushed repo a new project. Not URL-shaped, because a provisional identity is
// HOST-LOCAL while a real remote is global — two machines on the same unpushed
// repo land in two projects, and that difference has to be visible rather than
// hidden behind something that looks like a remote.
func TestProvisionalProjectID_IsStableAndNotRemoteShaped(t *testing.T) {
	root := t.TempDir()
	first, second := ProvisionalProjectID(root), ProvisionalProjectID(root)
	if first != second {
		t.Errorf("ProvisionalProjectID is not stable (%q vs %q); every session would create a new project", first, second)
	}
	if !strings.HasPrefix(first, "local:") {
		t.Errorf("ProvisionalProjectID = %q; it must be visibly NOT a remote URL, since its uniqueness is only host-local", first)
	}
	if ProvisionalProjectID(root) == ProvisionalProjectID(t.TempDir()) {
		t.Error("two different repos share a provisional identity, so they would share a namespace in the log")
	}
}

// TestOpenProcessLedger_UnresolvableHeadDoesNotBlockTheStore pins the ONE piece
// of provenance that is allowed to be missing.
//
// A repository with no commits yet has no HEAD. That is a legitimate state, and
// refusing to record anything because of it would mean the store cannot be
// enabled on a fresh repo — while the SHA it wants is only used to annotate
// events, never to identify anything. So this degrades to an absent field rather
// than to a failure, which is the opposite call from the remote URL above, and
// deliberately so.
func TestOpenProcessLedger_UnresolvableHeadDoesNotBlockTheStore(t *testing.T) {
	root := t.TempDir()
	d := newProcessDeps(t, &config.Config{EventLog: "true"}, "postgres://u@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	d.SprawlRoot = root
	d.Git = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "config --get remote.origin.url" {
			return []byte("https://example.invalid/fresh\n"), nil
		}
		// No commits: rev-parse HEAD fails, and so does any diff against it.
		return nil, errors.New("fatal: ambiguous argument 'HEAD'")
	}

	l, err := OpenProcessLedger(context.Background(), d)
	if err != nil {
		t.Fatalf("a repo with no commits must still be able to enable the store: %v", err)
	}
	if l == nil {
		t.Fatal("no Ledger was returned")
	}
	t.Cleanup(l.Close)
	// The DSN points at a refused port, so this is the degraded path — which is
	// what proves the function got all the way to Open rather than bailing.
	if l.DegradedError() == nil {
		t.Error("expected a degraded Ledger (the DSN points at a refused port); getting a healthy one means the fixture is not exercising what it claims")
	}
}

// TestOpenProcessLedger_ReadsTheSecretsFileWhenEnvIsUnset pins the fallback, and
// with it that OpenProcessLedger threads the user config dir through rather than
// reaching for the real ~/.config.
func TestOpenProcessLedger_ReadsTheSecretsFileWhenEnvIsUnset(t *testing.T) {
	cfgDir := t.TempDir()
	sprawlCfg := filepath.Join(cfgDir, "sprawl")
	if err := os.MkdirAll(sprawlCfg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secrets := filepath.Join(sprawlCfg, SecretsFileName)
	if err := os.WriteFile(secrets, []byte("db_dsn: postgres://u@127.0.0.1:1/db?sslmode=disable&connect_timeout=1\n"), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	if err := os.Chmod(secrets, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	d := newProcessDeps(t, &config.Config{EventLog: "true"}, "")
	d.UserConfigDir = func() (string, error) { return cfgDir, nil }

	l, err := OpenProcessLedger(context.Background(), d)
	if err != nil {
		t.Fatalf("the secrets file should have supplied a DSN: %v", err)
	}
	if l == nil {
		t.Fatal("no Ledger returned")
	}
	t.Cleanup(l.Close)
	if got := l.DSNSource(); got != secrets {
		t.Errorf("DSNSource = %q, want the secrets path %q", got, secrets)
	}
	if strings.Contains(l.DSNSource(), "postgres://") {
		t.Errorf("DSNSource leaks the DSN: %q", l.DSNSource())
	}
}

// TestOpenProcessLedger_InsecureSecretsFileIsRefused pins that the 0600 refusal
// survives being reached through this path too. A permission check that only
// fires when called directly is a permission check nothing enforces.
func TestOpenProcessLedger_InsecureSecretsFileIsRefused(t *testing.T) {
	cfgDir := t.TempDir()
	sprawlCfg := filepath.Join(cfgDir, "sprawl")
	if err := os.MkdirAll(sprawlCfg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secrets := filepath.Join(sprawlCfg, SecretsFileName)
	if err := os.WriteFile(secrets, []byte("db_dsn: postgres://u@h/db\n"), 0o644); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	if err := os.Chmod(secrets, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	d := newProcessDeps(t, &config.Config{EventLog: "true"}, "")
	d.UserConfigDir = func() (string, error) { return cfgDir, nil }

	_, err := OpenProcessLedger(context.Background(), d)
	if !errors.Is(err, ErrInsecureSecrets) {
		t.Errorf("got err=%v, want ErrInsecureSecrets", err)
	}
}

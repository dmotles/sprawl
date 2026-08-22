package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/store"
)

// The memory side of the plan's "dual-write then replace" decision: a HANDOFF
// summary is written to .sprawl/memory as it always was, and additionally
// recorded in the event log.
//
// The file write stays authoritative until M6, so the ordering and the failure
// policy both matter and are asserted: the event is recorded only AFTER the file
// lands, and nothing the event log does can fail the write.

func TestWriteSessionSummary_DualWritesOnlyForAHandoff(t *testing.T) {
	root := t.TempDir()
	var calls []string
	restore := setHandoffEventHookForTest(func(sprawlRoot string, s Session, body string) {
		calls = append(calls, s.SessionID)
	})
	defer restore()

	// A NON-handoff summary must not emit. Ordinary session summaries are
	// written on every session end; emitting for them would flood the log with
	// events the plan scopes to handoffs only.
	if err := WriteSessionSummary(root, Session{
		SessionID: "ordinary", Timestamp: time.Now(), Handoff: false,
	}, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("a non-handoff summary emitted %v; the plan scopes the dual-write to handoffs", calls)
	}

	// A handoff must emit exactly once.
	if err := WriteSessionSummary(root, Session{
		SessionID: "handoff-1", Timestamp: time.Now(), Handoff: true,
	}, "the summary"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}
	if len(calls) != 1 || calls[0] != "handoff-1" {
		t.Errorf("handoff emitted %v, want exactly [handoff-1]", calls)
	}
}

// TestWriteSessionSummary_EmitsOnlyAfterTheFileLands pins the ordering.
//
// The memory file is the system of record until M6. Emitting before the file
// exists would put an event in the log pointing at a summary that may never have
// been written — and summary_sha256 is what ties the two together, so a reader
// following it would find nothing.
func TestWriteSessionSummary_EmitsOnlyAfterTheFileLands(t *testing.T) {
	root := t.TempDir()
	var fileExistedAtEmit bool
	restore := setHandoffEventHookForTest(func(sprawlRoot string, s Session, body string) {
		matches, _ := filepath.Glob(filepath.Join(sprawlRoot, ".sprawl", "memory", "sessions", "*.md"))
		fileExistedAtEmit = len(matches) > 0
	})
	defer restore()

	if err := WriteSessionSummary(root, Session{
		SessionID: "ordered", Timestamp: time.Now(), Handoff: true,
	}, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}
	if !fileExistedAtEmit {
		t.Error("the event was recorded before the summary file existed; a reader following summary_sha256 would find nothing")
	}
}

// TestWriteSessionSummary_HookPanicDoesNotLoseTheHandoff is the failure-policy
// assertion, and it is deliberately harsher than the real hook's contract.
//
// RecordHandoff is documented never to return an error, but "documented" is not
// "enforced" — a future edit, or a nil map deep inside the store, would panic on
// the handoff path and take the session summary with it. The summary is the one
// artifact a handoff exists to produce, so an observability component must not be
// able to destroy it.
func TestWriteSessionSummary_HookPanicDoesNotLoseTheHandoff(t *testing.T) {
	root := t.TempDir()
	restore := setHandoffEventHookForTest(func(string, Session, string) {
		panic("the event log exploded")
	})
	defer restore()

	if err := WriteSessionSummary(root, Session{
		SessionID: "survives", Timestamp: time.Now(), Handoff: true,
	}, "the precious summary"); err != nil {
		t.Fatalf("a panicking event-log hook must not fail the handoff: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".sprawl", "memory", "sessions", "*.md"))
	if len(matches) != 1 {
		t.Errorf("the summary file was not written (%d matches) — the event log destroyed the handoff", len(matches))
	}
}

// ---------------------------------------------------------------------------
// QUM-1294: the DSN must not reach the log through this file's two warns.
//
// WHY THE SEAM. These tests drive warnLedgerUnusable / warnHandoffPanic
// directly rather than going through store.Process, because Process is a
// sync.Once over package globals logging through slog.Default() and cannot be
// pinned. The logger parameter is the seam; a bytes.Buffer is the observer.
//
// WHAT IS ASSERTED, in pairs, and never a "[redacted]" marker — a marker check
// passes with the credential printed right next to the marker:
//
//   - ABSENCE of the specific secret substrings (user, host, database name);
//   - and, SEPARATELY, that the DIAGNOSIS SURVIVED. An absence-only assertion
//     is satisfied by printing nothing at all, which is exactly what QUM-1280's
//     QA caught with a silencing mutation.
//
// CONTROLS RUN AND RECORDED, per CLAUDE.md's requirement that every assertion
// be watched failing in the direction it guards:
//
//   - POSITIVE CONTROL for the absence half — red-first against the unredacted
//     seam. All four leak tests fired, e.g.
//     `the warn leaked "qum1294host.invalid"` out of
//     "cannot parse `postgres://qum1294user:xxxxx@qum1294host.invalid:5432/qum1294db?sslmode=bogusvalue`".
//   - SILENCING MUTATION for the survival half — both warn bodies replaced with
//     a discard. All four tests failed on "nothing was logged at all", which is
//     the failure an absence-only assertion would have passed.
//   - DROPPED-ATTR MUTATION, the narrower one — `log.Warn(msg)` with the error
//     attr removed. The keeps loop itself fired ("the diagnosis lost
//     \"sslmode is invalid\""), so the survival half is carried by the
//     assertions and not merely by the emptiness guard.
//   - NEGATIVE CONTROLS — the two ...LeavesA...Untouched tests, green BEFORE
//     the fix as well as after, so the probe is shown to discriminate.
//
// Every credential below is obviously synthetic. This repo is public.

// synthetic DSN components. Not a real user, host or database anywhere.
const (
	probeUser = "qum1294user"
	probeHost = "qum1294host.invalid"
	probeDB   = "qum1294db"
	// probePass is a CANARY, not a demonstrated-firing assertion, and it is
	// labelled as one rather than left to look like coverage. Measured on the
	// red run: pgx masks the password to `xxxxx` in parse errors and omits it
	// from connect errors entirely, so it appears in NONE of the subjects below
	// and the checks for it cannot fire in either direction today. They exist so
	// a future driver or wrapper that starts printing it fails here. Same
	// framing as internal/store/redact_test.go's probePassword.
	probePass = "not-a-real-password-PROBE"
)

// bufLogger returns a logger writing to buf, which is the observer for the print.
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

// parseClassError provokes the pgx PARSE error through the PRODUCTION wrapper.
//
// store.OpenProcessLedger is the real code path — it is exported and fully
// injectable, and it is NOT behind Process's sync.Once — so this is the actual
// error value the live sink receives, driver text and store wrapper alike,
// rather than a hand-written string that "looks like" one. A hand-written
// lookalike is the substitution that let QUM-1279 ship a green test over a
// broken scenario.
func parseClassError(t *testing.T) error {
	t.Helper()
	dsn := "postgres://" + probeUser + ":" + probePass + "@" + probeHost + ":5432/" + probeDB + "?sslmode=bogusvalue"
	_, err := store.OpenProcessLedger(context.Background(), store.ProcessDeps{
		SprawlRoot: t.TempDir(),
		LoadConfig: func(string) (*config.Config, error) { return &config.Config{EventLog: "true"}, nil },
		Getenv: func(k string) string {
			if k == store.EnvDSN {
				return dsn
			}
			return ""
		},
		UserConfigDir: func() (string, error) { return "", errors.New("no user config dir in this test") },
		Git: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no origin remote in this test")
		},
	})
	if err == nil {
		t.Fatal("expected OpenProcessLedger to reject sslmode=bogusvalue; with no error there is no DSN-bearing error to redact")
	}
	return err
}

// connectClassError provokes a real pgx CONNECT error.
//
// Deliberately a copy of internal/store/redact_test.go's realPgxConnectError:
// that helper is unexported and this sink lives in another package, and
// exporting a test helper across a package boundary for two cases is worse than
// twenty duplicated lines. LookupFunc is consumed by pgconn's own
// buildConnectOneConfigs, not a test-only branch, so the error object, its
// formatting and the pgx code path are all the production ones.
//
// WHAT THIS CASE IS WORTH, stated rather than implied: store.Open routes pgx
// connect failures into degraded mode and returns a NIL error (see
// internal/store/ledger.go), so on the code as it stands the connect class does
// not reach this sink — the parse class does. This is prospective hardening
// against that routing changing, and a check that the connect grammars (which
// different RedactSecrets patterns handle entirely) are covered at this print
// too. It is not live coverage and is not counted as such.
func connectClassError(t *testing.T, lookup func(context.Context, string) ([]string, error)) error {
	t.Helper()
	dsn := "postgres://" + probeUser + ":" + probePass + "@" + probeHost + ":1/" + probeDB + "?sslmode=disable&connect_timeout=1"
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig of a synthetic well-formed DSN failed: %v", err)
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

// assertWarnIsSafe is the paired assertion: no secret, and the diagnosis intact.
//
// keeps is non-empty in every caller on purpose. It is the half that a
// silencing mutation must break: without it, deleting the print entirely
// satisfies every leak check in this file.
func assertWarnIsSafe(t *testing.T, got string, leaks, keeps []string) {
	t.Helper()
	// The contract is enforced, not just commented: a caller passing nil keeps
	// silently degrades this to an absence-only check, which is the vacuity mode
	// the header warns about.
	if len(keeps) == 0 || len(leaks) == 0 {
		t.Fatal("assertWarnIsSafe needs both halves; an absence-only check is satisfied by printing nothing")
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("nothing was logged at all; the sink is silent, so the leak assertions below would pass vacuously")
	}
	for _, secret := range leaks {
		if strings.Contains(got, secret) {
			t.Errorf("the warn leaked %q; a DSN component reached the log\nfull line: %s", secret, got)
		}
	}
	for _, keep := range keeps {
		if !strings.Contains(got, keep) {
			t.Errorf("the diagnosis lost %q — redaction that deletes the answer is one somebody removes to fix\nfull line: %s", keep, got)
		}
	}
}

// TestWarnLedgerUnusable_RedactsARealPgxParseError is the live defect: a
// malformed DSN, where pgx constructs the error FROM the connection string and
// quotes the whole URL. pgx masks the password; the user, host and database name
// leak, and each is employer-internal detail in its own right.
func TestWarnLedgerUnusable_RedactsARealPgxParseError(t *testing.T) {
	err := parseClassError(t)

	var buf bytes.Buffer
	warnLedgerUnusable(bufLogger(&buf), err)

	assertWarnIsSafe(t, buf.String(),
		[]string{probeUser, probeHost, probeDB, probePass},
		// The message the operator needs, the driver's own reason, and the
		// remedy hint — all three must survive.
		[]string{"event log unusable", "handoff recorded to memory only", "sslmode is invalid", "check the value for typos"},
	)
}

// TestWarnLedgerUnusable_RedactsARealPgxConnectError covers the OTHER shape.
// This was found via the parse error; the connect error renders completely
// differently (keyword form `user= database=`, with the hostname in the
// resolver's or the dialler's own text) and different RedactSecrets patterns do
// the work, so covering one is not covering the other.
func TestWarnLedgerUnusable_RedactsARealPgxConnectError(t *testing.T) {
	t.Run("resolver failure", func(t *testing.T) {
		err := connectClassError(t, func(_ context.Context, name string) ([]string, error) {
			return nil, &net.DNSError{Err: "no such host", Name: name, Server: "127.0.0.53:53", IsNotFound: true}
		})

		var buf bytes.Buffer
		warnLedgerUnusable(bufLogger(&buf), err)

		assertWarnIsSafe(t, buf.String(),
			[]string{probeUser, probeHost, probeDB, probePass},
			// 127.0.0.53:53 is the resolver the failure itself narrated, not a
			// value the operator supplied, so it must SURVIVE — that is the
			// over-redaction half of the check.
			[]string{"event log unusable", "no such host", "127.0.0.53:53"},
		)
	})

	t.Run("dial failure", func(t *testing.T) {
		err := connectClassError(t, func(context.Context, string) ([]string, error) {
			return []string{"127.0.0.1"}, nil
		})

		var buf bytes.Buffer
		warnLedgerUnusable(bufLogger(&buf), err)

		assertWarnIsSafe(t, buf.String(),
			[]string{probeUser, probeHost, probeDB, probePass},
			[]string{"event log unusable", "127.0.0.1:1"},
		)
	})
}

// TestWarnHandoffPanic_RedactsAPgxErrorInThePanicValue covers the sibling warn
// in the same function. The recovered value can be any pgx error from under
// store.Process, so it carries the same DSN exposure.
func TestWarnHandoffPanic_RedactsAPgxErrorInThePanicValue(t *testing.T) {
	err := parseClassError(t)

	var buf bytes.Buffer
	warnHandoffPanic(bufLogger(&buf), err)

	assertWarnIsSafe(t, buf.String(),
		[]string{probeUser, probeHost, probeDB, probePass},
		[]string{"panicked", "the summary file is unaffected", "sslmode is invalid"},
	)
}

// TestWarnLedgerUnusable_LeavesABenignErrorUntouched is the NEGATIVE control,
// and it must be GREEN both before and after the fix. It discriminates "redacts
// DSNs" from "mangles every error": the subject is the real production
// no-DSN-configured HintError, whose text is clean prose, and the assertion is
// byte-for-byte identity with err.Error() rather than an absence check.
//
// It reads the attr out of a JSON record rather than substring-searching a text
// line. The first version of this control did the latter and was RED pre-fix —
// not because anything was mangled, but because HintError.Error() contains a
// newline that TextHandler escapes to a literal \n, so the compare could never
// match. A negative control that is red before the fix is measuring the wrong
// thing; recorded here rather than quietly corrected.
func TestWarnLedgerUnusable_LeavesABenignErrorUntouched(t *testing.T) {
	_, err := store.OpenProcessLedger(context.Background(), store.ProcessDeps{
		SprawlRoot: t.TempDir(),
		LoadConfig: func(string) (*config.Config, error) { return &config.Config{EventLog: "true"}, nil },
		Getenv:     func(string) string { return "" },
		// No DSN from either source: Open's "enabled but no DSN" HintError.
		UserConfigDir: func() (string, error) { return t.TempDir(), nil },
		Git: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no origin remote in this test")
		},
	})
	if err == nil {
		t.Fatal("expected the enabled-but-no-DSN error; without it there is no benign subject to control against")
	}

	var buf bytes.Buffer
	warnLedgerUnusable(slog.New(slog.NewJSONHandler(&buf, nil)), err)

	var rec struct {
		Msg   string `json:"msg"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal(buf.Bytes(), &rec); jerr != nil {
		t.Fatalf("the warn did not produce one JSON record (%v); output was %q", jerr, buf.String())
	}
	if rec.Error != err.Error() {
		t.Errorf("a DSN-free error was altered on its way to the log.\nwant verbatim: %q\ngot:           %q", err.Error(), rec.Error)
	}
	if rec.Msg != "event log unusable, handoff recorded to memory only" {
		t.Errorf("the message was altered: %q", rec.Msg)
	}
}

// TestWarnHandoffPanic_LeavesABenignPanicValueUntouched is the negative control
// for the sibling sink, so both warns are shown to discriminate rather than
// mangle. Green before the fix as well as after.
func TestWarnHandoffPanic_LeavesABenignPanicValueUntouched(t *testing.T) {
	var buf bytes.Buffer
	warnHandoffPanic(slog.New(slog.NewJSONHandler(&buf, nil)), "the event log exploded")

	var rec struct {
		Msg   string `json:"msg"`
		Panic string `json:"panic"`
	}
	if jerr := json.Unmarshal(buf.Bytes(), &rec); jerr != nil {
		t.Fatalf("the warn did not produce one JSON record (%v); output was %q", jerr, buf.String())
	}
	if rec.Panic != "the event log exploded" {
		t.Errorf("a DSN-free panic value was altered on its way to the log: %q", rec.Panic)
	}
	if rec.Msg != "recording the handoff event panicked; the summary file is unaffected" {
		t.Errorf("the message was altered: %q", rec.Msg)
	}
}

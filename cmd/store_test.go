package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/store"
)

// `sprawl store` is the operator's only window into the event log, and its
// primary consumer is an AGENT (/cli-ux-best-practices). Two properties dominate
// these tests:
//
//  1. It must NEVER print the DSN. It is a database credential, and this repo is
//     public — a pasted `store doctor` transcript in an issue would leak it.
//  2. It must never report a number it did not measure. "0 spill files" and "the
//     spill directory could not be read" are different facts, and a reader
//     reasons from a plausible zero.

func newTestStoreDeps(t *testing.T) (*storeDeps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errBuf bytes.Buffer
	return &storeDeps{
		SprawlRoot: t.TempDir(),
		OpenLedger: func(context.Context, string) (*store.Ledger, error) { return nil, nil },
		Stdout:     &out,
		Stderr:     &errBuf,
	}, &out, &errBuf
}

// TestStoreStatus_DisabledSaysSoAndSaysHowToEnable pins the default output.
//
// "disabled" alone leaves an agent with nowhere to go, which is exactly the
// round trip the next-action-hint convention exists to prevent.
func TestStoreStatus_DisabledSaysSoAndSaysHowToEnable(t *testing.T) {
	deps, out, _ := newTestStoreDeps(t)
	if err := runStoreStatus(context.Background(), deps); err != nil {
		t.Fatalf("runStoreStatus: %v", err)
	}
	got := out.String()
	if !strings.Contains(strings.ToLower(got), "disabled") {
		t.Errorf("status does not say the store is disabled:\n%s", got)
	}
	if !strings.Contains(got, "event_log.enabled") {
		t.Errorf("status does not name the config key that enables it, so a caller has to go looking:\n%s", got)
	}
}

// TestStoreStatus_NeverPrintsTheDSN is the public-repo hygiene assertion, run
// against every subcommand rather than one, because the leak only has to happen
// once.
func TestStoreStatus_NeverPrintsTheDSN(t *testing.T) {
	const secret = "postgres://user:sup3rsecret@db.internal.example:5432/sprawl"

	deps, out, errBuf := newTestStoreDeps(t)
	deps.OpenLedger = func(context.Context, string) (*store.Ledger, error) {
		// A failure carrying the DSN in its text is the realistic leak: pgx
		// errors quote the connection string.
		return nil, errors.New("failed to connect to `" + secret + "`: connection refused")
	}

	for name, run := range map[string]func(context.Context, *storeDeps) error{
		"status": runStoreStatus,
		"doctor": runStoreDoctor,
	} {
		out.Reset()
		errBuf.Reset()
		_ = run(context.Background(), deps)
		combined := out.String() + errBuf.String()
		for _, leak := range []string{"sup3rsecret", secret} {
			if strings.Contains(combined, leak) {
				t.Errorf("%s printed the DSN or its password; this repo is public and transcripts get pasted into issues:\n%s", name, combined)
			}
		}
	}
}

// TestStoreDoctor_ReportsDegradedRatherThanSilentlyLookingHealthy pins that an
// outage is visible.
//
// A degraded Ledger is still ENABLED, so a doctor that only printed
// enabled/disabled would report a host whose events are all spilling to disk as
// perfectly healthy.
func TestStoreDoctor_ReportsDegradedRatherThanSilentlyLookingHealthy(t *testing.T) {
	deps, out, _ := newTestStoreDeps(t)
	deps.OpenLedger = func(ctx context.Context, root string) (*store.Ledger, error) {
		return store.Open(ctx, store.LedgerConfig{
			Enabled:    true,
			DSN:        "postgres://nobody@127.0.0.1:1/nothing?sslmode=disable&connect_timeout=1",
			DSNSource:  store.EnvDSN,
			RemoteURL:  "https://example.invalid/repo",
			SprawlRoot: root,
		})
	}
	if err := runStoreDoctor(context.Background(), deps); err != nil {
		t.Fatalf("doctor must report a degraded store rather than failing: %v", err)
	}
	// Keyed on the CONNECTION line specifically, not on the word "degraded"
	// appearing anywhere in the output.
	//
	// The looser predicate was measured and rejected: with the connection line
	// mutated to "ok", a substring search for "degraded"/"unreachable" still
	// matched — the trailing `next:` hint contains the word "degraded", so the
	// assertion passed while the doctor reported a healthy connection over a
	// dead one. Exactly the axis-mismatch the repo's testing rules warn about:
	// the claim is about the connection verdict, so the predicate has to read
	// the connection verdict.
	var connLine string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "connection:") {
			connLine = strings.TrimSpace(line)
			break
		}
	}
	if connLine == "" {
		t.Fatalf("doctor printed no connection line at all, so there is nothing to assert on:\n%s", out.String())
	}
	lowered := strings.ToLower(connLine)
	if !strings.Contains(lowered, "degraded") && !strings.Contains(lowered, "unreachable") {
		t.Errorf("the connection verdict is %q; a host whose every event is spilling to disk must not report a healthy connection", connLine)
	}
	if !strings.Contains(out.String(), store.EnvDSN) {
		t.Errorf("doctor does not name where the DSN came from, which is the one thing that localises a misconfiguration:\n%s", out.String())
	}
}

// TestStoreDoctor_DistinguishesUnmeasuredFromZero pins the repo's
// diagnostic rule: a surface must never render a number it did not measure.
//
// With no spill directory on disk, "0 spilled events" is a plausible,
// reassuring value that a reader reasons from — and it is indistinguishable
// from a real measurement. The honest output says it was not measured.
func TestStoreDoctor_DistinguishesUnmeasuredFromZero(t *testing.T) {
	deps, out, _ := newTestStoreDeps(t)
	if err := runStoreDoctor(context.Background(), deps); err != nil {
		t.Fatalf("runStoreDoctor: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "0 spill") || strings.Contains(got, "spill files: 0") {
		t.Errorf("doctor rendered a spill count it never measured; `not measured` and `measured as zero` are different facts:\n%s", got)
	}
}

// TestStoreMigrate_RequiresADSNAndSaysWhere pins that migrate refuses rather
// than silently doing nothing when the store is not configured.
//
// Silently succeeding is the worst option: an operator runs `store migrate`, sees
// no error, and believes the schema exists.
func TestStoreMigrate_RequiresADSNAndSaysWhere(t *testing.T) {
	deps, _, _ := newTestStoreDeps(t)
	deps.ResolveDSN = func() (string, string, error) { return "", "", nil }

	err := runStoreMigrate(context.Background(), deps)
	if err == nil {
		t.Fatal("migrate with no DSN must fail; silently succeeding would let an operator believe the schema exists")
	}
	if !strings.Contains(err.Error(), store.EnvDSN) {
		t.Errorf("the error should name where to put the DSN; got: %v", err)
	}
}

// TestStoreStatus_PointsAtTheSetupGuide pins that the disabled path names the
// setup doc.
//
// The primary consumer of this CLI is an agent (/cli-ux-best-practices), which
// cannot browse docs/ to discover that a guide exists. "enable it with: sprawl
// config set …" tells you the switch but not where the DSN comes from, what the
// 0600 requirement is, or why `store migrate` is a separate privileged step — so
// without this the next action after enabling is a guess.
func TestStoreStatus_PointsAtTheSetupGuide(t *testing.T) {
	deps, out, _ := newTestStoreDeps(t)
	if err := runStoreStatus(context.Background(), deps); err != nil {
		t.Fatalf("runStoreStatus: %v", err)
	}
	if !strings.Contains(out.String(), "docs/event-log-setup.md") {
		t.Errorf("the disabled output does not name the setup guide, so an agent has to guess what comes after enabling:\n%s", out.String())
	}
}

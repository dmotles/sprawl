package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/store"
	"github.com/google/uuid"
)

// `sprawl store dispatch` (QUM-1250).
//
// These assert the REFUSALS and the DISCLOSURES, which is the whole testable
// surface of the command without a database: every path that reaches the loop
// needs a live Postgres, and that is covered by the store package's own
// integration suite and by the dispatch e2e rows.
//
// The refusals matter more than they look. Each one is a state in which the
// command would otherwise start, print nothing alarming, and consume events it
// cannot act on — a process that appears to be dispatching and is not.

func newDispatchTestDeps(t *testing.T) (*storeDeps, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	return &storeDeps{
		SprawlRoot: t.TempDir(),
		OpenLedger: func(context.Context, string) (*store.Ledger, error) { return nil, nil },
		Stdout:     &out,
		Stderr:     &out,
	}, &out
}

// AN UNSET SPRAWL_ROOT IS REFUSED, like every other command in cmd/.
//
// Not merely consistency: with it unset, config.Load("") resolves a relative
// path, finds nothing, and reports the store disabled — so the command would
// exit 0 claiming there is nothing to dispatch on a host where the log is live.
// The same defect `store status` shipped with and had to fix.
func TestStoreDispatch_RefusesAnUnsetSprawlRoot(t *testing.T) {
	deps, _ := newDispatchTestDeps(t)
	deps.SprawlRoot = ""

	err := runStoreDispatch(context.Background(), deps)
	if err == nil {
		t.Fatal("dispatch started with no SPRAWL_ROOT; it would report 'nothing to dispatch' on a host whose log is live")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("the refusal does not name SPRAWL_ROOT: %v", err)
	}
	if !strings.Contains(err.Error(), "next:") {
		t.Errorf("the refusal carries no next-action hint, and the primary consumer of this CLI is an agent: %v", err)
	}
}

// A DISABLED store is refused with the enable sequence.
//
// (nil, nil) from OpenLedger means disabled, and a dispatcher polling a disabled
// store would loop forever finding nothing — indistinguishable from a quiet
// project.
func TestStoreDispatch_RefusesADisabledStore(t *testing.T) {
	deps, _ := newDispatchTestDeps(t)

	err := runStoreDispatch(context.Background(), deps)
	if err == nil {
		t.Fatal("dispatch started against a disabled store; it would poll forever finding nothing")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("the refusal does not say the store is disabled: %v", err)
	}
	if !strings.Contains(err.Error(), "event_log.enabled") {
		t.Errorf("the refusal does not name the flag to set: %v", err)
	}
	if !strings.Contains(err.Error(), "docs/event-log-setup.md") {
		t.Errorf("the refusal does not point at the setup guide, so an operator's next step is a guess: %v", err)
	}
}

// A MISCONFIGURATION from OpenLedger propagates rather than being swallowed.
//
// (nil, err) means enabled-but-broken — a missing DSN, an unreadable config —
// and dispatch must surface it. Treating it as "disabled" would hide a
// configuration error behind a cheerful no-op.
func TestStoreDispatch_PropagatesAnOpenFailure(t *testing.T) {
	deps, _ := newDispatchTestDeps(t)
	boom := errors.New("no DSN is configured")
	deps.OpenLedger = func(context.Context, string) (*store.Ledger, error) { return nil, boom }

	err := runStoreDispatch(context.Background(), deps)
	if !errors.Is(err, boom) {
		t.Errorf("runStoreDispatch returned %v, want the open failure — an enabled-but-broken store must not read as disabled", err)
	}
}

// THE CONSUMER NAME IS SHARED ACROSS HOSTS, and that is asserted rather than
// left as a comment.
//
// Two hosts must COMPETE for an event, and the shared consumer name is what
// expresses competing: `event_claims` is keyed (event_id, consumer), so a
// per-host consumer name would give every host its own key and every host would
// act on every event. That is the exactly-once failure arriving through a naming
// decision, and it would be invisible on a single-host test.
func TestStoreDispatch_ConsumerNameIsHostIndependent(t *testing.T) {
	if strings.Contains(dispatchConsumer, "%") || strings.Contains(dispatchConsumer, "-host") {
		t.Errorf("dispatchConsumer = %q looks host-derived; a per-host consumer name gives every host its own claim key, so every host acts on every event", dispatchConsumer)
	}
	if dispatchConsumer == "" {
		t.Error("dispatchConsumer is empty; an empty consumer merges this consumer's claims with every other one")
	}
}

// The sweep interval is much longer than the dispatch poll.
//
// The dispatcher chases latency on newly appended events; the sweeper looks for
// things that have NOT happened for tens of minutes. Sweeping at the dispatch
// interval would run the whole candidate query hundreds of times per stall
// threshold to reach the same answer.
func TestStoreDispatch_SweepIntervalIsMuchLongerThanTheDispatchPoll(t *testing.T) {
	// 5s is the top of the issue's stated 2-5s dispatch poll band. Anything at
	// or below it means the sweeper is running at dispatch cadence.
	const dispatchPollCeiling = 5 * time.Second
	if dispatchSweepInterval <= dispatchPollCeiling {
		t.Errorf("the sweep interval is %v, which is dispatch-poll territory; the candidate query would run hundreds of times per stall threshold to reach the same answer", dispatchSweepInterval)
	}
	// And not so long that a stall is invisible for most of a working day.
	if dispatchSweepInterval > store.DefaultStallAfter {
		t.Errorf("the sweep interval is %v, longer than the %v stall threshold, so a stalled goal waits a whole extra interval before anyone looks", dispatchSweepInterval, store.DefaultStallAfter)
	}
}

// fallbackOwner returns the ROOT agent, and an EMPTY string when there is none.
//
// The empty case is the load-bearing one: it DISABLES reassignment rather than
// reassigning to "". An owner_notify addressed to nobody is a contract nothing
// can ever ack, which is strictly worse than leaving the original owner named and
// letting the sweeper re-deliver.
func TestFallbackOwner_IsTheRootAgentAndEmptyWhenThereIsNone(t *testing.T) {
	root := t.TempDir()
	if got := fallbackOwner(root); got != "" {
		t.Errorf("fallbackOwner on a root with no recorded root agent = %q, want empty — reassigning to \"\" creates a contract nothing can ack", got)
	}

	// Written through the same file state.ReadRootName reads, so the mapping is
	// asserted against the real on-disk contract rather than a stub. There is no
	// WriteRootName in internal/state — the file is written by rootinit — so the
	// path is spelled here, and if it ever moves this test goes red rather than
	// silently asserting nothing.
	if err := os.MkdirAll(filepath.Join(root, ".sprawl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sprawl", "root-name"), []byte("weave\n"), 0o644); err != nil {
		t.Fatalf("writing root-name: %v", err)
	}
	if got := fallbackOwner(root); got != "weave" {
		t.Errorf("fallbackOwner = %q, want weave", got)
	}
}

// The command is registered under `store`, with its flags.
//
// A subcommand that exists in source and is not wired to its parent is
// unreachable, and nothing else here would notice.
func TestStoreDispatch_IsRegisteredWithItsFlags(t *testing.T) {
	var found bool
	for _, c := range storeCmd.Commands() {
		if c.Name() == "dispatch" {
			found = true
			for _, flag := range []string{"host", "once", "no-sweeper"} {
				if c.Flags().Lookup(flag) == nil {
					t.Errorf("the dispatch command has no --%s flag", flag)
				}
			}
			if c.Long == "" {
				t.Error("the dispatch command has no long help; an agent reading --help gets no statement of what a standalone run cannot do")
			}
			// The limits have to be discoverable from --help, not only from
			// startup output: an agent deciding WHETHER to run this reads help
			// first.
			for _, must := range []string{"event_claims", "cursor", "inert"} {
				if !strings.Contains(c.Long, must) {
					t.Errorf("the long help does not mention %q, so a caller cannot tell from --help what this does or does not guarantee", must)
				}
			}
		}
	}
	if !found {
		t.Fatal("`store dispatch` is not registered under `store`, so it is unreachable")
	}
}

// THE DEFAULT HOST IDENTITY MUST BE UNIQUE PER MACHINE.
//
// The first version used store.ProvisionalProjectID — `"local:" + sprawlRoot`, a
// FILESYSTEM PATH. Two machines with the same checkout path (the normal case for
// a container image, and exactly the deployment this milestone exists to serve)
// reported the same host, which breaks two of the three things host identity is
// load-bearing for:
//
//	affinity  both machines match `affinity == d.host`, so a worktree-bound event
//	          is claimable where the worktree does not exist.
//	reconcile machine B reads machine A's intents as its own, finds no local
//	          trace, and past the grace period emits spawn_failed for an agent
//	          that is alive and well on A.
//
// Found in code review. Asserted as "contains the hostname AND the root" rather
// than by comparing to a literal, because the exact format is not the property —
// distinguishing two machines is.
func TestDefaultHostIdentity_IsNotJustTheCheckoutPath(t *testing.T) {
	deps, _ := newDispatchTestDeps(t)
	got := defaultHostIdentity(deps)

	if got == "" {
		t.Skip("os.Hostname() failed on this host, so there is no identity to check")
	}
	if got == "local:"+deps.SprawlRoot || !strings.Contains(got, deps.SprawlRoot) {
		t.Errorf("the default host identity is %q; it must distinguish two MACHINES sharing a checkout path, and it must still distinguish two checkouts on one machine", got)
	}
	name, err := os.Hostname()
	if err == nil && !strings.Contains(got, name) {
		t.Errorf("the default host identity %q does not contain the hostname, so two machines with the same checkout path report the same host — a data-loss configuration", got)
	}
	// Two different roots on this machine must still be two hosts: they have
	// different worktrees, so affinity has to tell them apart.
	other, _ := newDispatchTestDeps(t)
	if defaultHostIdentity(other) == got {
		t.Error("two different checkouts on one machine report the same host identity, so a worktree-bound event would be claimable in the wrong one")
	}
}

// THE DSN NEVER REACHES THE SWEEP AND RECONCILE FAILURE OUTPUT (QUM-1279).
//
// Both surfaces below print an error that originates in the database layer, and
// a pgx PARSE error quotes the whole DSN it was handed. What it does not do is
// leak the password: internal/store/redact.go records the measurement against
// the pinned pgx v5.10.0 — the password is masked as `xxxxxx` in parse errors
// and omitted from connect errors. So the synthetic password below is testing
// PROSPECTIVE hardening (a future driver, or another library handed a DSN),
// while the host and database name are a leak that CLAUDE.md forbids in a public
// repo. Read redact.go before restating what pgx leaks; the unmeasured version
// of that claim was already wrong once.
//
// THE ASSERTION IS ABSENCE OF THE SECRET, NOT PRESENCE OF A MARKER. A test that
// checks for "[redacted]" passes while the password sits next to the marker,
// which is the exact failure this issue was filed about. Each row also asserts
// the DIAGNOSIS SURVIVED, because "sweep failed" with the cause dropped would
// satisfy an absence-only test and is a useless error message — and a useless
// error message is one somebody deletes the redaction to fix.
//
// WHAT THIS DOES NOT COVER, stated so the name is not read as more than it is:
// the `DegradedError` wrap at store_dispatch.go returns its error for cobra to
// print, and the slog logger wired onto deps.Stderr receives store records
// carrying raw `"error", err` attributes. Both are unredacted DSN carriers on
// the same stream and are filed separately rather than fixed here.
//
// AND ONE SEAM IS NOT COVERED: that runStoreDispatch itself routes its
// reconcile failure through reportReconcileFailure(deps.Stderr, ...) rather than
// os.Stderr. Everything above that call needs a live Postgres, so it is reached
// only by the dispatch e2e rows.
const (
	probeDSNPassword = "sup3r-synthetic-not-a-real-password"
	// Covered in URL form here, but no longer ONLY in URL form: since QUM-1281
	// RedactSecrets also redacts keyword-form host/user/database values and a
	// hostname in the two error grammars pgx and net.DNSError actually emit. A
	// bare hostname in arbitrary prose remains an explicit non-goal — see the
	// non-goals list in internal/store/redact.go. The keyword and connect-error
	// shapes are covered at that level, in internal/store/redact_test.go, where
	// the subject can be a real pgx error rather than a fabricated one.
	probeDSNHost = "leak-probe.invalid"
	probeDSNURL  = "postgres://leakuser:" + probeDSNPassword + "@" + probeDSNHost + ":5432/sprawl?sslmode=require"
)

// leakyPGXError is shaped like a pgx parse error, which quotes the whole DSN.
func leakyPGXError() error {
	return errors.New("cannot parse `" + probeDSNURL + "`: invalid port")
}

// leakyLocalAgents fails its snapshot with a DSN-bearing error. Both Sweep and
// Reconcile read the snapshot first, so this reaches the failure print on both.
type leakyLocalAgents struct{}

func (leakyLocalAgents) Snapshot(context.Context) ([]store.LocalAgent, error) {
	return nil, leakyPGXError()
}
func (leakyLocalAgents) Reclaim(context.Context, string) error { return nil }

type stubSweepReader struct{}

func (stubSweepReader) OpenGoals(context.Context, uuid.UUID) ([]store.StalledCandidate, error) {
	return nil, nil
}

type stubIntentReader struct{}

func (stubIntentReader) OpenIntents(context.Context, uuid.UUID, string) ([]store.OpenIntent, error) {
	return nil, nil
}

func (stubIntentReader) FailedIntents(context.Context, uuid.UUID, string) ([]store.FailedIntent, error) {
	return nil, nil
}

type stubEmitter struct{}

func (stubEmitter) Emit(context.Context, store.EmitRequest) (uuid.UUID, error) {
	return uuid.Nil, nil
}

type stubInjector struct{}

func (stubInjector) Inject(context.Context, string, string) error { return nil }

func TestStoreDispatch_SweepAndReconcileFailuresDoNotPrintTheDSN(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		// run drives one failure surface and returns what it emitted on
		// stdout and on the failure stream, separately.
		run func(t *testing.T) (out, errOut string)
		// wants are fragments of the surviving diagnosis. They are NOT static
		// banner text: a print that dropped the error entirely, or reduced it
		// to "[redacted]", loses them — so they guard both vacuity and the
		// over-redaction that makes an error worth deleting the redaction for.
		wants []string
		// errOnly is text that must appear on the failure stream and must NOT
		// appear on stdout. It pins the ROUTING claim in reportSweep's comment
		// — "a failure on stdout is invisible to a caller that separates the
		// streams" — which the concatenated leak assertions cannot see. Empty
		// for the control row, whose surface is stdout by design.
		errOnly string
	}{
		{
			name: "sweep failure",
			run: func(t *testing.T) (string, string) {
				t.Helper()
				var out, errOut bytes.Buffer
				reportSweep(ctx, &out, &errOut, store.SweeperDeps{
					Goals:     stubSweepReader{},
					Local:     leakyLocalAgents{},
					Emitter:   stubEmitter{},
					Injector:  stubInjector{},
					ProjectID: uuid.New(),
					Host:      "probe-host",
				})
				return out.String(), errOut.String()
			},
			errOnly: "sweep failed",
			wants:   []string{"sweep failed", "cannot read local agent state", "invalid port"},
		},
		{
			name: "startup reconciliation failure",
			run: func(t *testing.T) (string, string) {
				t.Helper()
				var out, errOut bytes.Buffer
				err := reportReconcile(ctx, &out, store.ReconcileDeps{
					Intents:   stubIntentReader{},
					Local:     leakyLocalAgents{},
					Emitter:   stubEmitter{},
					ProjectID: uuid.New(),
					Host:      "probe-host",
				})
				if err == nil {
					t.Fatal("reportReconcile returned nil; the probe never reached the failure path")
				}
				reportReconcileFailure(&errOut, err)
				return out.String(), errOut.String()
			},
			errOnly: "startup reconciliation did not complete",
			wants: []string{
				"startup reconciliation did not complete",
				"cannot read local agent state",
				"invalid port",
				// The line that tells an operator the process did NOT die.
				// Losing it would turn a best-effort tidy-up into what looks
				// like a fatal startup failure.
				"continuing anyway",
				"next:",
			},
		},
		{
			// NEGATIVE CONTROL: a surface already routed through RedactError.
			// It must be quiet both before and after the fix, which is what
			// shows the probe discriminates rather than firing on everything.
			name: "control: doctor, already redacted",
			run: func(t *testing.T) (string, string) {
				t.Helper()
				deps, buf := newDispatchTestDeps(t)
				deps.OpenLedger = func(context.Context, string) (*store.Ledger, error) {
					return nil, leakyPGXError()
				}
				if err := runStoreDoctor(ctx, deps); err != nil {
					t.Fatalf("runStoreDoctor: %v", err)
				}
				return buf.String(), ""
			},
			wants: []string{"connection: FAILED", "invalid port"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut := tc.run(t)
			got := out + errOut
			if tc.errOnly != "" {
				if !strings.Contains(errOut, tc.errOnly) {
					t.Errorf("%q is not on the failure stream; got stderr:\n%s", tc.errOnly, errOut)
				}
				if strings.Contains(out, tc.errOnly) {
					t.Errorf("%q reached stdout, where a caller separating the streams cannot see it as a failure; got stdout:\n%s", tc.errOnly, out)
				}
			}
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Fatalf("output does not contain %q, so either the diagnosis was lost or the absence assertions below would pass vacuously; got:\n%s", want, got)
				}
			}
			for _, secret := range []string{probeDSNPassword, probeDSNHost, probeDSNURL} {
				if strings.Contains(got, secret) {
					t.Errorf("failure output leaked %q; full output:\n%s", secret, got)
				}
			}
		})
	}
}

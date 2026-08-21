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

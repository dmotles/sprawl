package dispatchadapt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/store"
)

// The dispatch layer's local seams.
//
// What these tests are really defending is a set of HONEST NEGATIVE ANSWERS. A
// standalone dispatch process cannot see turn state and cannot tear down a
// worktree, and every assertion here exists to stop a future edit turning one of
// those "I do not know" answers into a confident wrong one.

// ---------------------------------------------------------------------------
// DiskAgents
// ---------------------------------------------------------------------------

// TURN STATE IS REPORTED AS UNOBSERVED, ALWAYS, from a process reading disk.
//
// This is the most important assertion in the package. Turn state lives only in
// the supervisor's in-memory phase machine and has no on-disk representation —
// internal/state.AgentState carries no such field — so this view genuinely does
// not know. Reporting InTurn: false with InTurnObserved: TRUE would be a
// negative answer derived from an unavailable observation, and the sweeper would
// read it as "idle" and poke every working agent in the fleet.
func TestDiskAgents_ReportsTurnStateAsUnobserved(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "alice", Status: "active", Branch: "b", Worktree: "/wt/alice"})

	got := snapshot(t, root)
	if len(got) != 1 {
		t.Fatalf("%d agents, want 1", len(got))
	}
	if got[0].InTurnObserved {
		t.Error("a disk-backed view claims to have OBSERVED turn state; it cannot, and the sweeper would treat every working agent as idle and poke it")
	}
	if got[0].InTurn {
		t.Error("a disk-backed view reports InTurn true, which it has no way to know")
	}
}

// The fields the gates actually read are carried through.
//
// The negative control for the assertion above: a view that returned nothing, or
// zero-valued entries, would satisfy "turn state is unobserved" perfectly while
// making every other gate unevaluable — and an unevaluable gate skips, so the
// sweeper would look correct and do nothing forever.
func TestDiskAgents_CarriesTheFieldsTheGatesRead(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{
		Name: "alice", Status: state.StatusPaused,
		Branch: "dmotles/x", Worktree: "/wt/alice", SessionID: "sess-1",
	})

	got := snapshot(t, root)
	if len(got) != 1 {
		t.Fatalf("%d agents, want 1", len(got))
	}
	a := got[0]
	if a.Name != "alice" {
		t.Errorf("Name = %q", a.Name)
	}
	if a.Status != state.StatusPaused {
		t.Errorf("Status = %q, want %q — the operator-paused gate reads this", a.Status, state.StatusPaused)
	}
	if a.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1 — the owner-dead check reads this to tell a recoverable death from a permanent one", a.SessionID)
	}
	if a.Branch != "dmotles/x" || a.Worktree != "/wt/alice" {
		t.Errorf("branch/worktree not carried: %+v", a)
	}
}

// A host with no agents is an empty list, not an error.
func TestDiskAgents_EmptyRootIsNotAnError(t *testing.T) {
	got := snapshot(t, t.TempDir())
	if len(got) != 0 {
		t.Errorf("%d agents on an empty root, want 0", len(got))
	}
}

// A LISTING FAILURE IS AN ERROR, not an empty host.
//
// Both the reconciler and the sweeper refuse to act on an unreadable snapshot,
// and that refusal is only possible if this reports the failure. Degrading to
// "no agents" would make every open spawn intent look traceless and every
// sweeper gate unevaluable.
func TestDiskAgents_ListingFailureIsReported(t *testing.T) {
	d := &DiskAgents{
		SprawlRoot: t.TempDir(),
		List: func(string) ([]*state.AgentState, error) {
			return nil, errors.New("permission denied")
		},
	}
	if _, err := d.Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot succeeded on a failed listing; the reconciler would then read this host as empty and fail every open intent")
	}
}

// RECLAIM REFUSES by default, and says what to do instead.
//
// The single destructive operation in the dispatch layer, and a standalone
// process is the worst place to perform it: tearing down a worktree needs the git
// machinery and retire bookkeeping internal/agentops owns, and doing it by hand
// would leave a half-torn-down agent that nothing knows how to recover.
//
// The refusal is asserted rather than documented because a future edit that
// "implements the missing piece" with an os.RemoveAll would be green otherwise.
func TestDiskAgents_ReclaimRefusesWithoutAnExplicitRemover(t *testing.T) {
	d := &DiskAgents{SprawlRoot: t.TempDir()}

	err := d.Reclaim(context.Background(), "doomed")
	if err == nil {
		t.Fatal("Reclaim silently succeeded with no remover wired; a caller would believe a worktree was torn down")
	}
	if !strings.Contains(err.Error(), "doomed") {
		t.Errorf("the refusal does not name the agent: %v", err)
	}
	// A refusal with no remedy is a dead end for whoever reads it.
	if !strings.Contains(err.Error(), "retire") {
		t.Errorf("the refusal does not say what to do instead: %v", err)
	}
}

// POSITIVE CONTROL: with a remover wired, Reclaim delegates to it.
//
// Without this leg, a Reclaim that refused unconditionally would satisfy the test
// above and make the in-process wiring M3a adds impossible.
func TestDiskAgents_ReclaimDelegatesToAWiredRemover(t *testing.T) {
	var removed []string
	d := &DiskAgents{
		SprawlRoot: "/root",
		Remove: func(_ context.Context, root, name string) error {
			removed = append(removed, root+":"+name)
			return nil
		},
	}
	if err := d.Reclaim(context.Background(), "doomed"); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if len(removed) != 1 || removed[0] != "/root:doomed" {
		t.Errorf("remover saw %v, want [/root:doomed]", removed)
	}
}

func TestDiskAgents_SatisfiesLocalAgents(t *testing.T) {
	var _ store.LocalAgents = (*DiskAgents)(nil)
}

// ---------------------------------------------------------------------------
// QueueInjector
// ---------------------------------------------------------------------------

// AN INJECTION WRITES BOTH DURABLE ARTIFACTS, and against the real packages.
//
// The maildir envelope and the queue entry are what the recipient's own drain
// reads; either alone is useless. Deliberately exercised through the real
// messages and agentloop packages rather than fakes — this repo's convention,
// and here it is the point: the assertion is that the recipient's existing drain
// will FIND these, which a fake cannot establish.
func TestQueueInjector_WritesTheEnvelopeAndTheQueueEntry(t *testing.T) {
	root := t.TempDir()
	q := &QueueInjector{SprawlRoot: root}

	if err := q.Inject(context.Background(), "weave", "a result landed: event abc"); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	inbox, err := messages.Inbox(root, "weave")
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("%d messages in weave's maildir, want 1", len(inbox))
	}
	if !strings.Contains(inbox[0].Body, "event abc") {
		t.Errorf("the envelope body is %q, which does not carry the notification", inbox[0].Body)
	}

	pending, err := agentloop.ListPending(root, "weave")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("%d pending queue entries, want 1 — the recipient's drain reads pending/, so without an entry the message is never injected", len(pending))
	}
	if pending[0].ShortID != inbox[0].ShortID {
		t.Errorf("the queue entry references short id %q but the envelope is %q; the drain renders that id into the recipient's prompt as the thing to read, so a mismatch points at nothing",
			pending[0].ShortID, inbox[0].ShortID)
	}
}

// THE ENTRY IS ASYNC-CLASS, NEVER INTERRUPT.
//
// An interrupt-class entry preempts the recipient's turn. The sweeper exists to
// help an agent that is NOT working, and preempting one that is would be exactly
// the harm the in-turn gate prevents, arriving by a different route. A
// notification is not urgent either — the result already landed.
func TestQueueInjector_EnqueuesAsyncNeverInterrupt(t *testing.T) {
	root := t.TempDir()
	q := &QueueInjector{SprawlRoot: root}
	if err := q.Inject(context.Background(), "weave", "body"); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	pending, err := agentloop.ListPending(root, "weave")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("%d pending entries, want 1", len(pending))
	}
	if pending[0].Class != agentloop.ClassAsync {
		t.Errorf("entry class is %v, want %v — an interrupt-class coordination nudge preempts a turn, which is the harm the sweeper's in-turn gate exists to prevent",
			pending[0].Class, agentloop.ClassAsync)
	}
}

// The sender identifies the COORDINATION LAYER, not a peer agent.
//
// The recipient's prompt renders "From <sender>", so a notification that claimed
// to come from another agent would be a lie about provenance in the one place an
// agent reads to decide what to do next.
func TestQueueInjector_SenderIsTheDispatcherIdentity(t *testing.T) {
	root := t.TempDir()
	q := &QueueInjector{SprawlRoot: root}
	if err := q.Inject(context.Background(), "weave", "body"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	inbox, err := messages.Inbox(root, "weave")
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if inbox[0].From != DispatcherIdentity {
		t.Errorf("From = %q, want %q", inbox[0].From, DispatcherIdentity)
	}
}

// THE ENVELOPE IS WRITTEN BEFORE THE QUEUE ENTRY.
//
// The entry carries the envelope's short id, and the drain renders that id into
// the recipient's prompt as the thing to read. The reverse order enqueues an
// entry whose id resolves to nothing, so the recipient is told to read a message
// that does not exist.
func TestQueueInjector_EnvelopeIsWrittenBeforeTheQueueEntry(t *testing.T) {
	var order []string
	q := &QueueInjector{
		SprawlRoot: t.TempDir(),
		Send: func(_, _, _, _, _ string) (string, error) {
			order = append(order, "send")
			return "abc", nil
		},
		Enqueue: func(_, _ string, e agentloop.Entry) (agentloop.Entry, error) {
			order = append(order, "enqueue:"+e.ShortID)
			return e, nil
		},
	}
	if err := q.Inject(context.Background(), "weave", "body"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if len(order) != 2 || order[0] != "send" || order[1] != "enqueue:abc" {
		t.Errorf("order = %v, want [send enqueue:abc] — the queue entry must reference an envelope that already exists", order)
	}
}

// A FAILED ENVELOPE WRITE MEANS NO QUEUE ENTRY.
//
// An entry referencing a short id that was never written tells the recipient to
// read a message that does not exist, on every drain, forever.
func TestQueueInjector_NoEnvelopeMeansNoQueueEntry(t *testing.T) {
	enqueued := 0
	q := &QueueInjector{
		SprawlRoot: t.TempDir(),
		Send:       func(_, _, _, _, _ string) (string, error) { return "", errors.New("disk full") },
		Enqueue: func(_, _ string, e agentloop.Entry) (agentloop.Entry, error) {
			enqueued++
			return e, nil
		},
	}
	if err := q.Inject(context.Background(), "weave", "body"); err == nil {
		t.Fatal("Inject succeeded although the envelope could not be written")
	}
	if enqueued != 0 {
		t.Errorf("enqueued %d entries with no envelope behind them", enqueued)
	}
}

// An empty recipient is refused rather than written somewhere unexpected.
func TestQueueInjector_RefusesAnEmptyRecipient(t *testing.T) {
	q := &QueueInjector{SprawlRoot: t.TempDir()}
	if err := q.Inject(context.Background(), "", "body"); err == nil {
		t.Error("Inject accepted an empty recipient")
	}
}

func TestQueueInjector_SatisfiesInjector(t *testing.T) {
	var _ store.Injector = (*QueueInjector)(nil)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func snapshot(t *testing.T, root string) []store.LocalAgent {
	t.Helper()
	got, err := (&DiskAgents{SprawlRoot: root}).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return got
}

// seedAgent writes a real state file through the real state package — this
// repo's convention, and it means the field mapping is asserted against the
// actual on-disk shape rather than against a struct literal.
func seedAgent(t *testing.T, root string, a *state.AgentState) {
	t.Helper()
	if err := os.MkdirAll(state.AgentsDir(root), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := state.SaveAgent(root, a); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state.AgentsDir(root), a.Name+".json")); err != nil {
		t.Fatalf("state file was not written: %v", err)
	}
}

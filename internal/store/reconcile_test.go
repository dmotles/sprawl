package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Startup reconciliation (QUM-1250, AC4).
//
// Split out of spawn_test.go so reconcile.go has the sibling _test.go CLAUDE.md
// requires of every new file under internal/. The tests were always here — the
// point of the move is DISCOVERABILITY, which is what that rule is for: a reader
// looking for reconcile's coverage should not have to know it lives in the spawn
// file.

type reconcileFixture struct {
	trace   *trace
	intents *fakeIntents
	local   *fakeLocalAgents
	emitter *recordingEmitter
	now     time.Time
	deps    ReconcileDeps
}

func newReconcileFixture(t *testing.T) *reconcileFixture {
	t.Helper()
	tr := &trace{}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	f := &reconcileFixture{
		trace:   tr,
		intents: &fakeIntents{},
		local:   &fakeLocalAgents{trace: tr},
		emitter: &recordingEmitter{trace: tr},
		now:     now,
	}
	f.deps = ReconcileDeps{
		Intents:   f.intents,
		Local:     f.local,
		Emitter:   f.emitter,
		ProjectID: uuid.New(),
		Host:      "host-a",
		Grace:     10 * time.Minute,
		Now:       func() time.Time { return f.now },
	}
	return f
}

func (f *reconcileFixture) run(t *testing.T) ReconcileResult {
	t.Helper()
	res, err := Reconcile(context.Background(), f.deps)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

// AC4 leg 1: AN ORPHAN IS ADOPTED.
//
// The local agent exists and its intent is still open — the exact state a crash
// between the spawn and the commit event leaves. Adoption closes the intent and
// spawns NOTHING: the resource is already there.
func TestReconcile_OrphanMatchingAnOpenIntentIsAdopted(t *testing.T) {
	f := newReconcileFixture(t)
	intentID := uuid.New()
	f.intents.open = []OpenIntent{{
		EventID: intentID, AgentName: "alice", AgentType: "engineer",
		HostAffinity: "host-a", At: f.now.Add(-time.Hour),
	}}
	f.local.agents = []LocalAgent{{Name: "alice", Status: "active", Worktree: "/wt/alice"}}

	res := f.run(t)

	if res.Adopted != 1 {
		t.Errorf("adopted %d, want 1", res.Adopted)
	}
	commits := f.emitter.byName("spawn_committed")
	if len(commits) != 1 {
		t.Fatalf("emitted %d spawn_committed events, want 1 — the intent stays open and the sweeper will chase it forever", len(commits))
	}
	if commits[0].ClosesEventID == nil || *commits[0].ClosesEventID != intentID {
		t.Errorf("the adoption does not close the intent it adopted")
	}
	if got := f.local.reclaimedNames(); len(got) != 0 {
		t.Errorf("adoption reclaimed %v — it destroyed the very resource it was supposed to adopt", got)
	}
}

// AC4 leg 3: A TRACELESS INTENT PAST THE GRACE PERIOD GETS spawn_failed.
func TestReconcile_TracelessIntentPastGraceEmitsSpawnFailed(t *testing.T) {
	f := newReconcileFixture(t)
	intentID := uuid.New()
	f.intents.open = []OpenIntent{{
		EventID: intentID, AgentName: "ghost", AgentType: "engineer",
		HostAffinity: "host-a", At: f.now.Add(-time.Hour),
	}}
	// No local agent at all.

	res := f.run(t)

	if res.Failed != 1 {
		t.Errorf("failed %d, want 1", res.Failed)
	}
	failed := f.emitter.byName("spawn_failed")
	if len(failed) != 1 {
		t.Fatalf("emitted %d spawn_failed events, want 1", len(failed))
	}
	if failed[0].ClosesEventID == nil || *failed[0].ClosesEventID != intentID {
		t.Error("spawn_failed does not close the intent it reports on")
	}
}

// NEGATIVE CONTROL for the leg above: an intent INSIDE the grace window is left
// alone.
//
// Direction: a subject known clean, where the probe must stay quiet. Without it,
// a reconciler that failed every traceless intent immediately would satisfy the
// test above perfectly — and would then kill every spawn still in flight in
// another process, because "the worktree does not exist yet" is the normal state
// for the first seconds of a spawn.
func TestReconcile_TracelessIntentInsideGraceIsLeftAlone(t *testing.T) {
	f := newReconcileFixture(t)
	f.intents.open = []OpenIntent{{
		EventID: uuid.New(), AgentName: "starting-up", AgentType: "engineer",
		HostAffinity: "host-a", At: f.now.Add(-time.Minute), // grace is 10m
	}}

	res := f.run(t)

	if res.Failed != 0 {
		t.Errorf("failed %d intents inside the grace window, want 0 — this kills spawns that are still in progress", res.Failed)
	}
	if got := f.emitter.names(); len(got) != 0 {
		t.Errorf("emitted %v for an in-flight spawn", got)
	}
}

// AC4 leg 2: A STRAY IS RECLAIMED — where a stray is a local resource whose
// intent WE ALREADY DECLARED FAILED.
//
// That definition is narrower than "matching no intent", deliberately, and the
// reason is in reconcile.go: taken literally, the wider rule deletes every agent
// on the host, because every agent spawned through the legacy MCP path has no
// intent at all. This leg is the attributable case — we said the spawn failed, so
// a resource for it must not survive.
func TestReconcile_LocalResourceForAFailedIntentIsReclaimed(t *testing.T) {
	f := newReconcileFixture(t)
	f.intents.failed = []FailedIntent{{
		EventID: uuid.New(), AgentName: "doomed", HostAffinity: "host-a",
		Branch: "dmotles/doomed", Worktree: "/wt/doomed",
	}}
	f.local.agents = []LocalAgent{{Name: "doomed", Branch: "dmotles/doomed", Worktree: "/wt/doomed"}}

	res := f.run(t)

	if res.Reclaimed != 1 {
		t.Errorf("reclaimed %d, want 1", res.Reclaimed)
	}
	if got := f.local.reclaimedNames(); len(got) != 1 || got[0] != "doomed" {
		t.Errorf("reclaimed %v, want [doomed]", got)
	}
	if got := f.emitter.byName("stray_reclaimed"); len(got) != 1 {
		t.Errorf("emitted %d stray_reclaimed events, want 1 — a destructive GC with no log trace is unattributable", len(got))
	}
}

// A REUSED NAME DOES NOT LICENSE DESTROYING A HEALTHY AGENT.
//
// Found in code review, and it is the one destructive path in this layer aimed at
// a live agent. Agent names are REUSED constantly here — that is the whole point
// of the retire/respawn cycle — so a failed intent for "zone" and a local agent
// called "zone" may be years apart and unrelated:
//
//  1. a dispatched spawn of "zone" fails; spawn_failed closes the intent.
//  2. later, "zone" is created again — by the legacy MCP path, by a successful
//     second dispatch, or by a human. It is healthy and working.
//  3. reconcile matches on the name and tears down its worktree.
//
// The intent already carries the branch and worktree it was going to create, so
// the guard costs nothing: reclaim only what the intent actually named.
func TestReconcile_AReusedNameIsNotReclaimedWhenTheResourceDiffers(t *testing.T) {
	f := newReconcileFixture(t)
	f.intents.failed = []FailedIntent{{
		EventID: uuid.New(), AgentName: "zone", HostAffinity: "host-a",
		Branch: "dmotles/zone-first-attempt", Worktree: "/wt/zone-first-attempt",
	}}
	// A DIFFERENT resource that happens to share the name.
	f.local.agents = []LocalAgent{{
		Name: "zone", Branch: "dmotles/zone-later", Worktree: "/wt/zone-later",
	}}

	res := f.run(t)

	if got := f.local.reclaimedNames(); len(got) != 0 {
		t.Fatalf("reclaimed %v — a live agent that merely reuses a failed intent's NAME was destroyed", got)
	}
	if got := f.emitter.byName("stray_reclaimed"); len(got) != 0 {
		t.Errorf("recorded %v for a resource it did not reclaim", got)
	}
	if res.Unattributed != 1 {
		t.Errorf("Unattributed = %d, want 1 — a refusal nobody can see is indistinguishable from a reconciler that did not look", res.Unattributed)
	}
}

// AN INTENT THAT NAMED NEITHER A BRANCH NOR A WORKTREE RECLAIMS NOTHING.
//
// "I cannot identify the resource" is not a licence to delete it. Without this
// leg, an intent with an empty branch and worktree would match any local agent of
// that name — which is the name-only behaviour the guard exists to remove,
// reachable through a sparse payload.
func TestReconcile_AnUnidentifiableIntentReclaimsNothing(t *testing.T) {
	f := newReconcileFixture(t)
	f.intents.failed = []FailedIntent{{EventID: uuid.New(), AgentName: "vague", HostAffinity: "host-a"}}
	f.local.agents = []LocalAgent{{Name: "vague", Branch: "b", Worktree: "/wt/vague"}}

	f.run(t)

	if got := f.local.reclaimedNames(); len(got) != 0 {
		t.Errorf("reclaimed %v on the strength of a name alone", got)
	}
}

// THE REFUSAL, and it is the most important assertion in this file.
//
// A local agent matching NO intent is left completely alone. Every agent on every
// host today was spawned through the legacy MCP path and has no intent, and the
// M1b dispatcher ships unwired, so it has created none — which means the literal
// reading of "GC local resources matching none" would, on its first pass,
// reclaim THE ENTIRE FLEET.
//
// This is asserted rather than merely documented because it is a destructive
// operation: a comment saying "we don't do that" is not a check, and the diff
// that turns this into the literal rule would otherwise be green.
func TestReconcile_LocalAgentMatchingNoIntentIsNeverTouched(t *testing.T) {
	f := newReconcileFixture(t)
	// No intents whatsoever — the state of every host in existence today.
	f.local.agents = []LocalAgent{
		{Name: "legacy-one", Worktree: "/wt/legacy-one"},
		{Name: "legacy-two", Worktree: "/wt/legacy-two"},
		{Name: "weave", Worktree: "/wt/weave"},
	}

	res := f.run(t)

	if got := f.local.reclaimedNames(); len(got) != 0 {
		t.Fatalf("the reconciler reclaimed %v — these are agents nothing in the log claims, which on a real host is every agent that exists", got)
	}
	if res.Reclaimed != 0 {
		t.Errorf("reclaimed %d, want 0", res.Reclaimed)
	}
	if got := f.emitter.names(); len(got) != 0 {
		t.Errorf("emitted %v about agents it has no business touching", got)
	}
	// It should still SAY it saw them, so an operator can tell an intentional
	// refusal from a reconciler that never ran.
	if res.Unattributed != 3 {
		t.Errorf("Unattributed = %d, want 3 — a silent refusal is indistinguishable from a reconciler that saw nothing", res.Unattributed)
	}
}

// RECONCILE ASKS THE READER FOR ITS OWN HOST'S INTENTS.
//
// This test was originally written as "another host's intent is ignored", with a
// fake that filtered by host itself — so it passed while measuring THE FAKE. The
// control proved it: replacing `d.Host` with `""` in the call left it green,
// because the fake's own predicate rejected the foreign intent either way. That
// is the /testing-practices provenance failure in miniature — the assertion
// observed an artifact the test supplied.
//
// So the responsibility is split where it actually lives. FILTERING is the
// READER's job and is done in SQL (see openIntentsSQL); it is asserted for real
// against Postgres by
// TestSpawnPg_OpenIntentIsVisibleToTheReaderAndScopedByHost. What is assertable
// hermetically, and all that is, is that Reconcile passes its OWN host down —
// because a reconciler that asked with the wrong host, or with none, would
// reconcile intents belonging to its peers: emitting spawn_failed for agents that
// exist perfectly well elsewhere, closing contracts those hosts are still working
// under, and making their eventual spawn_committed dead-letter.
func TestReconcile_AsksTheReaderForItsOwnHostsIntents(t *testing.T) {
	f := newReconcileFixture(t)
	f.deps.Host = "host-a"
	f.run(t)

	openHosts, failedHosts := f.intents.askedFor()
	if len(openHosts) != 1 || openHosts[0] != "host-a" {
		t.Errorf("OpenIntents was asked for hosts %v, want [host-a]", openHosts)
	}
	if len(failedHosts) != 1 || failedHosts[0] != "host-a" {
		t.Errorf("FailedIntents was asked for hosts %v, want [host-a] — reclaiming is destructive, and asking with the wrong host would license destroying another host's resources", failedHosts)
	}
}

// A reclaim that FAILS is reported and does not get counted as done.
//
// Counting it would make the result say a resource was cleaned up while the
// worktree is still on disk, and the next pass would see the same stray and
// report the same success.
func TestReconcile_FailedReclaimIsReportedNotCounted(t *testing.T) {
	f := newReconcileFixture(t)
	f.intents.failed = []FailedIntent{{
		EventID: uuid.New(), AgentName: "stuck", HostAffinity: "host-a", Worktree: "/wt/stuck",
	}}
	f.local.agents = []LocalAgent{{Name: "stuck", Worktree: "/wt/stuck"}}
	f.local.reclaimErr = errors.New("worktree is busy")

	res, err := Reconcile(context.Background(), f.deps)
	if err == nil {
		t.Fatal("Reconcile reported success although a reclaim failed")
	}
	if res.Reclaimed != 0 {
		t.Errorf("Reclaimed = %d after a failed reclaim, want 0", res.Reclaimed)
	}
}

// stray_reclaimed is emitted only AFTER the resource is actually gone.
//
// The reverse order records a cleanup that may not have happened. The log is
// append-only, so that claim can never be retracted — an operator reading the log
// would believe a worktree was reclaimed while it is still on disk.
func TestReconcile_StrayIsRecordedOnlyAfterTheResourceIsGone(t *testing.T) {
	f := newReconcileFixture(t)
	f.intents.failed = []FailedIntent{{
		EventID: uuid.New(), AgentName: "doomed", HostAffinity: "host-a", Worktree: "/wt/doomed",
	}}
	f.local.agents = []LocalAgent{{Name: "doomed", Worktree: "/wt/doomed"}}

	f.run(t)

	got := f.trace.all()
	want := []string{"reclaim:doomed", "emit:stray_reclaimed"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("trace = %v, want %v — the log must not claim a reclaim that has not happened yet", got, want)
	}
}

// An unreadable local snapshot stops the pass rather than being read as "this
// host has no agents".
//
// That is the difference between doing nothing and reclaiming everything: with an
// empty snapshot, every open intent looks traceless and gets spawn_failed, and
// every failed intent looks already-cleaned.
func TestReconcile_UnreadableLocalStateStopsThePass(t *testing.T) {
	f := newReconcileFixture(t)
	f.local.err = errors.New("cannot read .sprawl/agents")
	f.intents.open = []OpenIntent{{
		EventID: uuid.New(), AgentName: "alice", AgentType: "engineer",
		HostAffinity: "host-a", At: f.now.Add(-time.Hour),
	}}

	if _, err := Reconcile(context.Background(), f.deps); err == nil {
		t.Fatal("Reconcile succeeded although it could not read local state; every open intent would look traceless and be failed")
	}
	if got := f.emitter.names(); len(got) != 0 {
		t.Errorf("emitted %v while blind to local state", got)
	}
}

// A reconcile with nothing to do reports zeroes and does not error.
func TestReconcile_NothingToDoIsANoOp(t *testing.T) {
	f := newReconcileFixture(t)
	res := f.run(t)
	if res.Adopted != 0 || res.Failed != 0 || res.Reclaimed != 0 || res.Unattributed != 0 {
		t.Errorf("Reconcile on an empty host reported %+v, want zeroes", res)
	}
}

// Reconcile refuses an incomplete configuration.
func TestReconcile_RefusesAnIncompleteConfiguration(t *testing.T) {
	cases := map[string]func(d *ReconcileDeps){
		"no host":    func(d *ReconcileDeps) { d.Host = "" },
		"no project": func(d *ReconcileDeps) { d.ProjectID = uuid.Nil },
		"no intents": func(d *ReconcileDeps) { d.Intents = nil },
		"no local":   func(d *ReconcileDeps) { d.Local = nil },
		"no emitter": func(d *ReconcileDeps) { d.Emitter = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newReconcileFixture(t)
			mutate(&f.deps)
			if _, err := Reconcile(context.Background(), f.deps); err == nil {
				t.Errorf("Reconcile accepted a configuration with %s", name)
			}
		})
	}
}

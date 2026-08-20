package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The SPAWN_INTENT write-ahead (Appendix B item 2) and startup reconciliation.
//
// WHY THE WRITE-AHEAD EXISTS AT ALL, since a reader who has not followed the
// chain will read it as bookkeeping: claims.go's lease takeover deliberately
// trades exactly-once for liveness, so a second handler CAN run for one event
// after a crashed host's lease expires. The intent is what makes that survivable
// — the second handler finds a record that says "somebody was about to create
// this agent" and reconciles against local reality, instead of creating a second
// worktree for the same agent.
//
// Which makes the ORDER the entire mechanism: intent, THEN the local resource.
// Reversed, the crash window moves to exactly the wrong place — a worktree exists
// that no event mentions, so nothing can ever attribute it, and the reconciler is
// structurally unable to tell it from an agent a human spawned.

// ---------------------------------------------------------------------------
// Doubles
// ---------------------------------------------------------------------------

// recordingSpawner records spawn requests and shares an ordered trace with the
// appender double, so "intent before spawn" is assertable as an ORDER rather
// than inferred from an outcome.
type recordingSpawner struct {
	mu       sync.Mutex
	requests []SpawnRequest
	trace    *trace
	err      error
}

func (s *recordingSpawner) Spawn(_ context.Context, req SpawnRequest) error {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	if s.trace != nil {
		s.trace.add("spawn:" + req.AgentName)
	}
	err := s.err
	s.mu.Unlock()
	return err
}

func (s *recordingSpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *recordingSpawner) last() SpawnRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return SpawnRequest{}
	}
	return s.requests[len(s.requests)-1]
}

// recordingEmitter captures appended events in order.
type recordingEmitter struct {
	mu     sync.Mutex
	events []EmitRequest
	trace  *trace
	err    error
	// ids assigned to each emit, so a test can reference an appended event.
	ids []uuid.UUID
}

func (e *recordingEmitter) Emit(_ context.Context, req EmitRequest) (uuid.UUID, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.trace != nil {
		e.trace.add("emit:" + req.TypeName)
	}
	if e.err != nil {
		return uuid.Nil, e.err
	}
	id := req.EventID
	if id == uuid.Nil {
		id = uuid.New()
	}
	req.EventID = id
	e.events = append(e.events, req)
	e.ids = append(e.ids, id)
	return id, nil
}

func (e *recordingEmitter) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.events {
		out = append(out, ev.TypeName)
	}
	return out
}

func (e *recordingEmitter) byName(name string) []EmitRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []EmitRequest
	for _, ev := range e.events {
		if ev.TypeName == name {
			out = append(out, ev)
		}
	}
	return out
}

// fakeLocalAgents is this host's view of its own agents.
type fakeLocalAgents struct {
	mu         sync.Mutex
	agents     []LocalAgent
	reclaimed  []string
	trace      *trace
	err        error
	reclaimErr error
}

func (l *fakeLocalAgents) Snapshot(context.Context) ([]LocalAgent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	return append([]LocalAgent(nil), l.agents...), nil
}

func (l *fakeLocalAgents) Reclaim(_ context.Context, name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.trace != nil {
		l.trace.add("reclaim:" + name)
	}
	if l.reclaimErr != nil {
		return l.reclaimErr
	}
	l.reclaimed = append(l.reclaimed, name)
	return nil
}

func (l *fakeLocalAgents) reclaimedNames() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.reclaimed...)
}

// fakeIntents is the intent reader.
//
// IT DELIBERATELY DOES NOT FILTER BY HOST. An earlier version did, and the
// consequence was a test that passed while measuring the fake rather than the
// reconciler: "another host's intent is ignored" stayed green with the host
// argument replaced by "", because the fake rejected the foreign intent either
// way. Filtering belongs to the READER (it is a SQL predicate, see
// openIntentsSQL) and is asserted against a real database. What this fake
// records instead is WHICH HOST IT WAS ASKED FOR, which is the part the
// reconciler is actually responsible for.
type fakeIntents struct {
	mu          sync.Mutex
	open        []OpenIntent
	failed      []FailedIntent
	err         error
	openHosts   []string
	failedHosts []string
}

func (i *fakeIntents) OpenIntents(_ context.Context, _ uuid.UUID, host string) ([]OpenIntent, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.openHosts = append(i.openHosts, host)
	if i.err != nil {
		return nil, i.err
	}
	return append([]OpenIntent(nil), i.open...), nil
}

func (i *fakeIntents) FailedIntents(_ context.Context, _ uuid.UUID, host string) ([]FailedIntent, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.failedHosts = append(i.failedHosts, host)
	if i.err != nil {
		return nil, i.err
	}
	return append([]FailedIntent(nil), i.failed...), nil
}

func (i *fakeIntents) askedFor() (open, failed []string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.openHosts...), append([]string(nil), i.failedHosts...)
}

// ---------------------------------------------------------------------------
// The write-ahead
// ---------------------------------------------------------------------------

type spawnFixture struct {
	trace   *trace
	spawner *recordingSpawner
	emitter *recordingEmitter
	handler *SpawnHandler
}

func newSpawnFixture(t *testing.T) *spawnFixture {
	t.Helper()
	tr := &trace{}
	sp := &recordingSpawner{trace: tr}
	em := &recordingEmitter{trace: tr}
	h, err := NewSpawnHandler(SpawnHandlerDeps{
		Emitter: em,
		Spawner: sp,
		Host:    "host-a",
	})
	if err != nil {
		t.Fatalf("NewSpawnHandler: %v", err)
	}
	return &spawnFixture{trace: tr, spawner: sp, emitter: em, handler: h}
}

func spawnRequestEvent(t *testing.T, payload string) DispatchedEvent {
	t.Helper()
	return DispatchedEvent{
		Seq: 1, ID: uuid.New(),
		SchemaID:   SeedID("spawn_requested", 1),
		SchemaName: "spawn_requested", SchemaVersion: 1,
		WorkflowInstanceID: uuid.New(),
		Payload:            json.RawMessage(payload),
	}
}

const goodRequest = `{"agent_name":"alice","agent_type":"engineer","family":"engineering","branch":"dmotles/x"}`

// THE INTENT IS APPENDED BEFORE THE SPAWN, AND THE COMMIT AFTER IT.
//
// Asserted as an ORDER on a shared trace, not as an outcome, because every
// ordering of these three steps produces the same final state on a run that does
// not crash. Only the order distinguishes them, and only on the run that does.
func TestSpawnHandler_IntentIsAppendedBeforeTheSpawnAndCommittedAfter(t *testing.T) {
	f := newSpawnFixture(t)

	if err := f.handler.Handle(context.Background(), spawnRequestEvent(t, goodRequest)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got := f.trace.all()
	want := []string{"emit:spawn_intent", "spawn:alice", "emit:spawn_committed"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("trace = %v, want %v — the intent must exist in the log BEFORE any local resource does, or a crash leaves a worktree nothing can attribute", got, want)
	}
}

// A spawn that FAILS closes its intent with spawn_failed, and the contract does
// not stay open.
//
// An intent left open by a failed spawn is worse than no intent: the reconciler
// would see it forever, and after the grace period would emit a SECOND
// spawn_failed for it — except the first close already removed the contract, so
// the second append hits ErrNoOpenContract and dead-letters. Closing here is what
// keeps the log's open/close accounting honest.
func TestSpawnHandler_FailedSpawnClosesTheIntentWithSpawnFailed(t *testing.T) {
	f := newSpawnFixture(t)
	f.spawner.err = errors.New("worktree already exists")

	err := f.handler.Handle(context.Background(), spawnRequestEvent(t, goodRequest))
	if err == nil {
		t.Fatal("Handle reported success although the spawn failed; the dispatcher would advance past a spawn that never happened")
	}

	got := f.emitter.names()
	want := []string{"spawn_intent", "spawn_failed"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("emitted %v, want %v", got, want)
	}
	failed := f.emitter.byName("spawn_failed")
	if len(failed) != 1 {
		t.Fatalf("emitted %d spawn_failed events", len(failed))
	}
	if failed[0].ClosesEventID == nil {
		t.Error("spawn_failed does not close the intent, so the contract stays open forever")
	}
	payload, _ := json.Marshal(failed[0].Payload)
	if !strings.Contains(string(payload), "worktree already exists") {
		t.Errorf("spawn_failed does not carry the reason, so nobody can tell why: %s", payload)
	}
}

// spawn_committed CLOSES the intent, referencing it by id.
func TestSpawnHandler_CommittedClosesTheIntentItOpened(t *testing.T) {
	f := newSpawnFixture(t)
	if err := f.handler.Handle(context.Background(), spawnRequestEvent(t, goodRequest)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	intents := f.emitter.byName("spawn_intent")
	commits := f.emitter.byName("spawn_committed")
	if len(intents) != 1 || len(commits) != 1 {
		t.Fatalf("emitted %d intents and %d commits, want 1 each", len(intents), len(commits))
	}
	if commits[0].ClosesEventID == nil {
		t.Fatal("spawn_committed closes nothing, so every successful spawn leaves an open contract behind")
	}
	if *commits[0].ClosesEventID != intents[0].EventID {
		t.Errorf("spawn_committed closes %s, want the intent %s", *commits[0].ClosesEventID, intents[0].EventID)
	}
}

// IF THE INTENT CANNOT BE APPENDED, THE SPAWN DOES NOT HAPPEN.
//
// The whole write-ahead is void otherwise: a worktree would exist with no
// possible attribution, which is precisely the state the reconciler cannot act on
// and must not guess about. Failing loudly here is also consistent with the
// degraded-mode rule — spawn_intent is a contract event, so it is not spillable,
// so an unreachable database means no new coordination starts.
func TestSpawnHandler_NoIntentMeansNoSpawn(t *testing.T) {
	f := newSpawnFixture(t)
	f.emitter.err = errors.New("event log unreachable")

	if err := f.handler.Handle(context.Background(), spawnRequestEvent(t, goodRequest)); err == nil {
		t.Fatal("Handle succeeded although the intent could not be recorded")
	}
	if got := f.spawner.count(); got != 0 {
		t.Errorf("the spawner ran %d times with no intent recorded; the resulting worktree would be unattributable forever", got)
	}
}

// The intent carries this host as its affinity.
//
// Without it, a worktree-bound follow-up could be claimed by a host that does not
// have the worktree — which is the failure host affinity exists to prevent, and
// the intent is where the affinity has to originate because the spawn is what
// creates the host-bound resource.
func TestSpawnHandler_IntentCarriesThisHostAsItsAffinity(t *testing.T) {
	f := newSpawnFixture(t)
	if err := f.handler.Handle(context.Background(), spawnRequestEvent(t, goodRequest)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	intents := f.emitter.byName("spawn_intent")
	if len(intents) != 1 {
		t.Fatalf("emitted %d intents", len(intents))
	}
	payload, _ := json.Marshal(intents[0].Payload)
	if !strings.Contains(string(payload), `"host_affinity":"host-a"`) {
		t.Errorf("the intent does not name this host, so its worktree-bound follow-ups are claimable anywhere: %s", payload)
	}
}

// The request's own fields reach the spawner.
//
// The negative control for the ordering assertions: a handler that emitted the
// right events in the right order while spawning a differently-named agent would
// satisfy every other test in this file.
func TestSpawnHandler_RequestFieldsReachTheSpawner(t *testing.T) {
	f := newSpawnFixture(t)
	if err := f.handler.Handle(context.Background(),
		spawnRequestEvent(t, `{"agent_name":"bob","agent_type":"qa","family":"qa","parent":"weave","branch":"dmotles/y","subagent":true}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := f.spawner.last()
	if got.AgentName != "bob" || got.AgentType != "qa" || got.Family != "qa" ||
		got.Parent != "weave" || got.Branch != "dmotles/y" || !got.Subagent {
		t.Errorf("the spawner received %+v, which does not match the request", got)
	}
}

// A request missing the fields a spawn cannot proceed without is refused BEFORE
// an intent is written.
//
// An intent for an unnamed agent is unreconcilable by construction: the
// reconciler matches intents to local agents by name, so an intent with no name
// matches nothing, never adopts, and after the grace period emits a spawn_failed
// nobody can act on. Refusing before the write keeps that out of the log
// permanently — the log is append-only, so a bad intent cannot be cleaned up.
func TestSpawnHandler_RefusesAnIncompleteRequestBeforeWritingAnything(t *testing.T) {
	for _, payload := range []string{
		`{}`,
		`{"agent_type":"engineer"}`,
		`{"agent_name":"alice"}`,
		`{"agent_name":"","agent_type":"engineer"}`,
	} {
		f := newSpawnFixture(t)
		if err := f.handler.Handle(context.Background(), spawnRequestEvent(t, payload)); err == nil {
			t.Errorf("Handle accepted the request %s", payload)
		}
		if got := f.emitter.names(); len(got) != 0 {
			t.Errorf("request %s wrote %v to the append-only log before being refused", payload, got)
		}
		if got := f.spawner.count(); got != 0 {
			t.Errorf("request %s reached the spawner", payload)
		}
	}
}

// ---------------------------------------------------------------------------
// Reconciliation (AC4)
// ---------------------------------------------------------------------------

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
	}}
	f.local.agents = []LocalAgent{{Name: "doomed", Worktree: "/wt/doomed"}}

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
	f.intents.failed = []FailedIntent{{EventID: uuid.New(), AgentName: "stuck", HostAffinity: "host-a"}}
	f.local.agents = []LocalAgent{{Name: "stuck"}}
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
	f.intents.failed = []FailedIntent{{EventID: uuid.New(), AgentName: "doomed", HostAffinity: "host-a"}}
	f.local.agents = []LocalAgent{{Name: "doomed"}}

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

// ---------------------------------------------------------------------------
// Seeds
// ---------------------------------------------------------------------------

// The new seed types are shaped the way the contract requires.
//
// A build-time check, and the only kind that can fire while nothing emits these
// types yet — which is exactly the gap that let M1a ship agent_spawned marked
// spillable.
func TestSpawnSeeds_ContractShapes(t *testing.T) {
	reg := testRegistry(t)
	cases := []struct {
		name      string
		opens     bool
		closes    string
		spillable bool
	}{
		{"spawn_requested", false, "", false},
		{"spawn_intent", true, "", false},
		{"spawn_committed", false, "spawn_intent", false},
		{"spawn_failed", false, "spawn_intent", false},
		{"stray_reclaimed", false, "", false},
	}
	for _, tc := range cases {
		s, ok := reg.ByName(tc.name, 1)
		if !ok {
			t.Errorf("%s@1 is missing from the seed registry", tc.name)
			continue
		}
		if s.Opens != tc.opens {
			t.Errorf("%s@1 opens=%v, want %v", tc.name, s.Opens, tc.opens)
		}
		if s.Closes != tc.closes {
			t.Errorf("%s@1 closes=%q, want %q", tc.name, s.Closes, tc.closes)
		}
		if s.Spillable != tc.spillable {
			t.Errorf("%s@1 spillable=%v, want %v — a contract event recorded only in a local spill file is invisible to every other host", tc.name, s.Spillable, tc.spillable)
		}
	}
}

// Both closers really do close the same opener, which is what lets one intent be
// resolved either way.
func TestSpawnSeeds_CommittedAndFailedCloseTheSameOpener(t *testing.T) {
	reg := testRegistry(t)
	committed, ok1 := reg.ByName("spawn_committed", 1)
	failed, ok2 := reg.ByName("spawn_failed", 1)
	if !ok1 || !ok2 {
		t.Fatal("spawn_committed@1 or spawn_failed@1 is missing")
	}
	if committed.Closes != failed.Closes {
		t.Errorf("spawn_committed closes %q and spawn_failed closes %q; an intent must be resolvable by either", committed.Closes, failed.Closes)
	}
}

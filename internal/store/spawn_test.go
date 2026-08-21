package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
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
	// onEmit, when set, is called after a successful record — the seam a fixture
	// uses to mirror the side effects an append has inside its transaction (a
	// contract opening, say).
	onEmit func(req EmitRequest, id uuid.UUID)
	// recorded is every id this fake has accepted, so a repeat is refused.
	recorded map[uuid.UUID]bool
	// uniqueViolationOn makes Emit refuse these event ids the way Postgres
	// refuses a duplicate primary key. Needed because the handlers now rely on
	// `events.id UNIQUE` as their exclusion mechanism, so a fake that always
	// accepted an append could not exercise the already-recorded path at all.
	uniqueViolationOn map[uuid.UUID]bool
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
	// `events.id` IS UNIQUE, so re-recording an id this fake has already accepted
	// must be refused exactly as Postgres refuses it. Enforced here rather than
	// per test because the handlers now rely on that refusal as their exclusion
	// mechanism: a fake that accepted duplicates would let an idempotency defect
	// pass every test in the package.
	if e.uniqueViolationOn[id] || e.recorded[id] {
		return uuid.Nil, &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint \"events_id_key\""}
	}
	if e.recorded == nil {
		e.recorded = map[uuid.UUID]bool{}
	}
	e.recorded[id] = true
	req.EventID = id
	e.events = append(e.events, req)
	e.ids = append(e.ids, id)
	if e.onEmit != nil {
		e.onEmit(req, id)
	}
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

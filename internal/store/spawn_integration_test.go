//go:build store_pg

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The write-ahead and the reconciler against a real Postgres (QUM-1250, AC4).
//
// What only a real database can establish here: that open_contracts really does
// carry an unclosed spawn_intent and really does stop carrying it once closed,
// that the host predicate is applied by the query rather than hopefully in Go,
// and that AC1's no-duplicate-spawn guarantee holds for the actual spawn path
// rather than for a synthetic handler.

// spawnEnv extends the dispatch environment with the pieces the spawn path needs.
type spawnEnv struct {
	*dispatchEnv
	ledger  *Ledger
	emitter EventEmitter
	intents *PgIntentReader
}

func newSpawnEnv(t *testing.T) *spawnEnv {
	t.Helper()
	e := newDispatchEnv(t)
	// A Ledger built directly on the migrated pool: Open() would re-resolve
	// configuration, a DSN and git, none of which this test is about.
	l := &Ledger{
		enabled:   true,
		pool:      e.pool,
		registry:  e.registry,
		projectID: e.projectID,
		appender:  e.appender,
	}
	return &spawnEnv{
		dispatchEnv: e,
		ledger:      l,
		emitter:     LedgerEmitter{Ledger: l},
		intents:     &PgIntentReader{Pool: e.pool, Registry: e.registry},
	}
}

// requestSpawn appends a spawn_requested event and returns it as the dispatcher
// would hand it to a handler.
func (e *spawnEnv) requestSpawn(t *testing.T, payload string) DispatchedEvent {
	t.Helper()
	schema, ok := e.registry.ByName("spawn_requested", 1)
	if !ok {
		t.Fatal("spawn_requested@1 missing from the seed registry")
	}
	id := uuid.New()
	wf := uuid.New()
	if _, err := e.appender.Append(context.Background(), Event{
		ID: id, ProjectID: e.projectID, WorkflowInstanceID: wf,
		SchemaID: schema.ID, Payload: json.RawMessage(payload),
	}); err != nil {
		t.Fatalf("appending spawn_requested: %v", err)
	}
	return DispatchedEvent{
		Seq: 0, ID: id, ProjectID: e.projectID, WorkflowInstanceID: wf,
		SchemaID: schema.ID, SchemaName: "spawn_requested", SchemaVersion: 1,
		Payload: json.RawMessage(payload),
	}
}

// openContractCount counts open contracts of one event-type name.
func (e *spawnEnv) openContractCount(t *testing.T, typeName string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM open_contracts oc
		   JOIN events e ON e.id = oc.event_id
		  WHERE e.schema_id = ANY($1)`,
		schemaIDsFor(e.registry, typeName)).Scan(&n); err != nil {
		t.Fatalf("counting open %s contracts: %v", typeName, err)
	}
	return n
}

func (e *spawnEnv) eventCount(t *testing.T, typeName string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events WHERE schema_id = ANY($1)`,
		schemaIDsFor(e.registry, typeName)).Scan(&n); err != nil {
		t.Fatalf("counting %s events: %v", typeName, err)
	}
	return n
}

const spawnAlice = `{"agent_name":"alice","agent_type":"engineer","family":"engineering","branch":"dmotles/alice"}`

// ---------------------------------------------------------------------------
// The contract really opens and closes
// ---------------------------------------------------------------------------

// A successful spawn opens the intent contract and closes it, leaving nothing
// outstanding.
func TestSpawnPg_SuccessfulSpawnOpensAndClosesTheIntentContract(t *testing.T) {
	e := newSpawnEnv(t)
	h, err := NewSpawnHandler(SpawnHandlerDeps{Emitter: e.emitter, Spawner: okSpawner{}, Host: "host-a"})
	if err != nil {
		t.Fatalf("NewSpawnHandler: %v", err)
	}

	if err := h.Handle(context.Background(), e.requestSpawn(t, spawnAlice)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if got := e.eventCount(t, "spawn_intent"); got != 1 {
		t.Errorf("%d spawn_intent events, want 1", got)
	}
	if got := e.eventCount(t, "spawn_committed"); got != 1 {
		t.Errorf("%d spawn_committed events, want 1", got)
	}
	if got := e.openContractCount(t, "spawn_intent"); got != 0 {
		t.Errorf("%d intent contracts are still open after a successful spawn, want 0 — the sweeper would chase them forever", got)
	}
}

// A FAILED spawn also leaves nothing outstanding — via spawn_failed.
//
// This is the leg that proves both closers really close the same opener against
// a real open_contracts projection, which the hermetic test cannot: the appender
// deletes by closes_event_id, and if spawn_failed's `closes` name were wrong the
// append would be refused outright.
func TestSpawnPg_FailedSpawnClosesTheContractToo(t *testing.T) {
	e := newSpawnEnv(t)
	h, err := NewSpawnHandler(SpawnHandlerDeps{
		Emitter: e.emitter,
		Spawner: errSpawner{err: errors.New("branch already checked out")},
		Host:    "host-a",
	})
	if err != nil {
		t.Fatalf("NewSpawnHandler: %v", err)
	}

	if err := h.Handle(context.Background(), e.requestSpawn(t, spawnAlice)); err == nil {
		t.Fatal("Handle reported success although the spawn failed")
	}

	if got := e.eventCount(t, "spawn_failed"); got != 1 {
		t.Errorf("%d spawn_failed events, want 1", got)
	}
	if got := e.openContractCount(t, "spawn_intent"); got != 0 {
		t.Errorf("%d intent contracts are still open after a failed spawn, want 0", got)
	}
}

// An OPEN intent is genuinely visible as an open contract, and its host predicate
// is applied by the query.
func TestSpawnPg_OpenIntentIsVisibleToTheReaderAndScopedByHost(t *testing.T) {
	e := newSpawnEnv(t)
	ctx := context.Background()

	// A crash between the spawn and the commit: the intent is written and
	// nothing closes it. Emitted directly rather than through the handler,
	// because the handler cannot be made to fail in that window on purpose
	// without a seam that exists only for this test.
	intentID, err := e.emitter.Emit(ctx, EmitRequest{
		TypeName: "spawn_intent", TypeVersion: 1,
		WorkflowInstanceID: uuid.New(),
		Payload: map[string]any{
			"agent_name": "alice", "agent_type": "engineer", "host_affinity": "host-a",
		},
	})
	if err != nil {
		t.Fatalf("emitting an intent: %v", err)
	}

	mine, err := e.intents.OpenIntents(ctx, e.projectID, "host-a")
	if err != nil {
		t.Fatalf("OpenIntents(host-a): %v", err)
	}
	if len(mine) != 1 || mine[0].EventID != intentID || mine[0].AgentName != "alice" {
		t.Fatalf("OpenIntents(host-a) = %+v, want the one intent for alice", mine)
	}
	if mine[0].At.IsZero() {
		t.Error("the intent's timestamp is zero, so the grace-period comparison would treat every intent as ancient and fail it immediately")
	}

	theirs, err := e.intents.OpenIntents(ctx, e.projectID, "host-b")
	if err != nil {
		t.Fatalf("OpenIntents(host-b): %v", err)
	}
	if len(theirs) != 0 {
		t.Errorf("OpenIntents(host-b) returned %d of host-a's intents; host-b would fail contracts host-a is working under", len(theirs))
	}
}

// Once closed, an intent is no longer open — and it IS a failed intent.
//
// The two halves are the negative and positive controls for each other: a reader
// that returned everything would pass the first, and one that returned nothing
// would pass the second.
func TestSpawnPg_ClosedIntentLeavesOpenAndAppearsAsFailed(t *testing.T) {
	e := newSpawnEnv(t)
	ctx := context.Background()

	intentID, err := e.emitter.Emit(ctx, EmitRequest{
		TypeName: "spawn_intent", TypeVersion: 1,
		WorkflowInstanceID: uuid.New(),
		Payload:            map[string]any{"agent_name": "doomed", "agent_type": "engineer", "host_affinity": "host-a"},
	})
	if err != nil {
		t.Fatalf("emitting an intent: %v", err)
	}
	if _, err := e.emitter.Emit(ctx, EmitRequest{
		TypeName: "spawn_failed", TypeVersion: 1,
		WorkflowInstanceID: uuid.New(),
		ClosesEventID:      &intentID,
		Payload:            map[string]any{"agent_name": "doomed", "reason": "test", "host": "host-a"},
	}); err != nil {
		t.Fatalf("closing the intent: %v", err)
	}

	open, err := e.intents.OpenIntents(ctx, e.projectID, "host-a")
	if err != nil {
		t.Fatalf("OpenIntents: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("a closed intent is still reported open (%d); the reconciler would keep failing it and every append after the first would dead-letter", len(open))
	}

	failed, err := e.intents.FailedIntents(ctx, e.projectID, "host-a")
	if err != nil {
		t.Fatalf("FailedIntents: %v", err)
	}
	if len(failed) != 1 || failed[0].EventID != intentID || failed[0].AgentName != "doomed" {
		t.Errorf("FailedIntents = %+v, want the one failed intent for doomed — without it no stray is ever reclaimable", failed)
	}
}

// A COMMITTED intent is not a failed one.
//
// Without this, `FailedIntents` joining on any closer at all would license
// reclaiming the resources of every successfully-spawned agent — a destructive
// operation on healthy agents, which is the worst outcome available in this file.
func TestSpawnPg_ACommittedIntentIsNotReportedAsFailed(t *testing.T) {
	e := newSpawnEnv(t)
	ctx := context.Background()
	h, err := NewSpawnHandler(SpawnHandlerDeps{Emitter: e.emitter, Spawner: okSpawner{}, Host: "host-a"})
	if err != nil {
		t.Fatalf("NewSpawnHandler: %v", err)
	}
	if err := h.Handle(ctx, e.requestSpawn(t, spawnAlice)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	failed, err := e.intents.FailedIntents(ctx, e.projectID, "host-a")
	if err != nil {
		t.Fatalf("FailedIntents: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("a SUCCESSFUL spawn is reported as a failed intent (%+v); the reconciler would reclaim a healthy agent's worktree", failed)
	}
}

// ---------------------------------------------------------------------------
// AC4, end to end against a real log
// ---------------------------------------------------------------------------

// Adoption: the crash-between-spawn-and-commit state, resolved.
func TestSpawnPg_ReconcileAdoptsAnOrphanAndClosesItsContract(t *testing.T) {
	e := newSpawnEnv(t)
	ctx := context.Background()

	if _, err := e.emitter.Emit(ctx, EmitRequest{
		TypeName: "spawn_intent", TypeVersion: 1,
		WorkflowInstanceID: uuid.New(),
		Payload:            map[string]any{"agent_name": "alice", "agent_type": "engineer", "host_affinity": "host-a"},
	}); err != nil {
		t.Fatalf("emitting an intent: %v", err)
	}

	local := &fakeLocalAgents{agents: []LocalAgent{{Name: "alice", Worktree: "/wt/alice", Branch: "dmotles/alice"}}}
	res, err := Reconcile(ctx, ReconcileDeps{
		Intents: e.intents, Local: local, Emitter: e.emitter,
		ProjectID: e.projectID, Host: "host-a",
		Now: func() time.Time { return time.Now().Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Adopted != 1 {
		t.Errorf("adopted %d, want 1", res.Adopted)
	}
	if got := e.openContractCount(t, "spawn_intent"); got != 0 {
		t.Errorf("%d intent contracts still open after adoption, want 0", got)
	}
	if got := local.reclaimedNames(); len(got) != 0 {
		t.Fatalf("adoption RECLAIMED %v — it destroyed the resource it was adopting", got)
	}
}

// The traceless intent, and the stray, in one pass against a real log.
func TestSpawnPg_ReconcileFailsATracelessIntentThenReclaimsTheStray(t *testing.T) {
	e := newSpawnEnv(t)
	ctx := context.Background()

	if _, err := e.emitter.Emit(ctx, EmitRequest{
		TypeName: "spawn_intent", TypeVersion: 1,
		WorkflowInstanceID: uuid.New(),
		Payload:            map[string]any{"agent_name": "ghost", "agent_type": "engineer", "host_affinity": "host-a"},
	}); err != nil {
		t.Fatalf("emitting an intent: %v", err)
	}

	// Pass 1: no local trace, past grace -> spawn_failed.
	empty := &fakeLocalAgents{}
	res, err := Reconcile(ctx, ReconcileDeps{
		Intents: e.intents, Local: empty, Emitter: e.emitter,
		ProjectID: e.projectID, Host: "host-a",
		Now: func() time.Time { return time.Now().Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("Reconcile pass 1: %v", err)
	}
	if res.Failed != 1 {
		t.Fatalf("pass 1 failed %d intents, want 1", res.Failed)
	}
	if got := e.openContractCount(t, "spawn_intent"); got != 0 {
		t.Errorf("%d intent contracts still open, want 0", got)
	}

	// Pass 2: the resource turns up AFTER we declared the spawn failed. That is
	// the stray, and it is attributable, so it goes.
	withGhost := &fakeLocalAgents{agents: []LocalAgent{{Name: "ghost", Worktree: "/wt/ghost"}}}
	res2, err := Reconcile(ctx, ReconcileDeps{
		Intents: e.intents, Local: withGhost, Emitter: e.emitter,
		ProjectID: e.projectID, Host: "host-a",
		Now: func() time.Time { return time.Now().Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("Reconcile pass 2: %v", err)
	}
	if res2.Reclaimed != 1 {
		t.Errorf("pass 2 reclaimed %d, want 1", res2.Reclaimed)
	}
	if got := withGhost.reclaimedNames(); len(got) != 1 || got[0] != "ghost" {
		t.Errorf("reclaimed %v, want [ghost]", got)
	}
	if got := e.eventCount(t, "stray_reclaimed"); got != 1 {
		t.Errorf("%d stray_reclaimed events, want 1 — a destructive deletion with no log trace", got)
	}
}

// THE REFUSAL, against a real log: a host full of agents and an empty intent
// table reclaims NOTHING.
//
// This is the state of every host in the fleet today, and it is asserted here as
// well as hermetically because the destructive path runs against real data.
func TestSpawnPg_ReconcileTouchesNothingWhenNoIntentsExist(t *testing.T) {
	e := newSpawnEnv(t)
	local := &fakeLocalAgents{agents: []LocalAgent{
		{Name: "weave"}, {Name: "legacy-one"}, {Name: "legacy-two"},
	}}

	res, err := Reconcile(context.Background(), ReconcileDeps{
		Intents: e.intents, Local: local, Emitter: e.emitter,
		ProjectID: e.projectID, Host: "host-a",
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := local.reclaimedNames(); len(got) != 0 {
		t.Fatalf("reconcile reclaimed %v on a host whose agents no intent mentions — on a real host that is the entire fleet", got)
	}
	if res.Unattributed != 3 {
		t.Errorf("Unattributed = %d, want 3", res.Unattributed)
	}
}

// ---------------------------------------------------------------------------
// AC1 on the real spawn path
// ---------------------------------------------------------------------------

// THE SPAWN PATH ITSELF DOES NOT DOUBLE-SPAWN ACROSS A CRASH.
//
// AC1 stated against the actual handler rather than a synthetic one: kill the
// dispatcher after the claim and the spawn, restart it, and the agent is created
// once. The claim is what prevents the second call; the intent is what lets the
// reconciler tidy up the contract afterwards.
func TestSpawnPg_CrashAfterSpawnDoesNotSpawnTwice(t *testing.T) {
	e := newSpawnEnv(t)
	e.requestSpawn(t, spawnAlice)

	var spawns atomic.Int64
	spawner := countingSpawner{n: &spawns}
	handler, err := NewSpawnHandler(SpawnHandlerDeps{Emitter: e.emitter, Spawner: spawner, Host: "host-a"})
	if err != nil {
		t.Fatalf("NewSpawnHandler: %v", err)
	}

	deps := e.deps("host-a", handler)
	deps.Handlers = map[string]Handler{"spawn_requested": handler}
	deps.Cursor = failingCursor{} // the crash: the cursor never advances
	first, err := NewDispatcher(deps)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := first.Step(context.Background()); err == nil {
		t.Fatal("Step reported success although the cursor could not be written")
	}
	if got := spawns.Load(); got != 1 {
		t.Fatalf("the first pass spawned %d times, want 1", got)
	}

	// Restart: same claim table, cursor still at 0.
	deps2 := e.deps("host-a", handler)
	deps2.Handlers = map[string]Handler{"spawn_requested": handler}
	second, err := NewDispatcher(deps2)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := second.Step(context.Background()); err != nil {
		t.Fatalf("Step after the restart: %v", err)
	}

	if got := spawns.Load(); got != 1 {
		t.Errorf("the agent was spawned %d times across a crash and a restart, want exactly 1", got)
	}
}

// A DISABLED ledger refuses to report a recorded event, so the spawn does not
// happen.
//
// Ledger.Emit on a disabled ledger returns (0, nil) — records nothing, succeeds —
// which is right for telemetry and catastrophic here: the handler would take the
// nil error as "the intent is recorded", create a worktree, and leave a resource
// nothing in any log can attribute.
func TestSpawnPg_DisabledLedgerRefusesRatherThanSilentlyRecordingNothing(t *testing.T) {
	e := newSpawnEnv(t)
	var spawns atomic.Int64
	h, err := NewSpawnHandler(SpawnHandlerDeps{
		Emitter: LedgerEmitter{Ledger: nil}, // nil Ledger == disabled
		Spawner: countingSpawner{n: &spawns},
		Host:    "host-a",
	})
	if err != nil {
		t.Fatalf("NewSpawnHandler: %v", err)
	}

	err = h.Handle(context.Background(), e.requestSpawn(t, spawnAlice))
	if err == nil {
		t.Fatal("Handle succeeded against a disabled event log; the worktree it created would be unattributable forever")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("the refusal does not say the log is disabled: %v", err)
	}
	if got := spawns.Load(); got != 0 {
		t.Errorf("the spawner ran %d times with no intent recorded anywhere", got)
	}
}

// ---------------------------------------------------------------------------
// Spawner doubles
// ---------------------------------------------------------------------------

type okSpawner struct{}

func (okSpawner) Spawn(context.Context, SpawnRequest) error { return nil }

type errSpawner struct{ err error }

func (s errSpawner) Spawn(context.Context, SpawnRequest) error { return s.err }

type countingSpawner struct{ n *atomic.Int64 }

func (s countingSpawner) Spawn(context.Context, SpawnRequest) error {
	s.n.Add(1)
	return nil
}

//go:build store_pg

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Dispatcher behaviour against a real Postgres (QUM-1250, M1b).
//
// This file carries AC1 (crash between the side effect and the cursor advance
// must not repeat the work) and AC2 (two dispatchers, each event acted on exactly
// once), plus the structural proof that the cursor is only an optimisation: reset
// it and nothing is redone.
//
// Every dispatcher here is built with Doorbell nil — LISTEN disabled. That is
// AC7, and it is why these tests say something about correctness rather than
// about notification delivery.

// dispatchEnv is a migrated schema, a registered project, and an appender.
type dispatchEnv struct {
	pool      *pgxpool.Pool
	projectID uuid.UUID
	registry  *Registry
	appender  *Appender
	root      string
}

func newDispatchEnv(t *testing.T) *dispatchEnv {
	t.Helper()
	_, pool := newTestSchema(t)
	ctx := context.Background()

	projectID, _, err := ensureProject(ctx, pool, "https://example.invalid/dispatch-"+uuid.NewString()+".git")
	if err != nil {
		t.Fatalf("ensureProject: %v", err)
	}
	reg := testRegistry(t)
	return &dispatchEnv{
		pool:      pool,
		projectID: projectID,
		registry:  reg,
		appender:  NewAppender(AppenderDeps{Pool: pool, Registry: reg}),
		root:      t.TempDir(),
	}
}

// append writes one run_started event and returns its id.
func (e *dispatchEnv) append(t *testing.T, payload string) uuid.UUID {
	t.Helper()
	schema, ok := e.registry.ByName("run_started", 1)
	if !ok {
		t.Fatal("run_started@1 missing from the seed registry")
	}
	id := uuid.New()
	if _, err := e.appender.Append(context.Background(), Event{
		ID:                 id,
		ProjectID:          e.projectID,
		WorkflowInstanceID: uuid.New(),
		SchemaID:           schema.ID,
		Payload:            json.RawMessage(payload),
	}); err != nil {
		t.Fatalf("appending: %v", err)
	}
	return id
}

func (e *dispatchEnv) reader() *PgEventReader {
	return &PgEventReader{Pool: e.pool, Registry: e.registry}
}

// deps builds a dispatcher configuration against this environment.
func (e *dispatchEnv) deps(host string, h Handler) DispatcherDeps {
	return DispatcherDeps{
		Events:    e.reader(),
		Claims:    &PgClaimStore{Pool: e.pool},
		Cursor:    &FileCursorStore{Root: e.root},
		Registry:  e.registry,
		ProjectID: e.projectID,
		Host:      host,
		Consumer:  "dispatcher",
		Handlers:  map[string]Handler{"run_started": h},
		// Doorbell nil — AC7.
	}
}

const basePayload = `{"agent_name":"alice","agent_type":"engineer","session_id":"s1"}`

// ---------------------------------------------------------------------------
// The reader
// ---------------------------------------------------------------------------

// The scan returns events strictly after the cursor, in seq order, bounded by
// the limit, with the pinned schema resolved to a name.
func TestDispatchPg_ReaderIsOrderedBoundedAndStrictlyAfterTheCursor(t *testing.T) {
	e := newDispatchEnv(t)
	for i := 0; i < 5; i++ {
		e.append(t, basePayload)
	}

	got, err := e.reader().Read(context.Background(), e.projectID, 0, 3)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Read returned %d events with limit 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Errorf("events are not in ascending seq order: %d then %d", got[i-1].Seq, got[i].Seq)
		}
	}
	if got[0].SchemaName != "run_started" || got[0].SchemaVersion != 1 {
		t.Errorf("schema resolved to %s@%d, want run_started@1", got[0].SchemaName, got[0].SchemaVersion)
	}
	if got[0].ProjectID != e.projectID {
		t.Errorf("event carries project %s, want %s", got[0].ProjectID, e.projectID)
	}

	after, err := e.reader().Read(context.Background(), e.projectID, got[2].Seq, 10)
	if err != nil {
		t.Fatalf("Read after a cursor: %v", err)
	}
	if len(after) != 2 {
		t.Errorf("Read after seq %d returned %d events, want 2 — the scan is not STRICTLY after the cursor", got[2].Seq, len(after))
	}
	for _, ev := range after {
		if ev.Seq <= got[2].Seq {
			t.Errorf("Read after seq %d returned seq %d", got[2].Seq, ev.Seq)
		}
	}
}

// Another project's events are invisible.
//
// Cross-project dispatch would be a data-isolation failure, and the scan's
// project predicate is the only thing preventing it — a host dispatching another
// project's spawn intents would create agents in the wrong repository.
func TestDispatchPg_ReaderIsScopedToItsProject(t *testing.T) {
	e := newDispatchEnv(t)
	e.append(t, basePayload)

	other, _, err := ensureProject(context.Background(), e.pool, "https://example.invalid/other.git")
	if err != nil {
		t.Fatalf("ensureProject: %v", err)
	}
	schema, _ := e.registry.ByName("run_started", 1)
	if _, err := e.appender.Append(context.Background(), Event{
		ID: uuid.New(), ProjectID: other, WorkflowInstanceID: uuid.New(),
		SchemaID: schema.ID, Payload: json.RawMessage(basePayload),
	}); err != nil {
		t.Fatalf("appending to the other project: %v", err)
	}

	got, err := e.reader().Read(context.Background(), e.projectID, 0, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Read returned %d events, want 1 — the scan is leaking another project's events", len(got))
	}
	// Positive control that the other project's event really exists, so the
	// count above is a scoping result and not an empty database.
	otherGot, err := e.reader().Read(context.Background(), other, 0, 100)
	if err != nil {
		t.Fatalf("Read for the other project: %v", err)
	}
	if len(otherGot) != 1 {
		t.Fatalf("the other project has %d events, want 1 — the scoping assertion above measured nothing", len(otherGot))
	}
}

// An event carrying a schema_id this build does not know is REFUSED, not skipped.
//
// This is the rolling-upgrade case: a newer host publishes a type an older host
// has never heard of. Skipping it would be a silent drop, and — worse — the older
// host would advance its cursor past work it never did.
func TestDispatchPg_ReaderRefusesAnUnknownSchemaIDRatherThanSkippingIt(t *testing.T) {
	e := newDispatchEnv(t)
	ctx := context.Background()

	// Publish a schema this build's registry does not carry, then append an
	// event pinned to it. Done as the schema owner, which is what a newer host
	// running `store migrate` would effectively have done.
	futureID := uuid.New()
	if _, err := e.pool.Exec(ctx,
		`INSERT INTO event_type_schemas (id, name, version, json_schema, opens)
		 VALUES ($1, 'from_the_future', 1, '{"type":"object"}'::jsonb, false)`, futureID); err != nil {
		t.Fatalf("publishing a future schema: %v", err)
	}
	if _, err := e.pool.Exec(ctx,
		`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload)
		 VALUES ($1, $2, $3, $4, '{}'::jsonb)`,
		uuid.New(), e.projectID, uuid.New(), futureID); err != nil {
		t.Fatalf("appending a future event: %v", err)
	}

	got, err := e.reader().Read(ctx, e.projectID, 0, 100)
	if err == nil {
		t.Fatalf("Read returned %d events and no error over an event whose schema this build does not know; the cursor would advance past work nobody did", len(got))
	}
	if !strings.Contains(err.Error(), futureID.String()) {
		t.Errorf("the refusal does not name the unknown schema_id, so an operator cannot tell what to upgrade: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AC1 — crash between the side effect and the cursor advance
// ---------------------------------------------------------------------------

// A dispatcher that performs its side effect and then dies before recording the
// cursor DOES NOT REPEAT THE WORK when it restarts.
//
// This is AC1's mechanism at the dispatcher layer (commit 4 adds the
// spawn-specific form). The crash is simulated the honest way: the handler
// genuinely succeeds, and the cursor save genuinely fails, which is exactly the
// window a `kill -9` between those two lines produces. Then a FRESH dispatcher —
// new object, same claim table, same cursor file still at 0 — re-reads the event
// and must not act on it.
//
// The claim is what stops it. Nothing else can: the cursor says the event is
// unseen, the event is still in the log, and the handler has no memory.
func TestDispatchPg_CrashBetweenSideEffectAndCursorDoesNotRepeatTheWork(t *testing.T) {
	e := newDispatchEnv(t)
	e.append(t, basePayload)

	var calls atomic.Int64
	handler := HandlerFunc(func(context.Context, DispatchedEvent) error {
		calls.Add(1)
		return nil
	})

	// Pass 1: the handler succeeds, then the cursor write fails — the crash.
	deps := e.deps("host-a", handler)
	deps.Cursor = failingCursor{}
	first, err := NewDispatcher(deps)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := first.Step(context.Background()); err == nil {
		t.Fatal("Step reported success although the cursor could not be written; the crash this test simulates would be invisible")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("the side effect ran %d times in the first pass, want 1", got)
	}

	// Pass 2: a fresh dispatcher, a working cursor still at 0.
	second, err := NewDispatcher(e.deps("host-a", handler))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := second.Step(context.Background()); err != nil {
		t.Fatalf("Step after the restart: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("the side effect ran %d times across a crash and a restart, want exactly 1", got)
	}
}

// POSITIVE CONTROL for the test above, and AC1 asks for it by name: with the
// claim removed, the SAME scenario produces the duplicate.
//
// Direction: this runs the identical fixture against a claim store that grants
// every request — the defect IS present — so the probe MUST observe two calls.
// Without this leg, an implementation that never dispatched anything at all
// would satisfy "the side effect ran exactly once" by running it zero times on
// the second pass for the wrong reason.
func TestDispatchPg_WithoutTheClaimTheCrashDoesRepeatTheWork(t *testing.T) {
	e := newDispatchEnv(t)
	e.append(t, basePayload)

	var calls atomic.Int64
	handler := HandlerFunc(func(context.Context, DispatchedEvent) error {
		calls.Add(1)
		return nil
	})

	deps := e.deps("host-a", handler)
	deps.Claims = alwaysGrantClaims{}
	deps.Cursor = failingCursor{}
	first, err := NewDispatcher(deps)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := first.Step(context.Background()); err == nil {
		t.Fatal("Step reported success although the cursor could not be written")
	}

	deps2 := e.deps("host-a", handler)
	deps2.Claims = alwaysGrantClaims{}
	second, err := NewDispatcher(deps2)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := second.Step(context.Background()); err != nil {
		t.Fatalf("Step after the restart: %v", err)
	}

	if got := calls.Load(); got != 2 {
		t.Errorf("with the claim removed the side effect ran %d times, want 2 — this control is supposed to demonstrate the duplicate that claims prevent, and it did not", got)
	}
}

// failingCursor loads 0 and refuses to save: the crash window, made
// deterministic.
type failingCursor struct{}

func (failingCursor) Load(string) (int64, error) { return 0, nil }
func (failingCursor) Save(string, int64) error {
	return errors.New("simulated crash before the cursor was written")
}

// alwaysGrantClaims is the claim store with the guarantee removed — the subject
// for the positive control above, and nothing else.
type alwaysGrantClaims struct{}

func (alwaysGrantClaims) Claim(context.Context, uuid.UUID, string, string, time.Duration) (bool, error) {
	return true, nil
}

func (alwaysGrantClaims) Renew(context.Context, uuid.UUID, string, string, time.Duration) (bool, error) {
	return true, nil
}

func (alwaysGrantClaims) TakeoverExpired(context.Context, uuid.UUID, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (alwaysGrantClaims) Release(context.Context, uuid.UUID, string, string) error { return nil }

// ---------------------------------------------------------------------------
// The cursor is only an optimisation
// ---------------------------------------------------------------------------

// DELETE THE CURSOR, RE-RUN, AND NOTHING IS REDONE.
//
// The structural proof of the claim commit 1's header makes: the cursor is a
// scan-start optimisation and `event_claims` is the correctness mechanism. If
// this test fails, the cursor has quietly become load-bearing and AC1 is no
// longer satisfiable — a lost cursor would then mean repeated side effects.
//
// It is also the recovery procedure the design relies on, exercised: "delete the
// cursor to re-scan" must cost time and nothing else.
func TestDispatchPg_DeletingTheCursorRedoesNoWork(t *testing.T) {
	e := newDispatchEnv(t)
	for i := 0; i < 4; i++ {
		e.append(t, basePayload)
	}

	var calls atomic.Int64
	handler := HandlerFunc(func(context.Context, DispatchedEvent) error {
		calls.Add(1)
		return nil
	})
	d, err := NewDispatcher(e.deps("host-a", handler))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := d.Step(context.Background()); err != nil {
		t.Fatalf("first Step: %v", err)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("the first pass handled %d events, want 4 — the assertion below would be vacuous", got)
	}

	// Blow the cursor away, exactly as an operator or a lost disk would.
	if err := os.RemoveAll(DispatchDir(e.root)); err != nil {
		t.Fatalf("removing the cursor: %v", err)
	}

	fresh, err := NewDispatcher(e.deps("host-a", handler))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	res, err := fresh.Step(context.Background())
	if err != nil {
		t.Fatalf("Step after the cursor was deleted: %v", err)
	}
	if res.Scanned != 4 {
		t.Errorf("the re-scan looked at %d events, want 4 — it did not actually re-read the log, so this test proves nothing", res.Scanned)
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("the side effect ran %d times in total after a full re-scan, want 4 — the cursor is load-bearing and AC1 cannot hold", got)
	}
}

// ---------------------------------------------------------------------------
// AC2 — two dispatchers, one Postgres
// ---------------------------------------------------------------------------

// Two dispatchers with the same consumer split the log and never overlap.
//
// Both run concurrently against one database with LISTEN disabled. The
// assertions are the two halves that matter: TOTAL coverage (every event acted
// on) and DISJOINTNESS (no event acted on twice). Either alone is satisfiable by
// a broken implementation — a dispatcher that handles nothing is perfectly
// disjoint, and one that ignores claims has perfect coverage.
func TestDispatchPg_TwoDispatchersActOnEachEventExactlyOnce(t *testing.T) {
	e := newDispatchEnv(t)
	const events = 40
	for i := 0; i < events; i++ {
		e.append(t, basePayload)
	}

	var mu sync.Mutex
	handled := map[uuid.UUID]int{}
	byHost := map[string]int{}
	record := func(host string) Handler {
		return HandlerFunc(func(_ context.Context, ev DispatchedEvent) error {
			mu.Lock()
			defer mu.Unlock()
			handled[ev.ID]++
			byHost[host]++
			return nil
		})
	}

	// Separate cursor roots: the cursor is per-host local state, and sharing one
	// file would make this test a story about a shared file rather than about
	// claims.
	var wg sync.WaitGroup
	for _, host := range []string{"host-a", "host-b"} {
		deps := e.deps(host, record(host))
		deps.Cursor = &FileCursorStore{Root: t.TempDir()}
		d, err := NewDispatcher(deps)
		if err != nil {
			t.Fatalf("NewDispatcher(%s): %v", host, err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Several passes each, so the two interleave rather than one
			// finishing before the other starts.
			for i := 0; i < 5; i++ {
				if _, err := d.Step(context.Background()); err != nil {
					t.Errorf("Step: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(handled) != events {
		t.Errorf("%d distinct events were acted on, want %d — some events were dispatched by nobody", len(handled), events)
	}
	for id, n := range handled {
		if n != 1 {
			t.Errorf("event %s was acted on %d times, want exactly 1", id, n)
		}
	}
	// Not an assertion about the split being even — that is a scheduling
	// accident — but a check that BOTH dispatchers really participated. If one
	// did all the work, the disjointness above is a statement about a single
	// dispatcher and says nothing about two.
	if byHost["host-a"] == 0 || byHost["host-b"] == 0 {
		t.Errorf("only one dispatcher did any work (%v); the exactly-once result above was not measured across two hosts", byHost)
	}
}

// ---------------------------------------------------------------------------
// Host affinity against a real log (AC3)
// ---------------------------------------------------------------------------

// A worktree-bound event is claimed only by the host it names, and the wrong
// host does not even leave a claim row behind.
//
// The claim-row assertion is the one that matters beyond the handler count: if
// the wrong host claimed and then declined to act, the OWNING host would find
// its own work already taken and would never run it — a permanent stall that
// looks like an idle agent.
func TestDispatchPg_AffinedEventIsOnlyClaimedByItsHost(t *testing.T) {
	e := newDispatchEnv(t)
	id := e.append(t, `{"agent_name":"a","agent_type":"engineer","session_id":"s","host_affinity":"host-b"}`)

	var aCalls, bCalls atomic.Int64
	runOn := func(host string, counter *atomic.Int64) {
		d, err := NewDispatcher(e.deps(host, HandlerFunc(func(context.Context, DispatchedEvent) error {
			counter.Add(1)
			return nil
		})))
		if err != nil {
			t.Fatalf("NewDispatcher(%s): %v", host, err)
		}
		// A fresh cursor root per host.
		if _, err := d.Step(context.Background()); err != nil {
			t.Fatalf("Step(%s): %v", host, err)
		}
	}

	// host-a first, on its own cursor.
	e.root = t.TempDir()
	runOn("host-a", &aCalls)
	if got := aCalls.Load(); got != 0 {
		t.Errorf("host-a acted on an event affined to host-b %d times", got)
	}
	var claims int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM event_claims WHERE event_id = $1`, id).Scan(&claims); err != nil {
		t.Fatalf("counting claims: %v", err)
	}
	if claims != 0 {
		t.Errorf("host-a left %d claim row(s) on an event it may not handle; host-b would find its own work already taken", claims)
	}

	// host-b, the owner, on its own cursor.
	e.root = t.TempDir()
	runOn("host-b", &bCalls)
	if got := bCalls.Load(); got != 1 {
		t.Errorf("host-b acted on its own affined event %d times, want 1 — worktree-bound work would never run", got)
	}
}

// ---------------------------------------------------------------------------
// Lease takeover in the loop
// ---------------------------------------------------------------------------

// An event claimed by a host that never finished is picked up by another host
// once the lease expires — via TakeoverExpired, which the dispatcher must
// actually attempt.
//
// Without this the lease is decorative: a crashed host's claims would sit in the
// table forever and the events under them would never be dispatched by anyone,
// which is the failure mode event_claims' whole lease column exists to prevent.
func TestDispatchPg_ExpiredClaimIsTakenOverByAnotherHost(t *testing.T) {
	e := newDispatchEnv(t)
	id := e.append(t, basePayload)
	ctx := context.Background()

	// host-a claims and then "dies" — the claim exists, the work never happened.
	claims := &PgClaimStore{Pool: e.pool}
	if won, err := claims.Claim(ctx, id, "dispatcher", "host-a", time.Hour); err != nil || !won {
		t.Fatalf("seeding host-a's claim: won=%v err=%v", won, err)
	}

	// While the lease is LIVE, host-b must not act. This is the negative
	// control, in the same test, so a green result cannot come from a takeover
	// that fires unconditionally.
	var bCalls atomic.Int64
	handler := HandlerFunc(func(context.Context, DispatchedEvent) error {
		bCalls.Add(1)
		return nil
	})
	depsB := e.deps("host-b", handler)
	depsB.Cursor = &FileCursorStore{Root: t.TempDir()}
	dB, err := NewDispatcher(depsB)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := dB.Step(ctx); err != nil {
		t.Fatalf("Step while host-a's lease is live: %v", err)
	}
	if got := bCalls.Load(); got != 0 {
		t.Fatalf("host-b acted on an event whose lease is still live (%d calls)", got)
	}

	// Expire it, deterministically rather than by sleeping.
	if _, err := e.pool.Exec(ctx,
		`UPDATE event_claims SET lease_expires = now() - interval '1 second' WHERE event_id = $1`, id); err != nil {
		t.Fatalf("expiring the lease: %v", err)
	}

	depsB2 := e.deps("host-b", handler)
	depsB2.Cursor = &FileCursorStore{Root: t.TempDir()}
	dB2, err := NewDispatcher(depsB2)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := dB2.Step(ctx); err != nil {
		t.Fatalf("Step after the lease expired: %v", err)
	}
	if got := bCalls.Load(); got != 1 {
		t.Errorf("host-b acted %d times after host-a's lease expired, want 1 — a crashed host's events would never be dispatched by anyone", got)
	}

	var host string
	if err := e.pool.QueryRow(ctx,
		`SELECT host FROM event_claims WHERE event_id = $1`, id).Scan(&host); err != nil {
		t.Fatalf("reading the claim: %v", err)
	}
	if host != "host-b" {
		t.Errorf("the claim still names %q after the takeover, want host-b", host)
	}
}

// A dispatcher that finds nothing to do reports it, and does not error.
//
// The trivial case, asserted because `sprawl store dispatch` logs from
// StepResult and a nonzero Scanned on an empty log would make an idle system look
// busy.
func TestDispatchPg_EmptyLogIsANoOp(t *testing.T) {
	e := newDispatchEnv(t)
	d, err := NewDispatcher(e.deps("host-a", HandlerFunc(func(context.Context, DispatchedEvent) error {
		return fmt.Errorf("no event should have been dispatched")
	})))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	res, err := d.Step(context.Background())
	if err != nil {
		t.Fatalf("Step on an empty log: %v", err)
	}
	if res.Scanned != 0 || res.Handled != 0 {
		t.Errorf("Step on an empty log reported %+v, want zeroes", res)
	}
}

//go:build store_pg

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type appenderFixture struct {
	dsn       string
	pool      *pgxpool.Pool
	appender  *Appender
	registry  *Registry
	projectID uuid.UUID
	spill     *capturingSpiller
}

func newAppenderFixture(t *testing.T) *appenderFixture {
	t.Helper()
	dsn, pool := newTestSchema(t)
	reg, err := SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	projectID := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO projects (id, remote_url, created_at) VALUES ($1, $2, now())`,
		projectID, "https://example.invalid/"+projectID.String()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	spill := &capturingSpiller{}
	return &appenderFixture{
		dsn: dsn, pool: pool, registry: reg, projectID: projectID, spill: spill,
		appender: NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: spill}),
	}
}

func (f *appenderFixture) schemaID(t *testing.T, name string) uuid.UUID {
	t.Helper()
	s, ok := f.registry.ByName(name, 1)
	if !ok {
		t.Fatalf("seed %s@1 missing", name)
	}
	return s.ID
}

// openGoal appends a goal_opened and returns its event id.
func (f *appenderFixture) openGoal(t *testing.T, instance uuid.UUID, text string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := f.appender.Append(context.Background(), Event{
		ID:                 id,
		ProjectID:          f.projectID,
		WorkflowInstanceID: instance,
		SchemaID:           f.schemaID(t, "goal_opened"),
		Payload:            json.RawMessage(fmt.Sprintf(`{"goal_type":"RESEARCH","text":%q}`, text)),
	}); err != nil {
		t.Fatalf("open goal %q: %v", text, err)
	}
	return id
}

// closeGoal appends a goal_closed against goalID.
func (f *appenderFixture) closeGoal(t *testing.T, instance, goalID uuid.UUID) {
	t.Helper()
	if _, err := f.appender.Append(context.Background(), Event{
		ProjectID:          f.projectID,
		WorkflowInstanceID: instance,
		SchemaID:           f.schemaID(t, "goal_closed"),
		ClosesEventID:      &goalID,
		Payload:            json.RawMessage(`{"outcome":"success"}`),
	}); err != nil {
		t.Fatalf("close goal %s: %v", goalID, err)
	}
}

func (f *appenderFixture) countEvents(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events WHERE project_id = $1`, f.projectID).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// The one transaction
// ---------------------------------------------------------------------------

// TestAppend_WritesEventProjectionAndDoorbellInOneTransaction pins that all
// three effects of an opens-typed append are visible together, and that the
// doorbell fires only on commit.
//
// The LISTEN connection is established BEFORE the append and drained after: a
// notification observed on a connection that subscribed afterwards would prove
// nothing about ordering.
func TestAppend_WritesEventProjectionAndDoorbellInOneTransaction(t *testing.T) {
	f := newAppenderFixture(t)
	ctx := context.Background()

	listener, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatalf("listener connect: %v", err)
	}
	defer func() { _ = listener.Close(context.Background()) }()
	if _, err := listener.Exec(ctx, "LISTEN "+NotifyChannel); err != nil {
		t.Fatalf("LISTEN: %v", err)
	}

	instance := uuid.New()
	goalID := f.openGoal(t, instance, "the goal")

	var eventRows int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id = $1`, goalID).Scan(&eventRows); err != nil {
		t.Fatalf("count event: %v", err)
	}
	if eventRows != 1 {
		t.Errorf("event rows for the appended goal = %d, want 1", eventRows)
	}

	var contractRows int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM open_contracts WHERE event_id = $1`, goalID).Scan(&contractRows); err != nil {
		t.Fatalf("count contract: %v", err)
	}
	if contractRows != 1 {
		t.Errorf("open_contracts rows for the appended goal = %d, want 1 — the projection is maintained in the append transaction", contractRows)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	n, err := listener.WaitForNotification(waitCtx)
	if err != nil {
		t.Fatalf("no doorbell notification arrived within the deadline: %v", err)
	}
	if n.Channel != NotifyChannel {
		t.Errorf("notification on channel %q, want %q", n.Channel, NotifyChannel)
	}
	if !strings.Contains(n.Payload, instance.String()) {
		t.Errorf("doorbell payload %q does not name the workflow instance, so a consumer cannot route it", n.Payload)
	}
}

// TestAppend_RolledBackTransactionLeavesNothingAndRingsNoDoorbell is the
// negative control for the test above: it proves the three effects are genuinely
// transactional rather than three independent writes that happened to all
// succeed.
//
// The failure is induced with a real refusal from the appender's own projection
// check — a SECOND close of an already-closed goal — which happens AFTER the
// event insert has already run inside the transaction. That ordering is what
// makes this a rollback test rather than a validation test: something must have
// been written before the failure for a rollback to have work to do.
//
// It deliberately does NOT close a random non-existent uuid. That fails earlier
// and for a different reason — the closes_event_id foreign key (23503) — so it
// would exercise the FK rather than the projection check, and the event insert
// would never have succeeded in the first place.
//
// WHAT THIS DOES NOT COVER, stated because the test's name invites the wrong
// reading: the doorbell leg does NOT prove pg_notify is inside the transaction.
// The refusal happens BEFORE the notify would run, so the notify is never
// reached on this path — measured by moving pg_notify onto the connection pool,
// which leaves this test GREEN. The decisive assertion is hermetic:
// TestAppend_DoorbellIsIssuedOnTheTransactionNotThePool. What the leg here does
// establish is narrower and still worth having: no doorbell escapes from a
// rolled-back append along the path a real database actually takes.
func TestAppend_RolledBackTransactionLeavesNothingAndRingsNoDoorbell(t *testing.T) {
	f := newAppenderFixture(t)
	ctx := context.Background()

	instance := uuid.New()
	goal := f.openGoal(t, instance, "already closed")
	f.closeGoal(t, instance, goal)

	listener, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatalf("listener connect: %v", err)
	}
	defer func() { _ = listener.Close(context.Background()) }()
	if _, err := listener.Exec(ctx, "LISTEN "+NotifyChannel); err != nil {
		t.Fatalf("LISTEN: %v", err)
	}

	before := f.countEvents(t)

	closeID := uuid.New()
	_, err = f.appender.Append(ctx, Event{
		ID:                 closeID,
		ProjectID:          f.projectID,
		WorkflowInstanceID: instance,
		SchemaID:           f.schemaID(t, "goal_closed"),
		ClosesEventID:      &goal,
		Payload:            json.RawMessage(`{"outcome":"success"}`),
	})
	if !errors.Is(err, ErrNoOpenContract) {
		t.Fatalf("re-closing an already-closed goal: got err=%v, want ErrNoOpenContract", err)
	}

	if after := f.countEvents(t); after != before {
		t.Errorf("a refused close left %d event row(s) behind (before=%d, after=%d) — the insert and the projection update must be one transaction",
			after-before, before, after)
	}
	var orphanRows int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id = $1`, closeID).Scan(&orphanRows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if orphanRows != 0 {
		t.Errorf("the refused close event is present in the log")
	}

	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if n, err := listener.WaitForNotification(waitCtx); err == nil {
		t.Errorf("a rolled-back append rang the doorbell (payload %q) — pg_notify must be inside the transaction", n.Payload)
	}
}

// ---------------------------------------------------------------------------
// AC2: the appender rejects a payload violating its pinned schema
// ---------------------------------------------------------------------------

// TestAppend_RejectsPayloadViolatingItsPinnedSchema is AC2, asserted against a
// real database so the rejection is shown to prevent a WRITE and not merely to
// return an error.
func TestAppend_RejectsPayloadViolatingItsPinnedSchema(t *testing.T) {
	f := newAppenderFixture(t)
	ctx := context.Background()
	before := f.countEvents(t)

	// run_started requires agent_name, agent_type, session_id.
	_, err := f.appender.Append(ctx, Event{
		ProjectID:          f.projectID,
		WorkflowInstanceID: uuid.New(),
		SchemaID:           f.schemaID(t, "run_started"),
		Payload:            json.RawMessage(`{"agent_name":"finn","agent_type":"engineer"}`),
	})
	if !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("got err=%v, want ErrSchemaViolation", err)
	}
	if !strings.Contains(err.Error(), "session_id") {
		t.Errorf("the error must name the offending field; got: %v", err)
	}
	if after := f.countEvents(t); after != before {
		t.Errorf("a schema-violating append wrote %d row(s) to the log", after-before)
	}
	if f.spill.count() != 0 {
		t.Errorf("a schema-violating append spilled %d record(s) — a violation is an emitter bug, not an outage", f.spill.count())
	}

	// POSITIVE CONTROL: the same append with the missing field present lands
	// exactly one row. Without it, an appender that rejected everything — or one
	// pointed at the wrong project — would satisfy every assertion above.
	if _, err := f.appender.Append(ctx, Event{
		ProjectID:          f.projectID,
		WorkflowInstanceID: uuid.New(),
		SchemaID:           f.schemaID(t, "run_started"),
		Payload:            json.RawMessage(`{"agent_name":"finn","agent_type":"engineer","session_id":"s-1"}`),
	}); err != nil {
		t.Fatalf("control: the corrected payload must be accepted: %v", err)
	}
	if after := f.countEvents(t); after != before+1 {
		t.Errorf("control: expected exactly one new row, got %d", after-before)
	}
}

// ---------------------------------------------------------------------------
// AC4: open_contracts drop -> rebuild -> equals the anti-join
// ---------------------------------------------------------------------------

// contractSet reads the maintained projection for a project.
func contractSet(t *testing.T, pool *pgxpool.Pool, projectID uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT oc.event_id::text FROM open_contracts oc
		 JOIN events e ON e.id = oc.event_id
		 WHERE e.project_id = $1`, projectID)
	if err != nil {
		t.Fatalf("read open_contracts: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Strings(out)
	return out
}

// antiJoinSet runs Appendix A's correctness-reference open-goals query verbatim.
//
// Written out HERE, in the test, rather than reusing the production query: this
// is the independent oracle the projection is compared against, and an oracle
// that shares an implementation with its subject cannot disagree with it.
func antiJoinSet(t *testing.T, pool *pgxpool.Pool, projectID uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT g.id::text FROM events g
		 JOIN event_type_schemas s ON s.id = g.schema_id AND s.opens
		 LEFT JOIN events c ON c.closes_event_id = g.id
		 WHERE c.id IS NULL AND g.project_id = $1`, projectID)
	if err != nil {
		t.Fatalf("anti-join: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Strings(out)
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestOpenContracts_MaintainedProjectionEqualsTheAntiJoinAcrossDropAndRebuild is
// AC4.
//
// THREE distinct claims, and they are not the same claim:
//
//  1. The projection the APPENDER maintained incrementally, event by event,
//     equals the anti-join derived from the log. This is the one that matters:
//     the projection is an optimisation, and if it can drift from the log then
//     every outstanding-work answer is unreliable.
//  2. Dropping the projection and rebuilding it from the log reproduces it, so
//     the projection is genuinely derived and an operator can repair it.
//  3. The rebuilt projection also equals the independent anti-join, so
//     RebuildOpenContracts is not quietly computing something else.
//
// Claim 3 alone would be near-tautological (the rebuild IS an anti-join), which
// is exactly why claim 1 is asserted first and against a hand-written oracle.
func TestOpenContracts_MaintainedProjectionEqualsTheAntiJoinAcrossDropAndRebuild(t *testing.T) {
	f := newAppenderFixture(t)
	ctx := context.Background()

	// Build a non-trivial history: several instances, opens and closes
	// interleaved, some closed and some left open, plus a spawn/retire pair so
	// more than one opens-typed schema participates.
	instanceA, instanceB := uuid.New(), uuid.New()
	openA1 := f.openGoal(t, instanceA, "A1")
	openA2 := f.openGoal(t, instanceA, "A2")
	openB1 := f.openGoal(t, instanceB, "B1")
	f.closeGoal(t, instanceA, openA1)
	openB2 := f.openGoal(t, instanceB, "B2")
	f.closeGoal(t, instanceB, openB2)

	spawned := uuid.New()
	if _, err := f.appender.Append(ctx, Event{
		ID:                 spawned,
		ProjectID:          f.projectID,
		WorkflowInstanceID: instanceA,
		SchemaID:           f.schemaID(t, "agent_spawned"),
		Payload:            json.RawMessage(`{"agent_name":"zone","agent_type":"engineer"}`),
	}); err != nil {
		t.Fatalf("append agent_spawned: %v", err)
	}

	// Sanity: the fixture must actually leave contracts BOTH open and closed.
	// A history where everything is open, or everything closed, would let a
	// broken projection agree with the anti-join by accident.
	maintained := contractSet(t, f.pool, f.projectID)
	wantOpen := []string{openA2.String(), openB1.String(), spawned.String()}
	sort.Strings(wantOpen)
	if !sameSet(maintained, wantOpen) {
		t.Fatalf("the fixture did not produce the expected open set.\n maintained: %v\n want:       %v\n(closed: %s, %s)",
			maintained, wantOpen, openA1, openB2)
	}

	// Claim 1: maintained == anti-join.
	if anti := antiJoinSet(t, f.pool, f.projectID); !sameSet(maintained, anti) {
		t.Errorf("the incrementally maintained projection disagrees with the log.\n maintained: %v\n anti-join:  %v", maintained, anti)
	}

	// Claim 2 and 3: drop, rebuild, compare against both.
	if _, err := f.pool.Exec(ctx,
		`DELETE FROM open_contracts WHERE event_id IN (SELECT id FROM events WHERE project_id = $1)`,
		f.projectID); err != nil {
		t.Fatalf("drop projection: %v", err)
	}
	if got := contractSet(t, f.pool, f.projectID); len(got) != 0 {
		t.Fatalf("the projection was not actually dropped (%d rows remain), so the rebuild below would prove nothing", len(got))
	}
	if err := RebuildOpenContracts(ctx, f.pool, f.projectID); err != nil {
		t.Fatalf("RebuildOpenContracts: %v", err)
	}

	rebuilt := contractSet(t, f.pool, f.projectID)
	if !sameSet(rebuilt, maintained) {
		t.Errorf("drop -> rebuild did not reproduce the maintained projection.\n rebuilt:    %v\n maintained: %v", rebuilt, maintained)
	}
	if anti := antiJoinSet(t, f.pool, f.projectID); !sameSet(rebuilt, anti) {
		t.Errorf("the rebuilt projection disagrees with the anti-join.\n rebuilt:   %v\n anti-join: %v", rebuilt, anti)
	}
}

// TestOpenContracts_ComparisonDetectsADivergentProjection is the POSITIVE
// CONTROL for the test above.
//
// It plants a spurious projection row — the shape a lost close or a bug in the
// appender's maintenance would produce — and asserts the comparison NOTICES.
// Without it, "maintained == anti-join" is equally satisfied by a comparison
// that always reports equality, and there is no way to tell those apart from a
// green run.
func TestOpenContracts_ComparisonDetectsADivergentProjection(t *testing.T) {
	f := newAppenderFixture(t)
	ctx := context.Background()

	instance := uuid.New()
	goal := f.openGoal(t, instance, "real")
	f.closeGoal(t, instance, goal)

	if maintained, anti := contractSet(t, f.pool, f.projectID), antiJoinSet(t, f.pool, f.projectID); !sameSet(maintained, anti) {
		t.Fatalf("baseline must agree before the divergence is planted: %v vs %v", maintained, anti)
	}

	// Re-insert the projection row for the now-closed goal, as the owner. This
	// is exactly what a dropped DELETE in the appender would leave behind.
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO open_contracts (event_id, workflow_instance_id, opened_at) VALUES ($1,$2,now())`,
		goal, instance); err != nil {
		t.Fatalf("plant divergence: %v", err)
	}

	maintained := contractSet(t, f.pool, f.projectID)
	anti := antiJoinSet(t, f.pool, f.projectID)
	if sameSet(maintained, anti) {
		t.Errorf("the comparison did not notice a closed goal still present in the projection: maintained=%v anti-join=%v — every equality assertion in this file is therefore unfalsifiable",
			maintained, anti)
	}

	// And the repair path fixes it, which is what makes the drift recoverable
	// rather than merely detectable.
	if _, err := f.pool.Exec(ctx,
		`DELETE FROM open_contracts WHERE event_id IN (SELECT id FROM events WHERE project_id = $1)`, f.projectID); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := RebuildOpenContracts(ctx, f.pool, f.projectID); err != nil {
		t.Fatalf("RebuildOpenContracts: %v", err)
	}
	if got, want := contractSet(t, f.pool, f.projectID), antiJoinSet(t, f.pool, f.projectID); !sameSet(got, want) {
		t.Errorf("rebuild did not repair the divergence: %v vs %v", got, want)
	}
}

// TestAppend_DoubleCloseIsRefused pins that closes are final. The second close
// must be refused and must write nothing, or "outstanding work" would depend on
// delivery order.
func TestAppend_DoubleCloseIsRefused(t *testing.T) {
	f := newAppenderFixture(t)
	instance := uuid.New()
	goal := f.openGoal(t, instance, "closed once")
	f.closeGoal(t, instance, goal)

	before := f.countEvents(t)
	_, err := f.appender.Append(context.Background(), Event{
		ProjectID:          f.projectID,
		WorkflowInstanceID: instance,
		SchemaID:           f.schemaID(t, "goal_closed"),
		ClosesEventID:      &goal,
		Payload:            json.RawMessage(`{"outcome":"failure"}`),
	})
	if !errors.Is(err, ErrNoOpenContract) {
		t.Fatalf("a second close: got err=%v, want ErrNoOpenContract", err)
	}
	if after := f.countEvents(t); after != before {
		t.Errorf("the refused second close wrote %d row(s)", after-before)
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

// TestAppend_ConcurrentAppendsSerialiseWithoutDeadlock pins that the advisory
// lock orders same-instance appends rather than deadlocking them, and that it is
// scoped to the instance rather than global.
func TestAppend_ConcurrentAppendsSerialiseWithoutDeadlock(t *testing.T) {
	f := newAppenderFixture(t)
	ctx := context.Background()

	const n = 8
	run := func(instanceFor func(i int) uuid.UUID) []int64 {
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			seqs []int64
		)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				seq, err := f.appender.Append(ctx, Event{
					ProjectID:          f.projectID,
					WorkflowInstanceID: instanceFor(i),
					SchemaID:           f.schemaID(t, "turn_finished"),
					Payload:            json.RawMessage(`{"session_id":"s","input_tokens":1,"output_tokens":2}`),
				})
				if err != nil {
					t.Errorf("concurrent append %d: %v", i, err)
					return
				}
				mu.Lock()
				seqs = append(seqs, seq)
				mu.Unlock()
			}(i)
		}
		wg.Wait()
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		return seqs
	}

	same := uuid.New()
	seqs := run(func(int) uuid.UUID { return same })
	if len(seqs) != n {
		t.Fatalf("same-instance: %d of %d appends succeeded", len(seqs), n)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] == seqs[i-1] {
			t.Errorf("two same-instance appends were assigned the same seq %d — the global total order is not total", seqs[i])
		}
	}

	// Different instances: also fine, which is what shows the lock is scoped
	// per instance and not a global mutex on the whole log.
	if got := run(func(int) uuid.UUID { return uuid.New() }); len(got) != n {
		t.Errorf("distinct-instance: %d of %d appends succeeded", len(got), n)
	}
}

// ---------------------------------------------------------------------------
// Artifacts
// ---------------------------------------------------------------------------

// TestPutArtifact_IsContentAddressed pins dedup: the same content twice yields
// one row and the same id, and different content yields a different id.
//
// Both directions are needed. The first alone is satisfied by a function that
// always returns the same id; the second alone by one that never dedups.
func TestPutArtifact_IsContentAddressed(t *testing.T) {
	f := newAppenderFixture(t)
	ctx := context.Background()

	id1, err := PutArtifact(ctx, f.pool, "report", "the same content", "abc123")
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	id2, err := PutArtifact(ctx, f.pool, "report", "the same content", "abc123")
	if err != nil {
		t.Fatalf("PutArtifact (repeat): %v", err)
	}
	if id1 != id2 {
		t.Errorf("identical content produced two ids (%s, %s) — content addressing is not deduplicating", id1, id2)
	}

	var rows int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM artifacts`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("artifacts holds %d rows after storing the same content twice, want 1", rows)
	}

	id3, err := PutArtifact(ctx, f.pool, "report", "different content", "abc123")
	if err != nil {
		t.Fatalf("PutArtifact (different): %v", err)
	}
	if id3 == id1 {
		t.Error("different content produced the same id — the digest is not a function of the content")
	}
}

// TestAppend_WithArtifactReference pins that an event can carry an artifact and
// that the FK is satisfied by what PutArtifact returns. Without it, the thin
// event / fat artifact split is untested end to end.
func TestAppend_WithArtifactReference(t *testing.T) {
	f := newAppenderFixture(t)
	ctx := context.Background()

	artifactID, err := PutArtifact(ctx, f.pool, "report", strings.Repeat("long report body ", 1000), "deadbeef")
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	if _, err := f.appender.Append(ctx, Event{
		ProjectID:          f.projectID,
		WorkflowInstanceID: uuid.New(),
		SchemaID:           f.schemaID(t, "run_finished"),
		ArtifactID:         &artifactID,
		Payload:            json.RawMessage(`{"session_id":"s-1","outcome":"success"}`),
	}); err != nil {
		t.Fatalf("append with artifact: %v", err)
	}

	var got uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`SELECT artifact_id FROM events WHERE project_id = $1 AND artifact_id IS NOT NULL`, f.projectID).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != artifactID {
		t.Errorf("event references artifact %s, want %s", got, artifactID)
	}
}

// ---------------------------------------------------------------------------
// Runtime append-only verification
// ---------------------------------------------------------------------------

// TestVerifyAppendOnly_BothDirections pins the runtime guard that closes the
// operational hole the migration cannot.
//
// The GRANTs only make `events` append-only if the application actually connects
// as a user inheriting the app role. A DSN carrying owner or superuser
// credentials bypasses every REVOKE while the grants tests stay green, because
// those tests assume the role explicitly. VerifyAppendOnly asks the question
// about the connection ACTUALLY IN USE.
//
// Both directions are asserted in one run: as the owner it must report the
// problem, as the app role it must stay silent. A one-directional test here
// would be satisfied by a function that always returns nil, which is precisely
// the failure that matters.
func TestVerifyAppendOnly_BothDirections(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()

	err := VerifyAppendOnly(ctx, pool)
	if !errors.Is(err, ErrAppendOnlyNotEnforced) {
		t.Errorf("connected as the schema OWNER, VerifyAppendOnly returned %v; it must report ErrAppendOnlyNotEnforced, because an owner connection can rewrite history", err)
	}
	if err != nil && !strings.Contains(err.Error(), appRole) {
		t.Errorf("the error should name the role the deployment ought to be using; got: %v", err)
	}

	asAppRole(t, pool, func(ctx context.Context, conn *pgx.Conn) {
		if err := VerifyAppendOnly(ctx, connPool{conn}); err != nil {
			t.Errorf("connected as %s, VerifyAppendOnly returned %v; want nil", appRole, err)
		}
	})
}

// connPool adapts a single *pgx.Conn to the PgPool surface so VerifyAppendOnly
// can be asked about one specific connection's privileges. Only the methods
// VerifyAppendOnly uses are implemented; the rest fail loudly.
type connPool struct{ c *pgx.Conn }

func (p connPool) Begin(ctx context.Context) (pgx.Tx, error) { return p.c.Begin(ctx) }
func (p connPool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return p.c.Exec(ctx, sql, args...)
}

func (p connPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return p.c.QueryRow(ctx, sql, args...)
}
func (p connPool) Ping(ctx context.Context) error { return p.c.Ping(ctx) }

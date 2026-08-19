package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Hermetic appender tests. What they pin is the ORDER OF OPERATIONS and the
// DEGRADED-MODE SPLIT, both of which are stated as binding in Appendix B and
// neither of which a green integration test can distinguish from a wrong
// implementation:
//
//   - Appendix B item 7: validate the payload BEFORE taking the advisory lock.
//     An integration test cannot see the difference — both orders produce the
//     same rows — but under contention the wrong order holds a lock across work
//     that was going to be rejected anyway.
//   - Appendix B item 6: validate against the schema_id the CALLER PINNED,
//     never against whatever is latest. Two versions of one name are needed to
//     tell those apart, and a single-version fixture cannot.
//   - Degraded mode: telemetry spills and goal open/close fails LOUDLY. A test
//     with a reachable database exercises neither branch.

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// recordingPool is a PgPool that records every statement it is asked to run, in
// order, and can be told to fail at Begin (the connection-down case).
type recordingPool struct {
	mu sync.Mutex
	// calls is the ordered log of operations: "begin", "commit", "rollback",
	// or the leading words of a statement.
	calls    []string
	beginErr error
	// seq is what the INSERT ... RETURNING seq yields.
	seq int64
	// execErr, when set for a matching statement fragment, makes that Exec fail.
	execErr map[string]error
	// rowsAffected overrides the CommandTag row count for a matching fragment.
	rowsAffected map[string]int64
}

func newRecordingPool() *recordingPool {
	return &recordingPool{seq: 42, execErr: map[string]error{}, rowsAffected: map[string]int64{}}
}

func (p *recordingPool) record(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, s)
}

func (p *recordingPool) log() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func (p *recordingPool) Begin(context.Context) (pgx.Tx, error) {
	if p.beginErr != nil {
		p.record("begin:FAILED")
		return nil, p.beginErr
	}
	p.record("begin")
	return &recordingTx{pool: p}, nil
}

// Exec on the POOL is recorded with a "pool:" prefix, so an assertion can tell
// a statement issued on the connection pool apart from one issued on the
// transaction. That distinction is the only way to catch pg_notify being moved
// outside the append transaction — see
// TestAppend_DoorbellIsIssuedOnTheTransactionNotThePool.
func (p *recordingPool) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	p.record("pool:" + stmtLabel(sql))
	return pgconn.NewCommandTag(""), nil
}

func (p *recordingPool) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	p.record("pool:" + stmtLabel(sql))
	return errRow{err: errors.New("recordingPool: QueryRow is not stubbed for this statement")}
}

func (p *recordingPool) Ping(context.Context) error { return p.beginErr }

// recordingTx records statements and terminal outcome on its pool.
type recordingTx struct {
	pgx.Tx // embedded nil: any method this double does not implement panics
	// loudly rather than silently succeeding.
	pool *recordingPool
	done bool
}

func (t *recordingTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	label := stmtLabel(sql)
	t.pool.record(label)
	if err, ok := t.pool.execErr[label]; ok {
		return pgconn.NewCommandTag(""), err
	}
	if n, ok := t.pool.rowsAffected[label]; ok {
		return pgconn.NewCommandTag(commandTagFor(label, n)), nil
	}
	return pgconn.NewCommandTag(commandTagFor(label, 1)), nil
}

func (t *recordingTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	label := stmtLabel(sql)
	t.pool.record(label)
	if err, ok := t.pool.execErr[label]; ok {
		return errRow{err: err}
	}
	return seqRow{seq: t.pool.seq}
}

func (t *recordingTx) Commit(context.Context) error {
	if t.done {
		return nil
	}
	t.done = true
	t.pool.record("commit")
	return nil
}

func (t *recordingTx) Rollback(context.Context) error {
	if t.done {
		return nil
	}
	t.done = true
	t.pool.record("rollback")
	return nil
}

// commandTagFor renders a CommandTag string whose RowsAffected() parses to n.
func commandTagFor(label string, n int64) string {
	verb := "UPDATE"
	switch {
	case strings.HasPrefix(label, "insert"):
		verb = "INSERT 0"
	case strings.HasPrefix(label, "delete"):
		verb = "DELETE"
	case strings.HasPrefix(label, "select"):
		verb = "SELECT"
	}
	return verb + " " + itoa(n)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

type seqRow struct{ seq int64 }

func (r seqRow) Scan(dest ...any) error {
	if len(dest) != 1 {
		return errors.New("seqRow: want exactly one destination")
	}
	p, ok := dest[0].(*int64)
	if !ok {
		return errors.New("seqRow: destination is not *int64")
	}
	*p = r.seq
	return nil
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// stmtLabel reduces a statement to a short stable label for order assertions.
// Keyed on what the statement DOES rather than on its full text, so reformatting
// SQL does not break the assertions while reordering it still does.
func stmtLabel(sql string) string {
	s := strings.ToLower(strings.Join(strings.Fields(sql), " "))
	switch {
	case strings.Contains(s, "pg_advisory_xact_lock"):
		return "advisory_lock"
	case strings.Contains(s, "pg_advisory_lock"):
		return "SESSION_LOCK_FORBIDDEN"
	case strings.Contains(s, "pg_notify"):
		return "notify"
	case strings.HasPrefix(s, "insert into events"):
		return "insert_event"
	case strings.HasPrefix(s, "insert into open_contracts"):
		return "insert_open_contract"
	case strings.HasPrefix(s, "delete from open_contracts"):
		return "delete_open_contract"
	case strings.HasPrefix(s, "select") && strings.Contains(s, "event_type_schemas"):
		return "select_schema"
	default:
		return "other:" + s
	}
}

// capturingSpiller records what was handed to the degraded-mode spill.
type capturingSpiller struct {
	mu      sync.Mutex
	records []SpillRecord
	err     error
}

func (s *capturingSpiller) Write(_ context.Context, r SpillRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.records = append(s.records, r)
	return nil
}

func (s *capturingSpiller) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	return reg
}

func mustSchema(t *testing.T, reg *Registry, name string) *EventTypeSchema {
	t.Helper()
	s, ok := reg.ByName(name, 1)
	if !ok {
		t.Fatalf("seed schema %s@1 is missing", name)
	}
	return s
}

func newTestAppender(t *testing.T, pool *recordingPool, spill Spiller) *Appender {
	t.Helper()
	return NewAppender(AppenderDeps{Pool: pool, Registry: testRegistry(t), Spill: spill})
}

// runStartedEvent is a valid, SPILLABLE telemetry event.
func runStartedEvent(t *testing.T, reg *Registry) Event {
	t.Helper()
	return Event{
		ProjectID:          uuid.New(),
		WorkflowInstanceID: uuid.New(),
		SchemaID:           mustSchema(t, reg, "run_started").ID,
		Payload:            json.RawMessage(`{"agent_name":"finn","agent_type":"engineer","session_id":"s-1"}`),
	}
}

// goalOpenedEvent is a valid, NON-spillable contract event.
func goalOpenedEvent(t *testing.T, reg *Registry) Event {
	t.Helper()
	return Event{
		ProjectID:          uuid.New(),
		WorkflowInstanceID: uuid.New(),
		SchemaID:           mustSchema(t, reg, "goal_opened").ID,
		Payload:            json.RawMessage(`{"goal_type":"BACKEND_CODE_CHANGE","text":"do the thing"}`),
	}
}

func indexOf(calls []string, want string) int {
	for i, c := range calls {
		if c == want {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Order of operations (Appendix B items 5 and 7)
// ---------------------------------------------------------------------------

// TestAppend_ValidationHappensBeforeAnyTransaction is Appendix B item 7.
//
// The lock must not be taken for work that is going to be rejected. This is
// unobservable in the database — both orders produce identical rows — so the
// only way to assert it is to watch the call sequence and see that a rejected
// payload never reaches Begin at all.
func TestAppend_ValidationHappensBeforeAnyTransaction(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	spill := &capturingSpiller{}
	a := newTestAppender(t, pool, spill)

	ev := runStartedEvent(t, reg)
	ev.Payload = json.RawMessage(`{"agent_name":"finn"}`) // missing agent_type, session_id

	if _, err := a.Append(context.Background(), ev); !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("got err=%v, want ErrSchemaViolation", err)
	}
	if calls := pool.log(); len(calls) != 0 {
		t.Errorf("a payload rejected by validation touched the database: %v — validation must precede the transaction and the advisory lock", calls)
	}

	// POSITIVE CONTROL: the same appender, a valid payload, does reach Begin.
	// Without this leg an appender that never touched the database at all would
	// satisfy the assertion above.
	pool2 := newRecordingPool()
	a2 := newTestAppender(t, pool2, spill)
	if _, err := a2.Append(context.Background(), runStartedEvent(t, reg)); err != nil {
		t.Fatalf("control: a valid append must succeed: %v", err)
	}
	if got := indexOf(pool2.log(), "begin"); got != 0 {
		t.Errorf("control: a valid append must open a transaction first; call log: %v", pool2.log())
	}
}

// TestAppend_LockIsTakenInsideTheTransactionBeforeTheInsert pins the rest of the
// binding sequence: begin, advisory lock, insert, projection, notify, commit.
//
// It also pins that the lock is the XACT-scoped one. Appendix B item 7 says
// pg_advisory_xact_lock ONLY, because a session lock survives an abandoned
// transaction and wedges every future append for that workflow instance until
// the connection dies — a failure mode with no local symptom and no timeout.
func TestAppend_LockIsTakenInsideTheTransactionBeforeTheInsert(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	a := newTestAppender(t, pool, &capturingSpiller{})

	if _, err := a.Append(context.Background(), goalOpenedEvent(t, reg)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	calls := pool.log()

	if i := indexOf(calls, "SESSION_LOCK_FORBIDDEN"); i >= 0 {
		t.Errorf("the appender took a SESSION-scoped advisory lock; only pg_advisory_xact_lock is permitted, because a session lock survives an abandoned transaction: %v", calls)
	}

	want := []string{"begin", "advisory_lock", "insert_event", "insert_open_contract", "notify", "commit"}
	prev := -1
	for _, step := range want {
		at := indexOf(calls, step)
		if at < 0 {
			t.Errorf("step %q never happened; call log: %v", step, calls)
			continue
		}
		if at < prev {
			t.Errorf("step %q happened out of order; call log: %v (want the order %v)", step, calls, want)
		}
		prev = at
	}
}

// TestAppend_DoorbellIsIssuedOnTheTransactionNotThePool pins that pg_notify is
// part of the append transaction.
//
// This exists because the integration test that looks like it covers this — a
// rolled-back append must ring no doorbell — CANNOT. Measured, not assumed: with
// pg_notify moved from the transaction to the pool, that test stays GREEN,
// because the refusal it induces (a close matching no open contract) happens
// BEFORE the notify would run, so the notify is never reached either way.
//
// Why the placement matters: a notify issued outside the transaction fires
// whether or not the append commits. Every consumer would then be woken for
// events that do not exist, and — worse in the other direction — a doorbell
// would arrive before the row is visible, so a consumer that trusted it would
// read an empty log and go back to sleep until the next poll.
func TestAppend_DoorbellIsIssuedOnTheTransactionNotThePool(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	a := newTestAppender(t, pool, &capturingSpiller{})

	if _, err := a.Append(context.Background(), runStartedEvent(t, reg)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	calls := pool.log()
	if indexOf(calls, "pool:notify") >= 0 {
		t.Errorf("pg_notify was issued on the connection pool rather than on the append transaction, so it fires even when the append rolls back: %v", calls)
	}
	if indexOf(calls, "notify") < 0 {
		t.Fatalf("no doorbell was issued at all, so the assertion above passed vacuously: %v", calls)
	}
}

// TestAppend_SeqIsReturnedFromTheInsert pins that the caller gets the log
// position the database assigned, not a locally invented number.
func TestAppend_SeqIsReturnedFromTheInsert(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	pool.seq = 12345
	a := newTestAppender(t, pool, &capturingSpiller{})

	seq, err := a.Append(context.Background(), runStartedEvent(t, reg))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if seq != 12345 {
		t.Errorf("Append returned seq=%d, want the value the INSERT ... RETURNING produced (12345)", seq)
	}
}

// ---------------------------------------------------------------------------
// Contract maintenance
// ---------------------------------------------------------------------------

// TestAppend_ClosingTypeDeletesTheContract pins that a closes-typed append
// removes the open contract in the SAME transaction as the append.
func TestAppend_ClosingTypeDeletesTheContract(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	a := newTestAppender(t, pool, &capturingSpiller{})

	closed := uuid.New()
	ev := Event{
		ProjectID:          uuid.New(),
		WorkflowInstanceID: uuid.New(),
		SchemaID:           mustSchema(t, reg, "goal_closed").ID,
		ClosesEventID:      &closed,
		Payload:            json.RawMessage(`{"outcome":"success"}`),
	}
	if _, err := a.Append(context.Background(), ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	calls := pool.log()
	if indexOf(calls, "delete_open_contract") < 0 {
		t.Errorf("a closes-typed append did not delete the open contract: %v", calls)
	}
	if indexOf(calls, "insert_open_contract") >= 0 {
		t.Errorf("a closes-typed append also inserted an open contract: %v", calls)
	}
	if di, ci := indexOf(calls, "delete_open_contract"), indexOf(calls, "commit"); di > ci {
		t.Errorf("the projection delete happened after commit — it must be in the same transaction: %v", calls)
	}
}

// TestAppend_CloseWithoutClosesEventIDIsRejected pins that a close must name
// what it closes. Without it the projection row would never be deleted and the
// contract would stay open forever, which the sweeper reads as a stall.
func TestAppend_CloseWithoutClosesEventIDIsRejected(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	a := newTestAppender(t, pool, &capturingSpiller{})

	ev := Event{
		ProjectID:          uuid.New(),
		WorkflowInstanceID: uuid.New(),
		SchemaID:           mustSchema(t, reg, "goal_closed").ID,
		Payload:            json.RawMessage(`{"outcome":"success"}`),
	}
	_, err := a.Append(context.Background(), ev)
	if err == nil {
		t.Fatal("a closes-typed append with no ClosesEventID must be rejected")
	}
	if !strings.Contains(err.Error(), "closes_event_id") {
		t.Errorf("the error should name the missing field; got: %v", err)
	}
}

// TestAppend_CloseThatMatchesNoOpenContractIsRefused pins that closing an
// already-closed or unknown contract fails and writes nothing.
//
// Closes are final and the log is monotone: a second close would be a
// double-close, and silently accepting it would make "outstanding work" depend
// on delivery order.
func TestAppend_CloseThatMatchesNoOpenContractIsRefused(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	pool.rowsAffected["delete_open_contract"] = 0 // nothing matched
	a := newTestAppender(t, pool, &capturingSpiller{})

	closed := uuid.New()
	ev := Event{
		ProjectID:          uuid.New(),
		WorkflowInstanceID: uuid.New(),
		SchemaID:           mustSchema(t, reg, "goal_closed").ID,
		ClosesEventID:      &closed,
		Payload:            json.RawMessage(`{"outcome":"success"}`),
	}
	if _, err := a.Append(context.Background(), ev); !errors.Is(err, ErrNoOpenContract) {
		t.Fatalf("got err=%v, want ErrNoOpenContract", err)
	}
	calls := pool.log()
	if indexOf(calls, "commit") >= 0 {
		t.Errorf("a refused close committed its transaction: %v", calls)
	}
	if indexOf(calls, "rollback") < 0 {
		t.Errorf("a refused close did not roll back: %v", calls)
	}
}

// ---------------------------------------------------------------------------
// Version pinning (Appendix B item 6)
// ---------------------------------------------------------------------------

// TestAppend_ValidatesAgainstThePinnedVersionNotTheLatest is the assertion that
// "never latest" actually means something.
//
// It needs TWO versions of one name to say anything at all: with a
// single-version registry, "validated against the pinned id" and "validated
// against the latest" are the same statement and any implementation passes.
func TestAppend_ValidatesAgainstThePinnedVersionNotTheLatest(t *testing.T) {
	v1 := &EventTypeSchema{
		ID: SeedID("evolving", 1), Name: "evolving", Version: 1,
		JSONSchema: json.RawMessage(`{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`),
	}
	v2 := &EventTypeSchema{
		ID: SeedID("evolving", 2), Name: "evolving", Version: 2,
		JSONSchema: json.RawMessage(`{"type":"object","required":["a","b"],"properties":{"a":{"type":"string"},"b":{"type":"string"}}}`),
	}
	reg, err := NewRegistry([]*EventTypeSchema{v1, v2})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	pool := newRecordingPool()
	a := NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: &capturingSpiller{}})

	base := Event{ProjectID: uuid.New(), WorkflowInstanceID: uuid.New()}

	// A v1-shaped payload pinned to v1 must be accepted even though v2 exists
	// and would reject it. An implementation that resolved "latest" would fail
	// here — this is the leg that catches it.
	v1Event := base
	v1Event.SchemaID = v1.ID
	v1Event.Payload = json.RawMessage(`{"a":"x"}`)
	if _, err := a.Append(context.Background(), v1Event); err != nil {
		t.Errorf("a payload valid under the PINNED v1 was rejected, so the appender is not honouring the pin: %v", err)
	}

	// The same payload pinned to v2 must be rejected. Without this leg, an
	// appender that validated nothing would pass the leg above.
	v2Event := base
	v2Event.SchemaID = v2.ID
	v2Event.Payload = json.RawMessage(`{"a":"x"}`)
	if _, err := a.Append(context.Background(), v2Event); !errors.Is(err, ErrSchemaViolation) {
		t.Errorf("a payload missing v2's required field `b` was accepted under a v2 pin: err=%v", err)
	}
}

// TestAppend_UnknownSchemaIDIsRefused pins that an unresolvable pin is an error
// naming the id, rather than a permissive fallback.
func TestAppend_UnknownSchemaIDIsRefused(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	spill := &capturingSpiller{}
	a := newTestAppender(t, pool, spill)

	ev := runStartedEvent(t, reg)
	ev.SchemaID = uuid.MustParse("00000000-0000-0000-0000-0000000000ff")

	_, err := a.Append(context.Background(), ev)
	if err == nil {
		t.Fatal("an unknown schema_id must be refused")
	}
	if !strings.Contains(err.Error(), "00000000-0000-0000-0000-0000000000ff") {
		t.Errorf("the error must name the unresolvable schema id so it can be traced; got: %v", err)
	}
	if spill.count() != 0 {
		t.Errorf("an unresolvable schema id spilled %d record(s); with no schema there is no way to know whether the type is spillable, so it must not spill", spill.count())
	}
}

// ---------------------------------------------------------------------------
// Degraded mode
// ---------------------------------------------------------------------------

var errConnRefused = errors.New("dial tcp 127.0.0.1:1: connect: connection refused")

// sqlStateForeignKeyViolationHermetic duplicates the constant in the store_pg
// suite because that file is behind a build tag and this one is not.
const sqlStateForeignKeyViolationHermetic = "23503"

// TestAppend_DatabaseDown_TelemetrySpillsAndTheCallerIsNotBlocked pins the
// spillable half of the degraded-mode split.
//
// The requirement is that agents never brick on the store, so a lifecycle event
// with the DB down must NOT surface an error to its emitter — the emitter is a
// subscriber on an agent's runtime EventBus, and an error there propagates into
// the agent's own operation.
func TestAppend_DatabaseDown_TelemetrySpillsAndTheCallerIsNotBlocked(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	pool.beginErr = errConnRefused
	spill := &capturingSpiller{}
	a := newTestAppender(t, pool, spill)

	seq, err := a.Append(context.Background(), runStartedEvent(t, reg))
	if err != nil {
		t.Fatalf("a spillable telemetry event with the DB down must not return an error to its emitter: %v", err)
	}
	if seq != 0 {
		t.Errorf("a spilled event has no log position; got seq=%d, want 0", seq)
	}
	if spill.count() != 1 {
		t.Fatalf("spill holds %d record(s), want 1 — the event was neither stored nor spilled, which is a silent drop", spill.count())
	}
	rec := spill.records[0]
	if rec.SchemaID != mustSchema(t, reg, "run_started").ID {
		t.Errorf("spilled record carries schema_id %s, want the pinned one", rec.SchemaID)
	}
	if rec.Reason == "" {
		t.Error("a spilled record must carry why it spilled, or a replay cannot tell a transient outage from a permanent rejection")
	}
}

// TestAppend_DatabaseDown_GoalOpenFailsLoudlyWithAHint pins the other half, and
// it is the half that matters for correctness.
//
// A goal is cross-host coordination. A goal that exists only in a local spill
// file is invisible to every other host and to the sweeper, so it would look
// like work nobody is doing while an agent believes it was recorded. Failing
// loudly is the user decision recorded in the plan.
func TestAppend_DatabaseDown_GoalOpenFailsLoudlyWithAHint(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	pool.beginErr = errConnRefused
	spill := &capturingSpiller{}
	a := newTestAppender(t, pool, spill)

	_, err := a.Append(context.Background(), goalOpenedEvent(t, reg))
	if !errors.Is(err, ErrDegraded) {
		t.Fatalf("opening a goal with the DB down must fail with ErrDegraded; got: %v", err)
	}
	var hint *HintError
	if !errors.As(err, &hint) {
		t.Fatalf("the error must be a HintError carrying a next action; got %T: %v", err, err)
	}
	if hint.Hint == "" {
		t.Error("the HintError carries no hint")
	}
	if spill.count() != 0 {
		t.Errorf("a goal-open spilled %d record(s); a goal recorded only locally is invisible to every other host and must never spill", spill.count())
	}
}

// TestAppend_SchemaViolationIsNeverSpilled pins that the two failure classes
// stay separate even for a spillable type.
//
// A schema violation is an EMITTER BUG: the payload will be exactly as invalid
// on replay, so spilling it guarantees a dead-letter later and hides a bug that
// should surface now. Only transport failures are spillable.
func TestAppend_SchemaViolationIsNeverSpilled(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	pool.beginErr = errConnRefused // DB is ALSO down, so spilling is tempting
	spill := &capturingSpiller{}
	a := newTestAppender(t, pool, spill)

	ev := runStartedEvent(t, reg)
	ev.Payload = json.RawMessage(`{"agent_name":"finn"}`)

	if _, err := a.Append(context.Background(), ev); !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("got err=%v, want ErrSchemaViolation", err)
	}
	if spill.count() != 0 {
		t.Errorf("an invalid payload spilled %d record(s) — it would be just as invalid on replay, so this hides an emitter bug behind a dead letter", spill.count())
	}
}

// TestAppend_SpillFailureSurfacesRatherThanDroppingSilently pins "never a silent
// drop". If both the database and the spill are unavailable there is nothing
// left to do but tell the caller.
func TestAppend_SpillFailureSurfacesRatherThanDroppingSilently(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	pool.beginErr = errConnRefused
	spill := &capturingSpiller{err: errors.New("disk full")}
	a := newTestAppender(t, pool, spill)

	_, err := a.Append(context.Background(), runStartedEvent(t, reg))
	if err == nil {
		t.Fatal("with the database down AND the spill failing, Append must report the loss rather than return nil")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("the error should carry the spill failure; got: %v", err)
	}
}

// TestAppend_MidTransactionFailureRollsBackAndSpills pins that a connection
// that dies mid-transaction rolls back and, for a spillable type, still spills.
//
// WHY SPILLING IS SAFE HERE, since the instinct is that it risks a duplicate:
// the event id is minted BEFORE the transaction and carried in the spill record,
// and `events.id` is UNIQUE. So a replay of a record whose original append
// actually landed is refused by the database, and the replayer can read that
// refusal as "already recorded" rather than as an error. That makes replay
// idempotent, which in turn makes spilling the right call even in the genuinely
// ambiguous case — a failure AT COMMIT, where nobody can know whether the write
// landed. The alternative, declining to spill whenever the outcome is unknown,
// loses telemetry every time a connection drops at the wrong moment.
//
// The assertion on the id is therefore not incidental: it is what licenses the
// spill.
func TestAppend_MidTransactionFailureRollsBackAndSpills(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	pool.execErr["insert_event"] = errors.New("server closed the connection unexpectedly")
	spill := &capturingSpiller{}
	a := newTestAppender(t, pool, spill)

	ev := runStartedEvent(t, reg)
	ev.ID = uuid.New()
	if _, err := a.Append(context.Background(), ev); err != nil {
		t.Fatalf("a spillable event whose transaction failed must not surface an error to its emitter: %v", err)
	}

	calls := pool.log()
	if indexOf(calls, "rollback") < 0 {
		t.Errorf("no rollback after a mid-transaction failure: %v", calls)
	}
	if indexOf(calls, "commit") >= 0 {
		t.Errorf("committed despite a failed insert: %v", calls)
	}

	if spill.count() != 1 {
		t.Fatalf("spill holds %d record(s), want 1 — the event was neither committed nor spilled, which is a silent drop", spill.count())
	}
	if got := spill.records[0].EventID; got != ev.ID {
		t.Errorf("the spilled record carries event id %s but the append used %s; they must match or a replay cannot be deduplicated against events.id UNIQUE, and spilling an ambiguous outcome would risk a real duplicate", got, ev.ID)
	}
}

// TestAppend_ServerRefusalIsNotSpilled is the other side of the transport/refusal
// split, and it is the leg that stops the spill from becoming a dumping ground.
//
// A pgconn.PgError means the server RECEIVED the statement and answered — a
// constraint violation, a bad type, a missing FK. Replaying it produces the
// identical refusal, so spilling would convert a visible defect into a
// dead-letter entry nobody reads.
func TestAppend_ServerRefusalIsNotSpilled(t *testing.T) {
	reg := testRegistry(t)
	pool := newRecordingPool()
	pool.execErr["insert_event"] = &pgconn.PgError{Code: sqlStateForeignKeyViolationHermetic, Message: "insert or update on table \"events\" violates foreign key constraint"}
	spill := &capturingSpiller{}
	a := newTestAppender(t, pool, spill)

	if _, err := a.Append(context.Background(), runStartedEvent(t, reg)); err == nil {
		t.Fatal("a server-side refusal must surface as an error")
	}
	if spill.count() != 0 {
		t.Errorf("a server refusal spilled %d record(s) — it would be refused identically on replay, so this hides a real defect behind a dead letter", spill.count())
	}
}

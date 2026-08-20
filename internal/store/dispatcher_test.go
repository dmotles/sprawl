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

// Hermetic tests for the dispatcher loop.
//
// The loop is four decisions per event, and every one of them has a wrong
// version that a working-looking system cannot be distinguished from:
//
//	is this event mine?      (host affinity)   -> wrong: two hosts fight over a worktree
//	may I act on it?         (claim)           -> wrong: two hosts both spawn
//	act                      (handler)         -> wrong: nothing, this part is easy
//	record that I got here   (cursor)          -> wrong: work repeats, or is skipped
//
// Order matters as much as the decisions. The cursor is saved AFTER the handler
// returns, and asserting that is the only way to catch the reverse — which looks
// identical on every run that does not crash, and loses the event on the one that
// does.
//
// ALL OF THESE RUN WITH THE DOORBELL DISABLED (Doorbell nil is the fixture
// default). That is AC7, and it is a property of the whole file rather than one
// test: correctness is seq-cursor catch-up plus a poll, and LISTEN/NOTIFY is
// latency only. Exactly one test opts INTO a doorbell, and it exists as the
// positive control proving the nil-default suite is not passing vacuously.

// ---------------------------------------------------------------------------
// Doubles
// ---------------------------------------------------------------------------

// fakeEvents is an in-memory EventReader.
type fakeEvents struct {
	mu     sync.Mutex
	events []DispatchedEvent
	reads  []int64 // the afterSeq of every Read, in order
	err    error
}

func (f *fakeEvents) Read(_ context.Context, _ uuid.UUID, afterSeq int64, limit int) ([]DispatchedEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, afterSeq)
	if f.err != nil {
		return nil, f.err
	}
	var out []DispatchedEvent
	for _, e := range f.events {
		if e.Seq > afterSeq {
			out = append(out, e)
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// fakeClaims is an in-memory ClaimStore. It records the order of operations,
// which is what the "claim before the side effect" assertions read.
type fakeClaims struct {
	mu    sync.Mutex
	held  map[string]string // key -> owning host
	calls []string
	// refuse, when non-empty, makes Claim return false for these event ids.
	refuse map[uuid.UUID]bool
	err    error
	// claimConsumers is the consumer string of every Claim, in order.
	claimConsumers []string
	// trace, when set, also records claims into a SHARED ordered log. Needed
	// wherever an assertion spans this double and another one — the notify
	// handler's claim/record/inject ordering cannot be seen from a per-double
	// call list, only from a shared trace.
	trace *trace
}

func newFakeClaims() *fakeClaims {
	return &fakeClaims{held: map[string]string{}, refuse: map[uuid.UUID]bool{}}
}

func claimKey(id uuid.UUID, consumer string) string { return id.String() + "|" + consumer }

func (c *fakeClaims) Claim(_ context.Context, id uuid.UUID, consumer, host string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "claim:"+id.String())
	c.claimConsumers = append(c.claimConsumers, consumer)
	if c.trace != nil {
		c.trace.add("claim:" + id.String())
	}
	if c.err != nil {
		return false, c.err
	}
	if c.refuse[id] {
		return false, nil
	}
	k := claimKey(id, consumer)
	if _, taken := c.held[k]; taken {
		return false, nil
	}
	c.held[k] = host
	return true, nil
}

func (c *fakeClaims) Renew(_ context.Context, id uuid.UUID, consumer, host string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held[claimKey(id, consumer)] == host, nil
}

func (c *fakeClaims) TakeoverExpired(_ context.Context, id uuid.UUID, consumer, host string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "takeover:"+id.String())
	return false, nil
}

func (c *fakeClaims) Release(_ context.Context, id uuid.UUID, consumer, host string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "release:"+id.String())
	if c.held[claimKey(id, consumer)] == host {
		delete(c.held, claimKey(id, consumer))
	}
	return nil
}

// consumers records the claim CONSUMER string of every Claim call, which is what
// the per-recipient notification key is asserted on: the ordered call log keys on
// event id, so it cannot distinguish a claim taken per event from one taken per
// recipient.
func (c *fakeClaims) consumers() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.claimConsumers...)
}

func (c *fakeClaims) log() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// memCursor is an in-memory CursorStore that records the ORDER of saves relative
// to handler calls, via a shared trace.
type memCursor struct {
	mu    sync.Mutex
	at    map[string]int64
	trace *trace
	err   error
}

func newMemCursor(tr *trace) *memCursor {
	return &memCursor{at: map[string]int64{}, trace: tr}
}

func (c *memCursor) Load(consumer string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at[consumer], nil
}

func (c *memCursor) Save(consumer string, seq int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.at[consumer] = seq
	if c.trace != nil {
		c.trace.add(fmt.Sprintf("cursor:%d", seq))
	}
	return nil
}

func (c *memCursor) get(consumer string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at[consumer]
}

// trace is a shared ordered log so a test can assert handler-vs-cursor ordering.
type trace struct {
	mu   sync.Mutex
	seen []string
}

func (t *trace) add(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seen = append(t.seen, s)
}

func (t *trace) all() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.seen...)
}

// recordingHandler records every event it is given and can be told to fail.
type recordingHandler struct {
	mu       sync.Mutex
	handled  []int64
	trace    *trace
	err      error
	failOn   map[int64]error
	onHandle func()
}

func (h *recordingHandler) Handle(_ context.Context, ev DispatchedEvent) error {
	h.mu.Lock()
	h.handled = append(h.handled, ev.Seq)
	if h.trace != nil {
		h.trace.add(fmt.Sprintf("handle:%d", ev.Seq))
	}
	err := h.err
	if e, ok := h.failOn[ev.Seq]; ok {
		err = e
	}
	cb := h.onHandle
	h.mu.Unlock()
	if cb != nil {
		cb()
	}
	return err
}

func (h *recordingHandler) seqs() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int64(nil), h.handled...)
}

func (h *recordingHandler) count() int { return len(h.seqs()) }

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type dispatchFixture struct {
	events  *fakeEvents
	claims  *fakeClaims
	cursor  *memCursor
	handler *recordingHandler
	trace   *trace
	deps    DispatcherDeps
}

const dispatchType = "run_started"

func newDispatchFixture(t *testing.T, seqs ...int64) *dispatchFixture {
	t.Helper()
	tr := &trace{}
	reg := testRegistry(t)
	schema, ok := reg.ByName(dispatchType, 1)
	if !ok {
		t.Fatalf("%s@1 missing from the seed registry", dispatchType)
	}

	ev := &fakeEvents{}
	for _, s := range seqs {
		ev.events = append(ev.events, DispatchedEvent{
			Seq: s, ID: uuid.New(), SchemaID: schema.ID,
			SchemaName: schema.Name, SchemaVersion: schema.Version,
			Payload: json.RawMessage(`{}`),
		})
	}
	h := &recordingHandler{trace: tr, failOn: map[int64]error{}}
	f := &dispatchFixture{
		events: ev, claims: newFakeClaims(), cursor: newMemCursor(tr),
		handler: h, trace: tr,
	}
	f.deps = DispatcherDeps{
		Events:    ev,
		Claims:    f.claims,
		Cursor:    f.cursor,
		Registry:  reg,
		ProjectID: uuid.New(),
		Host:      "host-a",
		Consumer:  "dispatcher",
		Handlers:  map[string]Handler{dispatchType: h},
		// Doorbell deliberately nil — see the file header (AC7).
	}
	return f
}

func (f *dispatchFixture) dispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	d, err := NewDispatcher(f.deps)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return d
}

// step runs one catch-up pass and fails the test on error.
func (f *dispatchFixture) step(t *testing.T, d *Dispatcher) {
	t.Helper()
	if _, err := d.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The scan
// ---------------------------------------------------------------------------

// The catch-up scan reads strictly AFTER the cursor, in seq order, bounded.
//
// A mechanism assertion on the query text, and deliberately so: the SQL shape IS
// the requirement the issue states ("SELECT ... WHERE seq > last_seen_seq ORDER
// BY seq"), and the three properties in it are each a distinct silent defect.
// `>=` re-delivers one event forever. A missing ORDER BY lets Postgres return
// rows in any order it likes, so a close can be dispatched before its opener and
// the cursor advances past the opener unhandled. No LIMIT loads an entire
// project's history into memory on first run.
func TestDispatcher_ScanQueryIsCursorOrderedAndBounded(t *testing.T) {
	// Table aliases are stripped before matching. Without this the assertions
	// below key on the exact spelling `seq` and go RED on the correct query the
	// moment someone writes `e.seq` — which is what happened on the first run of
	// this test, and a false red trains a reader to weaken the assertion rather
	// than the alias.
	sql := strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(eventScanSQL), " ")), "e.", "")

	if !strings.Contains(sql, "seq > $") {
		t.Errorf("the scan is not strictly after the cursor; `seq >= $` re-delivers one event forever: %s", sql)
	}
	if !strings.Contains(sql, "order by seq") {
		t.Errorf("the scan has no ORDER BY seq, so events may arrive out of order and a close can be dispatched before its opener: %s", sql)
	}
	if !strings.Contains(sql, "limit") {
		t.Errorf("the scan is unbounded, so a first run on an established project reads the whole log into memory: %s", sql)
	}
	if !strings.Contains(sql, "project_id = $") {
		t.Errorf("the scan is not scoped to a project, so a host would dispatch another project's events: %s", sql)
	}
}

// A pass reads from the CURSOR, not from zero.
func TestDispatcher_ScanStartsFromTheStoredCursor(t *testing.T) {
	f := newDispatchFixture(t, 1, 2, 3)
	if err := f.cursor.Save("dispatcher", 2); err != nil {
		t.Fatalf("seeding the cursor: %v", err)
	}
	d := f.dispatcher(t)
	f.step(t, d)

	if got := f.handler.seqs(); len(got) != 1 || got[0] != 3 {
		t.Errorf("handled %v, want only seq 3 — the pass did not start from the stored cursor", got)
	}
}

// ---------------------------------------------------------------------------
// Cursor discipline
// ---------------------------------------------------------------------------

// THE CURSOR IS SAVED AFTER THE HANDLER RETURNS.
//
// This is the ordering AC1 rests on and the one a passing run cannot reveal:
// save-then-handle and handle-then-save produce identical output on every
// execution that does not crash. They differ only on the execution that does,
// and there the wrong order loses the event outright — the cursor says "done"
// for work that never happened, no claim is released, no lease expires, nothing
// comes back for it.
func TestDispatcher_CursorIsSavedAfterTheHandlerNotBefore(t *testing.T) {
	f := newDispatchFixture(t, 1, 2)
	d := f.dispatcher(t)
	f.step(t, d)

	got := f.trace.all()
	want := []string{"handle:1", "cursor:1", "handle:2", "cursor:2"}
	if len(got) != len(want) {
		t.Fatalf("trace = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("trace = %v, want %v — the cursor must be recorded only after the side effect has happened", got, want)
		}
	}
}

// THE CURSOR ADVANCES PAST AN EVENT NO HANDLER WANTS.
//
// Without this, the first event of a type this consumer does not handle wedges
// the host permanently: it is re-read on every poll, forever, and no event after
// it is ever dispatched. The log deliberately carries many types per project and
// each consumer handles a few, so unhandled types are the COMMON case, not an
// edge one.
//
// Note it must also not CLAIM them — claiming an event you are not going to act
// on would make the claim table grow with one row per event per consumer and,
// worse, would make a genuine handler added later see every historical event as
// already claimed.
func TestDispatcher_UnhandledTypesAdvanceTheCursorWithoutClaiming(t *testing.T) {
	f := newDispatchFixture(t, 1, 2)
	// No handler at all for this consumer.
	f.deps.Handlers = map[string]Handler{}
	d := f.dispatcher(t)
	f.step(t, d)

	if got := f.cursor.get("dispatcher"); got != 2 {
		t.Errorf("cursor is at %d after a pass over two unhandled events, want 2 — this host will re-read them forever and never reach anything after them", got)
	}
	if got := f.claims.log(); len(got) != 0 {
		t.Errorf("unhandled events were claimed: %v", got)
	}
}

// ---------------------------------------------------------------------------
// Claims
// ---------------------------------------------------------------------------

// The claim happens BEFORE the handler, and losing it means the handler is not
// called at all.
func TestDispatcher_LosingTheClaimSkipsTheHandlerButStillAdvances(t *testing.T) {
	f := newDispatchFixture(t, 1, 2)
	f.claims.refuse[f.events.events[0].ID] = true
	d := f.dispatcher(t)
	f.step(t, d)

	if got := f.handler.seqs(); len(got) != 1 || got[0] != 2 {
		t.Errorf("handled %v, want only seq 2 — the event whose claim was lost must not be acted on", got)
	}
	// Advancing past a lost claim is correct: another host owns that event, and
	// blocking on it would make one busy peer stall this host indefinitely.
	if got := f.cursor.get("dispatcher"); got != 2 {
		t.Errorf("cursor is at %d, want 2 — a claim lost to another host must not block this host", got)
	}
}

// A LOST CLAIM IS OFFERED A TAKEOVER BEFORE IT IS SKIPPED.
//
// Without this the lease column is decorative and a crashed host's events are
// dispatched by nobody, ever. It is asserted here as a CALL — that the
// dispatcher asks — because the answer depends on the database clock and the
// hermetic fake cannot model an expiry; the outcome is asserted for real in
// dispatcher_integration_test.go.
//
// The order is part of the claim: the plain insert must be tried FIRST and
// unconditionally, so the normal path can never steal. A dispatcher that asked
// for a takeover before attempting the insert would turn every claim into a
// steal while passing every other test in this file.
func TestDispatcher_LostClaimAttemptsATakeoverBeforeSkipping(t *testing.T) {
	f := newDispatchFixture(t, 1)
	id := f.events.events[0].ID
	f.claims.refuse[id] = true
	d := f.dispatcher(t)
	f.step(t, d)

	got := f.claims.log()
	want := []string{"claim:" + id.String(), "takeover:" + id.String()}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("claim log = %v, want %v — the insert must be attempted first, and a lost claim must be offered a takeover rather than skipped outright", got, want)
	}
}

// A SUCCESSFUL takeover leads to the handler running.
//
// Positive control for the test above: asking for a takeover and then ignoring
// the answer would satisfy the call-order assertion perfectly while never
// recovering a single crashed host's event.
func TestDispatcher_SuccessfulTakeoverRunsTheHandler(t *testing.T) {
	f := newDispatchFixture(t, 1)
	id := f.events.events[0].ID
	f.claims.refuse[id] = true
	f.deps.Claims = takeoverGranting{ClaimStore: f.claims}
	d := f.dispatcher(t)
	f.step(t, d)

	if got := f.handler.seqs(); len(got) != 1 || got[0] != 1 {
		t.Errorf("handled %v, want seq 1 — a won takeover must lead to the work actually being done", got)
	}
}

// takeoverGranting loses every Claim and wins every TakeoverExpired.
type takeoverGranting struct{ ClaimStore }

func (t takeoverGranting) TakeoverExpired(context.Context, uuid.UUID, string, string, time.Duration) (bool, error) {
	return true, nil
}

// A takeover FAILURE stops the pass rather than being read as "the lease is
// live".
//
// Same shape as the claim-failure assertion below and for the same reason: a
// transport error folded into "somebody else has it" makes the dispatcher skip
// the event and advance past it, and nothing brings it back.
func TestDispatcher_TakeoverErrorStopsThePass(t *testing.T) {
	f := newDispatchFixture(t, 1)
	f.claims.refuse[f.events.events[0].ID] = true
	f.deps.Claims = takeoverFailing{ClaimStore: f.claims}
	d := f.dispatcher(t)

	if _, err := d.Step(context.Background()); err == nil {
		t.Fatal("Step succeeded despite a takeover failure; the event would be skipped and advanced past on a transport blip")
	}
	if got := f.cursor.get("dispatcher"); got != 0 {
		t.Errorf("cursor advanced to %d on a takeover failure", got)
	}
}

type takeoverFailing struct{ ClaimStore }

func (takeoverFailing) TakeoverExpired(context.Context, uuid.UUID, string, string, time.Duration) (bool, error) {
	return false, errors.New("connection refused")
}

// A CLAIM FAILURE IS NOT A LOST CLAIM. The pass stops and the cursor does not
// move past the event.
//
// The counterpart to the transport assertion in claims_test.go, one level up: if
// the dispatcher treated a claim error as "somebody else has it" it would skip
// the event AND advance past it, and — because no claim row was ever written —
// nothing would ever bring it back. The event is lost by a network blip.
func TestDispatcher_ClaimErrorStopsThePassWithoutAdvancing(t *testing.T) {
	f := newDispatchFixture(t, 1, 2)
	f.claims.err = errors.New("connection refused")
	d := f.dispatcher(t)

	if _, err := d.Step(context.Background()); err == nil {
		t.Fatal("Step succeeded despite a claim failure; the caller has no way to know dispatch is not happening")
	}
	if got := f.handler.count(); got != 0 {
		t.Errorf("handler ran %d times despite the claim failing", got)
	}
	if got := f.cursor.get("dispatcher"); got != 0 {
		t.Errorf("cursor advanced to %d on a claim failure; those events would never be dispatched by anyone", got)
	}
}

// ---------------------------------------------------------------------------
// Host affinity (AC3)
// ---------------------------------------------------------------------------

// An event affined to ANOTHER host is neither claimed nor handled.
//
// Worktree-bound work is the reason: a step resuming an existing worktree can
// only run where that worktree is, and a second host acting on it would either
// fail confusingly or — worse — create a second worktree for the same agent.
func TestDispatcher_EventAffinedToAnotherHostIsNotClaimedOrHandled(t *testing.T) {
	f := newDispatchFixture(t, 1)
	f.events.events[0].Payload = json.RawMessage(`{"host_affinity":"host-b"}`)
	d := f.dispatcher(t)
	f.step(t, d)

	if got := f.handler.count(); got != 0 {
		t.Errorf("host-a handled an event affined to host-b (%d calls)", got)
	}
	if got := f.claims.log(); len(got) != 0 {
		t.Errorf("host-a claimed an event affined to host-b: %v — the owning host would then find it already taken", got)
	}
	if got := f.cursor.get("dispatcher"); got != 1 {
		t.Errorf("cursor is at %d, want 1 — another host's event must not block this host's scan", got)
	}
}

// POSITIVE CONTROL, and AC3 explicitly asks for it: the SAME fixture with the
// affinity naming THIS host is claimed and handled.
//
// Direction stated because it is the whole point: without this leg, a dispatcher
// that refused every affined event — or every event at all — would satisfy the
// assertion above perfectly, and worktree-bound work would simply never run.
func TestDispatcher_EventAffinedToThisHostIsClaimedAndHandled(t *testing.T) {
	f := newDispatchFixture(t, 1)
	f.events.events[0].Payload = json.RawMessage(`{"host_affinity":"host-a"}`)
	d := f.dispatcher(t)
	f.step(t, d)

	if got := f.handler.seqs(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("handled %v, want seq 1 — an event affined to THIS host must run here", got)
	}
	if got := f.claims.log(); len(got) == 0 || !strings.HasPrefix(got[0], "claim:") {
		t.Errorf("claim log = %v, want a claim first", got)
	}
}

// An UNAFFINED event is claimable by any host — the design's default.
//
// The third leg of the affinity triple. Without it, an implementation that
// treated "no affinity" as "affined to nobody" would pass both tests above while
// dispatching nothing at all, since almost every event carries no affinity.
func TestDispatcher_UnaffinedEventIsClaimedByAnyHost(t *testing.T) {
	for _, host := range []string{"host-a", "host-b", "some-other-host"} {
		f := newDispatchFixture(t, 1)
		f.deps.Host = host
		d := f.dispatcher(t)
		f.step(t, d)
		if got := f.handler.count(); got != 1 {
			t.Errorf("host %q handled %d unaffined events, want 1", host, got)
		}
	}
}

// A payload whose host_affinity is not a string is a REFUSAL, not a shrug.
//
// Reading it as "unaffined" would silently convert a malformed worktree-bound
// event into one any host may claim — which is precisely the outcome affinity
// exists to prevent, arriving through a type error.
func TestDispatcher_MalformedHostAffinityIsRefusedNotTreatedAsUnaffined(t *testing.T) {
	f := newDispatchFixture(t, 1)
	f.events.events[0].Payload = json.RawMessage(`{"host_affinity":42}`)
	d := f.dispatcher(t)

	if _, err := d.Step(context.Background()); err == nil {
		t.Fatal("Step accepted a non-string host_affinity; a malformed affinity must not degrade into 'any host may claim this'")
	}
	if got := f.handler.count(); got != 0 {
		t.Errorf("handler ran %d times on an event with a malformed affinity", got)
	}
}

// ---------------------------------------------------------------------------
// Handler failure
// ---------------------------------------------------------------------------

// A FAILING HANDLER BLOCKS THE CURSOR RATHER THAN SKIPPING THE EVENT, and
// releases its claim so a retry is possible.
//
// This is a deliberate choice with a real cost, so it is asserted rather than
// left to chance. The alternative — advance past the failure and carry on — is a
// SILENT DROP: the event is never retried by anyone, because the cursor says
// this host is done with it and the claim is gone. Blocking is head-of-line
// blocking, which is worse for throughput and better for correctness, and it is
// LOUD: the cursor visibly stops advancing while the log keeps growing.
//
// Releasing the claim is what makes the retry possible at all. Keeping it would
// mean waiting out the whole lease before anyone could try again.
func TestDispatcher_HandlerFailureBlocksTheCursorAndReleasesTheClaim(t *testing.T) {
	f := newDispatchFixture(t, 1, 2, 3)
	f.handler.failOn[2] = errors.New("worktree creation failed")
	d := f.dispatcher(t)

	if _, err := d.Step(context.Background()); err == nil {
		t.Fatal("Step reported success despite a handler failure")
	}

	// Seq 1 committed; seq 2 failed; seq 3 was never reached.
	if got := f.cursor.get("dispatcher"); got != 1 {
		t.Errorf("cursor is at %d, want 1 — progress before the failure must be kept, and the failing event must NOT be passed over", got)
	}
	if got := f.handler.seqs(); len(got) != 2 {
		t.Errorf("handled %v, want to stop at the failure rather than continuing past it", got)
	}
	failedID := f.events.events[1].ID
	if !contains(f.claims.log(), "release:"+failedID.String()) {
		t.Errorf("the failed event's claim was not released (%v); nobody can retry until the lease runs out", f.claims.log())
	}
}

// The retry actually happens on the next pass. Positive control for the test
// above: without it, "blocks the cursor" would be satisfied by a dispatcher that
// simply stopped forever.
func TestDispatcher_ABlockedEventIsRetriedOnTheNextPass(t *testing.T) {
	f := newDispatchFixture(t, 1)
	f.handler.failOn[1] = errors.New("transient")
	d := f.dispatcher(t)

	if _, err := d.Step(context.Background()); err == nil {
		t.Fatal("Step reported success despite a handler failure")
	}
	// The condition clears.
	f.handler.mu.Lock()
	delete(f.handler.failOn, 1)
	f.handler.mu.Unlock()

	f.step(t, d)
	if got := f.cursor.get("dispatcher"); got != 1 {
		t.Errorf("cursor is at %d after a successful retry, want 1", got)
	}
	if got := f.handler.count(); got != 2 {
		t.Errorf("handler was called %d times, want 2 (the failure and the retry)", got)
	}
}

// A cursor Save failure does not get read as success.
//
// If it did, the dispatcher would carry on with an in-memory position the file
// does not have, and a restart would re-dispatch everything since the last
// durable save. Claims absorb that safely, but silently: nobody would learn the
// cursor had stopped persisting.
func TestDispatcher_CursorSaveFailureIsReported(t *testing.T) {
	f := newDispatchFixture(t, 1)
	f.cursor.err = errors.New("disk full")
	d := f.dispatcher(t)

	if _, err := d.Step(context.Background()); err == nil {
		t.Fatal("Step reported success while unable to persist its cursor")
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// A dispatcher with no host, no consumer, or no project is refused at
// construction rather than misbehaving at run time.
//
// Every one of these has a silent failure mode downstream: an empty host makes
// affinity match nothing and makes claims un-renewable (see claims.go); an empty
// consumer merges this consumer's claims with another's; the nil project id
// scopes the scan to no project, so the loop polls forever and dispatches
// nothing, which looks exactly like an idle system.
func TestNewDispatcher_RefusesAnIncompleteConfiguration(t *testing.T) {
	base := func(t *testing.T) DispatcherDeps {
		f := newDispatchFixture(t)
		return f.deps
	}
	cases := map[string]func(d *DispatcherDeps){
		"no host":     func(d *DispatcherDeps) { d.Host = "" },
		"no consumer": func(d *DispatcherDeps) { d.Consumer = "" },
		"no project":  func(d *DispatcherDeps) { d.ProjectID = uuid.Nil },
		"no events":   func(d *DispatcherDeps) { d.Events = nil },
		"no claims":   func(d *DispatcherDeps) { d.Claims = nil },
		"no cursor":   func(d *DispatcherDeps) { d.Cursor = nil },
		"no registry": func(d *DispatcherDeps) { d.Registry = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			deps := base(t)
			mutate(&deps)
			if _, err := NewDispatcher(deps); err == nil {
				t.Errorf("NewDispatcher accepted a configuration with %s", name)
			}
		})
	}
}

// A complete configuration IS accepted — the negative control for the test
// above, which a constructor that refused everything would otherwise satisfy.
func TestNewDispatcher_AcceptsACompleteConfiguration(t *testing.T) {
	f := newDispatchFixture(t)
	if _, err := NewDispatcher(f.deps); err != nil {
		t.Fatalf("NewDispatcher refused a complete configuration: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The poll loop and the doorbell (AC7)
// ---------------------------------------------------------------------------

// RUN MAKES PROGRESS WITH THE DOORBELL DISABLED. This is AC7 stated as a test
// rather than as a property of the fixture defaults.
//
// Correctness is the poll; NOTIFY is latency. A dispatcher that only woke on a
// doorbell would appear to work perfectly in any environment where notifications
// flow and would silently stop dispatching the moment one was dropped — and
// pg_notify gives no delivery guarantee across a reconnect, so dropping one is
// normal operation, not a fault.
func TestDispatcher_RunMakesProgressWithTheDoorbellDisabled(t *testing.T) {
	f := newDispatchFixture(t, 1, 2)
	f.deps.Doorbell = nil
	f.deps.Poll = func() time.Duration { return time.Millisecond }

	d := f.dispatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	f.handler.onHandle = func() {
		if f.handler.count() >= 2 {
			cancel()
		}
	}
	go func() { done <- d.Run(ctx) }()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Run made no progress in 10s with the doorbell disabled; correctness must not depend on LISTEN/NOTIFY")
	}
	if got := f.cursor.get("dispatcher"); got != 2 {
		t.Errorf("cursor is at %d, want 2", got)
	}
}

// POSITIVE CONTROL for the doorbell-disabled default: with a doorbell wired, it
// IS consulted.
//
// Direction: the whole file runs with Doorbell nil, so nothing else here would
// notice if the doorbell code path were dead — or, worse, if `Doorbell != nil`
// silently disabled polling. This asserts the wire exists and is used; the test
// above asserts nothing depends on it.
func TestDispatcher_DoorbellIsConsultedWhenConfigured(t *testing.T) {
	f := newDispatchFixture(t, 1)
	rung := make(chan struct{}, 8)
	f.deps.Doorbell = doorbellFunc(func(ctx context.Context, d time.Duration) {
		select {
		case rung <- struct{}{}:
		default:
		}
		<-ctx.Done()
	})
	f.deps.Poll = func() time.Duration { return time.Hour } // so only the doorbell can be waiting

	d := f.dispatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	select {
	case <-rung:
	case <-time.After(10 * time.Second):
		t.Fatal("Run never consulted the configured doorbell, so the latency path is dead code")
	}
}

// doorbellFunc adapts a function to Doorbell.
type doorbellFunc func(ctx context.Context, d time.Duration)

func (f doorbellFunc) Wait(ctx context.Context, d time.Duration) { f(ctx, d) }

// Run returns when its context is cancelled, rather than spinning or hanging.
func TestDispatcher_RunStopsOnContextCancel(t *testing.T) {
	f := newDispatchFixture(t)
	f.deps.Poll = func() time.Duration { return time.Millisecond }
	d := f.dispatcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want nil or context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of its context being cancelled")
	}
}

// A failing pass does not kill the loop.
//
// Run is the process's whole reason to exist; if a transient claim error ended
// it, a two-second database blip would stop dispatch until somebody noticed and
// restarted the process by hand.
func TestDispatcher_RunSurvivesAFailingPass(t *testing.T) {
	f := newDispatchFixture(t, 1)
	f.deps.Poll = func() time.Duration { return time.Millisecond }
	f.claims.err = errors.New("connection refused")

	d := f.dispatcher(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := d.Run(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run exited with %v; a transient failure must not end the loop", err)
	}
	// It kept trying rather than giving up after the first failure.
	f.events.mu.Lock()
	reads := len(f.events.reads)
	f.events.mu.Unlock()
	if reads < 2 {
		t.Errorf("the loop made %d scan attempts across 300ms of failures, want at least 2 — it gave up after the first", reads)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

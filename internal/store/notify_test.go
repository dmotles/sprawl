package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Notification delivery: the event log IS the notification queue.
//
// The shape that matters, and the reason this is a CONTRACT rather than a fire
// and forget: OWNER_NOTIFY opens when a result lands, NOTIFY_ACKED closes it at
// the recipient's next turn boundary. So a delivery that never arrived stays
// visible as outstanding work instead of being assumed. The plan of record says
// it in as many words — "notification is itself an open/close pair so lost
// notifications are swept, not assumed".
//
// Which makes the ORDER load-bearing in the opposite direction from the spawn
// write-ahead, and it is worth being explicit because the two look alike:
//
//	spawn:  record the intent, THEN act. A crash leaves a record with no resource,
//	        which reconciliation can resolve.
//	notify: record the notify, THEN inject. A crash — or a failed injection —
//	        leaves an OPEN CONTRACT, which the sweeper re-delivers.
//
// Both put the log first. In both cases the alternative loses the only evidence
// that anything was supposed to happen.

// ---------------------------------------------------------------------------
// Doubles
// ---------------------------------------------------------------------------

// recordingInjector captures what was pushed into whose stream.
type recordingInjector struct {
	mu        sync.Mutex
	delivered []injected
	trace     *trace
	err       error
}

type injected struct {
	Recipient string
	Body      string
}

func (i *recordingInjector) Inject(_ context.Context, recipient, body string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.trace != nil {
		i.trace.add("inject:" + recipient)
	}
	if i.err != nil {
		return i.err
	}
	i.delivered = append(i.delivered, injected{Recipient: recipient, Body: body})
	return nil
}

func (i *recordingInjector) all() []injected {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]injected(nil), i.delivered...)
}

func (i *recordingInjector) count() int { return len(i.all()) }

// fakeEventLookup resolves an event by id — the opener a close refers to.
type fakeEventLookup struct {
	mu     sync.Mutex
	events map[uuid.UUID]DispatchedEvent
	err    error
}

func (l *fakeEventLookup) ByID(_ context.Context, id uuid.UUID) (DispatchedEvent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return DispatchedEvent{}, l.err
	}
	ev, ok := l.events[id]
	if !ok {
		return DispatchedEvent{}, fmt.Errorf("no event %s", id)
	}
	return ev, nil
}

// fakeOpenNotifies is the reader the ack consumer uses.
//
// IT ENFORCES THE SAME PRECONDITION AS THE REAL READER — an empty recipient is
// an error, exactly as in PgNotifyReader.OpenNotifies. That is not politeness: a
// fake more permissive than the implementation it stands for hides precisely the
// defect the caller's guard exists to prevent. Measured, on the first version of
// this file: with the handler's empty-name guard removed, a permissive fake
// returned "no notifications for recipient ”" and the test stayed GREEN, so the
// control could not fire. Under the real reader that same code path errors.
type fakeOpenNotifies struct {
	mu       sync.Mutex
	open     []OpenNotify
	askedFor []string
	err      error
}

func (f *fakeOpenNotifies) OpenNotifies(_ context.Context, _ uuid.UUID, recipient string) ([]OpenNotify, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.askedFor = append(f.askedFor, recipient)
	if recipient == "" {
		return nil, errors.New("fake: an empty recipient would match every notification with no recipient (PgNotifyReader refuses this too)")
	}
	if f.err != nil {
		return nil, f.err
	}
	var out []OpenNotify
	for _, o := range f.open {
		if o.Recipient == recipient {
			out = append(out, o)
		}
	}
	return out, nil
}

func (f *fakeOpenNotifies) queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.askedFor...)
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type notifyFixture struct {
	trace    *trace
	claims   *fakeClaims
	emitter  *recordingEmitter
	injector *recordingInjector
	lookup   *fakeEventLookup
	handler  *NotifyHandler
	notifies *trackingNotifies
	local    *fakeLocalAgents
	goalID   uuid.UUID
	closeEv  DispatchedEvent
}

func newNotifyFixture(t *testing.T, owner string) *notifyFixture {
	t.Helper()
	tr := &trace{}
	claims := newFakeClaims()
	claims.trace = tr
	f := &notifyFixture{
		trace:    tr,
		claims:   claims,
		notifies: newTrackingNotifies(),
		emitter:  &recordingEmitter{trace: tr},
		injector: &recordingInjector{trace: tr},
		lookup:   &fakeEventLookup{events: map[uuid.UUID]DispatchedEvent{}},
		goalID:   uuid.New(),
	}

	ownerJSON := "null"
	if owner != "" {
		ownerJSON = `"` + owner + `"`
	}
	f.lookup.events[f.goalID] = DispatchedEvent{
		ID: f.goalID, SchemaName: "goal_opened", SchemaVersion: 1,
		Payload: json.RawMessage(fmt.Sprintf(`{"goal_type":"research","text":"find out","owner":%s}`, ownerJSON)),
	}
	f.closeEv = DispatchedEvent{
		Seq: 10, ID: uuid.New(), SchemaName: "goal_closed", SchemaVersion: 1,
		WorkflowInstanceID: uuid.New(),
		ClosesEventID:      &f.goalID,
		Payload:            json.RawMessage(`{"outcome":"success","summary":"done"}`),
	}

	// Recording an owner_notify OPENS its contract, because that is what the
	// appender does inside the append transaction. Without this the fixture's
	// outstanding-check would always answer "not outstanding" and no test could
	// reach the injection at all.
	f.emitter.onEmit = func(req EmitRequest, id uuid.UUID) {
		if req.TypeName != "owner_notify" {
			return
		}
		// The RECIPIENT comes from the payload, not from a literal. An earlier
		// version hardcoded "weave" here, so every fixture with a different owner
		// opened its contract under the wrong name and the handler's
		// outstanding-check answered "not outstanding" — which made five
		// owner-dead tests fail for a reason that had nothing to do with them.
		fields, ok := req.Payload.(map[string]any)
		if !ok {
			return
		}
		recipient, _ := fields["recipient"].(string)
		f.notifies.opened(id, recipient)
	}
	h, err := NewNotifyHandler(NotifyHandlerDeps{
		Emitter:  f.emitter,
		Injector: f.injector,
		Lookup:   f.lookup,
		Notifies: f.notifies,
		Host:     "host-a",
		Consumer: "dispatcher",
	})
	if err != nil {
		t.Fatalf("NewNotifyHandler: %v", err)
	}
	f.handler = h
	return f
}

// ---------------------------------------------------------------------------
// Delivery
// ---------------------------------------------------------------------------

// RECORD, THEN INJECT — in that order.
//
// The record before the injection because a failed injection has to leave
// something to retry against; the reverse order delivers with no trace, so a
// half-happened delivery is indistinguishable from one that never started.
//
// There is no longer a claim in this sequence, and that is the code-review fix:
// a claim taken before the record is an "attempted" marker used as a "done"
// marker. Exclusion now comes from the derived event id and `events.id UNIQUE` —
// the database rather than a convention. See derived.go.
func TestNotifyHandler_RecordsThenInjects(t *testing.T) {
	f := newNotifyFixture(t, "weave")

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got := f.trace.all()
	want := []string{"emit:owner_notify", "inject:weave"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("trace = %v, want %v", got, want)
	}
	if claims := f.claims.log(); len(claims) != 0 {
		t.Errorf("the notify handler took %v; it must not hold a claim across its append — that was the permanent-loss defect", claims)
	}
}

// A SECOND PASS OVER A DELIVERED NOTIFICATION DELIVERS NOTHING.
//
// Exactly-once, now carried by the derived id plus the contract state rather than
// by a claim. The second pass's append is refused as a duplicate and the contract
// has been acked, so there is nothing to do.
func TestNotifyHandler_SecondPassOverADeliveredNotificationDoesNothing(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	f.notifies.ack(notifyIDFor(f.closeEv.ID, "weave"))

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if got := f.injector.count(); got != 1 {
		t.Errorf("injected %d times across two passes, want 1", got)
	}
	if got := len(f.emitter.byName("owner_notify")); got != 1 {
		t.Errorf("%d owner_notify events, want 1 — a second contract is a second thing the ack has to close", got)
	}
}

// ANOTHER HOST'S RECORD SUPPRESSES THIS HOST'S DELIVERY.
//
// The database refused our append as a duplicate, so somebody else recorded it.
// If their delivery is still outstanding this host WILL inject (that is the
// retry), and if it is acked this host does nothing — asserted separately above.
// What is asserted here is that a duplicate append is not read as a FAILURE: the
// old code returned nil on a lost claim and the dispatcher advanced; this code
// must neither fail the pass nor double-append.
func TestNotifyHandler_ADuplicateAppendIsNotAFailure(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	id := notifyIDFor(f.closeEv.ID, "weave")
	// Another host got there first, and its delivery already landed and acked.
	f.emitter.uniqueViolationOn = map[uuid.UUID]bool{id: true}

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle treated a duplicate append as a failure: %v", err)
	}
	if got := len(f.emitter.byName("owner_notify")); got != 0 {
		t.Errorf("%d owner_notify events recorded despite the duplicate refusal", got)
	}
	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d times for a notification another host had already delivered and had acked", got)
	}
}

// NO OWNER MEANS NO NOTIFICATION, and nothing is claimed either.
//
// A contract with no owner is legitimate — a system-opened goal, say — and
// notifying a nonexistent recipient would either fail loudly on every pass or,
// worse, open a contract that can never be acked because there is nobody to take
// a turn.
func TestNotifyHandler_NoOwnerMeansNoNotification(t *testing.T) {
	f := newNotifyFixture(t, "")

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d times for an unowned contract", got)
	}
	if got := f.claims.log(); len(got) != 0 {
		t.Errorf("claimed %v for an unowned contract", got)
	}
	if got := f.emitter.names(); len(got) != 0 {
		t.Errorf("emitted %v for an unowned contract — an owner_notify with no recipient can never be acked", got)
	}
}

// An event that closes nothing is not a landing result and is ignored.
func TestNotifyHandler_NonClosingEventIsIgnored(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	ev := f.closeEv
	ev.ClosesEventID = nil

	if err := f.handler.Handle(context.Background(), ev); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d times for an event that closes nothing", got)
	}
}

// A FAILED INJECTION LEAVES THE CONTRACT OPEN, and reports the failure.
//
// This is the whole reason the record comes first. The contract stays open and the
// error surfaces, so the dispatcher keeps its cursor on the event and the NEXT
// PASS retries the delivery (see TestNotifyHandler_RetryAfterAFailedInjectionReDelivers).
// An earlier version of this comment said the sweeper re-delivered; it does not,
// and no such code ever existed.
func TestNotifyHandler_FailedInjectionLeavesTheContractOpenAndReportsIt(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	f.injector.err = errors.New("recipient stdin is wedged")

	err := f.handler.Handle(context.Background(), f.closeEv)
	if err == nil {
		t.Fatal("Handle reported success although the injection failed; the dispatcher would advance past a result nobody was told about")
	}
	notifies := f.emitter.byName("owner_notify")
	if len(notifies) != 1 {
		t.Fatalf("%d owner_notify events, want 1 — a failed delivery must leave a record to retry against", len(notifies))
	}
	if got := f.emitter.byName("notify_acked"); len(got) != 0 {
		t.Errorf("emitted %v after a FAILED injection; the contract must stay open so the next pass retries", got)
	}
}

// THE BODY IS A SHORT SUMMARY NAMING THE EVENT, never a payload dump.
//
// Not a style rule: Real.SendMessage rejects a body over 500 runes outright (it
// is the first statement in the function, so a rejected send has no side
// effects), and documents 300. A notification that grew with its payload would
// therefore start failing to deliver at exactly the moment a result got
// interesting. The event id is what makes the short form sufficient — the
// recipient reads the detail out of the log.
func TestNotifyHandler_BodyIsAShortSummaryNamingTheEvent(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	huge := strings.Repeat("x", 4000)
	f.closeEv.Payload = json.RawMessage(fmt.Sprintf(`{"outcome":"success","summary":%q}`, huge))

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := f.injector.all()
	if len(got) != 1 {
		t.Fatalf("injected %d times, want 1", len(got))
	}
	if n := utf8.RuneCountInString(got[0].Body); n > notifyBodyMaxRunes {
		t.Errorf("the notification body is %d runes, over the %d cap; Real.SendMessage would refuse it outright and the delivery would fail for every large result", n, notifyBodyMaxRunes)
	}
	if !strings.Contains(got[0].Body, f.closeEv.ID.String()) {
		t.Errorf("the body does not name the event, so a truncated summary leaves the recipient no way to read the detail: %q", got[0].Body)
	}
	if strings.Contains(got[0].Body, huge[:200]) {
		t.Errorf("the body inlines the payload rather than pointing at it: %q", got[0].Body)
	}
}

// A short result's body is still a summary and still names the event — the
// negative control for the cap assertion, which a handler that always emitted an
// empty body would otherwise satisfy.
func TestNotifyHandler_ShortResultStillNamesTheEventAndItsOutcome(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	body := f.injector.all()[0].Body
	if !strings.Contains(body, f.closeEv.ID.String()) {
		t.Errorf("body does not name the event: %q", body)
	}
	if !strings.Contains(body, "goal_closed") {
		t.Errorf("body does not say what happened: %q", body)
	}
	if utf8.RuneCountInString(body) == 0 {
		t.Error("body is empty")
	}
}

// An unresolvable opener stops the pass rather than delivering to nobody.
func TestNotifyHandler_UnresolvableOpenerIsAnError(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	f.lookup.err = errors.New("connection refused")

	if err := f.handler.Handle(context.Background(), f.closeEv); err == nil {
		t.Fatal("Handle succeeded although it could not read the contract being closed; a transport blip would silently skip a notification")
	}
	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d times without knowing who owns the contract", got)
	}
}

// ---------------------------------------------------------------------------
// The ack (NOTIFY_ACKED)
// ---------------------------------------------------------------------------

type ackFixture struct {
	notifies *fakeOpenNotifies
	emitter  *recordingEmitter
	handler  *NotifyAckHandler
}

func newAckFixture(t *testing.T) *ackFixture {
	t.Helper()
	f := &ackFixture{
		notifies: &fakeOpenNotifies{},
		emitter:  &recordingEmitter{},
	}
	h, err := NewNotifyAckHandler(NotifyAckHandlerDeps{
		Emitter:  f.emitter,
		Notifies: f.notifies,
		Host:     "host-a",
	})
	if err != nil {
		t.Fatalf("NewNotifyAckHandler: %v", err)
	}
	f.handler = h
	return f
}

func turnFinishedEvent(agent string) DispatchedEvent {
	return DispatchedEvent{
		Seq: 20, ID: uuid.New(), SchemaName: "turn_finished", SchemaVersion: 1,
		WorkflowInstanceID: uuid.New(),
		Payload:            json.RawMessage(fmt.Sprintf(`{"agent_name":%q,"session_id":"s1","input_tokens":1,"output_tokens":1}`, agent)),
	}
}

// A TURN BOUNDARY CLOSES THE RECIPIENT'S OPEN NOTIFICATIONS.
//
// This is the plan's "NOTIFY_ACKED at the recipient's turn boundary", reached
// from the log rather than through a runtime hook — which is what keeps
// internal/supervisor untouched and keeps the local AgentState taxonomy the sole
// wake arbiter. turn_finished is already emitted for every turn (M1a), so the
// signal exists without adding one.
func TestNotifyAckHandler_TurnBoundaryClosesTheRecipientsNotifications(t *testing.T) {
	f := newAckFixture(t)
	n1, n2 := uuid.New(), uuid.New()
	f.notifies.open = []OpenNotify{
		{EventID: n1, Recipient: "weave"},
		{EventID: n2, Recipient: "weave"},
	}

	if err := f.handler.Handle(context.Background(), turnFinishedEvent("weave")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	acks := f.emitter.byName("notify_acked")
	if len(acks) != 2 {
		t.Fatalf("emitted %d notify_acked events, want 2 — an unacked notification stays outstanding forever and the sweeper re-delivers it", len(acks))
	}
	closed := map[uuid.UUID]bool{}
	for _, a := range acks {
		if a.ClosesEventID == nil {
			t.Fatal("notify_acked closes nothing")
		}
		closed[*a.ClosesEventID] = true
	}
	if !closed[n1] || !closed[n2] {
		t.Errorf("acks closed %v, want both %s and %s", closed, n1, n2)
	}
}

// ANOTHER AGENT'S TURN CLOSES NOTHING.
//
// Negative control for the assertion above, and the failure it guards is
// specific: closing on any turn at all would ack a notification the intended
// recipient never saw, and the whole point of the contract is that a lost
// delivery stays visible.
func TestNotifyAckHandler_AnotherAgentsTurnClosesNothing(t *testing.T) {
	f := newAckFixture(t)
	f.notifies.open = []OpenNotify{{EventID: uuid.New(), Recipient: "weave"}}

	if err := f.handler.Handle(context.Background(), turnFinishedEvent("someone-else")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := f.emitter.names(); len(got) != 0 {
		t.Errorf("emitted %v on another agent's turn boundary; weave's notification would be acked without weave ever seeing it", got)
	}
}

// A turn boundary with nothing outstanding does nothing, quietly.
//
// Every turn of every agent produces a turn_finished, so this is the
// overwhelmingly common case. Emitting an ack here would hit ErrNoOpenContract
// and dead-letter on essentially every turn in the system.
func TestNotifyAckHandler_TurnWithNothingOutstandingIsANoOp(t *testing.T) {
	f := newAckFixture(t)

	if err := f.handler.Handle(context.Background(), turnFinishedEvent("weave")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := f.emitter.names(); len(got) != 0 {
		t.Errorf("emitted %v with nothing outstanding; on a live system that is a dead-lettered append per turn", got)
	}
}

// A turn_finished with no agent_name cannot identify a recipient and is skipped
// rather than guessed at.
func TestNotifyAckHandler_TurnWithNoAgentNameIsSkipped(t *testing.T) {
	f := newAckFixture(t)
	f.notifies.open = []OpenNotify{{EventID: uuid.New(), Recipient: "weave"}}
	ev := turnFinishedEvent("weave")
	ev.Payload = json.RawMessage(`{"session_id":"s1","input_tokens":1,"output_tokens":1}`)

	if err := f.handler.Handle(context.Background(), ev); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := f.emitter.names(); len(got) != 0 {
		t.Errorf("emitted %v for a turn that names no agent", got)
	}
	// The reader must not even be ASKED. An empty recipient is refused by the
	// real reader, so asking would turn every anonymous turn boundary into a
	// dispatch error rather than a quiet skip.
	if got := f.notifies.queries(); len(got) != 0 {
		t.Errorf("the notify reader was queried with %v for a turn that names no agent", got)
	}
}

// Constructors refuse incomplete configurations.
func TestNotifyHandlers_RefuseIncompleteConfigurations(t *testing.T) {
	if _, err := NewNotifyHandler(NotifyHandlerDeps{}); err == nil {
		t.Error("NewNotifyHandler accepted an empty configuration")
	}
	if _, err := NewNotifyAckHandler(NotifyAckHandlerDeps{}); err == nil {
		t.Error("NewNotifyAckHandler accepted an empty configuration")
	}
}

// The two new seeds carry the contract shape.
func TestNotifySeeds_ContractShapes(t *testing.T) {
	reg := testRegistry(t)
	notify, ok := reg.ByName("owner_notify", 1)
	if !ok {
		t.Fatal("owner_notify@1 missing")
	}
	if !notify.Opens {
		t.Error("owner_notify does not open a contract, so a lost delivery is invisible rather than outstanding")
	}
	if notify.Spillable {
		t.Error("owner_notify is spillable; a contract in a local spill file is invisible to every other host")
	}
	acked, ok := reg.ByName("notify_acked", 1)
	if !ok {
		t.Fatal("notify_acked@1 missing")
	}
	if acked.Closes != "owner_notify" {
		t.Errorf("notify_acked closes %q, want owner_notify", acked.Closes)
	}
}

// ---------------------------------------------------------------------------
// Owner-dead handling: "never let a RESULT land unobserved"
// ---------------------------------------------------------------------------

// newDeadOwnerFixture is the notify fixture plus a local view and a fallback
// owner, so the owner-dead branch can be exercised.
func newDeadOwnerFixture(t *testing.T, owner string, local []LocalAgent) *notifyFixture {
	t.Helper()
	f := newNotifyFixture(t, owner)
	f.local = &fakeLocalAgents{agents: local}
	h, err := NewNotifyHandler(NotifyHandlerDeps{
		Emitter:       f.emitter,
		Injector:      f.injector,
		Lookup:        f.lookup,
		Notifies:      f.notifies,
		Local:         f.local,
		FallbackOwner: "weave",
		Host:          "host-a",
		Consumer:      "dispatcher",
	})
	if err != nil {
		t.Fatalf("NewNotifyHandler: %v", err)
	}
	f.handler = h
	return f
}

// A RETIRED OWNER'S RESULT IS REASSIGNED AND DELIVERED TO THE FALLBACK.
//
// The plan is explicit that this must not be papered over: "goal owner
// permanently dead → reassign ownership to root/workflow engine explicitly via a
// system event; never let a RESULT land unobserved". Without it the notification
// is addressed to an agent that cannot receive it, the injection fails on every
// pass, and the owner_notify contract stays open forever — a result nobody ever
// sees, with a growing pile of failed deliveries as the only symptom.
func TestNotifyHandler_ARetiredOwnersResultIsReassignedToTheFallback(t *testing.T) {
	f := newDeadOwnerFixture(t, "alice", []LocalAgent{{Name: "alice", Status: "retired"}})

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	reassigned := f.emitter.byName("ownership_reassigned")
	if len(reassigned) != 1 {
		t.Fatalf("%d ownership_reassigned events, want 1 — a silent redirect leaves a result delivered to an agent that never opened the contract, with nothing saying why", len(reassigned))
	}
	payload := fmt.Sprint(reassigned[0].Payload)
	if !strings.Contains(payload, "alice") || !strings.Contains(payload, "weave") {
		t.Errorf("the reassignment does not record both the old and new owner: %s", payload)
	}

	got := f.injector.all()
	if len(got) != 1 || got[0].Recipient != "weave" {
		t.Fatalf("delivered %+v, want one delivery to the fallback owner weave", got)
	}
	notifies := f.emitter.byName("owner_notify")
	if len(notifies) != 1 {
		t.Fatalf("%d owner_notify events, want 1", len(notifies))
	}
	if p := fmt.Sprint(notifies[0].Payload); !strings.Contains(p, "weave") {
		t.Errorf("the owner_notify names %s rather than the new owner, so the ack at weave's turn boundary would never close it: %s", "the dead owner", p)
	}
}

// POSITIVE CONTROL for the reassignment: a LIVE owner is not reassigned.
//
// Direction: a subject known clean, where the probe must stay quiet. Without it,
// a handler that reassigned unconditionally would satisfy the test above while
// routing every result in the system to root and never notifying the agent that
// actually asked for the work.
func TestNotifyHandler_ALiveOwnerIsNotReassigned(t *testing.T) {
	f := newDeadOwnerFixture(t, "alice", []LocalAgent{{Name: "alice", Status: "active"}})

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := f.emitter.byName("ownership_reassigned"); len(got) != 0 {
		t.Errorf("a LIVE owner was reassigned: %v", got)
	}
	if got := f.injector.all(); len(got) != 1 || got[0].Recipient != "alice" {
		t.Errorf("delivered %+v, want one delivery to alice", got)
	}
}

// A REVIVABLE resting owner is NOT dead.
//
// The distinction that matters most here, and the one the tempting implementation
// gets wrong: suspended, idle and complete agents are all revivable — a
// notification to them auto-wakes or waits — so reassigning them would hand root
// every result belonging to a quiet agent and permanently sever the agent from
// its own work. state.IsTerminal is the repo's own predicate and is imported
// rather than reimplemented.
func TestNotifyHandler_ARevivableRestingOwnerIsNotDead(t *testing.T) {
	for _, status := range []string{"suspended", "idle", "complete", "faulted", "paused"} {
		f := newDeadOwnerFixture(t, "alice", []LocalAgent{{Name: "alice", Status: status}})
		if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
			t.Fatalf("Handle(%s): %v", status, err)
		}
		if got := f.emitter.byName("ownership_reassigned"); len(got) != 0 {
			t.Errorf("an owner resting in status %q was reassigned, permanently severing a revivable agent from its own result", status)
		}
		if got := f.injector.all(); len(got) != 1 || got[0].Recipient != "alice" {
			t.Errorf("status %q: delivered %+v, want one delivery to alice", status, got)
		}
	}
}

// DIED WITH NO SESSION is permanent; died WITH a session is not.
//
// The plan's wording is "retired/died, no session", and the session is the
// discriminator: an agent recorded as died but still holding a session id may
// come back, and reassigning it would take its work away while it is recoverable.
func TestNotifyHandler_DiedWithNoSessionIsPermanentButWithOneIsNot(t *testing.T) {
	dead := newDeadOwnerFixture(t, "alice", []LocalAgent{{Name: "alice", Status: "died"}})
	if err := dead.handler.Handle(context.Background(), dead.closeEv); err != nil {
		t.Fatalf("Handle (no session): %v", err)
	}
	if got := dead.emitter.byName("ownership_reassigned"); len(got) != 1 {
		t.Errorf("%d reassignments for an owner that died with no session, want 1", len(got))
	}

	recoverable := newDeadOwnerFixture(t, "alice", []LocalAgent{{Name: "alice", Status: "died", SessionID: "s1"}})
	if err := recoverable.handler.Handle(context.Background(), recoverable.closeEv); err != nil {
		t.Fatalf("Handle (with session): %v", err)
	}
	if got := recoverable.emitter.byName("ownership_reassigned"); len(got) != 0 {
		t.Errorf("an owner that died but still holds a session was reassigned, taking its work away while it is still recoverable: %v", got)
	}
}

// AN OWNER THIS HOST CANNOT SEE IS NOT PRESUMED DEAD.
//
// A goal owned by an agent on another host is normal in a multi-host fleet, and
// absence from THIS host's state is not evidence of death. Reassigning on that
// basis would steal work from a perfectly healthy agent every time a result
// happened to land on the wrong host — and it would do so silently, because the
// reassignment event would look entirely legitimate.
//
// The delivery is attempted as addressed. If it genuinely cannot be delivered the
// injection fails, the owner_notify contract stays open, and the sweeper
// re-delivers — which is the mechanism already built for exactly this.
func TestNotifyHandler_AnUnseenOwnerIsNotPresumedDead(t *testing.T) {
	f := newDeadOwnerFixture(t, "alice", nil) // alice is on another host

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := f.emitter.byName("ownership_reassigned"); len(got) != 0 {
		t.Errorf("an owner merely absent from this host was declared dead and reassigned: %v", got)
	}
	if got := f.injector.all(); len(got) != 1 || got[0].Recipient != "alice" {
		t.Errorf("delivered %+v, want the delivery attempted to alice as addressed", got)
	}
}

// WITHOUT A LOCAL VIEW OR A FALLBACK, THE HANDLER DOES NOT REASSIGN — it behaves
// exactly as it did before this feature existed.
//
// Both are optional dependencies, and the degradation has to be toward "notify
// the owner as addressed" rather than toward "reassign to nothing": a fallback of
// "" would produce an owner_notify with an empty recipient, which is a contract
// nothing can ever ack.
func TestNotifyHandler_WithNoFallbackConfiguredItNeverReassigns(t *testing.T) {
	f := newNotifyFixture(t, "alice") // no Local, no FallbackOwner

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := f.emitter.byName("ownership_reassigned"); len(got) != 0 {
		t.Errorf("reassigned with no fallback owner configured: %v", got)
	}
	if got := f.injector.all(); len(got) != 1 || got[0].Recipient != "alice" {
		t.Errorf("delivered %+v, want one delivery to alice", got)
	}
}

// THE REASSIGNMENT IS RECORDED BEFORE THE NOTIFICATION IT REDIRECTS.
//
// Same rule as everywhere else in this diff, and here the consequence is
// attribution: a notification to root that arrived with no reassignment on the
// record is a result delivered to an agent that never opened the contract, with
// nothing in the log explaining it.
func TestNotifyHandler_ReassignmentIsRecordedBeforeTheNotification(t *testing.T) {
	f := newDeadOwnerFixture(t, "alice", []LocalAgent{{Name: "alice", Status: "retired"}})
	tr := &trace{}
	f.emitter.trace = tr
	f.injector.trace = tr

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := tr.all()
	want := []string{"emit:ownership_reassigned", "emit:owner_notify", "inject:weave"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("trace = %v, want %v", got, want)
	}
}

// A dead owner whose FALLBACK is also dead is reported rather than looped.
//
// Without a stop, a fallback that is itself gone would either be reassigned again
// (to itself, forever) or produce an undeliverable notification on every pass. One
// reassignment hop, then the failure surfaces.
func TestNotifyHandler_DoesNotReassignTheFallbackToItself(t *testing.T) {
	f := newDeadOwnerFixture(t, "weave", []LocalAgent{{Name: "weave", Status: "retired"}})

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := f.emitter.byName("ownership_reassigned"); len(got) != 0 {
		t.Errorf("the fallback owner was reassigned to itself: %v", got)
	}
}

// The new seed carries its intended shape.
func TestOwnershipSeed_Shape(t *testing.T) {
	reg := testRegistry(t)
	s, ok := reg.ByName("ownership_reassigned", 1)
	if !ok {
		t.Fatal("ownership_reassigned@1 is missing from the seed registry")
	}
	if s.Opens || s.Closes != "" {
		t.Errorf("ownership_reassigned takes part in a contract (opens=%v closes=%q); it is a system record, not outstanding work", s.Opens, s.Closes)
	}
	if s.Spillable {
		t.Error("ownership_reassigned is spillable; an ownership change recorded only on one host is invisible to every other reader of the contract")
	}
}

// ---------------------------------------------------------------------------
// The record-first rewrite (code-review fixes #1 and #3)
// ---------------------------------------------------------------------------

// trackingNotifies is an OpenNotify reader backed by the events the fixture's
// emitter recorded, so "is this contract still open" is answered from the same
// state a real database would answer it from.
//
// It exists because the notify handler's retry behaviour is now a FUNCTION of
// whether the contract is open, and a fake that answered a fixed value could not
// distinguish "delivered and acked" from "recorded but never delivered" — which
// is the exact pair the two defects below turned on.
type trackingNotifies struct {
	mu       sync.Mutex
	open     map[uuid.UUID]string // notify event id -> recipient
	acked    map[uuid.UUID]bool
	err      error
	askedFor []string
}

func newTrackingNotifies() *trackingNotifies {
	return &trackingNotifies{open: map[uuid.UUID]string{}, acked: map[uuid.UUID]bool{}}
}

func (t *trackingNotifies) OpenNotifies(_ context.Context, _ uuid.UUID, recipient string) ([]OpenNotify, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.askedFor = append(t.askedFor, recipient)
	if t.err != nil {
		return nil, t.err
	}
	if recipient == "" {
		return nil, errors.New("fake: an empty recipient would match every notification with no recipient")
	}
	var out []OpenNotify
	for id, r := range t.open {
		if r == recipient && !t.acked[id] {
			out = append(out, OpenNotify{EventID: id, Recipient: r})
		}
	}
	return out, nil
}

func (t *trackingNotifies) opened(id uuid.UUID, recipient string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.open[id] = recipient
}

func (t *trackingNotifies) ack(id uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.acked[id] = true
}

// notifyIDFor is the derived id the handler will use, computed independently.
func notifyIDFor(subject uuid.UUID, recipient string) uuid.UUID {
	return DerivedEventID(kindOwnerNotify, subject.String(), recipient)
}

// THE EVENT ID IS DERIVED FROM (subject, recipient), so two hosts attempting the
// same notification collide in the DATABASE rather than in a convention.
//
// This is the mechanism that replaced a claim, and it is asserted directly
// because everything below depends on it: if the id were random, every retry
// would append a second contract and the "is it still outstanding" question
// would be meaningless.
func TestNotifyHandler_EventIDIsDerivedFromSubjectAndRecipient(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := f.emitter.byName("owner_notify")
	if len(got) != 1 {
		t.Fatalf("%d owner_notify events, want 1", len(got))
	}
	want := notifyIDFor(f.closeEv.ID, "weave")
	if got[0].EventID != want {
		t.Errorf("owner_notify id = %s, want the derived %s — with a random id a retry appends a second contract and nothing can dedup it",
			got[0].EventID, want)
	}
	// Two different recipients for one result must NOT collide.
	if notifyIDFor(f.closeEv.ID, "weave") == notifyIDFor(f.closeEv.ID, "alice") {
		t.Error("two recipients of one result derive the same id, so only the first would ever be notified")
	}
	// The same recipient for two different results must not collide either.
	if notifyIDFor(f.closeEv.ID, "weave") == notifyIDFor(uuid.New(), "weave") {
		t.Error("two results derive the same id for one recipient, so only the first would ever be notified")
	}
}

// A FAILED APPEND DOES NOT SILENCE THE NOTIFICATION PERMANENTLY.
//
// The first HIGH defect from code review, verified there with a probe. The old
// code claimed `notify:<recipient>` BEFORE appending and never released it, so:
// append fails -> retry -> the claim is lost to OUR OWN ROW -> "another host is
// notifying this recipient" -> return nil -> the dispatcher records success and
// advances past the event. No event, no injection, no contract, cursor advanced,
// and one Debug line blaming a host that does not exist.
//
// A claim taken before the record is an ATTEMPTED marker being used as a DONE
// marker, and the two differ exactly when something fails.
func TestNotifyHandler_RetryAfterAFailedAppendStillDelivers(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	f.emitter.err = errors.New("connection reset")

	if err := f.handler.Handle(context.Background(), f.closeEv); err == nil {
		t.Fatal("Handle reported success although the append failed")
	}
	if got := f.injector.count(); got != 0 {
		t.Fatalf("injected %d times with no record written", got)
	}

	// The condition clears and the dispatcher retries the same event.
	f.emitter.mu.Lock()
	f.emitter.err = nil
	f.emitter.mu.Unlock()

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := len(f.emitter.byName("owner_notify")); got != 1 {
		t.Errorf("%d owner_notify events after the retry, want 1 — the result would have landed unobserved with nothing able to discover it", got)
	}
	if got := f.injector.count(); got != 1 {
		t.Errorf("injected %d times after the retry, want 1", got)
	}
}

// A FAILED INJECTION IS RE-DELIVERED BY THE NEXT PASS, because the contract is
// still open.
//
// The third HIGH defect: six comments claimed the SWEEPER re-delivered, and no
// such code existed anywhere. The mechanism that does exist is the DISPATCHER's
// retry — it keeps its cursor on a failing event — and it only works if the
// handler re-injects when it finds the contract outstanding. The old code hit the
// same lost-claim short-circuit and skipped.
func TestNotifyHandler_RetryAfterAFailedInjectionReDelivers(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	f.injector.err = errors.New("stdin is wedged")

	if err := f.handler.Handle(context.Background(), f.closeEv); err == nil {
		t.Fatal("Handle reported success although the injection failed")
	}
	// The contract is already open: the emitter's onEmit opened it when the
	// record landed, exactly as the appender does inside its transaction.
	f.injector.mu.Lock()
	f.injector.err = nil
	f.injector.mu.Unlock()

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := f.injector.count(); got != 1 {
		t.Errorf("injected %d times after the retry, want 1 — a failed delivery must be retried, and nothing in the sweeper does it", got)
	}
	// And exactly ONE contract, not two: the derived id made the append idempotent.
	if got := len(f.emitter.byName("owner_notify")); got != 1 {
		t.Errorf("%d owner_notify events after a retry, want 1 — a second contract is a second thing the ack has to close", got)
	}
}

// AN ACKED NOTIFICATION IS NOT RE-DELIVERED.
//
// The positive control's counterpart, and the assertion that stops the retry
// above from becoming a duplicate-delivery machine: once the contract is closed,
// a pass over the same event injects nothing.
func TestNotifyHandler_AnAckedNotificationIsNotReDelivered(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// The recipient took a turn, so the contract is closed. The second append is
	// a duplicate, which the emitter refuses because it has already recorded that
	// id — the same refusal Postgres gives.
	f.notifies.ack(notifyIDFor(f.closeEv.ID, "weave"))

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if got := f.injector.count(); got != 1 {
		t.Errorf("injected %d times in total, want 1 — an acked notification was delivered again", got)
	}
}

// An unreadable outstanding-notification check is an ERROR, not "not
// outstanding".
//
// Reading it as not-outstanding would skip the delivery on a transport blip while
// the dispatcher recorded success and advanced — the same permanent loss the
// claim short-circuit caused, arriving through the replacement mechanism.
func TestNotifyHandler_UnreadableOutstandingCheckIsAnError(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	f.notifies.err = errors.New("connection refused")

	if err := f.handler.Handle(context.Background(), f.closeEv); err == nil {
		t.Fatal("Handle succeeded although it could not tell whether the notification was still outstanding")
	}
}

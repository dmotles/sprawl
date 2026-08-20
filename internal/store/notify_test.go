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

	h, err := NewNotifyHandler(NotifyHandlerDeps{
		Emitter:  f.emitter,
		Injector: f.injector,
		Claims:   f.claims,
		Lookup:   f.lookup,
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

// CLAIM, RECORD, THEN INJECT — in that order.
//
// The claim first because injection is a side effect and two hosts must not both
// perform it. The record before the injection because a failed injection has to
// leave something for the sweeper to find; the reverse order delivers with no
// trace, so a delivery that half-happened is indistinguishable from one that
// never did.
func TestNotifyHandler_ClaimsRecordsThenInjects(t *testing.T) {
	f := newNotifyFixture(t, "weave")

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got := f.trace.all()
	want := []string{"claim:" + f.closeEv.ID.String(), "emit:owner_notify", "inject:weave"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("trace = %v, want %v", got, want)
	}
}

// The claim is keyed PER RECIPIENT, not per event.
//
// One landing result can owe notifications to more than one party, and a claim
// keyed on the event alone would deliver to the first and silently drop the rest.
func TestNotifyHandler_ClaimIsScopedToTheRecipient(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	log := f.claims.log()
	if len(log) == 0 {
		t.Fatal("no claim was taken")
	}
	if !strings.Contains(f.claims.consumers()[0], "weave") {
		t.Errorf("the claim consumer %q does not name the recipient, so a second recipient for the same event would be suppressed", f.claims.consumers()[0])
	}
}

// EXACTLY ONCE PER (EVENT, RECIPIENT). Two passes, one injection.
func TestNotifyHandler_SecondPassDoesNotRedeliver(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	for i := 0; i < 2; i++ {
		if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
			t.Fatalf("Handle pass %d: %v", i, err)
		}
	}
	if got := f.injector.count(); got != 1 {
		t.Errorf("injected %d times across two passes, want 1", got)
	}
	if got := len(f.emitter.byName("owner_notify")); got != 1 {
		t.Errorf("%d owner_notify events, want 1 — the recipient would see the same result twice and the sweeper would chase two contracts", got)
	}
}

// A LOSING claim means no injection AND no record. Both halves matter: a record
// without an injection is an open contract nobody will ever ack.
func TestNotifyHandler_LosingTheClaimDeliversNothing(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	f.claims.refuse[f.closeEv.ID] = true

	if err := f.handler.Handle(context.Background(), f.closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d times without holding the claim", got)
	}
	if got := f.emitter.names(); len(got) != 0 {
		t.Errorf("emitted %v without holding the claim; an owner_notify nobody delivers is an open contract nobody acks", got)
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
// This is the whole reason the record comes first. The contract stays open, so
// the sweeper finds it and re-delivers; and the error surfaces, so the dispatcher
// does not advance its cursor past a result nobody was told about.
func TestNotifyHandler_FailedInjectionLeavesTheContractOpenAndReportsIt(t *testing.T) {
	f := newNotifyFixture(t, "weave")
	f.injector.err = errors.New("recipient stdin is wedged")

	err := f.handler.Handle(context.Background(), f.closeEv)
	if err == nil {
		t.Fatal("Handle reported success although the injection failed; the dispatcher would advance past a result nobody was told about")
	}
	notifies := f.emitter.byName("owner_notify")
	if len(notifies) != 1 {
		t.Fatalf("%d owner_notify events, want 1 — a failed delivery must leave a sweepable record", len(notifies))
	}
	if got := f.emitter.byName("notify_acked"); len(got) != 0 {
		t.Errorf("emitted %v after a FAILED injection; the contract must stay open so the sweeper re-delivers", got)
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

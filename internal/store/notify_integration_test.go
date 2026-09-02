//go:build store_pg

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Notification delivery against a real Postgres (QUM-1250, M1b).
//
// What only a real database establishes here: that an owner_notify really does
// sit in open_contracts until acked and really does leave it afterwards, that the
// recipient predicate is applied by the query, and — the load-bearing one — that
// a FAILED delivery leaves a contract the sweeper can find.

// notifyEnv adds the notification pieces to the spawn environment.
type notifyEnv struct {
	*spawnEnv
	notifies *PgNotifyReader
	injector *recordingInjector
}

func newNotifyEnv(t *testing.T) *notifyEnv {
	t.Helper()
	e := newSpawnEnv(t)
	return &notifyEnv{
		spawnEnv: e,
		notifies: &PgNotifyReader{Pool: e.pool, Registry: e.registry},
		injector: &recordingInjector{},
	}
}

// openGoal appends a goal_opened owned by owner, and returns its id.
func (e *notifyEnv) openGoal(t *testing.T, owner string) uuid.UUID {
	t.Helper()
	id, err := e.emitter.Emit(context.Background(), EmitRequest{
		TypeName: "goal_opened", TypeVersion: 1,
		WorkflowInstanceID: uuid.New(),
		Payload:            map[string]any{"goal_type": "research", "text": "find out", "owner": owner},
	})
	if err != nil {
		t.Fatalf("opening a goal: %v", err)
	}
	return id
}

// closeGoal appends the goal_closed that lands the result, and returns it as the
// dispatcher would hand it to a handler.
func (e *notifyEnv) closeGoal(t *testing.T, goalID uuid.UUID) DispatchedEvent {
	t.Helper()
	wf := uuid.New()
	id, err := e.emitter.Emit(context.Background(), EmitRequest{
		TypeName: "goal_closed", TypeVersion: 1,
		WorkflowInstanceID: wf,
		ClosesEventID:      &goalID,
		Payload:            map[string]any{"outcome": "success", "summary": "done"},
	})
	if err != nil {
		t.Fatalf("closing the goal: %v", err)
	}
	return DispatchedEvent{
		ID: id, ProjectID: e.projectID, WorkflowInstanceID: wf,
		SchemaName: "goal_closed", SchemaVersion: 1,
		ClosesEventID: &goalID,
		Payload:       []byte(`{"outcome":"success","summary":"done"}`),
	}
}

func (e *notifyEnv) handler(t *testing.T, inj Injector) *NotifyHandler {
	t.Helper()
	h, err := NewNotifyHandler(NotifyHandlerDeps{
		Emitter:  e.emitter,
		Injector: inj,
		Lookup:   e.reader(),
		Notifies: e.notifies,
		Host:     "host-a",
		Consumer: "dispatcher",
	})
	if err != nil {
		t.Fatalf("NewNotifyHandler: %v", err)
	}
	return h
}

// ---------------------------------------------------------------------------
// The contract
// ---------------------------------------------------------------------------

// A landing result notifies its owner and leaves the notification OUTSTANDING
// until it is acked.
//
// The outstanding half is the point: a delivered-but-unacked notification is
// exactly the state a lost injection produces, and it has to be visible.
func TestNotifyPg_LandingResultNotifiesTheOwnerAndStaysOutstanding(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	goal := e.openGoal(t, "weave")
	closeEv := e.closeGoal(t, goal)

	if err := e.handler(t, e.injector).Handle(ctx, closeEv); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got := e.injector.all()
	if len(got) != 1 || got[0].Recipient != "weave" {
		t.Fatalf("injected %+v, want one delivery to weave", got)
	}
	open, err := e.notifies.OpenNotifies(ctx, e.projectID, "weave")
	if err != nil {
		t.Fatalf("OpenNotifies: %v", err)
	}
	if len(open) != 1 {
		t.Errorf("%d outstanding notifications for weave, want 1 — a delivery nobody can confirm arrived", len(open))
	}
}

// The recipient predicate is applied by the query, not hopefully in Go.
//
// Negative control for the assertion above: reading every recipient's
// notifications would make the ack handler close notifications belonging to
// whichever agent happened to take a turn first.
func TestNotifyPg_OutstandingNotificationsAreScopedToTheRecipient(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	if err := e.handler(t, e.injector).Handle(ctx, e.closeGoal(t, e.openGoal(t, "weave"))); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	other, err := e.notifies.OpenNotifies(ctx, e.projectID, "someone-else")
	if err != nil {
		t.Fatalf("OpenNotifies: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("someone-else has %d of weave's notifications outstanding", len(other))
	}
}

// A TURN BOUNDARY CLOSES IT, and it leaves open_contracts for real.
func TestNotifyPg_TurnBoundaryClosesTheContract(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	if err := e.handler(t, e.injector).Handle(ctx, e.closeGoal(t, e.openGoal(t, "weave"))); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	ack, err := NewNotifyAckHandler(NotifyAckHandlerDeps{
		Emitter: e.emitter, Notifies: e.notifies, Host: "host-a",
	})
	if err != nil {
		t.Fatalf("NewNotifyAckHandler: %v", err)
	}
	turn := turnFinishedEvent("weave")
	turn.ProjectID = e.projectID
	if err := ack.Handle(ctx, turn); err != nil {
		t.Fatalf("ack Handle: %v", err)
	}

	open, err := e.notifies.OpenNotifies(ctx, e.projectID, "weave")
	if err != nil {
		t.Fatalf("OpenNotifies: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("%d notifications are still outstanding after the recipient took a turn, want 0 — the sweeper would re-deliver a result the owner already saw", len(open))
	}
	if got := e.openContractCount(t, "owner_notify"); got != 0 {
		t.Errorf("%d owner_notify contracts still open, want 0", got)
	}
	if got := e.eventCount(t, "notify_acked"); got != 1 {
		t.Errorf("%d notify_acked events, want 1", got)
	}
}

// A FAILED INJECTION LEAVES THE CONTRACT OUTSTANDING AND UNDELIVERED, and the
// undelivered half is what the database has to establish.
//
// Delivered is an EXISTS subquery in openNotifiesSQL, and a fake reader can be
// made to agree with a wrong one. That flag is the ack's precondition, so
// getting it wrong here means every turn boundary closes contracts for results
// nobody was ever told about — the QUM-1325 defect, restored through the SQL.
func TestNotifyPg_FailedInjectionLeavesAnOutstandingUndeliveredContract(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	broken := &recordingInjector{err: errors.New("stdin is wedged")}

	if err := e.handler(t, broken).Handle(ctx, e.closeGoal(t, e.openGoal(t, "weave"))); err != nil {
		t.Fatalf("Handle returned %v; a failed injection must not fail the dispatch", err)
	}

	open, err := e.notifies.OpenNotifies(ctx, e.projectID, "weave")
	if err != nil {
		t.Fatalf("OpenNotifies: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("%d outstanding notifications after a FAILED delivery, want 1 — the result would sit unobserved with nothing to sweep", len(open))
	}
	if open[0].Delivered {
		t.Error("the outstanding notification reads as delivered although the injection failed; the recipient's next turn boundary would ack it")
	}
	if got := e.eventCount(t, "notify_attempt"); got != 1 {
		t.Errorf("%d notify_attempt events, want 1 — without one the sweeper's epoch never advances", got)
	}
	if got := e.eventCount(t, "notify_delivered"); got != 0 {
		t.Errorf("%d notify_delivered events after a failed injection, want 0", got)
	}
}

// A SUCCESSFUL DELIVERY MARKS THE CONTRACT DELIVERED — the positive leg of the
// pair above, and the negative control for the EXISTS subquery.
//
// Without it, a Delivered that was hard-wired false would satisfy every
// assertion above while breaking every ack in the system.
func TestNotifyPg_ASuccessfulDeliveryMarksTheContractDelivered(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()

	if err := e.handler(t, e.injector).Handle(ctx, e.closeGoal(t, e.openGoal(t, "weave"))); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	open, err := e.notifies.OpenNotifies(ctx, e.projectID, "weave")
	if err != nil {
		t.Fatalf("OpenNotifies: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("%d outstanding notifications, want 1", len(open))
	}
	if !open[0].Delivered {
		t.Error("a delivered notification reads as undelivered, so its recipient can never ack it and the sweeper re-delivers it forever")
	}
}

// THE SWEEP RE-DELIVERS AN UNDELIVERED NOTIFICATION, end to end through the real
// reader.
//
// The unit tests hand SweepNotifications its candidates. This is the only place
// that establishes openNotificationsSQL actually FINDS an undelivered
// notification and counts its attempts — three correlated subqueries keyed on a
// jsonb field, none of which a fake can get wrong on the implementation's behalf.
func TestNotifySweepPg_ReDeliversAnUndeliveredNotification(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	broken := &recordingInjector{err: errors.New("stdin is wedged")}
	if err := e.handler(t, broken).Handle(ctx, e.closeGoal(t, e.openGoal(t, "weave"))); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	reader := &PgNotifySweepReader{Pool: e.pool, Registry: e.registry}
	got, err := reader.OpenNotifications(ctx, e.projectID)
	if err != nil {
		t.Fatalf("OpenNotifications: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("%d re-delivery candidates, want 1", len(got))
	}
	if got[0].Recipient != "weave" || got[0].Delivered || got[0].Quarantined {
		t.Errorf("candidate = %+v, want recipient weave, undelivered, unquarantined", got[0])
	}
	if got[0].Attempts != 1 {
		t.Fatalf("candidate reports %d attempt(s), want 1 — the epoch is the count of notify_attempt events and it drives both the backoff and the cap", got[0].Attempts)
	}
	if got[0].LastAttemptAt.IsZero() {
		t.Error("candidate has no last-attempt time, so the backoff gate can never hold and it is re-delivered on every sweep")
	}

	// Past the epoch-1 backoff, with a working injector.
	working := &recordingInjector{}
	res, err := SweepNotifications(ctx, NotifySweeperDeps{
		Notifications: reader, Emitter: e.emitter, Injector: working,
		ProjectID: e.projectID, Host: "host-a",
		Now: func() time.Time { return time.Now().Add(2 * notifyBackoff(0)) },
	})
	if err != nil {
		t.Fatalf("SweepNotifications: %v", err)
	}
	if res.Delivered != 1 {
		t.Fatalf("result = %+v, want delivered 1", res)
	}
	if n := working.count(); n != 1 {
		t.Errorf("injected %d times, want 1", n)
	}
	if n := e.eventCount(t, "notify_delivered"); n != 1 {
		t.Errorf("%d notify_delivered events, want 1", n)
	}

	// And now the recipient's turn boundary CAN ack it — the loop closing, which
	// is the property the delivery predicate exists to make true.
	ack, err := NewNotifyAckHandler(NotifyAckHandlerDeps{Emitter: e.emitter, Notifies: e.notifies, Host: "host-a"})
	if err != nil {
		t.Fatalf("NewNotifyAckHandler: %v", err)
	}
	turn := turnFinishedEvent("weave")
	turn.ProjectID = e.projectID
	if err := ack.Handle(ctx, turn); err != nil {
		t.Fatalf("ack Handle: %v", err)
	}
	if n := e.openContractCount(t, "owner_notify"); n != 0 {
		t.Errorf("%d owner_notify contracts still open after a delivery and a turn boundary, want 0", n)
	}
}

// A TURN BOUNDARY DOES NOT ACK AN UNDELIVERED NOTIFICATION (QUM-1325), through
// the real reader.
//
// Negative control for the test above, and the one that only Postgres can
// settle: the predicate is an EXISTS over a jsonb field, so a subquery matching
// the wrong column reads as "delivered" for everything and the whole fix
// evaporates while every unit test stays green.
func TestNotifyPg_ATurnBoundaryDoesNotAckAnUndeliveredNotification(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	broken := &recordingInjector{err: errors.New("stdin is wedged")}
	if err := e.handler(t, broken).Handle(ctx, e.closeGoal(t, e.openGoal(t, "weave"))); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	ack, err := NewNotifyAckHandler(NotifyAckHandlerDeps{Emitter: e.emitter, Notifies: e.notifies, Host: "host-a"})
	if err != nil {
		t.Fatalf("NewNotifyAckHandler: %v", err)
	}
	turn := turnFinishedEvent("weave")
	turn.ProjectID = e.projectID
	if err := ack.Handle(ctx, turn); err != nil {
		t.Fatalf("ack Handle: %v", err)
	}

	if got := e.eventCount(t, "notify_acked"); got != 0 {
		t.Errorf("%d notify_acked events for a notification that was never delivered, want 0 — the result is discarded and nothing can ever find it again", got)
	}
	if got := e.openContractCount(t, "owner_notify"); got != 1 {
		t.Errorf("%d owner_notify contracts open, want 1 — the contract must survive the turn boundary for the sweeper to re-deliver it", got)
	}
}

// A CAPPED NOTIFICATION IS QUARANTINED AND THE CONTRACT STAYS OPEN.
//
// The open contract is the assertion Postgres is needed for: notify_undelivered
// declares no `closes`, and whether that actually leaves the open_contracts row
// alone is enforced by the appender's transaction, not by the seed's prose.
func TestNotifySweepPg_AtTheCapItQuarantinesAndLeavesTheContractOpen(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	broken := &recordingInjector{err: errors.New("stdin is wedged")}
	if err := e.handler(t, broken).Handle(ctx, e.closeGoal(t, e.openGoal(t, "weave"))); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	reader := &PgNotifySweepReader{Pool: e.pool, Registry: e.registry}
	deps := NotifySweeperDeps{
		Notifications: reader, Emitter: e.emitter, Injector: broken,
		ProjectID: e.projectID, Host: "host-a",
	}
	// Sweep past every backoff window until the cap is reached. The clock is
	// pushed far enough forward each pass that the backoff can never be the
	// reason a pass did nothing — otherwise this loop would "reach the cap" by
	// running out of iterations, which looks identical from the assertions below.
	for i := 0; i < maxNotifyAttempts+1; i++ {
		at := time.Now().Add(time.Duration(i+1) * 24 * time.Hour)
		if _, err := SweepNotifications(ctx, func() NotifySweeperDeps {
			d := deps
			d.Now = func() time.Time { return at }
			return d
		}()); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}

	if got := e.eventCount(t, "notify_attempt"); got != maxNotifyAttempts {
		t.Errorf("%d notify_attempt events, want exactly %d — the cap is the only thing that stops an unreachable recipient costing tokens forever", got, maxNotifyAttempts)
	}
	if got := e.eventCount(t, "notify_undelivered"); got != 1 {
		t.Errorf("%d notify_undelivered events, want 1", got)
	}
	if got := e.openContractCount(t, "owner_notify"); got != 1 {
		t.Errorf("%d owner_notify contracts open after quarantine, want 1 — quarantine stops the retries, it does not discharge the contract", got)
	}
}

// EXACTLY ONCE ACROSS TWO HOSTS — driven through the DISPATCHER, because that is
// where the exclusion now lives.
//
// This test used to call NotifyHandler.Handle twice directly and rely on a claim
// the handler took itself. That claim was removed: it was an "attempted" marker
// used as a "done" marker and it silently dropped notifications (see derived.go).
// So the honest question changed, and it is worth being precise about the answer:
//
//	the APPEND is idempotent, by the derived event id;
//	the INJECTION is excluded by the DISPATCHER's per-event claim, one level up;
//	and after a lease TAKEOVER a re-injection is possible — the same
//	at-least-once trade claims.go already documents, and the right one, because
//	the alternative is a result the owner never hears about.
//
// Calling the handler twice by hand tests none of that; it tests a path no
// production caller takes. Two dispatchers is the real configuration.
func TestNotifyPg_TwoDispatchersNotifyTheOwnerOnce(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	e.closeGoal(t, e.openGoal(t, "weave"))

	shared := &recordingInjector{}
	for _, host := range []string{"host-a", "host-b"} {
		notify := e.handler(t, shared)
		deps := e.deps(host, notify)
		deps.Handlers = map[string]Handler{"goal_closed": notify}
		deps.Cursor = &FileCursorStore{Root: t.TempDir()}
		d, err := NewDispatcher(deps)
		if err != nil {
			t.Fatalf("NewDispatcher(%s): %v", host, err)
		}
		if _, err := d.Step(ctx); err != nil {
			t.Fatalf("Step(%s): %v", host, err)
		}
	}

	if got := shared.count(); got != 1 {
		t.Errorf("the owner was notified %d times by two dispatchers, want exactly 1", got)
	}
	if got := e.eventCount(t, "owner_notify"); got != 1 {
		t.Errorf("%d owner_notify events, want 1 — the derived id should have made the second append a no-op", got)
	}
}

// THE DERIVED ID MAKES A SECOND APPEND A NO-OP, asserted directly against
// Postgres.
//
// The mechanism that replaced the claim, and the assertion that it is the
// DATABASE enforcing it: a second Emit of the same logical notification is
// refused with a unique violation rather than accepted as a second contract.
func TestNotifyPg_ASecondAppendOfTheSameNotificationIsRefused(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	closeEv := e.closeGoal(t, e.openGoal(t, "weave"))

	req := EmitRequest{
		TypeName: "owner_notify", TypeVersion: 1,
		EventID:            DerivedEventID(kindOwnerNotify, closeEv.ID.String(), "weave"),
		WorkflowInstanceID: uuid.New(),
		Payload: map[string]any{
			"recipient": "weave", "subject_event_id": closeEv.ID.String(),
		},
	}
	if _, err := e.emitter.Emit(ctx, req); err != nil {
		t.Fatalf("first append: %v", err)
	}
	_, err := e.emitter.Emit(ctx, req)
	if err == nil {
		t.Fatal("a second append of the same notification SUCCEEDED; the derived id is not excluding anything and two hosts would both notify")
	}
	if !IsUniqueViolation(err) {
		t.Errorf("the second append failed with %v, which IsUniqueViolation does not recognise — the handler would treat it as a real failure and never deliver", err)
	}
}

// An event that closes an UNOWNED contract notifies nobody and leaves nothing
// outstanding.
func TestNotifyPg_UnownedContractLeavesNothingOutstanding(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	// goal_opened's `owner` is optional, so omitting it is a legitimate state.
	goal, err := e.emitter.Emit(ctx, EmitRequest{
		TypeName: "goal_opened", TypeVersion: 1,
		WorkflowInstanceID: uuid.New(),
		Payload:            map[string]any{"goal_type": "research", "text": "system goal"},
	})
	if err != nil {
		t.Fatalf("opening an unowned goal: %v", err)
	}

	if err := e.handler(t, e.injector).Handle(ctx, e.closeGoal(t, goal)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := e.injector.count(); got != 0 {
		t.Errorf("injected %d times for an unowned contract", got)
	}
	if got := e.eventCount(t, "owner_notify"); got != 0 {
		t.Errorf("%d owner_notify events for an unowned contract — a contract with no recipient can never be acked", got)
	}
}

// The whole loop, driven by the dispatcher rather than by calling handlers
// directly: a goal closes, its owner is notified, the owner takes a turn, and
// nothing is left outstanding.
//
// This is the assertion that the two handlers compose — each is correct in
// isolation in the tests above, and a wiring mistake (registering the ack for the
// wrong type, say) would leave both of those green.
func TestNotifyPg_DispatcherDrivesTheFullNotifyThenAckCycle(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()

	goal := e.openGoal(t, "weave")
	e.closeGoal(t, goal)

	notify := e.handler(t, e.injector)
	ack, err := NewNotifyAckHandler(NotifyAckHandlerDeps{
		Emitter: e.emitter, Notifies: e.notifies, Host: "host-a",
	})
	if err != nil {
		t.Fatalf("NewNotifyAckHandler: %v", err)
	}

	deps := e.deps("host-a", notify)
	deps.Handlers = map[string]Handler{"goal_closed": notify, "turn_finished": ack}
	d, err := NewDispatcher(deps)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := d.Step(ctx); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if got := e.injector.count(); got != 1 {
		t.Fatalf("the dispatcher delivered %d notifications, want 1", got)
	}

	// weave takes a turn. M1a's lifecycle emitter would produce this; here it is
	// appended directly, because the point is the ack consumer, not the emitter.
	if _, err := e.emitter.Emit(ctx, EmitRequest{
		TypeName: "turn_finished", TypeVersion: 1,
		WorkflowInstanceID: uuid.New(),
		Payload: map[string]any{
			"agent_name": "weave", "session_id": "s1",
			"input_tokens": 10, "output_tokens": 20,
		},
	}); err != nil {
		t.Fatalf("emitting a turn boundary: %v", err)
	}
	if _, err := d.Step(ctx); err != nil {
		t.Fatalf("second Step: %v", err)
	}

	open, err := e.notifies.OpenNotifies(ctx, e.projectID, "weave")
	if err != nil {
		t.Fatalf("OpenNotifies: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("%d notifications outstanding after the owner took a turn, want 0", len(open))
	}
}

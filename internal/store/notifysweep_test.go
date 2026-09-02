package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The notification re-delivery sweep (QUM-1252).
//
// ORGANISED IN PAIRS, for sweeper_test.go's reason and with the same hazard: a
// "nothing was re-delivered" assertion is satisfied by a sweep that re-delivers
// nothing ever, by a fixture that produced no candidate, and by a typo in a
// field name. So every gate below is written against ONE fixture that IS
// re-deliverable, differing in exactly one field, and the baseline is asserted
// by TestSweepNotifications_ReDeliversAnUndeliveredNotification. Where a gate's
// reason string is what distinguishes it from the gate above, SkipReasons is
// asserted rather than the bare skip count — an earlier suite in this package
// had a test that believed it covered one gate and was actually satisfied by a
// different one.

type fakeNotifySweepReader struct {
	candidates []NotifyCandidate
	err        error
}

func (f *fakeNotifySweepReader) OpenNotifications(context.Context, uuid.UUID) ([]NotifyCandidate, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]NotifyCandidate(nil), f.candidates...), nil
}

type notifySweepFixture struct {
	reader   *fakeNotifySweepReader
	emitter  *recordingEmitter
	injector *recordingInjector
	now      time.Time
	notifyID uuid.UUID
	deps     NotifySweeperDeps
}

// newNotifySweepFixture builds a fixture whose single candidate IS undelivered
// and IS due for re-delivery, so every gate test is one field away from a
// delivery.
func newNotifySweepFixture(t *testing.T) *notifySweepFixture {
	t.Helper()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	f := &notifySweepFixture{
		reader:   &fakeNotifySweepReader{},
		emitter:  &recordingEmitter{},
		injector: &recordingInjector{},
		now:      now,
		notifyID: uuid.New(),
	}
	f.reader.candidates = []NotifyCandidate{{
		NotifyEventID:  f.notifyID,
		WorkflowID:     uuid.New(),
		Recipient:      "weave",
		SubjectEventID: uuid.New().String(),
		Body:           "a result landed",
		OpenedAt:       now.Add(-2 * time.Hour),
		// One attempt already made, well outside the epoch-0 backoff.
		Attempts:      1,
		LastAttemptAt: now.Add(-90 * time.Minute),
	}}
	f.deps = NotifySweeperDeps{
		Notifications: f.reader,
		Emitter:       f.emitter,
		Injector:      f.injector,
		ProjectID:     uuid.New(),
		Host:          "host-a",
		Now:           func() time.Time { return f.now },
	}
	return f
}

func (f *notifySweepFixture) only() *NotifyCandidate { return &f.reader.candidates[0] }

func (f *notifySweepFixture) sweep(t *testing.T) NotifySweepResult {
	t.Helper()
	res, err := SweepNotifications(context.Background(), f.deps)
	if err != nil {
		t.Fatalf("SweepNotifications: %v", err)
	}
	return res
}

// reasonForOnly returns the gate that held the fixture's single candidate back,
// failing the test if it was not skipped at all. Asserting the REASON rather
// than the count is what stops a gate test passing because a different gate
// fired first.
func (f *notifySweepFixture) reasonForOnly(t *testing.T, res NotifySweepResult) string {
	t.Helper()
	why, ok := res.SkipReasons[f.notifyID]
	if !ok {
		t.Fatalf("the candidate was not skipped at all (result %+v)", res)
	}
	return why
}

// ---------------------------------------------------------------------------
// The baseline — the control every gate test below depends on
// ---------------------------------------------------------------------------

// AN UNDELIVERED NOTIFICATION IS RE-DELIVERED, and the order is attempt, inject,
// delivered.
//
// This is the leg QUM-1250 shipped owing: nothing re-delivered a notification,
// and the only retry was the dispatcher re-running a handler that returned an
// error — which head-of-line blocked the whole project behind one unreachable
// recipient.
func TestSweepNotifications_ReDeliversAnUndeliveredNotification(t *testing.T) {
	f := newNotifySweepFixture(t)
	tr := &trace{}
	f.emitter.trace, f.injector.trace = tr, tr

	res := f.sweep(t)

	if res.Delivered != 1 || res.Considered != 1 {
		t.Fatalf("result = %+v, want considered 1, delivered 1", res)
	}
	if got := f.injector.count(); got != 1 {
		t.Fatalf("injected %d times, want 1", got)
	}
	if got, want := len(f.emitter.byName("notify_attempt")), 1; got != want {
		t.Errorf("%d notify_attempt events, want %d", got, want)
	}
	// RECORD-BEFORE. The epoch is a COUNT of attempts, so an attempt written only
	// after a successful injection leaves a failed one invisible and the next
	// pass recomputing an identical backoff key with no progress.
	want := []string{"emit:notify_attempt", "inject:weave", "emit:notify_delivered"}
	if got := tr.all(); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("trace = %v, want %v", got, want)
	}
}

// THE ATTEMPT IS KEYED ON (notification, epoch), which is what makes a second
// host's attempt at the same epoch collide and the NEXT epoch free.
//
// Positive control for the id itself: without a derived id the sweep would still
// deliver and every assertion above would still pass.
func TestSweepNotifications_TheAttemptIDIsDerivedFromTheEpoch(t *testing.T) {
	f := newNotifySweepFixture(t)
	f.sweep(t)

	attempts := f.emitter.byName("notify_attempt")
	if len(attempts) != 1 {
		t.Fatalf("%d notify_attempt events, want 1", len(attempts))
	}
	want := DerivedEventID(kindNotifyAttempt, f.notifyID.String(), "1")
	if attempts[0].EventID != want {
		t.Errorf("attempt id = %s, want %s — a non-derived id lets two hosts both deliver the same epoch", attempts[0].EventID, want)
	}
}

// ---------------------------------------------------------------------------
// The gates
// ---------------------------------------------------------------------------

// ALREADY DELIVERED: waiting on the recipient's turn boundary, not on a
// delivery. Negative leg; the baseline above is its positive control.
func TestSweepNotifications_ADeliveredNotificationIsNotSentAgain(t *testing.T) {
	f := newNotifySweepFixture(t)
	f.only().Delivered = true

	res := f.sweep(t)

	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d times for a notification that was already delivered; the recipient would be told the same thing twice for the crime of being busy", got)
	}
	if why := f.reasonForOnly(t, res); !strings.Contains(why, "already delivered") {
		t.Errorf("skipped for %q, want the already-delivered gate", why)
	}
}

// QUARANTINED: a notify_undelivered event exists, so it is never attempted
// again. Existence-based, mirroring goal_stuck — an escalation event that
// coexisted with continued retries would look like a fix and cost the same.
func TestSweepNotifications_AQuarantinedNotificationIsNotSentAgain(t *testing.T) {
	f := newNotifySweepFixture(t)
	f.only().Quarantined = true
	// At the cap as well, so this test cannot pass by way of the cap branch
	// re-quarantining it: the quarantine gate must fire FIRST.
	f.only().Attempts = maxNotifyAttempts

	res := f.sweep(t)

	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d times for a quarantined notification", got)
	}
	if got := len(f.emitter.byName("notify_undelivered")); got != 0 {
		t.Errorf("emitted %d notify_undelivered events for an already-quarantined notification; quarantine is once, ever", got)
	}
	if why := f.reasonForOnly(t, res); !strings.Contains(why, "quarantined") {
		t.Errorf("skipped for %q, want the quarantine gate", why)
	}
}

// INSIDE THE BACKOFF. The one-field difference from the baseline is
// LastAttemptAt; everything else is identical, so a sweep that ignored the
// backoff entirely would deliver here and fail.
func TestSweepNotifications_ANotificationInsideItsBackoffIsNotSentAgain(t *testing.T) {
	f := newNotifySweepFixture(t)
	// Epoch 1's wait is notifyBackoff(0) = the base interval. Half of it has
	// passed.
	f.only().LastAttemptAt = f.now.Add(-notifyBackoffBase / 2)

	res := f.sweep(t)

	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d times inside the backoff window; an unreachable recipient would be retried every sweep interval forever", got)
	}
	if why := f.reasonForOnly(t, res); !strings.Contains(why, "backoff") {
		t.Errorf("skipped for %q, want the backoff gate", why)
	}
}

// THE BACKOFF WIDENS WITH THE EPOCH — the positive leg of the pair above, and
// the assertion that the exponent is read from the attempt count rather than
// being a fixed interval.
//
// At epoch 4 the wait is 8x the base. An elapsed gap that would have been ample
// at epoch 1 must NOT deliver here; the same fixture one epoch's-worth later
// must.
func TestSweepNotifications_TheBackoffWidensWithTheAttemptCount(t *testing.T) {
	f := newNotifySweepFixture(t)
	f.only().Attempts = 4
	f.only().LastAttemptAt = f.now.Add(-2 * notifyBackoffBase)

	res := f.sweep(t)
	if got := f.injector.count(); got != 0 {
		t.Fatalf("injected %d times %s after attempt 4, whose backoff is %s — the exponent is not being read from the attempt count",
			got, 2*notifyBackoffBase, notifyBackoff(3))
	}
	if why := f.reasonForOnly(t, res); !strings.Contains(why, "backoff") {
		t.Errorf("skipped for %q, want the backoff gate", why)
	}

	// The control: past the wider window it DOES deliver, so the assertion above
	// is about the width and not about epoch 4 being unreachable.
	f2 := newNotifySweepFixture(t)
	f2.only().Attempts = 4
	f2.only().LastAttemptAt = f2.now.Add(-notifyBackoff(3) - time.Minute)
	if res := f2.sweep(t); res.Delivered != 1 {
		t.Errorf("result = %+v, want delivered 1 once the epoch-4 backoff has elapsed", res)
	}
}

// ---------------------------------------------------------------------------
// The cap
// ---------------------------------------------------------------------------

// AT THE CAP THE NOTIFICATION IS QUARANTINED AND NOTHING IS DELIVERED.
//
// Checked BEFORE the attempt, so the capped notification is not delivered one
// last time on the way to being quarantined.
func TestSweepNotifications_AtTheCapItIsQuarantinedAndNotDelivered(t *testing.T) {
	f := newNotifySweepFixture(t)
	f.only().Attempts = maxNotifyAttempts
	f.only().LastAttemptAt = f.now.Add(-30 * time.Hour)

	res := f.sweep(t)

	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d times at the cap; the cap is checked before the attempt precisely so this cannot happen", got)
	}
	if res.Quarantined != 1 {
		t.Fatalf("result = %+v, want quarantined 1", res)
	}
	stuck := f.emitter.byName("notify_undelivered")
	if len(stuck) != 1 {
		t.Fatalf("%d notify_undelivered events, want 1", len(stuck))
	}
	want := DerivedEventID(kindNotifyUndelivered, f.notifyID.String())
	if stuck[0].EventID != want {
		t.Errorf("quarantine id = %s, want the derived %s — N hosts reaching the cap in one pass must write one event, not N", stuck[0].EventID, want)
	}
	// THE CONTRACT STAYS OPEN. Closing it here would hide the one thing an
	// operator needs to see: a result that really is still unobserved.
	if stuck[0].ClosesEventID != nil {
		t.Errorf("notify_undelivered closes %s; quarantine stops the retries, it does not discharge the contract", *stuck[0].ClosesEventID)
	}
}

// ONE ATTEMPT SHORT OF THE CAP IT IS STILL DELIVERED — the positive leg, and the
// off-by-one control on the comparison above.
func TestSweepNotifications_OneAttemptShortOfTheCapItStillDelivers(t *testing.T) {
	f := newNotifySweepFixture(t)
	f.only().Attempts = maxNotifyAttempts - 1
	f.only().LastAttemptAt = f.now.Add(-30 * time.Hour)

	res := f.sweep(t)

	if res.Delivered != 1 || res.Quarantined != 0 {
		t.Errorf("result = %+v, want delivered 1 and quarantined 0 at attempt %d of %d",
			res, maxNotifyAttempts-1, maxNotifyAttempts)
	}
}

// ---------------------------------------------------------------------------
// Failure handling
// ---------------------------------------------------------------------------

// A FAILED INJECTION RECORDS THE ATTEMPT, RECORDS NO DELIVERY, AND DOES NOT STOP
// THE PASS.
//
// All three matter. The recorded attempt is what advances the backoff — without
// it an unreachable recipient is retried at the base interval forever and the cap
// is never reached. The absent notify_delivered is the ack's predicate. And the
// pass continuing is why one wedged recipient cannot stop every other
// notification in the project being swept.
func TestSweepNotifications_AFailedInjectionAdvancesTheBackoffAndContinues(t *testing.T) {
	f := newNotifySweepFixture(t)
	other := NotifyCandidate{
		NotifyEventID: uuid.New(), WorkflowID: uuid.New(), Recipient: "reachable",
		Body: "a result landed", OpenedAt: f.now.Add(-time.Hour),
	}
	f.reader.candidates = append(f.reader.candidates, other)
	f.injector.err = errors.New("recipient stdin is wedged")

	res, err := SweepNotifications(context.Background(), f.deps)
	if err != nil {
		t.Fatalf("SweepNotifications returned %v; an undeliverable recipient must not abort the pass", err)
	}
	if res.Considered != 2 {
		t.Fatalf("result = %+v, want considered 2 — the second candidate was never reached", res)
	}
	if got := len(f.emitter.byName("notify_attempt")); got != 2 {
		t.Errorf("%d notify_attempt events, want 2 — an attempt that is not recorded leaves the backoff and the cap frozen", got)
	}
	if got := len(f.emitter.byName("notify_delivered")); got != 0 {
		t.Errorf("emitted %d notify_delivered events although every injection failed; that record is what lets a turn boundary ack the contract", got)
	}
}

// AN UNREADABLE CANDIDATE LIST IS AN ERROR, not an empty sweep.
//
// Reading it as "nothing outstanding" would report a clean pass over a store it
// could not read — and a sweep that reports zero considered is exactly how a
// backlog of undelivered results stays invisible.
func TestSweepNotifications_AnUnreadableCandidateListIsAnError(t *testing.T) {
	f := newNotifySweepFixture(t)
	f.reader.err = errors.New("connection reset")

	if _, err := SweepNotifications(context.Background(), f.deps); err == nil {
		t.Fatal("SweepNotifications reported a clean pass over a reader that failed")
	}
}

// The deps guards. Each names something without which a gate cannot be
// evaluated, and the sweep must refuse rather than degrade — a sweep missing its
// injector would record attempts and deliver nothing, burning the cap in silence.
func TestSweepNotifications_RefusesIncompleteDeps(t *testing.T) {
	for _, tc := range []struct {
		name  string
		unset func(*NotifySweeperDeps)
	}{
		{"no reader", func(d *NotifySweeperDeps) { d.Notifications = nil }},
		{"no emitter", func(d *NotifySweeperDeps) { d.Emitter = nil }},
		{"no injector", func(d *NotifySweeperDeps) { d.Injector = nil }},
		{"no host", func(d *NotifySweeperDeps) { d.Host = "" }},
		{"no project", func(d *NotifySweeperDeps) { d.ProjectID = uuid.Nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNotifySweepFixture(t)
			tc.unset(&f.deps)
			if _, err := SweepNotifications(context.Background(), f.deps); err == nil {
				t.Errorf("SweepNotifications accepted deps with %s", tc.name)
			}
		})
	}
}

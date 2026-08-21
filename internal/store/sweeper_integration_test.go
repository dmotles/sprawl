//go:build store_pg

package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The sweeper's candidate query against a real Postgres (QUM-1250, AC5/AC6).
//
// The hermetic suite decides the GATES from a hand-built candidate. This file
// asserts that a real log produces the candidate the gates were given — which is
// the half that cannot be faked, and the half where a wrong predicate hides:
// every term here is derived (liveness from turn boundaries, the epoch from a
// poke count, quarantine from an event's existence), so a subtly wrong subquery
// produces a plausible candidate rather than an error.

type sweepEnv struct {
	*notifyEnv
	reader *PgSweepReader
}

func newSweepEnv(t *testing.T) *sweepEnv {
	t.Helper()
	e := newNotifyEnv(t)
	return &sweepEnv{notifyEnv: e, reader: &PgSweepReader{Pool: e.pool, Registry: e.registry}}
}

func (e *sweepEnv) candidates(t *testing.T) []StalledCandidate {
	t.Helper()
	got, err := e.reader.OpenGoals(context.Background(), e.projectID)
	if err != nil {
		t.Fatalf("OpenGoals: %v", err)
	}
	return got
}

func (e *sweepEnv) only(t *testing.T) StalledCandidate {
	t.Helper()
	got := e.candidates(t)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 candidate, got %d: %+v", len(got), got)
	}
	return got[0]
}

func (e *sweepEnv) emit(t *testing.T, typeName string, payload map[string]any) uuid.UUID {
	t.Helper()
	id, err := e.emitter.Emit(context.Background(), EmitRequest{
		TypeName: typeName, TypeVersion: 1,
		WorkflowInstanceID: uuid.New(),
		Payload:            payload,
	})
	if err != nil {
		t.Fatalf("emitting %s: %v", typeName, err)
	}
	return id
}

// ---------------------------------------------------------------------------
// The candidate set
// ---------------------------------------------------------------------------

// AN OPEN GOAL IS A CANDIDATE; A CLOSED ONE IS NOT.
//
// Both halves in one test because each is the other's control: a query returning
// everything passes the first, and one returning nothing passes the second.
func TestSweepPg_OnlyOpenGoalsAreCandidates(t *testing.T) {
	e := newSweepEnv(t)
	open := e.openGoal(t, "alice")
	closed := e.openGoal(t, "alice")
	e.closeGoal(t, closed)

	got := e.candidates(t)
	if len(got) != 1 {
		t.Fatalf("%d candidates, want 1 (the open goal only): %+v", len(got), got)
	}
	if got[0].GoalEventID != open {
		t.Errorf("candidate is %s, want the OPEN goal %s", got[0].GoalEventID, open)
	}
	if got[0].Owner != "alice" {
		t.Errorf("candidate owner is %q, want alice — the owner is read from the payload", got[0].Owner)
	}
}

// LIVENESS IS max(at) OVER TURN BOUNDARIES, FOR THIS OWNER.
//
// Three things asserted at once because they are one predicate: it uses
// turn-boundary events, it takes the LATEST, and it is scoped to the right agent.
func TestSweepPg_LastActivityIsTheOwnersLatestTurnBoundary(t *testing.T) {
	e := newSweepEnv(t)
	e.openGoal(t, "alice")

	// Another agent's turn must not count as alice's activity — otherwise a busy
	// fleet makes every idle owner look alive and nothing is ever swept.
	e.emit(t, "turn_finished", map[string]any{
		"agent_name": "bob", "session_id": "s0", "input_tokens": 1, "output_tokens": 1,
	})
	before := e.only(t)
	if !before.LastOwnerActivity.IsZero() {
		t.Fatalf("alice has activity %v after only BOB took a turn; liveness is not scoped to the owner", before.LastOwnerActivity)
	}

	// run_started counts as a boundary too: an agent mid-first-turn is alive.
	e.emit(t, "run_started", map[string]any{
		"agent_name": "alice", "agent_type": "engineer", "session_id": "s1",
	})
	started := e.only(t)
	if started.LastOwnerActivity.IsZero() {
		t.Fatal("run_started does not count as activity, so an agent inside its first turn reads as never-alive and gets poked")
	}

	// And the LATEST wins.
	e.emit(t, "turn_finished", map[string]any{
		"agent_name": "alice", "session_id": "s1", "input_tokens": 1, "output_tokens": 1,
	})
	latest := e.only(t)
	if !latest.LastOwnerActivity.After(started.LastOwnerActivity) {
		t.Errorf("last activity did not advance with a later turn boundary: %v then %v", started.LastOwnerActivity, latest.LastOwnerActivity)
	}
}

// AN OWNER WITH NO TURN BOUNDARY AT ALL HAS A ZERO TIMESTAMP, not now().
//
// The sweeper reads zero as "never took a turn" and falls back to the goal's own
// age. Substituting now() — the tempting COALESCE — would make every such goal
// permanently fresh and therefore never swept, silently.
func TestSweepPg_NoActivityIsAZeroTimeNotNow(t *testing.T) {
	e := newSweepEnv(t)
	e.openGoal(t, "never-ran")

	got := e.only(t)
	if !got.LastOwnerActivity.IsZero() {
		t.Errorf("LastOwnerActivity = %v for an owner that never took a turn, want the zero time — a substituted now() makes the goal immortal", got.LastOwnerActivity)
	}
	if got.OpenedAt.IsZero() {
		t.Error("OpenedAt is zero, so the fallback the sweeper uses instead is also unusable")
	}
}

// THE EPOCH IS A COUNT OF goal_poke EVENTS FOR THIS GOAL, and last-poke tracks
// the latest.
func TestSweepPg_PokeCountAndLastPokeAreDerivedFromTheLog(t *testing.T) {
	e := newSweepEnv(t)
	goal := e.openGoal(t, "alice")
	other := e.openGoal(t, "alice")

	if got := e.candidates(t); got[0].Pokes != 0 {
		t.Fatalf("a never-poked goal reports %d pokes, want 0", got[0].Pokes)
	}
	for i := 0; i < 3; i++ {
		e.emit(t, "goal_poke", map[string]any{
			"goal_event_id": goal.String(), "owner": "alice", "epoch": i,
		})
	}
	// A poke for a DIFFERENT goal must not raise this goal's epoch — otherwise a
	// busy project quarantines every goal at once.
	e.emit(t, "goal_poke", map[string]any{
		"goal_event_id": other.String(), "owner": "alice", "epoch": 0,
	})

	for _, c := range e.candidates(t) {
		switch c.GoalEventID {
		case goal:
			if c.Pokes != 3 {
				t.Errorf("goal reports %d pokes, want 3", c.Pokes)
			}
			if c.LastPokeAt.IsZero() {
				t.Error("LastPokeAt is zero on a poked goal, so the backoff window can never elapse... or never applies")
			}
		case other:
			if c.Pokes != 1 {
				t.Errorf("the other goal reports %d pokes, want 1 — poke counts are leaking between goals", c.Pokes)
			}
		}
	}
}

// QUARANTINE IS THE EXISTENCE OF A goal_stuck EVENT, per goal.
func TestSweepPg_QuarantineIsDerivedPerGoal(t *testing.T) {
	e := newSweepEnv(t)
	stuck := e.openGoal(t, "alice")
	fine := e.openGoal(t, "alice")

	e.emit(t, "goal_stuck", map[string]any{
		"goal_event_id": stuck.String(), "owner": "alice", "pokes": maxGoalPokes,
	})

	for _, c := range e.candidates(t) {
		switch c.GoalEventID {
		case stuck:
			if !c.Quarantined {
				t.Error("a goal with a goal_stuck event is not reported quarantined, so it keeps being poked forever")
			}
		case fine:
			if c.Quarantined {
				t.Error("a goal with NO goal_stuck event is reported quarantined, so it is never poked — quarantine is leaking between goals")
			}
		}
	}
}

// THE TRANSITIVE-BLOCK TERM EXCLUDES THE GOAL ITSELF.
//
// The single most consequential detail in the query. Without the exclusion every
// open goal is its own blocker, `other_open` is never zero, and the sweeper pokes
// NOTHING — a total failure that presents as a quiet fleet with no error
// anywhere.
func TestSweepPg_OtherOpenContractsExcludesTheGoalItself(t *testing.T) {
	e := newSweepEnv(t)
	e.openGoal(t, "alice")

	got := e.only(t)
	if got.OtherOpenContracts != 0 {
		t.Fatalf("a single open goal reports %d OTHER open contracts, want 0 — the goal is counting itself, so the sweeper will never poke anything", got.OtherOpenContracts)
	}
}

// POSITIVE CONTROL for the exclusion: a SECOND open contract owned by the same
// agent IS counted.
//
// Direction stated because the test above is a zero, and a zero is also what a
// term that counts nothing at all produces. This is the leg that proves the term
// can be non-zero.
func TestSweepPg_ASecondOpenContractForTheSameOwnerIsCounted(t *testing.T) {
	e := newSweepEnv(t)
	e.openGoal(t, "alice")
	e.openGoal(t, "alice") // a delegated child goal, from alice's point of view

	for _, c := range e.candidates(t) {
		if c.OtherOpenContracts != 1 {
			t.Errorf("goal %s reports %d other open contracts, want 1 — a blocked owner would be poked about work it cannot proceed with", c.GoalEventID, c.OtherOpenContracts)
		}
	}
}

// An open OWNER_NOTIFY also blocks, which is what makes "an open question" count
// without a question type existing yet.
//
// The term reads `owner` OR `recipient`, so any open contract addressed to the
// agent counts — including the notification contracts commit 5 introduced. That
// generality is deliberate: M3 adds USER_QUESTION and it will be counted without
// this query changing.
func TestSweepPg_AnOpenNotificationBlocksItsRecipient(t *testing.T) {
	e := newSweepEnv(t)
	e.openGoal(t, "alice")

	// Land a result for a DIFFERENT owner's goal, addressed to alice.
	notifyGoal := e.openGoal(t, "alice")
	if err := e.handler(t, e.injector).Handle(context.Background(), e.closeGoal(t, notifyGoal)); err != nil {
		t.Fatalf("notifying: %v", err)
	}

	got := e.candidates(t)
	if len(got) != 1 {
		t.Fatalf("%d candidates, want 1: %+v", len(got), got)
	}
	if got[0].OtherOpenContracts == 0 {
		t.Error("an unacked owner_notify addressed to alice does not block her goal; the sweeper would poke an agent that has a notification waiting")
	}
}

// Another owner's open contract does NOT block this one.
func TestSweepPg_AnotherOwnersContractDoesNotBlock(t *testing.T) {
	e := newSweepEnv(t)
	e.openGoal(t, "alice")
	e.openGoal(t, "bob")

	for _, c := range e.candidates(t) {
		if c.OtherOpenContracts != 0 {
			t.Errorf("goal owned by %s reports %d other open contracts, want 0 — blocking is leaking across owners, so a busy fleet is never swept", c.Owner, c.OtherOpenContracts)
		}
	}
}

// ---------------------------------------------------------------------------
// End to end
// ---------------------------------------------------------------------------

// A FULL SWEEP OVER A REAL LOG POKES A STALLED OWNER EXACTLY ONCE, AND THE
// SECOND SWEEP DOES NOT REPEAT IT.
//
// The second half is the part that needs a real log: the first sweep's own
// goal_poke event is what raises the epoch and starts the backoff, so
// "pokes once" and "does not immediately poke again" are the same mechanism
// observed twice.
func TestSweepPg_SweepPokesOnceThenRespectsItsOwnBackoff(t *testing.T) {
	e := newSweepEnv(t)
	e.openGoal(t, "alice")

	local := &fakeLocalAgents{agents: []LocalAgent{{Name: "alice", Status: "active"}}}
	inj := &recordingInjector{}
	deps := SweeperDeps{
		Goals: e.reader, Local: local, Claims: &PgClaimStore{Pool: e.pool},
		Emitter: e.emitter, Injector: inj,
		ProjectID: e.projectID, Host: "host-a",
		// A ONE-NANOSECOND stall threshold rather than an advanced clock, and the
		// difference matters. The first version of this test forced the stall with
		// Now = time.Now().Add(time.Hour), which ALSO made every backoff window
		// look elapsed — so the second sweep poked again and the test failed for a
		// reason that was entirely the fixture's. The sweeper's clock and the log's
		// `at` timestamps have to stay on one timeline, because the backoff is
		// measured BETWEEN them.
		StallAfter: time.Nanosecond,
		// No election — the sweeper must be correct without it.
	}

	first, err := Sweep(context.Background(), deps)
	if err != nil {
		t.Fatalf("first Sweep: %v", err)
	}
	if first.Poked != 1 {
		t.Fatalf("first sweep poked %d, want 1", first.Poked)
	}
	if got := inj.count(); got != 1 {
		t.Fatalf("injected %d pokes, want 1", got)
	}

	// Immediately again: the epoch is now 1 and the backoff has not elapsed.
	second, err := Sweep(context.Background(), deps)
	if err != nil {
		t.Fatalf("second Sweep: %v", err)
	}
	if second.Poked != 0 {
		t.Errorf("the second sweep poked %d times, want 0 — the sweeper is ignoring the backoff it just started", second.Poked)
	}
	if got := inj.count(); got != 1 {
		t.Errorf("%d pokes delivered in total, want 1", got)
	}
	if got := e.eventCount(t, "goal_poke"); got != 1 {
		t.Errorf("%d goal_poke events, want 1", got)
	}
}

// TWO SWEEPERS, ONE POKE — with the election DISABLED on both.
//
// This is what makes the election an efficiency measure rather than a correctness
// one: the (goal, epoch) claim already admits exactly one poker. If this test ever
// fails, the election has become load-bearing and the claim key is wrong.
func TestSweepPg_TwoSweepersWithNoElectionProduceOnePoke(t *testing.T) {
	e := newSweepEnv(t)
	e.openGoal(t, "alice")

	// BOTH SWEEPERS GET THE SAME CANDIDATE SNAPSHOT, which is what split-brain
	// actually means: two hosts read the log at the same moment, compute the SAME
	// epoch, and both try to poke it.
	//
	// Running them sequentially against the live reader does NOT test that, and
	// that is how the first version of this test failed: the second sweeper
	// legitimately saw epoch 1 and was held back by the backoff — a different
	// mechanism, so the assertion was measuring the wrong one. Freezing the
	// snapshot leaves the (goal, epoch) claim as the only thing that can decide
	// the outcome, deterministically and with no sleep.
	snapshot := e.candidates(t)
	if len(snapshot) != 1 || snapshot[0].Pokes != 0 {
		t.Fatalf("snapshot is %+v, want exactly one never-poked candidate — otherwise the assertion below is about something else", snapshot)
	}
	frozen := &fakeSweepReader{candidates: snapshot}

	shared := &recordingInjector{}
	for _, host := range []string{"host-a", "host-b"} {
		deps := SweeperDeps{
			Goals:  frozen,
			Local:  &fakeLocalAgents{agents: []LocalAgent{{Name: "alice", Status: "active"}}},
			Claims: &PgClaimStore{Pool: e.pool}, Emitter: e.emitter, Injector: shared,
			ProjectID: e.projectID, Host: host, StallAfter: time.Nanosecond,
		}
		if _, err := Sweep(context.Background(), deps); err != nil {
			t.Fatalf("Sweep(%s): %v", host, err)
		}
	}

	if got := shared.count(); got != 1 {
		t.Errorf("two sweepers delivered %d pokes, want exactly 1 — the (goal, epoch) claim is not admitting one poker", got)
	}
	if got := e.eventCount(t, "goal_poke"); got != 1 {
		t.Errorf("%d goal_poke events, want 1", got)
	}
}

// AC6 END TO END: a permanently silent owner reaches goal_stuck and then costs
// nothing.
//
// Driven by advancing an injected clock rather than by sleeping, so the whole
// backoff schedule is traversed in milliseconds. "Stops consuming tokens" is
// asserted as the delivery count going flat, not as the presence of the event.
func TestSweepPg_APermanentlyStalledGoalReachesGoalStuckAndStops(t *testing.T) {
	e := newSweepEnv(t)
	e.openGoal(t, "alice")

	inj := &recordingInjector{}
	clock := time.Now()
	deps := SweeperDeps{
		Goals:  e.reader,
		Local:  &fakeLocalAgents{agents: []LocalAgent{{Name: "alice", Status: "active"}}},
		Claims: &PgClaimStore{Pool: e.pool}, Emitter: e.emitter, Injector: inj,
		ProjectID: e.projectID, Host: "host-a", StallAfter: time.Minute,
		Now: func() time.Time { return clock },
	}

	// Sweep repeatedly, jumping the clock past each backoff window.
	for i := 0; i < maxGoalPokes+4; i++ {
		clock = clock.Add(24 * time.Hour)
		if _, err := Sweep(context.Background(), deps); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}

	if got := e.eventCount(t, "goal_poke"); got != maxGoalPokes {
		t.Errorf("%d goal_poke events, want exactly %d — the cap is not holding", got, maxGoalPokes)
	}
	if got := inj.count(); got != maxGoalPokes {
		t.Errorf("%d pokes delivered, want %d", got, maxGoalPokes)
	}
	if got := e.eventCount(t, "goal_stuck"); got != 1 {
		t.Errorf("%d goal_stuck events, want exactly 1 — a re-quarantine per sweep is its own noise", got)
	}

	// And it stays flat.
	before := inj.count()
	for i := 0; i < 10; i++ {
		clock = clock.Add(24 * time.Hour)
		if _, err := Sweep(context.Background(), deps); err != nil {
			t.Fatalf("post-quarantine sweep %d: %v", i, err)
		}
	}
	if got := inj.count(); got != before {
		t.Errorf("a quarantined goal delivered %d further pokes across 10 sweeps, want 0 — this is the token burn AC6 exists to stop", got-before)
	}
}

// ---------------------------------------------------------------------------
// The election, for real
// ---------------------------------------------------------------------------

// The election is pg_try_advisory_xact_lock and it GRANTS on a free lock.
//
// Asserted because a knob nobody exercises is a knob that has quietly stopped
// working: the whole sweeper suite runs with the election disabled, so without
// this the real implementation could be permanently broken and every other test
// would stay green.
func TestSweepPg_ElectionGrantsOnAFreeLock(t *testing.T) {
	e := newSweepEnv(t)
	elect := PgSweepElection(e.pool, e.projectID)

	won, err := elect(context.Background())
	if err != nil {
		t.Fatalf("election: %v", err)
	}
	if !won {
		t.Error("the election declined on a free lock, so no host would ever sweep")
	}
}

// A sweep with the REAL election wired still pokes.
//
// The composition check: the election being individually correct and the sweep
// being individually correct would not catch a wiring mistake that made every
// sweep decline.
func TestSweepPg_SweepWithTheRealElectionStillPokes(t *testing.T) {
	e := newSweepEnv(t)
	e.openGoal(t, "alice")

	inj := &recordingInjector{}
	res, err := Sweep(context.Background(), SweeperDeps{
		Goals:  e.reader,
		Local:  &fakeLocalAgents{agents: []LocalAgent{{Name: "alice", Status: "active"}}},
		Claims: &PgClaimStore{Pool: e.pool}, Emitter: e.emitter, Injector: inj,
		ProjectID: e.projectID, Host: "host-a", StallAfter: time.Nanosecond,
		Elect: PgSweepElection(e.pool, e.projectID),
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !res.Elected {
		t.Fatal("the sweep reported it was not elected against a free lock")
	}
	if got := inj.count(); got != 1 {
		t.Errorf("delivered %d pokes with the real election wired, want 1", got)
	}
}

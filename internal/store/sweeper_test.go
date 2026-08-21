package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/google/uuid"
)

// The taxonomy is read from internal/state rather than spelled as literals, so
// there is ONE definition of what "paused" and "terminal" mean and a change there
// cannot leave these gates asserting a status the product no longer uses.
const pausedStatus = state.StatusPaused

func terminalStatuses() []string {
	return []string{state.StatusRetired, state.StatusRetiring}
}

// The stall sweeper (QUM-1250, AC5 and AC6).
//
// HOW THIS FILE IS ORGANISED, and why it matters more here than anywhere else in
// the diff: AC5 asks for four does-NOT-poke properties, and a
// does-not-happen assertion is the single easiest thing in this repo to write
// vacuously. "No poke happened" is satisfied perfectly by a sweeper that never
// pokes anything, by a fixture that never produced a candidate, and by a typo in
// a fixture field name.
//
// So EVERY gate is written as a PAIR, adjacently, over one fixture that differs
// in exactly one field:
//
//	the negative leg  — the gate holds, nothing is poked
//	the positive leg  — the SAME fixture with that one field flipped IS poked
//
// The positive leg is the control, and it is what makes the negative leg mean
// anything. It is stated in each test's comment which direction it is, because
// naming a control never tells you it is aimed right.

// ---------------------------------------------------------------------------
// Doubles
// ---------------------------------------------------------------------------

type fakeSweepReader struct {
	mu         sync.Mutex
	candidates []StalledCandidate
	err        error
}

func (f *fakeSweepReader) OpenGoals(context.Context, uuid.UUID) ([]StalledCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return append([]StalledCandidate(nil), f.candidates...), nil
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type sweepFixture struct {
	reader   *fakeSweepReader
	local    *fakeLocalAgents
	claims   *fakeClaims
	emitter  *recordingEmitter
	injector *recordingInjector
	now      time.Time
	deps     SweeperDeps
	goalID   uuid.UUID
}

// newSweepFixture builds a fixture whose single candidate IS stalled and IS
// pokeable — so every gate test below is one field away from a poke, and the
// baseline itself is asserted by TestSweeper_AStalledOwnerIsPoked.
func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	goalID := uuid.New()

	f := &sweepFixture{
		reader:   &fakeSweepReader{},
		local:    &fakeLocalAgents{},
		claims:   newFakeClaims(),
		emitter:  &recordingEmitter{},
		injector: &recordingInjector{},
		now:      now,
		goalID:   goalID,
	}
	f.reader.candidates = []StalledCandidate{{
		GoalEventID: goalID,
		WorkflowID:  uuid.New(),
		Owner:       "alice",
		GoalType:    "research",
		OpenedAt:    now.Add(-3 * time.Hour),
		// Idle well past the stall threshold.
		LastOwnerActivity: now.Add(-2 * time.Hour),
	}}
	f.local.agents = []LocalAgent{{Name: "alice", Status: "active", Turn: TurnIdle}}
	f.deps = SweeperDeps{
		Goals:      f.reader,
		Local:      f.local,
		Emitter:    f.emitter,
		Injector:   f.injector,
		ProjectID:  uuid.New(),
		Host:       "host-a",
		StallAfter: 30 * time.Minute,
		Now:        func() time.Time { return f.now },
	}
	return f
}

func (f *sweepFixture) candidate() *StalledCandidate { return &f.reader.candidates[0] }

func (f *sweepFixture) sweep(t *testing.T) SweepResult {
	t.Helper()
	res, err := Sweep(context.Background(), f.deps)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	return res
}

// assertPoked / assertNotPoked read the same three observables, so a gate test
// and its control cannot accidentally measure different things.
func (f *sweepFixture) assertPoked(t *testing.T, why string) {
	t.Helper()
	if got := f.injector.count(); got != 1 {
		t.Errorf("%s: injected %d pokes, want 1 — this is the POSITIVE CONTROL leg and it must fire", why, got)
	}
	if got := len(f.emitter.byName("goal_poke")); got != 1 {
		t.Errorf("%s: %d goal_poke events, want 1", why, got)
	}
}

func (f *sweepFixture) assertNotPoked(t *testing.T, why string) {
	t.Helper()
	if got := f.injector.count(); got != 0 {
		t.Errorf("%s: injected %d pokes, want 0", why, got)
	}
	if got := f.emitter.names(); len(got) != 0 {
		t.Errorf("%s: emitted %v, want nothing", why, got)
	}
}

// assertGate asserts WHICH gate held the candidate back.
//
// Added because code review found the systemic version of a bug I had already
// hand-fixed once: assertNotPoked reads only "nothing happened", so EVERY
// negative leg is satisfied by ANY earlier gate. One test (human-owned) was
// found asserting the not-on-this-host gate by accident; fixing that test left
// the weakness in place for the rest, and a reordering of gateFor would leave the
// whole file green while three gates went untested.
//
// gateFor already returns the reason precisely so this could be asserted, and
// nothing was asserting it.
func (f *sweepFixture) assertGate(t *testing.T, res SweepResult, wantSubstring string) {
	t.Helper()
	got, ok := res.SkipReasons[f.goalID]
	if !ok {
		t.Fatalf("no skip reason recorded for the goal; the sweep did not skip it at all (result %+v)", res)
	}
	if !strings.Contains(got, wantSubstring) {
		t.Errorf("the goal was skipped for the reason %q, but this test is about %q — the assertion is aimed at a different gate than it names",
			got, wantSubstring)
	}
}

// ---------------------------------------------------------------------------
// The baseline: the sweeper does poke
// ---------------------------------------------------------------------------

// THE BASELINE, and every gate test below is this fixture with one field
// changed. If this ever fails, every does-NOT-poke assertion in this file becomes
// vacuous at once, so it is deliberately the first test in the file.
func TestSweeper_AStalledOwnerIsPoked(t *testing.T) {
	f := newSweepFixture(t)
	res := f.sweep(t)

	f.assertPoked(t, "a stalled, live, unblocked owner")
	if res.Poked != 1 {
		t.Errorf("Poked = %d, want 1", res.Poked)
	}
	pokes := f.emitter.byName("goal_poke")
	payload := fmt.Sprint(pokes[0].Payload)
	if !strings.Contains(payload, f.goalID.String()) {
		t.Errorf("the poke does not name the goal it is about: %s", payload)
	}
	if got := f.injector.all()[0].Recipient; got != "alice" {
		t.Errorf("poked %q, want alice", got)
	}
}

// A goal whose owner has been active RECENTLY is not stalled at all.
//
// Negative control for the baseline. Without it, a sweeper that poked every open
// goal on every pass would satisfy the baseline perfectly and would poke every
// working agent every few seconds.
func TestSweeper_ARecentlyActiveOwnerIsNotStalled(t *testing.T) {
	f := newSweepFixture(t)
	f.candidate().LastOwnerActivity = f.now.Add(-time.Minute) // threshold is 30m

	res := f.sweep(t)
	f.assertNotPoked(t, "an owner active one minute ago")
	f.assertGate(t, res, "stall threshold")
}

// LIVENESS COMES FROM TURN BOUNDARIES, NOT FROM WALL-CLOCK HEARTBEATS
// (Appendix B item 4). A long QUIET turn is legitimate: an agent can spend an
// hour inside one turn on a slow build. The supervisor heartbeat was deleted for
// exactly this reason (QUM-730/QUM-1071), and reintroducing a wall-clock notion
// of liveness here would re-create the defect one layer up.
//
// So an owner with a recent turn boundary is alive even though the goal has been
// open for hours — asserted by the test above — and this one asserts the other
// half: a goal open for hours whose owner has NEVER produced a turn boundary is
// judged on the GOAL's age, not treated as immortal because there is no activity
// timestamp to compare.
func TestSweeper_AnOwnerWithNoTurnBoundaryAtAllIsJudgedOnTheGoalsAge(t *testing.T) {
	f := newSweepFixture(t)
	f.candidate().LastOwnerActivity = time.Time{} // never took a turn
	f.candidate().OpenedAt = f.now.Add(-3 * time.Hour)

	f.sweep(t)
	f.assertPoked(t, "a goal open for three hours whose owner never took a turn")
}

// ... and the same with a YOUNG goal is left alone. The positive/negative pair
// for the no-activity path: without this leg, a sweeper that poked every
// activity-less owner would poke a goal opened one second ago.
func TestSweeper_AYoungGoalWithNoTurnBoundaryIsLeftAlone(t *testing.T) {
	f := newSweepFixture(t)
	f.candidate().LastOwnerActivity = time.Time{}
	f.candidate().OpenedAt = f.now.Add(-time.Minute)

	f.sweep(t)
	f.assertNotPoked(t, "a goal opened one minute ago whose owner has not taken a turn yet")
}

// ---------------------------------------------------------------------------
// AC5: the four does-NOT-poke gates, each with its would-have-poked control
// ---------------------------------------------------------------------------

// GATE 1 — IN TURN. Never poke an agent that is mid-turn.
//
// It is working, and a poke is at best noise and at worst a preemption. The
// AgentState taxonomy is the sole wake arbiter and InTurn is its observation.
func TestSweeper_DoesNotPokeAnInTurnOwner(t *testing.T) {
	f := newSweepFixture(t)
	f.local.agents = []LocalAgent{{Name: "alice", Status: "active", Turn: TurnInTurn}}

	res := f.sweep(t)
	f.assertNotPoked(t, "an owner that is mid-turn")
	f.assertGate(t, res, "mid-turn")
}

// GATE 1b — AN UNOBSERVED TURN STATE IS NOT AN IDLE ONE.
//
// The defect this guards was live in the first version of the sweeper and was
// found while writing the adapters, not by review: turn state exists ONLY in the
// supervisor's in-memory phase machine (internal/runtime's
// UnifiedRuntime.State().InTurn) and has no on-disk representation at all, so a
// sweeper running outside that process CANNOT observe it. With a plain bool such
// a process is forced to report `false`, the sweeper reads that as "idle", and
// every working agent gets poked — silently, on every sweep, with nothing in the
// log but a lot of pokes.
//
// internal/supervisor/runtime.go's InTurnObserved already names exactly this:
// "Accepting the session probe's 'not in turn' when the authority is absent
// would be a negative answer derived from an unavailable observation." Same
// shape, one layer up.
func TestSweeper_DoesNotPokeWhenTurnStateIsUnobservable(t *testing.T) {
	f := newSweepFixture(t)
	// The shape a non-supervisor process produces: idle-looking, but unknown.
	f.local.agents = []LocalAgent{{Name: "alice", Status: "active", Turn: TurnUnknown}}

	res := f.sweep(t)
	f.assertNotPoked(t, "an owner whose turn state this process cannot observe")
	f.assertGate(t, res, "not observable")
}

// POSITIVE CONTROL for gate 1b: the same fixture with the observation available.
//
// Direction: without this leg, a sweeper that skipped EVERYTHING would satisfy
// gate 1b perfectly — and since the whole point of the tri-state is to make one
// deployment inert rather than all of them, that is the failure mode it would
// hide.
func TestSweeper_PokesWhenTurnStateIsObservedAndIdle(t *testing.T) {
	f := newSweepFixture(t)
	f.local.agents = []LocalAgent{{Name: "alice", Status: "active", Turn: TurnIdle}}

	f.sweep(t)
	f.assertPoked(t, "the identical fixture with turn state observed and idle")
}

// POSITIVE CONTROL for gate 1: the same fixture with InTurn false IS poked.
func TestSweeper_PokesTheSameOwnerWhenNotInTurn(t *testing.T) {
	f := newSweepFixture(t)
	f.local.agents = []LocalAgent{{Name: "alice", Status: "active", Turn: TurnIdle}}

	f.sweep(t)
	f.assertPoked(t, "the identical fixture with InTurn flipped to false")
}

// GATE 2 — OPERATOR-PAUSED. StatusPaused is deliberately excluded from
// auto-resume, and a poke that woke a paused agent would override an operator's
// explicit decision. state.StatusPaused is imported rather than spelled as a
// literal, so there is one definition of the taxonomy.
func TestSweeper_DoesNotPokeAPausedOwner(t *testing.T) {
	f := newSweepFixture(t)
	f.local.agents = []LocalAgent{{Name: "alice", Status: pausedStatus, Turn: TurnIdle}}

	res := f.sweep(t)
	f.assertNotPoked(t, "an operator-paused owner")
	f.assertGate(t, res, "operator-paused")
}

// POSITIVE CONTROL for gate 2: same fixture, status active.
func TestSweeper_PokesTheSameOwnerWhenNotPaused(t *testing.T) {
	f := newSweepFixture(t)
	f.local.agents = []LocalAgent{{Name: "alice", Status: "active", Turn: TurnIdle}}

	f.sweep(t)
	f.assertPoked(t, "the identical fixture with the status flipped to active")
}

// GATE 3 — HUMAN-OWNED WAIT. There is no process to poke.
//
// A goal owned by the human is waiting on a person, and the sweeper has no way to
// nudge one. Poking would either fail on every pass or, worse, deliver to some
// agent that happens to share the name.
func TestSweeper_DoesNotPokeAHumanOwnedWait(t *testing.T) {
	f := newSweepFixture(t)
	f.candidate().Owner = HumanOwner
	// The human owner is deliberately PRESENT in the local set.
	//
	// The first version of this test set local.agents to nil, and the control
	// proved it vacuous: with the human gate removed the test still passed,
	// because the owner-is-not-on-this-host gate caught it instead. The assertion
	// was about gate ORDER by accident rather than about the human gate at all.
	//
	// Making the owner resolvable leaves the human gate as the only thing that
	// can hold — and it also asserts the property that actually matters: the
	// human is never poked EVEN IF something answering to that name exists
	// locally, so the gate does not depend on an absence to work.
	f.local.agents = []LocalAgent{{Name: HumanOwner, Status: "active", Turn: TurnIdle}}

	res := f.sweep(t)
	f.assertNotPoked(t, "a goal owned by the human, with that name resolvable locally")
	f.assertGate(t, res, "human-owned")
}

// POSITIVE CONTROL for gate 3: the same fixture owned by an AGENT is poked.
//
// Without it, a sweeper that refused every owner it could not find locally would
// satisfy gate 3 while never poking anything on a host that does not happen to
// hold the owner.
func TestSweeper_PokesTheSameGoalWhenAnAgentOwnsIt(t *testing.T) {
	f := newSweepFixture(t)
	f.candidate().Owner = "alice"

	f.sweep(t)
	f.assertPoked(t, "the identical fixture owned by an agent rather than the human")
}

// GATE 4 — TRANSITIVELY BLOCKED. An owner with another open contract is waiting,
// not stalled.
//
// That other contract is a child goal it delegated, or a question it asked. The
// owner is quiet BECAUSE it is blocked, so poking it tells it to get on with
// something it cannot get on with — and, because pokes cost tokens, does so
// repeatedly.
func TestSweeper_DoesNotPokeATransitivelyBlockedOwner(t *testing.T) {
	f := newSweepFixture(t)
	f.candidate().OtherOpenContracts = 1

	res := f.sweep(t)
	f.assertNotPoked(t, "an owner with another open contract outstanding")
	f.assertGate(t, res, "transitively blocked")
}

// POSITIVE CONTROL for gate 4: same fixture with that contract closed.
func TestSweeper_PokesTheSameOwnerOnceItIsUnblocked(t *testing.T) {
	f := newSweepFixture(t)
	f.candidate().OtherOpenContracts = 0

	f.sweep(t)
	f.assertPoked(t, "the identical fixture with the blocking contract closed")
}

// GATE 5 — TERMINAL. A retired or retiring owner cannot be woken at all.
//
// Not one of AC5's four, but the same class and the same hazard: state.IsTerminal
// is the repo's own predicate and is imported rather than reimplemented.
func TestSweeper_DoesNotPokeATerminalOwner(t *testing.T) {
	for _, status := range terminalStatuses() {
		f := newSweepFixture(t)
		f.local.agents = []LocalAgent{{Name: "alice", Status: status, Turn: TurnIdle}}

		f.sweep(t)
		f.assertNotPoked(t, "an owner whose status is "+status)
	}
}

// POSITIVE CONTROL for gate 5: a REVIVABLE resting status IS poked.
//
// This is the leg that matters, because the tempting implementation is "skip
// anything that is not active" — which would silently stop poking every
// suspended, idle or complete owner, i.e. exactly the agents a stalled goal is
// most likely to belong to.
func TestSweeper_PokesARevivableRestingOwner(t *testing.T) {
	for _, status := range []string{"suspended", "idle", "complete"} {
		f := newSweepFixture(t)
		f.local.agents = []LocalAgent{{Name: "alice", Status: status, Turn: TurnIdle}}

		f.sweep(t)
		f.assertPoked(t, "an owner resting in status "+status+" (revivable, not terminal)")
	}
}

// ---------------------------------------------------------------------------
// Backoff and AC6's cap
// ---------------------------------------------------------------------------

// BACKOFF IS EXPONENTIAL IN THE POKE COUNT, and the SCHEDULE is asserted rather
// than the count.
//
// Asserting only "it eventually stops" would pass an implementation that poked
// five times in five seconds and then quarantined — technically capped, and it
// would burn five pokes' worth of tokens in a moment while telling the agent
// nothing new.
func TestSweeper_BackoffIsExponentialInThePokeCount(t *testing.T) {
	for epoch := 0; epoch < 4; epoch++ {
		wait := pokeBackoff(epoch)
		want := time.Duration(1<<epoch) * pokeBackoffBase
		if wait != want {
			t.Errorf("pokeBackoff(%d) = %v, want %v", epoch, wait, want)
		}
	}
	// And it is monotonic, which is the property a future cap must not break.
	for epoch := 1; epoch < 8; epoch++ {
		if pokeBackoff(epoch) <= pokeBackoff(epoch-1) {
			t.Errorf("pokeBackoff is not increasing at epoch %d: %v then %v", epoch, pokeBackoff(epoch-1), pokeBackoff(epoch))
		}
	}
}

// A goal poked RECENTLY is not poked again until its backoff has elapsed.
func TestSweeper_RespectsTheBackoffWindow(t *testing.T) {
	f := newSweepFixture(t)
	f.candidate().Pokes = 2
	// One second INSIDE the window for the poke that already happened (epoch 1).
	// Pinning the boundary rather than picking a comfortably small elapsed time:
	// "one second ago" is blocked by any backoff at all, including a wrong one,
	// so it would not distinguish the schedule from a flat delay.
	f.candidate().LastPokeAt = f.now.Add(-pokeBackoff(1) + time.Second)

	res := f.sweep(t)
	f.assertNotPoked(t, "a goal one second inside its backoff window")
	f.assertGate(t, res, "backoff")
}

// POSITIVE CONTROL for the backoff: the same fixture with the window elapsed IS
// poked, and the poke records the epoch it belongs to.
func TestSweeper_PokesAgainOnceTheBackoffHasElapsed(t *testing.T) {
	f := newSweepFixture(t)
	f.candidate().Pokes = 2
	// Exactly ON the boundary for epoch 1's backoff. Together with the test
	// above this pins the schedule to the second, not merely "eventually".
	f.candidate().LastPokeAt = f.now.Add(-pokeBackoff(1))

	f.sweep(t)
	f.assertPoked(t, "the identical fixture exactly on its backoff boundary")

	// The epoch is read out of the payload MAP rather than matched in a rendered
	// string. An earlier version of this assertion was
	// `contains("epoch:2") || contains("2")`, and that second clause is
	// satisfied by any payload containing the digit 2 anywhere — including the
	// goal uuid, which almost always does. It was vacuous.
	poke := f.emitter.byName("goal_poke")[0]
	fields, ok := poke.Payload.(map[string]any)
	if !ok {
		t.Fatalf("the poke payload is %T, not a map", poke.Payload)
	}
	if got := fields["epoch"]; got != 2 {
		t.Errorf("the poke records epoch %v, want 2 — the backoff exponent and the dedup key must be the same number or they drift apart", got)
	}
}

// AC6: AT THE CAP, ONE goal_stuck AND NOTHING FURTHER, EVER.
//
// "Stops consuming tokens" is asserted as ZERO injector calls across many
// subsequent sweeps, not as the presence of a log line. A goal_stuck event that
// coexisted with continued poking would satisfy a log-line assertion perfectly
// while burning tokens forever.
func TestSweeper_AtTheCapItQuarantinesAndStopsForever(t *testing.T) {
	f := newSweepFixture(t)
	f.candidate().Pokes = maxGoalPokes
	f.candidate().LastPokeAt = f.now.Add(-100 * time.Hour)

	res := f.sweep(t)

	if res.Quarantined != 1 {
		t.Errorf("Quarantined = %d, want 1", res.Quarantined)
	}
	stuck := f.emitter.byName("goal_stuck")
	if len(stuck) != 1 {
		t.Fatalf("%d goal_stuck events, want 1", len(stuck))
	}
	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d pokes at the cap, want 0", got)
	}

	// Now the quarantine holds across many further sweeps. The reader reports it
	// as quarantined from here on, which is what a real log does once the
	// goal_stuck event exists.
	f.candidate().Quarantined = true
	for i := 0; i < 20; i++ {
		f.now = f.now.Add(24 * time.Hour)
		f.sweep(t)
	}
	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d pokes across 20 further sweeps of a quarantined goal, want 0 — this is the token burn AC6 exists to stop", got)
	}
	if got := len(f.emitter.byName("goal_stuck")); got != 1 {
		t.Errorf("%d goal_stuck events after 21 sweeps, want 1 — a re-quarantine per sweep is its own kind of noise", got)
	}
}

// POSITIVE CONTROL for the cap: one poke BELOW the cap still pokes.
//
// Without it, a sweeper that quarantined everything immediately would satisfy the
// cap test and would never poke anything at all.
func TestSweeper_JustBelowTheCapItStillPokes(t *testing.T) {
	f := newSweepFixture(t)
	f.candidate().Pokes = maxGoalPokes - 1
	f.candidate().LastPokeAt = f.now.Add(-100 * time.Hour)

	f.sweep(t)
	f.assertPoked(t, "a goal one poke below the cap")
	if got := f.emitter.byName("goal_stuck"); len(got) != 0 {
		t.Errorf("quarantined a goal below the cap: %v", got)
	}
}

// ---------------------------------------------------------------------------
// Poke discipline
// ---------------------------------------------------------------------------

// THE POKE'S EVENT ID IS DERIVED FROM (goal, epoch), so a split-brain second
// sweeper's poke collides in the DATABASE (Appendix B item 3).
//
// This replaced a claim. The claim was taken before the append and never
// released, so an append that FAILED left the epoch unchanged and the claim
// held — and every later sweep computed the same key, lost it to its own corpse,
// and skipped. The goal was never poked again AND never quarantined, reported
// under Skipped where it is indistinguishable from the five legitimate gates.
// Verified with a probe in code review.
func TestSweeper_PokeEventIDIsDerivedFromGoalAndEpoch(t *testing.T) {
	f := newSweepFixture(t)
	f.sweep(t)

	pokes := f.emitter.byName("goal_poke")
	if len(pokes) != 1 {
		t.Fatalf("%d goal_poke events, want 1", len(pokes))
	}
	want := DerivedEventID(kindGoalPoke, f.goalID.String(), "0")
	if pokes[0].EventID != want {
		t.Errorf("goal_poke id = %s, want the derived %s — with a random id two sweepers both poke", pokes[0].EventID, want)
	}
	if claims := f.claims.log(); len(claims) != 0 {
		t.Errorf("the sweeper took %v; holding a claim across the append is the defect this replaced", claims)
	}
}

// THE EPOCH IS IN THE ID, so the NEXT poke for a goal is a different event while
// a concurrent second sweeper's poke for the SAME epoch is the same one.
//
// Positive control for the test above: an id that ignored the epoch would satisfy
// "the id is derived" while making only the first poke for a goal possible, ever.
func TestSweeper_TheDerivedPokeIDAdvancesWithTheEpoch(t *testing.T) {
	seen := map[uuid.UUID]bool{}
	goal := uuid.New()
	for epoch := 0; epoch < 3; epoch++ {
		id := DerivedEventID(kindGoalPoke, goal.String(), fmt.Sprint(epoch))
		if seen[id] {
			t.Errorf("epoch %d derives an id already used, so only the first poke for a goal would ever be recorded", epoch)
		}
		seen[id] = true
	}
	// And a different goal at the same epoch is a different event.
	if DerivedEventID(kindGoalPoke, goal.String(), "0") == DerivedEventID(kindGoalPoke, uuid.NewString(), "0") {
		t.Error("two goals derive the same poke id at epoch 0, so poking one would suppress the other")
	}
}

// A DUPLICATE POKE APPEND IS "ALREADY DONE", NOT A FAILURE.
//
// The split-brain resolution, and the assertion that the refusal is read
// correctly: a second sweeper that treated 23505 as an error would fail its whole
// pass over an event another host had legitimately handled.
func TestSweeper_ADuplicatePokeAppendIsSkippedNotFailed(t *testing.T) {
	f := newSweepFixture(t)
	f.emitter.uniqueViolationOn = map[uuid.UUID]bool{
		DerivedEventID(kindGoalPoke, f.goalID.String(), "0"): true,
	}

	res := f.sweep(t)
	if res.Poked != 0 {
		t.Errorf("Poked = %d, want 0 — another sweeper already poked this epoch", res.Poked)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", res.Skipped)
	}
	if got := f.injector.count(); got != 0 {
		t.Errorf("injected %d pokes for an epoch another sweeper already recorded", got)
	}
}

// A FAILED POKE APPEND LEAVES THE GOAL POKEABLE.
//
// The second HIGH defect from code review, verified there with a probe. Because
// the epoch is derived from the COUNT of goal_poke events, an append that failed
// left the epoch unchanged — and the old code's claim, taken before the append and
// never released, then blocked that exact epoch forever.
func TestSweeper_AFailedPokeAppendLeavesTheGoalPokeable(t *testing.T) {
	f := newSweepFixture(t)
	f.emitter.err = errors.New("connection reset")

	if _, err := Sweep(context.Background(), f.deps); err == nil {
		t.Fatal("Sweep reported success although the poke could not be recorded")
	}
	if got := f.injector.count(); got != 0 {
		t.Fatalf("injected %d pokes with nothing recorded", got)
	}

	// The condition clears. The epoch is unchanged (nothing was written), so the
	// next sweep must poke this goal.
	f.emitter.mu.Lock()
	f.emitter.err = nil
	f.emitter.mu.Unlock()

	f.sweep(t)
	f.assertPoked(t, "the sweep after a failed poke append")
}

// THE POKE IS RECORDED BEFORE IT IS DELIVERED.
//
// Same rule as every other side effect in this diff, and here the reason is the
// backoff: the poke COUNT is the epoch, so a delivery that happened without a
// recorded event would leave the epoch unchanged and the next sweep would poke
// again immediately, at the same backoff, forever.
func TestSweeper_RecordsThePokeBeforeDeliveringIt(t *testing.T) {
	f := newSweepFixture(t)
	tr := &trace{}
	f.emitter.trace = tr
	f.injector.trace = tr

	f.sweep(t)

	got := tr.all()
	want := []string{"emit:goal_poke", "inject:alice"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("trace = %v, want %v — an unrecorded poke leaves the epoch unchanged, so the next sweep pokes again immediately and the backoff never advances", got, want)
	}
}

// A FAILED DELIVERY IS REPORTED and the poke stays recorded.
//
// Recorded on purpose: the poke was attempted, tokens may have been spent, and
// the backoff must advance regardless — otherwise a persistently unreachable
// owner is poked at the base interval forever, which is precisely the runaway
// AC6 exists to prevent.
func TestSweeper_FailedDeliveryIsReportedButStillCountsAsAPoke(t *testing.T) {
	f := newSweepFixture(t)
	f.injector.err = errors.New("recipient is gone")

	if _, err := Sweep(context.Background(), f.deps); err == nil {
		t.Fatal("Sweep reported success although the poke could not be delivered")
	}
	if got := len(f.emitter.byName("goal_poke")); got != 1 {
		t.Errorf("%d goal_poke events after a failed delivery, want 1 — without it the backoff never advances and an unreachable owner is poked forever", got)
	}
}

// ---------------------------------------------------------------------------
// Election
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Robustness
// ---------------------------------------------------------------------------

// An owner this host has never heard of is not poked, and not an error.
//
// A goal owned by an agent on ANOTHER host is normal in a multi-host fleet: this
// host cannot see its AgentState, so it cannot evaluate the InTurn or Paused
// gates, and poking blind would override an operator's pause on a machine it
// cannot observe. The elected sweeper being on the wrong host is a real gap and
// is called out in the file header rather than papered over.
func TestSweeper_AnUnknownOwnerIsSkippedNotPoked(t *testing.T) {
	f := newSweepFixture(t)
	f.local.agents = nil // alice is not on this host

	res := f.sweep(t)
	f.assertNotPoked(t, "an owner this host cannot observe")
	f.assertGate(t, res, "not on this host")
}

// An unreadable local snapshot stops the sweep.
//
// Degrading to "no local agents" would make every gate unevaluable, and the
// gates are the only thing standing between the sweeper and poking an in-turn or
// operator-paused agent.
func TestSweeper_UnreadableLocalStateStopsTheSweep(t *testing.T) {
	f := newSweepFixture(t)
	f.local.err = errors.New("cannot read .sprawl/agents")

	if _, err := Sweep(context.Background(), f.deps); err == nil {
		t.Fatal("Sweep succeeded while blind to local state; the InTurn and Paused gates cannot be evaluated without it")
	}
	f.assertNotPoked(t, "a sweep that could not read local state")
}

// Sweep refuses an incomplete configuration.
func TestSweeper_RefusesAnIncompleteConfiguration(t *testing.T) {
	cases := map[string]func(d *SweeperDeps){
		"no goals":    func(d *SweeperDeps) { d.Goals = nil },
		"no local":    func(d *SweeperDeps) { d.Local = nil },
		"no emitter":  func(d *SweeperDeps) { d.Emitter = nil },
		"no injector": func(d *SweeperDeps) { d.Injector = nil },
		"no host":     func(d *SweeperDeps) { d.Host = "" },
		"no project":  func(d *SweeperDeps) { d.ProjectID = uuid.Nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSweepFixture(t)
			mutate(&f.deps)
			if _, err := Sweep(context.Background(), f.deps); err == nil {
				t.Errorf("Sweep accepted a configuration with %s", name)
			}
		})
	}
}

// A sweep with nothing open reports zeroes.
func TestSweeper_NothingOpenIsANoOp(t *testing.T) {
	f := newSweepFixture(t)
	f.reader.candidates = nil

	res := f.sweep(t)
	if res.Poked != 0 || res.Quarantined != 0 || res.Considered != 0 {
		t.Errorf("Sweep over an empty log reported %+v, want zeroes", res)
	}
}

// The two new seeds carry their intended shapes, including goal_stuck's
// deliberate NON-contract shape.
func TestSweepSeeds_ContractShapes(t *testing.T) {
	reg := testRegistry(t)
	for _, name := range []string{"goal_poke", "goal_stuck"} {
		s, ok := reg.ByName(name, 1)
		if !ok {
			t.Errorf("%s@1 is missing from the seed registry", name)
			continue
		}
		if s.Opens {
			t.Errorf("%s@1 opens a contract; M1b ships no closer for either, so every one would be permanently outstanding work (see QUM-1250 decision 1)", name)
		}
		if s.Closes != "" {
			t.Errorf("%s@1 closes %q, want nothing", name, s.Closes)
		}
		if s.Spillable {
			t.Errorf("%s@1 is spillable; a poke or a quarantine recorded only locally is invisible to the other host's dedup, which double-pokes", name)
		}
	}
}

package supervisor

import (
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/card"
	"github.com/dmotles/sprawl/internal/cardresolve"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRunLedgerSubscriber_NilEmitterStillDrainsTheBus pins the DEFAULT path.
//
// The event-log flag is off by default, so a nil emitter is what almost every
// run gets. The subscriber must still consume from its channel: a subscriber
// that stopped reading would let its buffer fill, and a full buffer is what
// makes the EventBus start dropping — for this subscriber first, but the
// backpressure is a shared-bus concern. "Disabled" must cost nothing and break
// nothing.
func TestRunLedgerSubscriber_NilEmitterStillDrainsTheBus(t *testing.T) {
	bus := runtimepkg.NewEventBus()
	stop := runLedgerSubscriber(bus, nil, "ledger-test")

	// Publish more than the subscriber's buffer so a non-draining subscriber
	// would be visibly stuck rather than merely idle.
	for i := 0; i < 100; i++ {
		bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnStarted})
	}

	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop() did not return within 5s with a nil emitter — the subscriber goroutine is not draining its channel")
	}
}

// TestRunLedgerSubscriber_StopIsSafeToCallConcurrentlyAndRepeatedly pins that a
// double stop neither panics nor deadlocks. Both are reachable: the launcher's
// rollback path calls the stop functions and so does normal teardown.
//
// HONEST LIMIT, because the obvious reading of this test is wrong. It does NOT
// pin the sync.Once in runLedgerSubscriber: EventBus.SubscribeNamed already
// returns an unsub guarded by its own sync.Once (internal/runtime/eventbus.go
// ~348), and a receive from an already-closed doneCh returns immediately, so
// removing the guard here leaves this test GREEN. Measured, not assumed — with
// the guard deleted the test still passes.
//
// The guard is kept anyway, for one stated reason: runUsageSubscriber and
// runActivitySubscriber beside it have exactly this shape, and a subscriber that
// differed from its siblings would read as a deliberate distinction nobody made.
// It is symmetry, not a load-bearing invariant, and it is documented as such so
// nobody later cites this test as evidence for it.
func TestRunLedgerSubscriber_StopIsSafeToCallConcurrentlyAndRepeatedly(t *testing.T) {
	bus := runtimepkg.NewEventBus()
	stop := runLedgerSubscriber(bus, nil, "ledger-test-idem")

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop()
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent stop() calls deadlocked")
	}
}

// TestNewLifecycleEmitter_StoreDisabledYieldsNil pins that a host without the
// store gets no emitter and, crucially, no error path.
//
// The launch sequence must never fail because of the event log. This asserts the
// disabled shape at the seam the launcher actually calls.
func TestNewLifecycleEmitter_StoreDisabledYieldsNil(t *testing.T) {
	root := t.TempDir()
	if em := newLifecycleEmitter(t.Context(), RuntimeStartSpec{SprawlRoot: root, Name: "nobody"}, nil, "sess"); em != nil {
		t.Errorf("expected no emitter with the store disabled, got %#v", em)
	}
}

// TestSpawnContextFor_MapsTheLaunchOntoTheArtifact (QUM-1251, AC6).
//
// The mapping is tested separately from the write because the write cannot be
// reached hermetically: newLifecycleEmitter goes through store.Process, a
// process-wide sync.Once singleton that returns nil with the store off (the
// default). What IS reachable is the field-by-field translation, which is where
// a dropped or transposed field would live — every field here is a string, so a
// swap produces a perfectly well-formed artifact that describes another launch.
func TestSpawnContextFor_MapsTheLaunchOntoTheArtifact(t *testing.T) {
	// Every string is DISTINCT, including the two fields with more than one
	// candidate source: the spec's name vs the state's, and the state's type vs
	// the card's. A fixture that reused one value could not see the transposition
	// this test exists for.
	prep := &preparedLaunch{
		agentState: &state.AgentState{
			Name: "STATE-NAME", Type: "STATE-TYPE", Branch: "STATE-BRANCH",
		},
		sessionSpec:  backend.SessionSpec{Model: "SPEC-MODEL", Effort: "SPEC-EFFORT"},
		systemPrompt: "RENDERED-PROMPT",
		card: &card.Card{
			Name: "CARD-NAME", Version: 7, AgentType: "CARD-TYPE",
			ContentSHA256: "CARD-SHA",
		},
		cardSource: cardresolve.SourceDB,
	}

	got := spawnContextFor(RuntimeStartSpec{Name: "SPEC-NAME"}, prep, "sess-9")

	for _, f := range []struct{ field, got, want string }{
		// The SPEC's name is authoritative: it is what the launcher was asked to
		// start and what every other lifecycle field is keyed on.
		{"AgentName", got.AgentName, "SPEC-NAME"},
		// The STATE's type is authoritative, not the card's: the card is chosen
		// FROM the type, so recording the card's would make a fallback-seed
		// launch claim it ran the type it fell back to.
		{"AgentType", got.AgentType, "STATE-TYPE"},
		{"Branch", got.Branch, "STATE-BRANCH"},
		{"SessionID", got.SessionID, "sess-9"},
		{"Model", got.Model, "SPEC-MODEL"},
		{"Effort", got.Effort, "SPEC-EFFORT"},
		{"CardName", got.CardName, "CARD-NAME"},
		{"CardContentSHA256", got.CardContentSHA256, "CARD-SHA"},
		{"CardSource", got.CardSource, string(cardresolve.SourceDB)},
		{"RenderedPrompt", got.RenderedPrompt, "RENDERED-PROMPT"},
	} {
		if f.got != f.want {
			t.Errorf("%s = %q, want %q", f.field, f.got, f.want)
		}
	}
	if got.CardVersion != 7 {
		t.Errorf("CardVersion = %d, want 7", got.CardVersion)
	}
}

// TestSpawnContextFor_NoCardRecordsNoCardRatherThanAZeroOne is the control for
// the mapping above and the case a host with the store off but reachable hits
// whenever the card chain came up empty: prepareLaunch returns a nil card there,
// so an unguarded mapping panics on the launch path — the one place the store is
// forbidden from breaking.
func TestSpawnContextFor_NoCardRecordsNoCardRatherThanAZeroOne(t *testing.T) {
	prep := &preparedLaunch{
		agentState:   &state.AgentState{Name: "eng", Type: "engineer", Branch: "feat/x"},
		sessionSpec:  backend.SessionSpec{Model: "haiku", Effort: "low"},
		systemPrompt: "RENDERED-PROMPT",
		// A source WITH no card: not a state resolveCard produces, but the one
		// fixture that can tell "the source is reported only alongside the card
		// it describes" from "the source happens to be empty too".
		cardSource: cardresolve.SourceSeed,
	}
	got := spawnContextFor(RuntimeStartSpec{Name: "eng"}, prep, "sess-9")

	if got.CardSource != "" {
		t.Errorf("CardSource = %q with no card resolved — a hardcoded source is exactly the \"cannot say whether the database answered\" failure carrying it is meant to prevent", got.CardSource)
	}
	if got.CardName != "" || got.CardVersion != 0 || got.CardContentSHA256 != "" {
		t.Errorf("a launch with no card recorded card %s@%d (sha %q) — a definition that does not exist",
			got.CardName, got.CardVersion, got.CardContentSHA256)
	}
	// The prompt is still recorded: it is the one thing a replay cannot
	// reconstruct, and the run happened whether or not a card can be named.
	if got.RenderedPrompt != "RENDERED-PROMPT" {
		t.Errorf("RenderedPrompt = %q, want it recorded even with no card", got.RenderedPrompt)
	}
}

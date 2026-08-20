package supervisor

import (
	"sync"
	"testing"
	"time"

	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
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
	if em := newLifecycleEmitter(t.Context(), RuntimeStartSpec{SprawlRoot: root, Name: "nobody"}, "sess"); em != nil {
		t.Errorf("expected no emitter with the store disabled, got %#v", em)
	}
}

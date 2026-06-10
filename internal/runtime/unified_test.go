package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	livenesspkg "github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// Liveness-state literals used throughout these tests, mapping the legacy
// RuntimeState enum onto its liveness.State equivalent (QUM-626 M5 fold):
//
//	liveIdle         -> Running (non-autonomous)
//	liveTurnActive   -> Running·autonomous-turn
//	liveInterrupting -> Stopping
//	liveStopped      -> Stopped
var (
	liveIdle         = livenesspkg.State{Liveness: livenesspkg.Running}
	liveTurnActive   = livenesspkg.State{Liveness: livenesspkg.Running, InTurn: true}
	liveInterrupting = livenesspkg.State{Liveness: livenesspkg.Stopping}
	liveStopped      = livenesspkg.State{Liveness: livenesspkg.Stopped}
)

// mockUnifiedSession is a SessionHandle test double scoped to the unified
// runtime tests. It is independent of mockSession in turnloop_test.go so
// these tests can evolve separately.
type mockUnifiedSession struct {
	mu             sync.Mutex
	starts         []string
	onStart        func(call int) (<-chan *protocol.Message, error)
	interruptCalls int32
}

func (m *mockUnifiedSession) StartTurn(_ context.Context, prompt string, _ ...backend.TurnSpec) (<-chan *protocol.Message, error) {
	m.mu.Lock()
	m.starts = append(m.starts, prompt)
	call := len(m.starts) - 1
	cb := m.onStart
	m.mu.Unlock()
	if cb != nil {
		return cb(call)
	}
	ch := make(chan *protocol.Message)
	close(ch)
	return ch, nil
}

func (m *mockUnifiedSession) Interrupt(_ context.Context) error {
	atomic.AddInt32(&m.interruptCalls, 1)
	return nil
}

func (m *mockUnifiedSession) interruptCount() int {
	return int(atomic.LoadInt32(&m.interruptCalls))
}

func (m *mockUnifiedSession) startCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.starts)
}

// mockUnifiedSessionWithID adds a SessionID() method so we can test the
// unexported sessionIDProvider type-assertion path.
type mockUnifiedSessionWithID struct {
	mockUnifiedSession
	id string
}

func (m *mockUnifiedSessionWithID) SessionID() string { return m.id }

// makeResultMsg returns a minimal terminal result protocol.Message.
func makeResultMsg() *protocol.Message {
	return &protocol.Message{Type: "result", Subtype: "success"}
}

// waitForState polls rt.State() up to d, returning true on match.
func waitForState(t *testing.T, rt *UnifiedRuntime, want livenesspkg.State, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if rt.State() == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestNew_Defaults(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{
		Name:    "agent-1",
		Session: mock,
	})

	if rt.Queue() == nil {
		t.Errorf("Queue() = nil, want non-nil")
	}
	if rt.EventBus() == nil {
		t.Errorf("EventBus() = nil, want non-nil")
	}
	if got := rt.State(); got != liveIdle {
		t.Errorf("State() = %v, want liveIdle", got)
	}
	if got := rt.Name(); got != "agent-1" {
		t.Errorf("Name() = %q, want %q", got, "agent-1")
	}
}

func TestStart_Stop_Lifecycle(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- rt.Stop(stopCtx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s")
	}

	if got := rt.State(); got != liveStopped {
		t.Errorf("final State() = %v, want liveStopped", got)
	}
}

func TestStart_Twice_Errors(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	if err := rt.Start(context.Background()); err == nil {
		t.Errorf("second Start returned nil error, want non-nil")
	}
}

func TestStop_WithoutStart_NoOp(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Stop(context.Background()); err != nil {
		t.Errorf("Stop without Start: %v, want nil", err)
	}
}

func TestStateTransitions_NormalTurn(t *testing.T) {
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 1)
			ch <- makeResultMsg()
			close(ch)
			return ch, nil
		},
	}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "go"})

	// Wait for StartTurn to be invoked before asserting state, since State() no
	// longer peeks at the queue and would otherwise read Idle before the loop
	// has a chance to consume the queued item.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && mock.startCount() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := mock.startCount(); got < 1 {
		t.Errorf("StartTurn calls = %d, want >= 1", got)
	}
	// We may miss the brief TurnActive window, so only require eventual return to Idle.
	if !waitForState(t, rt, liveIdle, 2*time.Second) {
		t.Fatalf("did not return to liveIdle; current=%v", rt.State())
	}
}

func TestStateTransitions_InitialPrompt(t *testing.T) {
	released := make(chan struct{})
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 1)
			go func() {
				<-released
				ch <- makeResultMsg()
				close(ch)
			}()
			return ch, nil
		},
	}
	rt := New(RuntimeConfig{
		Name:          "x",
		Session:       mock,
		InitialPrompt: "boot",
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		select {
		case <-released:
		default:
			close(released)
		}
		_ = rt.Stop(context.Background())
	}()

	if !waitForState(t, rt, liveTurnActive, 2*time.Second) {
		t.Fatalf("did not enter liveTurnActive after InitialPrompt; current=%v", rt.State())
	}

	close(released)

	if !waitForState(t, rt, liveIdle, 2*time.Second) {
		t.Fatalf("did not return to liveIdle; current=%v", rt.State())
	}
}

// TestInterrupt_WhenIdle_ForwardsToSession pins QUM-435 Option A: even when no
// turn is active, UnifiedRuntime.Interrupt must unconditionally forward to the
// backend session. State must NOT be mutated (no turn is in flight to abort).
func TestInterrupt_WhenIdle_ForwardsToSession(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	// Make sure runtime is settled into Idle.
	if !waitForState(t, rt, liveIdle, 1*time.Second) {
		t.Fatalf("not idle before Interrupt; state=%v", rt.State())
	}

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Errorf("Interrupt while idle returned error: %v", err)
	}

	if got := mock.interruptCount(); got != 1 {
		t.Errorf("Session.Interrupt called %d times while idle, want 1 (QUM-435 Option A: always forward)", got)
	}
	if got := rt.State(); got != liveIdle {
		t.Errorf("State after idle Interrupt = %v, want liveIdle (no turn running, no state mutation)", got)
	}
}

func TestInterrupt_WhenActive(t *testing.T) {
	turnCh := make(chan *protocol.Message, 4)
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return turnCh, nil
		},
	}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		// Best-effort drain so Stop can complete.
		select {
		case <-turnCh:
		default:
		}
		_ = rt.Stop(context.Background())
	}()

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "long"})

	if !waitForState(t, rt, liveTurnActive, 2*time.Second) {
		t.Fatalf("did not enter liveTurnActive; current=%v", rt.State())
	}

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// State should reflect Interrupting promptly.
	if !waitForState(t, rt, liveInterrupting, 1*time.Second) {
		t.Errorf("did not enter liveInterrupting; current=%v", rt.State())
	}

	// Wait for Session.Interrupt to be observed.
	// QUM-435 Option A: under the new contract, UnifiedRuntime.Interrupt forwards
	// directly to session.Interrupt AND the loop also issues its per-turn
	// session.Interrupt while draining, so the count may be >= 1.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && mock.interruptCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := mock.interruptCount(); got < 1 {
		t.Errorf("Session.Interrupt count = %d, want >= 1", got)
	}

	// Release the turn so the loop returns to Idle.
	turnCh <- makeResultMsg()
	close(turnCh)

	if !waitForState(t, rt, liveIdle, 2*time.Second) {
		t.Errorf("did not return to liveIdle after interrupt drain; current=%v", rt.State())
	}
}

func TestWakeForDelivery_FiresQueueSignal(t *testing.T) {
	// Direct test against a NON-running runtime so we can read Signal() without
	// competing with the Run goroutine.
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.WakeForDelivery(context.Background()); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	select {
	case <-rt.Queue().Signal():
		// good
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Queue.Signal did not fire after WakeForDelivery")
	}
}

func TestWakeForDelivery_RepeatedSafe(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			_ = rt.WakeForDelivery(context.Background())
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("repeated WakeForDelivery blocked")
	}

	if got := rt.Queue().Len(); got != 0 {
		t.Errorf("Queue.Len after WakeForDelivery loop = %d, want 0", got)
	}
}

func TestQueueAndEventBus_StableIdentity(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	q1 := rt.Queue()
	q2 := rt.Queue()
	if q1 != q2 {
		t.Errorf("Queue() returned different pointers across calls: %p vs %p", q1, q2)
	}

	b1 := rt.EventBus()
	b2 := rt.EventBus()
	if b1 != b2 {
		t.Errorf("EventBus() returned different pointers across calls: %p vs %p", b1, b2)
	}
}

func TestSessionID_FromProvider(t *testing.T) {
	mock := &mockUnifiedSessionWithID{id: "abc-123"}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if got := rt.SessionID(); got != "abc-123" {
		t.Errorf("SessionID() = %q, want %q", got, "abc-123")
	}
}

func TestSessionID_WithoutProvider(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if got := rt.SessionID(); got != "" {
		t.Errorf("SessionID() = %q, want \"\"", got)
	}
}

func TestConcurrentStateReads(t *testing.T) {
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 1)
			ch <- makeResultMsg()
			close(ch)
			return ch, nil
		},
	}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	// Drive turns continuously while readers spin.
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "go"})
				time.Sleep(time.Millisecond)
			}
		}
	}()

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = rt.State()
				}
			}
		}()
	}

	time.Sleep(200 * time.Millisecond)
	close(stop)

	doneAll := make(chan struct{})
	go func() { wg.Wait(); close(doneAll) }()

	select {
	case <-doneAll:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent goroutines did not return")
	}
}

// TestRuntimeConfig_CapabilitiesPlumbed pins the QUM-398 wiring: callers must
// be able to plumb backend.Capabilities into the runtime config and recover
// them from the constructed UnifiedRuntime via Capabilities(). The supervisor
// uses this to forward capabilities into the RuntimeHandle for the registry
// snapshot.
func TestRuntimeConfig_CapabilitiesPlumbed(t *testing.T) {
	mock := &mockUnifiedSession{}
	caps := backend.Capabilities{
		SupportsInterrupt:  true,
		SupportsResume:     true,
		SupportsToolBridge: true,
	}
	rt := New(RuntimeConfig{
		Name:         "x",
		Session:      mock,
		Capabilities: caps,
	})

	got := rt.Capabilities()
	if got != caps {
		t.Fatalf("Capabilities() = %+v, want %+v", got, caps)
	}
}

func TestStop_BlocksUntilLoopExits(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	bus := rt.EventBus()
	sub, unsub := bus.Subscribe(64)
	defer unsub()

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop returns, EventStopped should already be observable.
	sawStopped := false
	deadline := time.After(1 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				break loop
			}
			if ev.Type == EventStopped {
				sawStopped = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	if !sawStopped {
		t.Errorf("did not observe EventStopped after Stop returned")
	}

	if got := rt.State(); got != liveStopped {
		t.Errorf("State after Stop = %v, want liveStopped", got)
	}
}

// TestDone_ClosedAfterStop pins QUM-434: rt.Done() must return a channel that
// closes once the turn loop goroutine has exited following Stop().
func TestDone_ClosedAfterStop(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := rt.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-rt.Done():
		// good
	case <-time.After(1 * time.Second):
		t.Fatal("Done() did not close within 1s after Stop returned")
	}
}

// TestDone_ClosedWithoutStart pins QUM-434: Stop()'s early-return branch (when
// Start was never called) must still close Done() so callers can rely on it.
func TestDone_ClosedWithoutStart(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop without Start: %v", err)
	}

	select {
	case <-rt.Done():
		// good
	case <-time.After(1 * time.Second):
		t.Fatal("Done() did not close within 1s after Stop on never-started runtime")
	}
}

// TestEnqueue_TurnStartedObservableViaEventBus pins QUM-413: callers wanting to
// see a turn start after Enqueue should subscribe to EventBus EventTurnStarted
// rather than poll State(). State() must not peek into the queue.
func TestEnqueue_TurnStartedObservableViaEventBus(t *testing.T) {
	released := make(chan struct{})
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 1)
			go func() {
				<-released
				ch <- makeResultMsg()
				close(ch)
			}()
			return ch, nil
		},
	}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	// Subscribe BEFORE Start so we capture the EventTurnStarted event.
	bus := rt.EventBus()
	sub, unsub := bus.Subscribe(64)
	defer unsub()

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		select {
		case <-released:
		default:
			close(released)
		}
		_ = rt.Stop(context.Background())
	}()

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "go"})

	// Use waitFor (turnloop_test.go, same package) to observe EventTurnStarted.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})

	// Post-event the wrapper has flipped state to TurnActive.
	if got := rt.State(); got != liveTurnActive {
		t.Errorf("State() after EventTurnStarted = %v, want liveTurnActive", got)
	}

	// Release the turn and observe completion.
	close(released)

	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnCompleted
	})

	if !waitForState(t, rt, liveIdle, 2*time.Second) {
		t.Errorf("did not return to liveIdle; current=%v", rt.State())
	}
}

// TestState_DoesNotPeekAtQueue pins QUM-413 directly: State() returns the
// stored runtime state and must NOT synthesize liveTurnActive based on queue
// length. Items enqueued before the loop has dequeued them must not affect the
// state read.
func TestState_DoesNotPeekAtQueue(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	// Do NOT Start. Enqueue directly and observe State().
	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "go"})

	if got := rt.State(); got != liveIdle {
		t.Errorf("State() with queued item but no running loop = %v, want liveIdle (state must not peek at queue)", got)
	}
}

// TestInterrupt_WhenStopped_NoOp pins the safety guard: after Stop, Interrupt
// must be a no-op and must not call session.Interrupt.
func TestInterrupt_WhenStopped_NoOp(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Snapshot count after Stop (Stop itself should not have called Interrupt
	// in this scenario; record the baseline so the test is robust).
	before := mock.interruptCount()

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Errorf("Interrupt after Stop returned error: %v", err)
	}

	if got := mock.interruptCount(); got != before {
		t.Errorf("Session.Interrupt count = %d, want %d (no-op when stopped)", got, before)
	}
}

// TestStop_DuringActiveTurn_CallsSessionInterrupt pins QUM-414: when Stop is
// called while a turn is in flight, the runtime must forward Session.Interrupt
// to the backend as a clean shutdown signal (independent of the ctx-cancel
// path that closes the StartTurn channel).
func TestStop_DuringActiveTurn_CallsSessionInterrupt(t *testing.T) {
	released := make(chan struct{})
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 1)
			go func() {
				<-released
				ch <- makeResultMsg()
				close(ch)
			}()
			return ch, nil
		},
	}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "long"})

	if !waitForState(t, rt, liveTurnActive, 2*time.Second) {
		t.Fatalf("did not enter liveTurnActive; current=%v", rt.State())
	}

	// Release the inner channel concurrently so Stop can drain.
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(released)
	}()

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rt.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := mock.interruptCount(); got < 1 {
		t.Errorf("Session.Interrupt count after Stop-during-active-turn = %d, want >= 1", got)
	}
	if got := rt.State(); got != liveStopped {
		t.Errorf("State after Stop = %v, want liveStopped", got)
	}
}

// TestStop_DuringActiveTurn_PublishesStoppedNotInterrupted pins QUM-414: the
// terminal lifecycle event for Stop is EventStopped — Stop must NOT also
// publish EventInterrupted. EventInterrupted is reserved for user-initiated
// Interrupt drains; Stop is a lifecycle shutdown.
func TestStop_DuringActiveTurn_PublishesStoppedNotInterrupted(t *testing.T) {
	released := make(chan struct{})
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 1)
			go func() {
				<-released
				ch <- makeResultMsg()
				close(ch)
			}()
			return ch, nil
		},
	}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	bus := rt.EventBus()
	sub, unsub := bus.Subscribe(64)
	defer unsub()

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "long"})

	if !waitForState(t, rt, liveTurnActive, 2*time.Second) {
		t.Fatalf("did not enter liveTurnActive; current=%v", rt.State())
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		close(released)
	}()

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rt.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Drain published events with a short deadline; capture the lifecycle types.
	sawStopped := false
	sawInterrupted := false
	deadline := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				break loop
			}
			switch ev.Type {
			case EventStopped:
				sawStopped = true
			case EventInterrupted:
				sawInterrupted = true
			}
		case <-deadline:
			break loop
		}
	}
	if !sawStopped {
		t.Errorf("did not observe EventStopped after Stop")
	}
	if sawInterrupted {
		t.Errorf("observed EventInterrupted after Stop; Stop must not publish it (use Interrupt for that)")
	}
}

// TestStop_Idle_CallsSessionInterruptOnce pins QUM-414: Stop forwards
// Session.Interrupt as a clean shutdown signal even when no turn is active.
// Backends are contracted to be idempotent and to no-op when nothing is in
// flight, so this is safe and gives the backend a uniform shutdown hook.
func TestStop_Idle_CallsSessionInterruptOnce(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !waitForState(t, rt, liveIdle, 1*time.Second) {
		t.Fatalf("not idle before Stop; state=%v", rt.State())
	}

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := mock.interruptCount(); got != 1 {
		t.Errorf("Session.Interrupt count after Stop on idle runtime = %d, want 1", got)
	}
}

// TestStop_Idempotent_NoExtraInterrupt pins QUM-414: a second Stop call must
// be a no-op and must not re-issue Session.Interrupt.
func TestStop_Idempotent_NoExtraInterrupt(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	first := mock.interruptCount()

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	second := mock.interruptCount()

	if first != 1 {
		t.Errorf("Session.Interrupt count after first Stop = %d, want 1", first)
	}
	if second != first {
		t.Errorf("Session.Interrupt count grew on second Stop: first=%d second=%d, want equal (idempotent)", first, second)
	}
}

// TestStop_WithoutStart_NoSessionInterrupt pins QUM-414: Stop on a never-
// started runtime must not invoke Session.Interrupt — there is no live session
// loop to wind down.
func TestStop_WithoutStart_NoSessionInterrupt(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop without Start: %v", err)
	}

	if got := mock.interruptCount(); got != 0 {
		t.Errorf("Session.Interrupt count after Stop on never-started runtime = %d, want 0", got)
	}
}

// TestDone_ClosesAfterLoopExit pins QUM-434: Done() must reflect the loop
// goroutine's actual completion, not be pre-closed at New() or before the
// loop has exited. While the runtime is running, Done() must NOT be closed.
func TestDone_ClosesAfterLoopExit(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Done() must not be closed before Stop has happened.
	select {
	case <-rt.Done():
		t.Fatal("Done() was already closed before Stop was called")
	default:
		// good — loop goroutine is still running.
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := rt.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-rt.Done():
		// good
	case <-time.After(1 * time.Second):
		t.Fatal("Done() did not close within 1s after loop exit")
	}
}

// TestWakeForDelivery_DoesNotArmPendingInterrupt_WhenIdle pins QUM-462:
// WakeForDelivery is the cooperative wake path used by the supervisor when a
// peer agent has enqueued an inbox item (e.g. WeaveRuntimeHandle.WakeForDelivery
// after sibling/child send_message). It must wake the loop so the queued item
// gets drained, but it must NOT arm `pendingInterrupt` against an idle
// runtime — doing so causes the wrapper to immediately interrupt the very
// turn that would deliver the inbox, so the user sees a banner but Claude
// never actually processes the message.
//
// Expected behaviour: enqueue a ClassInbox item, call WakeForDelivery
// while the runtime is idle, and observe a normal EventTurnCompleted (not
// EventInterrupted) on the EventBus.
//
// Post-QUM-550 slice 4 the underlying invariant is structurally trivial —
// WakeForDelivery never calls Session.Interrupt — but the assertion is kept
// as a sanity check.
func TestWakeForDelivery_DoesNotArmPendingInterrupt_WhenIdle(t *testing.T) {
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 1)
			ch <- makeResultMsg()
			close(ch)
			return ch, nil
		},
	}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	bus := rt.EventBus()
	sub, unsub := bus.Subscribe(64)
	defer unsub()

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	// Wait until the runtime is observably idle (loop blocked on Queue.Signal).
	if !waitForState(t, rt, liveIdle, 1*time.Second) {
		t.Fatalf("runtime did not reach liveIdle before WakeForDelivery; state=%v", rt.State())
	}

	// Mirror WeaveRuntimeHandle.WakeForDelivery: enqueue a ClassInbox item
	// (as if a sibling agent had send_message'd weave) then wake the loop via
	// WakeForDelivery.
	rt.Queue().Enqueue(QueueItem{Class: ClassInbox, Prompt: "[inbox] hello from sibling"})
	if err := rt.WakeForDelivery(context.Background()); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	// The terminal event for this turn must be EventTurnCompleted. If the
	// bug is present (pendingInterrupt armed against an idle runtime), the
	// wrapper's StartTurn consumes the flag and immediately calls
	// loop.Interrupt, so the terminal event is EventInterrupted instead.
	ev, seen := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnCompleted || ev.Type == EventInterrupted
	})
	if ev.Type != EventTurnCompleted {
		t.Fatalf("terminal event = %v, want EventTurnCompleted (QUM-462: WakeForDelivery must not arm pendingInterrupt against an idle runtime); seen=%v", ev.Type, seen)
	}
}

// --- QUM-626 (M5) characterization gate -------------------------------------
//
// The three tests below pin the *observable* Interrupt/Stop/State contract that
// the M5 refactor (QUM-626) must preserve. M5 will replace the private
// `state RuntimeState` field on UnifiedRuntime with a `liveness.State` field and
// delete the RuntimeState enum, while keeping StartTurn/Interrupt/Stop semantics
// identical. The real interrupt gate is the unexported `turnRunning` bool, NOT
// `state`: the `state` TurnActive→Interrupting flip is bookkeeping only.
//
// These tests are written against the CURRENT API (RuntimeState enum, State()
// returns RuntimeState) so they COMPILE and PASS today — this is a safety net,
// not a red test. After the M5 fold the `State() == StateX` assertions will be
// migrated to `State().Liveness == <mapped>`; they must still pass, proving the
// refactor preserved behavior.
//
// Observability note: the harness's mockUnifiedSession exposes only
// interruptCount() (a count of Session.Interrupt calls). There is no direct
// counter for TurnLoop.Interrupt, but the loop path is observable indirectly:
// UnifiedRuntime.Interrupt only calls loop.Interrupt when turnRunning is true,
// and the loop responds on its next tick by issuing its own Session.Interrupt.
// So the loop gate manifests as the Session.Interrupt count plus the
// liveTurnActive→liveInterrupting transition.

// TestUnifiedRuntime_InterruptStopGuard_Stopped_Characterization pins guard #1:
// Interrupt on a STOPPED runtime is a pure no-op — it returns nil, does NOT
// re-invoke Session.Interrupt, and does not panic. State() observes
// liveStopped both before and after the call. M5 must preserve this early
// `state == liveStopped` return (mapped to the liveness Stopped state).
func TestUnifiedRuntime_InterruptStopGuard_Stopped_Characterization(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := rt.State(); got != liveStopped {
		t.Fatalf("State() after Stop = %v, want liveStopped", got)
	}

	// Baseline: Stop on an idle runtime issues exactly one polite
	// Session.Interrupt (QUM-414). Snapshot it so the guard assertion is
	// robust to that lifecycle call.
	before := mock.interruptCount()

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Errorf("Interrupt after Stop = %v, want nil (no-op when stopped)", err)
	}

	if got := mock.interruptCount(); got != before {
		t.Errorf("Session.Interrupt count after stopped-Interrupt = %d, want %d (no-op when stopped)", got, before)
	}
	if got := rt.State(); got != liveStopped {
		t.Errorf("State() after stopped-Interrupt = %v, want liveStopped (unchanged)", got)
	}
}

// TestUnifiedRuntime_InterruptStopGuard_Idle_Characterization pins guard #2:
// Interrupt on an IDLE runtime (turnRunning == false) forwards to
// Session.Interrupt exactly once but does NOT drive loop.Interrupt (the
// turnRunning gate is closed). State stays liveIdle — the TurnActive→
// Interrupting bookkeeping flip only happens when state == liveTurnActive.
//
// The "no loop.Interrupt" claim is asserted indirectly: if loop.Interrupt had
// fired, the loop's next tick would issue a SECOND Session.Interrupt, pushing
// the count above 1. We pin count == 1 to encode that exactly the direct
// forward — and nothing from the loop — happened.
func TestUnifiedRuntime_InterruptStopGuard_Idle_Characterization(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	// Settle into Idle (loop blocked on Queue.Signal, no turn running).
	if !waitForState(t, rt, liveIdle, 1*time.Second) {
		t.Fatalf("not idle before Interrupt; state=%v", rt.State())
	}

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Errorf("Interrupt while idle = %v, want nil", err)
	}

	if got := mock.interruptCount(); got != 1 {
		t.Errorf("Session.Interrupt count after idle Interrupt = %d, want 1 (direct forward only; loop.Interrupt gated off by turnRunning==false)", got)
	}
	if got := rt.State(); got != liveIdle {
		t.Errorf("State() after idle Interrupt = %v, want liveIdle (no turn running, no state mutation)", got)
	}
}

// TestUnifiedRuntime_InterruptStopGuard_TurnActive_Characterization pins
// guard #3: when a turn is genuinely in flight (turnRunning == true), Interrupt
// flips State() liveTurnActive→liveInterrupting AND drives the loop. The loop
// path is observed via the Session.Interrupt count climbing to >= 2: one from
// the direct forward in UnifiedRuntime.Interrupt, one from the loop's tick after
// loop.Interrupt. After the turn drains, State() returns to liveIdle.
//
// The turn is held deterministically active with the released-channel idiom
// (same as TestInterrupt_WhenActive / TestStateTransitions_InitialPrompt): the
// onStart callback returns a channel whose terminal frame is only sent once the
// test closes `released`, so the runtime sits in liveTurnActive with no flaky
// sleep.
func TestUnifiedRuntime_InterruptStopGuard_TurnActive_Characterization(t *testing.T) {
	released := make(chan struct{})
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 1)
			go func() {
				<-released
				ch <- makeResultMsg()
				close(ch)
			}()
			return ch, nil
		},
	}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		// Ensure the held turn is released so Stop can drain.
		select {
		case <-released:
		default:
			close(released)
		}
		_ = rt.Stop(context.Background())
	}()

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "long"})

	if !waitForState(t, rt, liveTurnActive, 2*time.Second) {
		t.Fatalf("did not enter liveTurnActive; current=%v", rt.State())
	}

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt during active turn: %v", err)
	}

	// State must flip to Interrupting promptly (the TurnActive→Interrupting
	// bookkeeping). M5 maps liveInterrupting to its liveness equivalent.
	if !waitForState(t, rt, liveInterrupting, 1*time.Second) {
		t.Errorf("did not enter liveInterrupting; current=%v", rt.State())
	}

	// loop.Interrupt was driven (turnRunning==true), so the loop issues its own
	// Session.Interrupt in addition to the direct forward: count climbs to >= 2.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && mock.interruptCount() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := mock.interruptCount(); got < 2 {
		t.Errorf("Session.Interrupt count during active-turn Interrupt = %d, want >= 2 (direct forward + loop.Interrupt tick; loop gated on by turnRunning==true)", got)
	}

	// Release the turn so the loop drains back to Idle.
	close(released)

	if !waitForState(t, rt, liveIdle, 2*time.Second) {
		t.Errorf("did not return to liveIdle after interrupt drain; current=%v", rt.State())
	}
}

// TestInterrupt_WhenIdle_PublishesSyntheticEventInterrupted is the QUM-775
// item 4 regression test: when Interrupt fires against an idle runtime
// (turnRunning == false), the runtime must publish a synthetic
// EventInterrupted on the bus so the TUI can finalize its turnState. Before
// this fix, the TUI got no event when its turnState was wedged in
// TurnStreaming after a dropped EventTurnCompleted, leaving "Interrupting…"
// forever.
func TestInterrupt_WhenIdle_PublishesSyntheticEventInterrupted(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	// Subscribe BEFORE Interrupt so we observe the synthetic event.
	evCh, unsub := rt.EventBus().SubscribeNamed("probe", 8)
	defer unsub()

	if !waitForState(t, rt, liveIdle, 1*time.Second) {
		t.Fatalf("not idle before Interrupt; state=%v", rt.State())
	}

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt while idle: %v", err)
	}

	// We should receive a synthetic EventInterrupted within a short window.
	deadline := time.After(1 * time.Second)
	var saw bool
loop:
	for {
		select {
		case ev := <-evCh:
			if ev.Type == EventInterrupted {
				saw = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	if !saw {
		t.Fatalf("idle Interrupt did not publish synthetic EventInterrupted (TUI would stay wedged in TurnStreaming)")
	}
}

// TestInterrupt_WhenStopped_NoSyntheticEvent ensures the synthetic publish
// is gated off when the runtime is already Stopped — no event should appear
// on the bus.
func TestInterrupt_WhenStopped_NoSyntheticEvent(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	evCh, unsub := rt.EventBus().SubscribeNamed("probe", 8)
	defer unsub()

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Errorf("Interrupt after Stop: %v", err)
	}

	select {
	case ev := <-evCh:
		t.Errorf("unexpected event after stopped Interrupt: %+v", ev)
	case <-time.After(150 * time.Millisecond):
		// good
	}
}

// TestInterrupt_WhenTurnActive_NoSyntheticDoubleEmit confirms that when a
// turn is genuinely in flight, the idle-branch synthetic publish does NOT
// fire — the TurnLoop's own EventInterrupted emission is the single source
// of truth for the active-turn path.
func TestInterrupt_WhenTurnActive_NoSyntheticDoubleEmit(t *testing.T) {
	released := make(chan struct{})
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 1)
			go func() {
				<-released
				ch <- makeResultMsg()
				close(ch)
			}()
			return ch, nil
		},
	}
	rt := New(RuntimeConfig{Name: "x", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		select {
		case <-released:
		default:
			close(released)
		}
		_ = rt.Stop(context.Background())
	}()

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "long"})
	if !waitForState(t, rt, liveTurnActive, 2*time.Second) {
		t.Fatalf("did not enter liveTurnActive; current=%v", rt.State())
	}

	evCh, unsub := rt.EventBus().SubscribeNamed("probe", 16)
	defer unsub()

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt during active turn: %v", err)
	}

	// Count EventInterrupted within a short window AFTER subscribing, BEFORE
	// the loop tears down. The TurnLoop fires exactly one EventInterrupted
	// when it drains; the synthetic idle branch must NOT fire here.
	close(released)
	deadline := time.After(2 * time.Second)
	var interruptedCount int
collect:
	for {
		select {
		case ev := <-evCh:
			if ev.Type == EventInterrupted {
				interruptedCount++
			}
		case <-deadline:
			break collect
		}
		if interruptedCount > 1 {
			break collect
		}
	}
	if interruptedCount > 1 {
		t.Errorf("got %d EventInterrupted; want at most 1 (no double-emit on active-turn path)", interruptedCount)
	}
}

// CHANGELOG (QUM-550 slice 4): the QUM-510 regression test
// `TestInterruptDelivery_DoesNotArmPendingInterrupt_OnTurnEndBoundary` was
// removed alongside the legacy `InterruptDelivery` / `interruptForDelivery`
// runtime methods it exercised. That race lived inside the conditional
// `if turnRunning` gate of `interruptForDelivery`. With the helper gone,
// the race can't exist: `WakeForDelivery` never calls Session.Interrupt,
// and `ForceInterruptForDelivery` always does — neither makes a
// turn-running decision. The `TestWakeForDelivery_*` and
// `TestForceInterruptForDelivery_*` tests (slice 1) pin the modern
// behaviours.

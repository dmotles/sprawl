package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
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
func waitForState(t *testing.T, rt *UnifiedRuntime, want RuntimeState, d time.Duration) bool {
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
	if got := rt.State(); got != StateIdle {
		t.Errorf("State() = %v, want StateIdle", got)
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

	if got := rt.State(); got != StateStopped {
		t.Errorf("final State() = %v, want StateStopped", got)
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
	if !waitForState(t, rt, StateIdle, 2*time.Second) {
		t.Fatalf("did not return to StateIdle; current=%v", rt.State())
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

	if !waitForState(t, rt, StateTurnActive, 2*time.Second) {
		t.Fatalf("did not enter StateTurnActive after InitialPrompt; current=%v", rt.State())
	}

	close(released)

	if !waitForState(t, rt, StateIdle, 2*time.Second) {
		t.Fatalf("did not return to StateIdle; current=%v", rt.State())
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
	if !waitForState(t, rt, StateIdle, 1*time.Second) {
		t.Fatalf("not idle before Interrupt; state=%v", rt.State())
	}

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Errorf("Interrupt while idle returned error: %v", err)
	}

	if got := mock.interruptCount(); got != 1 {
		t.Errorf("Session.Interrupt called %d times while idle, want 1 (QUM-435 Option A: always forward)", got)
	}
	if got := rt.State(); got != StateIdle {
		t.Errorf("State after idle Interrupt = %v, want StateIdle (no turn running, no state mutation)", got)
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

	if !waitForState(t, rt, StateTurnActive, 2*time.Second) {
		t.Fatalf("did not enter StateTurnActive; current=%v", rt.State())
	}

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// State should reflect Interrupting promptly.
	if !waitForState(t, rt, StateInterrupting, 1*time.Second) {
		t.Errorf("did not enter StateInterrupting; current=%v", rt.State())
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

	if !waitForState(t, rt, StateIdle, 2*time.Second) {
		t.Errorf("did not return to StateIdle after interrupt drain; current=%v", rt.State())
	}
}

func TestInterruptDelivery_FiresQueueSignal(t *testing.T) {
	// Direct test against a NON-running runtime so we can read Signal() without
	// competing with the Run goroutine.
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.InterruptDelivery(context.Background()); err != nil {
		t.Fatalf("InterruptDelivery: %v", err)
	}

	select {
	case <-rt.Queue().Signal():
		// good
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Queue.Signal did not fire after InterruptDelivery")
	}
}

func TestInterruptDelivery_RepeatedSafe(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			_ = rt.InterruptDelivery(context.Background())
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("repeated InterruptDelivery blocked")
	}

	if got := rt.Queue().Len(); got != 0 {
		t.Errorf("Queue.Len after InterruptDelivery loop = %d, want 0", got)
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

	if got := rt.State(); got != StateStopped {
		t.Errorf("State after Stop = %v, want StateStopped", got)
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
	if got := rt.State(); got != StateTurnActive {
		t.Errorf("State() after EventTurnStarted = %v, want StateTurnActive", got)
	}

	// Release the turn and observe completion.
	close(released)

	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnCompleted
	})

	if !waitForState(t, rt, StateIdle, 2*time.Second) {
		t.Errorf("did not return to StateIdle; current=%v", rt.State())
	}
}

// TestState_DoesNotPeekAtQueue pins QUM-413 directly: State() returns the
// stored runtime state and must NOT synthesize StateTurnActive based on queue
// length. Items enqueued before the loop has dequeued them must not affect the
// state read.
func TestState_DoesNotPeekAtQueue(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	// Do NOT Start. Enqueue directly and observe State().
	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "go"})

	if got := rt.State(); got != StateIdle {
		t.Errorf("State() with queued item but no running loop = %v, want StateIdle (state must not peek at queue)", got)
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

	if !waitForState(t, rt, StateTurnActive, 2*time.Second) {
		t.Fatalf("did not enter StateTurnActive; current=%v", rt.State())
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
	if got := rt.State(); got != StateStopped {
		t.Errorf("State after Stop = %v, want StateStopped", got)
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

	if !waitForState(t, rt, StateTurnActive, 2*time.Second) {
		t.Fatalf("did not enter StateTurnActive; current=%v", rt.State())
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

	if !waitForState(t, rt, StateIdle, 1*time.Second) {
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

// TestInterruptDelivery_DoesNotArmPendingInterrupt_WhenIdle pins QUM-462:
// InterruptDelivery is the wake path used by the supervisor when a peer
// agent has enqueued an inbox item (e.g. WeaveRuntimeHandle.InterruptDelivery
// after sibling/child send_async). It must wake the loop so the queued item
// gets drained, but it must NOT arm `pendingInterrupt` against an idle
// runtime — doing so causes the wrapper to immediately interrupt the very
// turn that would deliver the inbox, so the user sees a banner but Claude
// never actually processes the message.
//
// Expected behaviour: enqueue a ClassInbox item, call InterruptDelivery
// while the runtime is idle, and observe a normal EventTurnCompleted (not
// EventInterrupted) on the EventBus.
func TestInterruptDelivery_DoesNotArmPendingInterrupt_WhenIdle(t *testing.T) {
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
	if !waitForState(t, rt, StateIdle, 1*time.Second) {
		t.Fatalf("runtime did not reach StateIdle before InterruptDelivery; state=%v", rt.State())
	}

	// Mirror WeaveRuntimeHandle.InterruptDelivery: enqueue a ClassInbox item
	// (as if a sibling agent had send_async'd weave) then wake the loop via
	// InterruptDelivery.
	rt.Queue().Enqueue(QueueItem{Class: ClassInbox, Prompt: "[inbox] hello from sibling"})
	if err := rt.InterruptDelivery(context.Background()); err != nil {
		t.Fatalf("InterruptDelivery: %v", err)
	}

	// The terminal event for this turn must be EventTurnCompleted. If the
	// bug is present (pendingInterrupt armed against an idle runtime), the
	// wrapper's StartTurn consumes the flag and immediately calls
	// loop.Interrupt, so the terminal event is EventInterrupted instead.
	ev, seen := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnCompleted || ev.Type == EventInterrupted
	})
	if ev.Type != EventTurnCompleted {
		t.Fatalf("terminal event = %v, want EventTurnCompleted (QUM-462: InterruptDelivery must not arm pendingInterrupt against an idle runtime); seen=%v", ev.Type, seen)
	}
}

// TestInterruptDelivery_DoesNotArmPendingInterrupt_OnTurnEndBoundary pins
// QUM-510: InterruptDelivery snapshots `turnRunning` under RLock, releases
// the lock, then conditionally calls rt.Interrupt. If a turn ends BETWEEN
// those two reads, rt.Interrupt's `else if !turnRunning` branch spuriously
// arms `pendingInterrupt`. The wrapper's next StartTurn (the one supposed
// to deliver the inbox prompt) consumes the flag and routes through
// loop.Interrupt, classifying the inbox turn's terminal event as
// EventInterrupted instead of EventTurnCompleted. The supervisor then
// MarkDelivered's the message even though Claude never saw it.
//
// Determinism: a package-private test hook fires inside InterruptDelivery
// between the snapshot and the decision. The hook closes the in-flight
// turn's events channel (turn1) and waits for the wrapper's post-loop Lock
// to flip turnRunning=false (StateIdle). After the hook returns,
// InterruptDelivery proceeds to its `if started && turnRunning { rt.Interrupt }`
// branch — using the *stale* snapshot (turnRunning=true), so rt.Interrupt
// IS called, and inside rt.Interrupt the live turnRunning is now false, so
// the buggy `else if !turnRunning { rt.pendingInterrupt = true }` branch
// fires.
//
// Note: the inbox item is enqueued AFTER InterruptDelivery returns (rather
// than before, as a naive reading of the bug ticket would suggest). This
// is required for determinism: if the inbox item were already enqueued
// when turn1 ends inside the hook, the loop goroutine would race
// InterruptDelivery's call to rt.Interrupt to grab rt.mu and start turn2
// before pendingInterrupt is armed. Enqueuing post-InterruptDelivery
// guarantees the loop is parked on Queue.Signal() during the race window.
// The bug semantics are preserved: pendingInterrupt is armed against an
// idle, queue-empty runtime, then the next inbox arrival drives the
// misclassified turn.
func TestInterruptDelivery_DoesNotArmPendingInterrupt_OnTurnEndBoundary(t *testing.T) {
	turn1Ch := make(chan *protocol.Message)
	turn2Ch := make(chan *protocol.Message, 1)

	var mock *mockUnifiedSession
	mock = &mockUnifiedSession{
		onStart: func(call int) (<-chan *protocol.Message, error) {
			switch call {
			case 0:
				return turn1Ch, nil
			case 1:
				// Defer the result message into a goroutine so the
				// wrapper's pending-interrupt path (if armed by the
				// QUM-510 bug) has time to deliver loop.Interrupt to
				// executeTurn's select BEFORE the terminal result is
				// observed. We synchronize on mock.interruptCount():
				// when bug-armed pendingInterrupt fires, wrapper calls
				// loop.Interrupt → executeTurn picks thisTurn → calls
				// Session.Interrupt → mock.interruptCount goes to 1.
				// With the bug fixed, no Interrupt is ever called and
				// the deadline elapses; we proceed to send the result
				// and publish EventTurnCompleted.
				go func() {
					deadline := time.Now().Add(500 * time.Millisecond)
					for time.Now().Before(deadline) {
						if mock.interruptCount() >= 1 {
							break
						}
						time.Sleep(2 * time.Millisecond)
					}
					turn2Ch <- makeResultMsg()
					close(turn2Ch)
				}()
				return turn2Ch, nil
			default:
				ch := make(chan *protocol.Message)
				close(ch)
				return ch, nil
			}
		},
	}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	bus := rt.EventBus()
	sub, unsub := bus.Subscribe(64)
	t.Cleanup(unsub)

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	})

	// Drive the runtime into StateTurnActive with turn1 in flight.
	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "first"})
	if !waitForState(t, rt, StateTurnActive, 2*time.Second) {
		t.Fatalf("runtime did not reach StateTurnActive; state=%v", rt.State())
	}

	// Install the hook BEFORE calling InterruptDelivery. The hook fires
	// after InterruptDelivery's RLock snapshot (which observes
	// turnRunning=true) but before its rt.Interrupt decision. The hook
	// ends turn1 and waits for the wrapper goroutine to flip
	// turnRunning=false (StateIdle), so by the time rt.Interrupt runs,
	// the buggy `else if !turnRunning` branch fires and arms
	// pendingInterrupt.
	t.Cleanup(func() { interruptDeliveryAfterSnapshotHook = nil })
	interruptDeliveryAfterSnapshotHook = func() {
		close(turn1Ch)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if rt.State() == StateIdle {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}

	if err := rt.InterruptDelivery(context.Background()); err != nil {
		t.Fatalf("InterruptDelivery: %v", err)
	}

	// At this point, the buggy code has armed pendingInterrupt against an
	// idle (queue-empty, loop parked on Signal) runtime. The fixed code
	// has NOT armed it. Enqueue the inbox item to drive turn2; the loop
	// will wake, drain the inbox, and the wrapper will or will not consume
	// pendingInterrupt depending on which code path is in effect.
	rt.Queue().Enqueue(QueueItem{Class: ClassInbox, Prompt: "[inbox] from ratz"})

	// Wait for the inbox turn's EventTurnStarted to confirm turn2 dispatched.
	_, _ = waitFor(t, sub, 3*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted && ev.Prompt == "[inbox] from ratz"
	})

	// Now wait for turn2's terminal event. With the QUM-510 bug present,
	// pendingInterrupt was armed; wrapper.StartTurn for turn2 consumed it
	// and called loop.Interrupt → terminal event is EventInterrupted. With
	// the bug fixed, turn2 completes normally → EventTurnCompleted.
	ev, seen := waitFor(t, sub, 3*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnCompleted ||
			ev.Type == EventInterrupted ||
			ev.Type == EventTurnFailed
	})
	if ev.Type != EventTurnCompleted {
		t.Fatalf("turn2 terminal event = %v, want EventTurnCompleted "+
			"(QUM-510: InterruptDelivery's TOCTOU race between RLock snapshot "+
			"and rt.Interrupt arms pendingInterrupt across the turn-end "+
			"boundary, causing the inbox-delivery turn to be classified as "+
			"EventInterrupted); seen=%v", ev.Type, seen)
	}
}

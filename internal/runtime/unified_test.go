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

	// We may miss the brief TurnActive window, so only require eventual return to Idle.
	if !waitForState(t, rt, StateIdle, 2*time.Second) {
		t.Fatalf("did not return to StateIdle; current=%v", rt.State())
	}
	if got := mock.startCount(); got < 1 {
		t.Errorf("StartTurn calls = %d, want >= 1", got)
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

func TestInterrupt_WhenIdle_NoOp(t *testing.T) {
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

	if got := mock.interruptCount(); got != 0 {
		t.Errorf("Session.Interrupt called %d times while idle, want 0", got)
	}
	if got := rt.State(); got != StateIdle {
		t.Errorf("State after idle Interrupt = %v, want StateIdle", got)
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && mock.interruptCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := mock.interruptCount(); got != 1 {
		t.Errorf("Session.Interrupt count = %d, want 1", got)
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

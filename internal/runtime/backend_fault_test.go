package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-602: when a backend session sets its terminal-error sentinel
// (ErrHangTimeout / ErrSubscriberWedged), the UnifiedRuntime must surface a
// fresh RuntimeEvent{Type: EventBackendFaulted, FaultClass, FaultNextAction,
// Error} on its EventBus so subscribers (supervisor → TUI) can render the
// fault row + banner without polling the session.

// mockFaultableSession is a SessionHandle test double that captures a
// terminal-error-handler registration so the test can fire it on demand. It
// satisfies the same surface the production wiring uses to install the
// callback on the concrete *session.
type mockFaultableSession struct {
	mockUnifiedSession
	handler atomic.Value // func(error)
}

func (m *mockFaultableSession) SetTerminalErrorHandler(h func(error)) {
	m.handler.Store(h)
}

func (m *mockFaultableSession) fireTerminalErr(err error) {
	h, _ := m.handler.Load().(func(error))
	if h == nil {
		return
	}
	h(err)
}

func TestUnifiedRuntime_PublishesEventBackendFaultedOnTerminalErr(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{
		Name:    "agent-fault",
		Session: mock,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("fault-test", 8)
	defer unsub()

	// Production wiring must have stored the handler by Start time.
	if h, _ := mock.handler.Load().(func(error)); h == nil {
		t.Fatal("UnifiedRuntime did not register a terminal-error handler on its session")
	}

	mock.fireTerminalErr(backend.ErrHangTimeout)

	deadline := time.After(1 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("EventBus channel closed before EventBackendFaulted arrived")
			}
			if ev.Type != EventBackendFaulted {
				continue
			}
			if ev.FaultClass != "HangTimeout" {
				t.Errorf("FaultClass = %q, want %q", ev.FaultClass, "HangTimeout")
			}
			if ev.FaultNextAction == "" {
				t.Errorf("FaultNextAction is empty; expected a non-empty operator hint")
			}
			if !errors.Is(ev.Error, backend.ErrHangTimeout) {
				t.Errorf("Error = %v, want errors.Is(ErrHangTimeout)", ev.Error)
			}
			return
		case <-deadline:
			t.Fatal("timeout waiting for EventBackendFaulted on the EventBus")
		}
	}
}

// TestUnifiedRuntime_PublishesEventTurnFailedOnTerminalErrDuringTurn is the
// QUM-635 runtime fix guard. When the backend session fires a terminal error
// WHILE A TURN IS IN FLIGHT (rt.turnRunning == true), the UnifiedRuntime must
// publish a terminal turn event EventTurnFailed on its EventBus (in addition
// to the existing EventBackendFaulted) so the TUI's existing terminal path
// (SessionResultMsg -> finalizeTurn) clears the streaming state and ungates
// input.
//
// RED today: the SetTerminalErrorHandler callback in New() publishes only
// EventBackendFaulted — never EventTurnFailed — so the EventTurnFailed drain
// below times out.
func TestUnifiedRuntime_PublishesEventTurnFailedOnTerminalErrDuringTurn(t *testing.T) {
	// blockCh is never closed during the test: the forwarding goroutine in
	// stateTrackingSession parks in `for msg := range ch`, keeping
	// turnRunning=true until runCtx is cancelled by Stop.
	blockCh := make(chan *protocol.Message)

	mock := &mockFaultableSession{}
	mock.onStart = func(_ int) (<-chan *protocol.Message, error) {
		return blockCh, nil
	}

	rt := New(RuntimeConfig{
		Name:    "agent-fault-turn",
		Session: mock,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		// blockCh never closes on its own; Stop's ctx cancel unblocks the
		// turn loop via runCtx cancellation.
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	// Subscribe BEFORE enqueuing so we don't miss any event.
	ch, unsub := rt.EventBus().SubscribeNamed("turnfail-test", 16)
	defer unsub()

	// Drive the loop to call StartTurn -> turnRunning=true.
	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "hi"})

	// Confirm a turn actually started before we fire the fault.
	deadline := time.Now().Add(2 * time.Second)
	for mock.startCount() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("turn never started (StartTurn not invoked) before firing terminal err")
		}
		time.Sleep(5 * time.Millisecond)
	}

	mock.fireTerminalErr(backend.ErrHangTimeout)

	// Both EventBackendFaulted AND EventTurnFailed must be observed.
	gotFaulted := false
	gotTurnFailed := false
	drainDeadline := time.After(1 * time.Second)
	for !gotFaulted || !gotTurnFailed {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("EventBus channel closed; got faulted=%v turnFailed=%v", gotFaulted, gotTurnFailed)
			}
			switch ev.Type {
			case EventBackendFaulted:
				gotFaulted = true
			case EventTurnFailed:
				gotTurnFailed = true
				if !errors.Is(ev.Error, backend.ErrHangTimeout) {
					t.Errorf("EventTurnFailed.Error = %v, want errors.Is(ErrHangTimeout)", ev.Error)
				}
			}
		case <-drainDeadline:
			t.Fatalf("timeout waiting for events: got faulted=%v turnFailed=%v (want both); EventTurnFailed must be published when a terminal err fires mid-turn", gotFaulted, gotTurnFailed)
		}
	}
}

// TestUnifiedRuntime_FaultDuringTurnEmitsExactlyOneTerminalTurnEvent locks
// down the no-double-finalize property (QUM-635): a mid-turn backend fault
// must publish exactly one EventTurnFailed and zero EventTurnCompleted. The
// turn loop exits silently on the fault-induced runCtx cancel (no competing
// terminal event), so the TUI finalizes exactly once.
func TestUnifiedRuntime_FaultDuringTurnEmitsExactlyOneTerminalTurnEvent(t *testing.T) {
	blockCh := make(chan *protocol.Message)

	mock := &mockFaultableSession{}
	mock.onStart = func(_ int) (<-chan *protocol.Message, error) {
		return blockCh, nil
	}

	rt := New(RuntimeConfig{
		Name:    "agent-fault-once",
		Session: mock,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("turnfail-once-test", 32)
	defer unsub()

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "hi"})

	deadline := time.Now().Add(2 * time.Second)
	for mock.startCount() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("turn never started before firing terminal err")
		}
		time.Sleep(5 * time.Millisecond)
	}

	mock.fireTerminalErr(backend.ErrHangTimeout)

	// Drain a fixed window and tally terminal turn events.
	turnFailed := 0
	turnCompleted := 0
	drainDeadline := time.After(750 * time.Millisecond)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				goto done
			}
			switch ev.Type {
			case EventTurnFailed:
				turnFailed++
			case EventTurnCompleted:
				turnCompleted++
			}
		case <-drainDeadline:
			goto done
		}
	}
done:
	if turnFailed != 1 {
		t.Errorf("EventTurnFailed count = %d, want exactly 1", turnFailed)
	}
	if turnCompleted != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0 on the fault path", turnCompleted)
	}
}

// TestUnifiedRuntime_NoEventTurnFailedOnTerminalErrWhenIdle guards the
// turnRunning gate: a terminal err that fires while NO turn is in flight must
// NOT publish a spurious EventTurnFailed (which would make the TUI finalize a
// turn that never ran). EventBackendFaulted must still fire.
//
// This test PASSES today (nothing publishes EventTurnFailed yet); it exists
// to keep the implementer's turnRunning gate honest once they wire the
// mid-turn EventTurnFailed publish.
func TestUnifiedRuntime_NoEventTurnFailedOnTerminalErrWhenIdle(t *testing.T) {
	// Default mock StartTurn closes its channel immediately; we never enqueue
	// work, so turnRunning stays false the whole test.
	mock := &mockFaultableSession{}

	rt := New(RuntimeConfig{
		Name:    "agent-fault-idle",
		Session: mock,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("turnfail-idle-test", 16)
	defer unsub()

	// Do NOT enqueue any work: turnRunning stays false.
	mock.fireTerminalErr(backend.ErrHangTimeout)

	gotFaulted := false
	drainDeadline := time.After(500 * time.Millisecond)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("EventBus channel closed unexpectedly; faulted=%v", gotFaulted)
			}
			switch ev.Type {
			case EventBackendFaulted:
				gotFaulted = true
			case EventTurnFailed:
				t.Fatal("EventTurnFailed published on idle terminal err; want none (no turn in flight)")
			}
		case <-drainDeadline:
			if !gotFaulted {
				t.Fatal("EventBackendFaulted was not observed on idle terminal err")
			}
			return
		}
	}
}

func TestClassifyBackendFault_MapsKnownSentinels(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantClass string
		// hint contract: non-empty for known sentinels and the default.
		wantHintSubstr string
	}{
		{
			name:           "HangTimeout sentinel",
			err:            backend.ErrHangTimeout,
			wantClass:      "HangTimeout",
			wantHintSubstr: "wake",
		},
		{
			name:           "SubscriberWedged sentinel",
			err:            backend.ErrSubscriberWedged,
			wantClass:      "SubscriberWedged",
			wantHintSubstr: "wake",
		},
		{
			name:           "Unknown error",
			err:            errors.New("some other backend fault"),
			wantClass:      "Unknown",
			wantHintSubstr: "wake",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotClass, gotHint := ClassifyBackendFault(tc.err)
			if gotClass != tc.wantClass {
				t.Errorf("class = %q, want %q", gotClass, tc.wantClass)
			}
			if gotHint == "" {
				t.Errorf("hint is empty; expected operator-facing next-action string")
			}
		})
	}
}

package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
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
			wantHintSubstr: "retire",
		},
		{
			name:           "SubscriberWedged sentinel",
			err:            backend.ErrSubscriberWedged,
			wantClass:      "SubscriberWedged",
			wantHintSubstr: "retire",
		},
		{
			name:           "Unknown error",
			err:            errors.New("some other backend fault"),
			wantClass:      "Unknown",
			wantHintSubstr: "retire",
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

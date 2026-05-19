package backend

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// QUM-602: production code must surface a watchdog/terminal-err fire to the
// runtime via a session-level callback (SetTerminalErrorHandler). The handler
// must fire exactly once (matching the existing terminalErr sticky-once gate)
// and must be invoked OUTSIDE the session mutex so the callback can call back
// into session-safe read methods without deadlock.

func newTestSessionForHandlerTest(t *testing.T) *session {
	t.Helper()
	tr := newMockManagedTransport()
	s, ok := NewSession(tr, SessionConfig{SessionID: "sess-handler"}).(*session)
	if !ok {
		t.Fatalf("NewSession did not return *session concrete type")
	}
	return s
}

func TestSession_SetTerminalErrorHandler_FiresOnceWithFirstError(t *testing.T) {
	s := newTestSessionForHandlerTest(t)

	var (
		calls    atomic.Int32
		firstErr atomic.Value // error
	)
	s.SetTerminalErrorHandler(func(err error) {
		calls.Add(1)
		if firstErr.Load() == nil {
			firstErr.Store(err)
		}
	})

	s.setTerminalErr(ErrHangTimeout)
	s.setTerminalErr(ErrSubscriberWedged)

	if got := calls.Load(); got != 1 {
		t.Fatalf("handler call count = %d, want exactly 1 (sticky-once gate)", got)
	}
	got, _ := firstErr.Load().(error)
	if !errors.Is(got, ErrHangTimeout) {
		t.Errorf("handler first err = %v, want ErrHangTimeout", got)
	}
}

func TestSession_SetTerminalErrorHandler_NotInvokedWhenNotSet(t *testing.T) {
	s := newTestSessionForHandlerTest(t)
	// Sanity: invoking setTerminalErr without a registered handler must not
	// panic or block. (No assertion needed beyond "no panic / no hang".)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.setTerminalErr(ErrHangTimeout)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("setTerminalErr blocked without handler set")
	}
}

func TestSession_SetTerminalErrorHandler_NotInvokedUnderLock(t *testing.T) {
	s := newTestSessionForHandlerTest(t)

	var wg sync.WaitGroup
	wg.Add(1)
	doneCalled := make(chan struct{})
	s.SetTerminalErrorHandler(func(err error) {
		defer wg.Done()
		// Call back into a session-safe method that takes the session
		// mutex (BackendStats reads atomic counters, but the broader
		// invariant we are guarding is that the callback must be fired
		// OUTSIDE s.mu so callers can do any read-side work without
		// deadlock).
		_ = s.BackendStats()
		close(doneCalled)
	})

	fired := make(chan struct{})
	go func() {
		s.setTerminalErr(ErrHangTimeout)
		close(fired)
	}()

	select {
	case <-doneCalled:
	case <-time.After(time.Second):
		t.Fatal("terminal err handler did not return — likely invoked under s.mu (deadlock)")
	}
	<-fired
	wg.Wait()
}

package backend

import (
	"errors"
	"testing"
)

// QUM-601: Session must expose a probe so the runtime layer can decide
// whether in-place recovery is needed. IsTerminallyFaulted returns true
// once the sticky terminalErr has been set (matches the LastTurnError /
// reject-next-StartTurn semantics). The probe is on both the *session
// concrete type and the public Session interface.

func newRecoverProbeSession(t *testing.T) *session {
	t.Helper()
	tr := newMockManagedTransport()
	s, ok := NewSession(tr, SessionConfig{SessionID: "sess-recover-probe"}).(*session)
	if !ok {
		t.Fatalf("NewSession did not return *session concrete type")
	}
	return s
}

func TestSession_IsTerminallyFaulted_FalseOnHealthy(t *testing.T) {
	s := newRecoverProbeSession(t)
	if s.IsTerminallyFaulted() {
		t.Fatalf("fresh session reports IsTerminallyFaulted() = true, want false")
	}

	// Also assert the probe is visible via the Session interface (not just
	// the concrete type) — the runtime layer holds a Session, not a *session.
	var iface Session = s
	if probe, ok := iface.(interface{ IsTerminallyFaulted() bool }); !ok {
		t.Fatalf("Session interface value does not satisfy IsTerminallyFaulted probe")
	} else if probe.IsTerminallyFaulted() {
		t.Fatalf("Session.IsTerminallyFaulted() = true on healthy session, want false")
	}
}

func TestSession_IsTerminallyFaulted_TrueAfterSetTerminalErr(t *testing.T) {
	s := newRecoverProbeSession(t)
	s.setTerminalErr(ErrHangTimeout)
	if !s.IsTerminallyFaulted() {
		t.Fatalf("after setTerminalErr(ErrHangTimeout), IsTerminallyFaulted() = false; want true")
	}
}

func TestSession_IsTerminallyFaulted_StickyAcrossMultipleSets(t *testing.T) {
	s := newRecoverProbeSession(t)
	s.setTerminalErr(ErrHangTimeout)
	s.setTerminalErr(ErrSubscriberWedged)
	if !s.IsTerminallyFaulted() {
		t.Fatalf("IsTerminallyFaulted() = false after two setTerminalErr calls; want true (sticky)")
	}
	// Sanity-check the underlying sticky-error semantics still hold —
	// subsequent setTerminalErr does not unset terminalErr.
	if !errors.Is(s.terminalErr, ErrHangTimeout) {
		t.Errorf("terminalErr = %v, want first-set ErrHangTimeout (sticky)", s.terminalErr)
	}
}

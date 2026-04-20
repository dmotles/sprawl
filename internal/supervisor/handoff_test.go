package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/state"
)

// installHandoffSeams wires fake dependencies for Handoff onto r and returns
// pointers to the captured call data + a writeSignal flag.
type handoffCapture struct {
	wroteSession   bool
	sessionArg     memory.Session
	bodyArg        string
	wroteSignal    bool
	listAgentsCall int
	sessionID      string
	listErr        error
	writeErr       error
	signalErr      error
	readIDErr      error
	now            time.Time
}

func installHandoffSeams(r *Real, c *handoffCapture) {
	r.handoffReadLastSessionID = func(string) (string, error) {
		return c.sessionID, c.readIDErr
	}
	r.handoffListAgents = func(string) ([]*state.AgentState, error) {
		c.listAgentsCall++
		if c.listErr != nil {
			return nil, c.listErr
		}
		return []*state.AgentState{{Name: "ratz"}, {Name: "ghost"}}, nil
	}
	r.handoffWriteSessionSummary = func(_ string, s memory.Session, body string) error {
		c.wroteSession = true
		c.sessionArg = s
		c.bodyArg = body
		return c.writeErr
	}
	r.handoffWriteSignalFile = func(string) error {
		c.wroteSignal = true
		return c.signalErr
	}
	if !c.now.IsZero() {
		r.handoffNow = func() time.Time { return c.now }
	}
}

func TestHandoff_RejectsEmptySummary(t *testing.T) {
	r, _ := newTestSupervisor(t)
	capt := &handoffCapture{sessionID: "abc"}
	installHandoffSeams(r, capt)

	for _, in := range []string{"", "   \n\t "} {
		err := r.Handoff(context.Background(), in)
		if err == nil {
			t.Errorf("Handoff(%q): expected error, got nil", in)
		}
	}

	if capt.wroteSession || capt.wroteSignal {
		t.Errorf("Handoff wrote side effects on empty input: wroteSession=%v wroteSignal=%v",
			capt.wroteSession, capt.wroteSignal)
	}

	select {
	case <-r.HandoffRequested():
		t.Error("channel fired on empty-input rejection")
	default:
	}
}

func TestHandoff_WritesSessionAndSignal(t *testing.T) {
	r, _ := newTestSupervisor(t)
	fixed := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	capt := &handoffCapture{sessionID: "sess-123", now: fixed}
	installHandoffSeams(r, capt)

	body := "## Summary\nstuff happened\n"
	if err := r.Handoff(context.Background(), body); err != nil {
		t.Fatalf("Handoff: %v", err)
	}

	if !capt.wroteSession {
		t.Fatal("WriteSessionSummary not called")
	}
	if !capt.wroteSignal {
		t.Error("WriteHandoffSignal not called")
	}
	if capt.sessionArg.SessionID != "sess-123" {
		t.Errorf("SessionID = %q, want sess-123", capt.sessionArg.SessionID)
	}
	if !capt.sessionArg.Handoff {
		t.Error("Session.Handoff = false, want true")
	}
	if !capt.sessionArg.Timestamp.Equal(fixed) {
		t.Errorf("Timestamp = %v, want %v", capt.sessionArg.Timestamp, fixed)
	}
	if len(capt.sessionArg.AgentsActive) != 2 || capt.sessionArg.AgentsActive[0] != "ratz" {
		t.Errorf("AgentsActive = %v, want [ratz ghost]", capt.sessionArg.AgentsActive)
	}
	if capt.bodyArg != body {
		t.Errorf("body = %q, want %q", capt.bodyArg, body)
	}
}

func TestHandoff_FiresChannelOnSuccess(t *testing.T) {
	r, _ := newTestSupervisor(t)
	capt := &handoffCapture{sessionID: "sess"}
	installHandoffSeams(r, capt)

	if err := r.Handoff(context.Background(), "body"); err != nil {
		t.Fatalf("Handoff: %v", err)
	}

	select {
	case <-r.HandoffRequested():
	case <-time.After(time.Second):
		t.Fatal("HandoffRequested() channel did not fire within 1s")
	}
}

func TestHandoff_ChannelSendIsNonBlocking(t *testing.T) {
	r, _ := newTestSupervisor(t)
	capt := &handoffCapture{sessionID: "sess"}
	installHandoffSeams(r, capt)

	// Fire Handoff several times without draining the channel; non-blocking
	// send semantics should keep the second+ calls from hanging.
	for i := 0; i < 3; i++ {
		done := make(chan error, 1)
		go func() { done <- r.Handoff(context.Background(), "body") }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Handoff call %d: %v", i, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("Handoff call %d blocked on channel send", i)
		}
	}
}

func TestHandoff_MissingSessionID(t *testing.T) {
	r, _ := newTestSupervisor(t)
	capt := &handoffCapture{sessionID: ""}
	installHandoffSeams(r, capt)

	err := r.Handoff(context.Background(), "body")
	if err == nil {
		t.Fatal("expected error for missing session ID")
	}
	if capt.wroteSession || capt.wroteSignal {
		t.Error("wrote side effects despite missing session ID")
	}
}

func TestHandoff_PropagatesReadSessionIDError(t *testing.T) {
	r, _ := newTestSupervisor(t)
	boom := errors.New("disk gone")
	capt := &handoffCapture{readIDErr: boom}
	installHandoffSeams(r, capt)

	if err := r.Handoff(context.Background(), "body"); err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wraps %v", err, boom)
	}
}

func TestHandoff_PropagatesWriteError(t *testing.T) {
	r, _ := newTestSupervisor(t)
	boom := errors.New("write failed")
	capt := &handoffCapture{sessionID: "s", writeErr: boom}
	installHandoffSeams(r, capt)

	if err := r.Handoff(context.Background(), "body"); err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wraps %v", err, boom)
	}
	if capt.wroteSignal {
		t.Error("signal written despite write error")
	}
	select {
	case <-r.HandoffRequested():
		t.Error("channel fired despite write error")
	default:
	}
}

func TestHandoff_InterfaceSatisfied(t *testing.T) {
	var _ Supervisor = (*Real)(nil)
}

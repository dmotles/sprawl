package agentloop

import (
	"context"
	"io"
	"testing"
	"time"

	backend "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

type stopTestSession struct {
	closeCalls int
	killCalls  int
	waitDone   chan error
}

func (s *stopTestSession) Initialize(context.Context, backend.InitSpec) error { return nil }
func (s *stopTestSession) StartTurn(context.Context, string, ...backend.TurnSpec) (<-chan *protocol.Message, error) {
	ch := make(chan *protocol.Message)
	close(ch)
	return ch, nil
}
func (s *stopTestSession) Interrupt(context.Context) error { return nil }
func (s *stopTestSession) Close() error {
	s.closeCalls++
	return nil
}
func (s *stopTestSession) Wait() error                        { return <-s.waitDone }
func (s *stopTestSession) Kill() error                        { s.killCalls++; s.waitDone <- nil; return nil }
func (s *stopTestSession) LastTurnError() error               { return io.EOF }
func (s *stopTestSession) SessionID() string                  { return "sess-finn" }
func (s *stopTestSession) Capabilities() backend.Capabilities { return backend.Capabilities{} }

func TestClaudeBackendProcess_StopKillsOnContextDeadline(t *testing.T) {
	session := &stopTestSession{waitDone: make(chan error, 1)}
	proc := &claudeBackendProcess{
		session: session,
		running: true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := proc.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop() error = %v, want nil after successful kill fallback", err)
	}
	if session.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", session.closeCalls)
	}
	if session.killCalls != 1 {
		t.Fatalf("Kill() calls = %d, want 1", session.killCalls)
	}
	if proc.IsRunning() {
		t.Fatal("process should report not running after Stop")
	}
}

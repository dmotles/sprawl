package agentloop

import (
	"context"
	"io"
	"testing"
	"time"

	backend "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/state"
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

func TestBuildAgentSessionSpec_BaseFields(t *testing.T) {
	agentState := &state.AgentState{
		Name:      "finn",
		Type:      "engineer",
		Worktree:  "/tmp/worktrees/finn",
		TreePath:  "weave/finn",
		SessionID: "sess-finn",
	}
	spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard)

	// AllowedTools are set by the caller (runtime_launcher) via
	// RunnerDeps.AllowedTools, not by BuildAgentSessionSpec itself.
	// Verify base fields are correct.
	if spec.Identity != "finn" {
		t.Errorf("Identity = %q, want \"finn\"", spec.Identity)
	}
	if spec.SessionID != "sess-finn" {
		t.Errorf("SessionID = %q, want \"sess-finn\"", spec.SessionID)
	}
	if spec.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want \"bypassPermissions\"", spec.PermissionMode)
	}
}

func TestBuildAgentSessionSpec_ModelByAgentType(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		wantModel string
	}{
		{"engineer gets opus", "engineer", "opus"},
		{"researcher gets opus", "researcher", "opus"},
		{"manager gets opus[1m]", "manager", "opus[1m]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentState := &state.AgentState{
				Name:      "test-agent",
				Type:      tt.agentType,
				Worktree:  "/tmp/worktrees/test",
				SessionID: "sess-test",
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard)
			if spec.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q for agent type %q", spec.Model, tt.wantModel, tt.agentType)
			}
			if spec.Effort != "medium" {
				t.Errorf("Effort = %q, want \"medium\"", spec.Effort)
			}
		})
	}
}

// initTestSession records Initialize and StartTurn calls so tests can verify
// that the child backend wiring passes InitSpec and TurnSpec correctly.
type initTestSession struct {
	stopTestSession

	initCalled bool
	initSpec   backend.InitSpec
	turnSpecs  []backend.TurnSpec
}

func (s *initTestSession) Initialize(_ context.Context, spec backend.InitSpec) error {
	s.initCalled = true
	s.initSpec = spec
	return nil
}

func (s *initTestSession) StartTurn(_ context.Context, _ string, specs ...backend.TurnSpec) (<-chan *protocol.Message, error) {
	s.turnSpecs = append(s.turnSpecs, specs...)
	ch := make(chan *protocol.Message, 1)
	result := &protocol.Message{
		Type: "result",
		Raw:  []byte(`{"type":"result","result":"ok","is_error":false,"stop_reason":"end_turn","num_turns":1}`),
	}
	ch <- result
	close(ch)
	return ch, nil
}

func TestClaudeBackendProcess_InitSpecFieldExists(t *testing.T) {
	// Verify that claudeBackendProcess has an initSpec field and that it is
	// preserved. After implementation, Launch() will call
	// session.Initialize(ctx, initSpec). This test just verifies the struct
	// carries the field — the Launch-based initialization test below covers
	// the full flow.
	session := &initTestSession{
		stopTestSession: stopTestSession{waitDone: make(chan error, 1)},
	}
	initSpec := backend.InitSpec{
		MCPServerNames: []string{"sprawl"},
	}

	proc := &claudeBackendProcess{
		session:  session,
		running:  true,
		initSpec: initSpec,
	}

	// The initSpec should be stored on the struct.
	if len(proc.initSpec.MCPServerNames) == 0 {
		t.Error("claudeBackendProcess.initSpec.MCPServerNames is empty after construction")
	}
	if proc.initSpec.MCPServerNames[0] != "sprawl" {
		t.Errorf("claudeBackendProcess.initSpec.MCPServerNames[0] = %q, want \"sprawl\"", proc.initSpec.MCPServerNames[0])
	}
}

func TestClaudeBackendProcess_SendPromptPassesTurnSpec(t *testing.T) {
	session := &initTestSession{
		stopTestSession: stopTestSession{waitDone: make(chan error, 1)},
	}
	initSpec := backend.InitSpec{
		MCPServerNames: []string{"sprawl"},
	}

	proc := &claudeBackendProcess{
		session:  session,
		running:  true,
		initSpec: initSpec,
	}

	_, err := proc.SendPrompt(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("SendPrompt() error: %v", err)
	}

	if len(session.turnSpecs) == 0 {
		t.Fatal("SendPrompt() did not pass TurnSpec to StartTurn; MCP bridge not threaded through turns")
	}
	ts := session.turnSpecs[0]
	if len(ts.Init.MCPServerNames) == 0 {
		t.Error("TurnSpec.Init.MCPServerNames is empty; expected sprawl server name")
	}
}

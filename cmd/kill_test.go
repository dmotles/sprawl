package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dmotles/dendrarchy/internal/state"
)

// killMockRunner implements tmux.Runner for kill tests.
type killMockRunner struct {
	hasSession       bool
	killWindowCalled bool
	killWindowErr    error
	killWindowSession string
	killWindowWindow  string
	pids             []int
	pidsErr          error
}

func (m *killMockRunner) HasSession(name string) bool                  { return m.hasSession }
func (m *killMockRunner) NewSession(name string, env map[string]string, shellCmd string) error {
	return nil
}
func (m *killMockRunner) NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}
func (m *killMockRunner) NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}
func (m *killMockRunner) Attach(name string) error { return nil }

func (m *killMockRunner) KillWindow(sessionName, windowName string) error {
	m.killWindowCalled = true
	m.killWindowSession = sessionName
	m.killWindowWindow = windowName
	return m.killWindowErr
}

func (m *killMockRunner) ListWindowPIDs(sessionName, windowName string) ([]int, error) {
	return m.pids, m.pidsErr
}

func newTestKillDeps(t *testing.T) (*killDeps, *killMockRunner, string, []int) {
	t.Helper()
	tmpDir := t.TempDir()
	runner := &killMockRunner{}
	signaled := make([]int, 0)

	deps := &killDeps{
		tmuxRunner: runner,
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return tmpDir
			}
			return ""
		},
		signalFunc: func(pid int, sig syscall.Signal) error {
			signaled = append(signaled, int(sig))
			return nil
		},
		sleepFunc:    func(d time.Duration) {},
		processAlive: func(pid int) bool { return false },
	}

	// Ensure agents dir exists
	os.MkdirAll(state.AgentsDir(tmpDir), 0755)

	return deps, runner, tmpDir, signaled
}

func createTestAgent(t *testing.T, dendraRoot string, agent *state.AgentState) {
	t.Helper()
	if err := state.SaveAgent(dendraRoot, agent); err != nil {
		t.Fatalf("saving test agent: %v", err)
	}
}

func TestKill_HappyPath(t *testing.T) {
	deps, runner, tmpDir, _ := newTestKillDeps(t)
	runner.pids = []int{12345}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		TmuxSession: "dendra-root-children",
		TmuxWindow:  "alice",
	})

	err := runKill(deps, "alice", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify state updated to killed
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Status != "killed" {
		t.Errorf("status = %q, want %q", agentState.Status, "killed")
	}

	// Verify tmux window was closed
	if !runner.killWindowCalled {
		t.Error("expected KillWindow to be called")
	}
	if runner.killWindowSession != "dendra-root-children" {
		t.Errorf("kill window session = %q, want %q", runner.killWindowSession, "dendra-root-children")
	}
	if runner.killWindowWindow != "alice" {
		t.Errorf("kill window name = %q, want %q", runner.killWindowWindow, "alice")
	}
}

func TestKill_GracefulSignals(t *testing.T) {
	deps, runner, tmpDir, _ := newTestKillDeps(t)
	runner.pids = []int{12345}

	var signals []syscall.Signal
	deps.signalFunc = func(pid int, sig syscall.Signal) error {
		signals = append(signals, sig)
		return nil
	}
	deps.processAlive = func(pid int) bool { return true }

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		TmuxSession: "dendra-root-children",
		TmuxWindow:  "alice",
	})

	err := runKill(deps, "alice", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should send SIGTERM first, then SIGKILL after process is still alive
	if len(signals) != 2 {
		t.Fatalf("expected 2 signals, got %d: %v", len(signals), signals)
	}
	if signals[0] != syscall.SIGTERM {
		t.Errorf("first signal = %v, want SIGTERM", signals[0])
	}
	if signals[1] != syscall.SIGKILL {
		t.Errorf("second signal = %v, want SIGKILL", signals[1])
	}
}

func TestKill_ForceSkipsGraceful(t *testing.T) {
	deps, runner, tmpDir, _ := newTestKillDeps(t)
	runner.pids = []int{12345}

	var signals []syscall.Signal
	deps.signalFunc = func(pid int, sig syscall.Signal) error {
		signals = append(signals, sig)
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		TmuxSession: "dendra-root-children",
		TmuxWindow:  "alice",
	})

	err := runKill(deps, "alice", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Force: should only send SIGKILL, no SIGTERM
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d: %v", len(signals), signals)
	}
	if signals[0] != syscall.SIGKILL {
		t.Errorf("signal = %v, want SIGKILL", signals[0])
	}
}

func TestKill_AlreadyKilled_IsNoOp(t *testing.T) {
	deps, runner, tmpDir, _ := newTestKillDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "killed",
		TmuxSession: "dendra-root-children",
		TmuxWindow:  "alice",
	})

	err := runKill(deps, "alice", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT have tried to kill the window
	if runner.killWindowCalled {
		t.Error("KillWindow should not be called for already-killed agent")
	}
}

func TestKill_AgentNotFound(t *testing.T) {
	deps, _, _, _ := newTestKillDeps(t)

	err := runKill(deps, "nonexistent", false)
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestKill_MissingDendraRoot(t *testing.T) {
	deps, _, _, _ := newTestKillDeps(t)
	deps.getenv = func(key string) string { return "" }

	err := runKill(deps, "alice", false)
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

func TestKill_NoProcesses_StillUpdatesState(t *testing.T) {
	deps, _, tmpDir, _ := newTestKillDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		TmuxSession: "dendra-root-children",
		TmuxWindow:  "alice",
	})

	err := runKill(deps, "alice", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Status != "killed" {
		t.Errorf("status = %q, want %q", agentState.Status, "killed")
	}
}

func TestKill_PreservesState(t *testing.T) {
	deps, _, tmpDir, _ := newTestKillDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Type:        "engineer",
		Family:      "engineering",
		Parent:      "root",
		Prompt:      "test task",
		Branch:      "dendra/alice",
		Worktree:    "/path/to/worktree",
		TmuxSession: "dendra-root-children",
		TmuxWindow:  "alice",
		Status:      "active",
		CreatedAt:   "2026-01-01T00:00:00Z",
	})

	err := runKill(deps, "alice", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all state is preserved except status
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Type != "engineer" {
		t.Errorf("Type = %q, want %q", agentState.Type, "engineer")
	}
	if agentState.Worktree != "/path/to/worktree" {
		t.Errorf("Worktree = %q, want %q", agentState.Worktree, "/path/to/worktree")
	}
	if agentState.Branch != "dendra/alice" {
		t.Errorf("Branch = %q, want %q", agentState.Branch, "dendra/alice")
	}
	// State file should still exist
	_, err = os.Stat(filepath.Join(state.AgentsDir(tmpDir), "alice.json"))
	if err != nil {
		t.Errorf("state file should still exist: %v", err)
	}
}

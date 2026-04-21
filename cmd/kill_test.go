package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
)

// killMockRunner implements tmux.Runner for kill tests.
type killMockRunner struct {
	mu                sync.Mutex
	hasSession        bool
	killWindowCalled  bool
	killWindowErr     error
	killWindowSession string
	killWindowWindow  string
	// Default ListWindowPIDs behavior (used when listWindowPIDsFunc is nil).
	pids    []int
	pidsErr error
	// Function-based mock for per-call control of ListWindowPIDs.
	listWindowPIDsFunc func(sessionName, windowName string) ([]int, error)
}

func (m *killMockRunner) HasWindow(string, string) bool { return false }
func (m *killMockRunner) HasSession(name string) bool   { return m.hasSession }
func (m *killMockRunner) NewSession(name string, env map[string]string, shellCmd string) error {
	return nil
}

func (m *killMockRunner) NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}

func (m *killMockRunner) NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}
func (m *killMockRunner) SendKeys(sessionName, windowName string, keys string) error { return nil }
func (m *killMockRunner) Attach(name string) error                                   { return nil }
func (m *killMockRunner) SourceFile(string, string) error                            { return nil }
func (m *killMockRunner) SetEnvironment(string, string, string) error                { return nil }

func (m *killMockRunner) KillWindow(sessionName, windowName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killWindowCalled = true
	m.killWindowSession = sessionName
	m.killWindowWindow = windowName
	return m.killWindowErr
}

func (m *killMockRunner) ListSessionNames() ([]string, error) { return nil, nil }

func (m *killMockRunner) ListWindowPIDs(sessionName, windowName string) ([]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listWindowPIDsFunc != nil {
		return m.listWindowPIDsFunc(sessionName, windowName)
	}
	return m.pids, m.pidsErr
}

func newTestKillDeps(t *testing.T) (*killDeps, *killMockRunner, string) {
	t.Helper()
	tmpDir := t.TempDir()
	runner := &killMockRunner{}

	deps := &killDeps{
		TmuxRunner: runner,
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		WriteFile:  func(path string, data []byte, perm os.FileMode) error { return nil },
		RemoveFile: func(path string) error { return nil },
		SleepFunc:  func(d time.Duration) {},
	}

	// Ensure agents dir exists
	os.MkdirAll(state.AgentsDir(tmpDir), 0o755)

	return deps, runner, tmpDir
}

func createTestAgent(t *testing.T, sprawlRoot string, agent *state.AgentState) {
	t.Helper()
	if err := state.SaveAgent(sprawlRoot, agent); err != nil {
		t.Fatalf("saving test agent: %v", err)
	}
}

func TestKill_InvalidAgentNameReturnsError(t *testing.T) {
	deps, _, _ := newTestKillDeps(t)
	err := runKill(deps, "../evil", false)
	if err == nil {
		t.Fatal("expected error for invalid agent name")
	}
	if !strings.Contains(err.Error(), "invalid agent name") {
		t.Errorf("error should mention 'invalid agent name', got: %v", err)
	}
}

func TestKill_HappyPath(t *testing.T) {
	deps, runner, tmpDir := newTestKillDeps(t)

	// Track writeFile calls to verify sentinel path.
	var writtenPaths []string
	deps.WriteFile = func(path string, data []byte, perm os.FileMode) error {
		writtenPaths = append(writtenPaths, path)
		return nil
	}

	// ListWindowPIDs: first call returns PIDs (window alive), second call returns error (window gone).
	callCount := 0
	runner.listWindowPIDsFunc = func(sessionName, windowName string) ([]int, error) {
		callCount++
		if callCount <= 1 {
			return []int{12345}, nil
		}
		return nil, errors.New("window not found")
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
	})

	err := runKill(deps, "alice", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify sentinel file was written.
	expectedSentinel := filepath.Join(tmpDir, ".sprawl", "agents", "alice.kill")
	sentinelWritten := false
	for _, p := range writtenPaths {
		if p == expectedSentinel {
			sentinelWritten = true
			break
		}
	}
	if !sentinelWritten {
		t.Errorf("expected sentinel file to be written at %s, got writes: %v", expectedSentinel, writtenPaths)
	}

	// Window disappeared during polling, so KillWindow should NOT be called.
	if runner.killWindowCalled {
		t.Error("KillWindow should not be called when window disappears gracefully")
	}

	// Verify state updated to killed.
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Status != "killed" {
		t.Errorf("status = %q, want %q", agentState.Status, "killed")
	}
}

func TestKill_GracefulTimeout_FallsBackToForce(t *testing.T) {
	deps, runner, tmpDir := newTestKillDeps(t)

	var writtenPaths []string
	deps.WriteFile = func(path string, data []byte, perm os.FileMode) error {
		writtenPaths = append(writtenPaths, path)
		return nil
	}

	var removedPaths []string
	deps.RemoveFile = func(path string) error {
		removedPaths = append(removedPaths, path)
		return nil
	}

	// ListWindowPIDs always returns success (window never goes away on its own).
	runner.pids = []int{12345}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
	})

	err := runKill(deps, "alice", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sentinel file should have been written.
	expectedSentinel := filepath.Join(tmpDir, ".sprawl", "agents", "alice.kill")
	sentinelWritten := false
	for _, p := range writtenPaths {
		if p == expectedSentinel {
			sentinelWritten = true
			break
		}
	}
	if !sentinelWritten {
		t.Errorf("expected sentinel file to be written at %s", expectedSentinel)
	}

	// After all 10 poll iterations, KillWindow should be called as fallback.
	if !runner.killWindowCalled {
		t.Error("expected KillWindow to be called after graceful timeout")
	}

	// Sentinel should be cleaned up.
	sentinelCleaned := false
	for _, p := range removedPaths {
		if p == expectedSentinel {
			sentinelCleaned = true
			break
		}
	}
	if !sentinelCleaned {
		t.Errorf("expected sentinel file to be cleaned up at %s", expectedSentinel)
	}

	// Verify state updated to killed.
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Status != "killed" {
		t.Errorf("status = %q, want %q", agentState.Status, "killed")
	}
}

func TestKill_ForceSkipsGraceful(t *testing.T) {
	deps, runner, tmpDir := newTestKillDeps(t)

	// Track writeFile calls: sentinel should NOT be written with force.
	var writtenPaths []string
	deps.WriteFile = func(path string, data []byte, perm os.FileMode) error {
		writtenPaths = append(writtenPaths, path)
		return nil
	}

	runner.pids = []int{12345}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
	})

	err := runKill(deps, "alice", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Force: no sentinel should be written.
	if len(writtenPaths) > 0 {
		t.Errorf("expected no sentinel writes with force, got: %v", writtenPaths)
	}

	// KillWindow should be called immediately.
	if !runner.killWindowCalled {
		t.Error("expected KillWindow to be called with --force")
	}
	expectedChildrenSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName)
	if runner.killWindowSession != expectedChildrenSession {
		t.Errorf("kill window session = %q, want %q", runner.killWindowSession, expectedChildrenSession)
	}
	if runner.killWindowWindow != "alice" {
		t.Errorf("kill window name = %q, want %q", runner.killWindowWindow, "alice")
	}

	// Verify state updated to killed.
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Status != "killed" {
		t.Errorf("status = %q, want %q", agentState.Status, "killed")
	}
}

func TestKill_AlreadyKilled_IsNoOp(t *testing.T) {
	deps, runner, tmpDir := newTestKillDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "killed",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
	})

	err := runKill(deps, "alice", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT have tried to kill the window.
	if runner.killWindowCalled {
		t.Error("KillWindow should not be called for already-killed agent")
	}
}

func TestKill_AgentNotFound(t *testing.T) {
	deps, _, _ := newTestKillDeps(t)

	err := runKill(deps, "nonexistent", false)
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestKill_MissingSprawlRoot(t *testing.T) {
	deps, _, _ := newTestKillDeps(t)
	deps.Getenv = func(key string) string { return "" }

	err := runKill(deps, "alice", false)
	if err == nil {
		t.Fatal("expected error for missing SPRAWL_ROOT")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should mention SPRAWL_ROOT, got: %v", err)
	}
}

func TestKill_NoProcesses_StillUpdatesState(t *testing.T) {
	deps, _, tmpDir := newTestKillDeps(t)

	// Track sentinel writes: sentinel should still be written even when window is already gone.
	var writtenPaths []string
	deps.WriteFile = func(path string, data []byte, perm os.FileMode) error {
		writtenPaths = append(writtenPaths, path)
		return nil
	}

	// Window is already gone (ListWindowPIDs returns error).
	deps.TmuxRunner.(*killMockRunner).pidsErr = errors.New("window not found")

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
	})

	err := runKill(deps, "alice", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sentinel should still be written.
	expectedSentinel := filepath.Join(tmpDir, ".sprawl", "agents", "alice.kill")
	sentinelWritten := false
	for _, p := range writtenPaths {
		if p == expectedSentinel {
			sentinelWritten = true
			break
		}
	}
	if !sentinelWritten {
		t.Errorf("expected sentinel file to be written at %s even when window is gone", expectedSentinel)
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
	deps, _, tmpDir := newTestKillDeps(t)

	// Window is gone immediately so no polling needed.
	deps.TmuxRunner.(*killMockRunner).pidsErr = errors.New("window not found")

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Type:        "engineer",
		Family:      "engineering",
		Parent:      "root",
		Prompt:      "test task",
		Branch:      "sprawl/alice",
		Worktree:    "/path/to/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Status:      "active",
		CreatedAt:   "2026-01-01T00:00:00Z",
	})

	err := runKill(deps, "alice", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all state is preserved except status.
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
	if agentState.Branch != "sprawl/alice" {
		t.Errorf("Branch = %q, want %q", agentState.Branch, "sprawl/alice")
	}
	if agentState.Status != "killed" {
		t.Errorf("Status = %q, want %q", agentState.Status, "killed")
	}

	// State file should still exist.
	_, err = os.Stat(filepath.Join(state.AgentsDir(tmpDir), "alice.json"))
	if err != nil {
		t.Errorf("state file should still exist: %v", err)
	}
}

func TestKill_SentinelFileCleanup(t *testing.T) {
	deps, runner, tmpDir := newTestKillDeps(t)

	var removedPaths []string
	deps.RemoveFile = func(path string) error {
		removedPaths = append(removedPaths, path)
		return nil
	}

	// Test graceful path: window disappears on second poll call.
	callCount := 0
	runner.listWindowPIDsFunc = func(sessionName, windowName string) ([]int, error) {
		callCount++
		if callCount <= 1 {
			return []int{12345}, nil
		}
		return nil, errors.New("window not found")
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
	})

	err := runKill(deps, "alice", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify removeFile was called with the sentinel path.
	expectedSentinel := filepath.Join(tmpDir, ".sprawl", "agents", "alice.kill")
	sentinelRemoved := false
	for _, p := range removedPaths {
		if p == expectedSentinel {
			sentinelRemoved = true
			break
		}
	}
	if !sentinelRemoved {
		t.Errorf("expected removeFile called with sentinel path %s, got: %v", expectedSentinel, removedPaths)
	}
}

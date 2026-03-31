package cmd

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/dendra/internal/state"
)

// reportMockRunner implements tmux.Runner for report tests.
type reportMockRunner struct {
	sendKeysCalled  bool
	sendKeysSession string
	sendKeysWindow  string
	sendKeysText    string
	sendKeysErr     error
}

func (m *reportMockRunner) HasSession(name string) bool { return false }
func (m *reportMockRunner) NewSession(name string, env map[string]string, shellCmd string) error {
	return nil
}
func (m *reportMockRunner) NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}
func (m *reportMockRunner) NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}
func (m *reportMockRunner) KillWindow(sessionName, windowName string) error { return nil }
func (m *reportMockRunner) ListWindowPIDs(sessionName, windowName string) ([]int, error) {
	return nil, nil
}
func (m *reportMockRunner) Attach(name string) error { return nil }

func (m *reportMockRunner) SendKeys(sessionName, windowName string, keys string) error {
	m.sendKeysCalled = true
	m.sendKeysSession = sessionName
	m.sendKeysWindow = windowName
	m.sendKeysText = keys
	return m.sendKeysErr
}

func newTestReportDeps(t *testing.T) (*reportDeps, *reportMockRunner, string) {
	t.Helper()
	tmpDir := t.TempDir()
	runner := &reportMockRunner{}

	deps := &reportDeps{
		tmuxRunner: runner,
		getenv: func(key string) string {
			switch key {
			case "DENDRA_ROOT":
				return tmpDir
			case "DENDRA_AGENT_IDENTITY":
				return "alice"
			}
			return ""
		},
		nowFunc: func() time.Time {
			return time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
		},
	}

	os.MkdirAll(state.AgentsDir(tmpDir), 0755)
	return deps, runner, tmpDir
}

func TestReportStatus_HappyPath(t *testing.T) {
	deps, runner, tmpDir := newTestReportDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Parent: "root",
		Status: "active",
	})

	err := runReport(deps, "status", "working on tests")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify state updated
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.LastReportType != "status" {
		t.Errorf("LastReportType = %q, want %q", agentState.LastReportType, "status")
	}
	if agentState.LastReportMessage != "working on tests" {
		t.Errorf("LastReportMessage = %q, want %q", agentState.LastReportMessage, "working on tests")
	}
	if agentState.LastReportAt != "2026-03-31T12:00:00Z" {
		t.Errorf("LastReportAt = %q, want %q", agentState.LastReportAt, "2026-03-31T12:00:00Z")
	}
	// Status should NOT change for "status" report type
	if agentState.Status != "active" {
		t.Errorf("Status = %q, want %q (should not change for status report)", agentState.Status, "active")
	}
	// Should NOT notify parent for status reports
	if runner.sendKeysCalled {
		t.Error("SendKeys should not be called for status reports")
	}
}

func TestReportDone_HappyPath(t *testing.T) {
	deps, runner, tmpDir := newTestReportDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Parent: "root",
		Status: "active",
	})

	err := runReport(deps, "done", "finished implementing feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify state updated
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.LastReportType != "done" {
		t.Errorf("LastReportType = %q, want %q", agentState.LastReportType, "done")
	}
	if agentState.LastReportMessage != "finished implementing feature" {
		t.Errorf("LastReportMessage = %q, want %q", agentState.LastReportMessage, "finished implementing feature")
	}
	if agentState.Status != "done" {
		t.Errorf("Status = %q, want %q", agentState.Status, "done")
	}

	// Should notify parent via tmux SendKeys
	if !runner.sendKeysCalled {
		t.Fatal("expected SendKeys to be called for done report")
	}
	if runner.sendKeysSession != "dendra-root" {
		t.Errorf("sendKeysSession = %q, want %q", runner.sendKeysSession, "dendra-root")
	}
	if runner.sendKeysWindow != "root" {
		t.Errorf("sendKeysWindow = %q, want %q", runner.sendKeysWindow, "root")
	}
	if !strings.Contains(runner.sendKeysText, "alice") {
		t.Errorf("sendKeysText should contain agent name, got: %q", runner.sendKeysText)
	}
	if !strings.Contains(runner.sendKeysText, "done") {
		t.Errorf("sendKeysText should contain 'done', got: %q", runner.sendKeysText)
	}
	if !strings.Contains(runner.sendKeysText, "finished implementing feature") {
		t.Errorf("sendKeysText should contain message, got: %q", runner.sendKeysText)
	}
}

func TestReportProblem_HappyPath(t *testing.T) {
	deps, runner, tmpDir := newTestReportDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Parent: "root",
		Status: "active",
	})

	err := runReport(deps, "problem", "blocked on API access")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.LastReportType != "problem" {
		t.Errorf("LastReportType = %q, want %q", agentState.LastReportType, "problem")
	}
	if agentState.Status != "problem" {
		t.Errorf("Status = %q, want %q", agentState.Status, "problem")
	}

	// Should notify parent
	if !runner.sendKeysCalled {
		t.Fatal("expected SendKeys to be called for problem report")
	}
	if !strings.Contains(runner.sendKeysText, "problem") {
		t.Errorf("sendKeysText should contain 'problem', got: %q", runner.sendKeysText)
	}
}

func TestReportDone_NonRootParent_NoSendKeys(t *testing.T) {
	deps, runner, tmpDir := newTestReportDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Parent: "bob", // non-root parent
		Status: "active",
	})

	err := runReport(deps, "done", "task complete")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// State should still be updated
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Status != "done" {
		t.Errorf("Status = %q, want %q", agentState.Status, "done")
	}

	// Should NOT send keys for non-root parent
	if runner.sendKeysCalled {
		t.Error("SendKeys should not be called for non-root parent")
	}
}

func TestReport_MissingAgentIdentity(t *testing.T) {
	deps, _, _ := newTestReportDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_ROOT" {
			return "/tmp/test"
		}
		return ""
	}

	err := runReport(deps, "status", "test")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_AGENT_IDENTITY")
	}
	if !strings.Contains(err.Error(), "DENDRA_AGENT_IDENTITY") {
		t.Errorf("error should mention DENDRA_AGENT_IDENTITY, got: %v", err)
	}
}

func TestReport_MissingDendraRoot(t *testing.T) {
	deps, _, _ := newTestReportDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_AGENT_IDENTITY" {
			return "alice"
		}
		return ""
	}

	err := runReport(deps, "status", "test")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

func TestReport_AgentNotFound(t *testing.T) {
	deps, _, _ := newTestReportDeps(t)

	err := runReport(deps, "status", "test")
	if err == nil {
		t.Fatal("expected error for agent not found")
	}
	if !strings.Contains(err.Error(), "loading agent state") {
		t.Errorf("error should mention loading agent state, got: %v", err)
	}
}

func TestReportDone_SendKeysFailure_NonFatal(t *testing.T) {
	deps, runner, tmpDir := newTestReportDeps(t)
	runner.sendKeysErr = fmt.Errorf("tmux session not found")

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Parent: "root",
		Status: "active",
	})

	// Should NOT return error even if SendKeys fails
	err := runReport(deps, "done", "finished")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// State should still be updated
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Status != "done" {
		t.Errorf("Status = %q, want %q", agentState.Status, "done")
	}
}

func TestReportStatus_PreservesExistingFields(t *testing.T) {
	deps, _, tmpDir := newTestReportDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Type:        "engineer",
		Family:      "engineering",
		Parent:      "root",
		Prompt:      "build something",
		Branch:      "dendra/alice",
		Worktree:    "/path/to/worktree",
		TmuxSession: "dendra-root-children",
		TmuxWindow:  "alice",
		Status:      "active",
		CreatedAt:   "2026-01-01T00:00:00Z",
	})

	err := runReport(deps, "status", "halfway done")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}

	// All existing fields should be preserved
	if agentState.Type != "engineer" {
		t.Errorf("Type = %q, want %q", agentState.Type, "engineer")
	}
	if agentState.Family != "engineering" {
		t.Errorf("Family = %q, want %q", agentState.Family, "engineering")
	}
	if agentState.Branch != "dendra/alice" {
		t.Errorf("Branch = %q, want %q", agentState.Branch, "dendra/alice")
	}
	if agentState.Worktree != "/path/to/worktree" {
		t.Errorf("Worktree = %q, want %q", agentState.Worktree, "/path/to/worktree")
	}
	if agentState.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("CreatedAt = %q, want %q", agentState.CreatedAt, "2026-01-01T00:00:00Z")
	}
}

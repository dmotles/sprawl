package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
)

// newTestSpawnSubagentDeps creates test fixtures for spawn subagent tests.
// It pre-saves a parent agent state so the subagent can read it.
func newTestSpawnSubagentDeps(t *testing.T) (*spawnSubagentDeps, *spawnMockRunner, string) {
	t.Helper()
	tmpDir := t.TempDir()

	runner := &spawnMockRunner{}

	// Pre-save a parent agent state
	parentWorktree := filepath.Join(tmpDir, ".dendra", "worktrees", "root")
	parentState := &state.AgentState{
		Name:     "root",
		Type:     "engineer",
		Family:   "engineering",
		Worktree: parentWorktree,
		Branch:   "dendra/root",
		Status:   "active",
	}
	if err := state.SaveAgent(tmpDir, parentState); err != nil {
		t.Fatalf("saving parent state: %v", err)
	}

	// Persist namespace and root name for subagent to read as fallback
	state.WriteNamespace(tmpDir, tmux.DefaultNamespace)
	state.WriteRootName(tmpDir, tmux.DefaultRootName)

	deps := &spawnSubagentDeps{
		tmuxRunner: runner,
		getenv: func(key string) string {
			switch key {
			case "DENDRA_AGENT_IDENTITY":
				return "root"
			case "DENDRA_ROOT":
				return tmpDir
			case "DENDRA_NAMESPACE":
				return tmux.DefaultNamespace
			case "DENDRA_TREE_PATH":
				return tmux.DefaultRootName
			}
			return ""
		},
		findDendra: func() (string, error) {
			return "/usr/local/bin/dendra", nil
		},
		loadAgent: state.LoadAgent,
	}

	return deps, runner, tmpDir
}

func TestSpawnSubagent_HappyPath(t *testing.T) {
	deps, runner, tmpDir := newTestSpawnSubagentDeps(t)

	err := runSpawnSubagent(deps, "engineering", "engineer", "run tests")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have created a new session (no existing session)
	if !runner.newSessionWithWindowCalled {
		t.Error("expected NewSessionWithWindow to be called")
	}
	expectedChildrenSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName)
	if runner.newSessionWithWindowSession != expectedChildrenSession {
		t.Errorf("session = %q, want %q", runner.newSessionWithWindowSession, expectedChildrenSession)
	}

	// Window name should be the allocated agent name (first in pool)
	expectedName := agent.NamePool[0]
	if runner.newSessionWithWindowWindow != expectedName {
		t.Errorf("window = %q, want %q", runner.newSessionWithWindowWindow, expectedName)
	}

	// Verify env vars passed to tmux
	if runner.newSessionWithWindowEnv["DENDRA_AGENT_IDENTITY"] != expectedName {
		t.Errorf("env DENDRA_AGENT_IDENTITY = %q, want %q", runner.newSessionWithWindowEnv["DENDRA_AGENT_IDENTITY"], expectedName)
	}
	if runner.newSessionWithWindowEnv["DENDRA_ROOT"] != tmpDir {
		t.Errorf("env DENDRA_ROOT = %q, want %q", runner.newSessionWithWindowEnv["DENDRA_ROOT"], tmpDir)
	}

	// Verify state was saved
	agentState, err := state.LoadAgent(tmpDir, expectedName)
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if !agentState.Subagent {
		t.Error("state Subagent should be true")
	}
	if agentState.Type != "engineer" {
		t.Errorf("state Type = %q, want %q", agentState.Type, "engineer")
	}
	if agentState.Family != "engineering" {
		t.Errorf("state Family = %q, want %q", agentState.Family, "engineering")
	}
	if agentState.Parent != "root" {
		t.Errorf("state Parent = %q, want %q", agentState.Parent, "root")
	}
	if agentState.Prompt != "run tests" {
		t.Errorf("state Prompt = %q, want %q", agentState.Prompt, "run tests")
	}
	if agentState.Status != "active" {
		t.Errorf("state Status = %q, want %q", agentState.Status, "active")
	}
	// Should use parent's worktree, not a new one
	expectedWorktree := filepath.Join(tmpDir, ".dendra", "worktrees", "root")
	if agentState.Worktree != expectedWorktree {
		t.Errorf("state Worktree = %q, want parent worktree %q", agentState.Worktree, expectedWorktree)
	}
	if agentState.Branch != "dendra/root" {
		t.Errorf("state Branch = %q, want parent branch %q", agentState.Branch, "dendra/root")
	}
	// SessionID should be a valid UUID
	if len(agentState.SessionID) != 36 || agentState.SessionID[8] != '-' {
		t.Errorf("state SessionID = %q, want valid UUID format", agentState.SessionID)
	}
}

func TestSpawnSubagent_SecondChild_AddsWindow(t *testing.T) {
	deps, runner, _ := newTestSpawnSubagentDeps(t)

	// First spawn creates session
	err := runSpawnSubagent(deps, "engineering", "engineer", "task 1")
	if err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	if !runner.newSessionWithWindowCalled {
		t.Error("expected NewSessionWithWindow for first child")
	}

	// Now the session exists
	runner.hasSession = true
	runner.newSessionWithWindowCalled = false

	// Second spawn should add a window
	err = runSpawnSubagent(deps, "engineering", "engineer", "task 2")
	if err != nil {
		t.Fatalf("second spawn: %v", err)
	}
	if runner.newSessionWithWindowCalled {
		t.Error("NewSessionWithWindow should not be called for second child")
	}
	if !runner.newWindowCalled {
		t.Error("expected NewWindow for second child")
	}

	// Verify second child got a different name
	secondName := agent.NamePool[1]
	if runner.newWindowWindow != secondName {
		t.Errorf("second window = %q, want %q", runner.newWindowWindow, secondName)
	}
}

func TestSpawnSubagent_MissingIdentity(t *testing.T) {
	deps, _, _ := newTestSpawnSubagentDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_ROOT" {
			return "/tmp/test"
		}
		return ""
	}

	err := runSpawnSubagent(deps, "engineering", "engineer", "task")
	if err == nil {
		t.Fatal("expected error for missing identity")
	}
	if !strings.Contains(err.Error(), "DENDRA_AGENT_IDENTITY") {
		t.Errorf("error should mention DENDRA_AGENT_IDENTITY, got: %v", err)
	}
}

func TestSpawnSubagent_MissingDendraRoot(t *testing.T) {
	deps, _, _ := newTestSpawnSubagentDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_AGENT_IDENTITY" {
			return "root"
		}
		return ""
	}

	err := runSpawnSubagent(deps, "engineering", "engineer", "task")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

func TestSpawnSubagent_ParentNotFound(t *testing.T) {
	deps, _, _ := newTestSpawnSubagentDeps(t)
	deps.getenv = func(key string) string {
		switch key {
		case "DENDRA_AGENT_IDENTITY":
			return "nonexistent"
		case "DENDRA_ROOT":
			return "/tmp/test"
		}
		return ""
	}
	deps.loadAgent = func(dendraRoot, name string) (*state.AgentState, error) {
		return nil, fmt.Errorf("reading agent state for %q: file not found", name)
	}

	err := runSpawnSubagent(deps, "engineering", "engineer", "task")
	if err == nil {
		t.Fatal("expected error for missing parent")
	}
	if !strings.Contains(err.Error(), "parent") {
		t.Errorf("error should mention parent, got: %v", err)
	}
}

func TestSpawnSubagent_InvalidType(t *testing.T) {
	deps, _, _ := newTestSpawnSubagentDeps(t)

	err := runSpawnSubagent(deps, "engineering", "foo", "task")
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
	if !strings.Contains(err.Error(), "invalid agent type") {
		t.Errorf("error should mention 'invalid agent type', got: %v", err)
	}
}

func TestSpawnSubagent_UnsupportedType(t *testing.T) {
	deps, _, _ := newTestSpawnSubagentDeps(t)

	err := runSpawnSubagent(deps, "engineering", "manager", "task")
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
	if !strings.Contains(err.Error(), "not yet supported") {
		t.Errorf("error should mention 'not yet supported', got: %v", err)
	}
}

func TestSpawnSubagent_InvalidFamily(t *testing.T) {
	deps, _, _ := newTestSpawnSubagentDeps(t)

	err := runSpawnSubagent(deps, "foo", "engineer", "task")
	if err == nil {
		t.Fatal("expected error for invalid family")
	}
	if !strings.Contains(err.Error(), "invalid agent family") {
		t.Errorf("error should mention 'invalid agent family', got: %v", err)
	}
}

func TestSpawnSubagent_TmuxFails(t *testing.T) {
	deps, runner, _ := newTestSpawnSubagentDeps(t)
	runner.newSessionWithWindowErr = errors.New("tmux exploded")

	err := runSpawnSubagent(deps, "engineering", "engineer", "task")
	if err == nil {
		t.Fatal("expected error for tmux failure")
	}
	if !strings.Contains(err.Error(), "tmux") {
		t.Errorf("error should mention tmux, got: %v", err)
	}
}

func TestSpawnSubagent_FindDendraFails(t *testing.T) {
	deps, runner, _ := newTestSpawnSubagentDeps(t)
	deps.findDendra = func() (string, error) {
		return "", errors.New("dendra binary not found")
	}

	err := runSpawnSubagent(deps, "engineering", "engineer", "task")
	if err == nil {
		t.Fatal("expected error when findDendra fails")
	}
	if !strings.Contains(err.Error(), "dendra") {
		t.Errorf("error should mention dendra, got: %v", err)
	}

	// Tmux should not have been called
	if runner.newSessionWithWindowCalled || runner.newWindowCalled {
		t.Error("tmux should not be called when findDendra fails")
	}
}

func TestSpawnSubagent_NamePoolExhausted(t *testing.T) {
	deps, _, tmpDir := newTestSpawnSubagentDeps(t)

	// Fill all names
	agentsDir := state.AgentsDir(tmpDir)
	for _, name := range agent.NamePool {
		os.WriteFile(filepath.Join(agentsDir, name+".json"), []byte("{}"), 0644)
	}

	err := runSpawnSubagent(deps, "engineering", "engineer", "task")
	if err == nil {
		t.Fatal("expected error for exhausted name pool")
	}
	if !strings.Contains(err.Error(), "no more agents") {
		t.Errorf("error should mention name exhaustion, got: %v", err)
	}
}

func TestSpawnSubagent_ShellCmd_UsesParentWorktree(t *testing.T) {
	deps, runner, tmpDir := newTestSpawnSubagentDeps(t)

	err := runSpawnSubagent(deps, "engineering", "engineer", "run tests")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd := runner.newSessionWithWindowCmd
	expectedName := agent.NamePool[0]
	parentWorktree := filepath.Join(tmpDir, ".dendra", "worktrees", "root")

	// Should use parent's worktree in cd command
	expectedCmd := "cd " + tmux.ShellQuote(parentWorktree) + " && " +
		tmux.BuildShellCmd("/usr/local/bin/dendra", []string{"agent-loop", expectedName})

	if cmd != expectedCmd {
		t.Errorf("shell command mismatch\n  got:  %s\n  want: %s", cmd, expectedCmd)
	}

	// Should NOT contain 'claude'
	if strings.Contains(cmd, "claude") {
		t.Error("shell command should NOT contain 'claude'")
	}
}

func TestSpawnSubagentCmd_Registered(t *testing.T) {
	found := false
	for _, sub := range spawnCmd.Commands() {
		if sub.Name() == "subagent" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'subagent' to be a subcommand of 'spawn', but it was not found")
	}
}

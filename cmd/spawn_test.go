package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
)

// spawnMockRunner implements tmux.Runner for spawn tests.
type spawnMockRunner struct {
	hasSession              bool
	newSessionWithWindowErr error
	newWindowErr            error

	// Recorded calls
	newSessionWithWindowCalled  bool
	newSessionWithWindowSession string
	newSessionWithWindowWindow  string
	newSessionWithWindowEnv     map[string]string
	newSessionWithWindowCmd     string

	newWindowCalled  bool
	newWindowSession string
	newWindowWindow  string
	newWindowEnv     map[string]string
	newWindowCmd     string
}

func (m *spawnMockRunner) HasWindow(string, string) bool { return false }

func (m *spawnMockRunner) HasSession(name string) bool {
	return m.hasSession
}

func (m *spawnMockRunner) NewSession(name string, env map[string]string, shellCmd string) error {
	return nil
}

func (m *spawnMockRunner) NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	m.newSessionWithWindowCalled = true
	m.newSessionWithWindowSession = sessionName
	m.newSessionWithWindowWindow = windowName
	m.newSessionWithWindowEnv = env
	m.newSessionWithWindowCmd = shellCmd
	return m.newSessionWithWindowErr
}

func (m *spawnMockRunner) NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	m.newWindowCalled = true
	m.newWindowSession = sessionName
	m.newWindowWindow = windowName
	m.newWindowEnv = env
	m.newWindowCmd = shellCmd
	return m.newWindowErr
}

func (m *spawnMockRunner) KillWindow(sessionName, windowName string) error {
	return nil
}

func (m *spawnMockRunner) ListWindowPIDs(sessionName, windowName string) ([]int, error) {
	return nil, nil
}

func (m *spawnMockRunner) SendKeys(sessionName, windowName string, keys string) error {
	return nil
}

func (m *spawnMockRunner) ListSessionNames() ([]string, error) { return nil, nil }

func (m *spawnMockRunner) Attach(name string) error {
	return nil
}

// mockWorktreeCreator implements worktree.Creator for testing.
type mockWorktreeCreator struct {
	worktreePath string
	branchName   string
	err          error
	calledWith   struct {
		repoRoot   string
		agentName  string
		branchName string
		baseBranch string
	}
}

func (m *mockWorktreeCreator) Create(repoRoot, agentName, branchName, baseBranch string) (string, string, error) {
	m.calledWith.repoRoot = repoRoot
	m.calledWith.agentName = agentName
	m.calledWith.branchName = branchName
	m.calledWith.baseBranch = baseBranch
	if m.err != nil {
		return "", "", m.err
	}
	path := m.worktreePath
	branch := m.branchName
	if path == "" {
		path = filepath.Join(repoRoot, ".dendra", "worktrees", agentName)
	}
	if branch == "" {
		branch = branchName
	}
	return path, branch, nil
}

func newTestSpawnDeps(t *testing.T) (*spawnDeps, *spawnMockRunner, *mockWorktreeCreator, string) {
	t.Helper()
	tmpDir := t.TempDir()

	runner := &spawnMockRunner{}
	creator := &mockWorktreeCreator{}

	// Persist namespace and root name for spawn to read as fallback
	state.WriteNamespace(tmpDir, tmux.DefaultNamespace)
	state.WriteRootName(tmpDir, tmux.DefaultRootName)

	deps := &spawnDeps{
		tmuxRunner:      runner,
		worktreeCreator: creator,
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
		currentBranch: func(repoRoot string) (string, error) {
			return "main", nil
		},
		findDendra: func() (string, error) {
			return "/usr/local/bin/dendra", nil
		},
	}

	// Ensure agents dir exists
	os.MkdirAll(state.AgentsDir(tmpDir), 0755)

	return deps, runner, creator, tmpDir
}

func TestSpawn_HappyPath(t *testing.T) {
	deps, runner, creator, tmpDir := newTestSpawnDeps(t)

	err := runSpawn(deps, "engineering", "engineer", "implement login page", "feature/login")
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
	if runner.newSessionWithWindowEnv["DENDRA_AGENT_IDENTITY"] != expectedName {
		t.Errorf("env DENDRA_AGENT_IDENTITY = %q, want %q", runner.newSessionWithWindowEnv["DENDRA_AGENT_IDENTITY"], expectedName)
	}
	if runner.newSessionWithWindowEnv["DENDRA_ROOT"] != tmpDir {
		t.Errorf("env DENDRA_ROOT = %q, want %q", runner.newSessionWithWindowEnv["DENDRA_ROOT"], tmpDir)
	}

	// Verify worktree creator was called
	if creator.calledWith.agentName != expectedName {
		t.Errorf("worktree agentName = %q, want %q", creator.calledWith.agentName, expectedName)
	}
	if creator.calledWith.branchName != "feature/login" {
		t.Errorf("worktree branchName = %q, want %q", creator.calledWith.branchName, "feature/login")
	}
	if creator.calledWith.baseBranch != "main" {
		t.Errorf("worktree baseBranch = %q, want %q", creator.calledWith.baseBranch, "main")
	}

	// Verify state was saved
	agentState, err := state.LoadAgent(tmpDir, expectedName)
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
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
	if agentState.Status != "active" {
		t.Errorf("state Status = %q, want %q", agentState.Status, "active")
	}
	if agentState.Branch != "feature/login" {
		t.Errorf("state Branch = %q, want %q", agentState.Branch, "feature/login")
	}
	// SessionID should be a valid UUID (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx)
	if len(agentState.SessionID) != 36 || agentState.SessionID[8] != '-' || agentState.SessionID[13] != '-' || agentState.SessionID[18] != '-' || agentState.SessionID[23] != '-' {
		t.Errorf("state SessionID = %q, want valid UUID format", agentState.SessionID)
	}
}

func TestSpawn_SecondChild_AddsWindow(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	// First spawn creates session
	err := runSpawn(deps, "engineering", "engineer", "task 1", "feature/task1")
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
	err = runSpawn(deps, "engineering", "engineer", "task 2", "feature/task2")
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

	// Verify both state files exist
	agents, err := state.ListAgents(tmpDir)
	if err != nil {
		t.Fatalf("listing agents: %v", err)
	}
	if len(agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(agents))
	}
}

func TestSpawn_MissingIdentity(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_ROOT" {
			return "/tmp/test"
		}
		return ""
	}

	err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for missing identity")
	}
	if !strings.Contains(err.Error(), "DENDRA_AGENT_IDENTITY") {
		t.Errorf("error should mention DENDRA_AGENT_IDENTITY, got: %v", err)
	}
}

func TestSpawn_MissingDendraRoot(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_AGENT_IDENTITY" {
			return "root"
		}
		return ""
	}

	err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

func TestSpawn_NamePoolExhausted(t *testing.T) {
	deps, _, _, tmpDir := newTestSpawnDeps(t)

	// Fill all names
	agentsDir := state.AgentsDir(tmpDir)
	for _, name := range agent.NamePool {
		os.WriteFile(filepath.Join(agentsDir, name+".json"), []byte("{}"), 0644)
	}

	err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for exhausted name pool")
	}
	if !strings.Contains(err.Error(), "no more agents") {
		t.Errorf("error should mention name exhaustion, got: %v", err)
	}
}

func TestSpawn_WorktreeCreationFails(t *testing.T) {
	deps, runner, creator, _ := newTestSpawnDeps(t)
	creator.err = errors.New("git worktree failed")

	err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for worktree failure")
	}
	if !strings.Contains(err.Error(), "worktree") {
		t.Errorf("error should mention worktree, got: %v", err)
	}
	// Tmux should not have been called
	if runner.newSessionWithWindowCalled || runner.newWindowCalled {
		t.Error("tmux should not be called when worktree creation fails")
	}
}

func TestSpawn_TmuxFails(t *testing.T) {
	deps, runner, _, _ := newTestSpawnDeps(t)
	runner.newSessionWithWindowErr = errors.New("tmux exploded")

	err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for tmux failure")
	}
	if !strings.Contains(err.Error(), "tmux") {
		t.Errorf("error should mention tmux, got: %v", err)
	}
}

func TestSpawn_UnsupportedType(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)

	err := runSpawn(deps, "engineering", "tester", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
	if !strings.Contains(err.Error(), "not yet supported") {
		t.Errorf("error should mention 'not yet supported', got: %v", err)
	}
}

func TestSpawn_InvalidType(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)

	err := runSpawn(deps, "engineering", "foo", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
	if !strings.Contains(err.Error(), "invalid agent type") {
		t.Errorf("error should mention 'invalid agent type', got: %v", err)
	}
}

func TestSpawn_ResearcherType_HappyPath(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	err := runSpawn(deps, "engineering", "researcher", "investigate auth libraries", "feature/research")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !runner.newSessionWithWindowCalled {
		t.Error("expected NewSessionWithWindow to be called")
	}

	// Verify state was saved with researcher type
	expectedName := agent.NamePool[0]
	agentState, err := state.LoadAgent(tmpDir, expectedName)
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Type != "researcher" {
		t.Errorf("state Type = %q, want %q", agentState.Type, "researcher")
	}
}

func TestSpawn_ManagerType_HappyPath(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	err := runSpawn(deps, "engineering", "manager", "coordinate feature work", "feature/manage")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !runner.newSessionWithWindowCalled {
		t.Error("expected NewSessionWithWindow to be called")
	}

	// Verify state was saved with manager type
	expectedName := agent.NamePool[0]
	agentState, err := state.LoadAgent(tmpDir, expectedName)
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Type != "manager" {
		t.Errorf("state Type = %q, want %q", agentState.Type, "manager")
	}
}

func TestSpawn_ManagerInSupportedTypes(t *testing.T) {
	if !supportedTypes["manager"] {
		t.Error("manager should be in supportedTypes")
	}
}

func TestSpawn_ResearcherInSupportedTypes(t *testing.T) {
	if !supportedTypes["researcher"] {
		t.Error("researcher should be in supportedTypes")
	}
}

func TestSpawn_InvalidFamily(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)

	err := runSpawn(deps, "foo", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for invalid family")
	}
	if !strings.Contains(err.Error(), "invalid agent family") {
		t.Errorf("error should mention 'invalid agent family', got: %v", err)
	}
}

// TestSpawn_ShellCmd_ContainsDendraAgentLoop verifies the exact shape of the
// shell command passed to tmux: cd '<worktree>' && '<dendra>' 'agent-loop' '<name>'
// Also verifies the command does NOT reference claude directly.
func TestSpawn_ShellCmd_ContainsDendraAgentLoop(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	err := runSpawn(deps, "engineering", "engineer", "build login page", "feature/login")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd := runner.newSessionWithWindowCmd
	expectedName := agent.NamePool[0]
	expectedWorktree := filepath.Join(tmpDir, ".dendra", "worktrees", expectedName)

	// Verify the shell command structure:
	// cd '<worktree>' && '<dendra_path>' 'agent-loop' '<name>'
	expectedCmd := "cd " + tmux.ShellQuote(expectedWorktree) + " && " +
		tmux.BuildShellCmd("/usr/local/bin/dendra", []string{"agent-loop", expectedName})

	if cmd != expectedCmd {
		t.Errorf("shell command mismatch\n  got:  %s\n  want: %s", cmd, expectedCmd)
	}

	// The shell command should NOT contain 'claude' -- we launch via dendra agent-loop now.
	if strings.Contains(cmd, "claude") {
		t.Error("shell command should NOT contain 'claude'; spawn now launches via dendra agent-loop")
	}
}

// TestSpawnAgentCmd_Registered verifies that spawnAgentCmd is registered as a
// child of spawnCmd. After the refactor, `dendra spawn agent` should be a valid
// subcommand.
func TestSpawnAgentCmd_Registered(t *testing.T) {
	found := false
	for _, sub := range spawnCmd.Commands() {
		if sub.Name() == "agent" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'agent' to be a subcommand of 'spawn', but it was not found")
	}
}

// TestSpawn_FindDendraFails verifies that when findDendra returns an error,
// runSpawn propagates it.
func TestSpawn_FindDendraFails(t *testing.T) {
	deps, runner, _, _ := newTestSpawnDeps(t)
	deps.findDendra = func() (string, error) {
		return "", errors.New("dendra binary not found")
	}

	err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
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

// TestSpawn_WritesInitialPromptFile verifies that spawning an agent writes the
// initial prompt to .dendra/agents/<name>/prompts/initial.md.
func TestSpawn_WritesInitialPromptFile(t *testing.T) {
	deps, _, _, tmpDir := newTestSpawnDeps(t)

	err := runSpawn(deps, "engineering", "engineer", "implement the login page", "feature/login")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedName := agent.NamePool[0]
	promptPath := filepath.Join(tmpDir, ".dendra", "agents", expectedName, "prompts", "initial.md")

	data, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("reading initial prompt file: %v", err)
	}
	if string(data) != "implement the login page" {
		t.Errorf("prompt file content = %q, want %q", string(data), "implement the login page")
	}
}

// TestSpawn_AgentPromptContainsFileRef verifies that the agent state's Prompt
// field contains an @file reference to initial.md, not the raw prompt text.
func TestSpawn_AgentPromptContainsFileRef(t *testing.T) {
	deps, _, _, tmpDir := newTestSpawnDeps(t)

	err := runSpawn(deps, "engineering", "engineer", "implement the login page", "feature/login")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedName := agent.NamePool[0]
	agentState, err := state.LoadAgent(tmpDir, expectedName)
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}

	expectedPromptPath := filepath.Join(tmpDir, ".dendra", "agents", expectedName, "prompts", "initial.md")

	// Should contain @file reference
	if !strings.Contains(agentState.Prompt, "@"+expectedPromptPath) {
		t.Errorf("agent Prompt should contain @file reference, got: %q", agentState.Prompt)
	}

	// Should NOT contain the raw prompt text
	if strings.Contains(agentState.Prompt, "implement the login page") {
		t.Errorf("agent Prompt should not contain raw prompt text, got: %q", agentState.Prompt)
	}
}

// TestSpawn_BareSpawnCmd_HasRunE verifies that `dendra spawn` (without the
// "agent" subcommand) has a RunE handler for backward compatibility.
// Both `dendra spawn --flags...` and `dendra spawn agent --flags...` should work.
func TestSpawn_BareSpawnCmd_HasRunE(t *testing.T) {
	// spawnCmd must have RunE so bare 'dendra spawn --family ...' still works
	if spawnCmd.RunE == nil {
		t.Fatal("spawnCmd.RunE should be set for backward-compatible bare 'dendra spawn' usage")
	}

	// Flags should be persistent (inherited by subcommands)
	familyFlag := spawnCmd.PersistentFlags().Lookup("family")
	if familyFlag == nil {
		t.Error("expected 'family' to be a persistent flag on spawnCmd")
	}
	typeFlag := spawnCmd.PersistentFlags().Lookup("type")
	if typeFlag == nil {
		t.Error("expected 'type' to be a persistent flag on spawnCmd")
	}
	promptFlag := spawnCmd.PersistentFlags().Lookup("prompt")
	if promptFlag == nil {
		t.Error("expected 'prompt' to be a persistent flag on spawnCmd")
	}
}

func TestSpawn_BranchFlagRequired(t *testing.T) {
	branchFlag := spawnCmd.PersistentFlags().Lookup("branch")
	if branchFlag == nil {
		t.Fatal("expected 'branch' to be a persistent flag on spawnCmd")
	}
}

func TestSpawn_EmptyBranch(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)
	err := runSpawn(deps, "engineering", "engineer", "task", "")
	if err == nil {
		t.Fatal("expected error for empty branch")
	}
	if !strings.Contains(err.Error(), "branch") {
		t.Errorf("error should mention branch, got: %v", err)
	}
}

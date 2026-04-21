package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
)

// spawnMockRunner implements tmux.Runner for spawn tests.
type spawnMockRunner struct {
	hasSession              bool
	newSessionWithWindowErr error
	newWindowErr            error

	// Per-call errors for NewWindow (supports retry testing).
	// If set, newWindowErrs[callIndex] is used instead of newWindowErr.
	newWindowErrs []error

	// Recorded calls
	newSessionWithWindowCalled  bool
	newSessionWithWindowSession string
	newSessionWithWindowWindow  string
	newSessionWithWindowEnv     map[string]string
	newSessionWithWindowCmd     string

	newWindowCalled    bool
	newWindowCallCount int
	newWindowSession   string
	newWindowWindow    string
	newWindowEnv       map[string]string
	newWindowCmd       string

	sourceFileCalled  bool
	sourceFileSession string
	sourceFilePath    string

	// callLog records the order of tmux operations for ordering verification.
	callLog []string

	// onNewWindow is called when NewWindow is invoked (before error checking).
	// Useful for verifying preconditions like state file existence.
	onNewWindow func(sessionName, windowName string)

	// onNewSessionWithWindow is called when NewSessionWithWindow is invoked.
	onNewSessionWithWindow func(sessionName, windowName string)
}

func (m *spawnMockRunner) HasWindow(string, string) bool { return false }

func (m *spawnMockRunner) HasSession(name string) bool {
	return m.hasSession
}

func (m *spawnMockRunner) NewSession(name string, env map[string]string, shellCmd string) error {
	return nil
}

func (m *spawnMockRunner) NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	m.callLog = append(m.callLog, "NewSessionWithWindow")
	if m.onNewSessionWithWindow != nil {
		m.onNewSessionWithWindow(sessionName, windowName)
	}
	m.newSessionWithWindowCalled = true
	m.newSessionWithWindowSession = sessionName
	m.newSessionWithWindowWindow = windowName
	m.newSessionWithWindowEnv = env
	m.newSessionWithWindowCmd = shellCmd
	return m.newSessionWithWindowErr
}

func (m *spawnMockRunner) NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	m.callLog = append(m.callLog, "NewWindow")
	if m.onNewWindow != nil {
		m.onNewWindow(sessionName, windowName)
	}
	m.newWindowCalled = true
	idx := m.newWindowCallCount
	m.newWindowCallCount++
	m.newWindowSession = sessionName
	m.newWindowWindow = windowName
	m.newWindowEnv = env
	m.newWindowCmd = shellCmd
	if idx < len(m.newWindowErrs) {
		return m.newWindowErrs[idx]
	}
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

func (m *spawnMockRunner) SourceFile(sessionName, filePath string) error {
	m.sourceFileCalled = true
	m.sourceFileSession = sessionName
	m.sourceFilePath = filePath
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
		path = filepath.Join(repoRoot, ".sprawl", "worktrees", agentName)
	}
	if branch == "" {
		branch = branchName
	}
	return path, branch, nil
}

func newTestSpawnDeps(t *testing.T) (*spawnDeps, *spawnMockRunner, *mockWorktreeCreator, string) {
	t.Helper()
	tmpDir := t.TempDir()

	runner := &spawnMockRunner{
		// Default: no tmux session exists, so NewWindow fails and
		// NewSessionWithWindow is used as fallback (matches first-spawn scenario).
		newWindowErr: errors.New("no session"),
	}
	creator := &mockWorktreeCreator{}

	// Persist namespace and root name for spawn to read as fallback
	state.WriteNamespace(tmpDir, tmux.DefaultNamespace)
	state.WriteRootName(tmpDir, tmux.DefaultRootName)

	deps := &spawnDeps{
		TmuxRunner:      runner,
		WorktreeCreator: creator,
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_AGENT_IDENTITY":
				return "root"
			case "SPRAWL_ROOT":
				return tmpDir
			case "SPRAWL_NAMESPACE":
				return tmux.DefaultNamespace
			case "SPRAWL_TREE_PATH":
				return tmux.DefaultRootName
			}
			return ""
		},
		CurrentBranch: func(repoRoot string) (string, error) {
			return "main", nil
		},
		FindSprawl: func() (string, error) {
			return "/usr/local/bin/sprawl", nil
		},
		NewSpawnLock: func(lockPath string) (func() error, func() error) {
			return func() error { return nil }, func() error { return nil }
		},
		LoadConfig:     func(string) (*config.Config, error) { return &config.Config{}, nil },
		RunScript:      func(string, string, map[string]string) ([]byte, error) { return nil, nil },
		WorktreeRemove: func(string, string, bool) error { return nil },
	}

	// Ensure agents dir exists
	os.MkdirAll(state.AgentsDir(tmpDir), 0o755)

	return deps, runner, creator, tmpDir
}

func TestSpawn_HappyPath(t *testing.T) {
	deps, runner, creator, tmpDir := newTestSpawnDeps(t)

	_, err := runSpawn(deps, "engineering", "engineer", "implement login page", "feature/login")
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
	// Window name should be the allocated agent name (first in engineer pool)
	expectedName := agent.EngineerNames[0]
	if runner.newSessionWithWindowWindow != expectedName {
		t.Errorf("window = %q, want %q", runner.newSessionWithWindowWindow, expectedName)
	}
	if runner.newSessionWithWindowEnv["SPRAWL_AGENT_IDENTITY"] != expectedName {
		t.Errorf("env SPRAWL_AGENT_IDENTITY = %q, want %q", runner.newSessionWithWindowEnv["SPRAWL_AGENT_IDENTITY"], expectedName)
	}
	if runner.newSessionWithWindowEnv["SPRAWL_ROOT"] != tmpDir {
		t.Errorf("env SPRAWL_ROOT = %q, want %q", runner.newSessionWithWindowEnv["SPRAWL_ROOT"], tmpDir)
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

	// First spawn creates session (NewWindow fails, falls back to NewSessionWithWindow)
	_, err := runSpawn(deps, "engineering", "engineer", "task 1", "feature/task1")
	if err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	if !runner.newSessionWithWindowCalled {
		t.Error("expected NewSessionWithWindow for first child")
	}

	// Now the session exists — NewWindow will succeed
	runner.newWindowErr = nil
	runner.newSessionWithWindowCalled = false

	// Second spawn should add a window (NewWindow succeeds directly)
	_, err = runSpawn(deps, "engineering", "engineer", "task 2", "feature/task2")
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
	secondName := agent.EngineerNames[1]
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
	deps.Getenv = func(key string) string {
		if key == "SPRAWL_ROOT" {
			return "/tmp/test"
		}
		return ""
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for missing identity")
	}
	if !strings.Contains(err.Error(), "SPRAWL_AGENT_IDENTITY") {
		t.Errorf("error should mention SPRAWL_AGENT_IDENTITY, got: %v", err)
	}
}

func TestSpawn_MissingSprawlRoot(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)
	deps.Getenv = func(key string) string {
		if key == "SPRAWL_AGENT_IDENTITY" {
			return "root"
		}
		return ""
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for missing SPRAWL_ROOT")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should mention SPRAWL_ROOT, got: %v", err)
	}
}

func TestSpawn_NamePoolExhausted_UsesFallback(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	// Fill all engineer names
	agentsDir := state.AgentsDir(tmpDir)
	for _, name := range agent.EngineerNames {
		os.WriteFile(filepath.Join(agentsDir, name+".json"), []byte("{}"), 0o644)
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v (should fall back to numeric names)", err)
	}

	// Should have allocated a fallback name like "runner-1"
	if runner.newSessionWithWindowWindow != "runner-1" {
		t.Errorf("window = %q, want %q", runner.newSessionWithWindowWindow, "runner-1")
	}
}

func TestSpawn_WorktreeCreationFails(t *testing.T) {
	deps, runner, creator, _ := newTestSpawnDeps(t)
	creator.err = errors.New("git worktree failed")

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
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
	// All tmux attempts fail: NewWindow (default err) -> NewSessionWithWindow -> NewWindow retry
	runner.newSessionWithWindowErr = errors.New("tmux exploded")

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for tmux failure")
	}
	if !strings.Contains(err.Error(), "tmux") {
		t.Errorf("error should mention tmux, got: %v", err)
	}
}

func TestSpawn_UnsupportedType(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)

	_, err := runSpawn(deps, "engineering", "tester", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
	if !strings.Contains(err.Error(), "not yet supported") {
		t.Errorf("error should mention 'not yet supported', got: %v", err)
	}
}

func TestSpawn_InvalidType(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)

	_, err := runSpawn(deps, "engineering", "foo", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
	if !strings.Contains(err.Error(), "invalid agent type") {
		t.Errorf("error should mention 'invalid agent type', got: %v", err)
	}
}

func TestSpawn_ResearcherType_HappyPath(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	_, err := runSpawn(deps, "engineering", "researcher", "investigate auth libraries", "feature/research")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !runner.newSessionWithWindowCalled {
		t.Error("expected NewSessionWithWindow to be called")
	}

	// Verify state was saved with researcher type
	expectedName := agent.ResearcherNames[0]
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

	_, err := runSpawn(deps, "engineering", "manager", "coordinate feature work", "feature/manage")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !runner.newSessionWithWindowCalled {
		t.Error("expected NewSessionWithWindow to be called")
	}

	// Verify state was saved with manager type
	expectedName := agent.ManagerNames[0]
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

	_, err := runSpawn(deps, "foo", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for invalid family")
	}
	if !strings.Contains(err.Error(), "invalid agent family") {
		t.Errorf("error should mention 'invalid agent family', got: %v", err)
	}
}

// TestSpawn_ShellCmd_ContainsSprawlAgentLoop verifies the exact shape of the
// shell command passed to tmux: cd '<worktree>' && '<sprawl>' 'agent-loop' '<name>'
// Also verifies the command does NOT reference claude directly.
func TestSpawn_ShellCmd_ContainsSprawlAgentLoop(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	_, err := runSpawn(deps, "engineering", "engineer", "build login page", "feature/login")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd := runner.newSessionWithWindowCmd
	expectedName := agent.EngineerNames[0]
	expectedWorktree := filepath.Join(tmpDir, ".sprawl", "worktrees", expectedName)

	// Verify the shell command structure:
	// cd '<worktree>' && '<sprawl_path>' 'agent-loop' '<name>'
	expectedCmd := "cd " + tmux.ShellQuote(expectedWorktree) + " && " +
		tmux.BuildShellCmd("/usr/local/bin/sprawl", []string{"agent-loop", expectedName})

	if cmd != expectedCmd {
		t.Errorf("shell command mismatch\n  got:  %s\n  want: %s", cmd, expectedCmd)
	}

	// The shell command should NOT contain 'claude' -- we launch via sprawl agent-loop now.
	if strings.Contains(cmd, "claude") {
		t.Error("shell command should NOT contain 'claude'; spawn now launches via sprawl agent-loop")
	}
}

// TestSpawnAgentCmd_Registered verifies that spawnAgentCmd is registered as a
// child of spawnCmd. After the refactor, `sprawl spawn agent` should be a valid
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

// TestSpawn_FindSprawlFails verifies that when findSprawl returns an error,
// runSpawn propagates it.
func TestSpawn_FindSprawlFails(t *testing.T) {
	deps, runner, _, _ := newTestSpawnDeps(t)
	deps.FindSprawl = func() (string, error) {
		return "", errors.New("sprawl binary not found")
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error when findSprawl fails")
	}
	if !strings.Contains(err.Error(), "sprawl") {
		t.Errorf("error should mention sprawl, got: %v", err)
	}

	// Tmux should not have been called
	if runner.newSessionWithWindowCalled || runner.newWindowCalled {
		t.Error("tmux should not be called when findSprawl fails")
	}
}

// TestSpawn_WritesInitialPromptFile verifies that spawning an agent writes the
// initial prompt to .sprawl/agents/<name>/prompts/initial.md.
func TestSpawn_WritesInitialPromptFile(t *testing.T) {
	deps, _, _, tmpDir := newTestSpawnDeps(t)

	_, err := runSpawn(deps, "engineering", "engineer", "implement the login page", "feature/login")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedName := agent.EngineerNames[0]
	promptPath := filepath.Join(tmpDir, ".sprawl", "agents", expectedName, "prompts", "initial.md")

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

	_, err := runSpawn(deps, "engineering", "engineer", "implement the login page", "feature/login")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedName := agent.EngineerNames[0]
	agentState, err := state.LoadAgent(tmpDir, expectedName)
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}

	expectedPromptPath := filepath.Join(tmpDir, ".sprawl", "agents", expectedName, "prompts", "initial.md")

	// Should contain @file reference
	if !strings.Contains(agentState.Prompt, "@"+expectedPromptPath) {
		t.Errorf("agent Prompt should contain @file reference, got: %q", agentState.Prompt)
	}

	// Should NOT contain the raw prompt text
	if strings.Contains(agentState.Prompt, "implement the login page") {
		t.Errorf("agent Prompt should not contain raw prompt text, got: %q", agentState.Prompt)
	}
}

// TestSpawn_BareSpawnCmd_HasRunE verifies that `sprawl spawn` (without the
// "agent" subcommand) has a RunE handler for backward compatibility.
// Both `sprawl spawn --flags...` and `sprawl spawn agent --flags...` should work.
func TestSpawn_BareSpawnCmd_HasRunE(t *testing.T) {
	// spawnCmd must have RunE so bare 'sprawl spawn --family ...' still works
	if spawnCmd.RunE == nil {
		t.Fatal("spawnCmd.RunE should be set for backward-compatible bare 'sprawl spawn' usage")
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
	// --branch lives on `spawn agent` (not on `spawn` persistently), so that
	// `spawn subagent` does not inherit the requirement. See QUM-276.
	branchFlag := spawnAgentCmd.Flags().Lookup("branch")
	if branchFlag == nil {
		t.Fatal("expected 'branch' to be a local flag on spawnAgentCmd")
	}
	if spawnCmd.PersistentFlags().Lookup("branch") != nil {
		t.Error("'branch' must NOT be a persistent flag on spawnCmd (subagent would inherit it)")
	}
}

func TestSpawn_SprawlBinPropagated(t *testing.T) {
	deps, runner, _, _ := newTestSpawnDeps(t)
	originalGetenv := deps.Getenv
	deps.Getenv = func(key string) string {
		if key == "SPRAWL_BIN" {
			return "/custom/sprawl"
		}
		return originalGetenv(key)
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := runner.newSessionWithWindowEnv
	if env["SPRAWL_BIN"] != "/custom/sprawl" {
		t.Errorf("env SPRAWL_BIN = %q, want %q", env["SPRAWL_BIN"], "/custom/sprawl")
	}
}

func TestSpawn_SprawlBinNotPropagatedWhenUnset(t *testing.T) {
	deps, runner, _, _ := newTestSpawnDeps(t)
	// Default getenv returns "" for SPRAWL_BIN

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := runner.newSessionWithWindowEnv
	if _, ok := env["SPRAWL_BIN"]; ok {
		t.Errorf("env should not contain SPRAWL_BIN when unset, got %q", env["SPRAWL_BIN"])
	}
}

func TestSpawn_EmptyBranch(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)
	_, err := runSpawn(deps, "engineering", "engineer", "task", "")
	if err == nil {
		t.Fatal("expected error for empty branch")
	}
	if !strings.Contains(err.Error(), "branch") {
		t.Errorf("error should mention branch, got: %v", err)
	}
}

func TestSpawn_SprawlTestModePropagated(t *testing.T) {
	deps, runner, _, _ := newTestSpawnDeps(t)
	originalGetenv := deps.Getenv
	deps.Getenv = func(key string) string {
		if key == "SPRAWL_TEST_MODE" {
			return "1"
		}
		return originalGetenv(key)
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("runSpawn error: %v", err)
	}

	env := runner.newSessionWithWindowEnv
	if env["SPRAWL_TEST_MODE"] != "1" {
		t.Errorf("env SPRAWL_TEST_MODE = %q, want %q", env["SPRAWL_TEST_MODE"], "1")
	}
}

func TestSpawn_SprawlTestModeNotPropagatedWhenUnset(t *testing.T) {
	deps, runner, _, _ := newTestSpawnDeps(t)
	// Default getenv returns "" for SPRAWL_TEST_MODE

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("runSpawn error: %v", err)
	}

	env := runner.newSessionWithWindowEnv
	if _, ok := env["SPRAWL_TEST_MODE"]; ok {
		t.Errorf("env should not contain SPRAWL_TEST_MODE when unset, got %q", env["SPRAWL_TEST_MODE"])
	}
}

// TestSpawn_CrossTypeIsolation verifies that spawning agents of different types
// assigns names from their respective pools, not from a shared pool.
func TestSpawn_CrossTypeIsolation(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	// Spawn an engineer (NewWindow fails, falls back to NewSessionWithWindow)
	_, err := runSpawn(deps, "engineering", "engineer", "build feature", "feature/eng")
	if err != nil {
		t.Fatalf("engineer spawn: %v", err)
	}
	engineerName := runner.newSessionWithWindowWindow

	// Session now exists — NewWindow will succeed
	runner.newWindowErr = nil
	runner.newSessionWithWindowCalled = false

	// Spawn a researcher
	_, err = runSpawn(deps, "engineering", "researcher", "investigate auth", "feature/research")
	if err != nil {
		t.Fatalf("researcher spawn: %v", err)
	}
	researcherName := runner.newWindowWindow

	// Reset for manager
	runner.newWindowCalled = false

	// Spawn a manager
	_, err = runSpawn(deps, "engineering", "manager", "coordinate work", "feature/manage")
	if err != nil {
		t.Fatalf("manager spawn: %v", err)
	}
	managerName := runner.newWindowWindow

	// Verify each name comes from its respective pool
	if engineerName != agent.EngineerNames[0] {
		t.Errorf("engineer name = %q, want %q (from EngineerNames)", engineerName, agent.EngineerNames[0])
	}
	if researcherName != agent.ResearcherNames[0] {
		t.Errorf("researcher name = %q, want %q (from ResearcherNames)", researcherName, agent.ResearcherNames[0])
	}
	if managerName != agent.ManagerNames[0] {
		t.Errorf("manager name = %q, want %q (from ManagerNames)", managerName, agent.ManagerNames[0])
	}

	// Verify all three names are distinct
	if engineerName == researcherName || engineerName == managerName || researcherName == managerName {
		t.Errorf("names should be distinct: engineer=%q, researcher=%q, manager=%q", engineerName, researcherName, managerName)
	}

	// Verify state files have correct types
	agents, err := state.ListAgents(tmpDir)
	if err != nil {
		t.Fatalf("listing agents: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(agents))
	}
}

func TestSpawn_ResearcherPoolExhausted_UsesFallback(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	// Fill all researcher names
	agentsDir := state.AgentsDir(tmpDir)
	for _, name := range agent.ResearcherNames {
		os.WriteFile(filepath.Join(agentsDir, name+".json"), []byte("{}"), 0o644)
	}

	_, err := runSpawn(deps, "engineering", "researcher", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v (should fall back to numeric names)", err)
	}

	if runner.newSessionWithWindowWindow != "decker-1" {
		t.Errorf("window = %q, want %q", runner.newSessionWithWindowWindow, "decker-1")
	}
}

func TestSpawn_ManagerPoolExhausted_UsesFallback(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	// Fill all manager names
	agentsDir := state.AgentsDir(tmpDir)
	for _, name := range agent.ManagerNames {
		os.WriteFile(filepath.Join(agentsDir, name+".json"), []byte("{}"), 0o644)
	}

	_, err := runSpawn(deps, "engineering", "manager", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v (should fall back to numeric names)", err)
	}

	if runner.newSessionWithWindowWindow != "fixer-1" {
		t.Errorf("window = %q, want %q", runner.newSessionWithWindowWindow, "fixer-1")
	}
}

// TestSpawn_TmuxNewWindow_FallsBackToNewSession verifies that when NewWindow
// fails (session doesn't exist), runSpawn falls back to NewSessionWithWindow.
func TestSpawn_TmuxNewWindow_FallsBackToNewSession(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	// NewWindow fails on first call (no session yet), succeeds after
	runner.newWindowErrs = []error{errors.New("session not found")}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// NewWindow was tried first, then NewSessionWithWindow succeeded
	if runner.newWindowCallCount != 1 {
		t.Errorf("newWindowCallCount = %d, want 1", runner.newWindowCallCount)
	}
	if !runner.newSessionWithWindowCalled {
		t.Error("expected NewSessionWithWindow to be called as fallback")
	}

	// Agent state should still be saved
	expectedName := agent.EngineerNames[0]
	_, err = state.LoadAgent(tmpDir, expectedName)
	if err != nil {
		t.Fatalf("agent state not saved: %v", err)
	}
}

// TestSpawn_TmuxBothPathsFail_RetryNewWindowSucceeds verifies the full retry
// chain: NewWindow fails -> NewSessionWithWindow fails -> NewWindow retry succeeds.
func TestSpawn_TmuxBothPathsFail_RetryNewWindowSucceeds(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	// First NewWindow fails, then succeeds on second try
	runner.newWindowErrs = []error{errors.New("session not found"), nil}
	runner.newSessionWithWindowErr = errors.New("duplicate session")

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if runner.newWindowCallCount != 2 {
		t.Errorf("newWindowCallCount = %d, want 2", runner.newWindowCallCount)
	}
	if !runner.newSessionWithWindowCalled {
		t.Error("expected NewSessionWithWindow to be called")
	}

	// Agent state should still be saved
	expectedName := agent.EngineerNames[0]
	_, err = state.LoadAgent(tmpDir, expectedName)
	if err != nil {
		t.Fatalf("agent state not saved: %v", err)
	}
}

// TestSpawn_TmuxAllAttemptsExhausted_ReturnsError verifies that when all three
// tmux attempts fail, runSpawn returns an error.
func TestSpawn_TmuxAllAttemptsExhausted_ReturnsError(t *testing.T) {
	deps, runner, _, _ := newTestSpawnDeps(t)

	// All attempts fail
	runner.newWindowErrs = []error{
		errors.New("session not found"),
		errors.New("session not found again"),
	}
	runner.newSessionWithWindowErr = errors.New("duplicate session")

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error when all tmux attempts fail")
	}
	if !strings.Contains(err.Error(), "tmux") {
		t.Errorf("error should mention tmux, got: %v", err)
	}

	if runner.newWindowCallCount != 2 {
		t.Errorf("newWindowCallCount = %d, want 2", runner.newWindowCallCount)
	}
}

// TestSpawn_SpawnLockAcquiredAndReleased verifies the spawn lock is acquired
// and released during a successful spawn.
func TestSpawn_SpawnLockAcquiredAndReleased(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)

	acquireCount := 0
	releaseCount := 0
	deps.NewSpawnLock = func(lockPath string) (func() error, func() error) {
		return func() error {
				acquireCount++
				return nil
			}, func() error {
				releaseCount++
				return nil
			}
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if acquireCount != 1 {
		t.Errorf("acquireCount = %d, want 1", acquireCount)
	}
	if releaseCount != 1 {
		t.Errorf("releaseCount = %d, want 1", releaseCount)
	}
}

// TestSpawn_SpawnLockReleasedOnError verifies the spawn lock is released even
// when spawn fails partway through (e.g., worktree creation fails).
func TestSpawn_SpawnLockReleasedOnError(t *testing.T) {
	deps, _, creator, _ := newTestSpawnDeps(t)
	creator.err = errors.New("git worktree failed")

	acquireCount := 0
	releaseCount := 0
	deps.NewSpawnLock = func(lockPath string) (func() error, func() error) {
		return func() error {
				acquireCount++
				return nil
			}, func() error {
				releaseCount++
				return nil
			}
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error for worktree failure")
	}

	if acquireCount != 1 {
		t.Errorf("acquireCount = %d, want 1", acquireCount)
	}
	if releaseCount != 1 {
		t.Errorf("releaseCount = %d, want 1 (lock should be released even on error)", releaseCount)
	}
}

// TestSpawn_SpawnLockAcquireFailure verifies that when the lock cannot be
// acquired, runSpawn returns an error without proceeding.
func TestSpawn_SpawnLockAcquireFailure(t *testing.T) {
	deps, runner, _, _ := newTestSpawnDeps(t)

	deps.NewSpawnLock = func(lockPath string) (func() error, func() error) {
		return func() error {
				return errors.New("lock held by another process")
			}, func() error {
				return nil
			}
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error when lock acquisition fails")
	}
	if !strings.Contains(err.Error(), "spawn lock") {
		t.Errorf("error should mention spawn lock, got: %v", err)
	}

	// Nothing else should have been called
	if runner.newSessionWithWindowCalled || runner.newWindowCalled {
		t.Error("tmux should not be called when lock acquisition fails")
	}
}

func TestSpawn_SetupScript_Runs(t *testing.T) {
	deps, _, _, tmpDir := newTestSpawnDeps(t)

	// Configure a setup script
	setupScript := "npm install"
	cfg := &config.Config{}
	cfg.Set("worktree.setup", setupScript)

	deps.LoadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}

	var scriptCalled bool
	var gotScript, gotWorkDir string
	var gotEnv map[string]string
	deps.RunScript = func(script, workDir string, env map[string]string) ([]byte, error) {
		scriptCalled = true
		gotScript = script
		gotWorkDir = workDir
		gotEnv = env
		return []byte("ok"), nil
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !scriptCalled {
		t.Fatal("expected runScript to be called when worktree.setup is configured")
	}
	if gotScript != setupScript {
		t.Errorf("script = %q, want %q", gotScript, setupScript)
	}

	expectedName := agent.EngineerNames[0]
	expectedWorktree := filepath.Join(tmpDir, ".sprawl", "worktrees", expectedName)
	if gotWorkDir != expectedWorktree {
		t.Errorf("workDir = %q, want %q", gotWorkDir, expectedWorktree)
	}

	if gotEnv["SPRAWL_AGENT_IDENTITY"] == "" {
		t.Error("env should contain SPRAWL_AGENT_IDENTITY")
	}
	if gotEnv["SPRAWL_ROOT"] != tmpDir {
		t.Errorf("env SPRAWL_ROOT = %q, want %q", gotEnv["SPRAWL_ROOT"], tmpDir)
	}
}

func TestSpawn_SetupScript_Failure_CleansUpWorktree(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)

	cfg := &config.Config{}
	cfg.Set("worktree.setup", "npm install")

	deps.LoadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}
	deps.RunScript = func(string, string, map[string]string) ([]byte, error) {
		return []byte("ERR"), errors.New("script failed")
	}

	var worktreeRemoved bool
	deps.WorktreeRemove = func(repoRoot, worktreePath string, force bool) error {
		worktreeRemoved = true
		return nil
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error when setup script fails")
	}
	if !strings.Contains(err.Error(), "setup script failed") {
		t.Errorf("error should contain 'setup script failed', got: %v", err)
	}
	if !strings.Contains(err.Error(), "Escalate") {
		t.Errorf("error should contain 'Escalate', got: %v", err)
	}
	if !worktreeRemoved {
		t.Error("expected worktreeRemove to be called on setup script failure")
	}
}

func TestSpawn_SetupScript_NotConfigured_Skipped(t *testing.T) {
	deps, _, _, _ := newTestSpawnDeps(t)

	// Default empty config (no worktree.setup)
	var scriptCalled bool
	deps.RunScript = func(string, string, map[string]string) ([]byte, error) {
		scriptCalled = true
		return nil, nil
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if scriptCalled {
		t.Error("runScript should NOT be called when worktree.setup is not configured")
	}
}

func TestSpawn_SourcesTmuxConfigWhenExists(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	// Create the tmux.conf file that init would have generated
	sprawlDir := filepath.Join(tmpDir, ".sprawl")
	confPath := filepath.Join(sprawlDir, "tmux.conf")
	if err := os.WriteFile(confPath, []byte("# sprawl tmux config\n"), 0o644); err != nil {
		t.Fatalf("writing tmux.conf: %v", err)
	}

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !runner.sourceFileCalled {
		t.Error("expected SourceFile to be called when tmux.conf exists")
	}
	if runner.sourceFilePath != confPath {
		t.Errorf("SourceFile path = %q, want %q", runner.sourceFilePath, confPath)
	}
}

func TestSpawn_SkipsSourceFileWhenNoConfig(t *testing.T) {
	deps, runner, _, _ := newTestSpawnDeps(t)

	// Don't create tmux.conf — simulate init not having been run yet

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if runner.sourceFileCalled {
		t.Error("expected SourceFile NOT to be called when tmux.conf doesn't exist")
	}
}

// TestSpawn_StateFileExistsBeforeTmux verifies that the agent state file is
// written BEFORE the tmux session/window is created (QUM-190 fix).
func TestSpawn_StateFileExistsBeforeTmux(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	expectedName := agent.EngineerNames[0]
	var stateExistedAtTmuxCall bool

	// Hook into tmux calls to check if state file exists at that point
	checkState := func(_, _ string) {
		_, err := state.LoadAgent(tmpDir, expectedName)
		stateExistedAtTmuxCall = err == nil
	}
	runner.onNewWindow = checkState
	runner.onNewSessionWithWindow = checkState

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !stateExistedAtTmuxCall {
		t.Error("agent state file should exist BEFORE tmux session/window is created")
	}
}

// TestSpawn_TmuxFailure_CleansUpStateFile verifies that when all tmux attempts
// fail, the already-written state file is cleaned up (QUM-190).
func TestSpawn_TmuxFailure_CleansUpStateFile(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	// All tmux attempts fail
	runner.newWindowErrs = []error{
		errors.New("session not found"),
		errors.New("session not found again"),
	}
	runner.newSessionWithWindowErr = errors.New("session creation failed")

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error when all tmux attempts fail")
	}

	// State file should have been cleaned up
	expectedName := agent.EngineerNames[0]
	_, loadErr := state.LoadAgent(tmpDir, expectedName)
	if loadErr == nil {
		t.Error("agent state file should be cleaned up after tmux failure")
	}
}

// TestSpawn_TmuxFailure_CleansUpPromptFile verifies that when all tmux attempts
// fail, the prompt file is also cleaned up.
func TestSpawn_TmuxFailure_CleansUpPromptFile(t *testing.T) {
	deps, runner, _, tmpDir := newTestSpawnDeps(t)

	// All tmux attempts fail
	runner.newWindowErrs = []error{
		errors.New("session not found"),
		errors.New("session not found again"),
	}
	runner.newSessionWithWindowErr = errors.New("session creation failed")

	_, err := runSpawn(deps, "engineering", "engineer", "task", "feature/x")
	if err == nil {
		t.Fatal("expected error when all tmux attempts fail")
	}

	// Prompt file should have been cleaned up
	expectedName := agent.EngineerNames[0]
	promptPath := filepath.Join(tmpDir, ".sprawl", "agents", expectedName, "prompts", "initial.md")
	if _, statErr := os.Stat(promptPath); statErr == nil {
		t.Error("prompt file should be cleaned up after tmux failure")
	}
}

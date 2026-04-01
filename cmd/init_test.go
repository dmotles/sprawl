package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
)

// mockRunner implements tmux.Runner for testing.
type mockRunner struct {
	hasSession            bool
	newSessionErr         error
	newSessionWithWinErr  error
	attachErr             error
	newSessionName        string
	newSessionEnv         map[string]string
	newSessionCmd         string
	newSessionWithWinName string
	newSessionWithWinWin  string
	newSessionWithWinEnv  map[string]string
	newSessionWithWinCmd  string
	attachCalled          bool
	attachName            string
}

func (m *mockRunner) HasSession(name string) bool {
	return m.hasSession
}

func (m *mockRunner) NewSession(name string, env map[string]string, shellCmd string) error {
	m.newSessionName = name
	m.newSessionEnv = env
	m.newSessionCmd = shellCmd
	return m.newSessionErr
}

func (m *mockRunner) NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	m.newSessionWithWinName = sessionName
	m.newSessionWithWinWin = windowName
	m.newSessionWithWinEnv = env
	m.newSessionWithWinCmd = shellCmd
	return m.newSessionWithWinErr
}

func (m *mockRunner) NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}

func (m *mockRunner) KillWindow(sessionName, windowName string) error {
	return nil
}

func (m *mockRunner) ListWindowPIDs(sessionName, windowName string) ([]int, error) {
	return nil, nil
}

func (m *mockRunner) ListSessionNames() ([]string, error) { return nil, nil }

func (m *mockRunner) SendKeys(sessionName, windowName string, keys string) error {
	return nil
}

func (m *mockRunner) Attach(name string) error {
	m.attachCalled = true
	m.attachName = name
	return m.attachErr
}

// mockLauncher implements agent.Launcher for testing.
type mockLauncher struct {
	binary    string
	binaryErr error
	args      []string
}

func (m *mockLauncher) FindBinary() (string, error) {
	return m.binary, m.binaryErr
}

func (m *mockLauncher) BuildArgs(opts agent.LaunchOpts) []string {
	return m.args
}

func defaultGetenv(key string) string {
	return ""
}

func TestRunInit_ExistingSession_Attaches(t *testing.T) {
	runner := &mockRunner{hasSession: true}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, getenv: defaultGetenv}
	err := runInit(deps, tmux.DefaultRootName, tmux.DefaultNamespace)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !runner.attachCalled {
		t.Error("expected Attach to be called")
	}
	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace, tmux.DefaultRootName)
	if runner.attachName != expectedSession {
		t.Errorf("attached to %q, want %q", runner.attachName, expectedSession)
	}
	if runner.newSessionWithWinName != "" {
		t.Error("NewSessionWithWindow should not have been called")
	}
}

func TestRunInit_NoSession_CreatesAndAttaches(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{
		binary: "/usr/bin/claude",
		args:   []string{"--name", tmux.RootSessionName(tmux.DefaultNamespace, tmux.DefaultRootName)},
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, getenv: defaultGetenv}
	// Override Getwd by using a known cwd
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultRootName, tmux.DefaultNamespace)

	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace, tmux.DefaultRootName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.newSessionWithWinName != expectedSession {
		t.Errorf("NewSessionWithWindow session = %q, want %q", runner.newSessionWithWinName, expectedSession)
	}
	if runner.newSessionWithWinWin != tmux.RootWindowName {
		t.Errorf("NewSessionWithWindow window = %q, want %q", runner.newSessionWithWinWin, tmux.RootWindowName)
	}
	if runner.newSessionWithWinEnv["DENDRA_AGENT_IDENTITY"] != tmux.DefaultRootName {
		t.Errorf("DENDRA_AGENT_IDENTITY = %q, want %q", runner.newSessionWithWinEnv["DENDRA_AGENT_IDENTITY"], tmux.DefaultRootName)
	}
	if runner.newSessionWithWinEnv["DENDRA_NAMESPACE"] != tmux.DefaultNamespace {
		t.Errorf("DENDRA_NAMESPACE = %q, want %q", runner.newSessionWithWinEnv["DENDRA_NAMESPACE"], tmux.DefaultNamespace)
	}
	if runner.newSessionWithWinEnv["DENDRA_TREE_PATH"] != tmux.DefaultRootName {
		t.Errorf("DENDRA_TREE_PATH = %q, want %q", runner.newSessionWithWinEnv["DENDRA_TREE_PATH"], tmux.DefaultRootName)
	}
	if !runner.attachCalled {
		t.Error("expected Attach to be called after NewSession")
	}

	// Verify namespace was persisted
	ns := state.ReadNamespace(tmpDir)
	if ns != tmux.DefaultNamespace {
		t.Errorf("persisted namespace = %q, want %q", ns, tmux.DefaultNamespace)
	}

	// Verify root name was persisted
	rn := state.ReadRootName(tmpDir)
	if rn != tmux.DefaultRootName {
		t.Errorf("persisted root name = %q, want %q", rn, tmux.DefaultRootName)
	}
}

func TestRunInit_CustomNameAndNamespace(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{
		binary: "/usr/bin/claude",
		args:   []string{},
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, getenv: defaultGetenv}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, "kai", "🌲")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedSession := tmux.RootSessionName("🌲", "kai")
	if runner.newSessionWithWinName != expectedSession {
		t.Errorf("NewSessionWithWindow session = %q, want %q", runner.newSessionWithWinName, expectedSession)
	}
	if runner.newSessionWithWinEnv["DENDRA_NAMESPACE"] != "🌲" {
		t.Errorf("DENDRA_NAMESPACE = %q, want %q", runner.newSessionWithWinEnv["DENDRA_NAMESPACE"], "🌲")
	}
	if runner.newSessionWithWinEnv["DENDRA_TREE_PATH"] != "kai" {
		t.Errorf("DENDRA_TREE_PATH = %q, want %q", runner.newSessionWithWinEnv["DENDRA_TREE_PATH"], "kai")
	}

	// Verify persistence
	ns := state.ReadNamespace(tmpDir)
	if ns != "🌲" {
		t.Errorf("persisted namespace = %q, want %q", ns, "🌲")
	}
	rn := state.ReadRootName(tmpDir)
	if rn != "kai" {
		t.Errorf("persisted root name = %q, want %q", rn, "kai")
	}
}

func TestRunInit_AutoPickNamespace(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{
		binary: "/usr/bin/claude",
		args:   []string{},
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, getenv: defaultGetenv}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Empty namespace triggers auto-pick. With no sessions, should pick 🌳.
	err := runInit(deps, tmux.DefaultRootName, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.newSessionWithWinEnv["DENDRA_NAMESPACE"] != "🌳" {
		t.Errorf("DENDRA_NAMESPACE = %q, want %q", runner.newSessionWithWinEnv["DENDRA_NAMESPACE"], "🌳")
	}
}

func TestRunInit_NamespacePersisted(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{
		binary: "/usr/bin/claude",
		args:   []string{},
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, getenv: defaultGetenv}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, "test", "🌴")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check the file exists
	nsPath := filepath.Join(tmpDir, ".dendra", "namespace")
	data, err := os.ReadFile(nsPath)
	if err != nil {
		t.Fatalf("reading namespace file: %v", err)
	}
	if string(data) != "🌴" {
		t.Errorf("namespace file = %q, want %q", string(data), "🌴")
	}
}

func TestRunInit_NewSessionFails_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{
		hasSession:           false,
		newSessionWithWinErr: errors.New("tmux exploded"),
	}
	launcher := &mockLauncher{
		binary: "/usr/bin/claude",
		args:   []string{},
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, getenv: defaultGetenv}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultRootName, tmux.DefaultNamespace)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if runner.attachCalled {
		t.Error("Attach should not be called when NewSession fails")
	}
}

func TestRunInit_ClaudeNotFound_ReturnsError(t *testing.T) {
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{
		binaryErr: errors.New("not found"),
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, getenv: defaultGetenv}
	err := runInit(deps, tmux.DefaultRootName, tmux.DefaultNamespace)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

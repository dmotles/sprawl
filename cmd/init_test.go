package cmd

import (
	"errors"
	"testing"

	"github.com/dmotles/dendra/internal/agent"
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

func TestRunInit_ExistingSession_Attaches(t *testing.T) {
	runner := &mockRunner{hasSession: true}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher}
	err := runInit(deps)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !runner.attachCalled {
		t.Error("expected Attach to be called")
	}
	if runner.attachName != tmux.RootSessionName {
		t.Errorf("attached to %q, want %q", runner.attachName, tmux.RootSessionName)
	}
	if runner.newSessionWithWinName != "" {
		t.Error("NewSessionWithWindow should not have been called")
	}
}

func TestRunInit_NoSession_CreatesAndAttaches(t *testing.T) {
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{
		binary: "/usr/bin/claude",
		args:   []string{"--name", "dendra-root"},
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher}
	err := runInit(deps)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.newSessionWithWinName != tmux.RootSessionName {
		t.Errorf("NewSessionWithWindow session = %q, want %q", runner.newSessionWithWinName, tmux.RootSessionName)
	}
	if runner.newSessionWithWinWin != tmux.RootWindowName {
		t.Errorf("NewSessionWithWindow window = %q, want %q", runner.newSessionWithWinWin, tmux.RootWindowName)
	}
	if runner.newSessionWithWinEnv["DENDRA_AGENT_IDENTITY"] != "root" {
		t.Errorf("DENDRA_AGENT_IDENTITY = %q, want %q", runner.newSessionWithWinEnv["DENDRA_AGENT_IDENTITY"], "root")
	}
	if !runner.attachCalled {
		t.Error("expected Attach to be called after NewSession")
	}
}

func TestRunInit_NewSessionFails_ReturnsError(t *testing.T) {
	runner := &mockRunner{
		hasSession:           false,
		newSessionWithWinErr: errors.New("tmux exploded"),
	}
	launcher := &mockLauncher{
		binary: "/usr/bin/claude",
		args:   []string{},
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher}
	err := runInit(deps)

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

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher}
	err := runInit(deps)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
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

func (m *mockRunner) HasWindow(string, string) bool { return false }

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
}

func (m *mockLauncher) FindBinary() (string, error) {
	return m.binary, m.binaryErr
}

func defaultGetenv(key string) string {
	return ""
}

func TestRunInit_ExistingSession_Attaches(t *testing.T) {
	runner := &mockRunner{hasSession: true}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, findDendra: func() (string, error) { return "/usr/bin/dendra", nil }, getenv: defaultGetenv}
	err := runInit(deps, tmux.DefaultNamespace, false)
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
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, findDendra: func() (string, error) { return "/usr/bin/dendra", nil }, getenv: defaultGetenv}
	// Override Getwd by using a known cwd
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)

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

func TestRunInit_CustomNamespace(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{
		binary: "/usr/bin/claude",
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, findDendra: func() (string, error) { return "/usr/bin/dendra", nil }, getenv: defaultGetenv}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, "🌲", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Root name is always DefaultRootName ("sensei")
	expectedSession := tmux.RootSessionName("🌲", tmux.DefaultRootName)
	if runner.newSessionWithWinName != expectedSession {
		t.Errorf("NewSessionWithWindow session = %q, want %q", runner.newSessionWithWinName, expectedSession)
	}
	if runner.newSessionWithWinEnv["DENDRA_NAMESPACE"] != "🌲" {
		t.Errorf("DENDRA_NAMESPACE = %q, want %q", runner.newSessionWithWinEnv["DENDRA_NAMESPACE"], "🌲")
	}
	if runner.newSessionWithWinEnv["DENDRA_TREE_PATH"] != tmux.DefaultRootName {
		t.Errorf("DENDRA_TREE_PATH = %q, want %q", runner.newSessionWithWinEnv["DENDRA_TREE_PATH"], tmux.DefaultRootName)
	}

	// Verify persistence
	ns := state.ReadNamespace(tmpDir)
	if ns != "🌲" {
		t.Errorf("persisted namespace = %q, want %q", ns, "🌲")
	}
	rn := state.ReadRootName(tmpDir)
	if rn != tmux.DefaultRootName {
		t.Errorf("persisted root name = %q, want %q", rn, tmux.DefaultRootName)
	}
}

func TestRunInit_AutoPickNamespace(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{
		binary: "/usr/bin/claude",
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, findDendra: func() (string, error) { return "/usr/bin/dendra", nil }, getenv: defaultGetenv}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Empty namespace triggers auto-pick. With no sessions, should pick 🌳.
	err := runInit(deps, "", false)
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
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, findDendra: func() (string, error) { return "/usr/bin/dendra", nil }, getenv: defaultGetenv}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, "🌴", false)
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
	}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, findDendra: func() (string, error) { return "/usr/bin/dendra", nil }, getenv: defaultGetenv}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if runner.attachCalled {
		t.Error("Attach should not be called when NewSession fails")
	}
}

func TestRunInit_Detached_NoSession_CreatesButDoesNotAttach(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, findDendra: func() (string, error) { return "/usr/bin/dendra", nil }, getenv: defaultGetenv}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runInit(deps, tmux.DefaultNamespace, true)

	w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Session should be created
	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace, tmux.DefaultRootName)
	if runner.newSessionWithWinName != expectedSession {
		t.Errorf("NewSessionWithWindow session = %q, want %q", runner.newSessionWithWinName, expectedSession)
	}

	// Attach should NOT be called
	if runner.attachCalled {
		t.Error("Attach should not be called in detached mode")
	}

	// Output should contain useful info
	output := string(out)
	if !strings.Contains(output, "detached") {
		t.Errorf("output should contain 'detached', got: %s", output)
	}
	if !strings.Contains(output, expectedSession) {
		t.Errorf("output should contain session name %q, got: %s", expectedSession, output)
	}
	if !strings.Contains(output, "tmux attach-session -t") {
		t.Errorf("output should contain attach command, got: %s", output)
	}
}

func TestRunInit_Detached_ExistingSession_DoesNotAttach(t *testing.T) {
	runner := &mockRunner{hasSession: true}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, findDendra: func() (string, error) { return "/usr/bin/dendra", nil }, getenv: defaultGetenv}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runInit(deps, tmux.DefaultNamespace, true)

	w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Attach should NOT be called
	if runner.attachCalled {
		t.Error("Attach should not be called in detached mode")
	}

	// Output should contain session info
	output := string(out)
	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace, tmux.DefaultRootName)
	if !strings.Contains(output, expectedSession) {
		t.Errorf("output should contain session name %q, got: %s", expectedSession, output)
	}
}

func TestRunInit_NotDetached_BehaviorUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, findDendra: func() (string, error) { return "/usr/bin/dendra", nil }, getenv: defaultGetenv}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should create session AND attach
	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace, tmux.DefaultRootName)
	if runner.newSessionWithWinName != expectedSession {
		t.Errorf("NewSessionWithWindow session = %q, want %q", runner.newSessionWithWinName, expectedSession)
	}
	if !runner.attachCalled {
		t.Error("expected Attach to be called when not detached")
	}
}

func TestRunInit_DendraBinPropagated(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner:     runner,
		claudeLauncher: launcher,
		findDendra:     func() (string, error) { return "/usr/bin/dendra", nil },
		getenv: func(key string) string {
			if key == "DENDRA_BIN" {
				return "/custom/dendra"
			}
			return ""
		},
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := runner.newSessionWithWinEnv
	if env["DENDRA_BIN"] != "/custom/dendra" {
		t.Errorf("env DENDRA_BIN = %q, want %q", env["DENDRA_BIN"], "/custom/dendra")
	}
}

func TestRunInit_DendraBinNotPropagatedWhenUnset(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner:     runner,
		claudeLauncher: launcher,
		findDendra:     func() (string, error) { return "/usr/bin/dendra", nil },
		getenv:         defaultGetenv,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := runner.newSessionWithWinEnv
	if _, ok := env["DENDRA_BIN"]; ok {
		t.Errorf("env should not contain DENDRA_BIN when unset, got %q", env["DENDRA_BIN"])
	}
}

func TestRunInit_DendraTestModePropagated(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner:     runner,
		claudeLauncher: launcher,
		findDendra:     func() (string, error) { return "/usr/bin/dendra", nil },
		getenv: func(key string) string {
			if key == "DENDRA_TEST_MODE" {
				return "1"
			}
			return ""
		},
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := runner.newSessionWithWinEnv
	if env["DENDRA_TEST_MODE"] != "1" {
		t.Errorf("env DENDRA_TEST_MODE = %q, want %q", env["DENDRA_TEST_MODE"], "1")
	}
}

func TestRunInit_DendraTestModeNotPropagatedWhenUnset(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner:     runner,
		claudeLauncher: launcher,
		findDendra:     func() (string, error) { return "/usr/bin/dendra", nil },
		getenv:         defaultGetenv,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := runner.newSessionWithWinEnv
	if _, ok := env["DENDRA_TEST_MODE"]; ok {
		t.Errorf("env should not contain DENDRA_TEST_MODE when unset, got %q", env["DENDRA_TEST_MODE"])
	}
}

func TestRunInit_DendraNotFound_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{tmuxRunner: runner, claudeLauncher: launcher, findDendra: func() (string, error) { return "", errors.New("not found") }, getenv: defaultGetenv}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if runner.attachCalled {
		t.Error("Attach should not be called when findDendra fails")
	}
}

package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	sourceFileCalled      bool
	sourceFileSession     string
	sourceFilePath        string
	setEnvCalls           []setEnvCall
	setEnvAfterNewSession bool
}

type setEnvCall struct {
	Session string
	Key     string
	Value   string
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

func (m *mockRunner) SourceFile(sessionName, filePath string) error {
	m.sourceFileCalled = true
	m.sourceFileSession = sessionName
	m.sourceFilePath = filePath
	return nil
}

func (m *mockRunner) SetEnvironment(sessionName, key, value string) error {
	if m.newSessionWithWinName != "" {
		m.setEnvAfterNewSession = true
	}
	m.setEnvCalls = append(m.setEnvCalls, setEnvCall{Session: sessionName, Key: key, Value: value})
	return nil
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

// happyGitStatus returns a clean repo status (no changes).
func happyGitStatus(string) (string, error) { return "", nil }

// happyReadFile returns a .gitignore that already has .sprawl/* entries.
func happyReadFile(string) ([]byte, error) {
	return []byte(".sprawl/*\n!.sprawl/config.yaml\n"), nil
}

func TestRunInit_ExistingSession_Attaches(t *testing.T) {
	runner := &mockRunner{hasSession: true}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: nil, readFile: nil, appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !runner.attachCalled {
		t.Error("expected Attach to be called")
	}
	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace)
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

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	// Override Getwd by using a known cwd
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)

	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.newSessionWithWinName != expectedSession {
		t.Errorf("NewSessionWithWindow session = %q, want %q", runner.newSessionWithWinName, expectedSession)
	}
	if runner.newSessionWithWinWin != tmux.RootWindowName {
		t.Errorf("NewSessionWithWindow window = %q, want %q", runner.newSessionWithWinWin, tmux.RootWindowName)
	}
	if runner.newSessionWithWinEnv["SPRAWL_AGENT_IDENTITY"] != tmux.DefaultRootName {
		t.Errorf("SPRAWL_AGENT_IDENTITY = %q, want %q", runner.newSessionWithWinEnv["SPRAWL_AGENT_IDENTITY"], tmux.DefaultRootName)
	}
	if runner.newSessionWithWinEnv["SPRAWL_NAMESPACE"] != tmux.DefaultNamespace {
		t.Errorf("SPRAWL_NAMESPACE = %q, want %q", runner.newSessionWithWinEnv["SPRAWL_NAMESPACE"], tmux.DefaultNamespace)
	}
	if runner.newSessionWithWinEnv["SPRAWL_TREE_PATH"] != tmux.DefaultRootName {
		t.Errorf("SPRAWL_TREE_PATH = %q, want %q", runner.newSessionWithWinEnv["SPRAWL_TREE_PATH"], tmux.DefaultRootName)
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

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, "🌲", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Root name is always DefaultRootName ("weave")
	expectedSession := tmux.RootSessionName("🌲")
	if runner.newSessionWithWinName != expectedSession {
		t.Errorf("NewSessionWithWindow session = %q, want %q", runner.newSessionWithWinName, expectedSession)
	}
	if runner.newSessionWithWinEnv["SPRAWL_NAMESPACE"] != "🌲" {
		t.Errorf("SPRAWL_NAMESPACE = %q, want %q", runner.newSessionWithWinEnv["SPRAWL_NAMESPACE"], "🌲")
	}
	if runner.newSessionWithWinEnv["SPRAWL_TREE_PATH"] != tmux.DefaultRootName {
		t.Errorf("SPRAWL_TREE_PATH = %q, want %q", runner.newSessionWithWinEnv["SPRAWL_TREE_PATH"], tmux.DefaultRootName)
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

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Empty namespace triggers auto-pick. With no sessions, should pick ⚡.
	err := runInit(deps, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.newSessionWithWinEnv["SPRAWL_NAMESPACE"] != "⚡" {
		t.Errorf("SPRAWL_NAMESPACE = %q, want %q", runner.newSessionWithWinEnv["SPRAWL_NAMESPACE"], "⚡")
	}
}

func TestRunInit_NamespacePersisted(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{
		binary: "/usr/bin/claude",
	}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, "🌴", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check the file exists
	nsPath := filepath.Join(tmpDir, ".sprawl", "namespace")
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

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
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

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
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
	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace)
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

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: nil, readFile: nil, appendFile: nil, gitAdd: nil, gitCommit: nil,
	}

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
	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace)
	if !strings.Contains(output, expectedSession) {
		t.Errorf("output should contain session name %q, got: %s", expectedSession, output)
	}
}

func TestRunInit_NotDetached_BehaviorUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should create session AND attach
	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace)
	if runner.newSessionWithWinName != expectedSession {
		t.Errorf("NewSessionWithWindow session = %q, want %q", runner.newSessionWithWinName, expectedSession)
	}
	if !runner.attachCalled {
		t.Error("expected Attach to be called when not detached")
	}
}

func TestRunInit_SprawlBinPropagated(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner:     runner,
		claudeLauncher: launcher,
		getenv: func(key string) string {
			if key == "SPRAWL_BIN" {
				return "/custom/sprawl"
			}
			return ""
		},
		gitStatus:  happyGitStatus,
		readFile:   happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := runner.newSessionWithWinEnv
	if env["SPRAWL_BIN"] != "/custom/sprawl" {
		t.Errorf("env SPRAWL_BIN = %q, want %q", env["SPRAWL_BIN"], "/custom/sprawl")
	}
}

func TestRunInit_SprawlBinNotPropagatedWhenUnset(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner:     runner,
		claudeLauncher: launcher,
		getenv:         defaultGetenv,
		gitStatus:      happyGitStatus,
		readFile:       happyReadFile,
		appendFile:     nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := runner.newSessionWithWinEnv
	if _, ok := env["SPRAWL_BIN"]; ok {
		t.Errorf("env should not contain SPRAWL_BIN when unset, got %q", env["SPRAWL_BIN"])
	}
}

func TestRunInit_SprawlTestModePropagated(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner:     runner,
		claudeLauncher: launcher,
		getenv: func(key string) string {
			if key == "SPRAWL_TEST_MODE" {
				return "1"
			}
			return ""
		},
		gitStatus:  happyGitStatus,
		readFile:   happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := runner.newSessionWithWinEnv
	if env["SPRAWL_TEST_MODE"] != "1" {
		t.Errorf("env SPRAWL_TEST_MODE = %q, want %q", env["SPRAWL_TEST_MODE"], "1")
	}
}

func TestRunInit_SprawlTestModeNotPropagatedWhenUnset(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner:     runner,
		claudeLauncher: launcher,
		getenv:         defaultGetenv,
		gitStatus:      happyGitStatus,
		readFile:       happyReadFile,
		appendFile:     nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := runner.newSessionWithWinEnv
	if _, ok := env["SPRAWL_TEST_MODE"]; ok {
		t.Errorf("env should not contain SPRAWL_TEST_MODE when unset, got %q", env["SPRAWL_TEST_MODE"])
	}
}

func TestRunInit_DirtyRepo_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:     defaultGetenv,
		gitStatus:  func(string) (string, error) { return "M file.go", nil },
		readFile:   happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "clean repo state") {
		t.Errorf("error should contain 'clean repo state', got: %v", err)
	}
	if runner.newSessionWithWinName != "" {
		t.Error("NewSessionWithWindow should not have been called")
	}
	if runner.attachCalled {
		t.Error("Attach should not be called when repo is dirty")
	}
}

func TestRunInit_GitStatusFails_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:     defaultGetenv,
		gitStatus:  func(string) (string, error) { return "", errors.New("not a git repo") },
		readFile:   happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not a git repo") {
		t.Errorf("error should contain 'not a git repo', got: %v", err)
	}
}

func TestRunInit_NoGitignore_CreatesAndCommits(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	var mu sync.Mutex
	var appendFileCalls []struct {
		path string
		data []byte
	}
	var gitAddCalls []struct {
		dir   string
		paths []string
	}
	var gitCommitCalls []struct {
		dir     string
		message string
	}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus,
		readFile:  func(string) ([]byte, error) { return nil, os.ErrNotExist },
		appendFile: func(path string, data []byte) error {
			mu.Lock()
			defer mu.Unlock()
			appendFileCalls = append(appendFileCalls, struct {
				path string
				data []byte
			}{path, data})
			return nil
		},
		gitAdd: func(dir string, paths ...string) error {
			mu.Lock()
			defer mu.Unlock()
			gitAddCalls = append(gitAddCalls, struct {
				dir   string
				paths []string
			}{dir, paths})
			return nil
		},
		gitCommit: func(dir, message string) error {
			mu.Lock()
			defer mu.Unlock()
			gitCommitCalls = append(gitCommitCalls, struct {
				dir     string
				message string
			}{dir, message})
			return nil
		},
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify appendFile was called with correct content
	if len(appendFileCalls) != 1 {
		t.Fatalf("expected 1 appendFile call, got %d", len(appendFileCalls))
	}
	content := string(appendFileCalls[0].data)
	if !strings.Contains(content, ".sprawl/*") {
		t.Errorf("appendFile content should contain '.sprawl/*', got: %s", content)
	}
	if !strings.Contains(content, "!.sprawl/config.yaml") {
		t.Errorf("appendFile content should contain '!.sprawl/config.yaml', got: %s", content)
	}

	// Verify gitAdd was called with .gitignore
	if len(gitAddCalls) != 1 {
		t.Fatalf("expected 1 gitAdd call, got %d", len(gitAddCalls))
	}
	foundGitignore := false
	for _, p := range gitAddCalls[0].paths {
		if p == ".gitignore" {
			foundGitignore = true
		}
	}
	if !foundGitignore {
		t.Errorf("gitAdd should include '.gitignore', got: %v", gitAddCalls[0].paths)
	}

	// Verify gitCommit was called with message containing .sprawl
	if len(gitCommitCalls) != 1 {
		t.Fatalf("expected 1 gitCommit call, got %d", len(gitCommitCalls))
	}
	if !strings.Contains(gitCommitCalls[0].message, ".sprawl") {
		t.Errorf("gitCommit message should contain '.sprawl', got: %s", gitCommitCalls[0].message)
	}

	// Verify session was created
	if runner.newSessionWithWinName == "" {
		t.Error("expected NewSessionWithWindow to be called")
	}
}

func TestRunInit_GitignoreWithoutSprawl_AppendsAndCommits(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	var appendFileCalled bool
	var appendFileData []byte
	var gitAddCalled bool
	var gitCommitCalled bool

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus,
		readFile:  func(string) ([]byte, error) { return []byte("node_modules/\n"), nil },
		appendFile: func(path string, data []byte) error {
			appendFileCalled = true
			appendFileData = data
			return nil
		},
		gitAdd: func(dir string, paths ...string) error {
			gitAddCalled = true
			return nil
		},
		gitCommit: func(dir, message string) error {
			gitCommitCalled = true
			return nil
		},
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !appendFileCalled {
		t.Error("expected appendFile to be called")
	}
	content := string(appendFileData)
	if !strings.Contains(content, ".sprawl/*") {
		t.Errorf("appendFile content should contain '.sprawl/*', got: %s", content)
	}
	if !strings.Contains(content, "!.sprawl/config.yaml") {
		t.Errorf("appendFile content should contain '!.sprawl/config.yaml', got: %s", content)
	}
	if !gitAddCalled {
		t.Error("expected gitAdd to be called")
	}
	if !gitCommitCalled {
		t.Error("expected gitCommit to be called")
	}
	if runner.newSessionWithWinName == "" {
		t.Error("expected NewSessionWithWindow to be called")
	}
}

func TestRunInit_GitignoreAlreadyHasSprawl_Proceeds(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus,
		readFile:  func(string) ([]byte, error) { return []byte(".sprawl/*\n!.sprawl/config.yaml\n"), nil },
		appendFile: func(string, []byte) error {
			t.Fatal("appendFile should not be called when .sprawl/* already in .gitignore")
			return nil
		},
		gitAdd: nil,
		gitCommit: func(string, string) error {
			t.Fatal("gitCommit should not be called when .sprawl/* already in .gitignore")
			return nil
		},
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if runner.newSessionWithWinName == "" {
		t.Error("expected NewSessionWithWindow to be called")
	}
}

func TestRunInit_AppendFileFails_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:     defaultGetenv,
		gitStatus:  happyGitStatus,
		readFile:   func(string) ([]byte, error) { return nil, os.ErrNotExist },
		appendFile: func(string, []byte) error { return errors.New("permission denied") },
		gitAdd:     nil,
		gitCommit:  nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error should contain 'permission denied', got: %v", err)
	}
	if runner.newSessionWithWinName != "" {
		t.Error("NewSessionWithWindow should not have been called")
	}
}

func TestRunInit_GitAddFails_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:     defaultGetenv,
		gitStatus:  happyGitStatus,
		readFile:   func(string) ([]byte, error) { return nil, os.ErrNotExist },
		appendFile: func(string, []byte) error { return nil },
		gitAdd:     func(string, ...string) error { return errors.New("git add failed") },
		gitCommit:  nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "git add failed") {
		t.Errorf("error should contain 'git add failed', got: %v", err)
	}
	if runner.newSessionWithWinName != "" {
		t.Error("NewSessionWithWindow should not have been called")
	}
}

func TestRunInit_GitCommitFails_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:     defaultGetenv,
		gitStatus:  happyGitStatus,
		readFile:   func(string) ([]byte, error) { return nil, os.ErrNotExist },
		appendFile: func(string, []byte) error { return nil },
		gitAdd:     func(string, ...string) error { return nil },
		gitCommit:  func(string, string) error { return errors.New("git commit failed") },
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "git commit failed") {
		t.Errorf("error should contain 'git commit failed', got: %v", err)
	}
	if runner.newSessionWithWinName != "" {
		t.Error("NewSessionWithWindow should not have been called")
	}
}

func TestRunInit_ExistingSession_SkipsCleanCheck(t *testing.T) {
	runner := &mockRunner{hasSession: true}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv: defaultGetenv,
		gitStatus: func(string) (string, error) {
			t.Fatal("gitStatus should not be called when session already exists")
			return "", nil
		},
		readFile: func(string) ([]byte, error) {
			t.Fatal("readFile should not be called when session already exists")
			return nil, nil
		},
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !runner.attachCalled {
		t.Error("expected Attach to be called for existing session")
	}
}

// --- New tests for bash loop generation ---

func TestRunInit_BashLoopGenerated(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	shellCmd := runner.newSessionWithWinCmd
	// The shell command should contain _root-session (the new subcommand name)
	if !strings.Contains(shellCmd, "_root-session") {
		t.Errorf("expected shell command to contain '_root-session', got: %q", shellCmd)
	}
	// The shell command should be a bash loop (contain 'while true')
	if !strings.Contains(shellCmd, "while true") {
		t.Errorf("expected shell command to contain 'while true' (bash loop), got: %q", shellCmd)
	}
}

func TestRunInit_BashLoopUsesSprawlBinFallback(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	shellCmd := runner.newSessionWithWinCmd
	// Should use ${SPRAWL_BIN:-sprawl} pattern, not an absolute path to sprawl
	if !strings.Contains(shellCmd, "${SPRAWL_BIN:-sprawl}") {
		t.Errorf("expected shell command to use '${SPRAWL_BIN:-sprawl}' fallback pattern, got: %q", shellCmd)
	}
}

func TestRunInit_BashLoopExitCodeHandling(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	shellCmd := runner.newSessionWithWinCmd
	// Should handle exit code 42 as shutdown
	if !strings.Contains(shellCmd, "42") {
		t.Errorf("expected shell command to handle exit code 42, got: %q", shellCmd)
	}
	// Should have explicit break on exit 42
	if !strings.Contains(shellCmd, "break") {
		t.Errorf("expected shell command to contain 'break' for shutdown exit code, got: %q", shellCmd)
	}
}

func TestRunInit_BashLoopSignalTrap(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	shellCmd := runner.newSessionWithWinCmd
	// Should have a trap for TERM and INT signals
	if !strings.Contains(shellCmd, "trap") {
		t.Errorf("expected shell command to contain 'trap' for signal handling, got: %q", shellCmd)
	}
	if !strings.Contains(shellCmd, "TERM") {
		t.Errorf("expected shell command to trap TERM signal, got: %q", shellCmd)
	}
	if !strings.Contains(shellCmd, "INT") {
		t.Errorf("expected shell command to trap INT signal, got: %q", shellCmd)
	}
}

func TestRunInit_PersistsAccentColor(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	color := state.ReadAccentColor(tmpDir)
	if color == "" {
		t.Error("expected accent color to be persisted after init")
	}
}

func TestRunInit_PersistsVersion(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	version := state.ReadVersion(tmpDir)
	if version == "" {
		t.Error("expected version to be persisted after init")
	}
}

func TestRunInit_GeneratesTmuxConfigAndSources(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify tmux.conf was generated
	confPath := filepath.Join(tmpDir, ".sprawl", "tmux.conf")
	if _, err := os.Stat(confPath); os.IsNotExist(err) {
		t.Error("expected .sprawl/tmux.conf to be generated after init")
	}

	// Verify SourceFile was called
	if !runner.sourceFileCalled {
		t.Error("expected SourceFile to be called after session creation")
	}
	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace)
	if runner.sourceFileSession != expectedSession {
		t.Errorf("SourceFile session = %q, want %q", runner.sourceFileSession, expectedSession)
	}
	if runner.sourceFilePath != confPath {
		t.Errorf("SourceFile path = %q, want %q", runner.sourceFilePath, confPath)
	}
}

func TestRunInit_SetsSprawlMessagingLegacy(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The first window (weave itself) must receive SPRAWL_MESSAGING=legacy via
	// the new-session -e env — otherwise weave itself would not get it.
	if runner.newSessionWithWinEnv["SPRAWL_MESSAGING"] != "legacy" {
		t.Errorf("new-session env SPRAWL_MESSAGING = %q, want %q",
			runner.newSessionWithWinEnv["SPRAWL_MESSAGING"], "legacy")
	}

	// The session env must also be set via set-environment so child windows
	// (spawned later by `sprawl spawn`) inherit it automatically.
	expectedSession := tmux.RootSessionName(tmux.DefaultNamespace)
	found := false
	for _, c := range runner.setEnvCalls {
		if c.Session == expectedSession && c.Key == "SPRAWL_MESSAGING" && c.Value == "legacy" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected SetEnvironment(%q, SPRAWL_MESSAGING, legacy), got calls: %+v",
			expectedSession, runner.setEnvCalls)
	}

	// Ordering: SetEnvironment requires the session to exist, so it must run
	// after NewSessionWithWindow.
	if !runner.setEnvAfterNewSession {
		t.Error("SetEnvironment must be called after NewSessionWithWindow (session must exist first)")
	}
}

func TestRunInit_ExistingSession_DoesNotSetEnvironment(t *testing.T) {
	// When attaching to an existing session, we must not re-run SetEnvironment —
	// the session was configured on its original init.
	runner := &mockRunner{hasSession: true}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: nil, readFile: nil, appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runner.setEnvCalls) != 0 {
		t.Errorf("SetEnvironment should not be called when attaching to existing session, got: %+v", runner.setEnvCalls)
	}
}

func TestRunInit_ReusesExistingAccentColor(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Pre-write a specific accent color
	if err := state.WriteAccentColor(tmpDir, "colour198"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The pre-written color should be preserved, not overwritten with a random one
	got := state.ReadAccentColor(tmpDir)
	if got != "colour198" {
		t.Errorf("accent color = %q, want %q (should reuse existing)", got, "colour198")
	}
}

func TestRunInit_PicksNewColorWhenNoneSaved(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &mockRunner{hasSession: false}
	launcher := &mockLauncher{binary: "/usr/bin/claude"}

	deps := &initDeps{
		tmuxRunner: runner, claudeLauncher: launcher,
		getenv:    defaultGetenv,
		gitStatus: happyGitStatus, readFile: happyReadFile,
		appendFile: nil, gitAdd: nil, gitCommit: nil,
	}
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	err := runInit(deps, tmux.DefaultNamespace, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := state.ReadAccentColor(tmpDir)
	if got == "" {
		t.Error("expected a color to be picked and persisted when none exists")
	}
}

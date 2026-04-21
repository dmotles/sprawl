package agent

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
)

// retireTestRunner implements tmux.Runner for retire tests.
type retireTestRunner struct {
	mu                 sync.Mutex
	hasSession         bool
	killWindowCalled   bool
	killWindowErr      error
	killWindowSession  string
	killWindowWindow   string
	pids               []int
	pidsErr            error
	listWindowPIDsFunc func(sessionName, windowName string) ([]int, error)
}

func (m *retireTestRunner) HasWindow(string, string) bool { return false }
func (m *retireTestRunner) HasSession(name string) bool   { return m.hasSession }
func (m *retireTestRunner) NewSession(name string, env map[string]string, shellCmd string) error {
	return nil
}

func (m *retireTestRunner) NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}

func (m *retireTestRunner) NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}
func (m *retireTestRunner) SendKeys(sessionName, windowName string, keys string) error { return nil }
func (m *retireTestRunner) Attach(name string) error                                   { return nil }
func (m *retireTestRunner) SourceFile(string, string) error                            { return nil }
func (m *retireTestRunner) SetEnvironment(string, string, string) error                { return nil }
func (m *retireTestRunner) ListSessionNames() ([]string, error)                        { return nil, nil }

func (m *retireTestRunner) KillWindow(sessionName, windowName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killWindowCalled = true
	m.killWindowSession = sessionName
	m.killWindowWindow = windowName
	return m.killWindowErr
}

func (m *retireTestRunner) ListWindowPIDs(sessionName, windowName string) ([]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listWindowPIDsFunc != nil {
		return m.listWindowPIDsFunc(sessionName, windowName)
	}
	return m.pids, m.pidsErr
}

func newTestRetireDeps(t *testing.T) (*RetireDeps, *retireTestRunner, string) {
	t.Helper()
	tmpDir := t.TempDir()
	runner := &retireTestRunner{}

	deps := &RetireDeps{
		TmuxRunner:     runner,
		WriteFile:      func(path string, data []byte, perm os.FileMode) error { return nil },
		RemoveFile:     func(path string) error { return nil },
		SleepFunc:      func(d time.Duration) {},
		WorktreeRemove: func(repoRoot, worktreePath string, force bool) error { return nil },
		GitStatus:      func(worktreePath string) (string, error) { return "", nil },
		RemoveAll:      func(path string) error { return nil },
		ReadDir:        os.ReadDir,
		ArchiveMessage: func(sprawlRoot, agent, msgID string) error { return nil },
		Stderr:         io.Discard,
	}

	os.MkdirAll(state.AgentsDir(tmpDir), 0o755)

	return deps, runner, tmpDir
}

func createRetireTestAgent(t *testing.T, sprawlRoot string, agent *state.AgentState) {
	t.Helper()
	if err := state.SaveAgent(sprawlRoot, agent); err != nil {
		t.Fatalf("saving test agent: %v", err)
	}
}

func TestRetireAgent_HappyPath(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)

	// Window disappears immediately during graceful poll.
	runner.pidsErr = os.ErrNotExist

	agent := &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	}
	createRetireTestAgent(t, tmpDir, agent)

	err := RetireAgent(deps, tmpDir, agent, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// State file should be deleted.
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted after retire")
	}
}

func TestRetireAgent_SkipShutdown(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)

	// Track whether shutdown-related deps are exercised.
	var sentinelWritten bool
	deps.WriteFile = func(path string, data []byte, perm os.FileMode) error {
		sentinelWritten = true
		return nil
	}

	agent := &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	}
	createRetireTestAgent(t, tmpDir, agent)

	err := RetireAgent(deps, tmpDir, agent, false, true /* skipShutdown */)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With skipShutdown=true, no sentinel file should be written and no tmux calls.
	if sentinelWritten {
		t.Error("expected no sentinel file with skipShutdown=true")
	}
	if runner.killWindowCalled {
		t.Error("expected no KillWindow call with skipShutdown=true")
	}

	// State file should still be deleted (teardown still happens).
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted after retire with skipShutdown")
	}
}

func TestRetireAgent_DirtyWorktree_Refuses(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)
	deps.GitStatus = func(worktreePath string) (string, error) {
		return "M some/file.go", nil // dirty
	}

	agent := &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	}
	createRetireTestAgent(t, tmpDir, agent)

	err := RetireAgent(deps, tmpDir, agent, false, true)
	if err == nil {
		t.Fatal("expected error for dirty worktree")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("error should mention uncommitted changes, got: %v", err)
	}

	// State file should still exist (retire was refused).
	_, loadErr := state.LoadAgent(tmpDir, "alice")
	if loadErr != nil {
		t.Errorf("state file should still exist after refused retire: %v", loadErr)
	}
}

func TestRetireAgent_DirtyWorktree_ForceOverrides(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)
	deps.GitStatus = func(worktreePath string) (string, error) {
		return "M some/file.go", nil // dirty
	}

	var removedForce bool
	deps.WorktreeRemove = func(repoRoot, worktreePath string, force bool) error {
		removedForce = force
		return nil
	}

	agent := &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	}
	createRetireTestAgent(t, tmpDir, agent)

	err := RetireAgent(deps, tmpDir, agent, true /* force */, true)
	if err != nil {
		t.Fatalf("unexpected error with force=true: %v", err)
	}

	if !removedForce {
		t.Error("expected force removal of worktree")
	}

	// State should be deleted.
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted")
	}
}

func TestRetireAgent_Subagent_SkipsWorktree(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	worktreeRemoveCalled := false
	deps.WorktreeRemove = func(repoRoot, worktreePath string, force bool) error {
		worktreeRemoveCalled = true
		return nil
	}

	agent := &state.AgentState{
		Name:        "sub-alice",
		Status:      "active",
		Branch:      "sprawl/sub-alice",
		Worktree:    "/some/worktree/sub-alice",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "sub-alice",
		Parent:      "alice",
		Subagent:    true,
	}
	createRetireTestAgent(t, tmpDir, agent)

	err := RetireAgent(deps, tmpDir, agent, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Subagent worktree should NOT be removed (belongs to parent).
	if worktreeRemoveCalled {
		t.Error("worktreeRemove should NOT be called for subagent")
	}

	// State file should be deleted.
	_, err = state.LoadAgent(tmpDir, "sub-alice")
	if err == nil {
		t.Error("expected subagent state to be deleted")
	}
}

func TestRetireAgent_EmptyWorktree_SkipsRemoval(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	worktreeRemoveCalled := false
	deps.WorktreeRemove = func(repoRoot, worktreePath string, force bool) error {
		worktreeRemoveCalled = true
		return nil
	}

	agent := &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "", // no worktree
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	}
	createRetireTestAgent(t, tmpDir, agent)

	err := RetireAgent(deps, tmpDir, agent, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if worktreeRemoveCalled {
		t.Error("worktree remove should not be called when worktree is empty")
	}
}

func TestRetireAgent_WorktreeRemoveFailure_Warns(t *testing.T) {
	var stderr bytes.Buffer
	deps, _, tmpDir := newTestRetireDeps(t)
	deps.WorktreeRemove = func(repoRoot, worktreePath string, force bool) error {
		return errors.New("worktree already gone")
	}
	deps.Stderr = &stderr

	agent := &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	}
	createRetireTestAgent(t, tmpDir, agent)

	// Should succeed despite worktree removal failure.
	err := RetireAgent(deps, tmpDir, agent, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have printed a warning.
	if !strings.Contains(stderr.String(), "worktree") {
		t.Errorf("expected warning about worktree removal failure, got stderr: %q", stderr.String())
	}

	// State should still be deleted.
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted despite worktree removal failure")
	}
}

func TestRetireAgent_CleansUpLogs(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	agent := &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	}
	createRetireTestAgent(t, tmpDir, agent)

	// Create a logs directory with a fake log file.
	logsDir := filepath.Join(tmpDir, ".sprawl", "agents", "alice", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("failed to create logs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "agent.log"), []byte("log data"), 0o644); err != nil {
		t.Fatalf("failed to create log file: %v", err)
	}

	// Track what path removeAll is called with.
	var removedPath string
	deps.RemoveAll = func(path string) error {
		removedPath = path
		return os.RemoveAll(path)
	}

	err := RetireAgent(deps, tmpDir, agent, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify removeAll was called with the correct logs directory.
	if removedPath != logsDir {
		t.Errorf("removeAll called with %q, want %q", removedPath, logsDir)
	}

	// Verify the logs directory was actually removed.
	if _, err := os.Stat(logsDir); !os.IsNotExist(err) {
		t.Errorf("logs directory should have been removed, but still exists")
	}
}

func TestRetireAgent_ArchivesMessages(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	agent := &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	}
	createRetireTestAgent(t, tmpDir, agent)

	// Create message files in new/ and cur/
	msgsDir := filepath.Join(tmpDir, ".sprawl", "messages", "alice")
	for _, sub := range []string{"new", "cur", "sent", "archive"} {
		os.MkdirAll(filepath.Join(msgsDir, sub), 0o755)
	}
	os.WriteFile(filepath.Join(msgsDir, "new", "msg1.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(msgsDir, "cur", "msg2.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(msgsDir, "sent", "msg3.json"), []byte(`{}`), 0o644)

	var archived []string
	deps.ArchiveMessage = func(sprawlRoot, agentName, msgID string) error {
		archived = append(archived, msgID)
		return nil
	}

	err := RetireAgent(deps, tmpDir, agent, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have archived msg1 (from new/) and msg2 (from cur/) but NOT msg3 (sent/).
	if len(archived) != 2 {
		t.Fatalf("expected 2 archived messages, got %d: %v", len(archived), archived)
	}
	sort.Strings(archived)
	if archived[0] != "msg1" || archived[1] != "msg2" {
		t.Errorf("expected archived [msg1 msg2], got %v", archived)
	}
}

func TestRetireAgent_ArchiveMessages_NoMessages(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	agent := &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	}
	createRetireTestAgent(t, tmpDir, agent)

	// No message directories exist at all — should be a no-op.
	archiveCalled := false
	deps.ArchiveMessage = func(sprawlRoot, agentName, msgID string) error {
		archiveCalled = true
		return nil
	}

	err := RetireAgent(deps, tmpDir, agent, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if archiveCalled {
		t.Error("archive should not be called when no messages exist")
	}
}

func TestRetireAgent_ArchiveMessages_FailureIsWarning(t *testing.T) {
	var stderr bytes.Buffer
	deps, _, tmpDir := newTestRetireDeps(t)
	deps.Stderr = &stderr

	agent := &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	}
	createRetireTestAgent(t, tmpDir, agent)

	// Create a message in new/
	msgsDir := filepath.Join(tmpDir, ".sprawl", "messages", "alice")
	os.MkdirAll(filepath.Join(msgsDir, "new"), 0o755)
	os.WriteFile(filepath.Join(msgsDir, "new", "msg1.json"), []byte(`{}`), 0o644)

	deps.ArchiveMessage = func(sprawlRoot, agentName, msgID string) error {
		return errors.New("archive failed")
	}

	// Should succeed despite archive failure (warning only).
	err := RetireAgent(deps, tmpDir, agent, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have printed a warning.
	if !strings.Contains(stderr.String(), "archive") {
		t.Errorf("expected warning about archive failure, got stderr: %q", stderr.String())
	}

	// State should still be deleted.
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted despite archive failure")
	}
}

func TestRetireAgent_ArchiveMessages_NewAgentHasEmptyInbox(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	agentName := "alice"
	agent := &state.AgentState{
		Name:        agentName,
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", agentName),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  agentName,
		Parent:      "root",
	}
	createRetireTestAgent(t, tmpDir, agent)

	// Create messages in new/ and cur/
	msgsDir := filepath.Join(tmpDir, ".sprawl", "messages", agentName)
	for _, sub := range []string{"new", "cur", "archive"} {
		os.MkdirAll(filepath.Join(msgsDir, sub), 0o755)
	}
	os.WriteFile(filepath.Join(msgsDir, "new", "msg1.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(msgsDir, "cur", "msg2.json"), []byte(`{}`), 0o644)

	// Use real archive: move files from new/cur to archive
	deps.ArchiveMessage = func(sprawlRoot, agnt, msgID string) error {
		agentDir := filepath.Join(sprawlRoot, ".sprawl", "messages", agnt)
		filename := msgID + ".json"
		dstPath := filepath.Join(agentDir, "archive", filename)
		// Try new/ first, then cur/
		src := filepath.Join(agentDir, "new", filename)
		if err := os.Rename(src, dstPath); err == nil {
			return nil
		}
		src = filepath.Join(agentDir, "cur", filename)
		return os.Rename(src, dstPath)
	}

	err := RetireAgent(deps, tmpDir, agent, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// new/ and cur/ should be empty (messages moved to archive/)
	for _, sub := range []string{"new", "cur"} {
		entries, err := os.ReadDir(filepath.Join(msgsDir, sub))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("reading %s: %v", sub, err)
		}
		if len(entries) > 0 {
			t.Errorf("%s/ should be empty after retire, has %d files", sub, len(entries))
		}
	}

	// archive/ should have the messages
	entries, err := os.ReadDir(filepath.Join(msgsDir, "archive"))
	if err != nil {
		t.Fatalf("reading archive: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("archive/ should have 2 messages, has %d", len(entries))
	}
}

func TestGracefulShutdown_Normal(t *testing.T) {
	runner := &retireTestRunner{}

	// Window disappears on second poll.
	callCount := 0
	runner.listWindowPIDsFunc = func(sessionName, windowName string) ([]int, error) {
		callCount++
		if callCount <= 1 {
			return []int{12345}, nil
		}
		return nil, errors.New("window not found")
	}

	var writtenPaths []string
	var removedPaths []string

	sd := &ShutdownDeps{
		TmuxRunner: runner,
		WriteFile: func(path string, data []byte, perm os.FileMode) error {
			writtenPaths = append(writtenPaths, path)
			return nil
		},
		RemoveFile: func(path string) error {
			removedPaths = append(removedPaths, path)
			return nil
		},
		SleepFunc: func(d time.Duration) {},
	}

	tmpDir := t.TempDir()
	agent := &state.AgentState{
		Name:        "alice",
		TmuxSession: "test-session",
		TmuxWindow:  "alice",
	}

	GracefulShutdown(sd, tmpDir, agent, false)

	// Sentinel should have been written.
	expectedSentinel := filepath.Join(tmpDir, ".sprawl", "agents", "alice.kill")
	sentinelWritten := slices.Contains(writtenPaths, expectedSentinel)
	if !sentinelWritten {
		t.Errorf("expected sentinel file to be written at %s, got writes: %v", expectedSentinel, writtenPaths)
	}

	// Window disappeared gracefully, so KillWindow should NOT be called.
	if runner.killWindowCalled {
		t.Error("KillWindow should not be called when window disappears gracefully")
	}

	// Sentinel should be cleaned up.
	sentinelRemoved := slices.Contains(removedPaths, expectedSentinel)
	if !sentinelRemoved {
		t.Errorf("expected sentinel file cleanup at %s, got removes: %v", expectedSentinel, removedPaths)
	}
}

func TestGracefulShutdown_Force(t *testing.T) {
	runner := &retireTestRunner{}
	runner.pids = []int{12345}

	var writtenPaths []string
	sd := &ShutdownDeps{
		TmuxRunner: runner,
		WriteFile: func(path string, data []byte, perm os.FileMode) error {
			writtenPaths = append(writtenPaths, path)
			return nil
		},
		RemoveFile: func(path string) error { return nil },
		SleepFunc:  func(d time.Duration) {},
	}

	tmpDir := t.TempDir()
	agent := &state.AgentState{
		Name:        "alice",
		TmuxSession: "test-session",
		TmuxWindow:  "alice",
	}

	GracefulShutdown(sd, tmpDir, agent, true)

	// With force=true, no sentinel should be written.
	if len(writtenPaths) > 0 {
		t.Errorf("expected no sentinel writes with force=true, got: %v", writtenPaths)
	}

	// KillWindow should be called immediately.
	if !runner.killWindowCalled {
		t.Error("expected KillWindow to be called with force=true")
	}
}

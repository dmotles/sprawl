package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/tui"
)

func TestEnter_DefaultsToCwdWhenSprawlRootEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}

	var programCalled bool
	deps := &enterDeps{
		getenv: func(string) string { return "" },
		getwd:  func() (string, error) { return tmpDir, nil },
		runProgram: func(tea.Model) error {
			programCalled = true
			return nil
		},
		newSession: nil,
	}

	err := runEnter(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !programCalled {
		t.Error("runProgram should have been called when SPRAWL_ROOT empty but cwd available")
	}
}

func TestEnter_GetwdErrorReturnsError(t *testing.T) {
	deps := &enterDeps{
		getenv: func(string) string { return "" },
		getwd:  func() (string, error) { return "", errors.New("no cwd") },
		runProgram: func(tea.Model) error {
			return nil
		},
		newSession: nil,
	}

	err := runEnter(deps)
	if err == nil {
		t.Fatal("expected error when getwd fails and SPRAWL_ROOT empty")
	}
	if !strings.Contains(err.Error(), "no cwd") {
		t.Errorf("error = %q, want it to mention getwd failure", err.Error())
	}
}

func TestEnter_EnvVarOverridesCwd(t *testing.T) {
	envDir := t.TempDir()
	cwdDir := t.TempDir()

	factory := &mockSessionFactory{}
	deps := &enterDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return envDir
			}
			return ""
		},
		getwd: func() (string, error) { return cwdDir, nil },
		runProgram: func(tea.Model) error {
			return nil
		},
		newSession: factory.newSession,
	}

	if err := runEnter(deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if factory.sprawlDir != envDir {
		t.Errorf("sprawlRoot = %q, want env override %q", factory.sprawlDir, envDir)
	}
}

func TestEnter_Success(t *testing.T) {
	tmpDir := t.TempDir()

	// Write accent-color file so it can be read by state.ReadAccentColor().
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "accent-color"), []byte("colour212"), 0o644); err != nil {
		t.Fatalf("setup write accent-color: %v", err)
	}

	var programCalled bool
	deps := &enterDeps{
		getenv: func(key string) string {
			switch key {
			case "SPRAWL_ROOT":
				return tmpDir
			default:
				return ""
			}
		},
		runProgram: func(m tea.Model) error {
			programCalled = true
			return nil
		},
		newSession: nil,
	}

	err := runEnter(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !programCalled {
		t.Error("runProgram should have been called")
	}
}

func TestEnter_ProgramError(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "accent-color"), []byte("colour212"), 0o644); err != nil {
		t.Fatalf("setup write accent-color: %v", err)
	}

	programErr := errors.New("program failed")
	deps := &enterDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(tea.Model) error {
			return programErr
		},
		newSession: nil,
	}

	err := runEnter(deps)
	if err == nil {
		t.Fatal("expected error when runProgram fails")
	}
	if !strings.Contains(err.Error(), "program failed") {
		t.Errorf("error = %q, want it to contain 'program failed'", err.Error())
	}
}

func TestEnter_DefaultAccentColor(t *testing.T) {
	tmpDir := t.TempDir()

	// Create state dir but no accent-color file.
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}

	var programCalled bool
	deps := &enterDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(tea.Model) error {
			programCalled = true
			return nil
		},
		newSession: nil,
	}

	err := runEnter(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !programCalled {
		t.Error("runProgram should have been called even without accent-color file")
	}
}

// --- New tests for session integration ---

// mockSessionFactory returns a newSession function and tracks whether it was called.
type mockSessionFactory struct {
	bridge         *tui.Bridge
	err            error
	called         bool
	sprawlDir      string
	lastForceFresh bool
	wasResume      bool
}

func (f *mockSessionFactory) newSession(sprawlRoot string, forceFresh bool) (*tui.Bridge, bool, error) {
	f.called = true
	f.sprawlDir = sprawlRoot
	f.lastForceFresh = forceFresh
	return f.bridge, f.wasResume, f.err
}

func TestEnter_WithSession(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "accent-color"), []byte("colour212"), 0o644); err != nil {
		t.Fatalf("setup write accent-color: %v", err)
	}

	factory := &mockSessionFactory{}

	var capturedModel tea.Model
	deps := &enterDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(m tea.Model) error {
			capturedModel = m
			return nil
		},
		newSession: factory.newSession,
	}

	err := runEnter(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !factory.called {
		t.Error("newSession should have been called")
	}
	if factory.sprawlDir != tmpDir {
		t.Errorf("newSession sprawlRoot = %q, want %q", factory.sprawlDir, tmpDir)
	}
	if capturedModel == nil {
		t.Fatal("runProgram should have received a model")
	}
}

func TestEnter_SessionError(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "accent-color"), []byte("colour212"), 0o644); err != nil {
		t.Fatalf("setup write accent-color: %v", err)
	}

	factory := &mockSessionFactory{
		err: errors.New("failed to create session"),
	}

	deps := &enterDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(tea.Model) error {
			return nil
		},
		newSession: factory.newSession,
	}

	err := runEnter(deps)
	if err == nil {
		t.Fatal("expected error when newSession fails")
	}
	if !strings.Contains(err.Error(), "failed to create session") {
		t.Errorf("error = %q, want it to contain 'failed to create session'", err.Error())
	}
}

// --- Graceful shutdown tests ---

type shutdownMockSupervisor struct {
	agents       []supervisor.AgentInfo
	statusErr    error
	killCalled   []string
	shutdownDone bool
}

func (s *shutdownMockSupervisor) Spawn(_ context.Context, _ supervisor.SpawnRequest) (*supervisor.AgentInfo, error) {
	return nil, nil
}

func (s *shutdownMockSupervisor) Status(_ context.Context) ([]supervisor.AgentInfo, error) {
	return s.agents, s.statusErr
}

func (s *shutdownMockSupervisor) Delegate(_ context.Context, _, _ string) error   { return nil }
func (s *shutdownMockSupervisor) Message(_ context.Context, _, _, _ string) error { return nil }
func (s *shutdownMockSupervisor) Merge(_ context.Context, _, _ string, _ bool) error {
	return nil
}
func (s *shutdownMockSupervisor) Retire(_ context.Context, _ string, _, _ bool) error { return nil }

func (s *shutdownMockSupervisor) Kill(_ context.Context, name string) error {
	s.killCalled = append(s.killCalled, name)
	return nil
}

func (s *shutdownMockSupervisor) Shutdown(_ context.Context) error {
	s.shutdownDone = true
	return nil
}

func TestEnter_GracefulShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "accent-color"), []byte("colour212"), 0o644); err != nil {
		t.Fatalf("setup write accent-color: %v", err)
	}

	mockSup := &shutdownMockSupervisor{
		agents: []supervisor.AgentInfo{
			{Name: "tower", Status: "active"},
			{Name: "finn", Status: "active"},
		},
	}

	deps := &enterDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(tea.Model) error {
			return nil
		},
		newSession:    nil,
		newSupervisor: func(_ string) supervisor.Supervisor { return mockSup },
	}

	err := runEnter(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mockSup.killCalled) != 2 {
		t.Errorf("expected 2 Kill calls, got %d: %v", len(mockSup.killCalled), mockSup.killCalled)
	}
	if !mockSup.shutdownDone {
		t.Error("Shutdown should have been called")
	}
}

func TestBuildSessionEnv_ContainsAgentIdentity(t *testing.T) {
	env := buildSessionEnv()

	var foundIdentity, foundEmitEvents bool
	for _, e := range env {
		if e == "SPRAWL_AGENT_IDENTITY=weave" {
			foundIdentity = true
		}
		if e == "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1" {
			foundEmitEvents = true
		}
	}
	if !foundIdentity {
		t.Error("buildSessionEnv should include SPRAWL_AGENT_IDENTITY=weave")
	}
	if !foundEmitEvents {
		t.Error("buildSessionEnv should include CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
	}
}

// --- QUM-259 Phase 4: finalizeHandoff wiring ---
//
// These tests exercise the contract documented in the phase-4 plan:
//   * enterDeps gains a finalizeHandoff callback.
//   * makeRestartFunc always invokes finalizeHandoff BEFORE newSession.
//   * finalize errors are logged but do not block newSession.
//   * runEnter invokes finalizeHandoff on clean shutdown, skipped on crash.

// fakeFinalize returns a finalize callback that records every invocation and
// appends the sentinel "finalize" to the order slice. If errToReturn is
// non-nil, it is returned from the callback.
func fakeFinalize(order *[]string, count *int32, errToReturn error) func(context.Context, string, io.Writer) error {
	return func(_ context.Context, _ string, _ io.Writer) error {
		atomic.AddInt32(count, 1)
		*order = append(*order, "finalize")
		return errToReturn
	}
}

// orderedSessionFactory appends "newSession" to order on every call and
// returns the stored wasResume / err.
type orderedSessionFactory struct {
	order      *[]string
	calls      int32
	wasResume  bool
	err        error
	forceFresh []bool
}

func (f *orderedSessionFactory) newSession(_ string, forceFresh bool) (*tui.Bridge, bool, error) {
	atomic.AddInt32(&f.calls, 1)
	*f.order = append(*f.order, "newSession")
	f.forceFresh = append(f.forceFresh, forceFresh)
	return nil, f.wasResume, f.err
}

func TestEnter_RestartFunc_CallsFinalizeBeforeNewSession(t *testing.T) {
	var order []string
	var finCount int32
	fact := &orderedSessionFactory{order: &order}

	state := &restartState{}
	rf := makeRestartFunc(
		fact.newSession,
		fakeFinalize(&order, &finCount, nil),
		"/tmp/sprawl",
		state,
		nil,
		io.Discard,
	)

	_, err := rf()
	if err != nil {
		t.Fatalf("restartFunc returned unexpected error: %v", err)
	}
	if len(order) != 2 {
		t.Fatalf("order = %v, want exactly 2 entries (finalize, newSession)", order)
	}
	if order[0] != "finalize" {
		t.Errorf("order[0] = %q, want %q (finalize must run before newSession)", order[0], "finalize")
	}
	if order[1] != "newSession" {
		t.Errorf("order[1] = %q, want %q", order[1], "newSession")
	}
	if finCount != 1 {
		t.Errorf("finalize called %d times, want 1", finCount)
	}
	if fact.calls != 1 {
		t.Errorf("newSession called %d times, want 1", fact.calls)
	}
}

func TestEnter_RestartFunc_FinalizeError_DoesNotBlockNewSession(t *testing.T) {
	var order []string
	var finCount int32
	fact := &orderedSessionFactory{order: &order}

	rf := makeRestartFunc(
		fact.newSession,
		fakeFinalize(&order, &finCount, errors.New("finalize failed")),
		"/tmp/sprawl",
		&restartState{},
		nil,
		io.Discard,
	)

	_, err := rf()
	if err != nil {
		t.Fatalf("restartFunc should not propagate finalize error, got: %v", err)
	}
	if fact.calls != 1 {
		t.Errorf("newSession should still be called when finalize fails, got calls=%d", fact.calls)
	}
	if finCount != 1 {
		t.Errorf("finalize should have been called once, got %d", finCount)
	}
}

func TestEnter_RestartFunc_ForceFreshWhenResumeDiedFast(t *testing.T) {
	var order []string
	var finCount int32
	fact := &orderedSessionFactory{order: &order}

	state := &restartState{
		lastWasResume: true,
		lastStartedAt: time.Now(), // well within resumeFailureWindow
	}
	rf := makeRestartFunc(
		fact.newSession,
		fakeFinalize(&order, &finCount, nil),
		"/tmp/sprawl",
		state,
		nil,
		io.Discard,
	)

	if _, err := rf(); err != nil {
		t.Fatalf("restartFunc returned unexpected error: %v", err)
	}
	if len(fact.forceFresh) != 1 || !fact.forceFresh[0] {
		t.Errorf("forceFresh history = %v, want [true] (resume-died-fast path)", fact.forceFresh)
	}
	if finCount != 1 {
		t.Errorf("finalize should run exactly once, got %d", finCount)
	}
}

func TestEnter_RestartFunc_NoForceFreshWhenNotResume(t *testing.T) {
	var order []string
	var finCount int32
	fact := &orderedSessionFactory{order: &order}

	state := &restartState{
		lastWasResume: false,
		lastStartedAt: time.Now(),
	}
	rf := makeRestartFunc(
		fact.newSession,
		fakeFinalize(&order, &finCount, nil),
		"/tmp/sprawl",
		state,
		nil,
		io.Discard,
	)

	if _, err := rf(); err != nil {
		t.Fatalf("restartFunc returned unexpected error: %v", err)
	}
	if len(fact.forceFresh) != 1 || fact.forceFresh[0] {
		t.Errorf("forceFresh history = %v, want [false] (not a resume)", fact.forceFresh)
	}
}

func TestEnter_ShutdownPath_CallsFinalizeOnCleanExit(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}

	var finCount int32
	deps := &enterDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(tea.Model) error { return nil }, // clean exit
		newSession: nil,
		finalizeHandoff: func(_ context.Context, _ string, _ io.Writer) error {
			atomic.AddInt32(&finCount, 1)
			return nil
		},
	}

	if err := runEnter(deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if finCount != 1 {
		t.Errorf("finalizeHandoff called %d times on clean shutdown, want 1", finCount)
	}
}

func TestEnter_ShutdownPath_SkipsFinalizeOnProgramError(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}

	var finCount int32
	deps := &enterDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(tea.Model) error { return errors.New("tui crashed") },
		newSession: nil,
		finalizeHandoff: func(_ context.Context, _ string, _ io.Writer) error {
			atomic.AddInt32(&finCount, 1)
			return nil
		},
	}

	if err := runEnter(deps); err == nil {
		t.Fatal("expected runEnter to propagate runProgram error")
	}
	if finCount != 0 {
		t.Errorf("finalizeHandoff called %d times after crash, want 0 (skipped on error)", finCount)
	}
}

func TestEnter_NilNewSessionSkipsBridge(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "accent-color"), []byte("colour212"), 0o644); err != nil {
		t.Fatalf("setup write accent-color: %v", err)
	}

	var programCalled bool
	deps := &enterDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(m tea.Model) error {
			programCalled = true
			return nil
		},
		newSession: nil, // no session factory
	}

	err := runEnter(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !programCalled {
		t.Error("runProgram should still be called when newSession is nil")
	}
}

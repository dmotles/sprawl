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
	"github.com/dmotles/sprawl/internal/agentloop"
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
		runProgram: func(tea.Model, func(func(tea.Msg))) error {
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
		runProgram: func(tea.Model, func(func(tea.Msg))) error {
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
		runProgram: func(tea.Model, func(func(tea.Msg))) error {
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
		runProgram: func(m tea.Model, _ func(func(tea.Msg))) error {
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
		runProgram: func(tea.Model, func(func(tea.Msg))) error {
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
		runProgram: func(tea.Model, func(func(tea.Msg))) error {
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
	// capturedSup records the supervisor threaded into newSession. QUM-329.
	capturedSup supervisor.Supervisor
}

func (f *mockSessionFactory) newSession(sprawlRoot string, sup supervisor.Supervisor, forceFresh bool, _ func()) (*tui.Bridge, bool, error) {
	f.called = true
	f.sprawlDir = sprawlRoot
	f.lastForceFresh = forceFresh
	f.capturedSup = sup
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
		runProgram: func(m tea.Model, _ func(func(tea.Msg))) error {
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
		runProgram: func(tea.Model, func(func(tea.Msg))) error {
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

func (s *shutdownMockSupervisor) Retire(_ context.Context, _ string, _, _, _, _ bool) error {
	return nil
}

func (s *shutdownMockSupervisor) Kill(_ context.Context, name string) error {
	s.killCalled = append(s.killCalled, name)
	return nil
}

func (s *shutdownMockSupervisor) Shutdown(_ context.Context) error {
	s.shutdownDone = true
	return nil
}
func (s *shutdownMockSupervisor) Handoff(_ context.Context, _ string) error { return nil }
func (s *shutdownMockSupervisor) HandoffRequested() <-chan struct{}         { return nil }
func (s *shutdownMockSupervisor) PeekActivity(_ context.Context, _ string, _ int) ([]agentloop.ActivityEntry, error) {
	return nil, nil
}

func (s *shutdownMockSupervisor) SendAsync(_ context.Context, _, _, _, _ string, _ []string) (*supervisor.SendAsyncResult, error) {
	return &supervisor.SendAsyncResult{}, nil
}

func (s *shutdownMockSupervisor) Peek(_ context.Context, _ string, _ int) (*supervisor.PeekResult, error) {
	return &supervisor.PeekResult{}, nil
}

func (s *shutdownMockSupervisor) ReportStatus(_ context.Context, _, _, _, _ string) (*supervisor.ReportStatusResult, error) {
	return &supervisor.ReportStatusResult{}, nil
}

func (s *shutdownMockSupervisor) SendInterrupt(_ context.Context, _, _, _, _ string) (*supervisor.SendInterruptResult, error) {
	return &supervisor.SendInterruptResult{}, nil
}

func (s *shutdownMockSupervisor) MessagesList(_ context.Context, _ string, _ int) (*supervisor.MessagesListResult, error) {
	return &supervisor.MessagesListResult{}, nil
}

func (s *shutdownMockSupervisor) MessagesRead(_ context.Context, _ string) (*supervisor.MessagesReadResult, error) {
	return &supervisor.MessagesReadResult{}, nil
}

func (s *shutdownMockSupervisor) MessagesArchive(_ context.Context, _ string) (*supervisor.MessagesArchiveResult, error) {
	return &supervisor.MessagesArchiveResult{}, nil
}

func (s *shutdownMockSupervisor) MessagesPeek(_ context.Context) (*supervisor.MessagesPeekResult, error) {
	return &supervisor.MessagesPeekResult{}, nil
}

// Clean `sprawl enter` shutdown must stop supervisor-owned child runtimes via
// Supervisor.Shutdown. Same-process child runtimes are owned by the host
// process and do not survive a clean host exit.
func TestEnter_CleanShutdown_StopsRuntimeBackedAgentsViaShutdown(t *testing.T) {
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
		runProgram: func(tea.Model, func(func(tea.Msg))) error {
			return nil
		},
		newSession:    nil,
		newSupervisor: func(_ string) supervisor.Supervisor { return mockSup },
	}

	err := runEnter(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mockSup.killCalled) != 0 {
		t.Errorf("clean shutdown should go through Supervisor.Shutdown, not per-agent Kill calls; got %d Kill calls: %v", len(mockSup.killCalled), mockSup.killCalled)
	}
	if !mockSup.shutdownDone {
		t.Error("Shutdown should have been called to stop supervisor-owned runtimes")
	}
}

// TestWrapEnterStderrForResumeScan is the TUI-path unit coverage for QUM-261:
// the stderr writer used by `sprawl enter`'s Claude subprocess must be wrapped
// so that the "No conversation found with session ID:" marker fires a kill
// callback. The subprocess exiting fast then satisfies makeRestartFunc's
// existing resumeFailureWindow heuristic and force-freshes the next session.
func TestWrapEnterStderrForResumeScan(t *testing.T) {
	var sink strings.Builder
	var killed int32
	w := wrapEnterStderrForResumeScan(&sink, func() { atomic.AddInt32(&killed, 1) })

	if _, err := w.Write([]byte("No conversation found with session ID: bogus\n")); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if atomic.LoadInt32(&killed) != 1 {
		t.Errorf("kill callback should fire exactly once on marker; got %d", killed)
	}
	if !strings.Contains(sink.String(), "No conversation found") {
		t.Errorf("underlying stderr must still receive the line for logging; got %q", sink.String())
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
	order            *[]string
	calls            int32
	wasResume        bool
	err              error
	forceFresh       []bool
	resumeFailureCBs []func()
	capturedSup      []supervisor.Supervisor
}

func (f *orderedSessionFactory) newSession(_ string, sup supervisor.Supervisor, forceFresh bool, onResumeFailure func()) (*tui.Bridge, bool, error) {
	atomic.AddInt32(&f.calls, 1)
	*f.order = append(*f.order, "newSession")
	f.forceFresh = append(f.forceFresh, forceFresh)
	f.resumeFailureCBs = append(f.resumeFailureCBs, onResumeFailure)
	f.capturedSup = append(f.capturedSup, sup)
	return nil, f.wasResume, f.err
}

func TestEnter_RestartFunc_CallsFinalizeBeforeNewSession(t *testing.T) {
	var order []string
	var finCount int32
	fact := &orderedSessionFactory{order: &order}

	state := &restartState{}
	rf := makeRestartFunc(
		fact.newSession,
		nil, // sup — exercised in TestEnter_SharedSupervisor_ThreadedEndToEnd below
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
		nil, // sup — exercised in TestEnter_SharedSupervisor_ThreadedEndToEnd below
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
		nil, // sup — exercised in TestEnter_SharedSupervisor_ThreadedEndToEnd below
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

// TestEnter_RestartFunc_MarkerTripped_ForcesFreshRegardlessOfWindow covers
// QUM-261: claude printed "No conversation found with session ID:" and stayed
// alive past resumeFailureWindow before we killed it. The restart must still
// force-fresh — the elapsed-time heuristic alone would re-resume the dead ID.
func TestEnter_RestartFunc_MarkerTripped_ForcesFreshRegardlessOfWindow(t *testing.T) {
	var order []string
	var finCount int32
	fact := &orderedSessionFactory{order: &order}

	state := &restartState{
		lastWasResume: true,
		// Pretend 1 minute has elapsed — well past the 5s window.
		lastStartedAt: time.Now().Add(-time.Minute),
	}
	state.resumeMarkerTripped.Store(true)

	rf := makeRestartFunc(
		fact.newSession,
		nil, // sup — exercised in TestEnter_SharedSupervisor_ThreadedEndToEnd below
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
		t.Errorf("forceFresh history = %v, want [true] (marker-tripped path)", fact.forceFresh)
	}
	if state.resumeMarkerTripped.Load() {
		t.Errorf("marker-tripped flag should be consumed (reset to false) after restart")
	}
}

// TestEnter_RestartFunc_ResumeFailureCallback_SetsMarkerFlag verifies the
// onResumeFailure callback threaded through newSession is the one runEnter /
// restartFunc uses to flip the marker flag. Without this wiring the TUI
// stderr scanner's kill + restart cycle would loop forever on the dead ID.
func TestEnter_RestartFunc_ResumeFailureCallback_SetsMarkerFlag(t *testing.T) {
	var order []string
	var finCount int32
	fact := &orderedSessionFactory{order: &order}
	state := &restartState{}

	rf := makeRestartFunc(
		fact.newSession,
		nil, // sup — exercised in TestEnter_SharedSupervisor_ThreadedEndToEnd below
		fakeFinalize(&order, &finCount, nil),
		"/tmp/sprawl",
		state,
		nil,
		io.Discard,
	)
	if _, err := rf(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fact.resumeFailureCBs) != 1 || fact.resumeFailureCBs[0] == nil {
		t.Fatalf("expected newSession to receive a non-nil onResumeFailure callback")
	}
	// Simulate the stderr marker scanner firing after the subprocess launched.
	fact.resumeFailureCBs[0]()
	if !state.resumeMarkerTripped.Load() {
		t.Errorf("onResumeFailure callback must flip state.resumeMarkerTripped")
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
		nil, // sup — exercised in TestEnter_SharedSupervisor_ThreadedEndToEnd below
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

// Clean shutdown (Ctrl+C) must NOT invoke FinalizeHandoff. FinalizeHandoff
// clears last-session-id when a handoff-signal is present, which would break
// resume-by-default (QUM-255) on the next `sprawl enter`. The explicit
// /handoff path still runs finalize via makeRestartFunc.
func TestEnter_ShutdownPath_DoesNotCallFinalizeOnCleanExit(t *testing.T) {
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
		runProgram: func(tea.Model, func(func(tea.Msg))) error { return nil }, // clean exit
		newSession: nil,
		finalizeHandoff: func(_ context.Context, _ string, _ io.Writer) error {
			atomic.AddInt32(&finCount, 1)
			return nil
		},
	}

	if err := runEnter(deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if finCount != 0 {
		t.Errorf("finalizeHandoff called %d times on clean shutdown, want 0", finCount)
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
		runProgram: func(tea.Model, func(func(tea.Msg))) error { return errors.New("tui crashed") },
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
		runProgram: func(m tea.Model, _ func(func(tea.Msg))) error {
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

// TestEnter_SharedSupervisor_ThreadedEndToEnd is the QUM-329 regression guard.
//
// Exactly ONE supervisor per `sprawl enter` must be shared across:
//   - the MCP server wired to the claude subprocess (observed via the
//     `sup` argument threaded into newSession → newSessionImpl →
//     sprawlmcp.New)
//   - the HandoffRequested() listener goroutine (observed via the
//     supervisor passed to tui.NewAppModel)
//
// We prove the shared-instance invariant by pointer-equality: the
// supervisor returned from deps.newSupervisor MUST equal the one
// captured by the newSession factory AND the one captured when
// restartFunc (re-)invokes newSession on a handoff teardown. If they
// diverge, the handoff MCP tool fires on a channel the TUI
// never drains — exactly the QUM-329 failure mode.
func TestEnter_SharedSupervisor_ThreadedEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}

	sentinelSup := &shutdownMockSupervisor{}
	factory := &mockSessionFactory{}

	deps := &enterDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		getwd: func() (string, error) { return tmpDir, nil },
		runProgram: func(tea.Model, func(func(tea.Msg))) error {
			return nil
		},
		newSession:    factory.newSession,
		newSupervisor: func(_ string) supervisor.Supervisor { return sentinelSup },
	}

	if err := runEnter(deps); err != nil {
		t.Fatalf("unexpected error from runEnter: %v", err)
	}

	if !factory.called {
		t.Fatal("newSession should have been called")
	}
	if factory.capturedSup == nil {
		t.Fatal("newSession received nil supervisor — QUM-329 regression (MCP server would own a separate instance)")
	}
	if factory.capturedSup != sentinelSup {
		t.Errorf("newSession supervisor (%p) != newSupervisor return (%p); QUM-329 invariant broken — MCP and listener would hit different Handoff channels", factory.capturedSup, sentinelSup)
	}
}

// TestMakeRestartFunc_ThreadsSupervisor ensures makeRestartFunc passes its
// `sup` argument through to newSession on every restart invocation. Without
// this wiring, the post-handoff restart would build a fresh MCP server
// bound to the wrong supervisor.
func TestMakeRestartFunc_ThreadsSupervisor(t *testing.T) {
	var order []string
	var finCount int32
	fact := &orderedSessionFactory{order: &order}

	sentinelSup := &shutdownMockSupervisor{}
	rf := makeRestartFunc(
		fact.newSession,
		sentinelSup,
		fakeFinalize(&order, &finCount, nil),
		"/tmp/sprawl",
		&restartState{},
		nil,
		io.Discard,
	)

	if _, err := rf(); err != nil {
		t.Fatalf("restartFunc returned unexpected error: %v", err)
	}
	if _, err := rf(); err != nil {
		t.Fatalf("restartFunc returned unexpected error on second invocation: %v", err)
	}
	if len(fact.capturedSup) != 2 {
		t.Fatalf("expected 2 newSession invocations, got %d", len(fact.capturedSup))
	}
	for i, got := range fact.capturedSup {
		if got != sentinelSup {
			t.Errorf("call %d: newSession got supervisor %p, want %p (QUM-329)", i, got, sentinelSup)
		}
	}
}

// TestDefaultEnterDeps_SupervisorCallerIsWeave is a QUM-333 regression
// guard. The TUI supervisor's CallerName is stamped into every child
// agent's Parent field on Spawn (via the MCP spawn tool) and used
// as the "From" in every supervisor-originated message delivery. It MUST
// be "weave" — the root agent's identity.
//
// Pre-fix the value was "enter", a ghost name with no agent state file.
// Children got parent="enter" so their `sprawl report done` deliveries
// landed in `.sprawl/messages/enter/` and `.sprawl/agents/enter/queue/`
// instead of weave's maildir and harness queue. Weave's QUM-323 drain
// found nothing and weave never woke up.
//
// The regression was introduced by QUM-329's supervisor unification:
// before QUM-329 the MCP server owned a second supervisor created with
// CallerName: rootName (= "weave"), so this one's "enter" placeholder
// was harmless. QUM-329 merged the two but kept the wrong name.
func TestDefaultEnterDeps_SupervisorCallerIsWeave(t *testing.T) {
	// Nil the package-level override so we exercise the production
	// defaultEnterDeps constructor, not a test shim.
	prev := defaultEnterDeps
	defaultEnterDeps = nil
	defer func() { defaultEnterDeps = prev }()

	deps := resolveEnterDeps()
	if deps.newSupervisor == nil {
		t.Fatal("newSupervisor is nil")
	}
	tmpDir := t.TempDir()
	sup := deps.newSupervisor(tmpDir)
	if sup == nil {
		t.Fatal("newSupervisor returned nil")
	}
	realSup, ok := sup.(*supervisor.Real)
	if !ok {
		t.Fatalf("expected *supervisor.Real, got %T", sup)
	}
	if got := realSup.CallerName(); got != "weave" {
		t.Errorf("CallerName = %q, want \"weave\" (QUM-333 — pre-fix was \"enter\", routed children's reports into a phantom queue)", got)
	}
}

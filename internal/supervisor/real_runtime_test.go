package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/sprawlmcp/calllog"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

type spawnPathWorktreeCreator struct {
	path string
}

func (c *spawnPathWorktreeCreator) Create(_, agentName, branchName, _ string) (string, string, error) {
	if err := os.MkdirAll(c.path, 0o755); err != nil {
		return "", "", err
	}
	return c.path, branchName, nil
}

type spawnRollbackSpy struct {
	worktreeRemoves []string
	branchDeletes   []string
}

func (s *spawnRollbackSpy) WorktreeRemove(_ string, worktreePath string, _ bool) error {
	s.worktreeRemoves = append(s.worktreeRemoves, worktreePath)
	return os.RemoveAll(worktreePath)
}

func (s *spawnRollbackSpy) GitBranchDelete(_ string, branchName string) error {
	s.branchDeletes = append(s.branchDeletes, branchName)
	return nil
}

func ensureRuntime(t *testing.T, r *Real, sprawlRoot string, agentState *state.AgentState) *AgentRuntime {
	t.Helper()
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: sprawlRoot,
		Agent:      agentState,
	})
	if rt == nil {
		t.Fatal("Ensure() returned nil runtime")
	}
	return rt
}

func ensureRuntimeWithStarter(t *testing.T, r *Real, sprawlRoot string, agentState *state.AgentState, starter RuntimeStarter) *AgentRuntime {
	t.Helper()
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: sprawlRoot,
		Agent:      agentState,
		Starter:    starter,
	})
	if rt == nil {
		t.Fatal("Ensure() returned nil runtime")
	}
	return rt
}

func TestRealSpawn_RegistersRuntimeForSpawnedAgent(t *testing.T) {
	r, _ := newFakeReal(t)
	r.spawnFn = func(_ *agentops.SpawnDeps, _, _, _, _ string) (*state.AgentState, error) {
		return testAgentState("alice"), nil
	}

	if _, err := r.Spawn(context.Background(), SpawnRequest{
		Family: "engineering",
		Type:   "engineer",
		Prompt: "build feature",
		Branch: "dmotles/alice",
	}); err != nil {
		t.Fatalf("Spawn() error: %v", err)
	}

	rt, ok := r.runtimeRegistry.Get("alice")
	if !ok {
		t.Fatal("runtime registry missing alice after Spawn")
	}
	if rt.Snapshot().Name != "alice" {
		t.Fatalf("runtime snapshot name = %q", rt.Snapshot().Name)
	}
}

func TestRealSpawn_StartsTrackedRuntime(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	starter := &runtimeTestStarter{session: session}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, starter)
	r.spawnFn = func(_ *agentops.SpawnDeps, _, _, _, _ string) (*state.AgentState, error) {
		return agentState, nil
	}

	if _, err := r.Spawn(context.Background(), SpawnRequest{
		Family: "engineering",
		Type:   "engineer",
		Prompt: "build feature",
		Branch: "dmotles/alice",
	}); err != nil {
		t.Fatalf("Spawn() error: %v", err)
	}

	if got := rt.Snapshot().Liveness; got != liveness.Running {
		t.Fatalf("Lifecycle = %q, want %q", got, liveness.Running)
	}
	if len(starter.specs) != 1 {
		t.Fatalf("runtime starter calls = %d, want 1", len(starter.specs))
	}
}

func TestRealSpawn_FreshSpawnUsesBootstrapAndRuntimeStarter(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	worktreePath := filepath.Join(tmpDir, ".sprawl", "worktrees", "alice")
	r.spawnFn = agentops.PrepareSpawn
	r.spawnDeps = &agentops.SpawnDeps{
		WorktreeCreator: &spawnPathWorktreeCreator{path: worktreePath},
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_AGENT_IDENTITY":
				return "weave"
			case "SPRAWL_ROOT":
				return tmpDir
			}
			return ""
		},
		CurrentBranch: func(string) (string, error) { return "main", nil },
		NewSpawnLock: func(string) (func() error, func() error) {
			return func() error { return nil }, func() error { return nil }
		},
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		RunScript:      agentops.RunBashScript,
		WorktreeRemove: agentops.RealWorktreeRemove,
	}
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	r.runtimeStarter = &runtimeTestStarter{session: session}

	info, err := r.Spawn(context.Background(), SpawnRequest{
		Family: "engineering",
		Type:   "engineer",
		Prompt: "build feature",
		Branch: "dmotles/alice",
	})
	if err != nil {
		t.Fatalf("Spawn() error: %v", err)
	}

	rt, ok := r.runtimeRegistry.Get(info.Name)
	if !ok {
		t.Fatalf("runtime registry missing %s after fresh Spawn", info.Name)
	}
	if got := rt.Snapshot().Liveness; got != liveness.Running {
		t.Fatalf("Lifecycle = %q, want %q", got, liveness.Running)
	}
}

func TestRealSpawn_RuntimeStartFailureRollsBackPersistedArtifacts(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	worktreePath := filepath.Join(tmpDir, ".sprawl", "worktrees", "alice")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	agentState.Worktree = worktreePath
	promptPath := filepath.Join(tmpDir, ".sprawl", "agents", "alice", "prompts", "initial.md")
	if err := os.MkdirAll(filepath.Dir(promptPath), 0o755); err != nil {
		t.Fatalf("mkdir prompt dir: %v", err)
	}
	if err := os.WriteFile(promptPath, []byte("build feature"), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}
	agentState.Prompt = fmt.Sprintf("Your task is in @%s — read it and begin working.", promptPath)
	saveTestAgent(t, tmpDir, agentState)
	rollbackSpy := &spawnRollbackSpy{}
	r.spawnDeps.WorktreeRemove = rollbackSpy.WorktreeRemove
	r.spawnDeps.GitBranchDelete = rollbackSpy.GitBranchDelete

	starter := &runtimeTestStarter{err: errors.New("runtime start failed")}
	ensureRuntimeWithStarter(t, r, tmpDir, agentState, starter)
	r.spawnFn = func(_ *agentops.SpawnDeps, _, _, _, _ string) (*state.AgentState, error) {
		return agentState, nil
	}

	_, err := r.Spawn(context.Background(), SpawnRequest{
		Family: "engineering",
		Type:   "engineer",
		Prompt: "build feature",
		Branch: "dmotles/alice",
	})
	if err == nil {
		t.Fatal("Spawn() error = nil, want runtime start failure")
	}
	if _, loadErr := state.LoadAgent(tmpDir, "alice"); loadErr == nil {
		t.Fatal("state file should be removed on runtime start failure")
	}
	if _, statErr := os.Stat(promptPath); statErr == nil {
		t.Fatal("prompt file should be removed on runtime start failure")
	}
	if _, ok := r.runtimeRegistry.Get("alice"); ok {
		t.Fatal("runtime should be removed on runtime start failure")
	}
	if len(rollbackSpy.worktreeRemoves) != 1 || rollbackSpy.worktreeRemoves[0] != worktreePath {
		t.Fatalf("worktree cleanup calls = %v, want [%s]", rollbackSpy.worktreeRemoves, worktreePath)
	}
	if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
		t.Fatalf("worktree should be removed on runtime start failure, stat err = %v", statErr)
	}
	if len(rollbackSpy.branchDeletes) != 1 || rollbackSpy.branchDeletes[0] != agentState.Branch {
		t.Fatalf("branch cleanup calls = %v, want [%s]", rollbackSpy.branchDeletes, agentState.Branch)
	}
}

func TestRealSpawn_FailedSpawnDoesNotRegisterRuntime(t *testing.T) {
	r, _ := newFakeReal(t)
	r.spawnFn = func(_ *agentops.SpawnDeps, _, _, _, _ string) (*state.AgentState, error) {
		return nil, errors.New("boom")
	}

	if _, err := r.Spawn(context.Background(), SpawnRequest{
		Family: "engineering",
		Type:   "engineer",
		Prompt: "build feature",
		Branch: "dmotles/alice",
	}); err == nil {
		t.Fatal("Spawn() error = nil, want boom")
	}

	if _, ok := r.runtimeRegistry.Get("alice"); ok {
		t.Fatal("failed Spawn() should not register a runtime")
	}
}

func TestRealDelegate_UpdatesRuntimeAfterPersistedSuccess(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntime(t, r, tmpDir, agentState)

	if err := r.Delegate(context.Background(), "alice", "implement feature"); err != nil {
		t.Fatalf("Delegate() error: %v", err)
	}

	if rt.Snapshot().QueueDepth != 1 {
		t.Fatalf("QueueDepth = %d, want 1", rt.Snapshot().QueueDepth)
	}
}

func TestRealDelegate_SignalsWakeOnlyAfterPersistedSuccess(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{
		session: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
	})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	if err := r.Delegate(context.Background(), "alice", "implement feature"); err != nil {
		t.Fatalf("Delegate() error: %v", err)
	}

	snap := rt.Snapshot()
	if snap.WakeCount != 1 {
		t.Fatalf("WakeCount = %d, want 1", snap.WakeCount)
	}
	if snap.InterruptCount != 0 {
		t.Fatalf("InterruptCount = %d, want 0 for delegate wake-only behavior", snap.InterruptCount)
	}
}

func TestRealDelegate_DoesNotCreateRuntimeWhenAgentIsUntracked(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, testAgentState("alice"))

	if err := r.Delegate(context.Background(), "alice", "implement feature"); err != nil {
		t.Fatalf("Delegate() error: %v", err)
	}

	if _, ok := r.runtimeRegistry.Get("alice"); ok {
		t.Fatal("Delegate() should not auto-create a runtime for an untracked agent")
	}
}

func TestRealDelegate_FailedPersistLeavesRuntimeUnchanged(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	rt := ensureRuntime(t, r, tmpDir, agentState)

	before := rt.Snapshot()
	err := r.Delegate(context.Background(), "alice", "implement feature")
	if err == nil {
		t.Fatal("Delegate() error = nil, want failure when state file is missing")
	}

	after := rt.Snapshot()
	if after != before {
		t.Fatalf("snapshot changed on failed Delegate: before=%+v after=%+v", before, after)
	}
}

func TestRealReportStatus_SignalsParentRuntimeWakeAfterFullPersistence(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	parent := testAgentState("alice")
	child := testAgentState("bob")
	child.Parent = "alice"
	saveTestAgent(t, tmpDir, parent)
	saveTestAgent(t, tmpDir, child)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, parent, &runtimeTestStarter{
		session: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
	})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	res, err := r.ReportStatus(context.Background(), "bob", "working", "writing tests")
	if err != nil {
		t.Fatalf("ReportStatus() error: %v", err)
	}
	if res == nil || res.ReportedAt == "" {
		t.Fatalf("ReportStatus() result = %+v, want reported timestamp", res)
	}

	snap := rt.Snapshot()
	if snap.WakeCount != 1 {
		t.Fatalf("WakeCount = %d, want 1 after report delivery (QUM-550 slice 2 cooperative wake)", snap.WakeCount)
	}
	if snap.InterruptCount != 0 {
		t.Fatalf("InterruptCount = %d, want 0 for report delivery cooperative wake path", snap.InterruptCount)
	}
}

// QUM-559: removed TestRealReportStatus_QueueFailureDoesNotSignalParentRuntime
// and TestRealReportStatus_MaildirFailureDoesNotSignalParentRuntime — those
// pinned the OLD maildir/queue delivery contract for report_status. The new
// contract is state-only persistence + an in-process ephemeral ring; there is
// no maildir or harness-queue write to fail.

func TestRealReportStatus_UpdatesRuntimeAfterPersistedSuccess(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntime(t, r, tmpDir, agentState)

	_, err := r.ReportStatus(context.Background(), "alice", "working", "writing tests")
	if err != nil {
		t.Fatalf("ReportStatus() error: %v", err)
	}

	snap := rt.Snapshot()
	if snap.LastReport.State != "working" {
		t.Fatalf("LastReport.State = %q", snap.LastReport.State)
	}
	if snap.LastReport.Message != "writing tests" {
		t.Fatalf("LastReport.Message = %q", snap.LastReport.Message)
	}
}

func TestRealReportStatus_FailedPersistLeavesRuntimeUnchanged(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	rt := ensureRuntime(t, r, tmpDir, agentState)

	before := rt.Snapshot()
	_, err := r.ReportStatus(context.Background(), "alice", "working", "writing tests")
	if err == nil {
		t.Fatal("ReportStatus() error = nil, want failure when state file is missing")
	}

	after := rt.Snapshot()
	if after != before {
		t.Fatalf("snapshot changed on failed ReportStatus: before=%+v after=%+v", before, after)
	}
}

// TestRealReportStatus_DoesNotInterruptParentSession pins QUM-550 slice 2:
// report_status must route the parent-runtime notification through the
// cooperative WakeForDelivery path — never Session.Interrupt and never
// ForceInterruptDelivery. This mirrors the SendAsync rewire in slice 1.
func TestRealReportStatus_DoesNotInterruptParentSession(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	parent := testAgentState("alice")
	child := testAgentState("bob")
	child.Parent = "alice"
	saveTestAgent(t, tmpDir, parent)
	saveTestAgent(t, tmpDir, child)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, parent, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	// NEW 4-arg ReportStatus signature: no detail.
	if _, err := r.ReportStatus(context.Background(), "bob", "working", "summary text"); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}

	if session.interrupts != 0 {
		t.Errorf("session.Interrupt called %d times by ReportStatus; want 0 (QUM-550 slice 2 cooperative lock-in)", session.interrupts)
	}
	if session.forceInterruptDeliveryCalls != 0 {
		t.Errorf("session.ForceInterruptDelivery calls = %d; want 0 (report_status must use cooperative wake)", session.forceInterruptDeliveryCalls)
	}
	if session.wakeForDeliveryCalls < 1 {
		t.Errorf("session.WakeForDelivery calls = %d, want >= 1 after ReportStatus rewire", session.wakeForDeliveryCalls)
	}

	snap := rt.Snapshot()
	if snap.InterruptCount != 0 {
		t.Errorf("parent runtime InterruptCount = %d, want 0 — cooperative wake must not bump it", snap.InterruptCount)
	}
	if snap.WakeCount < 1 {
		t.Errorf("parent runtime WakeCount = %d, want >= 1 after cooperative wake", snap.WakeCount)
	}
}

// TestRealReportStatus_DrainedStatusChangeLineContainsSummaryVerbatim is the
// QUM-614 successor to the legacy QUM-559 ring-drain test. The
// status-notification line drained from the parent's maildir via
// inboxprompt.DrainStatusChangeLines must contain the summary verbatim, with
// no \n\n separator (detail-concat regression).
func TestRealReportStatus_DrainedStatusChangeLineContainsSummaryVerbatim(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	parent := testAgentState("alice")
	child := testAgentState("bob")
	child.Parent = "alice"
	saveTestAgent(t, tmpDir, parent)
	saveTestAgent(t, tmpDir, child)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, parent, &runtimeTestStarter{
		session: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
	})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	const summary = "MY-SLICE2-SUMMARY"
	if _, err := r.ReportStatus(context.Background(), "bob", "working", summary); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}

	drained := inboxprompt.DrainStatusChangeLines(tmpDir, "alice")
	if len(drained) != 1 {
		t.Fatalf("DrainStatusChangeLines(alice) len = %d, want 1; got %#v", len(drained), drained)
	}
	if !strings.Contains(drained[0], summary) {
		t.Errorf("drained line missing summary: %q", drained[0])
	}
	if strings.Contains(drained[0], "\n\n") {
		t.Errorf("drained line contains \\n\\n separator (detail concat leaked): %q", drained[0])
	}
}

func TestRealKill_UpdatesRuntimeAfterPersistedSuccess(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntime(t, r, tmpDir, agentState)
	r.killFn = func(_ *agentops.KillDeps, name string, force bool) error {
		updated, err := state.LoadAgent(tmpDir, name)
		if err != nil {
			return err
		}
		updated.Status = "killed"
		return state.SaveAgent(tmpDir, updated)
	}

	if err := r.Kill(context.Background(), "alice"); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	snap := rt.Snapshot()
	if snap.Status != "killed" {
		t.Fatalf("Status = %q, want killed", snap.Status)
	}
	if snap.Liveness != liveness.Killed {
		t.Fatalf("Lifecycle = %q, want %q", snap.Liveness, liveness.Killed)
	}
}

func TestRealKill_RuntimeBackedAgentSkipsLegacyKillFn(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	r.killFn = func(*agentops.KillDeps, string, bool) error {
		t.Fatal("legacy killFn should not be called for a runtime-backed child")
		return nil
	}

	if err := r.Kill(context.Background(), "alice"); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	updated, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if updated.Status != "killed" {
		t.Fatalf("Status = %q, want killed", updated.Status)
	}
	if session.stopCalls != 1 {
		t.Fatalf("runtime stop calls = %d, want 1", session.stopCalls)
	}
}

func TestRealKill_FailedPersistLeavesRuntimeUnchanged(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntime(t, r, tmpDir, agentState)
	r.killFn = func(*agentops.KillDeps, string, bool) error { return errors.New("boom") }

	before := rt.Snapshot()
	err := r.Kill(context.Background(), "alice")
	if err == nil {
		t.Fatal("Kill() error = nil, want boom")
	}

	after := rt.Snapshot()
	if after != before {
		t.Fatalf("snapshot changed on failed Kill: before=%+v after=%+v", before, after)
	}
}

func TestRealKill_StartedRuntimeFailureLeavesRuntimeNotStarted(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	// QUM-372: SaveAgent now uses atomic rename via os.CreateTemp; the
	// previous "chmod 0o400 on alice.json" trick no longer forces a write
	// error because rename overwrites mode bits. Make the *agents dir*
	// read-only so CreateTemp fails up-front, which is the equivalent
	// persistence-failure trigger.
	agentsDir := state.AgentsDir(tmpDir)
	if err := os.Chmod(agentsDir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(agentsDir, 0o755) })

	err := r.Kill(context.Background(), "alice")
	if err == nil {
		t.Fatal("Kill() error = nil, want failure after runtime stop")
	}
	if got := rt.Snapshot().Liveness; got == liveness.Running {
		t.Fatalf("Lifecycle = %q, want not-started after failed persistence", got)
	}
	if _, ok := r.startedRuntime("alice"); ok {
		t.Fatal("startedRuntime should reject a stopped runtime after persistence failure")
	}
	if session.stopCalls != 1 {
		t.Fatalf("runtime stop calls = %d, want 1", session.stopCalls)
	}
}

func TestRealRetire_CascadeRemovesDescendantRuntimesAfterSuccess(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	parent := testAgentState("alice")
	child := testAgentState("bob")
	child.Parent = "alice"
	grandchild := testAgentState("carol")
	grandchild.Parent = "bob"
	other := testAgentState("dave")
	for _, agentState := range []*state.AgentState{parent, child, grandchild, other} {
		saveTestAgent(t, tmpDir, agentState)
		ensureRuntime(t, r, tmpDir, agentState)
	}
	r.retireFn = func(_ context.Context, _ *agentops.RetireDeps, _ string, _, _, _, _, _, _ bool) error { return nil }

	if err := r.Retire(context.Background(), "", "alice", false, false, true, false); err != nil {
		t.Fatalf("Retire() error: %v", err)
	}

	for _, name := range []string{"alice", "bob", "carol"} {
		if _, ok := r.runtimeRegistry.Get(name); ok {
			t.Fatalf("runtime %q still present after cascade retire", name)
		}
	}
	if _, ok := r.runtimeRegistry.Get("dave"); !ok {
		t.Fatal("unrelated runtime dave should remain registered")
	}
}

func TestRealRetire_FailedPersistLeavesRuntimeUnchanged(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntime(t, r, tmpDir, agentState)
	r.retireFn = func(context.Context, *agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error {
		return errors.New("boom")
	}

	before := rt.Snapshot()
	err := r.Retire(context.Background(), "", "alice", false, false, false, false)
	if err == nil {
		t.Fatal("Retire() error = nil, want boom")
	}

	after := rt.Snapshot()
	if after != before {
		t.Fatalf("snapshot changed on failed Retire: before=%+v after=%+v", before, after)
	}
	if _, ok := r.runtimeRegistry.Get("alice"); !ok {
		t.Fatal("runtime alice should remain registered after failed retire")
	}
}

func TestRealRetire_StartedRuntimeFailureLeavesRuntimeNotStarted(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Worktree = ""
	agentState.Branch = ""
	saveTestAgent(t, tmpDir, agentState)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	r.retireFn = func(context.Context, *agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error {
		return errors.New("boom")
	}

	err := r.Retire(context.Background(), "", "alice", false, false, false, false)
	if err == nil {
		t.Fatal("Retire() error = nil, want boom")
	}
	if got := rt.Snapshot().Liveness; got == liveness.Running {
		t.Fatalf("Lifecycle = %q, want not-started after failed retire", got)
	}
	if _, ok := r.startedRuntime("alice"); ok {
		t.Fatal("startedRuntime should reject a stopped runtime after retire failure")
	}
	if session.stopCalls != 1 {
		t.Fatalf("runtime stop calls = %d, want 1", session.stopCalls)
	}
}

func TestRealRetire_RuntimeBackedAgentStopsRuntimeBeforeLegacyRetireFn(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Worktree = ""
	agentState.Branch = ""
	saveTestAgent(t, tmpDir, agentState)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	r.retireFn = func(_ context.Context, _ *agentops.RetireDeps, name string, cascade, force, abandon, mergeFirst, yes, noValidate bool) error {
		if name != "alice" {
			t.Fatalf("retireFn name = %q, want alice", name)
		}
		if session.stopCalls != 1 {
			t.Fatalf("retireFn called before runtime stop; stop calls = %d", session.stopCalls)
		}
		if cascade {
			t.Fatal("runtime-backed retire should recurse children before calling retireFn")
		}
		return state.DeleteAgent(tmpDir, name)
	}

	if err := r.Retire(context.Background(), "", "alice", false, false, false, false); err != nil {
		t.Fatalf("Retire() error: %v", err)
	}

	if _, err := state.LoadAgent(tmpDir, "alice"); err == nil {
		t.Fatal("state file should be removed after runtime-backed retire")
	}
	if _, ok := r.runtimeRegistry.Get("alice"); ok {
		t.Fatal("runtime should be removed after runtime-backed retire")
	}
	if session.stopCalls != 1 {
		t.Fatalf("runtime stop calls = %d, want 1", session.stopCalls)
	}
}

func TestRealRetire_RuntimeBackedAgentRequiresCascadeWhenChildrenExist(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	parent := testAgentState("alice")
	child := testAgentState("bob")
	child.Parent = "alice"
	saveTestAgent(t, tmpDir, parent)
	saveTestAgent(t, tmpDir, child)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, parent, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	r.retireFn = func(context.Context, *agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error {
		t.Fatal("retireFn should not run when active children require --cascade")
		return nil
	}

	err := r.Retire(context.Background(), "", "alice", false, false, false, false)
	if err == nil {
		t.Fatal("Retire() error = nil, want active-children guard")
	}
	if session.stopCalls != 0 {
		t.Fatalf("runtime stop calls = %d, want 0 when guard fails", session.stopCalls)
	}
	if _, ok := r.runtimeRegistry.Get("alice"); !ok {
		t.Fatal("runtime should remain registered when retire is rejected")
	}
}

func TestRealKill_OfflineCleanupWhenNoRuntimeIsLive(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	r.killFn = func(*agentops.KillDeps, string, bool) error {
		updated, err := state.LoadAgent(tmpDir, "alice")
		if err != nil {
			return err
		}
		updated.Status = "killed"
		return state.SaveAgent(tmpDir, updated)
	}

	if err := r.Kill(context.Background(), "alice"); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	updated, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if updated.Status != "killed" {
		t.Fatalf("Status = %q, want killed", updated.Status)
	}
}

func TestRealKill_TmuxUnavailableAlreadyKilledIsIdempotent(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Status = "killed"
	saveTestAgent(t, tmpDir, agentState)
	r.killFn = func(*agentops.KillDeps, string, bool) error {
		t.Fatal("legacy killFn should not run for already-killed agent")
		return nil
	}

	if err := r.Kill(context.Background(), "alice"); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}
}

func TestRealRetire_OfflineCleanupWhenNoRuntimeIsLive(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	r.retireFn = func(context.Context, *agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error {
		return state.DeleteAgent(tmpDir, "alice")
	}

	if err := r.Retire(context.Background(), "", "alice", false, false, false, false); err != nil {
		t.Fatalf("Retire() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state.AgentsDir(tmpDir), "alice.json")); !os.IsNotExist(err) {
		t.Fatalf("expected alice state file to be removed, stat err=%v", err)
	}
}

func TestRealShutdown_StopsRuntimeBackedChildren(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	r.killFn = func(*agentops.KillDeps, string, bool) error {
		t.Fatal("legacy killFn should not be called during Shutdown for runtime-backed children")
		return nil
	}

	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}

	updated, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	// QUM-372: graceful Shutdown of a running runtime-backed child must
	// now mark it suspended (so the next sprawl-enter can auto-resume it),
	// NOT killed. Explicit Kill is the only path that still sets killed.
	if updated.Status != state.StatusSuspended {
		t.Fatalf("Status = %q, want %q (QUM-372: Shutdown suspends rather than kills)", updated.Status, state.StatusSuspended)
	}
	if session.stopCalls != 1 {
		t.Fatalf("runtime stop calls = %d, want 1", session.stopCalls)
	}
}

// TestRealShutdown_TransitionMatrix pins the QUM-372 Shutdown status mapping:
// any runtime-backed agent in a non-terminal state flips to "suspended"; agents
// already in terminal-ish / faulted states ({killed, retired, retiring,
// faulted}) are left as-is so the next launch's RecoverAgents scan skips
// suspendable ones and a faulted agent stays recoverable. QUM-625 (slice M4)
// replaced the legacy "done" skip with "faulted" — StatusDone is never written
// anymore (completion lives on the outcome axis).
func TestRealShutdown_TransitionMatrix(t *testing.T) {
	cases := []struct {
		name       string
		preStatus  string
		wantStatus string
	}{
		{"running becomes suspended", state.StatusRunning, state.StatusSuspended},
		{"active becomes suspended", state.StatusActive, state.StatusSuspended},
		{"suspended stays suspended", state.StatusSuspended, state.StatusSuspended},
		{"killed stays killed", state.StatusKilled, state.StatusKilled},
		{"retired stays retired", state.StatusRetired, state.StatusRetired},
		{"retiring stays retiring", state.StatusRetiring, state.StatusRetiring},
		{"faulted stays faulted", state.StatusFaulted, state.StatusFaulted},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r, tmpDir := newFakeReal(t)
			ag := testAgentState("alice")
			ag.Status = tc.preStatus
			saveTestAgent(t, tmpDir, ag)
			session := &runtimeTestSession{
				sessionID: "sess-alice",
				caps:      backendpkg.Capabilities{SupportsInterrupt: true},
			}
			rt := ensureRuntimeWithStarter(t, r, tmpDir, ag, &runtimeTestStarter{session: session})
			if err := rt.Start(); err != nil {
				t.Fatalf("runtime.Start: %v", err)
			}

			if err := r.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}

			updated, err := state.LoadAgent(tmpDir, "alice")
			if err != nil {
				t.Fatalf("LoadAgent: %v", err)
			}
			if updated.Status != tc.wantStatus {
				t.Errorf("pre=%q post=%q, want %q", tc.preStatus, updated.Status, tc.wantStatus)
			}
		})
	}
}

func TestRealRetire_CascadeFailureRemovesDescendantsAlreadyRetiredOnDisk(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	parent := testAgentState("alice")
	child := testAgentState("bob")
	child.Parent = "alice"
	grandchild := testAgentState("carol")
	grandchild.Parent = "bob"
	for _, agentState := range []*state.AgentState{parent, child, grandchild} {
		saveTestAgent(t, tmpDir, agentState)
		ensureRuntime(t, r, tmpDir, agentState)
	}
	r.retireFn = func(_ context.Context, _ *agentops.RetireDeps, _ string, _, _, _, _, _, _ bool) error {
		if err := state.DeleteAgent(tmpDir, "bob"); err != nil {
			return err
		}
		if err := state.DeleteAgent(tmpDir, "carol"); err != nil {
			return err
		}
		return errors.New("parent teardown failed")
	}

	err := r.Retire(context.Background(), "", "alice", false, false, true, false)
	if err == nil {
		t.Fatal("Retire() error = nil, want cascade failure")
	}

	if _, ok := r.runtimeRegistry.Get("alice"); !ok {
		t.Fatal("parent runtime should remain registered after cascade failure")
	}
	for _, name := range []string{"bob", "carol"} {
		if _, ok := r.runtimeRegistry.Get(name); ok {
			t.Fatalf("runtime %q should be removed after it retires on disk during cascade failure", name)
		}
	}
}

// ---------------------------------------------------------------------------
// QUM-546: retire/kill runtime-stop checkpoints
// ---------------------------------------------------------------------------
//
// Real.Retire and Real.Kill bracket the runtime.Stop call with
// `<op>.runtime-stop-{start,done}` checkpoints so operators reading the
// JSONL call log can see exactly how long Stop took, whether the bounded
// session.Wait fired (`wait_timeout`), and where the call sat between Stop
// and the existing `retire.preflight` emission inside agentops.Retire.

// checkpointRecord captures the fields the QUM-546 assertions care about
// from one row of mcp-calls.jsonl.
type checkpointRecord struct {
	Phase  string         `json:"phase"`
	Step   string         `json:"step"`
	CallID string         `json:"call_id"`
	KV     map[string]any `json:"kv"`
}

// readCheckpointSteps returns, in JSONL order, the `step` of every checkpoint
// record in mcp-calls.jsonl whose call_id matches wantCallID. Tests use this
// to assert ordering of the new runtime-stop checkpoints relative to the
// pre-existing retire.preflight / retire.checkpoint-saved /
// retire.worktree-removed emissions inside agentops.Retire.
func readCheckpointSteps(t *testing.T, sprawlRoot, wantCallID string) []checkpointRecord {
	t.Helper()
	path := filepath.Join(sprawlRoot, ".sprawl", "logs", "mcp-calls.jsonl")
	f, err := os.Open(path) //nolint:gosec // test-controlled path under t.TempDir()
	if err != nil {
		t.Fatalf("open mcp-calls.jsonl: %v", err)
	}
	defer func() { _ = f.Close() }()

	var out []checkpointRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var rec checkpointRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("unmarshal jsonl line %q: %v", scanner.Text(), err)
		}
		if rec.Phase != "checkpoint" {
			continue
		}
		if wantCallID != "" && rec.CallID != wantCallID {
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan jsonl: %v", err)
	}
	return out
}

// findStep returns the first checkpointRecord whose Step equals step, or nil.
func findStep(recs []checkpointRecord, step string) *checkpointRecord {
	for i := range recs {
		if recs[i].Step == step {
			return &recs[i]
		}
	}
	return nil
}

func TestRealRetire_EmitsRuntimeStopCheckpoints(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Worktree = ""
	agentState.Branch = ""
	saveTestAgent(t, tmpDir, agentState)

	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	logger, err := calllog.Open(tmpDir)
	if err != nil {
		t.Fatalf("calllog.Open: %v", err)
	}
	r.SetCallLogger(logger)

	// Make retireFn emit the three existing checkpoints via deps.Checkpoint
	// so we can assert ordering relative to the new runtime-stop pair.
	r.retireFn = func(_ context.Context, deps *agentops.RetireDeps, name string, _, _, _, _, _, _ bool) error {
		if deps.Checkpoint != nil {
			deps.Checkpoint("retire.preflight", "agent_name", name)
			deps.Checkpoint("retire.checkpoint-saved", "agent_name", name)
			deps.Checkpoint("retire.worktree-removed", "agent_name", name)
		}
		return state.DeleteAgent(tmpDir, name)
	}

	ctx := calllog.WithCallID(context.Background(), "test-retire-1")
	if err := r.Retire(ctx, "", "alice", false, false, false, false); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("logger.Close: %v", err)
	}

	recs := readCheckpointSteps(t, tmpDir, "test-retire-1")
	wantOrder := []string{
		"retire.runtime-stop-start",
		"retire.runtime-stop-done",
		"retire.preflight",
		"retire.checkpoint-saved",
		"retire.worktree-removed",
	}
	gotSteps := make([]string, 0, len(recs))
	for _, rec := range recs {
		gotSteps = append(gotSteps, rec.Step)
	}
	for _, want := range wantOrder {
		if findStep(recs, want) == nil {
			t.Fatalf("missing checkpoint step %q; got steps in order: %v", want, gotSteps)
		}
	}
	// Strict ordering.
	idx := func(step string) int {
		for i, s := range gotSteps {
			if s == step {
				return i
			}
		}
		return -1
	}
	for i := 0; i < len(wantOrder)-1; i++ {
		if idx(wantOrder[i]) >= idx(wantOrder[i+1]) {
			t.Fatalf("order violation: %q (idx=%d) must precede %q (idx=%d); got steps %v",
				wantOrder[i], idx(wantOrder[i]), wantOrder[i+1], idx(wantOrder[i+1]), gotSteps)
		}
	}

	done := findStep(recs, "retire.runtime-stop-done")
	if done == nil {
		t.Fatalf("retire.runtime-stop-done not present")
	}
	if done.KV == nil {
		t.Fatalf("retire.runtime-stop-done kv is nil; want duration_ms + wait_timeout")
	}
	dur, ok := done.KV["duration_ms"]
	if !ok {
		t.Errorf("kv missing duration_ms; got kv=%v", done.KV)
	}
	// JSON unmarshals numbers as float64.
	if durF, isF := dur.(float64); !isF || durF < 0 {
		t.Errorf("duration_ms = %v (type %T), want non-negative number", dur, dur)
	}
	wt, ok := done.KV["wait_timeout"]
	if !ok {
		t.Errorf("kv missing wait_timeout; got kv=%v", done.KV)
	}
	if got, ok := wt.(bool); !ok || got {
		t.Errorf("wait_timeout = %v (type %T), want false on clean stop", wt, wt)
	}
}

func TestRealRetire_RuntimeStopDoneIncludesWaitTimeoutTrue(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Worktree = ""
	agentState.Branch = ""
	saveTestAgent(t, tmpDir, agentState)

	session := &runtimeTestSession{
		sessionID:        "sess-alice",
		caps:             backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		stopWaitTimedOut: true,
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	logger, err := calllog.Open(tmpDir)
	if err != nil {
		t.Fatalf("calllog.Open: %v", err)
	}
	r.SetCallLogger(logger)
	r.retireFn = func(_ context.Context, _ *agentops.RetireDeps, name string, _, _, _, _, _, _ bool) error {
		return state.DeleteAgent(tmpDir, name)
	}

	ctx := calllog.WithCallID(context.Background(), "test-retire-2")
	if err := r.Retire(ctx, "", "alice", false, false, false, false); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("logger.Close: %v", err)
	}

	recs := readCheckpointSteps(t, tmpDir, "test-retire-2")
	done := findStep(recs, "retire.runtime-stop-done")
	if done == nil {
		t.Fatalf("retire.runtime-stop-done missing; got %d records", len(recs))
	}
	wt, _ := done.KV["wait_timeout"].(bool)
	if !wt {
		t.Errorf("wait_timeout = %v, want true when handle.StopWaitTimedOut() reports true", done.KV["wait_timeout"])
	}
}

func TestRealKill_EmitsRuntimeStopCheckpoints(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)

	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	logger, err := calllog.Open(tmpDir)
	if err != nil {
		t.Fatalf("calllog.Open: %v", err)
	}
	r.SetCallLogger(logger)

	ctx := calllog.WithCallID(context.Background(), "test-kill-1")
	if err := r.Kill(ctx, "alice"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("logger.Close: %v", err)
	}

	recs := readCheckpointSteps(t, tmpDir, "test-kill-1")
	start := findStep(recs, "kill.runtime-stop-start")
	done := findStep(recs, "kill.runtime-stop-done")
	if start == nil || done == nil {
		gotSteps := make([]string, 0, len(recs))
		for _, rec := range recs {
			gotSteps = append(gotSteps, rec.Step)
		}
		t.Fatalf("missing kill.runtime-stop-{start,done}; got steps: %v", gotSteps)
	}

	if done.KV == nil {
		t.Fatalf("kill.runtime-stop-done kv is nil")
	}
	if _, ok := done.KV["duration_ms"]; !ok {
		t.Errorf("kv missing duration_ms; got %v", done.KV)
	}
	wt, ok := done.KV["wait_timeout"]
	if !ok {
		t.Errorf("kv missing wait_timeout; got %v", done.KV)
	}
	if got, ok := wt.(bool); !ok || got {
		t.Errorf("wait_timeout = %v, want false on clean kill", wt)
	}
}

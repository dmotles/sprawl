package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
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

	if got := rt.Snapshot().Lifecycle; got != RuntimeLifecycleStarted {
		t.Fatalf("Lifecycle = %q, want %q", got, RuntimeLifecycleStarted)
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
	if got := rt.Snapshot().Lifecycle; got != RuntimeLifecycleStarted {
		t.Fatalf("Lifecycle = %q, want %q", got, RuntimeLifecycleStarted)
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
	if err := rt.Start(context.Background()); err != nil {
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

func TestRealSendAsync_SignalsInterruptAfterFullPersistenceAndSkipsWakeFile(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{
		session: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	res, err := r.SendAsync(context.Background(), "alice", "hello", "world", "", nil)
	if err != nil {
		t.Fatalf("SendAsync() error: %v", err)
	}
	if res == nil || res.MessageID == "" {
		t.Fatalf("SendAsync() result = %+v, want non-empty message id", res)
	}

	snap := rt.Snapshot()
	if snap.InterruptCount != 1 {
		t.Fatalf("InterruptCount = %d, want 1 after async delivery", snap.InterruptCount)
	}
	if snap.WakeCount != 0 {
		t.Fatalf("WakeCount = %d, want 0 when async delivery uses interrupt-capable signal", snap.WakeCount)
	}

	wakePath := filepath.Join(tmpDir, ".sprawl", "agents", "alice.wake")
	if _, err := os.Stat(wakePath); !os.IsNotExist(err) {
		t.Fatalf("wake file should not exist for runtime-backed async delivery, stat err = %v", err)
	}
}

func TestRealSendAsync_QueueFailureDoesNotSignalRuntime(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{
		session: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	queuePath := filepath.Join(tmpDir, ".sprawl", "agents", "alice", "queue")
	if err := os.MkdirAll(filepath.Dir(queuePath), 0o755); err != nil {
		t.Fatalf("mkdir queue parent: %v", err)
	}
	if err := os.WriteFile(queuePath, []byte("block queue dir"), 0o644); err != nil {
		t.Fatalf("write queue blocker: %v", err)
	}

	_, err := r.SendAsync(context.Background(), "alice", "hello", "world", "", nil)
	if err == nil {
		t.Fatal("SendAsync() error = nil, want queue failure")
	}

	snap := rt.Snapshot()
	if snap.WakeCount != 0 || snap.InterruptCount != 0 {
		t.Fatalf("snapshot changed on failed SendAsync: %+v", snap)
	}
}

func TestRealSendAsync_MaildirFailureDoesNotSignalRuntime(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{
		session: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	maildirPath := filepath.Join(tmpDir, ".sprawl", "messages", "alice")
	if err := os.MkdirAll(filepath.Dir(maildirPath), 0o755); err != nil {
		t.Fatalf("mkdir maildir parent: %v", err)
	}
	if err := os.WriteFile(maildirPath, []byte("block maildir"), 0o644); err != nil {
		t.Fatalf("write maildir blocker: %v", err)
	}

	_, err := r.SendAsync(context.Background(), "alice", "hello", "world", "", nil)
	if err == nil {
		t.Fatal("SendAsync() error = nil, want maildir failure")
	}

	snap := rt.Snapshot()
	if snap.WakeCount != 0 || snap.InterruptCount != 0 {
		t.Fatalf("snapshot changed on maildir-failed SendAsync: %+v", snap)
	}
}

func TestRealSendInterrupt_SignalsInterruptAfterFullPersistenceAndSkipsWakeFile(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{
		session: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	res, err := r.SendInterrupt(context.Background(), "alice", "urgent", "stop", "resume later")
	if err != nil {
		t.Fatalf("SendInterrupt() error: %v", err)
	}
	if res == nil || !res.Interrupted {
		t.Fatalf("SendInterrupt() result = %+v, want interrupted result", res)
	}

	snap := rt.Snapshot()
	if snap.InterruptCount != 1 {
		t.Fatalf("InterruptCount = %d, want 1 after interrupt delivery", snap.InterruptCount)
	}
	if snap.WakeCount != 0 {
		t.Fatalf("WakeCount = %d, want 0 when interrupt delivery uses the interrupt-capable signal only", snap.WakeCount)
	}

	wakePath := filepath.Join(tmpDir, ".sprawl", "agents", "alice.wake")
	if _, err := os.Stat(wakePath); !os.IsNotExist(err) {
		t.Fatalf("wake file should not exist for runtime-backed interrupt delivery, stat err = %v", err)
	}
}

func TestRealSendInterrupt_QueueFailureDoesNotSignalRuntime(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{
		session: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	queuePath := filepath.Join(tmpDir, ".sprawl", "agents", "alice", "queue")
	if err := os.MkdirAll(filepath.Dir(queuePath), 0o755); err != nil {
		t.Fatalf("mkdir queue parent: %v", err)
	}
	if err := os.WriteFile(queuePath, []byte("block queue dir"), 0o644); err != nil {
		t.Fatalf("write queue blocker: %v", err)
	}

	_, err := r.SendInterrupt(context.Background(), "alice", "urgent", "stop", "resume later")
	if err == nil {
		t.Fatal("SendInterrupt() error = nil, want queue failure")
	}

	snap := rt.Snapshot()
	if snap.WakeCount != 0 || snap.InterruptCount != 0 {
		t.Fatalf("snapshot changed on failed SendInterrupt: %+v", snap)
	}
}

func TestRealSendInterrupt_MaildirFailureDoesNotSignalRuntime(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{
		session: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	maildirPath := filepath.Join(tmpDir, ".sprawl", "messages", "alice")
	if err := os.MkdirAll(filepath.Dir(maildirPath), 0o755); err != nil {
		t.Fatalf("mkdir maildir parent: %v", err)
	}
	if err := os.WriteFile(maildirPath, []byte("block maildir"), 0o644); err != nil {
		t.Fatalf("write maildir blocker: %v", err)
	}

	_, err := r.SendInterrupt(context.Background(), "alice", "urgent", "stop", "resume later")
	if err == nil {
		t.Fatal("SendInterrupt() error = nil, want maildir failure")
	}

	snap := rt.Snapshot()
	if snap.WakeCount != 0 || snap.InterruptCount != 0 {
		t.Fatalf("snapshot changed on maildir-failed SendInterrupt: %+v", snap)
	}
}

func TestRealReportStatus_SignalsParentRuntimeAfterFullPersistenceAndSkipsWakeFile(t *testing.T) {
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
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	res, err := r.ReportStatus(context.Background(), "bob", "working", "writing tests", "red phase")
	if err != nil {
		t.Fatalf("ReportStatus() error: %v", err)
	}
	if res == nil || res.ReportedAt == "" {
		t.Fatalf("ReportStatus() result = %+v, want reported timestamp", res)
	}

	snap := rt.Snapshot()
	if snap.InterruptCount != 1 {
		t.Fatalf("InterruptCount = %d, want 1 after report delivery", snap.InterruptCount)
	}
	if snap.WakeCount != 0 {
		t.Fatalf("WakeCount = %d, want 0 for report delivery interrupt path", snap.WakeCount)
	}

	wakePath := filepath.Join(tmpDir, ".sprawl", "agents", "alice.wake")
	if _, err := os.Stat(wakePath); !os.IsNotExist(err) {
		t.Fatalf("wake file should not exist for runtime-backed report delivery, stat err = %v", err)
	}
}

func TestRealReportStatus_QueueFailureDoesNotSignalParentRuntime(t *testing.T) {
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
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	queuePath := filepath.Join(tmpDir, ".sprawl", "agents", "alice", "queue")
	if err := os.MkdirAll(filepath.Dir(queuePath), 0o755); err != nil {
		t.Fatalf("mkdir queue parent: %v", err)
	}
	if err := os.WriteFile(queuePath, []byte("block queue dir"), 0o644); err != nil {
		t.Fatalf("write queue blocker: %v", err)
	}

	_, err := r.ReportStatus(context.Background(), "bob", "working", "writing tests", "red phase")
	if err == nil {
		t.Fatal("ReportStatus() error = nil, want queue failure")
	}

	snap := rt.Snapshot()
	if snap.WakeCount != 0 || snap.InterruptCount != 0 {
		t.Fatalf("snapshot changed on failed ReportStatus: %+v", snap)
	}
}

func TestRealReportStatus_MaildirFailureDoesNotSignalParentRuntime(t *testing.T) {
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
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	maildirPath := filepath.Join(tmpDir, ".sprawl", "messages", "alice")
	if err := os.MkdirAll(filepath.Dir(maildirPath), 0o755); err != nil {
		t.Fatalf("mkdir maildir parent: %v", err)
	}
	if err := os.WriteFile(maildirPath, []byte("block maildir"), 0o644); err != nil {
		t.Fatalf("write maildir blocker: %v", err)
	}

	_, err := r.ReportStatus(context.Background(), "bob", "working", "writing tests", "red phase")
	if err == nil {
		t.Fatal("ReportStatus() error = nil, want maildir failure")
	}

	snap := rt.Snapshot()
	if snap.WakeCount != 0 || snap.InterruptCount != 0 {
		t.Fatalf("snapshot changed on maildir-failed ReportStatus: %+v", snap)
	}
}

func TestRealReportStatus_UpdatesRuntimeAfterPersistedSuccess(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	rt := ensureRuntime(t, r, tmpDir, agentState)

	_, err := r.ReportStatus(context.Background(), "alice", "working", "writing tests", "red phase")
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
	_, err := r.ReportStatus(context.Background(), "alice", "working", "writing tests", "red phase")
	if err == nil {
		t.Fatal("ReportStatus() error = nil, want failure when state file is missing")
	}

	after := rt.Snapshot()
	if after != before {
		t.Fatalf("snapshot changed on failed ReportStatus: before=%+v after=%+v", before, after)
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
	if snap.Lifecycle != RuntimeLifecycleKilled {
		t.Fatalf("Lifecycle = %q, want %q", snap.Lifecycle, RuntimeLifecycleKilled)
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
	if err := rt.Start(context.Background()); err != nil {
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
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	agentPath := filepath.Join(state.AgentsDir(tmpDir), "alice.json")
	if err := os.Chmod(agentPath, 0o400); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	err := r.Kill(context.Background(), "alice")
	if err == nil {
		t.Fatal("Kill() error = nil, want failure after runtime stop")
	}
	if got := rt.Snapshot().Lifecycle; got == RuntimeLifecycleStarted {
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
	r.retireFn = func(_ *agentops.RetireDeps, _ string, _, _, _, _, _, _ bool) error { return nil }

	if err := r.Retire(context.Background(), "alice", false, false, true, false); err != nil {
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
	r.retireFn = func(*agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error {
		return errors.New("boom")
	}

	before := rt.Snapshot()
	err := r.Retire(context.Background(), "alice", false, false, false, false)
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
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	r.retireFn = func(*agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error {
		return errors.New("boom")
	}

	err := r.Retire(context.Background(), "alice", false, false, false, false)
	if err == nil {
		t.Fatal("Retire() error = nil, want boom")
	}
	if got := rt.Snapshot().Lifecycle; got == RuntimeLifecycleStarted {
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
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	r.retireFn = func(_ *agentops.RetireDeps, name string, cascade, force, abandon, mergeFirst, yes, noValidate bool) error {
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

	if err := r.Retire(context.Background(), "alice", false, false, false, false); err != nil {
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
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	r.retireFn = func(*agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error {
		t.Fatal("retireFn should not run when active children require --cascade")
		return nil
	}

	err := r.Retire(context.Background(), "alice", false, false, false, false)
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

func TestBuildRunnerDeps_InjectsChildTreePathAndNamespace(t *testing.T) {
	sprawlRoot := t.TempDir()
	state.WriteNamespace(sprawlRoot, "test-ns")
	deps := buildRunnerDeps(RuntimeStartSpec{
		Name:       "alice",
		Worktree:   filepath.Join(sprawlRoot, ".sprawl", "worktrees", "alice"),
		SprawlRoot: sprawlRoot,
		SessionID:  "sess-alice",
		TreePath:   "weave/alice",
	})

	if got := deps.Getenv("SPRAWL_AGENT_IDENTITY"); got != "alice" {
		t.Fatalf("SPRAWL_AGENT_IDENTITY = %q, want alice", got)
	}
	if got := deps.Getenv("SPRAWL_ROOT"); got != sprawlRoot {
		t.Fatalf("SPRAWL_ROOT = %q, want %q", got, sprawlRoot)
	}
	if got := deps.Getenv("SPRAWL_TREE_PATH"); got != "weave/alice" {
		t.Fatalf("SPRAWL_TREE_PATH = %q, want weave/alice", got)
	}
	if got := deps.Getenv("SPRAWL_NAMESPACE"); got != "test-ns" {
		t.Fatalf("SPRAWL_NAMESPACE = %q, want test-ns", got)
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
	r.retireFn = func(*agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error {
		return state.DeleteAgent(tmpDir, "alice")
	}

	if err := r.Retire(context.Background(), "alice", false, false, false, false); err != nil {
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
	if err := rt.Start(context.Background()); err != nil {
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
	if updated.Status != "killed" {
		t.Fatalf("Status = %q, want killed", updated.Status)
	}
	if session.stopCalls != 1 {
		t.Fatalf("runtime stop calls = %d, want 1", session.stopCalls)
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
	r.retireFn = func(_ *agentops.RetireDeps, _ string, _, _, _, _, _, _ bool) error {
		if err := state.DeleteAgent(tmpDir, "bob"); err != nil {
			return err
		}
		if err := state.DeleteAgent(tmpDir, "carol"); err != nil {
			return err
		}
		return errors.New("parent teardown failed")
	}

	err := r.Retire(context.Background(), "alice", false, false, true, false)
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

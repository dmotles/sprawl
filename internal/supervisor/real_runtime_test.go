package supervisor

import (
	"context"
	"errors"
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/state"
)

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

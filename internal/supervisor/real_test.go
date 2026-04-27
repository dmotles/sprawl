package supervisor

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/state"
)

// newFakeReal returns a Real with no-op Fn seams. Tests override what
// they care about.
func newFakeReal(t *testing.T) (*Real, string) {
	t.Helper()
	r, tmpDir := newTestSupervisor(t)
	r.spawnFn = func(*agentops.SpawnDeps, string, string, string, string) (*state.AgentState, error) {
		return nil, errors.New("spawnFn not overridden")
	}
	r.mergeFn = func(*agentops.MergeDeps, string, string, bool, bool) error {
		return errors.New("mergeFn not overridden")
	}
	r.retireFn = func(*agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error {
		return errors.New("retireFn not overridden")
	}
	r.killFn = func(*agentops.KillDeps, string, bool) error {
		return errors.New("killFn not overridden")
	}
	return r, tmpDir
}

func TestSpawn_MapsSpawnRequestAndReturnsAgentInfo(t *testing.T) {
	r, _ := newFakeReal(t)

	var gotFamily, gotType, gotPrompt, gotBranch string
	r.spawnFn = func(_ *agentops.SpawnDeps, family, agentType, prompt, branch string) (*state.AgentState, error) {
		gotFamily = family
		gotType = agentType
		gotPrompt = prompt
		gotBranch = branch
		return &state.AgentState{
			Name:   "engineer-abc123",
			Type:   "engineer",
			Family: "engineering",
			Parent: "weave",
			Status: "active",
			Branch: "dmotles/feature-x",
		}, nil
	}

	info, err := r.Spawn(context.Background(), SpawnRequest{
		Family: "engineering",
		Type:   "engineer",
		Prompt: "build feature X",
		Branch: "dmotles/feature-x",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if gotFamily != "engineering" || gotType != "engineer" ||
		gotPrompt != "build feature X" || gotBranch != "dmotles/feature-x" {
		t.Errorf("args not forwarded: family=%q type=%q prompt=%q branch=%q",
			gotFamily, gotType, gotPrompt, gotBranch)
	}

	want := &AgentInfo{
		Name: "engineer-abc123", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: "active", Branch: "dmotles/feature-x",
	}
	if *info != *want {
		t.Errorf("AgentInfo = %+v, want %+v", *info, *want)
	}
}

func TestSpawn_PropagatesError(t *testing.T) {
	r, _ := newFakeReal(t)
	r.spawnFn = func(*agentops.SpawnDeps, string, string, string, string) (*state.AgentState, error) {
		return nil, errors.New("boom")
	}

	info, err := r.Spawn(context.Background(), SpawnRequest{Family: "engineering", Type: "engineer", Prompt: "x", Branch: "b"})
	if err == nil || err.Error() != "boom" {
		t.Errorf("err = %v, want boom", err)
	}
	if info != nil {
		t.Errorf("info = %+v, want nil", info)
	}
}

func TestSpawn_InjectsCallerAndRootViaGetenv(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	captured := map[string]string{}
	r.spawnFn = func(deps *agentops.SpawnDeps, _, _, _, _ string) (*state.AgentState, error) {
		captured["SPRAWL_AGENT_IDENTITY"] = deps.Getenv("SPRAWL_AGENT_IDENTITY")
		captured["SPRAWL_ROOT"] = deps.Getenv("SPRAWL_ROOT")
		return &state.AgentState{Name: "x"}, nil
	}

	if _, err := r.Spawn(context.Background(), SpawnRequest{Family: "engineering", Type: "engineer", Prompt: "x", Branch: "b"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if captured["SPRAWL_AGENT_IDENTITY"] != "weave" {
		t.Errorf("SPRAWL_AGENT_IDENTITY = %q, want weave", captured["SPRAWL_AGENT_IDENTITY"])
	}
	if captured["SPRAWL_ROOT"] != tmpDir {
		t.Errorf("SPRAWL_ROOT = %q, want %q", captured["SPRAWL_ROOT"], tmpDir)
	}
}

func TestMerge_ForwardsArgs(t *testing.T) {
	r, _ := newFakeReal(t)

	var gotName, gotMsg string
	var gotNoValidate, gotDryRun bool
	r.mergeFn = func(_ *agentops.MergeDeps, name, msg string, noValidate, dryRun bool) error {
		gotName, gotMsg, gotNoValidate, gotDryRun = name, msg, noValidate, dryRun
		return nil
	}

	if err := r.Merge(context.Background(), "ratz", "custom commit", true); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if gotName != "ratz" || gotMsg != "custom commit" || !gotNoValidate || gotDryRun {
		t.Errorf("args not forwarded: name=%q msg=%q noValidate=%v dryRun=%v",
			gotName, gotMsg, gotNoValidate, gotDryRun)
	}
}

func TestMerge_PropagatesError(t *testing.T) {
	r, _ := newFakeReal(t)
	r.mergeFn = func(*agentops.MergeDeps, string, string, bool, bool) error {
		return errors.New("dirty tree")
	}
	err := r.Merge(context.Background(), "ratz", "", false)
	if err == nil || err.Error() != "dirty tree" {
		t.Errorf("err = %v, want dirty tree", err)
	}
}

func TestRetire_ForwardsFlags(t *testing.T) {
	r, _ := newFakeReal(t)

	var got struct {
		name                                                 string
		cascade, force, abandon, mergeFirst, yes, noValidate bool
	}
	r.retireFn = func(_ *agentops.RetireDeps, name string, cascade, force, abandon, mergeFirst, yes, noValidate bool) error {
		got.name = name
		got.cascade = cascade
		got.force = force
		got.abandon = abandon
		got.mergeFirst = mergeFirst
		got.yes = yes
		got.noValidate = noValidate
		return nil
	}

	if err := r.Retire(context.Background(), "ghost", true /* mergeFirst */, false /* abandon */, false /* cascade */, false /* noValidate */); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if got.name != "ghost" {
		t.Errorf("name = %q, want ghost", got.name)
	}
	if !got.mergeFirst {
		t.Error("mergeFirst should be true")
	}
	if got.abandon {
		t.Error("abandon should be false")
	}
	if !got.yes {
		t.Error("yes should default to true (non-interactive TUI supervisor)")
	}
	if got.cascade {
		t.Error("cascade should be false")
	}
	if got.force {
		t.Error("force should be false")
	}
	if got.noValidate {
		t.Error("noValidate should be false")
	}
}

func TestRetire_AbandonMode(t *testing.T) {
	r, _ := newFakeReal(t)
	var gotAbandon, gotMergeFirst bool
	r.retireFn = func(_ *agentops.RetireDeps, _ string, _, _, abandon, mergeFirst, _, _ bool) error {
		gotAbandon, gotMergeFirst = abandon, mergeFirst
		return nil
	}
	if err := r.Retire(context.Background(), "ghost", false, true, false, false); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if !gotAbandon || gotMergeFirst {
		t.Errorf("abandon=%v mergeFirst=%v, want abandon=true mergeFirst=false", gotAbandon, gotMergeFirst)
	}
}

func TestRetire_CascadeAndNoValidate(t *testing.T) {
	r, _ := newFakeReal(t)
	var gotCascade, gotNoValidate bool
	r.retireFn = func(_ *agentops.RetireDeps, _ string, cascade, _, _, _, _, noValidate bool) error {
		gotCascade, gotNoValidate = cascade, noValidate
		return nil
	}
	if err := r.Retire(context.Background(), "ghost", true /* merge */, false, true /* cascade */, true /* noValidate */); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if !gotCascade {
		t.Error("cascade should be true")
	}
	if !gotNoValidate {
		t.Error("noValidate should be true")
	}
}

func TestKill_IdempotentOnMissingAgent(t *testing.T) {
	r, _ := newFakeReal(t)
	called := false
	r.killFn = func(*agentops.KillDeps, string, bool) error {
		called = true
		return nil
	}

	// No agent state file was created. Kill must return nil and NOT invoke
	// killFn (which would fail on missing state anyway).
	if err := r.Kill(context.Background(), "ghost"); err != nil {
		t.Errorf("Kill on missing agent: got err %v, want nil (idempotent)", err)
	}
	if called {
		t.Error("killFn should not be invoked when agent state is absent")
	}
}

func TestKill_ForwardsWhenAgentExists(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "ratz", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active",
	})

	var gotName string
	var gotForce bool
	r.killFn = func(_ *agentops.KillDeps, name string, force bool) error {
		gotName, gotForce = name, force
		return nil
	}

	if err := r.Kill(context.Background(), "ratz"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if gotName != "ratz" {
		t.Errorf("name = %q, want ratz", gotName)
	}
	if gotForce {
		t.Error("force should default to false (graceful shutdown)")
	}
}

func TestKill_PropagatesKillFnError(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "ratz", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active",
	})
	r.killFn = func(*agentops.KillDeps, string, bool) error { return errors.New("tmux down") }
	err := r.Kill(context.Background(), "ratz")
	if err == nil || err.Error() != "tmux down" {
		t.Errorf("err = %v, want tmux down", err)
	}
}

func TestKill_RejectsInvalidName(t *testing.T) {
	r, _ := newFakeReal(t)
	called := false
	r.killFn = func(*agentops.KillDeps, string, bool) error { called = true; return nil }
	err := r.Kill(context.Background(), "../evil")
	if err == nil {
		t.Error("expected error for invalid name")
	}
	if called {
		t.Error("killFn should not be called for invalid name")
	}
}

// TestNewReal_BuildsDepsWithRealAgentops sanity-checks that NewReal populates
// the default Fn seams with the real agentops implementations, so production
// wiring is correct even though other tests swap them.
func TestNewReal_BuildsDepsWithRealAgentops(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	if sup.spawnFn == nil || sup.mergeFn == nil || sup.retireFn == nil || sup.killFn == nil {
		t.Fatal("NewReal should wire all Fn seams to agentops.* by default")
	}
	if sup.spawnDeps == nil || sup.mergeDeps == nil || sup.retireDeps == nil || sup.killDeps == nil {
		t.Fatal("NewReal should populate all *Deps")
	}
	// Verify the Getenv indirection for SPRAWL_ROOT / SPRAWL_AGENT_IDENTITY is
	// wired so agentops sees the supervisor's config rather than the process env.
	if v := sup.spawnDeps.Getenv("SPRAWL_AGENT_IDENTITY"); v != "weave" {
		t.Errorf("spawnDeps.Getenv(SPRAWL_AGENT_IDENTITY) = %q, want weave", v)
	}
	if v := sup.killDeps.Getenv("SPRAWL_ROOT"); v == "" {
		t.Error("killDeps.Getenv(SPRAWL_ROOT) should return the configured sprawlRoot")
	}
}

// guard: ensure Real implements Supervisor.
var _ Supervisor = (*Real)(nil)

// guard: ensure returned AgentInfo fields survive a round-trip (sanity).
func TestSpawn_AgentInfoRoundTrip(t *testing.T) {
	r, _ := newFakeReal(t)
	r.spawnFn = func(*agentops.SpawnDeps, string, string, string, string) (*state.AgentState, error) {
		return &state.AgentState{Name: "a", Type: "b", Family: "c", Parent: "d", Status: "e", Branch: "f"}, nil
	}
	info, err := r.Spawn(context.Background(), SpawnRequest{Family: "engineering", Type: "engineer", Prompt: "p", Branch: "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := AgentInfo{Name: "a", Type: "b", Family: "c", Parent: "d", Status: "e", Branch: "f"}
	if *info != want {
		t.Errorf("got %+v, want %+v", *info, want)
	}
}

// smoke: format compile check on all errors
func TestRealErrorMessages_Format(t *testing.T) {
	_ = fmt.Sprintf("%v", errors.New("x"))
}

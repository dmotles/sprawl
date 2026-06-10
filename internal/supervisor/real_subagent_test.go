package supervisor

import (
	"context"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
)

// countingWorktreeCreator is a Creator that counts invocations. Sub-agent
// spawns must NEVER call Create — they reuse the parent's worktree (QUM-709).
type countingWorktreeCreator struct {
	calls int
	path  string
}

func (c *countingWorktreeCreator) Create(_, _, branchName, _ string) (string, string, error) {
	c.calls++
	return c.path, branchName, nil
}

// subagentSpawnDeps returns a SpawnDeps wired against tmpDir suitable for
// driving agentops.PrepareSpawn in subagent tests. The caller identity is
// supplied via context, so Getenv("SPRAWL_AGENT_IDENTITY") falls through to
// r.spawnDepsForCaller's override.
func subagentSpawnDeps(tmpDir string, wc *countingWorktreeCreator) *agentops.SpawnDeps {
	return &agentops.SpawnDeps{
		WorktreeCreator: wc,
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		CurrentBranch: func(string) (string, error) { return "main", nil },
		NewSpawnLock: func(string) (func() error, func() error) {
			return func() error { return nil }, func() error { return nil }
		},
		LoadConfig:      func(string) (*config.Config, error) { return &config.Config{}, nil },
		RunScript:       func(string, string, map[string]string) ([]byte, error) { return nil, nil },
		WorktreeRemove:  func(string, string, bool) error { return nil },
		GitBranchDelete: func(string, string) error { return nil },
	}
}

// TestRealSpawn_Subagent_SharesParentWorktreeAndBranch locks the QUM-709
// contract: when SpawnRequest.Subagent=true and the caller is a real on-disk
// agent of an allowed type, the child shares the parent's worktree and branch
// verbatim. No worktree is created.
func TestRealSpawn_Subagent_SharesParentWorktreeAndBranch(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	r.gitRevParseHEAD = func(string) (string, error) { return "", nil }

	parentWT := tmpDir + "/parent-wt"
	parentBranch := "dmotles/parent-feature"
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:     "runner-1",
		Type:     "engineer",
		Family:   "engineering",
		Parent:   "weave",
		Branch:   parentBranch,
		Worktree: parentWT,
		Status:   "active",
		TreePath: "weave/runner-1",
		Subagent: false,
	})

	wc := &countingWorktreeCreator{path: "/should/not/be/used"}
	r.spawnFn = agentops.PrepareSpawn
	r.spawnDeps = subagentSpawnDeps(tmpDir, wc)

	ctx := backendpkg.WithCallerIdentity(context.Background(), "runner-1")
	info, err := r.Spawn(ctx, SpawnRequest{
		Subagent: true,
		Family:   "engineering",
		Type:     "engineer",
		Prompt:   "do x",
	})
	if err != nil {
		t.Fatalf("Spawn(subagent): %v", err)
	}

	if !info.Subagent {
		t.Errorf("info.Subagent = false, want true")
	}
	if info.SharedWorktreeWith != "runner-1" {
		t.Errorf("info.SharedWorktreeWith = %q, want %q", info.SharedWorktreeWith, "runner-1")
	}

	sub, err := state.LoadAgent(tmpDir, info.Name)
	if err != nil {
		t.Fatalf("LoadAgent(%s): %v", info.Name, err)
	}
	if sub.Worktree != parentWT {
		t.Errorf("sub.Worktree = %q, want %q (must share parent's verbatim)", sub.Worktree, parentWT)
	}
	if sub.Branch != parentBranch {
		t.Errorf("sub.Branch = %q, want %q (must share parent's verbatim)", sub.Branch, parentBranch)
	}
	if !sub.Subagent {
		t.Errorf("sub.Subagent = false, want true")
	}
	if sub.Parent != "runner-1" {
		t.Errorf("sub.Parent = %q, want %q", sub.Parent, "runner-1")
	}
	if wc.calls != 0 {
		t.Errorf("WorktreeCreator.Create calls = %d, want 0 (sub-agent must not create a worktree)", wc.calls)
	}
}

// TestRealSpawn_Subagent_RootCannotHost locks: the root weave (no AgentState
// on disk for the caller) cannot host sub-agents. The error must identify the
// caller by name.
func TestRealSpawn_Subagent_RootCannotHost(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	r.gitRevParseHEAD = func(string) (string, error) { return "", nil }

	wc := &countingWorktreeCreator{path: "/unused"}
	r.spawnFn = agentops.PrepareSpawn
	r.spawnDeps = subagentSpawnDeps(tmpDir, wc)

	ctx := backendpkg.WithCallerIdentity(context.Background(), "weave")
	_, err := r.Spawn(ctx, SpawnRequest{
		Subagent: true,
		Family:   "engineering",
		Type:     "engineer",
		Prompt:   "x",
	})
	if err == nil {
		t.Fatal("Spawn(subagent) from root: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "root cannot host sub-agents") {
		t.Errorf("error %q missing %q", err, "root cannot host sub-agents")
	}
	if !strings.Contains(err.Error(), "weave") {
		t.Errorf("error %q missing caller name %q", err, "weave")
	}
}

// TestRealSpawn_Subagent_BranchRejected locks: when Subagent=true, the Branch
// field must be empty. A non-empty Branch is rejected with a precise error.
func TestRealSpawn_Subagent_BranchRejected(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	r.gitRevParseHEAD = func(string) (string, error) { return "", nil }

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:     "runner-1",
		Type:     "engineer",
		Family:   "engineering",
		Parent:   "weave",
		Branch:   "dmotles/parent-feature",
		Worktree: tmpDir + "/parent-wt",
		Status:   "active",
		TreePath: "weave/runner-1",
	})

	wc := &countingWorktreeCreator{path: "/unused"}
	r.spawnFn = agentops.PrepareSpawn
	r.spawnDeps = subagentSpawnDeps(tmpDir, wc)

	ctx := backendpkg.WithCallerIdentity(context.Background(), "runner-1")
	_, err := r.Spawn(ctx, SpawnRequest{
		Subagent: true,
		Family:   "engineering",
		Type:     "engineer",
		Prompt:   "x",
		Branch:   "should-not-be-set",
	})
	if err == nil {
		t.Fatal("Spawn(subagent) with Branch set: expected error, got nil")
	}
	want := "branch must not be set when subagent is true; sub-agents share the parent's branch"
	if err.Error() != want && !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want exact substring %q", err.Error(), want)
	}
}

// TestRealSpawn_Subagent_DepthCapAtThree locks: the consecutive-subagent
// chain depth is capped at MaxSubagentChainDepth (3). When the parent chain
// already has 3 sub-agents stacked, further sub-agent spawns are rejected.
func TestRealSpawn_Subagent_DepthCapAtThree(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	r.gitRevParseHEAD = func(string) (string, error) { return "", nil }

	parentWT := tmpDir + "/parent-wt"
	parentBranch := "dmotles/parent-feature"
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:     "parent",
		Type:     "engineer",
		Family:   "engineering",
		Parent:   "weave",
		Branch:   parentBranch,
		Worktree: parentWT,
		Status:   "active",
		Subagent: false,
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:     "sub1",
		Type:     "engineer",
		Family:   "engineering",
		Parent:   "parent",
		Branch:   parentBranch,
		Worktree: parentWT,
		Status:   "active",
		Subagent: true,
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:     "sub2",
		Type:     "engineer",
		Family:   "engineering",
		Parent:   "sub1",
		Branch:   parentBranch,
		Worktree: parentWT,
		Status:   "active",
		Subagent: true,
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:     "sub3",
		Type:     "engineer",
		Family:   "engineering",
		Parent:   "sub2",
		Branch:   parentBranch,
		Worktree: parentWT,
		Status:   "active",
		Subagent: true,
	})

	wc := &countingWorktreeCreator{path: "/unused"}
	r.spawnFn = agentops.PrepareSpawn
	r.spawnDeps = subagentSpawnDeps(tmpDir, wc)

	ctx := backendpkg.WithCallerIdentity(context.Background(), "sub3")
	_, err := r.Spawn(ctx, SpawnRequest{
		Subagent: true,
		Family:   "engineering",
		Type:     "engineer",
		Prompt:   "x",
	})
	if err == nil {
		t.Fatal("Spawn(subagent) at depth 3: expected error, got nil")
	}
	for _, want := range []string{
		"sub-agent depth limit (3) reached",
		"collapse work into the current sub-agent or escalate",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing substring %q", err.Error(), want)
		}
	}
}

// TestRealSpawn_Subagent_TypeNotPermitted locks: only types in
// AgentTypesAllowedToSpawnSubAgents (manager/engineer/researcher/qa) may host
// sub-agents. A "tester" parent must be rejected.
func TestRealSpawn_Subagent_TypeNotPermitted(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	r.gitRevParseHEAD = func(string) (string, error) { return "", nil }

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:     "tester-1",
		Type:     "tester",
		Family:   "engineering",
		Parent:   "weave",
		Branch:   "dmotles/tester",
		Worktree: tmpDir + "/tester-wt",
		Status:   "active",
	})

	wc := &countingWorktreeCreator{path: "/unused"}
	r.spawnFn = agentops.PrepareSpawn
	r.spawnDeps = subagentSpawnDeps(tmpDir, wc)

	ctx := backendpkg.WithCallerIdentity(context.Background(), "tester-1")
	_, err := r.Spawn(ctx, SpawnRequest{
		Subagent: true,
		Family:   "engineering",
		Type:     "engineer",
		Prompt:   "x",
	})
	if err == nil {
		t.Fatal("Spawn(subagent) from tester: expected error, got nil")
	}
	if !strings.Contains(err.Error(), `agent type "tester" is not permitted to spawn sub-agents`) {
		t.Errorf("error %q missing expected disallowed-type substring", err.Error())
	}
}

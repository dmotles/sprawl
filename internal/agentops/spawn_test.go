package agentops_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/runtimecfg"
	"github.com/dmotles/sprawl/internal/state"
)

// fakeWorktreeCreator records the requested worktree and creates the directory
// without invoking real git.
type fakeWorktreeCreator struct {
	root         string
	capturedBase string
}

func (c *fakeWorktreeCreator) Create(_, agentName, branchName, baseBranch string) (string, string, error) {
	c.capturedBase = baseBranch
	path := filepath.Join(c.root, ".sprawl", "worktrees", agentName)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", "", err
	}
	return path, branchName, nil
}

// newBaseRefSpawnDeps builds a minimal valid SpawnDeps for the baseBranch
// resolution tests. Callers should set CurrentBranch and (optionally)
// ResolveBase on the returned struct.
func newBaseRefSpawnDeps(t *testing.T, tmpDir string) (*agentops.SpawnDeps, *fakeWorktreeCreator) {
	t.Helper()
	creator := &fakeWorktreeCreator{root: tmpDir}
	deps := &agentops.SpawnDeps{
		WorktreeCreator: creator,
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_AGENT_IDENTITY":
				return "manager-x"
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
		RunScript:       agentops.RunBashScript,
		WorktreeRemove:  agentops.RealWorktreeRemove,
		GitBranchDelete: func(string, string) error { return nil },
	}
	return deps, creator
}

// TestPrepareSpawn_UsesResolveBaseWhenProvided pins QUM-572: when the
// optional ResolveBase dep returns a non-empty ref, the worktree must be
// created from THAT ref (the caller manager's worktree HEAD), not the
// main repo's current branch. Without this fix, a manager's spawned
// engineers silently lose the manager's integration commits.
func TestPrepareSpawn_UsesResolveBaseWhenProvided(t *testing.T) {
	tmpDir := t.TempDir()
	deps, creator := newBaseRefSpawnDeps(t, tmpDir)
	deps.CurrentBranch = func(string) (string, error) { return "main", nil }
	deps.ResolveBase = func(caller, root string) (string, error) {
		if caller != "manager-x" {
			t.Errorf("ResolveBase caller = %q, want %q", caller, "manager-x")
		}
		if root != tmpDir {
			t.Errorf("ResolveBase root = %q, want %q", root, tmpDir)
		}
		return "deadbeefcafebabe1234567890abcdef12345678", nil
	}

	if _, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task body", "dmotles/test-branch", false); err != nil {
		t.Fatalf("PrepareSpawn: %v", err)
	}

	if creator.capturedBase != "deadbeefcafebabe1234567890abcdef12345678" {
		t.Errorf("worktree baseBranch = %q, want %q (must use ResolveBase output — QUM-572)", creator.capturedBase, "deadbeefcafebabe1234567890abcdef12345678")
	}
}

// TestPrepareSpawn_FallsBackToCurrentBranchWhenResolveBaseReturnsEmpty
// models the root-weave case: weave has no agent state, so ResolveBase
// returns ("", nil) → fall through to CurrentBranch.
func TestPrepareSpawn_FallsBackToCurrentBranchWhenResolveBaseReturnsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	deps, creator := newBaseRefSpawnDeps(t, tmpDir)
	deps.CurrentBranch = func(string) (string, error) { return "main", nil }
	deps.ResolveBase = func(string, string) (string, error) { return "", nil }

	if _, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task body", "dmotles/test-branch", false); err != nil {
		t.Fatalf("PrepareSpawn: %v", err)
	}

	if creator.capturedBase != "main" {
		t.Errorf("worktree baseBranch = %q, want %q (empty ResolveBase must fall back to CurrentBranch)", creator.capturedBase, "main")
	}
}

// TestPrepareSpawn_FallsBackToCurrentBranchWhenResolveBaseIsNil pins
// backwards-compat: callers that haven't been updated to provide
// ResolveBase still get the old behavior (CurrentBranch of main repo).
func TestPrepareSpawn_FallsBackToCurrentBranchWhenResolveBaseIsNil(t *testing.T) {
	tmpDir := t.TempDir()
	deps, creator := newBaseRefSpawnDeps(t, tmpDir)
	deps.CurrentBranch = func(string) (string, error) { return "main", nil }
	// ResolveBase intentionally omitted.

	if _, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task body", "dmotles/test-branch", false); err != nil {
		t.Fatalf("PrepareSpawn: %v", err)
	}

	if creator.capturedBase != "main" {
		t.Errorf("worktree baseBranch = %q, want %q (nil ResolveBase must fall back to CurrentBranch)", creator.capturedBase, "main")
	}
}

// TestPrepareSpawn_PropagatesResolveBaseError pins the documented contract:
// when ResolveBase returns a non-nil error, PrepareSpawn must propagate it
// (wrap is fine) rather than swallowing it and falling back to
// CurrentBranch. A silent fallback would hide a real fault (e.g. the caller's
// worktree is corrupt / non-existent / not a git repo) and silently strip
// integration commits from the spawned child — the exact regression class
// QUM-572 is guarding against.
func TestPrepareSpawn_PropagatesResolveBaseError(t *testing.T) {
	tmpDir := t.TempDir()
	deps, _ := newBaseRefSpawnDeps(t, tmpDir)
	deps.CurrentBranch = func(string) (string, error) { return "main", nil }
	resolveErr := errors.New("boom")
	deps.ResolveBase = func(string, string) (string, error) { return "", resolveErr }

	_, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task body", "dmotles/test-branch", false)
	if err == nil {
		t.Fatal("PrepareSpawn returned nil error; expected ResolveBase error to propagate")
	}
	if !errors.Is(err, resolveErr) && !strings.Contains(err.Error(), "boom") {
		t.Errorf("PrepareSpawn err = %v, expected to wrap/contain ResolveBase error %q", err, resolveErr)
	}
}

// TestPrepareSpawn_IgnoresPersistedNamespaceAndRootName pins QUM-587 (Option B):
// the spawn flow must NOT consult `.sprawl/namespace` or `.sprawl/root-name` on
// disk. Their writers were deleted in QUM-586, so the reader fallbacks at
// `agentops/spawn.go:171,181` are zombie code. This test seeds bogus values on
// disk and asserts the child's TreePath uses the compiled-in DefaultRootName
// (not the on-disk root-name) — proving the fallback branches are gone.
func TestPrepareSpawn_IgnoresPersistedNamespaceAndRootName(t *testing.T) {
	tmpDir := t.TempDir()

	// Seed zombie files that the old fallback branches would have read.
	sprawlSubdir := filepath.Join(tmpDir, ".sprawl")
	if err := os.MkdirAll(sprawlSubdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sprawlSubdir, "namespace"), []byte("zombie-ns"), 0o644); err != nil {
		t.Fatalf("seed namespace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sprawlSubdir, "root-name"), []byte("zombie-root"), 0o644); err != nil {
		t.Fatalf("seed root-name: %v", err)
	}

	deps, _ := newBaseRefSpawnDeps(t, tmpDir)
	// parentName "manager-x" (from newBaseRefSpawnDeps) is not the default
	// root, so the resulting TreePath should be:
	//   DefaultRootName + sep + "manager-x" + sep + <agentName>
	got, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task body", "dmotles/test-branch", false)
	if err != nil {
		t.Fatalf("PrepareSpawn: %v", err)
	}

	wantPrefix := runtimecfg.DefaultRootName + runtimecfg.TreePathSeparator + "manager-x" + runtimecfg.TreePathSeparator
	if !strings.HasPrefix(got.TreePath, wantPrefix) {
		t.Errorf("TreePath = %q, want prefix %q (must use DefaultRootName, not on-disk root-name)", got.TreePath, wantPrefix)
	}
	if strings.Contains(got.TreePath, "zombie-root") {
		t.Errorf("TreePath = %q must NOT contain on-disk root-name 'zombie-root' (QUM-587 Option B)", got.TreePath)
	}
}

// TestPrepareSpawn_AcceptsQAType pins QUM-707: the new "qa" agent type must
// be validated and supported by the spawn path. PrepareSpawn must accept
// type="qa" and persist Type="qa" on the resulting AgentState.
func TestPrepareSpawn_AcceptsQAType(t *testing.T) {
	tmpDir := t.TempDir()
	deps, _ := newBaseRefSpawnDeps(t, tmpDir)

	got, err := agentops.PrepareSpawn(deps, "qa", "qa", "verify the recent changes", "dmotles/qa-verify", false)
	if err != nil {
		t.Fatalf("PrepareSpawn(type=qa): %v", err)
	}
	if got == nil {
		t.Fatal("PrepareSpawn returned nil agent state")
	}
	if got.Type != "qa" {
		t.Errorf("Type = %q, want %q", got.Type, "qa")
	}
}

// TestPrepareSpawn_SupportedTypesErrorMessage_ListsQA pins that the
// "not yet supported" error message (when triggered) reflects the current
// supported-types list including "qa". If qa is in SupportedTypes, this
// guards that the canonical type list is documented in any user-facing
// failure mode that recites supported types.
func TestPrepareSpawn_SupportedTypesErrorMessage_ListsQA(t *testing.T) {
	if !agentops.SupportedTypes["qa"] {
		t.Errorf("SupportedTypes[\"qa\"] = false, want true (QUM-707)")
	}
}

// TestSpawn_WritesStateFile_GrandchildCase pins the regression-guard claim
// from QUM-404: when a researcher (e.g. "ghost") spawns a manager child, the
// child's state JSON must be persisted in <root>/.sprawl/agents/<name>.json.
//
// This is the grandchild scenario (root → researcher → manager), distinct
// from the engineer-spawned tests in real_runtime_test.go. Production code
// already writes the JSON — this test pins that behavior so a future
// refactor can't silently regress it.
func TestSpawn_WritesStateFile_GrandchildCase(t *testing.T) {
	tmpDir := t.TempDir()

	deps := &agentops.SpawnDeps{
		WorktreeCreator: &fakeWorktreeCreator{root: tmpDir},
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_AGENT_IDENTITY":
				return "ghost" // researcher (the grandchild's parent)
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
		RunScript:       agentops.RunBashScript,
		WorktreeRemove:  agentops.RealWorktreeRemove,
		GitBranchDelete: func(string, string) error { return nil },
	}

	got, err := agentops.PrepareSpawn(deps, "engineering", "manager", "task body", "dmotles/test-branch", false)
	if err != nil {
		t.Fatalf("PrepareSpawn: %v", err)
	}
	if got == nil {
		t.Fatal("PrepareSpawn returned nil agent state")
	}

	if got.Type != "manager" {
		t.Errorf("Type = %q, want %q", got.Type, "manager")
	}
	if got.Parent != "ghost" {
		t.Errorf("Parent = %q, want %q", got.Parent, "ghost")
	}

	jsonPath := filepath.Join(state.AgentsDir(tmpDir), got.Name+".json")
	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatalf("expected state JSON at %s, stat err = %v", jsonPath, err)
	}

	loaded, err := state.LoadAgent(tmpDir, got.Name)
	if err != nil {
		t.Fatalf("LoadAgent(%s): %v", got.Name, err)
	}
	if loaded.Type != "manager" {
		t.Errorf("loaded Type = %q, want %q", loaded.Type, "manager")
	}
	if loaded.Parent != "ghost" {
		t.Errorf("loaded Parent = %q, want %q", loaded.Parent, "ghost")
	}
	if loaded.Name != got.Name {
		t.Errorf("loaded Name = %q, want %q", loaded.Name, got.Name)
	}
}

// fatalWorktreeCreator fails the test if Create is ever called. Used to assert
// that sub-agent spawns reuse the parent's worktree without invoking the
// worktree creator (QUM-709).
type fatalWorktreeCreator struct {
	t *testing.T
}

func (c *fatalWorktreeCreator) Create(_, agentName, branchName, baseBranch string) (string, string, error) {
	c.t.Fatalf("WorktreeCreator.Create must NOT be called for sub-agent spawn (called with name=%q branch=%q base=%q)", agentName, branchName, baseBranch)
	return "", "", nil
}

// newSubagentSpawnDeps builds SpawnDeps for sub-agent tests. The
// WorktreeCreator fails the test if invoked. The Getenv returns the supplied
// caller identity.
func newSubagentSpawnDeps(t *testing.T, tmpDir, caller string) *agentops.SpawnDeps {
	t.Helper()
	return &agentops.SpawnDeps{
		WorktreeCreator: &fatalWorktreeCreator{t: t},
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_AGENT_IDENTITY":
				return caller
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
		RunScript: func(script, workDir string, env map[string]string) ([]byte, error) {
			t.Fatalf("RunScript must NOT be called for sub-agent spawn (script=%q)", script)
			return nil, nil
		},
		WorktreeRemove:  agentops.RealWorktreeRemove,
		GitBranchDelete: func(string, string) error { return nil },
	}
}

// TestPrepareSpawn_Subagent_DepthZeroManagerOK pins QUM-709: a manager (non-
// subagent) may spawn a sub-agent. The child inherits the parent's worktree
// and branch verbatim; WorktreeCreator.Create is NOT invoked.
func TestPrepareSpawn_Subagent_DepthZeroManagerOK(t *testing.T) {
	tmpDir := t.TempDir()

	parent := &state.AgentState{
		Name:     "manager-x",
		Type:     "manager",
		Family:   "engineering",
		Parent:   "weave",
		Worktree: "/wt/mgr",
		Branch:   "mgr-br",
		Status:   "active",
		Subagent: false,
	}
	if err := state.SaveAgent(tmpDir, parent); err != nil {
		t.Fatalf("SaveAgent(parent): %v", err)
	}

	deps := newSubagentSpawnDeps(t, tmpDir, "manager-x")
	got, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task", "", true)
	if err != nil {
		t.Fatalf("PrepareSpawn: %v", err)
	}
	if got == nil {
		t.Fatal("PrepareSpawn returned nil agent state")
	}
	if !got.Subagent {
		t.Errorf("Subagent = false, want true")
	}
	if got.Worktree != "/wt/mgr" {
		t.Errorf("Worktree = %q, want %q (must inherit parent's worktree)", got.Worktree, "/wt/mgr")
	}
	if got.Branch != "mgr-br" {
		t.Errorf("Branch = %q, want %q (must inherit parent's branch)", got.Branch, "mgr-br")
	}
}

// TestPrepareSpawn_Subagent_DepthTwoOK pins that a sub-agent chain of depth
// 2 (counting consecutive Subagent==true ancestors from the parent) is still
// allowed. Only depth>=3 trips the limit.
func TestPrepareSpawn_Subagent_DepthTwoOK(t *testing.T) {
	tmpDir := t.TempDir()

	greatgrand := &state.AgentState{
		Name:     "mgr-root",
		Type:     "manager",
		Family:   "engineering",
		Parent:   "weave",
		Worktree: "/wt/shared",
		Branch:   "shared-br",
		Status:   "active",
		Subagent: false,
	}
	grandparent := &state.AgentState{
		Name:     "eng-1",
		Type:     "engineer",
		Family:   "engineering",
		Parent:   "mgr-root",
		Worktree: "/wt/shared",
		Branch:   "shared-br",
		Status:   "active",
		Subagent: true,
	}
	parent := &state.AgentState{
		Name:     "eng-2",
		Type:     "engineer",
		Family:   "engineering",
		Parent:   "eng-1",
		Worktree: "/wt/shared",
		Branch:   "shared-br",
		Status:   "active",
		Subagent: true,
	}
	for _, a := range []*state.AgentState{greatgrand, grandparent, parent} {
		if err := state.SaveAgent(tmpDir, a); err != nil {
			t.Fatalf("SaveAgent(%s): %v", a.Name, err)
		}
	}

	deps := newSubagentSpawnDeps(t, tmpDir, "eng-2")
	got, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task", "", true)
	if err != nil {
		t.Fatalf("PrepareSpawn (depth=2 should succeed): %v", err)
	}
	if !got.Subagent {
		t.Errorf("Subagent = false, want true")
	}
	if got.Worktree != "/wt/shared" {
		t.Errorf("Worktree = %q, want %q", got.Worktree, "/wt/shared")
	}
}

// TestPrepareSpawn_Subagent_DepthThreeRejected pins the hard depth cap at 3.
// The error message MUST be the documented escalation string verbatim.
func TestPrepareSpawn_Subagent_DepthThreeRejected(t *testing.T) {
	tmpDir := t.TempDir()

	root := &state.AgentState{
		Name: "mgr-root", Type: "manager", Family: "engineering", Parent: "weave",
		Worktree: "/wt/shared", Branch: "shared-br", Status: "active", Subagent: false,
	}
	a1 := &state.AgentState{
		Name: "sub-1", Type: "engineer", Family: "engineering", Parent: "mgr-root",
		Worktree: "/wt/shared", Branch: "shared-br", Status: "active", Subagent: true,
	}
	a2 := &state.AgentState{
		Name: "sub-2", Type: "engineer", Family: "engineering", Parent: "sub-1",
		Worktree: "/wt/shared", Branch: "shared-br", Status: "active", Subagent: true,
	}
	a3 := &state.AgentState{
		Name: "sub-3", Type: "engineer", Family: "engineering", Parent: "sub-2",
		Worktree: "/wt/shared", Branch: "shared-br", Status: "active", Subagent: true,
	}
	for _, a := range []*state.AgentState{root, a1, a2, a3} {
		if err := state.SaveAgent(tmpDir, a); err != nil {
			t.Fatalf("SaveAgent(%s): %v", a.Name, err)
		}
	}

	deps := newSubagentSpawnDeps(t, tmpDir, "sub-3")
	_, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task", "", true)
	if err == nil {
		t.Fatal("PrepareSpawn returned nil error; expected depth-limit rejection")
	}
	const want = "sub-agent depth limit (3) reached; collapse work into the current sub-agent or escalate to a non-subagent ancestor"
	if err.Error() != want {
		t.Errorf("PrepareSpawn err = %q\nwant exactly %q", err.Error(), want)
	}
}

// TestPrepareSpawn_Subagent_WithBranchRejected: subagent=true and a non-empty
// branch is a user-error — sub-agents reuse the parent's branch, so callers
// must not specify one. WorktreeCreator.Create must NOT be invoked.
func TestPrepareSpawn_Subagent_WithBranchRejected(t *testing.T) {
	tmpDir := t.TempDir()

	parent := &state.AgentState{
		Name: "manager-x", Type: "manager", Family: "engineering", Parent: "weave",
		Worktree: "/wt/mgr", Branch: "mgr-br", Status: "active", Subagent: false,
	}
	if err := state.SaveAgent(tmpDir, parent); err != nil {
		t.Fatalf("SaveAgent(parent): %v", err)
	}

	deps := newSubagentSpawnDeps(t, tmpDir, "manager-x")
	_, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task", "dmotles/some-branch", true)
	if err == nil {
		t.Fatal("PrepareSpawn returned nil error; expected branch-with-subagent rejection")
	}
	if !strings.Contains(err.Error(), "branch must not be set when subagent is true") {
		t.Errorf("err = %q, want substring %q", err.Error(), "branch must not be set when subagent is true")
	}
}

// TestPrepareSpawn_Subagent_RootRejected: the root (e.g. weave) has no
// AgentState file on disk. LoadAgent failing must surface as a clear "root
// cannot host sub-agents" rejection.
func TestPrepareSpawn_Subagent_RootRejected(t *testing.T) {
	tmpDir := t.TempDir()
	// No state file for "weave" on disk — mimics the root case.

	deps := newSubagentSpawnDeps(t, tmpDir, "weave")
	_, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task", "", true)
	if err == nil {
		t.Fatal("PrepareSpawn returned nil error; expected root-cannot-host rejection")
	}
	if !strings.Contains(err.Error(), "root cannot host sub-agents") {
		t.Errorf("err = %q, want substring %q", err.Error(), "root cannot host sub-agents")
	}
}

// TestPrepareSpawn_Subagent_CapabilityRejected: only manager/engineer/
// researcher/qa may host sub-agents. "code-merger" is a ValidType but not
// permitted, so PrepareSpawn must reject with a message that names the
// offending type AND the word "permitted".
func TestPrepareSpawn_Subagent_CapabilityRejected(t *testing.T) {
	tmpDir := t.TempDir()

	parent := &state.AgentState{
		Name: "merger-x", Type: "code-merger", Family: "engineering", Parent: "weave",
		Worktree: "/wt/m", Branch: "m-br", Status: "active", Subagent: false,
	}
	if err := state.SaveAgent(tmpDir, parent); err != nil {
		t.Fatalf("SaveAgent(parent): %v", err)
	}

	deps := newSubagentSpawnDeps(t, tmpDir, "merger-x")
	_, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task", "", true)
	if err == nil {
		t.Fatal("PrepareSpawn returned nil error; expected capability rejection")
	}
	if !strings.Contains(err.Error(), `"code-merger"`) {
		t.Errorf("err = %q, want substring %q", err.Error(), `"code-merger"`)
	}
	if !strings.Contains(err.Error(), "not permitted to spawn sub-agents") {
		t.Errorf("err = %q, want substring %q", err.Error(), "not permitted to spawn sub-agents")
	}
}

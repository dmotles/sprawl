package agentops

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRetire_EmitsCheckpoints verifies that the retire path threads
// per-call observability checkpoints (QUM-494). The exact set of seams
// is bounded — at minimum a preflight checkpoint (before state mutation)
// and a worktree-removed checkpoint (after agent.RetireAgent succeeds)
// must be emitted on the happy path.
func TestRetire_EmitsCheckpoints(t *testing.T) {
	sprawlRoot := t.TempDir()

	// Set up a fake worktree directory for the agent.
	agentName := "finn"
	worktreePath := filepath.Join(sprawlRoot, ".sprawl", "worktrees", agentName)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	// Persist agent state (real state package, no mocking).
	agentState := &state.AgentState{
		Name:     agentName,
		Type:     "engineer",
		Family:   "engineering",
		Branch:   "dmotles/finn-feature",
		Worktree: worktreePath,
		Parent:   "weave",
		Status:   "active",
	}
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	var steps []string
	deps := &RetireDeps{
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_ROOT":
				return sprawlRoot
			case "SPRAWL_AGENT_IDENTITY":
				return "weave"
			}
			return ""
		},
		WorktreeRemove: func(repoRoot, worktreePath string, force bool) error {
			return os.RemoveAll(worktreePath)
		},
		GitStatus:           func(string) (string, error) { return "", nil },
		RemoveAll:           os.RemoveAll,
		GitBranchDelete:     func(string, string) error { return nil },
		GitBranchIsMerged:   func(string, string) (bool, error) { return false, nil },
		GitBranchSafeDelete: func(string, string) error { return nil },
		GitUnmergedCommits:  func(string, string) ([]string, error) { return nil, nil },
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		RunScript: func(script, workDir string, env map[string]string) ([]byte, error) {
			return nil, nil
		},
		Checkpoint: func(step string, _ ...any) {
			steps = append(steps, step)
		},
	}

	if _, err := Retire(context.Background(), deps, agentName, false, false, false, false, false); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	// Required checkpoints. The implementer chooses the exact seam names but
	// these prefixes guard against silently dropping observability.
	required := []string{
		"retire.preflight",
		"retire.checkpoint-saved",
		"retire.worktree-removed",
	}

	have := make(map[string]bool, len(steps))
	for _, s := range steps {
		have[s] = true
	}
	for _, want := range required {
		if !have[want] {
			t.Errorf("missing required checkpoint %q (got: %v)", want, steps)
		}
	}
}

// TestRetire_NilCheckpointSafe pins that nil Checkpoint is allowed.
func TestRetire_NilCheckpointSafe(t *testing.T) {
	sprawlRoot := t.TempDir()
	agentName := "ghost"
	worktreePath := filepath.Join(sprawlRoot, ".sprawl", "worktrees", agentName)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	agentState := &state.AgentState{
		Name: agentName, Type: "researcher", Family: "engineering",
		Branch: "dmotles/ghost", Worktree: worktreePath, Parent: "weave",
		Status: "active",
	}
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	deps := &RetireDeps{
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return sprawlRoot
			}
			return ""
		},
		WorktreeRemove:      func(_, p string, _ bool) error { return os.RemoveAll(p) },
		GitStatus:           func(string) (string, error) { return "", nil },
		RemoveAll:           os.RemoveAll,
		GitBranchDelete:     func(string, string) error { return nil },
		GitBranchIsMerged:   func(string, string) (bool, error) { return false, nil },
		GitBranchSafeDelete: func(string, string) error { return nil },
		GitUnmergedCommits:  func(string, string) ([]string, error) { return nil, nil },
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		RunScript:  func(string, string, map[string]string) ([]byte, error) { return nil, nil },
		Checkpoint: nil,
	}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil Checkpoint panicked: %v", r)
		}
	}()
	if _, err := Retire(context.Background(), deps, agentName, false, false, false, false, false); err != nil {
		t.Fatalf("Retire: %v", err)
	}
}

// TestRetire_Subagent_SkipsWorktreeAndBranchDelete pins QUM-709: retiring a
// sub-agent must NOT remove the shared worktree (already gated by
// agent.RetireAgent) and must NOT delete the shared branch via
// GitBranchDelete or GitBranchSafeDelete. The state file IS removed.
func TestRetire_Subagent_SkipsWorktreeAndBranchDelete(t *testing.T) {
	sprawlRoot := t.TempDir()
	agentName := "sub-x"

	// Shared worktree dir (lives outside .sprawl/worktrees to make it obvious
	// it isn't owned by the sub-agent).
	sharedWT := filepath.Join(sprawlRoot, "shared-wt")
	if err := os.MkdirAll(sharedWT, 0o755); err != nil {
		t.Fatalf("mkdir shared worktree: %v", err)
	}

	agentState := &state.AgentState{
		Name:     agentName,
		Type:     "engineer",
		Family:   "engineering",
		Parent:   "manager-x",
		Branch:   "shared-br",
		Worktree: sharedWT,
		Status:   "active",
		Subagent: true,
	}
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	var removedWorktree, deletedBranch bool
	deps := &RetireDeps{
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_ROOT":
				return sprawlRoot
			case "SPRAWL_AGENT_IDENTITY":
				return "manager-x"
			}
			return ""
		},
		WorktreeRemove: func(_, _ string, _ bool) error {
			removedWorktree = true
			return nil
		},
		GitStatus:       func(string) (string, error) { return "", nil },
		RemoveAll:       os.RemoveAll,
		GitBranchDelete: func(string, string) error { deletedBranch = true; return nil },
		GitBranchIsMerged: func(string, string) (bool, error) {
			return false, nil
		},
		GitBranchSafeDelete: func(string, string) error { deletedBranch = true; return nil },
		GitUnmergedCommits:  func(string, string) ([]string, error) { return nil, nil },
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		RunScript: func(string, string, map[string]string) ([]byte, error) { return nil, nil },
	}

	// abandon=true, yes=true: typical sub-agent retire path.
	if _, err := Retire(context.Background(), deps, agentName, false, false, true, false, true); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	if removedWorktree {
		t.Errorf("WorktreeRemove was called for sub-agent retire; must be skipped (shared worktree)")
	}
	if deletedBranch {
		t.Errorf("GitBranchDelete/GitBranchSafeDelete was called for sub-agent retire; must be skipped (shared branch)")
	}

	// Shared worktree dir must still exist.
	if _, err := os.Stat(sharedWT); err != nil {
		t.Errorf("shared worktree dir was removed: %v", err)
	}

	// State file must be gone.
	if _, err := state.LoadAgent(sprawlRoot, agentName); err == nil {
		t.Errorf("state file for retired sub-agent still loads; want not-found")
	}
}

// TestRetire_TerminalStatuses_Succeed pins QUM-739 Bug 1: retire (with or
// without --abandon) must succeed when the agent is in any terminal status
// and no live runtime is registered. The previous TerminalAgentError gate
// refused these cases, trapping zombie agents.
func TestRetire_TerminalStatuses_Succeed(t *testing.T) {
	terminalStatuses := []string{
		// QUM-1186: state.StatusStopped removed — LoadAgent now migrates the
		// legacy "stopped" sentinel to StatusSuspended, which is not a
		// resolved orphan, so it is no longer reachable as a terminal status
		// here. See the matching note in merge_test.go.
		state.StatusFaulted,
		state.StatusRetired,
		state.StatusKilled,
		state.StatusDied,
		state.StatusResumeFailed,
		// QUM-787: StatusComplete is a resolved-orphan resting state
		// and must be retire-able with or without --abandon.
		state.StatusComplete,
	}
	for _, status := range terminalStatuses {
		for _, abandon := range []bool{false, true} {
			name := status
			if abandon {
				name = status + "+abandon"
			}
			t.Run(name, func(t *testing.T) {
				sprawlRoot := t.TempDir()
				agentName := "zombie"
				worktreePath := filepath.Join(sprawlRoot, ".sprawl", "worktrees", agentName)
				if err := os.MkdirAll(worktreePath, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				agentState := &state.AgentState{
					Name:     agentName,
					Type:     "engineer",
					Family:   "engineering",
					Branch:   "dmotles/zombie",
					Worktree: worktreePath,
					Parent:   "weave",
					Status:   status,
				}
				if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
					t.Fatalf("SaveAgent: %v", err)
				}
				deps := terminalRetireDeps(sprawlRoot)
				_, err := Retire(context.Background(), deps, agentName,
					false /* cascade */, false /* force */, abandon,
					false /* mergeFirst */, true /* yes */)
				if err != nil {
					t.Fatalf("Retire(status=%q, abandon=%v): unexpected error: %v", status, abandon, err)
				}
				if _, err := state.LoadAgent(sprawlRoot, agentName); err == nil {
					t.Errorf("state file still loads after Retire; expected removal")
				}
			})
		}
	}
}

// TestRetire_TerminalChildren_DoNotRequireCascade pins QUM-739 Bug 2 on the
// retire path: a parent whose only children are in terminal status must be
// retire-able without --cascade.
func TestRetire_TerminalChildren_DoNotRequireCascade(t *testing.T) {
	sprawlRoot := t.TempDir()
	parentName := "parent"
	parentWT := filepath.Join(sprawlRoot, ".sprawl", "worktrees", parentName)
	if err := os.MkdirAll(parentWT, 0o755); err != nil {
		t.Fatalf("mkdir parent wt: %v", err)
	}
	parentState := &state.AgentState{
		Name: parentName, Type: "manager", Family: "engineering",
		Branch: "dmotles/parent", Worktree: parentWT, Parent: "weave",
		Status: state.StatusActive,
	}
	if err := state.SaveAgent(sprawlRoot, parentState); err != nil {
		t.Fatalf("SaveAgent parent: %v", err)
	}

	terminalStatuses := []string{
		// QUM-1186: state.StatusStopped removed — LoadAgent now migrates the
		// legacy "stopped" sentinel to StatusSuspended, which is not a
		// resolved orphan, so it is no longer reachable as a terminal status
		// here. See the matching note in merge_test.go.
		state.StatusFaulted,
		state.StatusRetired,
		state.StatusKilled,
		state.StatusDied,
		state.StatusResumeFailed,
		// QUM-787: complete children are resolved orphans and must not
		// block parent retire (no --cascade required).
		state.StatusComplete,
	}
	for i, s := range terminalStatuses {
		childName := "child" + s
		childWT := filepath.Join(sprawlRoot, ".sprawl", "worktrees", childName)
		if err := os.MkdirAll(childWT, 0o755); err != nil {
			t.Fatalf("mkdir child wt %d: %v", i, err)
		}
		child := &state.AgentState{
			Name: childName, Type: "engineer", Family: "engineering",
			Branch: "dmotles/" + childName, Worktree: childWT, Parent: parentName,
			Status: s,
		}
		if err := state.SaveAgent(sprawlRoot, child); err != nil {
			t.Fatalf("SaveAgent child: %v", err)
		}
	}

	deps := terminalRetireDeps(sprawlRoot)
	// cascade=false: must still succeed because all children are terminal.
	if _, err := Retire(context.Background(), deps, parentName,
		false /* cascade */, false /* force */, false, /* abandon */
		false /* mergeFirst */, false /* yes */); err != nil {
		t.Fatalf("Retire parent with terminal-only children: %v", err)
	}
}

// TestRetire_ActiveChildBlocks pins the negative case: a live child still
// blocks parent retire without --cascade. Guards against the filter being too
// permissive.
func TestRetire_ActiveChildBlocks(t *testing.T) {
	sprawlRoot := t.TempDir()
	parentName := "parent"
	parentWT := filepath.Join(sprawlRoot, ".sprawl", "worktrees", parentName)
	if err := os.MkdirAll(parentWT, 0o755); err != nil {
		t.Fatalf("mkdir parent wt: %v", err)
	}
	parentState := &state.AgentState{
		Name: parentName, Type: "manager", Family: "engineering",
		Branch: "dmotles/parent", Worktree: parentWT, Parent: "weave",
		Status: state.StatusActive,
	}
	if err := state.SaveAgent(sprawlRoot, parentState); err != nil {
		t.Fatalf("SaveAgent parent: %v", err)
	}
	childWT := filepath.Join(sprawlRoot, ".sprawl", "worktrees", "kid")
	if err := os.MkdirAll(childWT, 0o755); err != nil {
		t.Fatalf("mkdir child wt: %v", err)
	}
	child := &state.AgentState{
		Name: "kid", Type: "engineer", Family: "engineering",
		Branch: "dmotles/kid", Worktree: childWT, Parent: parentName,
		Status: state.StatusActive,
	}
	if err := state.SaveAgent(sprawlRoot, child); err != nil {
		t.Fatalf("SaveAgent child: %v", err)
	}

	deps := terminalRetireDeps(sprawlRoot)
	_, err := Retire(context.Background(), deps, parentName, false, false, false, false, false)
	if err == nil {
		t.Fatalf("Retire of parent with active child must fail without --cascade")
	}
}

// retireRecords captures the teardown side effects observed during a retire so
// cascade tests can assert that resolved-orphan descendants were actually torn
// down (QUM-852).
type retireRecords struct {
	worktreesRemoved []string
	branchesDeleted  []string
}

// recordingRetireDeps builds RetireDeps like terminalRetireDeps but records the
// worktree paths passed to WorktreeRemove and the branch names passed to
// GitBranchDelete / GitBranchSafeDelete, so a test can verify orphan children
// had their worktrees and branches cleaned up.
func recordingRetireDeps(sprawlRoot string) (*RetireDeps, *retireRecords) {
	rec := &retireRecords{}
	deps := terminalRetireDeps(sprawlRoot)
	deps.WorktreeRemove = func(_, p string, _ bool) error {
		rec.worktreesRemoved = append(rec.worktreesRemoved, p)
		return os.RemoveAll(p)
	}
	deps.GitBranchDelete = func(_, branch string) error {
		rec.branchesDeleted = append(rec.branchesDeleted, branch)
		return nil
	}
	deps.GitBranchSafeDelete = func(_, branch string) error {
		rec.branchesDeleted = append(rec.branchesDeleted, branch)
		return nil
	}
	return deps, rec
}

// saveTestAgent persists a minimal agent state with its own worktree dir and
// branch, returning the worktree path.
func saveTestAgent(t *testing.T, sprawlRoot, name, parent, status string) string {
	t.Helper()
	wt := filepath.Join(sprawlRoot, ".sprawl", "worktrees", name)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree %s: %v", name, err)
	}
	st := &state.AgentState{
		Name: name, Type: "engineer", Family: "engineering",
		Branch: "dmotles/" + name, Worktree: wt, Parent: parent, Status: status,
	}
	if err := state.SaveAgent(sprawlRoot, st); err != nil {
		t.Fatalf("SaveAgent %s: %v", name, err)
	}
	return wt
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestRetire_Cascade_TearsDownResolvedOrphanChildren is the primary QUM-852
// repro: cascade+abandon on a parent whose children are resolved orphans
// (complete/killed/faulted/died) must fully tear each child down — state file
// deleted, worktree removed, branch deleted — not silently skip them.
func TestRetire_Cascade_TearsDownResolvedOrphanChildren(t *testing.T) {
	for _, childStatus := range []string{
		state.StatusComplete, state.StatusKilled, state.StatusFaulted, state.StatusDied,
	} {
		t.Run(childStatus, func(t *testing.T) {
			sprawlRoot := t.TempDir()
			saveTestAgent(t, sprawlRoot, "parent", "weave", state.StatusActive)
			childWT := saveTestAgent(t, sprawlRoot, "kid", "parent", childStatus)

			deps, rec := recordingRetireDeps(sprawlRoot)
			retired, err := Retire(context.Background(), deps, "parent",
				true /* cascade */, false /* force */, true /* abandon */, false /* mergeFirst */, true /* yes */)
			if err != nil {
				t.Fatalf("Retire cascade: %v", err)
			}

			if _, err := state.LoadAgent(sprawlRoot, "kid"); err == nil {
				t.Errorf("child state file still loads after cascade; want deleted")
			}
			if _, err := os.Stat(childWT); err == nil {
				t.Errorf("child worktree %s still exists after cascade; want removed", childWT)
			}
			if !contains(rec.branchesDeleted, "dmotles/kid") {
				t.Errorf("child branch not deleted; GitBranchDelete calls = %v", rec.branchesDeleted)
			}
			if !contains(retired, "kid") || !contains(retired, "parent") {
				t.Errorf("returned retired set = %v, want to include both kid and parent", retired)
			}
		})
	}
}

// TestRetire_Cascade_ReturnsRetiredSet_BottomUp pins the ordering contract:
// descendants precede ancestors, target last.
func TestRetire_Cascade_ReturnsRetiredSet_BottomUp(t *testing.T) {
	sprawlRoot := t.TempDir()
	saveTestAgent(t, sprawlRoot, "parent", "weave", state.StatusActive)
	saveTestAgent(t, sprawlRoot, "child", "parent", state.StatusActive)
	saveTestAgent(t, sprawlRoot, "grand", "child", state.StatusComplete)

	deps, _ := recordingRetireDeps(sprawlRoot)
	retired, err := Retire(context.Background(), deps, "parent", true, false, true, false, true)
	if err != nil {
		t.Fatalf("Retire cascade: %v", err)
	}
	want := []string{"grand", "child", "parent"}
	if len(retired) != len(want) {
		t.Fatalf("retired = %v, want %v", retired, want)
	}
	for i, name := range want {
		if retired[i] != name {
			t.Errorf("retired[%d] = %q, want %q (retired=%v)", i, retired[i], name, retired)
		}
	}
}

// TestRetire_Cascade_Recursive_ResolvedOrphanGrandchild proves recursion reaches
// an orphan nested under a live child, not just direct orphan children.
func TestRetire_Cascade_Recursive_ResolvedOrphanGrandchild(t *testing.T) {
	sprawlRoot := t.TempDir()
	saveTestAgent(t, sprawlRoot, "parent", "weave", state.StatusActive)
	saveTestAgent(t, sprawlRoot, "child", "parent", state.StatusActive)
	grandWT := saveTestAgent(t, sprawlRoot, "grand", "child", state.StatusFaulted)

	deps, _ := recordingRetireDeps(sprawlRoot)
	if _, err := Retire(context.Background(), deps, "parent", true, false, true, false, true); err != nil {
		t.Fatalf("Retire cascade: %v", err)
	}
	if _, err := state.LoadAgent(sprawlRoot, "grand"); err == nil {
		t.Errorf("faulted grandchild state file still loads; want deleted")
	}
	if _, err := os.Stat(grandWT); err == nil {
		t.Errorf("faulted grandchild worktree %s still exists; want removed", grandWT)
	}
}

// TestRetire_Cascade_Recurses_ThroughOrphanIntermediate pins the AC phrasing
// "deep tree with a resolved-orphan intermediate": cascade must descend INTO a
// resolved-orphan (complete) child's own subtree and tear down its children,
// not stop at the orphan.
func TestRetire_Cascade_Recurses_ThroughOrphanIntermediate(t *testing.T) {
	sprawlRoot := t.TempDir()
	saveTestAgent(t, sprawlRoot, "parent", "weave", state.StatusActive)
	saveTestAgent(t, sprawlRoot, "mid", "parent", state.StatusComplete)
	leafWT := saveTestAgent(t, sprawlRoot, "leaf", "mid", state.StatusComplete)

	deps, _ := recordingRetireDeps(sprawlRoot)
	retired, err := Retire(context.Background(), deps, "parent", true, false, true, false, true)
	if err != nil {
		t.Fatalf("Retire cascade: %v", err)
	}
	for _, name := range []string{"leaf", "mid"} {
		if _, err := state.LoadAgent(sprawlRoot, name); err == nil {
			t.Errorf("%s state file still loads; want deleted (cascade must recurse through orphan intermediate)", name)
		}
	}
	if _, err := os.Stat(leafWT); err == nil {
		t.Errorf("leaf worktree %s still exists; want removed", leafWT)
	}
	if !contains(retired, "leaf") || !contains(retired, "mid") {
		t.Errorf("retired set = %v, want to include leaf and mid", retired)
	}
}

// TestRetire_Cascade_ExcludesRetiredRetiringChildren pins the filter as
// "exclude only {retired, retiring}" — those children have nothing left to
// clean and must not be re-torn-down.
func TestRetire_Cascade_ExcludesRetiredRetiringChildren(t *testing.T) {
	sprawlRoot := t.TempDir()
	saveTestAgent(t, sprawlRoot, "parent", "weave", state.StatusActive)
	saveTestAgent(t, sprawlRoot, "gone", "parent", state.StatusRetired)
	saveTestAgent(t, sprawlRoot, "leaving", "parent", state.StatusRetiring)

	deps, rec := recordingRetireDeps(sprawlRoot)
	retired, err := Retire(context.Background(), deps, "parent", true, false, true, false, true)
	if err != nil {
		t.Fatalf("Retire cascade: %v", err)
	}
	if contains(retired, "gone") || contains(retired, "leaving") {
		t.Errorf("retired set %v must not include retired/retiring children", retired)
	}
	if contains(rec.branchesDeleted, "dmotles/gone") || contains(rec.branchesDeleted, "dmotles/leaving") {
		t.Errorf("retired/retiring children must not be re-torn-down; branch deletes = %v", rec.branchesDeleted)
	}
}

// TestRetire_ReturnsOwnName_NonCascade pins that a leaf retire returns exactly
// its own name.
func TestRetire_ReturnsOwnName_NonCascade(t *testing.T) {
	sprawlRoot := t.TempDir()
	saveTestAgent(t, sprawlRoot, "solo", "weave", state.StatusComplete)

	deps, _ := recordingRetireDeps(sprawlRoot)
	retired, err := Retire(context.Background(), deps, "solo", false, false, true, false, true)
	if err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if len(retired) != 1 || retired[0] != "solo" {
		t.Errorf("retired = %v, want [solo]", retired)
	}
}

// TestRetire_Cascade_Abandon_DeletesOrphanChildBranches isolates the branch-leak
// symptom and its negative: with abandon the orphan child's branch is deleted;
// without abandon it is not force-deleted.
func TestRetire_Cascade_Abandon_DeletesOrphanChildBranches(t *testing.T) {
	t.Run("abandon deletes child branch", func(t *testing.T) {
		sprawlRoot := t.TempDir()
		saveTestAgent(t, sprawlRoot, "parent", "weave", state.StatusActive)
		saveTestAgent(t, sprawlRoot, "kid", "parent", state.StatusComplete)

		deps, rec := recordingRetireDeps(sprawlRoot)
		if _, err := Retire(context.Background(), deps, "parent", true, false, true /* abandon */, false, true); err != nil {
			t.Fatalf("Retire cascade abandon: %v", err)
		}
		if !contains(rec.branchesDeleted, "dmotles/kid") {
			t.Errorf("child branch not abandoned; GitBranchDelete calls = %v", rec.branchesDeleted)
		}
	})

	t.Run("no abandon does not force-delete child branch", func(t *testing.T) {
		sprawlRoot := t.TempDir()
		saveTestAgent(t, sprawlRoot, "parent", "weave", state.StatusActive)
		saveTestAgent(t, sprawlRoot, "kid", "parent", state.StatusComplete)

		deps, rec := recordingRetireDeps(sprawlRoot)
		// GitBranchIsMerged returns false in terminalRetireDeps, so an
		// unmerged branch is preserved (not force-deleted) without abandon.
		if _, err := Retire(context.Background(), deps, "parent", true, false, false /* abandon */, false, true); err != nil {
			t.Fatalf("Retire cascade: %v", err)
		}
		// The child must still be fully torn down (teardown-minus-branch),
		// not skipped — only the branch is preserved without abandon.
		if _, err := state.LoadAgent(sprawlRoot, "kid"); err == nil {
			t.Errorf("child state file still loads without abandon; want deleted")
		}
		if contains(rec.branchesDeleted, "dmotles/kid") {
			t.Errorf("child branch force-deleted without abandon; GitBranchDelete calls = %v", rec.branchesDeleted)
		}
	})
}

// TestRetireDeps_HasNoMergeSeams stops the second merge call site coming back.
//
// QUM-1088 was caused by this layer having its own merge: it built a
// merge.Config from the STALE spawn-time agentState.Branch while
// agentops.Merge had resolved the worktree's real HEAD since QUM-511. The fix
// was deletion, and a deletion is only durable if reintroducing it fails.
// Structural, not a comment: no DoMerge seam, no merge to do.
func TestRetireDeps_HasNoMergeSeams(t *testing.T) {
	tp := reflect.TypeOf(RetireDeps{})
	for _, name := range []string{"DoMerge", "NewMergeDeps"} {
		if _, ok := tp.FieldByName(name); ok {
			t.Errorf("RetireDeps carries a %q seam again.\n"+
				"The merge belongs to the supervisor (Real.Retire -> Real.Merge -> agentops.Merge), which is\n"+
				"where worktree-HEAD branch resolution, the detached-HEAD refusal, preconditions 7/8 and\n"+
				"mergeSem all live. A second call site here is how QUM-1088 happened.", name)
		}
	}
}

// TestRetire_MergeFirst_DoesNotMerge pins what mergeFirst still MEANS here.
//
// It no longer performs a merge — the supervisor does that before calling this
// function. What it still does is select the branch-delete behaviour:
// GitBranchSafeDelete ("we merged it; delete it if git agrees it is merged")
// instead of the preserve-and-warn default. That is easy to lose, because a
// reader who greps for uses of mergeFirst finds only a switch arm and may
// conclude the parameter is dead.
//
// (The red demonstration of the QUM-1088 loss this replaced lived here, driving
// real git through the inline merge block. With the block deleted there is
// nothing at this layer to demonstrate it against; the live version is
// TestRetire_MergeFirst_MergesWorktreeHeadBranch_NotStateBranch in
// internal/supervisor, which drives the real engine through the real routing.)
func TestRetire_MergeFirst_DoesNotMerge(t *testing.T) {
	sprawlRoot := t.TempDir()
	wt := filepath.Join(sprawlRoot, ".sprawl", "worktrees", "m")
	if err := os.MkdirAll(wt, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := state.SaveAgent(sprawlRoot, &state.AgentState{
		Name: "m", Type: "engineer", Family: "engineering",
		Branch: "agent/m", Worktree: wt, Parent: "weave", Status: "active",
	}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	deps := terminalRetireDeps(sprawlRoot)
	deps.Getenv = func(key string) string {
		switch key {
		case "SPRAWL_ROOT":
			return sprawlRoot
		case "SPRAWL_AGENT_IDENTITY":
			return "weave"
		}
		return ""
	}
	var safeDeleted, forceDeleted bool
	deps.GitBranchSafeDelete = func(string, string) error { safeDeleted = true; return nil }
	deps.GitBranchDelete = func(string, string) error { forceDeleted = true; return nil }

	if _, err := Retire(context.Background(), deps, "m", false, false, false, true /* mergeFirst */, true); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	if !safeDeleted {
		t.Error("mergeFirst must still select GitBranchSafeDelete; if this is failing, the parameter has been treated as dead and branch-deletion behaviour changed silently")
	}
	if forceDeleted {
		t.Error("mergeFirst must never force-delete the branch")
	}
}

// terminalRetireDeps builds RetireDeps suitable for the QUM-739 tests above.
// No live runtime is wired in (the agentops.Retire path treats absence of a
// runtime as offline cleanup); all git operations are stubbed.
func terminalRetireDeps(sprawlRoot string) *RetireDeps {
	return &RetireDeps{
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return sprawlRoot
			}
			return ""
		},
		WorktreeRemove:      func(_, p string, _ bool) error { return os.RemoveAll(p) },
		GitStatus:           func(string) (string, error) { return "", nil },
		RemoveAll:           os.RemoveAll,
		GitBranchDelete:     func(string, string) error { return nil },
		GitBranchIsMerged:   func(string, string) (bool, error) { return false, nil },
		GitBranchSafeDelete: func(string, string) error { return nil },
		GitUnmergedCommits:  func(string, string) ([]string, error) { return nil, nil },
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		RunScript: func(string, string, map[string]string) ([]byte, error) { return nil, nil },
	}
}

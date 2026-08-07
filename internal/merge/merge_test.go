package merge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/state"
)

// testNow is the fixed clock used by newTestDeps for premerge ref names, so
// the ref path is fully deterministic in assertions (QUM-1090).
var testNow = time.Date(2026, 5, 18, 12, 0, 0, 123000000, time.UTC)

// testPremergeBase is the ref prefix newTestDeps' clock + newTestConfig's
// agent name produce.
const testPremergeBase = "refs/sprawl/premerge/test-agent/20260518T120000.123Z"

// Fixture tip sentinels. Distinct per role, which is what lets an assertion
// about the PARENT tip fail when handed the branch tip and vice versa.
const (
	testMergeBase = "aaa111"
	testAgentTip  = "bbb222"
	testParentTip = "ppp444"
)

// newTestDeps builds a Deps whose seams MODEL the repository, rather than
// returning a constant per seam.
//
// The ff-merge in particular has to move the parent (QUM-1087): the engine
// proves step 3 was a pure ref move by reading the parent tip back and
// requiring it to equal the rebased branch tip. A fixture whose GitFFMerge is
// an inert no-op makes that predicate UNSATISFIABLE — every test using the
// fixture would fail on the correct implementation, and the pressure would be
// to weaken the predicate rather than fix the fixture. So the mutable
// parentTip below is not incidental convenience; it is what keeps the
// invariant assertable.
func newTestDeps() *Deps {
	parentTip := testParentTip
	return &Deps{
		LockAcquire:  func(lockPath string) (func(), error) { return func() {}, nil },
		GitMergeBase: func(repoRoot, a, b string) (string, error) { return testMergeBase, nil },
		// QUM-1090: worktree-aware so a test asserting "the parent ref got
		// the PARENT tip" cannot pass vacuously by both tips being equal.
		GitRevParseHead: func(worktree string) (string, error) {
			if worktree == "/worktree/parent" {
				return parentTip, nil
			}
			return testAgentTip, nil
		},
		GitRevParseRef: func(worktree, rev string) (string, error) {
			if rev == "sprawl/test-agent" {
				return testAgentTip, nil
			}
			return parentTip, nil
		},
		GitIsAncestor:  func(worktree, ancestor, descendant string) (bool, error) { return true, nil },
		GitRebase:      func(worktree, onto string) error { return nil },
		GitRebaseAbort: func(worktree string) error { return nil },
		// Models the ref move: the parent ends up at the branch tip. See the
		// doc comment above — an inert no-op here would make the engine's
		// post-ff SHA-equality check impossible to satisfy.
		GitFFMerge: func(worktree, branch string) error {
			parentTip = testAgentTip
			return nil
		},
		RunTestsStreaming: func(ctx context.Context, dir, command string, sink func(string)) (string, error) {
			return "ok", nil
		},
		WritePoke:       func(sprawlRoot, agentName, content string) error { return nil },
		GitUpdateRef:    func(worktree, ref, newSHA string) error { return nil },
		GitUpdateRefCAS: func(worktree, ref, newSHA, oldSHA string) error { return nil },
		GitSymbolicRefHead: func(worktree string) (string, error) {
			return "refs/heads/sprawl/test-agent", nil
		},
		Now:    func() time.Time { return testNow },
		Stderr: io.Discard,
	}
}

func newTestConfig() *Config {
	return &Config{
		SprawlRoot:     "/tmp/sprawl-test",
		AgentName:      "test-agent",
		AgentBranch:    "sprawl/test-agent",
		AgentWorktree:  "/worktree/agent",
		ParentBranch:   "main",
		ParentWorktree: "/worktree/parent",
		ValidateCmd:    "make validate",
		AgentState: &state.AgentState{
			Name:   "test-agent",
			Type:   "engineer",
			Family: "engineering",
			Branch: "sprawl/test-agent",
		},
	}
}

func TestMerge_HappyPath(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	var lockAcquired bool
	var unlockCalled bool
	deps.LockAcquire = func(lockPath string) (func(), error) {
		lockAcquired = true
		if !strings.Contains(lockPath, "test-agent.lock") {
			t.Errorf("lock path should contain agent name, got %q", lockPath)
		}
		return func() { unlockCalled = true }, nil
	}

	var rebaseCalled bool
	deps.GitRebase = func(worktree, onto string) error {
		rebaseCalled = true
		if worktree != "/worktree/agent" {
			t.Errorf("rebase worktree = %q, want /worktree/agent", worktree)
		}
		if onto != "main" {
			t.Errorf("rebase onto = %q, want main", onto)
		}
		return nil
	}

	var ffMergeCalled bool
	innerFF := deps.GitFFMerge
	deps.GitFFMerge = func(worktree, branch string) error {
		ffMergeCalled = true
		if worktree != "/worktree/parent" {
			t.Errorf("ff-merge worktree = %q, want /worktree/parent", worktree)
		}
		// The VALIDATED SHA, not the branch name. A name is resolved by git at
		// ff time and can point somewhere newer than what was validated
		// (QUM-1087 B1), so the engine passes the tip it verified.
		if branch != testAgentTip {
			t.Errorf("ff-merge rev = %q, want the validated tip %q (not a branch name — it would re-resolve)", branch, testAgentTip)
		}
		if branch == cfg.AgentBranch {
			t.Errorf("ff-merge was given the branch NAME %q; it must be given the validated SHA", branch)
		}
		return innerFF(worktree, branch)
	}

	var pokeCalled bool
	deps.WritePoke = func(sprawlRoot, agentName, content string) error {
		pokeCalled = true
		if agentName != "test-agent" {
			t.Errorf("poke agent = %q, want test-agent", agentName)
		}
		return nil
	}

	result, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.WasNoOp {
		t.Error("result should not be a no-op")
	}
	// The ff'd parent tip, read back from the ref — never a commit the engine
	// invented, and never one that exists on no ref (QUM-1087).
	if result.MergedTip != testAgentTip {
		t.Errorf("MergedTip = %q, want the post-ff parent tip %q", result.MergedTip, testAgentTip)
	}
	if !lockAcquired {
		t.Error("lock should be acquired")
	}
	if !unlockCalled {
		t.Error("unlock should be called")
	}
	if !rebaseCalled {
		t.Error("git rebase should be called")
	}
	if !ffMergeCalled {
		t.Error("git merge --ff-only should be called")
	}
	if !pokeCalled {
		t.Error("poke should be written")
	}
}

func TestMerge_ZeroCommit_NoOp(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	// merge-base and agent HEAD are the same -> no-op
	deps.GitMergeBase = func(repoRoot, a, b string) (string, error) { return "same-sha", nil }
	deps.GitRevParseHead = func(worktree string) (string, error) { return "same-sha", nil }

	var rebaseCalled, ffMergeCalled, pokeCalled bool
	deps.GitRebase = func(worktree, onto string) error { rebaseCalled = true; return nil }
	deps.GitFFMerge = func(worktree, branch string) error { ffMergeCalled = true; return nil }
	deps.WritePoke = func(sprawlRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("result should not be nil")
	}
	if !result.WasNoOp {
		t.Error("result should be a no-op when merge-base == agent HEAD")
	}
	if rebaseCalled {
		t.Error("rebase should NOT be called for no-op")
	}
	if ffMergeCalled {
		t.Error("ff-merge should NOT be called for no-op")
	}
	if pokeCalled {
		t.Error("poke should NOT be written for no-op")
	}
}

func TestMerge_DryRun(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	cfg.DryRun = true

	var stderr bytes.Buffer
	deps.Stderr = &stderr

	var lockAcquired, rebaseCalled, ffMergeCalled, pokeCalled bool
	deps.LockAcquire = func(lockPath string) (func(), error) { lockAcquired = true; return func() {}, nil }
	deps.GitRebase = func(worktree, onto string) error { rebaseCalled = true; return nil }
	deps.GitFFMerge = func(worktree, branch string) error { ffMergeCalled = true; return nil }
	deps.WritePoke = func(sprawlRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("result should not be nil")
	}
	if lockAcquired {
		t.Error("lock should NOT be acquired during dry-run")
	}
	if rebaseCalled {
		t.Error("rebase should NOT be called during dry-run")
	}
	if ffMergeCalled {
		t.Error("ff-merge should NOT be called during dry-run")
	}
	if pokeCalled {
		t.Error("poke should NOT be written during dry-run")
	}

	output := stderr.String()
	if !strings.Contains(output, "dry-run") {
		t.Errorf("dry-run output should contain 'dry-run', got: %q", output)
	}
	if !strings.Contains(output, "test-agent") {
		t.Errorf("dry-run output should mention agent name, got: %q", output)
	}
}

func TestMerge_LockAcquireFailure(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	deps.LockAcquire = func(lockPath string) (func(), error) {
		return nil, fmt.Errorf("lock contention timeout")
	}

	var rebaseCalled bool
	deps.GitRebase = func(worktree, onto string) error { rebaseCalled = true; return nil }

	result, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected error from lock acquire failure")
	}
	if !strings.Contains(err.Error(), "lock") {
		t.Errorf("error should mention lock, got: %v", err)
	}
	if result != nil {
		t.Error("result should be nil on error")
	}
	if rebaseCalled {
		t.Error("git operations should NOT proceed when lock fails")
	}
}

func TestMerge_RebaseConflict_AbortsAndErrors(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	deps.GitRebase = func(worktree, onto string) error {
		return fmt.Errorf("CONFLICT (content): merge conflict in main.go")
	}

	var rebaseAbortCalled bool
	deps.GitRebaseAbort = func(worktree string) error {
		rebaseAbortCalled = true
		return nil
	}

	var ffMergeCalled, pokeCalled, unlockCalled bool
	deps.LockAcquire = func(lockPath string) (func(), error) {
		return func() { unlockCalled = true }, nil
	}
	deps.GitFFMerge = func(worktree, branch string) error { ffMergeCalled = true; return nil }
	deps.WritePoke = func(sprawlRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected error from rebase conflict")
	}
	if !rebaseAbortCalled {
		t.Error("rebase --abort should be called on conflict")
	}
	// Error should include the pre-REBASE SHA for recovery
	if !strings.Contains(err.Error(), "bbb222") {
		t.Errorf("error should include the pre-rebase SHA for recovery, got: %v", err)
	}
	if ffMergeCalled {
		t.Error("ff-merge should NOT be called after rebase conflict")
	}
	if pokeCalled {
		t.Error("poke should NOT be written after rebase conflict")
	}
	if !unlockCalled {
		t.Error("lock should still be released on rebase conflict")
	}
	if result != nil {
		t.Error("result should be nil on error")
	}
}

func TestMerge_FFMergeFailure(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	deps.GitFFMerge = func(worktree, branch string) error {
		return fmt.Errorf("not a fast-forward")
	}

	var pokeCalled bool
	deps.WritePoke = func(sprawlRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected error from ff-merge failure")
	}
	if pokeCalled {
		t.Error("poke should NOT be written after ff-merge failure")
	}
	if result != nil {
		t.Error("result should be nil on error")
	}
}

// TestMerge_ValidationFail_NoRollbackBecauseNothingWasMerged replaces the old
// TestMerge_PostMergeValidation_Fail_RollsBack.
//
// The rename is the substance, not cosmetics. The old test asserted that a
// validate failure called `git reset --hard` on the PARENT worktree — i.e. it
// pinned the rollback as required behaviour, and it was green throughout the
// period when that rollback was destroying data (S5b: it rewound a
// pre-existing parent commit whenever the agent's content was already
// upstream). The rollback is not fixed here; it does not exist, and neither
// does the primitive that performed it.
func TestMerge_ValidationFail_NoRollbackBecauseNothingWasMerged(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	deps.RunTestsStreaming = func(ctx context.Context, dir, command string, sink func(string)) (string, error) {
		return "FAIL: TestSomething\nexit status 1", fmt.Errorf("tests failed")
	}

	var ffCalled bool
	deps.GitFFMerge = func(worktree, branch string) error { ffCalled = true; return nil }

	var pokeCalled, unlockCalled bool
	deps.LockAcquire = func(lockPath string) (func(), error) {
		return func() { unlockCalled = true }, nil
	}
	deps.WritePoke = func(sprawlRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected error from validation failure")
	}
	if ffCalled {
		t.Error("the ff-merge must NOT run when validation failed: the parent is mutated only after the tree is known good")
	}
	if pokeCalled {
		t.Error("poke should NOT be written after validation failure")
	}
	if !unlockCalled {
		t.Error("lock should still be released on validation failure")
	}
	if !strings.Contains(err.Error(), "--no-validate") {
		t.Errorf("error should suggest --no-validate, got: %v", err)
	}
	if !strings.Contains(err.Error(), "FAIL: TestSomething") {
		t.Errorf("error should include test output, got: %v", err)
	}
	// The old message said "Merge rolled back. Your branch is back to its
	// pre-merge state." Both halves were false in the S5b case and the first
	// is now false in every case. A message claiming a rollback is worse than
	// no message: it tells the reader the parent was touched and restored.
	for _, lie := range []string{"rolled back", "roll back", "reset --hard"} {
		if strings.Contains(strings.ToLower(err.Error()), lie) {
			t.Errorf("error claims a rollback that never happens (%q): %v", lie, err)
		}
	}
	if !strings.Contains(err.Error(), cfg.ParentBranch) || !strings.Contains(err.Error(), "NOT modified") {
		t.Errorf("error should state plainly that the parent was not modified, got: %v", err)
	}
	if result != nil {
		t.Error("result should be nil on error")
	}
}

func TestMerge_NoValidate_SkipsTests(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	cfg.NoValidate = true

	var testsCalled bool
	deps.RunTestsStreaming = func(ctx context.Context, dir, command string, sink func(string)) (string, error) {
		testsCalled = true
		return "ok", nil
	}

	var pokeCalled bool
	deps.WritePoke = func(sprawlRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if testsCalled {
		t.Error("RunTests should NOT be called when NoValidate is true")
	}
	if !pokeCalled {
		t.Error("poke should still be written when NoValidate is true")
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
}

func TestMerge_PokeWrittenBeforeLockRelease(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	var order []string
	deps.WritePoke = func(sprawlRoot, agentName, content string) error {
		order = append(order, "poke")
		return nil
	}
	deps.LockAcquire = func(lockPath string) (func(), error) {
		return func() { order = append(order, "unlock") }, nil
	}

	_, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pokeIdx := -1
	unlockIdx := -1
	for i, op := range order {
		if op == "poke" && pokeIdx == -1 {
			pokeIdx = i
		}
		if op == "unlock" && unlockIdx == -1 {
			unlockIdx = i
		}
	}

	if pokeIdx == -1 {
		t.Fatal("poke was not called")
	}
	if unlockIdx == -1 {
		t.Fatal("unlock was not called")
	}
	if pokeIdx >= unlockIdx {
		t.Errorf("poke (index %d) must happen before unlock (index %d)", pokeIdx, unlockIdx)
	}
}

// TestMerge_StepOrdering pins the whole sequence as an ordered slice.
//
// This is ONE of three layers and is not sufficient alone: an ordered-slice
// assertion is satisfied by editing the expected slice, so a reordering
// regression can be "fixed" by moving a string. The layer that cannot be
// defeated that way is TestMerge_ValidateStrictlyPrecedesFFMerge's in-seam
// guards; the third is the checkpoint sequence. Keep all three.
func TestMerge_StepOrdering(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	parentTip := testParentTip
	var order []string
	deps.LockAcquire = func(lockPath string) (func(), error) {
		order = append(order, "lock")
		return func() { order = append(order, "unlock") }, nil
	}
	deps.GitMergeBase = func(repoRoot, a, b string) (string, error) {
		order = append(order, "merge-base")
		return testMergeBase, nil
	}
	deps.GitRevParseHead = func(worktree string) (string, error) {
		order = append(order, "rev-parse-head")
		if worktree == cfg.ParentWorktree {
			return parentTip, nil
		}
		return testAgentTip, nil
	}
	deps.GitRevParseRef = func(worktree, rev string) (string, error) {
		order = append(order, "rev-parse-ref")
		if rev == cfg.AgentBranch {
			return testAgentTip, nil
		}
		return parentTip, nil
	}
	deps.GitIsAncestor = func(worktree, ancestor, descendant string) (bool, error) {
		order = append(order, "is-ancestor")
		return true, nil
	}
	deps.GitUpdateRef = func(worktree, ref, newSHA string) error {
		order = append(order, "update-ref")
		return nil
	}
	deps.GitRebase = func(worktree, onto string) error {
		order = append(order, "rebase")
		return nil
	}
	deps.GitFFMerge = func(worktree, branch string) error {
		order = append(order, "ff-merge")
		parentTip = testAgentTip
		return nil
	}
	deps.RunTestsStreaming = func(ctx context.Context, dir, command string, sink func(string)) (string, error) {
		order = append(order, "validate")
		return "ok", nil
	}
	deps.WritePoke = func(sprawlRoot, agentName, content string) error {
		order = append(order, "poke")
		return nil
	}

	_, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read this slice as the QUM-1087 contract:
	//   - both update-ref writes precede REBASE, which is now the first mutation
	//   - the ff precondition (rev-parse-ref + is-ancestor) is checked right
	//     after the rebase, so a bad rebase is diagnosed before anything
	//     expensive runs
	//   - VALIDATE precedes FF-MERGE — the inversion this issue exists for
	//   - a final rev-parse-head reads the parent back to prove the ref moved
	expected := []string{
		"lock", "merge-base", "rev-parse-head", "rev-parse-head",
		"update-ref", "update-ref",
		"rebase",
		"rev-parse-ref", "rev-parse-head", "is-ancestor",
		"validate",
		"ff-merge", "rev-parse-head",
		"poke", "unlock",
	}
	if len(order) != len(expected) {
		t.Fatalf("expected %d operations, got %d: %v", len(expected), len(order), order)
	}
	for i, op := range expected {
		if order[i] != op {
			t.Errorf("step %d: got %q, want %q (full order: %v)", i, order[i], op, order)
		}
	}
}

package merge

import (
	"bytes"
	"context"
	"errors"
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

func newTestDeps() *Deps {
	return &Deps{
		LockAcquire:  func(lockPath string) (func(), error) { return func() {}, nil },
		GitMergeBase: func(repoRoot, a, b string) (string, error) { return "aaa111", nil },
		// QUM-1090: worktree-aware so a test asserting "the parent ref got
		// the PARENT tip" cannot pass vacuously by both tips being equal.
		GitRevParseHead: func(worktree string) (string, error) {
			if worktree == "/worktree/parent" {
				return "ppp444", nil
			}
			return "bbb222", nil
		},
		GitLogRange: func(worktree, base, head string) ([]CommitRecord, error) {
			return []CommitRecord{{SHA: "d00dfeed" + strings.Repeat("0", 32), Message: "COMMIT-SUBJ-SENTINEL\n\nCOMMIT-BODY-SENTINEL"}}, nil
		},
		GitResetSoft:   func(worktree, ref string) error { return nil },
		GitCommit:      func(worktree, message string) (string, error) { return "ccc333", nil },
		GitRebase:      func(worktree, onto string) error { return nil },
		GitRebaseAbort: func(worktree string) error { return nil },
		GitFFMerge:     func(worktree, branch string) error { return nil },
		GitResetHard:   func(worktree string) error { return nil },
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
			// A sentinel, not prose: any test that lets the status blurb reach
			// a commit message fails by name rather than by looking plausible.
			LastReportMessage: blurbSentinel,
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

	var resetSoftCalled bool
	deps.GitResetSoft = func(worktree, ref string) error {
		resetSoftCalled = true
		if worktree != "/worktree/agent" {
			t.Errorf("reset-soft worktree = %q, want /worktree/agent", worktree)
		}
		if ref != "aaa111" {
			t.Errorf("reset-soft ref = %q, want merge-base aaa111", ref)
		}
		return nil
	}

	var commitWorktree string
	deps.GitCommit = func(worktree, message string) (string, error) {
		commitWorktree = worktree
		return "abc1234", nil
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
	deps.GitFFMerge = func(worktree, branch string) error {
		ffMergeCalled = true
		if worktree != "/worktree/parent" {
			t.Errorf("ff-merge worktree = %q, want /worktree/parent", worktree)
		}
		if branch != "sprawl/test-agent" {
			t.Errorf("ff-merge branch = %q, want sprawl/test-agent", branch)
		}
		return nil
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
	if result.CommitHash != "abc1234" {
		t.Errorf("commit hash = %q, want abc1234", result.CommitHash)
	}
	if !lockAcquired {
		t.Error("lock should be acquired")
	}
	if !unlockCalled {
		t.Error("unlock should be called")
	}
	if !resetSoftCalled {
		t.Error("git reset --soft should be called")
	}
	if commitWorktree != "/worktree/agent" {
		t.Errorf("commit worktree = %q, want /worktree/agent", commitWorktree)
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

	var resetSoftCalled, rebaseCalled, ffMergeCalled, pokeCalled bool
	deps.GitResetSoft = func(worktree, ref string) error { resetSoftCalled = true; return nil }
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
	if resetSoftCalled {
		t.Error("reset-soft should NOT be called for no-op")
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

	var lockAcquired, resetSoftCalled, rebaseCalled, ffMergeCalled, pokeCalled bool
	deps.LockAcquire = func(lockPath string) (func(), error) { lockAcquired = true; return func() {}, nil }
	deps.GitResetSoft = func(worktree, ref string) error { resetSoftCalled = true; return nil }
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
	if resetSoftCalled {
		t.Error("reset-soft should NOT be called during dry-run")
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

	var resetSoftCalled bool
	deps.GitResetSoft = func(worktree, ref string) error { resetSoftCalled = true; return nil }

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
	if resetSoftCalled {
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
	// Error should include the pre-squash SHA for recovery
	if !strings.Contains(err.Error(), "bbb222") {
		t.Errorf("error should include pre-squash SHA for recovery, got: %v", err)
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

func TestMerge_PostMergeValidation_Fail_RollsBack(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	deps.RunTestsStreaming = func(ctx context.Context, dir, command string, sink func(string)) (string, error) {
		return "FAIL: TestSomething\nexit status 1", fmt.Errorf("tests failed")
	}

	var resetHardWorktree string
	deps.GitResetHard = func(worktree string) error {
		resetHardWorktree = worktree
		return nil
	}

	var pokeCalled, unlockCalled bool
	deps.LockAcquire = func(lockPath string) (func(), error) {
		return func() { unlockCalled = true }, nil
	}
	deps.WritePoke = func(sprawlRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected error from post-merge validation failure")
	}
	if resetHardWorktree != "/worktree/parent" {
		t.Errorf("reset-hard worktree = %q, want /worktree/parent", resetHardWorktree)
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

func TestMerge_StepOrdering(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	var order []string
	deps.LockAcquire = func(lockPath string) (func(), error) {
		order = append(order, "lock")
		return func() { order = append(order, "unlock") }, nil
	}
	deps.GitMergeBase = func(repoRoot, a, b string) (string, error) {
		order = append(order, "merge-base")
		return "aaa111", nil
	}
	deps.GitRevParseHead = func(worktree string) (string, error) {
		order = append(order, "rev-parse")
		if worktree == "/worktree/parent" {
			return "ppp444", nil
		}
		return "bbb222", nil
	}
	deps.GitUpdateRef = func(worktree, ref, newSHA string) error {
		order = append(order, "update-ref")
		return nil
	}
	deps.GitResetSoft = func(worktree, ref string) error {
		order = append(order, "reset-soft")
		return nil
	}
	deps.GitCommit = func(worktree, message string) (string, error) {
		order = append(order, "commit")
		return "ccc333", nil
	}
	deps.GitRebase = func(worktree, onto string) error {
		order = append(order, "rebase")
		return nil
	}
	deps.GitFFMerge = func(worktree, branch string) error {
		order = append(order, "ff-merge")
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

	// QUM-1090: the second rev-parse reads the PARENT tip and both
	// update-ref writes land before reset-soft, the first mutation.
	expected := []string{
		"lock", "merge-base", "rev-parse", "rev-parse",
		"update-ref", "update-ref", "reset-soft", "commit",
		"rebase", "ff-merge", "validate", "poke", "unlock",
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

func TestMerge_CommitMessage_WithOverride(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	cfg.MessageOverride = "Custom merge message"

	var capturedMessage string
	deps.GitCommit = func(worktree, message string) (string, error) {
		capturedMessage = message
		return "abc1234", nil
	}

	_, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(capturedMessage, "Custom merge message") {
		t.Errorf("commit message should contain override, got: %q", capturedMessage)
	}
	if !strings.Contains(capturedMessage, "Co-Authored-By:") {
		t.Errorf("commit message should contain co-author, got: %q", capturedMessage)
	}
}

// TestMerge_CommitMessage_Default supersedes a test of the same name that
// asserted the commit message contained AgentState.LastReportMessage — i.e.
// it ratified the QUM-1105 defect, and would have had to be deleted to fix
// it. Recorded rather than quietly rewritten: a test asserting the current
// mechanism instead of the intended outcome is the shape that makes a defect
// look load-bearing.
func TestMerge_CommitMessage_Default(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	var capturedMessage string
	deps.GitCommit = func(worktree, message string) (string, error) {
		capturedMessage = message
		return "abc1234", nil
	}

	_, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(capturedMessage, "COMMIT-SUBJ-SENTINEL") {
		t.Errorf("commit message should carry the agent commit's subject, got: %q", capturedMessage)
	}
	if !strings.Contains(capturedMessage, "COMMIT-BODY-SENTINEL") {
		t.Errorf("commit message should carry the agent commit's body, got: %q", capturedMessage)
	}
	if strings.Contains(capturedMessage, blurbSentinel) {
		t.Errorf("the status blurb reached the commit message, got: %q", capturedMessage)
	}
	if !strings.Contains(capturedMessage, "Co-Authored-By:") {
		t.Errorf("commit message should contain co-author, got: %q", capturedMessage)
	}
	// The recovery ref is where the source commits still exist after the
	// squash, so the message pointing at it is the whole reason the SHA index
	// is useful. Asserted rather than read off the comment that claims it.
	if !strings.Contains(capturedMessage, testPremergeBase+"/agent") {
		t.Errorf("commit message should name the premerge /agent recovery ref, got: %q", capturedMessage)
	}
}

// TestMerge_CommitMessage_DerivationErrorAbortsBeforeAnyMutation — a failing
// GitLogRange must refuse the merge while the branch is still intact, never
// after `reset --soft` has rewound it. Complements S14, which covers the
// empty-range half in real git.
func TestMerge_CommitMessage_DerivationErrorAbortsBeforeAnyMutation(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	deps.GitLogRange = func(worktree, base, head string) ([]CommitRecord, error) {
		return nil, errors.New("LOGRANGE-BOOM")
	}
	var mutated []string
	deps.GitUpdateRef = func(worktree, ref, newSHA string) error {
		mutated = append(mutated, "GitUpdateRef")
		return nil
	}
	deps.GitResetSoft = func(worktree, ref string) error {
		mutated = append(mutated, "GitResetSoft")
		return nil
	}
	deps.GitCommit = func(worktree, message string) (string, error) {
		mutated = append(mutated, "GitCommit")
		return "abc1234", nil
	}

	_, err := Merge(context.Background(), cfg, deps)

	if err == nil {
		t.Fatal("merge should fail when the agent's commits cannot be read")
	}
	if !strings.Contains(err.Error(), "LOGRANGE-BOOM") {
		t.Errorf("error should wrap the cause, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--message") {
		t.Errorf("error should name the explicit-message remedy, got: %v", err)
	}
	if len(mutated) > 0 {
		t.Errorf("the repository was mutated despite an abortable failure: %v", mutated)
	}
}

// TestMerge_DryRun_ShowsTheDerivedMessage — the dry run reads the same
// derivation, so its preview cannot disagree with what the real merge would
// write. The second leg pins that a derivation failure is reported as the
// blocker it is: a dry run that prints a plan for a merge that will refuse is
// worse than one that prints nothing.
func TestMerge_DryRun_ShowsTheDerivedMessage(t *testing.T) {
	t.Run("derivable", func(t *testing.T) {
		deps := newTestDeps()
		cfg := newTestConfig()
		cfg.DryRun = true
		var stderr bytes.Buffer
		deps.Stderr = &stderr

		if _, err := Merge(context.Background(), cfg, deps); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(stderr.String(), "COMMIT-BODY-SENTINEL") {
			t.Errorf("dry run should preview the derived message, got:\n%s", stderr.String())
		}
		if strings.Contains(stderr.String(), blurbSentinel) {
			t.Errorf("dry run previewed the status blurb, got:\n%s", stderr.String())
		}
	})

	// Both revision reads failing yields "(unknown)" for each, and two
	// unrelated failures then compare EQUAL. Keying the no-op purely on the
	// values would report "no-op (no new commits)" — a claim about the
	// branch — for a dry run that could read nothing at all.
	t.Run("both revision reads fail", func(t *testing.T) {
		deps := newTestDeps()
		cfg := newTestConfig()
		cfg.DryRun = true
		deps.GitMergeBase = func(repoRoot, a, b string) (string, error) { return "", errors.New("BASE-BOOM") }
		deps.GitRevParseHead = func(worktree string) (string, error) { return "", errors.New("HEAD-BOOM") }
		var logged bool
		deps.GitLogRange = func(worktree, base, head string) ([]CommitRecord, error) {
			logged = true
			return nil, nil
		}
		var stderr bytes.Buffer
		deps.Stderr = &stderr

		result, err := Merge(context.Background(), cfg, deps)
		if err != nil {
			t.Fatalf("dry run should not itself fail: %v", err)
		}
		if result.WasNoOp {
			t.Error("a dry run that could read neither revision must not claim the merge is a no-op")
		}
		if logged {
			t.Error(`the literal "(unknown)" was handed to git log as a revision`)
		}
		for _, want := range []string{"BASE-BOOM", "HEAD-BOOM"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("the real cause %q was swallowed, got:\n%s", want, stderr.String())
			}
		}
	})

	t.Run("underivable", func(t *testing.T) {
		deps := newTestDeps()
		cfg := newTestConfig()
		cfg.DryRun = true
		deps.GitLogRange = func(worktree, base, head string) ([]CommitRecord, error) { return nil, nil }
		var stderr bytes.Buffer
		deps.Stderr = &stderr

		if _, err := Merge(context.Background(), cfg, deps); err != nil {
			t.Fatalf("dry run should not itself fail: %v", err)
		}
		if !strings.Contains(stderr.String(), "CANNOT BE DERIVED") {
			t.Errorf("dry run should report that the real merge would fail, got:\n%s", stderr.String())
		}
	})
}

func TestMerge_EmptyValidateCmd_SkipsWithWarning(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	cfg.ValidateCmd = ""
	cfg.NoValidate = false

	var testsCalled bool
	deps.RunTestsStreaming = func(ctx context.Context, dir, command string, sink func(string)) (string, error) {
		testsCalled = true
		return "ok", nil
	}

	var stderr bytes.Buffer
	deps.Stderr = &stderr

	var pokeCalled bool
	deps.WritePoke = func(sprawlRoot, agentName, content string) error {
		pokeCalled = true
		return nil
	}

	result, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if testsCalled {
		t.Error("RunTests should NOT be called when ValidateCmd is empty")
	}
	if !strings.Contains(stderr.String(), "no validate command configured") {
		t.Errorf("stderr should warn about missing validate command, got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "sprawl config set validate") {
		t.Errorf("stderr should contain config hint 'sprawl config set validate', got: %q", stderr.String())
	}
	if !pokeCalled {
		t.Error("poke should still be written when ValidateCmd is empty")
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
}

func TestMerge_ValidateCmd_PassedToRunTests(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	cfg.ValidateCmd = "npm test"

	var capturedCommand string
	deps.RunTestsStreaming = func(ctx context.Context, dir, command string, sink func(string)) (string, error) {
		capturedCommand = command
		return "ok", nil
	}

	_, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedCommand != "npm test" {
		t.Errorf("RunTests command = %q, want %q", capturedCommand, "npm test")
	}
}

// TestMerge_CommitMessage_NoReport supersedes a test of the same name that
// asserted the `<agent>: merge branch '<branch>'` placeholder — the SECOND
// silent fallback QUM-1105 removes. Same note as TestMerge_CommitMessage_
// Default: it is recorded, not quietly dropped.
func TestMerge_CommitMessage_NoReport(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	cfg.AgentState.LastReportMessage = ""

	var capturedMessage string
	deps.GitCommit = func(worktree, message string) (string, error) {
		capturedMessage = message
		return "abc1234", nil
	}

	_, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(capturedMessage, "COMMIT-SUBJ-SENTINEL") {
		t.Errorf("an absent status report must not change the source of the message, got: %q", capturedMessage)
	}
	if strings.Contains(capturedMessage, "merge branch") {
		t.Errorf("the no-report placeholder subject is still being produced, got: %q", capturedMessage)
	}
}

// --- QUM-494: per-call checkpoint observability ---

// recordingCheckpoint returns a Deps.Checkpoint and a pointer to the
// recorded steps slice.
func recordingCheckpoint() (func(step string, kv ...any), *[]string) {
	var steps []string
	return func(step string, _ ...any) {
		steps = append(steps, step)
	}, &steps
}

func TestMerge_EmitsCheckpointSequence(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	cp, steps := recordingCheckpoint()
	deps.Checkpoint = cp

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("merge: %v", err)
	}

	want := []string{
		"merge.lock-acquired",
		"merge.premerge-refs-written",
		"merge.squash-committed",
		"merge.rebased",
		"merge.ff-merged",
		"merge.validate-started",
		"merge.validate-ended",
		"merge.poke-written",
	}
	if len(*steps) != len(want) {
		t.Fatalf("steps = %v, want %v", *steps, want)
	}
	for i, s := range want {
		if (*steps)[i] != s {
			t.Errorf("steps[%d] = %q, want %q (full: %v)", i, (*steps)[i], s, *steps)
		}
	}
}

func TestMerge_CheckpointEmitsValidateEndedOnFailure(t *testing.T) {
	// QUM-588: validate-ended is emitted on BOTH success and failure with
	// an `exit` kv so the TUI popup can detect end-of-validate regardless
	// of outcome and auto-restore on failure. poke-written remains
	// success-only because the merge is rolled back on failure.
	deps := newTestDeps()
	cfg := newTestConfig()

	deps.RunTestsStreaming = func(ctx context.Context, dir, command string, sink func(string)) (string, error) {
		return "FAIL", fmt.Errorf("tests failed")
	}

	cp, steps := recordingCheckpoint()
	deps.Checkpoint = cp

	if _, err := Merge(context.Background(), cfg, deps); err == nil {
		t.Fatal("expected merge to fail when validate fails")
	}

	if len(*steps) == 0 {
		t.Fatal("expected at least one checkpoint")
	}
	last := (*steps)[len(*steps)-1]
	if last != "merge.validate-ended" {
		t.Errorf("last step = %q, want merge.validate-ended (steps=%v)", last, *steps)
	}
	for _, s := range *steps {
		if s == "merge.poke-written" {
			t.Error("poke-written should not be emitted on validate failure")
		}
	}
}

func TestMerge_NilCheckpointSafe(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	deps.Checkpoint = nil

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("merge with nil Checkpoint panicked: %v", r)
		}
	}()
	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Errorf("merge: %v", err)
	}
}

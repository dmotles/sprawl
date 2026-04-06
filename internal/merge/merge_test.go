package merge

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

func newTestDeps() *Deps {
	return &Deps{
		LockAcquire:     func(lockPath string) (func(), error) { return func() {}, nil },
		GitMergeBase:    func(repoRoot, a, b string) (string, error) { return "aaa111", nil },
		GitRevParseHead: func(worktree string) (string, error) { return "bbb222", nil },
		GitResetSoft:    func(worktree, ref string) error { return nil },
		GitCommit:       func(worktree, message string) (string, error) { return "ccc333", nil },
		GitRebase:       func(worktree, onto string) error { return nil },
		GitRebaseAbort:  func(worktree string) error { return nil },
		GitFFMerge:      func(worktree, branch string) error { return nil },
		GitResetHard:    func(worktree string) error { return nil },
		RunTests:        func(dir string) (string, error) { return "ok", nil },
		WritePoke:       func(dendraRoot, agentName, content string) error { return nil },
		Stderr:          io.Discard,
	}
}

func newTestConfig() *Config {
	return &Config{
		DendraRoot:     "/tmp/dendra-test",
		AgentName:      "test-agent",
		AgentBranch:    "dendra/test-agent",
		AgentWorktree:  "/worktree/agent",
		ParentBranch:   "main",
		ParentWorktree: "/worktree/parent",
		AgentState: &state.AgentState{
			Name:              "test-agent",
			Type:              "engineer",
			Family:            "engineering",
			Branch:            "dendra/test-agent",
			LastReportMessage: "completed the task",
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
		if branch != "dendra/test-agent" {
			t.Errorf("ff-merge branch = %q, want dendra/test-agent", branch)
		}
		return nil
	}

	var pokeCalled bool
	deps.WritePoke = func(dendraRoot, agentName, content string) error {
		pokeCalled = true
		if agentName != "test-agent" {
			t.Errorf("poke agent = %q, want test-agent", agentName)
		}
		return nil
	}

	result, err := Merge(cfg, deps)
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
	deps.WritePoke = func(dendraRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(cfg, deps)
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
	deps.WritePoke = func(dendraRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(cfg, deps)
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

	result, err := Merge(cfg, deps)
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
	deps.WritePoke = func(dendraRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(cfg, deps)
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
	deps.WritePoke = func(dendraRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(cfg, deps)
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

	deps.RunTests = func(dir string) (string, error) {
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
	deps.WritePoke = func(dendraRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(cfg, deps)
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
	deps.RunTests = func(dir string) (string, error) {
		testsCalled = true
		return "ok", nil
	}

	var pokeCalled bool
	deps.WritePoke = func(dendraRoot, agentName, content string) error { pokeCalled = true; return nil }

	result, err := Merge(cfg, deps)
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
	deps.WritePoke = func(dendraRoot, agentName, content string) error {
		order = append(order, "poke")
		return nil
	}
	deps.LockAcquire = func(lockPath string) (func(), error) {
		return func() { order = append(order, "unlock") }, nil
	}

	_, err := Merge(cfg, deps)
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
		return "bbb222", nil
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
	deps.RunTests = func(dir string) (string, error) {
		order = append(order, "validate")
		return "ok", nil
	}
	deps.WritePoke = func(dendraRoot, agentName, content string) error {
		order = append(order, "poke")
		return nil
	}

	_, err := Merge(cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{
		"lock", "merge-base", "rev-parse", "reset-soft", "commit",
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

	_, err := Merge(cfg, deps)
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

func TestMerge_CommitMessage_Default(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	var capturedMessage string
	deps.GitCommit = func(worktree, message string) (string, error) {
		capturedMessage = message
		return "abc1234", nil
	}

	_, err := Merge(cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(capturedMessage, "test-agent:") {
		t.Errorf("commit message should contain agent name, got: %q", capturedMessage)
	}
	if !strings.Contains(capturedMessage, "completed the task") {
		t.Errorf("commit message should contain last report, got: %q", capturedMessage)
	}
	if !strings.Contains(capturedMessage, "Co-Authored-By:") {
		t.Errorf("commit message should contain co-author, got: %q", capturedMessage)
	}
}

func TestMerge_CommitMessage_NoReport(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	cfg.AgentState.LastReportMessage = ""

	var capturedMessage string
	deps.GitCommit = func(worktree, message string) (string, error) {
		capturedMessage = message
		return "abc1234", nil
	}

	_, err := Merge(cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(capturedMessage, "test-agent:") {
		t.Errorf("commit message should contain agent name, got: %q", capturedMessage)
	}
	if !strings.Contains(capturedMessage, "merge branch") {
		t.Errorf("commit message should use fallback format, got: %q", capturedMessage)
	}
}

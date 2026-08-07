package merge

// QUM-1087's ref-move acceptance criterion: step 3 must be PROVEN a pure ref
// move by direct predicates, not inferred from `--ff-only` exiting 0.
//
// The reason `--ff-only`'s exit status cannot stand in for either predicate is
// the S5b shape: with the agent's content already upstream, the rebase drops
// it, the branch and the parent become the same commit, and `--ff-only` exits 0
// having moved nothing. That is precisely the case a validate-failure rollback
// then mis-rewound. So:
//
//	before: merge-base --is-ancestor <parent-tip> <rebased-branch-tip>  (exit 0)
//	after:  <parent tip after> == <rebased branch tip>                  (SHA equality)
//
// The SHA-equality half is the one that DISCRIMINATES — the ancestor check is
// also satisfied in the no-move case, since a commit is its own ancestor.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// The two diagnosis phrases that must identify their paths UNIQUELY. Declared
// once and asserted from both sides — each path requires its own phrase and
// requires the absence of the other's. A phrase both paths could emit is a
// source-level distinction, not an assertion-level one.
const (
	ffPredicatePhrase = "did not produce a fast-forwardable branch"
	parentMovedPhrase = "moved during validation"
)

// ancestorCall records one GitIsAncestor invocation with its arguments in the
// order the seam received them.
type ancestorCall struct {
	worktree   string
	ancestor   string
	descendant string
}

// TestMerge_FFPredicate_IsInvokedWithTheParentAsAncestor pins the ARGUMENT
// ORDER, and it needs to be its own test rather than a clause inside the
// happy-path one.
//
// `--is-ancestor <parent> <branch>` asks "is the parent contained in the
// branch" — the question whose true answer means "fast-forwardable". The
// reversed order asks "is the branch contained in the parent", a DIFFERENT
// question that is also true whenever the two are equal. This is CLAUDE.md's
// "check that the question the command answers is the question you are
// claiming" hazard.
//
// WHAT A SWAP ACTUALLY DOES, measured rather than assumed. An earlier version of
// this comment claimed a swap "leaves every other assertion green"; that was
// wrong, and the negative control said so. In the MOCK-based tests in this
// package the swap IS invisible — newTestDeps' GitIsAncestor returns true
// regardless of its arguments, so nothing else can notice — and that is what
// this test is for. In the REAL-GIT scenario tests it is caught loudly:
// post-rebase the parent is a strict ancestor of the branch, so the reversed
// question answers FALSE and the merge refuses, breaking HappyPath_NoLoss, S3,
// S5c and AgentBranchMovesDuringValidation.
//
// Both facts are worth keeping straight. The mocked layer needs this test
// because it cannot see the swap; the real-git layer is defence in depth that
// happens to catch it. Do not delete this test on the strength of the scenario
// tests catching it — a future fixture change that makes the scenarios not
// exercise a moved parent would silently remove that coverage, and this test
// would still hold.
func TestMerge_FFPredicate_IsInvokedWithTheParentAsAncestor(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	// The fixture already gives the parent and the branch DISTINCT tips and
	// models the ff as a ref move. Both matter here: distinct tips are what
	// make a swapped argument order detectable at all, and the modelled ref
	// move is what lets the merge reach its end so this assertion runs.
	var calls []ancestorCall
	inner := deps.GitIsAncestor
	deps.GitIsAncestor = func(worktree, ancestor, descendant string) (bool, error) {
		calls = append(calls, ancestorCall{worktree, ancestor, descendant})
		return inner(worktree, ancestor, descendant)
	}

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("merge: %v", err)
	}

	if len(calls) == 0 {
		t.Fatal("the ff-merge precondition was never checked: --ff-only's exit status is being trusted instead of a direct predicate")
	}
	c := calls[0]
	if c.ancestor == c.descendant {
		t.Fatalf("fixture precondition: the two tips must differ, or a swapped argument order is undetectable (both %q)", c.ancestor)
	}
	if c.ancestor != testParentTip {
		t.Errorf("--is-ancestor ancestor arg = %q, want the PARENT tip %q.\n"+
			"The predicate must ask \"is the parent contained in the rebased branch\"; with the\n"+
			"arguments reversed it asks whether the branch is contained in the parent, which is a\n"+
			"different claim that is also true whenever the two are equal.", c.ancestor, testParentTip)
	}
	if c.descendant != testAgentTip {
		t.Errorf("--is-ancestor descendant arg = %q, want the REBASED BRANCH tip %q", c.descendant, testAgentTip)
	}
}

// TestMerge_FFPredicate_FalseRefusesTheMerge — the predicate says the parent is
// not contained in the rebased branch, so the rebase did not produce a
// fast-forwardable branch. Surface that; do not reconcile it, and do not
// proceed to the ff.
func TestMerge_FFPredicate_FalseRefusesTheMerge(t *testing.T) {
	tr := &seamTrace{}
	deps, _ := tracedDeps(tr)
	cfg := newTestConfig()

	var ffCalled bool
	deps.GitFFMerge = func(string, string) error { ffCalled = true; return nil }
	deps.GitIsAncestor = func(string, string, string) (bool, error) { return false, nil }

	_, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected the merge to refuse when the ff precondition is false")
	}
	if ffCalled {
		t.Error("the ff-merge ran despite its precondition being false")
	}
	// The path-unique assertion. NOT the bare substring "rebase": today's
	// parent-moved message also contains the word "rebase" ("unexpected after
	// a clean rebase"), and a remedy phrased as "re-rebase and retry" would
	// too — so "rebase" cannot discriminate. A dedicated phrase can.
	if !strings.Contains(err.Error(), ffPredicatePhrase) {
		t.Errorf("the error must diagnose the rebase as the cause (QUM-1087: %q), want the phrase %q, got: %v",
			"step 1 did not rebase correctly and that is the signal to surface", ffPredicatePhrase, err)
	}
	if strings.Contains(err.Error(), parentMovedPhrase) {
		t.Errorf("this path must NOT be reported as a parent that moved during validation: %v", err)
	}
	// AC 1/AC 2 hold on THIS path too, not just on the validate-failure one.
	if got := tr.mutationsAgainst(cfg.ParentWorktree); len(got) != 0 {
		t.Errorf("the parent was mutated by %v on the ff-precondition-false path; want none\ntrace: %v", got, tr.calls)
	}
}

// TestMerge_FFPredicate_ErrorIsNotSilentlyFalse — a genuine git failure in the
// predicate must not read as "not an ancestor". The two have opposite correct
// responses: one is a broken repository to report, the other is a rebase
// diagnosis.
func TestMerge_FFPredicate_ErrorIsNotSilentlyFalse(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	sentinel := errors.New("IS-ANCESTOR-BROKEN-SENTINEL")
	deps.GitIsAncestor = func(string, string, string) (bool, error) { return false, sentinel }
	var ffCalled bool
	deps.GitFFMerge = func(string, string) error { ffCalled = true; return nil }

	_, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected the merge to fail when the ff precondition cannot be evaluated")
	}
	if ffCalled {
		t.Error("the ff-merge ran despite the precondition being unevaluable")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("the underlying git failure must be wrapped, not replaced by a rebase diagnosis: %v", err)
	}
}

// TestMerge_PostFF_ParentTipMustEqualTheRebasedTip is the DISCRIMINATING half
// of the ref-move proof. `--ff-only` exits 0 without moving the parent when it
// is already up to date; only the SHA equality catches that.
func TestMerge_PostFF_ParentTipMustEqualTheRebasedTip(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	const rebasedTip = "REBASED-BRANCH-TIP-SENTINEL"
	// The parent tip does NOT change across the ff — exactly the S5b shape,
	// where --ff-only reports "Already up to date" and exits 0.
	deps.GitRevParseHead = func(worktree string) (string, error) {
		if worktree == cfg.ParentWorktree {
			return "PARENT-TIP-UNMOVED", nil
		}
		return "agent-head", nil
	}
	deps.GitRevParseRef = func(worktree, rev string) (string, error) {
		if rev == cfg.AgentBranch {
			return rebasedTip, nil
		}
		return "PARENT-TIP-UNMOVED", nil
	}
	deps.GitIsAncestor = func(string, string, string) (bool, error) { return true, nil }
	deps.GitFFMerge = func(string, string) error { return nil } // exits 0, moves nothing

	_, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected the merge to fail: --ff-only exited 0 without moving the parent, which is the S5b shape")
	}
	// Both SHAs named, so the reader can see WHICH two disagreed.
	if !strings.Contains(err.Error(), rebasedTip) || !strings.Contains(err.Error(), "PARENT-TIP-UNMOVED") {
		t.Errorf("the error must name both the parent tip and the rebased tip, got: %v", err)
	}
}

// TestMerge_ParentMovedDuringValidation_FailsLoudly — the AC's race case. The
// correct response is a loud failure naming both SHAs so the caller can
// re-rebase and re-run; it is explicitly NOT something to reconcile.
func TestMerge_ParentMovedDuringValidation_FailsLoudly(t *testing.T) {
	tr := &seamTrace{}
	deps, _ := tracedDeps(tr)
	cfg := newTestConfig()

	// The parent tip read BEFORE validation differs from the one read after —
	// a second merge landed while validate was running.
	const before = "PARENT-TIP-BEFORE-VALIDATE"
	const after = "PARENT-TIP-MOVED-DURING-VALIDATE"
	validated := false
	deps.RunTestsStreaming = func(ctx context.Context, dir, cmd string, sink func(string)) (string, error) {
		validated = true
		return "ok", nil
	}
	deps.GitRevParseHead = func(worktree string) (string, error) {
		if worktree == cfg.ParentWorktree {
			if validated {
				return after, nil
			}
			return before, nil
		}
		return "agent-head", nil
	}
	deps.GitIsAncestor = func(string, string, string) (bool, error) { return true, nil }
	ffErr := errors.New("FF-ONLY-REFUSED-SENTINEL: Not possible to fast-forward, aborting")
	deps.GitFFMerge = func(string, string) error { return ffErr }

	_, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected a loud failure when --ff-only is refused")
	}
	// The path-unique assertion: this path, and only this path, must diagnose
	// that the PARENT MOVED, and must name both tips. The ff-precondition
	// path above diagnoses the rebase instead. That is what makes the two
	// distinguishable at the assertion level rather than by reading which
	// function was called.
	if !strings.Contains(err.Error(), before) || !strings.Contains(err.Error(), after) {
		t.Errorf("the error must name the parent tip before AND after validation, got: %v", err)
	}
	if !strings.Contains(err.Error(), parentMovedPhrase) {
		t.Errorf("the error must diagnose a parent that moved during validation (phrase %q), got: %v", parentMovedPhrase, err)
	}
	if strings.Contains(err.Error(), ffPredicatePhrase) {
		t.Errorf("this path must NOT be reported as a bad rebase — the rebase was fine and the parent moved underneath it: %v", err)
	}
	if !errors.Is(err, ffErr) {
		t.Errorf("git's own refusal must be wrapped: %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "unexpected") {
		t.Errorf("a parent that moved during validation is an EXPECTED race with a defined remedy, not an anomaly: %v", err)
	}
	// This is the path where a "tidy up after ourselves" instinct is most
	// likely to reach for a reset, so the parent-untouched invariant is
	// asserted here explicitly rather than assumed from the other tests.
	// GitFFMerge is permitted: it was ATTEMPTED and refused by git, which is
	// the whole scenario.
	for _, seam := range tr.mutationsAgainst(cfg.ParentWorktree) {
		if seam != "GitFFMerge" {
			t.Errorf("the parent was mutated by %q after --ff-only was refused; want no mutation beyond the refused ff\ntrace: %v", seam, tr.calls)
		}
	}
}

// TestMerge_Result_ReportsTheFFdParentTip — the Result must not report a SHA
// that exists on no ref. Under the old squash flow it reported a pre-rebase
// squash hash, which after the rebase existed nowhere.
func TestMerge_Result_ReportsTheFFdParentTip(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	const rebasedTip = "REBASED-AND-FFD-TIP"
	ffDone := false
	deps.GitRevParseHead = func(worktree string) (string, error) {
		if worktree == cfg.ParentWorktree {
			if ffDone {
				return rebasedTip, nil
			}
			return "PARENT-TIP-BEFORE", nil
		}
		return "agent-head", nil
	}
	deps.GitRevParseRef = func(worktree, rev string) (string, error) {
		if rev == cfg.AgentBranch {
			return rebasedTip, nil
		}
		return "PARENT-TIP-BEFORE", nil
	}
	deps.GitIsAncestor = func(string, string, string) (bool, error) { return true, nil }
	deps.GitFFMerge = func(string, string) error { ffDone = true; return nil }

	res, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.MergedTip != rebasedTip {
		t.Errorf("Result.MergedTip = %q, want the ff'd parent tip %q", res.MergedTip, rebasedTip)
	}
	// Negative control on the same assertion: it must not be satisfiable by
	// the PRE-merge parent tip, which is the value a careless implementation
	// would carry forward.
	if res.MergedTip == "PARENT-TIP-BEFORE" {
		t.Error("Result.MergedTip is the pre-merge parent tip: it reports where the parent WAS, not what was merged")
	}
}

// TestMerge_FFRefusedWithParentUnmoved_DoesNotBlameTheRebase pins the
// discrimination in the THIRD case, which the first cut of ffMergeFailureError
// got wrong.
//
// The function tells "the parent moved during validation" from "the rebase did
// not produce a fast-forwardable branch" by re-reading the parent tip. That is a
// two-way split over three causes: `git merge --ff-only` ALSO refuses when the
// parent worktree has local changes it would overwrite, with the parent tip
// unmoved and the branch a perfectly good descendant. Verified directly against
// git — parent unmoved, `--is-ancestor` true, exit 1 with "Your local changes to
// the following files would be overwritten by merge".
//
// It is reachable rather than theoretical: precondition 7 checks the caller's
// worktree clean at merge START, and since QUM-1087 validation runs elsewhere
// for minutes, during which the parent checkout — a live weave or human working
// tree — can acquire edits.
//
// Blaming the rebase there would send the caller to re-rebase, which fixes
// nothing. So the message must name both candidates and defer to git's own text.
func TestMerge_FFRefusedWithParentUnmoved_DoesNotBlameTheRebase(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	// The parent tip is the SAME before and after validation — not the race.
	deps.RunTestsStreaming = func(ctx context.Context, dir, cmd string, sink func(string)) (string, error) {
		return "ok", nil
	}
	dirtyErr := errors.New("error: Your local changes to the following files would be overwritten by merge:\n\tf.txt")
	deps.GitFFMerge = func(string, string) error { return dirtyErr }

	_, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected the refused fast-forward to fail the merge")
	}
	// git's own text must survive, because it is what actually discriminates.
	if !errors.Is(err, dirtyErr) {
		t.Errorf("git's own refusal must be wrapped so the caller can see the real cause: %v", err)
	}
	// Not the race — the parent did not move.
	if strings.Contains(err.Error(), parentMovedFailure) {
		t.Errorf("must not report a parent that did not move as having moved: %v", err)
	}
	// And it must NOT assert the rebase as THE cause. Naming it as one
	// candidate is correct; stating it is what the first cut did wrong.
	msg := err.Error()
	if strings.Contains(msg, ffPredicateFailure) && !strings.Contains(msg, "Either") {
		t.Errorf("the message asserts the rebase as the sole cause; it must present both candidates and let git's message discriminate: %v", err)
	}
	if !strings.Contains(msg, "local changes") {
		t.Errorf("the message must name the dirty-parent-worktree possibility, got: %v", err)
	}
	// Directing the caller to just re-run would be actively wrong here.
	if strings.Contains(strings.ToLower(msg), "re-run the merge") && !strings.Contains(msg, "will not help") {
		t.Errorf("must not tell the caller to simply re-run when re-running cannot help: %v", err)
	}
}

// TestMerge_PostRebaseNoOp_IsReportedNotSilent covers the case flux found: with
// the agent's delta already upstream under other SHAs, the rebase drops
// everything, the ff is a ref move to where the parent already is, and step 10's
// equality HOLDS — so the merge succeeds having moved nothing.
//
// That success is correct and QUM-1087 lists reclassifying it as out of scope
// (it is a ref move to where the parent already is, and the content really is on
// the parent). What is NOT acceptable is it being indistinguishable from a
// landing: "Merged agent X" with an unchanged parent tip is exactly the shape
// this series exists to stop people misreading. So it is announced.
//
// The design doc previously claimed QUM-1087 "covers detecting the post-rebase
// no-op". It does not, and did not; that claim is deleted.
func TestMerge_PostRebaseNoOp_IsReportedNotSilent(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	var stderr bytes.Buffer
	deps.Stderr = &stderr

	// The rebase drops everything: the branch tip IS the parent tip.
	const sameTip = "PARENT-AND-BRANCH-SAME-TIP"
	deps.GitRevParseHead = func(worktree string) (string, error) {
		if worktree == cfg.ParentWorktree {
			return sameTip, nil
		}
		return testAgentTip, nil
	}
	deps.GitRevParseRef = func(worktree, rev string) (string, error) { return sameTip, nil }

	res, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("a post-rebase no-op is a legitimate success, not an error: %v", err)
	}
	if res.MergedTip != sameTip {
		t.Errorf("MergedTip = %q, want the (unchanged) parent tip %q", res.MergedTip, sameTip)
	}
	// The point of the test: it must SAY the parent did not move.
	out := stderr.String()
	if !strings.Contains(out, "did not move") {
		t.Errorf("a merge that moved nothing must say so; stderr was:\n%s", out)
	}
	if !strings.Contains(out, "already present") {
		t.Errorf("the note must explain WHY nothing moved (content already upstream), got:\n%s", out)
	}
}

// TestMerge_OrdinaryMerge_DoesNotClaimANoOp is the negative control for the
// above: the note must not appear on a merge that really did advance the parent,
// or it is noise that trains readers to ignore it.
func TestMerge_OrdinaryMerge_DoesNotClaimANoOp(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	var stderr bytes.Buffer
	deps.Stderr = &stderr

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if strings.Contains(stderr.String(), "did not move") {
		t.Errorf("an ordinary merge that advanced the parent must not report a no-op; stderr:\n%s", stderr.String())
	}
}

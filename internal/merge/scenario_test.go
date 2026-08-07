package merge

// Merge-safety scenario tests: they drive the real merge engine with its
// production git deps against real git repos and assert what is REACHABLE
// FROM REFS afterwards.
//
// Why this shape, and why here. The invariant these guard is a property of
// git state, not of a call sequence — "no committed work becomes unreachable
// from all refs" cannot be checked with mock Deps, because mocks have no
// object store. The unit tests in merge_test.go / premerge_test.go pin what
// the engine CALLS; these pin what the repository LOOKS LIKE afterwards.
// Both are needed and neither substitutes for the other.
//
// Provenance: ported from the scratch research harness behind the merge
// safety analysis (`.sprawl/incidents/2026-08-06-merge-safety-audit/`). As
// tests rather than a program someone remembers to run, they are compiled,
// vetted, linted and race-checked by every `make validate`, so the invariant
// stays gated after this series ends. Contract: `docs/designs/merge-engine.md`.
//
// Porting status, kept current because a stale one misleads in both
// directions:
//   - S5b (agent content already upstream) IS ported — QUM-1087 gated it and
//     QUM-1087 has landed. It is the headline scenario.
//   - S5c (parent moves during validation) IS ported, but NOT as the analysis's
//     wall-clock race, which would flake. The validate command itself commits
//     to the parent, making the interleaving scripted and deterministic.
//   - S5c's TWO-MERGE form IS now ported as well, and still not as a race:
//     TestMergeSafety_ConcurrentMergesOfDifferentAgents_NoLoss drives two real
//     Merge calls for DIFFERENT AGENTS (which the per-agent flock does not
//     serialise) and scripts the interleaving through an injected
//     RunTestsStreaming. It is the argument for QUM-1089 being an efficiency
//     issue rather than a safety one, so read its header before weakening
//     anything it touches — QUM-1089 AC #5 points back at it by name.
//   - S7 (QUM-1088, the stale advertised branch on the retire path) is NOT here.
//     It is a retire-layer defect and lives in internal/agentops/retire_test.go
//     and internal/supervisor/real_retire_merge_test.go.
//
// Reflog is excluded from every REACHABILITY check — the invariant is about
// refs, and a reflog entry does not survive `git gc`. It is used, in exactly
// one helper (reflogEntryCount), to answer a DIFFERENT question: how many times
// was a ref moved? That question has no ref-based answer, and it is the only
// way to distinguish "the parent was left alone" from "the parent was moved
// and then moved back", which is precisely QUM-1087's claim.

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/state"
)

// scenarioRepo is a sandbox: a "main checkout" that doubles as SprawlRoot and
// the parent worktree, plus agent worktrees.
type scenarioRepo struct {
	t       *testing.T
	root    string
	commits map[string]string // label -> full SHA
}

func newScenarioRepo(t *testing.T) *scenarioRepo {
	t.Helper()
	r := &scenarioRepo{t: t, root: t.TempDir(), commits: map[string]string{}}
	r.git(r.root, "init", "-q", "-b", "main")
	r.commitFile(r.root, "init", "README.md", "base\n")
	return r
}

// git runs a git command that must succeed.
func (r *scenarioRepo) git(dir string, args ...string) string {
	r.t.Helper()
	out, err := r.gitOK(dir, args...)
	if err != nil {
		r.t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return out
}

// scenarioGit runs a git command and returns its combined output and error.
//
// A FREE FUNCTION rather than a *scenarioRepo method, and deliberately so:
// TestMergeSafety_ConcurrentMergesOfDifferentAgents_NoLoss calls it from a
// NON-TEST GOROUTINE. `t.Fatalf` off the test goroutine calls runtime.Goexit on
// the wrong goroutine — the test does not stop, it keeps running with the
// failure recorded and the remaining assertions silently skipped. Keeping this
// t-free makes that impossible by construction rather than by remembering.
// Anything reachable from that goroutine must touch neither r.t nor r.commits
// (an unsynchronised map).
func scenarioGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@x",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@x",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// gitOK runs a git command and returns its combined output and error.
func (r *scenarioRepo) gitOK(dir string, args ...string) (string, error) {
	return scenarioGit(dir, args...)
}

func (r *scenarioRepo) commitFile(dir, label, file, content string) string {
	r.t.Helper()
	full := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		r.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		r.t.Fatalf("write: %v", err)
	}
	r.git(dir, "add", "--", file)
	r.git(dir, "commit", "-q", "-m", label)
	sha := r.git(dir, "rev-parse", "HEAD")
	r.commits[label] = sha
	return sha
}

func (r *scenarioRepo) addWorktree(branch string) string {
	r.t.Helper()
	wt := filepath.Join(r.root, ".sprawl", "worktrees", strings.ReplaceAll(branch, "/", "_"))
	r.git(r.root, "worktree", "add", "-q", "-b", branch, wt)
	return wt
}

func (r *scenarioRepo) sha(ref string) string { return r.git(r.root, "rev-parse", ref) }

// reachableFromBranches reports whether sha is an ancestor of any refs/heads/*
// tip. `git branch --contains` walks ONLY refs/heads/*, which is what makes
// it a meaningful complement to reachableFromPremergeRefs below.
func (r *scenarioRepo) reachableFromBranches(sha string) bool {
	out, err := r.gitOK(r.root, "branch", "--contains", sha)
	return err == nil && strings.TrimSpace(out) != ""
}

// reachableFromParentBranch reports whether sha is reachable from the PARENT
// branch specifically (`git merge-base --is-ancestor sha main`).
//
// This is the checker reachableFromBranches cannot be. The invariant the ACs
// state is about the PARENT — "a validate failure leaves the parent branch's
// SHA byte-identical" — and reachableFromBranches walks all of refs/heads/*,
// so it reports "reachable" for a commit that main lost as long as ANY branch
// still contains it. That is not a hypothetical weakness: in the S5b shape
// below the rebase leaves the agent branch pointing AT the old parent tip, so
// the agent branch holds the very commit main was rewound off and the
// branch-scoped checker goes green on a real loss. S5b asserts the two
// checkers DISAGREE there, which is what makes this one's necessity a
// demonstrated fact rather than a design preference.
func (r *scenarioRepo) reachableFromParentBranch(sha string) bool {
	_, err := r.gitOK(r.root, "merge-base", "--is-ancestor", "--", sha, "main")
	return err == nil
}

// reflogEntryCount returns the number of reflog entries for ref.
//
// Reachability checks cannot see a mutation that was undone: a parent moved
// forward and then reset back is byte-identical at the end, and every SHA
// comparison passes. The reflog is the only witness that the parent was
// mutated AT ALL, which is the actual QUM-1087 claim — "the parent is mutated
// exactly once, forward-only, after the tree is already green". A rollback
// that restores the right SHA still violates it.
//
// Note this is the one place in this file that reads the reflog, and it does
// not contradict the file header's "reflog is deliberately excluded from every
// REACHABILITY check": it is not being used to decide whether an object
// survived. It is being used to count mutations of a ref.
func (r *scenarioRepo) reflogEntryCount(ref string) int {
	out, err := r.gitOK(r.root, "reflog", "show", "--format=%H", ref)
	if err != nil || strings.TrimSpace(out) == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(out), "\n"))
}

// reachableFromPremergeRefs reports whether sha is reachable from any ref
// under refs/sprawl/premerge/.
func (r *scenarioRepo) reachableFromPremergeRefs(sha string) bool {
	out, err := r.gitOK(r.root, "for-each-ref", "--format=%(refname)", "--contains", sha, PremergeRefPrefix)
	return err == nil && strings.TrimSpace(out) != ""
}

// premergeRefsForAgent returns the premerge refs whose <agent> component is
// agentName. The concurrent scenario has two merges writing four refs, so the
// per-agent pair has to be picked out of the set rather than counted globally
// (assertPremergePair's "exactly 2" would hard-fail there).
func (r *scenarioRepo) premergeRefsForAgent(agentName string) []string {
	var out []string
	for _, ref := range r.premergeRefs() {
		parts := strings.Split(ref, "/")
		if len(parts) == 6 && parts[3] == agentName {
			out = append(out, ref)
		}
	}
	return out
}

// reachableFromPremergeRefsOfAgent scopes the reachability question to ONE
// agent's refs. The unscoped reachableFromPremergeRefs would let agent A's
// recovery pair vouch for agent B's tip — which is the one claim a two-merge
// test must not be allowed to make, since it would pass with an entire merge's
// refs missing.
func (r *scenarioRepo) reachableFromPremergeRefsOfAgent(sha, agentName string) bool {
	out, err := r.gitOK(r.root, "for-each-ref", "--format=%(refname)",
		"--contains", sha, PremergeRefPrefix+"/"+agentName)
	return err == nil && strings.TrimSpace(out) != ""
}

func (r *scenarioRepo) premergeRefs() []string {
	out := r.git(r.root, "for-each-ref", "--format=%(refname)", PremergeRefPrefix)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func (r *scenarioRepo) fileOnRef(ref, path string) string {
	out, err := r.gitOK(r.root, "show", ref+":"+path)
	if err != nil {
		return ""
	}
	return out
}

// scenarioCfg builds a Config the way agentops.Merge does — the agent branch
// resolved from the worktree's actual HEAD branch (QUM-511).
func (r *scenarioRepo) scenarioCfg(agentName, agentBranch, agentWT, validateCmd string) *Config {
	return &Config{
		SprawlRoot:     r.root,
		AgentName:      agentName,
		AgentBranch:    agentBranch,
		AgentWorktree:  agentWT,
		ParentBranch:   "main",
		ParentWorktree: r.root,
		NoValidate:     validateCmd == "",
		ValidateCmd:    validateCmd,
		AgentState: &state.AgentState{
			Name: agentName, Branch: agentBranch, Type: "engineer", Family: "engineering",
		},
	}
}

// scenarioDeps is RealDeps with the poke write stubbed (no .sprawl/agents dir
// in these sandboxes) and stderr discarded.
func scenarioDeps() *Deps {
	d := RealDeps(io.Discard)
	d.WritePoke = func(_, _, _ string) error { return nil }
	return d
}

// assertPremergePair asserts exactly the two recovery refs exist and that
// each of the two named tips is reachable from them. It is the shared
// QUM-1090 part-A assertion for every scenario below.
func assertPremergePair(t *testing.T, r *scenarioRepo, agentTip, parentTip string) {
	t.Helper()
	refs := r.premergeRefs()
	if len(refs) != 2 {
		t.Fatalf("want exactly 2 premerge refs, got %d: %v", len(refs), refs)
	}
	gotAgent, gotParent := false, false
	for _, ref := range refs {
		switch {
		case strings.HasSuffix(ref, "/agent"):
			gotAgent = true
		case strings.HasSuffix(ref, "/parent"):
			gotParent = true
		default:
			t.Errorf("unexpected premerge ref leaf: %s", ref)
		}
		// Every ref must parse back out to a timestamp, or gc cannot age it.
		parts := strings.Split(ref, "/")
		if len(parts) != 6 {
			t.Errorf("ref %q does not split into 6 components", ref)
			continue
		}
		if _, err := time.Parse(PremergeTSLayout, parts[4]); err != nil {
			t.Errorf("ref %q timestamp does not parse with PremergeTSLayout: %v", ref, err)
		}
	}
	if !gotAgent || !gotParent {
		t.Errorf("want an /agent and a /parent ref, got %v", refs)
	}
	if !r.reachableFromPremergeRefs(agentTip) {
		t.Errorf("pre-merge AGENT tip %s is not reachable from any premerge ref (%v)", agentTip[:8], refs)
	}
	if !r.reachableFromPremergeRefs(parentTip) {
		t.Errorf("pre-merge PARENT tip %s is not reachable from any premerge ref (%v)", parentTip[:8], refs)
	}
}

// TestMergeSafety_ReachabilityCheckerDetectsLoss is the S0 POSITIVE CONTROL.
// Every assertion in this file rests on reachableFromBranches actually being
// able to report false, so plant a real loss and require it to be seen. A
// checker that can only say "reachable" would make every scenario below pass
// vacuously.
func TestMergeSafety_ReachabilityCheckerDetectsLoss(t *testing.T) {
	r := newScenarioRepo(t)
	victim := r.commitFile(r.root, "victim", "victim.txt", "v\n")

	if !r.reachableFromBranches(victim) {
		t.Fatal("precondition: the planted commit must start out reachable")
	}
	// A hard reset makes it reachable from no branch (reflog only).
	r.git(r.root, "reset", "--hard", "-q", "HEAD~1")

	if r.reachableFromBranches(victim) {
		t.Errorf("checker failed to detect a planted loss: %s still reports reachable", victim[:8])
	}
	if r.reachableFromPremergeRefs(victim) {
		t.Errorf("premerge checker reported reachable with no premerge refs written")
	}
}

// TestMergeSafety_ParentScopedCheckerDetectsLoss is the POSITIVE CONTROL for
// reachableFromParentBranch, and it also pins the exact case where it and
// reachableFromBranches must give DIFFERENT answers.
//
// Without the disagreement half this control would only prove the new checker
// can say no — which the old one can too — and would leave open the reading
// that it is the old checker under a new name. The two must be shown to
// measure different things on the same repository state.
func TestMergeSafety_ParentScopedCheckerDetectsLoss(t *testing.T) {
	r := newScenarioRepo(t)
	victim := r.commitFile(r.root, "victim", "victim.txt", "v\n")

	if !r.reachableFromParentBranch(victim) {
		t.Fatal("precondition: the planted commit must start out reachable from main")
	}

	// Park the commit on a side branch, then rewind main off it. Reachable
	// from a branch; NOT reachable from main.
	r.git(r.root, "branch", "keeper", victim)
	r.git(r.root, "reset", "--hard", "-q", "HEAD~1")

	if r.reachableFromParentBranch(victim) {
		t.Errorf("parent-scoped checker failed to detect a loss from main: %s still reports reachable", victim[:8])
	}
	t.Run("the two checkers disagree, which is why this one exists", func(t *testing.T) {
		if !r.reachableFromBranches(victim) {
			t.Fatalf("precondition: %s must still be branch-reachable via keeper, or this proves nothing", victim[:8])
		}
		// The branch-scoped checker says "fine" about a commit main lost.
		// This is the S5b blind spot, constructed directly.
		if r.reachableFromParentBranch(victim) {
			t.Error("the two checkers agreed; the parent-scoped one is not measuring the parent")
		}
	})
	t.Run("reflog counting can see a ref move", func(t *testing.T) {
		// Positive control for the OTHER new instrument. A reflog assertion
		// that can never report a change would make every "the parent was
		// never mutated" claim below vacuous.
		before := r.reflogEntryCount("main")
		if before == 0 {
			t.Fatal("precondition: main must already have reflog entries")
		}
		r.commitFile(r.root, "another", "another.txt", "a\n")
		if after := r.reflogEntryCount("main"); after <= before {
			t.Errorf("reflog count did not grow across a commit: %d -> %d", before, after)
		}
	})
}

// TestMergeSafety_S5b_AlreadyUpstream_ValidateFails is the HEADLINE QUM-1087
// scenario: the agent's content is already upstream under a different SHA, so
// the rebase drops it, `git merge --ff-only` exits 0 WITHOUT MOVING THE
// PARENT, and the validate-failure rollback then rewinds a commit that was
// never part of this merge.
//
// The audit deliberately did not port this (see the file header) because it
// gates QUM-1087. It is ported now, and it is the test that fails at HEAD with
// real, observable data loss on main.
//
// Note which assertion discriminates. `--ff-only` exiting 0 is exactly what
// makes this shape possible, so its exit status proves nothing; and
// reachableFromBranches ALSO proves nothing here, because the dropped rebase
// leaves the agent branch pointing at the old parent tip and therefore holding
// the very commit main lost. Only the parent-scoped checker and the SHA
// equality can see it.
func TestMergeSafety_S5b_AlreadyUpstream_ValidateFails(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/u")
	agentOrig := r.commitFile(wt, "u-work", "u.txt", "u\n")

	// The same change lands on main independently — same content, different
	// SHA. This is the QUM-1083 shape: a squash-merge elsewhere, a
	// cherry-pick, or a human applying the same patch.
	r.commitFile(r.root, "main-equivalent", "u.txt", "u\n")
	// ...and then main acquires a commit of its own, which is the victim.
	victim := r.commitFile(r.root, "main-victim", "victim.txt", "v\n")
	mainBefore := r.sha("main")
	reflogBefore := r.reflogEntryCount("main")

	if agentOrig == r.commits["main-equivalent"] {
		t.Fatal("precondition: the two commits must have different SHAs")
	}

	_, err := Merge(context.Background(), r.scenarioCfg("u", "agent/u", wt, "false"), scenarioDeps())

	if err == nil {
		t.Fatal("expected the merge to fail on validation")
	}
	// ARRIVAL. Every assertion below is satisfied by ANY early refusal — a
	// merge that bailed at step 2 for an unrelated reason would leave main
	// pristine and pass all of them, while never exercising the
	// already-upstream shape this test exists for. So first establish that we
	// got here the intended way.
	if !strings.Contains(err.Error(), "validation") {
		t.Fatalf("the merge did not fail on VALIDATION, so the already-upstream shape was never exercised: %v", err)
	}
	t.Run("the rebase really did drop the agent's work as already upstream", func(t *testing.T) {
		// The premise of the scenario, asserted rather than assumed. If the
		// rebase kept a commit, this is an ordinary merge and the S5b
		// no-move-on-ff condition never arose.
		if got := r.sha("agent/u"); got != mainBefore {
			t.Errorf("agent/u = %s, want it to have collapsed onto main's tip %s — the rebase did not drop the already-upstream work, so this is not the S5b shape",
				got[:8], mainBefore[:8])
		}
	})
	t.Run("main's SHA is byte-identical to before the merge was invoked", func(t *testing.T) {
		// AC 1, in its strong form: captured before invocation, compared
		// after. Not "GitResetHard was not called" — that is a claim about
		// our wiring, and it goes green if anything else moves the parent.
		if got := r.sha("main"); got != mainBefore {
			t.Errorf("main = %s, want byte-identical %s", got[:8], mainBefore[:8])
		}
	})
	t.Run("main's own commit is still reachable FROM MAIN", func(t *testing.T) {
		if !r.reachableFromParentBranch(victim) {
			t.Errorf("main-victim %s is no longer reachable from main: the rollback rewound a pre-existing parent commit", victim[:8])
		}
	})
	t.Run("the parent was never mutated at all, not merely restored", func(t *testing.T) {
		// The assertion an undone rollback cannot satisfy. QUM-1087's claim
		// is that the parent moves exactly once, forward, after the tree is
		// green — a mutate-then-restore is byte-identical at the end and
		// still violates it.
		if got := r.reflogEntryCount("main"); got != reflogBefore {
			t.Errorf("main's reflog grew %d -> %d: the parent was mutated during a merge that failed validation", reflogBefore, got)
		}
	})
	t.Run("both pre-merge tips stay ref-reachable", func(t *testing.T) {
		assertPremergePair(t, r, agentOrig, mainBefore)
	})
}

// TestMergeSafety_S2_RebaseConflict — the agent is behind main and both
// touched the same file, so the rebase conflicts and the merge fails.
//
// Before QUM-1090 this left the agent branch at the SQUASH commit with its
// original commits reachable from no ref, and printed a manual
// `git reset --hard` for a human to run. Now the tool restores the branch
// itself, and the recovery refs cover the attempt either way.
func TestMergeSafety_S2_RebaseConflict(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/b")
	agentOrig := r.commitFile(wt, "agent-conflict", "shared.txt", "agent version\n")
	r.commitFile(r.root, "main-conflict", "shared.txt", "main version\n")
	mainBefore := r.sha("main")

	_, err := Merge(context.Background(), r.scenarioCfg("b", "agent/b", wt, "true"), scenarioDeps())

	if err == nil {
		t.Fatal("expected the merge to fail on a rebase conflict")
	}
	t.Run("main is untouched by the failed merge", func(t *testing.T) {
		if got := r.sha("main"); got != mainBefore {
			t.Errorf("main = %s, want unchanged %s", got[:8], mainBefore[:8])
		}
		if !r.reachableFromBranches(r.commits["main-conflict"]) {
			t.Error("main's own commit became unreachable from all branches")
		}
	})
	t.Run("agent branch is auto-restored to its pre-merge tip", func(t *testing.T) {
		// INVERTED relative to the research harness this was ported from,
		// deliberately. That harness asserted `tip != orig` under the label
		// "agent branch NO LONGER at original commit (failed merge rewrote
		// it)" — i.e. it encoded the DEFECT as the expected outcome, and
		// passed. QUM-1090 part B makes the tool restore the branch, so the
		// correct assertion is the opposite one. If you are diffing this
		// file against the harness, this is not a porting error.
		if got := r.sha("agent/b"); got != agentOrig {
			t.Errorf("agent/b = %s, want its original tip %s (QUM-1090 part B restores it via CAS)", got[:8], agentOrig[:8])
		}
		if c := r.fileOnRef("agent/b", "shared.txt"); c != "agent version" {
			t.Errorf("agent content on the restored branch = %q, want %q", c, "agent version")
		}
	})
	t.Run("the restore leaves the agent worktree clean", func(t *testing.T) {
		// This is the claim rebaseFailureError's comment makes about why no
		// worktree reset is needed. Assert it rather than trusting it.
		out, gerr := r.gitOK(wt, "status", "--porcelain")
		if gerr != nil {
			t.Fatalf("git status: %v: %s", gerr, out)
		}
		if out != "" {
			t.Errorf("agent worktree is dirty after the ref-only restore:\n%s", out)
		}
	})
	t.Run("the error text does not send the caller to a manual reset --hard", func(t *testing.T) {
		if strings.Contains(err.Error(), "reset --hard") {
			t.Errorf("error still prescribes a manual reset --hard: %v", err)
		}
	})
	t.Run("both pre-merge tips stay ref-reachable", func(t *testing.T) {
		assertPremergePair(t, r, agentOrig, mainBefore)
	})
}

// TestMergeSafety_S5_ValidateFails — the merge lands on main and validation
// then fails, so main is rolled back.
//
// The agent branch is DELIBERATELY left squashed-and-rebased here (unlike
// S2): the merge did happen and was rejected on quality, so the rebased
// branch is a legitimate state to iterate on. That means the agent's
// ORIGINAL commits are reachable from no branch, and the recovery ref is the
// entire safety net — which is exactly what this test pins.
func TestMergeSafety_S5_ValidateFails(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/v")
	r.commitFile(wt, "v-work-1", "v.txt", "1\n")
	agentOrig := r.commitFile(wt, "v-work-2", "v.txt", "1\n2\n")
	r.commitFile(r.root, "main-pre", "other.txt", "m\n")
	mainBefore := r.sha("main")
	reflogBefore := r.reflogEntryCount("main")

	_, err := Merge(context.Background(), r.scenarioCfg("v", "agent/v", wt, "false"), scenarioDeps())

	if err == nil {
		t.Fatal("expected the merge to fail on validation")
	}
	t.Run("main is restored to its exact pre-merge tip", func(t *testing.T) {
		if got := r.sha("main"); got != mainBefore {
			t.Errorf("main = %s, want %s", got[:8], mainBefore[:8])
		}
		if !r.reachableFromBranches(r.commits["main-pre"]) {
			t.Error("main's pre-merge commit became unreachable from all branches")
		}
		if !r.reachableFromParentBranch(r.commits["main-pre"]) {
			t.Error("main's pre-merge commit is no longer reachable from main itself")
		}
	})
	t.Run("main was never mutated at all, not mutated and restored", func(t *testing.T) {
		// The SHA assertion above passes at HEAD: the rollback puts main
		// back. THIS is the one that does not, and it is the difference
		// between "the parent ends up correct" and QUM-1087's actual claim,
		// "the parent is never touched until the tree is green".
		if got := r.reflogEntryCount("main"); got != reflogBefore {
			t.Errorf("main's reflog grew %d -> %d: the parent was moved forward and then rewound, rather than left alone", reflogBefore, got)
		}
	})
	t.Run("the agent branch is NOT restored (deliberate asymmetry with S2)", func(t *testing.T) {
		if got := r.sha("agent/v"); got == agentOrig {
			t.Errorf("agent/v = %s: a validate failure must leave the squashed+rebased branch alone", got[:8])
		}
		if c := r.fileOnRef("agent/v", "v.txt"); c != "1\n2" {
			t.Errorf("agent content on the squash = %q, want %q", c, "1\n2")
		}
	})
	t.Run("the original commits are reachable from NO branch", func(t *testing.T) {
		// Precondition for the next subtest: without this, "reachable from a
		// premerge ref" would prove nothing.
		for _, label := range []string{"v-work-1", "v-work-2"} {
			if r.reachableFromBranches(r.commits[label]) {
				t.Errorf("%s is still branch-reachable; the recovery-ref assertion below would be vacuous", label)
			}
		}
	})
	t.Run("both pre-merge tips stay ref-reachable", func(t *testing.T) {
		assertPremergePair(t, r, agentOrig, mainBefore)
	})
	t.Run("the error names the recovery refs", func(t *testing.T) {
		for _, ref := range r.premergeRefs() {
			if !strings.Contains(err.Error(), ref) {
				t.Errorf("error must name %s, got: %v", ref, err)
			}
		}
	})
}

// TestMergeSafety_PremergeRefCheckerDiscriminatesContents — S0b, the standing
// replacement witness for every assertPremergePair in this file.
//
// S0 only proves reachableFromPremergeRefs returns false when NO premerge
// refs exist; it cannot distinguish "the checker works" from "the checker
// says yes whenever any ref exists". S6's old pin used to supply the missing
// half as a side effect of the engine failing to restore — so QUM-1100 closed
// that window and took the witness with it. This constructs the
// branch-unreachable state DIRECTLY (plant a commit, move the ref off it),
// which no fix to the engine can remove.
func TestMergeSafety_PremergeRefCheckerDiscriminatesContents(t *testing.T) {
	r := newScenarioRepo(t)
	held := r.commitFile(r.root, "held", "held.txt", "h\n")
	wt := r.addWorktree("agent/w")
	orphan := r.commitFile(wt, "orphan", "o.txt", "o\n")
	r.git(wt, "reset", "--hard", "-q", "HEAD~1")

	if r.reachableFromBranches(orphan) {
		t.Fatal("precondition: the planted commit must be branch-unreachable")
	}
	r.git(r.root, "update-ref", PremergeRefPrefix+"/w/20260806T000000.000Z/agent", held)

	if !r.reachableFromPremergeRefs(held) {
		t.Error("checker cannot see a sha that a premerge ref DOES contain")
	}
	if r.reachableFromPremergeRefs(orphan) {
		t.Error("checker reports reachable for a sha NO premerge ref contains — it cannot say no")
	}
}

// TestMergeSafety_HappyPath_NoLoss is the baseline: a clean merge must land
// the agent's content on main without removing anything main already had.
// Its purpose here is to keep the failure-path scenarios above honest — if
// the engine were broken outright, they could all "pass" for the wrong
// reason.
func TestMergeSafety_HappyPath_NoLoss(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/a")
	agentOrig := r.commitFile(wt, "agent-work", "agent.txt", "a\n")
	r.commitFile(r.root, "main-ahead", "mainfile.txt", "m\n")
	mainBefore := r.sha("main")

	res, err := Merge(context.Background(), r.scenarioCfg("a", "agent/a", wt, "true"), scenarioDeps())
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.WasNoOp {
		t.Fatal("precondition: expected a real merge, not a no-op")
	}
	t.Run("main keeps its own commit and gains the agent's content", func(t *testing.T) {
		if !r.reachableFromBranches(r.commits["main-ahead"]) {
			t.Error("main's pre-existing commit became unreachable")
		}
		if c := r.fileOnRef("main", "agent.txt"); c != "a" {
			t.Errorf("agent content on main = %q, want %q", c, "a")
		}
		if c := r.fileOnRef("main", "mainfile.txt"); c != "m" {
			t.Errorf("main's own content = %q, want %q", c, "m")
		}
	})
	t.Run("both pre-merge tips stay ref-reachable even on success", func(t *testing.T) {
		assertPremergePair(t, r, agentOrig, mainBefore)
	})
}

// TestMergeSafety_S3_FFIsAPureRefMoveOfTheAgentsOwnCommits discharges two ACs
// at once, in real git, because both are claims about the repository rather
// than about the call sequence:
//
//   - "step 3 is proven a pure ref move by a direct predicate, not by
//     --ff-only exiting 0"
//   - "the agent's individual commits appear on the parent; no squash commit is
//     created by the engine"
//
// The second is the one with no other coverage anywhere: a content check
// (HappyPath_NoLoss) is satisfied by a squash just as well as by a
// fast-forward, so only counting commits and comparing subjects can tell them
// apart.
func TestMergeSafety_S3_FFIsAPureRefMoveOfTheAgentsOwnCommits(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/ff")
	r.commitFile(wt, "ff-work-1", "f1.txt", "1\n")
	r.commitFile(wt, "ff-work-2", "f2.txt", "2\n")
	agentTip := r.commitFile(wt, "ff-work-3", "f3.txt", "3\n")
	// main moves ahead, so the rebase genuinely rewrites the agent's commits
	// and this is not the trivial already-descended case.
	r.commitFile(r.root, "main-ahead", "m.txt", "m\n")
	mainBefore := r.sha("main")
	reflogBefore := r.reflogEntryCount("main")

	res, err := Merge(context.Background(), r.scenarioCfg("ff", "agent/ff", wt, "true"), scenarioDeps())
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.WasNoOp {
		t.Fatal("precondition: expected a real merge")
	}

	t.Run("the parent tip EQUALS the rebased branch tip", func(t *testing.T) {
		// The discriminating half of the ref-move proof. --ff-only exits 0
		// without moving the parent when already up to date, so its exit
		// status cannot establish this.
		mainAfter := r.sha("main")
		branchAfter := r.sha("agent/ff")
		if mainAfter != branchAfter {
			t.Errorf("main = %s but agent/ff = %s; the ff was not a pure ref move onto the branch", mainAfter[:8], branchAfter[:8])
		}
		if res.MergedTip != mainAfter {
			t.Errorf("Result.MergedTip = %s, want main's actual tip %s", res.MergedTip, mainAfter)
		}
		// And the reported tip must exist on a ref — the old CommitHash named
		// a pre-rebase squash that, after the rebase, existed on none.
		if _, gerr := r.gitOK(r.root, "cat-file", "-e", res.MergedTip+"^{commit}"); gerr != nil {
			t.Errorf("Result.MergedTip %s is not a commit in this repository", res.MergedTip)
		}
		if !r.reachableFromParentBranch(res.MergedTip) {
			t.Errorf("Result.MergedTip %s is not reachable from main: it names a commit on no parent ref", res.MergedTip[:8])
		}
	})
	t.Run("the parent only moved FORWARD", func(t *testing.T) {
		if _, gerr := r.gitOK(r.root, "merge-base", "--is-ancestor", "--", mainBefore, "main"); gerr != nil {
			t.Errorf("main's previous tip %s is no longer an ancestor of main: the parent did not move forward", mainBefore[:8])
		}
		if got := r.reflogEntryCount("main"); got != reflogBefore+1 {
			t.Errorf("main's reflog grew by %d, want exactly 1: the parent must be mutated exactly once", got-reflogBefore)
		}
	})
	t.Run("the agent's OWN commits land, and no squash commit is created", func(t *testing.T) {
		// Three commits in, three commits on. A squash would give 1.
		count := r.git(r.root, "rev-list", "--count", mainBefore+"..main")
		if count != "3" {
			t.Errorf("main gained %s commit(s), want 3 — the engine must fast-forward the agent's own commits, not squash them", count)
		}
		subjects := r.git(r.root, "log", "--format=%s", "--reverse", mainBefore+"..main")
		want := "ff-work-1\nff-work-2\nff-work-3"
		if subjects != want {
			t.Errorf("subjects on main =\n%s\nwant\n%s", subjects, want)
		}
		// No merge commit either: a fast-forward creates none.
		if merges := r.git(r.root, "rev-list", "--merges", mainBefore+"..main"); merges != "" {
			t.Errorf("main gained merge commit(s) %q; a fast-forward creates none", merges)
		}
		// And nothing on main may carry the engine's own provenance trailers,
		// which is what a squash message looked like (QUM-1105, now retired).
		body := r.git(r.root, "log", "--format=%B", mainBefore+"..main")
		for _, trailer := range []string{"Squash-Merge:", "Source-Commit:", "Premerge-Ref:"} {
			if strings.Contains(body, trailer) {
				t.Errorf("a commit on main carries the engine's %s trailer: the engine composed a message, which it must no longer do", trailer)
			}
		}
	})
	t.Run("the agent's original SHAs are gone but its content is intact", func(t *testing.T) {
		// The rebase rewrote them, so the ORIGINAL tip is branch-unreachable —
		// which is exactly what the premerge /agent ref is for.
		if r.reachableFromBranches(agentTip) {
			t.Errorf("the pre-rebase tip %s is still branch-reachable; the rebase did not rewrite it and this test is not exercising a rebase", agentTip[:8])
		}
		for f, want := range map[string]string{"f1.txt": "1", "f2.txt": "2", "f3.txt": "3", "m.txt": "m"} {
			if got := r.fileOnRef("main", f); got != want {
				t.Errorf("main:%s = %q, want %q", f, got, want)
			}
		}
	})
	t.Run("both pre-merge tips stay ref-reachable", func(t *testing.T) {
		assertPremergePair(t, r, agentTip, mainBefore)
	})
}

// TestMergeSafety_S5c_ParentMovesDuringValidation is the second confirmed loss
// mode, made DETERMINISTIC.
//
// The original audit treated S5c as a wall-clock race between two merges and
// did not port it, because a race would flake inside `make validate`. It does
// not have to be a race: the validate command is an arbitrary shell command run
// by the engine, so a validate command that itself commits to the parent
// produces the same interleaving every time — a second merge landing while the
// first is validating.
//
// This scenario is only REACHABLE because validation now precedes the
// fast-forward. Under the old order the ff had already happened before validate
// ran, and the rollback then removed the interloper's commit while leaving its
// own — both results wrong, both messages lying.
func TestMergeSafety_S5c_ParentMovesDuringValidation(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/race")
	agentOrig := r.commitFile(wt, "race-work", "race.txt", "r\n")
	mainBefore := r.sha("main")

	// The validate command lands an unrelated commit ON THE PARENT, then
	// succeeds. Deterministic, and a faithful model of another merge finishing
	// first: git's own index/HEAD are the only shared state involved.
	//
	// `git -C <parent>` IS LOAD-BEARING, and getting it wrong is instructive.
	// Written without it, the command inherits the validate command's working
	// directory — which since QUM-1087 is the AGENT worktree, not the parent —
	// so it commits to the AGENT BRANCH and this scenario silently becomes a
	// different one. It also then PASSES the "interloper survives" subtest for
	// entirely the wrong reason, because the agent's commit reaches main by
	// fast-forward. That variant turned out to be worth keeping on its own
	// terms: see TestMergeSafety_AgentBranchMovesDuringValidation below.
	validateCmd := "git -C " + r.root + " -c user.name=t -c user.email=t@x commit -q --allow-empty -m interloper"

	_, err := Merge(context.Background(), r.scenarioCfg("race", "agent/race", wt, validateCmd), scenarioDeps())

	if err == nil {
		t.Fatal("expected the merge to fail: the parent moved while validation was running, so the fast-forward is no longer possible")
	}
	t.Run("the interloper's commit survives", func(t *testing.T) {
		// The old rollback deleted exactly this. It is another merge's work.
		subjects := r.git(r.root, "log", "--format=%s", "-1", "main")
		if subjects != "interloper" {
			t.Errorf("main's tip subject = %q, want \"interloper\": the merge destroyed work that landed during its validation", subjects)
		}
		if !r.reachableFromParentBranch(mainBefore) {
			t.Errorf("main's pre-merge tip %s is no longer reachable from main", mainBefore[:8])
		}
	})
	t.Run("none of the agent's work landed", func(t *testing.T) {
		if c := r.fileOnRef("main", "race.txt"); c != "" {
			t.Errorf("agent content reached main (%q) despite the merge failing", c)
		}
	})
	t.Run("the failure names both parent SHAs and diagnoses the race", func(t *testing.T) {
		mainAfter := r.sha("main")
		if !strings.Contains(err.Error(), mainBefore) {
			t.Errorf("error must name the parent tip as of validation start (%s): %v", mainBefore[:8], err)
		}
		if !strings.Contains(err.Error(), mainAfter) {
			t.Errorf("error must name the parent's current tip (%s): %v", mainAfter[:8], err)
		}
		if !strings.Contains(err.Error(), parentMovedFailure) {
			t.Errorf("error must diagnose that the parent moved during validation, got: %v", err)
		}
		// Path-unique in the other direction. The phrase can only arrive here
		// from ffMergeFailureError's `default` leg (the ff PRECONDITION reads
		// the parent tip before validation, so it passes in this shape), and
		// that leg does NOT claim the rebase is at fault — it names two
		// candidates and defers to git's message. What is wrong with it here
		// is its premise: it reports that nothing moved underneath this merge,
		// which is false, and hands the caller two candidate causes, neither
		// of which is the real one, while suppressing the true remedy
		// (re-rebase onto the new tip and re-run).
		if strings.Contains(err.Error(), ffPredicateFailure) {
			t.Errorf("error took the parent-did-not-move leg: it reports that nothing moved underneath this merge — false, the parent moved — and offers two candidate causes, neither of which is the real one, while suppressing the true remedy (re-rebase and re-run): %v", err)
		}
	})
	t.Run("both pre-merge tips stay ref-reachable", func(t *testing.T) {
		assertPremergePair(t, r, agentOrig, mainBefore)
	})
}

// TestMergeSafety_AgentBranchMovesDuringValidation pins the property that makes
// "the parent receives exactly the tree that passed validation" true BY
// CONSTRUCTION rather than by detection.
//
// The window is real, not contrived. The per-agent flock has no second taker
// anywhere in the tree — nothing on the agent side acquires it — so a live agent
// CAN commit while its own merge is validating, and since QUM-1087 validation
// runs in that agent's worktree for as long as a validate takes.
//
// The first cut of QUM-1087 fast-forwarded the branch NAME. git resolves a name
// at ff time, so the parent advanced onto the agent's NEWER tip and the engine
// only noticed afterwards, in the post-ff equality check — having already
// mutated the parent with a tree nothing validated. The fix is to fast-forward
// the validated SHA. Then the parent cannot receive the unvalidated commit at
// all, and the merge SUCCEEDS, which is correct: the engine merged exactly what
// it verified and the agent's later commit is simply not part of this merge.
//
// This test was originally written asserting the merge FAILED with a loud
// "unverified state" message. That was pinning the detection of a hazard the
// design can avoid — kept as a note because it is the CLAUDE.md lesson again:
// the old assertion passed, red-first and all, while describing worse behaviour
// than the code should have.
func TestMergeSafety_AgentBranchMovesDuringValidation(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/moving")
	agentOrig := r.commitFile(wt, "moving-work", "mv.txt", "1\n")
	r.commitFile(r.root, "main-ahead", "m.txt", "m\n")
	mainBefore := r.sha("main")

	// No -C: this commits in the validate command's cwd, which IS the agent
	// worktree. That models the agent committing during its own merge.
	validateCmd := "git -c user.name=t -c user.email=t@x commit -q --allow-empty -m snuck-in"

	res, err := Merge(context.Background(), r.scenarioCfg("moving", "agent/moving", wt, validateCmd), scenarioDeps())

	// READ THE REPOSITORY BEFORE DECIDING WHAT err MEANS.
	//
	// This ordering is awkward and deliberate. Under the branch-name-ff
	// regression this test DID go red — but it died at the `err != nil` Fatalf
	// below, so no subtest ran and the whole reported failure was "the merge
	// should SUCCEED". Meanwhile main had actually received `snuck-in`: the
	// harm was real, present, and unnamed. A failure message that describes our
	// expectation instead of the damage sends the reader to the wrong question.

	// PREMISE first: without a branch that actually moved during validation,
	// nothing below is about the window. Asserted via the tip SUBJECT, not
	// against res.MergedTip, because res is nil on the failing path — the
	// premise has to survive exactly the case this reordering exists for.
	if subj := r.git(r.root, "log", "--format=%s", "-1", "agent/moving"); subj != "snuck-in" {
		// err is reported too: a regression that makes Merge fail BEFORE
		// validation runs also leaves the branch unmoved, and without err this
		// premise would blame the fixture for a real engine failure — the same
		// reported-the-wrong-question mistake this reordering exists to fix,
		// just pointing the other way.
		t.Fatalf("agent/moving tip subject = %q, want \"snuck-in\": the validate command did not commit on the branch, so this configuration does not exercise the window at all (Merge returned err=%v)", subj, err)
	}

	// THE HARM, checked and reported before err is interpreted. t.Errorf, not
	// Fatalf, so it is recorded even though the Fatalf below then aborts.
	if subjects := r.git(r.root, "log", "--format=%s", mainBefore+"..main"); strings.Contains(subjects, "snuck-in") {
		t.Errorf("main RECEIVED the commit made DURING validation — the parent now carries a tree nothing validated:\n%s\n"+
			"The fast-forward must target the validated SHA, not the branch name.", subjects)
	}

	if err != nil {
		t.Fatalf("the merge should SUCCEED, landing exactly the validated tip: %v", err)
	}

	t.Run("the validated work still landed on the parent", func(t *testing.T) {
		// The snuck-in half of this subtest was hoisted above the err check on
		// purpose — see the comment there. Keeping a copy here would report the
		// same harm twice on the one path where it fires.
		if c := r.fileOnRef("main", "mv.txt"); c != "1" {
			t.Errorf("main:mv.txt = %q, want %q — the validated work must still land", c, "1")
		}
	})
	t.Run("PREMISE: the agent branch moved past the validated tip", func(t *testing.T) {
		// A premise, not a result — kept as its own subtest rather than filed
		// under a landing assertion it is not about. The subject-level premise
		// above the err check is the one that must survive a failing merge;
		// this is its res-dependent other half.
		if got := r.sha("agent/moving"); got == res.MergedTip {
			t.Errorf("agent/moving = %s equals the merged tip; the validate command did not move the branch past it, so this configuration does not exercise the window", got[:8])
		}
	})
	t.Run("the parent is at exactly the tip that was validated", func(t *testing.T) {
		if got := r.sha("main"); got != res.MergedTip {
			t.Errorf("main = %s, want the reported merged tip %s", got[:8], res.MergedTip[:8])
		}
		// And the agent branch is legitimately AHEAD of the parent now, which
		// is an ordinary state, not an error.
		if _, aerr := r.gitOK(r.root, "merge-base", "--is-ancestor", "--", "main", "agent/moving"); aerr != nil {
			t.Errorf("main should be an ancestor of agent/moving after this merge")
		}
	})
	t.Run("both pre-merge tips stay ref-reachable", func(t *testing.T) {
		assertPremergePair(t, r, agentOrig, mainBefore)
	})
}

// TestMergeSafety_MergeInvokesNoCommitHook asserts the property that deletes the
// whole S6 failure class: **a merge never creates a commit, so it can never run
// the pre-commit hook.**
//
// Why this is worth its own test rather than being left as a consequence. S6
// fired TWICE in production on 2026-08-06, on unrelated content (once code-only,
// once a docs-only branch with a single new markdown file) and from two
// different triggers (a hook failure, and a flaky 600ms wall-clock budget that
// blew to 1.59s under ~10-agent load). Each time, `git commit` for the squash
// exited non-zero, the engine's `reset --soft` was left standing, and the
// agent's committed work ended up staged-but-uncommitted with the branch rewound
// to the parent tip — reported afterwards as "agent has uncommitted changes",
// which is true and misdescribes the cause. The trigger was incidental; the
// defect was that a ROUTINE non-zero `git commit` destroyed committed work, and
// any hook failure produces it. No commit, no hook, no class.
//
// It is asserted OUT-OF-SEAM, and that is the point. A Deps-level check ("we did
// not call GitCommit") tests our own bookkeeping and is satisfied by any new
// commit path we did not think of. A hook that writes a sentinel file observes
// the USER-VISIBLE property — git itself did not run my hook — and those two
// come apart exactly when someone adds a commit through an unexpected route.
func TestMergeSafety_MergeInvokesNoCommitHook(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/hook")
	// TWO agent commits, and the arity is load-bearing. With one, a squash —
	// the actual S6 shape — collapses it to one and the commit-count assertion
	// below cannot move, so `reset --soft main && git commit --no-verify` made
	// the ENTIRE test pass: no hook fired (--no-verify), the count was
	// unchanged (1 → 1), and the premerge pair is untouched by a squash. The
	// assertion whose stated job is closing the --no-verify gap was blind to it
	// BY FIXTURE ARITY. Two commits make a squash observable as 2 → 1.
	agentOrig := r.commitFile(wt, "hook-work", "h.txt", "1\n")
	r.commitFile(wt, "hook-work-2", "h2.txt", "2\n")
	r.commitFile(r.root, "main-ahead", "m.txt", "m\n")

	// A real pre-commit hook in the shared hooks dir, so it is installed for the
	// repo and every worktree of it — the same mechanism this repo uses.
	hooksDir := strings.TrimSpace(r.git(r.root, "rev-parse", "--git-common-dir"))
	if !filepath.IsAbs(hooksDir) {
		hooksDir = filepath.Join(r.root, hooksDir)
	}
	hooksDir = filepath.Join(hooksDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	// Pin core.hooksPath in the REPO's own config, because the control and the
	// subject do not run git the same way: r.gitOK sanitises the environment
	// (GIT_CONFIG_GLOBAL=/dev/null), while the merge engine shells out with the
	// ambient one. An ambient global core.hooksPath would therefore send the
	// SUBJECT's git somewhere else while leaving the control firing happily —
	// the test would pass with its own detector disabled. Repo-local config
	// beats global, so this makes the two agree.
	r.git(r.root, "config", "core.hooksPath", hooksDir)
	sentinel := filepath.Join(r.root, "PRE-COMMIT-HOOK-RAN")
	hook := "#!/bin/sh\necho fired >> " + sentinel + "\n"
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte(hook), 0o700); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	// POSITIVE CONTROL, first and non-negotiable: prove the hook actually fires
	// in this fixture. Without it, "the hook did not fire" is satisfied by a
	// hook that could never fire, and the whole test is vacuous.
	r.commitFile(r.root, "control-commit", "control.txt", "c\n")
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("positive control: the pre-commit hook did not fire for an ordinary commit, so this fixture cannot detect one (%v)", err)
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatalf("removing sentinel between control and subject: %v", err)
	}
	// Declared HERE, after the control commit moved main, so there is no
	// earlier value to be misread as the baseline.
	mainBefore := r.sha("main")

	res, err := Merge(context.Background(), r.scenarioCfg("hook", "agent/hook", wt, "true"), scenarioDeps())
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.WasNoOp {
		t.Fatal("precondition: expected a real merge")
	}

	t.Run("the pre-commit hook never ran", func(t *testing.T) {
		// Named for what it OBSERVES, not for the stronger property it implies.
		// A commit made with --no-verify creates a commit and fires no hook, so
		// "no commit was created" would be claiming more than this reads. The
		// commit-count half of the claim is asserted in the next subtest.
		data, readErr := os.ReadFile(sentinel)
		switch {
		case errors.Is(readErr, fs.ErrNotExist):
			// The pass: no sentinel, so git ran no pre-commit hook.
		case readErr != nil:
			// Any OTHER read error must not take the pass arm silently.
			t.Fatalf("reading the sentinel: %v — the hook assertion could not be evaluated, which is not a pass", readErr)
		default:
			t.Errorf("the pre-commit hook FIRED during a merge (sentinel: %q).\n"+
				"The merge must create no commit: a non-zero `git commit` here is routine (it runs the\n"+
				"project's whole validate gate) and it destroyed committed work twice in production on\n"+
				"2026-08-06. If a commit has been reintroduced into the merge path, that entire class is back.",
				strings.TrimSpace(string(data)))
		}
	})
	t.Run("and the merge still landed the agent's work, creating no commit of its own", func(t *testing.T) {
		// So the subtest above cannot pass by the merge having done nothing.
		if c := r.fileOnRef("main", "h.txt"); c != "1" {
			t.Errorf("main:h.txt = %q, want %q — the merge did not land, so 'no hook fired' proves nothing", c, "1")
		}
		if got := r.sha("main"); got == mainBefore {
			t.Errorf("main did not move (%s); the merge was a no-op and the hook assertion is vacuous", got[:8])
		}
		// Closes the --no-verify gap the subtest above cannot see, in BOTH
		// directions: the agent contributed exactly two commits, so a count of
		// 3 means the engine added one and a count of 1 means it squashed them.
		// The squash direction is why the fixture makes two commits rather than
		// one — see the comment at the top of this test.
		if n := strings.TrimSpace(r.git(r.root, "rev-list", "--count", mainBefore+"..main")); n != "2" {
			t.Errorf("main gained %s commit(s), want exactly the agent's 2 — the merge created or squashed commits of its own", n)
		}
	})
	t.Run("both pre-merge tips stay ref-reachable", func(t *testing.T) {
		assertPremergePair(t, r, agentOrig, mainBefore)
	})
}

// concurrentMergeFailsafe bounds the two channel waits in the concurrent
// scenario. IT IS NOT SYNCHRONISATION and no assertion depends on its value:
// the correct path never consults the clock, and the interleaving is scripted
// by channels. It exists so a WIRING BUG (the seam never reached, the goroutine
// never returning) reports "the loser never reached validate" in seconds
// instead of burning `go test`'s panic budget inside `make validate`.
const concurrentMergeFailsafe = 2 * time.Minute

// TestMergeSafety_ConcurrentMergesOfDifferentAgents_NoLoss is the S5c shape as
// the original audit posed it: TWO REAL MERGES, of two DIFFERENT AGENTS,
// overlapping on one SprawlRoot. The existing S5c models the interleaving with
// a validate command that commits to the parent; this one uses the actual
// window, because the actual window is what QUM-1089 proposes to close.
//
// WHY THE WINDOW IS REAL. The engine's only intrinsic lock is per-agent —
// merge.go keys it on cfg.AgentName — so two agents take two different flocks
// and do not serialise at all. supervisor's mergeSem is a field on Real and is
// not reachable from merge.Merge; the separate-process `sprawl merge` CLI
// bypasses it regardless. This is the tree's behaviour, not a contrivance.
//
// WHAT IT ESTABLISHES, and why it matters beyond itself: post-QUM-1087 the
// interleaving loses NO COMMITTED WORK. One merge fast-forwards and wins; the
// other's --ff-only REFUSES because the parent moved, and its branch is left
// rebased and intact. That is WASTED work, not LOST work — which is the entire
// argument for downgrading QUM-1089 from a safety issue to an efficiency one.
// QUM-1089 AC #5 points back here: serialisation must not be achieved by
// weakening the --ff-only refusal that makes this race safe.
//
// SCRIPTED, NOT RACED. There is no sleep and no wall-clock ordering anywhere on
// the correct path; the loser's validate is an injected Deps.RunTestsStreaming
// that blocks on a channel until the winner has completely finished, so the
// interleaving is identical on every run and under -race. A flaky test in this
// file would get quarantined and then trusted, which is a worse end state than
// no test.
//
// A consequence worth stating, because it is what makes this admissible inside
// `make validate`: NO TWO GIT PROCESSES EVER RUN AT THE SAME TIME. Every git
// invocation the loser makes happens before it signals; the winner runs
// entirely while the loser is parked in the seam holding no git process; the
// loser's ff attempt happens while the test goroutine is blocked on the result.
// There is therefore no .git/index.lock or packed-refs contention to flake on.
// The merges are concurrent in the sense that matters — the loser holds its
// lock and is committed to a rebased tip across the winner's whole lifetime —
// without being concurrent at the syscall level.
func TestMergeSafety_ConcurrentMergesOfDifferentAgents_NoLoss(t *testing.T) {
	r := newScenarioRepo(t)
	loserWT := r.addWorktree("agent/loser")
	winnerWT := r.addWorktree("agent/winner")
	loserOrig := r.commitFile(loserWT, "loser-work", "loser.txt", "L\n")
	winnerOrig := r.commitFile(winnerWT, "winner-work", "winner.txt", "W\n")
	// main moves ahead of BOTH, so each merge performs a genuine rebase rather
	// than the trivial already-descended case, and both original tips become
	// branch-unreachable — which is what makes the recovery-ref subtest below
	// non-vacuous. Disjoint files, so neither rebase conflicts.
	r.commitFile(r.root, "main-ahead", "m.txt", "m\n")
	mainBefore := r.sha("main")
	reflogBefore := r.reflogEntryCount("main")

	// Non-empty, so cfg.NoValidate is false and the validate seam is reached at
	// all (scenarioCfg derives NoValidate from emptiness). Never executed — the
	// seam is replaced — and the premise below asserts the seam saw exactly this
	// string, so a future change that drops the injection cannot pass silently.
	const loserValidateCmd = "echo REPLACED-BY-THE-INJECTED-SEAM-NEVER-EXECUTED"

	root := r.root // captured by value: the seam closure must not close over r.

	type loserObs struct {
		dir, command string
		branchTip    string
		parentTip    string
		ffableNow    bool
		gitErr       error
	}
	observed := make(chan loserObs, 1)

	winnerDone := make(chan struct{})
	var releaseOnce sync.Once
	releaseWinner := func() { releaseOnce.Do(func() { close(winnerDone) }) }
	type mergeOutcome struct {
		res *Result
		err error
	}
	loserFinished := make(chan mergeOutcome, 1)

	// Unblocks the loser goroutine on EVERY exit path, including a t.Fatalf
	// above the normal release, and then JOINS it. Without the release a failed
	// premise leaks a goroutine parked forever on winnerDone; without the join
	// the test returns while that goroutine is still running git inside
	// t.TempDir(), which cleanup is concurrently removing.
	//
	// Honest note: I predicted that unjoined shape would produce "TempDir
	// RemoveAll cleanup: directory not empty" and it did NOT reproduce in 13
	// runs. It is closed anyway because it is nearly free and the failure it
	// would cause is a confusing one to debug, not because it was observed.
	// loserJoined guards against the deferred join waiting on a channel the
	// happy path has ALREADY drained — which would block for the full failsafe
	// and turn a 0.15s test into a 120s one. Read and written only on the test
	// goroutine (the defer runs there too), so no synchronisation is needed.
	loserJoined := false
	defer func() {
		releaseWinner()
		if loserJoined {
			return
		}
		select {
		case <-loserFinished:
		case <-time.After(concurrentMergeFailsafe):
		}
	}()

	loserDeps := scenarioDeps()
	loserDeps.RunTestsStreaming = func(_ context.Context, dir, command string, _ func(string)) (string, error) {
		// RUNS ON THE LOSER'S GOROUTINE. Nothing here may touch r.t or
		// r.commits — see scenarioGit's comment. Every observation is taken by
		// value and carried back over a channel rather than shared.
		obs := loserObs{dir: dir, command: command}
		obs.branchTip, obs.gitErr = scenarioGit(root, "rev-parse", "refs/heads/agent/loser")
		if obs.gitErr == nil {
			obs.parentTip, obs.gitErr = scenarioGit(root, "rev-parse", "main")
		}
		// Is the loser fast-forwardable RIGHT NOW? Its "yes" here, paired with
		// the refusal after the winner lands, is what proves the window was
		// OPEN and then CLOSED — rather than never having been open at all.
		_, ffErr := scenarioGit(root, "merge-base", "--is-ancestor", "--", "main", "refs/heads/agent/loser")
		obs.ffableNow = ffErr == nil
		// THE SEND IS THE SIGNAL. One channel carries both "I have rebased and
		// am inside validate" and the evidence for it, so the gate and the
		// premise cannot come apart: there is no ordering in which the test
		// proceeds without the evidence that it proceeded correctly.
		observed <- obs
		<-winnerDone
		return "", nil
	}

	// Both Configs are built HERE, on the test goroutine, and only the values
	// are handed to the goroutines. r.scenarioCfg touches only r.root today, so
	// calling it from a non-test goroutine would be safe by accident — but that
	// contradicts the invariant scenarioGit's comment states, and the most
	// natural future edit to a helper in this file (adding r.t.Helper()) would
	// silently break it.
	loserCfg := r.scenarioCfg("loser", "agent/loser", loserWT, loserValidateCmd)
	winnerCfg := r.scenarioCfg("winner", "agent/winner", winnerWT, "true")

	go func() {
		res, err := Merge(context.Background(), loserCfg, loserDeps)
		loserFinished <- mergeOutcome{res: res, err: err}
	}()

	var obs loserObs
	select {
	case obs = <-observed:
	case <-time.After(concurrentMergeFailsafe):
		t.Fatal("the loser merge never reached its validate seam, so the concurrent window was never opened; this is a wiring failure, not a slow machine")
	}

	// ---- PREMISE: the window is genuinely open. ----
	// Every assertion after the winner runs is ALSO satisfied by a loser that
	// never started, or that failed before validating. Establish otherwise
	// FIRST — the absence of loss means nothing unless the window was open.
	if obs.gitErr != nil {
		t.Fatalf("premise reads inside the loser's validate seam failed: %v", obs.gitErr)
	}
	if obs.dir != loserWT {
		t.Errorf("validate ran in %q, want the AGENT worktree %q (QUM-1087)", obs.dir, loserWT)
	}
	if obs.command != loserValidateCmd {
		t.Errorf("the seam saw command %q, want %q — this is not the merge's real validate call site", obs.command, loserValidateCmd)
	}
	if obs.branchTip == loserOrig {
		t.Fatalf("agent/loser is still at its pre-merge tip %s inside validate: the loser had NOT rebased, so it is not in the window this test is about", loserOrig[:8])
	}
	if !obs.ffableNow {
		t.Fatalf("agent/loser was NOT fast-forwardable onto main at the moment it entered validate: its later refusal would not be attributable to the winner")
	}
	if obs.parentTip != mainBefore {
		t.Fatalf("main was already at %s when the loser entered validate, want the pre-merge tip %s: the winner landed BEFORE the window opened and the merges did not overlap", obs.parentTip[:8], mainBefore[:8])
	}
	// The lock premise, PROBED rather than inferred. An os.Stat on the lock file
	// proves nothing: RealLockAcquire creates it with O_CREATE and never removes
	// it, and unlock is only LOCK_UN + Close, so the path exists whether or not
	// anything holds the lock. Deleting the syscall.Flock call from
	// RealLockAcquire entirely left the file-existence version of this test
	// fully green — so the header's central claim, that the loser HOLDS its lock
	// across the winner's whole lifetime, was the one premise component asserted
	// by name and unverified in fact.
	//
	// flock(2) is per OPEN FILE DESCRIPTION, not per process, so a second open
	// of the same path from this goroutine genuinely contends with the loser's.
	loserLockPath := filepath.Join(root, ".sprawl", "locks", "loser.lock")
	probeFD, probeErr := syscall.Open(loserLockPath, syscall.O_RDWR, 0)
	if probeErr != nil {
		t.Errorf("opening the loser's lock file %s to probe it: %v", loserLockPath, probeErr)
	} else {
		flockErr := syscall.Flock(probeFD, syscall.LOCK_EX|syscall.LOCK_NB)
		if flockErr == nil {
			_ = syscall.Flock(probeFD, syscall.LOCK_UN)
			t.Errorf("the loser's flock at %s was ACQUIRABLE while it is mid-merge: it does not hold its lock, so the winner is not running concurrently with a lock-holding merge and this test's central premise is false", loserLockPath)
		} else if !errors.Is(flockErr, syscall.EWOULDBLOCK) {
			t.Errorf("probing the loser's flock returned %v, want EWOULDBLOCK (held)", flockErr)
		}
		_ = syscall.Close(probeFD)
	}

	// ---- The winner runs to completion WHILE the loser holds its lock. ----
	// That this returns at all is the demonstration that per-agent locks do not
	// serialise different agents: were the lock shared, this call would deadlock.
	//
	// It runs on ITS OWN GOROUTINE, bounded by the failsafe, and that is not
	// ceremony: run inline, a SHARED merge lock (exactly what QUM-1089 proposes)
	// blocks this call forever — the loser waits on winnerDone, and the deferred
	// releaseWinner never fires because the test goroutine is BLOCKED, not
	// EXITED. Measured: `panic: test timed out`, which under `make validate` is
	// the 10-minute package timeout and aborts the whole internal/merge binary,
	// losing every other test's result with it. The single most likely future
	// change to this area is the one the inline form could not report.
	winnerFinished := make(chan mergeOutcome, 1)
	go func() {
		res, err := Merge(context.Background(), winnerCfg, scenarioDeps())
		winnerFinished <- mergeOutcome{res: res, err: err}
	}()
	var win mergeOutcome
	select {
	case win = <-winnerFinished:
	case <-time.After(concurrentMergeFailsafe):
		t.Fatal("the winning merge blocked while the loser held its lock: merges are now SERIALISED, so this test's premise (two agents do not contend) no longer holds. If you are implementing QUM-1089's per-root lock, this test must be rewritten, not deleted — its assertions are what stop serialisation being achieved by weakening the --ff-only refusal.")
	}
	winRes, winErr := win.res, win.err
	if winErr != nil {
		t.Fatalf("the winning merge must succeed — the parent has not moved and its rebase is clean: %v", winErr)
	}
	if winRes.WasNoOp {
		t.Fatal("precondition: the winner must be a real merge, not a no-op")
	}
	// Captured on the test goroutine, strictly before the loser is released, so
	// "the parent is unmoved BY THE LOSER" compares against a value the loser
	// demonstrably could not have influenced.
	mainAfterWinner := r.sha("main")
	if mainAfterWinner == mainBefore {
		t.Fatal("precondition: main did not move for the winner, so there is nothing for the loser's ff to be refused against")
	}

	releaseWinner()
	var lose mergeOutcome
	select {
	case lose = <-loserFinished:
		loserJoined = true
	case <-time.After(concurrentMergeFailsafe):
		t.Fatal("the loser merge never returned after the winner finished")
	}

	// ---- READ THE REPOSITORY AND REPORT HARM BEFORE INTERPRETING err. ----
	// Same ordering, and the same reason, as
	// TestMergeSafety_AgentBranchMovesDuringValidation: a regression that makes
	// the loser SUCCEED (an --ff-only weakened into a force, which is exactly
	// what QUM-1089 AC #5 forbids) would otherwise die at "expected the loser to
	// fail" while main had actually lost the winner's commit — a report naming
	// our expectation instead of the damage. t.Errorf, not Fatalf.
	if got := r.sha("main"); got != mainAfterWinner {
		t.Errorf("main = %s but was %s when the winner finished: THE LOSING MERGE MUTATED THE PARENT. The loser must refuse and touch nothing.", got[:8], mainAfterWinner[:8])
	}
	if !r.reachableFromParentBranch(winRes.MergedTip) {
		t.Errorf("the WINNER's merged tip %s is no longer reachable from main: the losing merge destroyed another merge's landed work — this is the S5c loss, live.", winRes.MergedTip[:8])
	}
	if c := r.fileOnRef("main", "winner.txt"); c != "W" {
		t.Errorf("main:winner.txt = %q, want %q — the winner's content is gone from the parent", c, "W")
	}

	if lose.err == nil {
		t.Fatalf("the losing merge must FAIL: main moved to %s during its validation, so its --ff-only cannot be legal. It returned success (%+v).", mainAfterWinner[:8], lose.res)
	}

	t.Run("the loser's --ff-only failed loudly, diagnosed as a moved parent", func(t *testing.T) {
		// ARRIVAL. Every assertion here is satisfied by any early refusal: a
		// loser that bailed at step 2 would leave main pristine and pass them
		// all without ever reaching the ff. Pin the path.
		if !strings.Contains(lose.err.Error(), parentMovedFailure) {
			t.Fatalf("the loser did not fail at the fast-forward with a moved-parent diagnosis, so the concurrent shape was never exercised: %v", lose.err)
		}
		// The phrase can only arrive here from ffMergeFailureError's `default`
		// leg, which does NOT assert the rebase — it names two candidates and
		// lets git's message discriminate. The defect it would signal is its
		// premise, and under M1 (the loser rewinds main) its closing line is
		// flatly false about the exact damage just done.
		if strings.Contains(lose.err.Error(), ffPredicateFailure) {
			t.Errorf("the loser's failure took the parent-did-not-move leg: it reports that nothing moved underneath this merge and that the parent was not modified — both false — and hands the caller two candidate causes, neither of which is the real one, while suppressing the true remedy (re-rebase and re-run): %v", lose.err)
		}
		for _, want := range []string{mainBefore, mainAfterWinner} {
			if !strings.Contains(lose.err.Error(), want) {
				t.Errorf("the error must name both parent SHAs so the caller can see what moved; missing %s: %v", want[:8], lose.err)
			}
		}
	})
	t.Run("the parent was mutated exactly once, by the winner", func(t *testing.T) {
		// The assertion a mutate-then-restore cannot satisfy. SHA equality above
		// passes for a parent that was rewound and put back; only the mutation
		// COUNT distinguishes "the loser left it alone" from "the loser moved it
		// and moved it back", and QUM-1087's claim is the former.
		if got := r.reflogEntryCount("main"); got != reflogBefore+1 {
			t.Errorf("main's reflog grew by %d, want exactly 1 (the winner's fast-forward): the losing merge mutated the parent", got-reflogBefore)
		}
	})
	t.Run("WASTED work, not LOST work: the loser's branch is intact", func(t *testing.T) {
		// The claim is about the WORK, not about the error. An error message is
		// satisfied by a branch that was destroyed on the way to producing it.
		//
		// THE TIP CHECK IS THE ONE CARRYING THE CLAIM, and it is here because
		// the other three cannot detect the loss they are named for. Restoring
		// the loser's branch off its rebase (back to preRebaseSHA) on the
		// ff-refusal path leaves all three green: the content is identical
		// either way; `rev-list --count mainBefore..agent/loser` is 1 for BOTH
		// tips, since loserOrig is base+loser-work and mainBefore is
		// base+main-ahead; and main is unaffected. Measured — the only red was
		// in a DIFFERENT subtest (the recovery-ref non-vacuity guard), pointing
		// at vacuity rather than at the rewound branch.
		if got := r.sha("agent/loser"); got != obs.branchTip {
			t.Errorf("agent/loser = %s, want the rebased tip %s the merge left it at: the refused merge REWOUND the branch off its rebase, so the work is not where the caller was told it is", got[:8], obs.branchTip[:8])
		}
		if c := r.fileOnRef("agent/loser", "loser.txt"); c != "L" {
			t.Errorf("agent/loser:loser.txt = %q, want %q — the refused merge damaged the loser's content", c, "L")
		}
		// Its commit is still its own, on its own branch, ready to re-rebase.
		if n := strings.TrimSpace(r.git(r.root, "rev-list", "--count", mainBefore+"..refs/heads/agent/loser")); n != "1" {
			t.Errorf("agent/loser carries %s commit(s) past the old main tip, want 1: the refused merge did not leave the branch re-runnable", n)
		}
		// And the loser is legitimately behind: nothing of its work reached main.
		if c := r.fileOnRef("main", "loser.txt"); c != "" {
			t.Errorf("the loser's content reached main (%q) despite the merge failing", c)
		}
	})
	t.Run("both merges wrote their own recovery pair; neither clobbered the other's", func(t *testing.T) {
		if all := r.premergeRefs(); len(all) != 4 {
			t.Fatalf("want 4 premerge refs (2 merges x 2), got %d: %v", len(all), all)
		}
		// Non-vacuity: both original tips were rewritten by their rebases, so
		// the premerge refs are their ONLY holders.
		for name, sha := range map[string]string{"loser": loserOrig, "winner": winnerOrig} {
			if r.reachableFromBranches(sha) {
				t.Errorf("%s's pre-rebase tip %s is still branch-reachable; the recovery-ref assertions below would be vacuous", name, sha[:8])
			}
		}
		for _, tc := range []struct{ agent, sha, what string }{
			{"loser", loserOrig, "the loser's pre-rebase tip"},
			{"winner", winnerOrig, "the winner's pre-rebase tip"},
			{"loser", mainBefore, "the parent tip the loser saw"},
			{"winner", mainBefore, "the parent tip the winner saw"},
		} {
			if pair := r.premergeRefsForAgent(tc.agent); len(pair) != 2 {
				t.Errorf("agent %q: want 2 premerge refs, got %d: %v", tc.agent, len(pair), pair)
			}
			// Scoped to the agent's OWN refs: unscoped, the winner's /parent ref
			// would vouch for the loser's parent tip and this would pass with an
			// entire merge's refs missing.
			if !r.reachableFromPremergeRefsOfAgent(tc.sha, tc.agent) {
				t.Errorf("%s (%s) is not reachable from %s's own premerge refs", tc.what, tc.sha[:8], tc.agent)
			}
		}
	})
}

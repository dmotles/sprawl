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
// Deliberately NOT ported: the analysis's S5b/S5c (they gate QUM-1087, and
// S5c is a wall-clock race that would flake in validate) and S7 (QUM-1088).
//
// Reflog is deliberately excluded from every reachability check — the
// invariant is about refs, and a reflog entry does not survive `git gc`.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// gitOK runs a git command and returns its combined output and error.
func (r *scenarioRepo) gitOK(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@x",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@x",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
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

// reachableFromPremergeRefs reports whether sha is reachable from any ref
// under refs/sprawl/premerge/.
func (r *scenarioRepo) reachableFromPremergeRefs(sha string) bool {
	out, err := r.gitOK(r.root, "for-each-ref", "--format=%(refname)", "--contains", sha, PremergeRefPrefix)
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

// TestMergeSafety_S6_CommitFailsAfterSoftReset — the squash commit fails
// after `git reset --soft <mergeBase>`.
//
// EMPHASIS DELIBERATELY INVERTED from the research harness, which called this
// a "crash window". `git commit` IS a subprocess: it runs the pre-commit
// hook, which runs `make validate`. A non-zero exit is therefore the ROUTINE
// case, not an exotic one, and the window is as wide as a validate run, on
// every merge. It fired in production on 2026-08-06 — an unrelated test flake
// (QUM-1070) failed the hook and left 3026 insertions across 30 files
// reachable from no ref. The audit's "narrow (no subprocess between the two
// steps)" was false on its own terms.
//
// A true crash (SIGKILL) in the same window is the RARE sibling and is the
// one case this fix cannot cover: no code runs, so the premerge refs written
// at step 3a are the entire net. The injected failing GitCommit models the
// routine case; the on-disk state before the restore is identical either way.
func TestMergeSafety_S6_CommitFailsAfterSoftReset(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/k")
	r.commitFile(wt, "k-work-1", "k.txt", "1\n")
	agentOrig := r.commitFile(wt, "k-work-2", "k.txt", "1\n2\n")
	mainBefore := r.sha("main")

	deps := scenarioDeps()
	deps.GitCommit = func(_, _ string) (string, error) {
		return "", fmt.Errorf("git commit: exit status 1: pre-commit hook rejected the squash")
	}
	_, err := Merge(context.Background(), r.scenarioCfg("k", "agent/k", wt, ""), deps)

	if err == nil {
		t.Fatal("expected the failed squash commit to fail the merge")
	}
	t.Run("main is untouched", func(t *testing.T) {
		if got := r.sha("main"); got != mainBefore {
			t.Errorf("main = %s, want unchanged %s", got[:8], mainBefore[:8])
		}
	})
	// QUM-1100 inverts what this asserted before. The old subtest ("the
	// window is real: work is branch-unreachable and only staged") REQUIRED
	// the branch to sit at the merge base — it encoded the defect as the
	// expected outcome, and it was green while the defect was live in
	// production. It asserted the window opens and never that anything
	// closes it.
	t.Run("the agent branch is restored to its pre-merge tip", func(t *testing.T) {
		if got := r.sha("agent/k"); got != agentOrig {
			t.Errorf("agent/k = %s, want its original tip %s — the engine must undo its own reset --soft", got[:8], agentOrig[:8])
		}
		for _, label := range []string{"k-work-1", "k-work-2"} {
			if !r.reachableFromBranches(r.commits[label]) {
				t.Errorf("%s is reachable from no branch; the work was orphaned", label)
			}
		}
		if c := r.fileOnRef("agent/k", "k.txt"); c != "1\n2" {
			t.Errorf("content on the restored branch = %q, want %q", c, "1\n2")
		}
	})
	t.Run("the restore leaves the agent worktree clean", func(t *testing.T) {
		// The crux, made falsifiable. Distinct from S2's identical-looking
		// assertion: there `rebase --abort` had already rewritten index and
		// worktree, and cleanliness followed from tree(squash)==tree(pre).
		// HERE nothing was ever written — `reset --soft` moves only the ref
		// and a hook-rejected commit writes nothing at all — so the staged
		// content the production retry saw was the UNCHANGED index compared
		// against a REWOUND HEAD. Moving the ref back collapses that
		// comparison to empty. Never add a reset/checkout on this path: in
		// the crash variant the index is the only live copy of the work.
		out, gerr := r.gitOK(wt, "status", "--porcelain")
		if gerr != nil {
			t.Fatalf("git status: %v: %s", gerr, out)
		}
		if out != "" {
			t.Errorf("worktree dirty after the ref-only restore:\n%s", out)
		}
	})
	t.Run("the error states the branch was restored and names the tip", func(t *testing.T) {
		if !strings.Contains(err.Error(), agentOrig) {
			t.Errorf("error must name the restored tip %s, got: %v", agentOrig[:8], err)
		}
		if !strings.Contains(err.Error(), premergeRestoredClaim) {
			t.Errorf("error must say the branch was restored, got: %v", err)
		}
	})
	t.Run("the error does not prescribe a manual hard reset", func(t *testing.T) {
		if strings.Contains(err.Error(), "reset --hard") {
			t.Errorf("the index may be the only live copy; never prescribe a hard reset: %v", err)
		}
	})
	t.Run("both pre-merge tips stay ref-reachable", func(t *testing.T) {
		assertPremergePair(t, r, agentOrig, mainBefore)
	})
}

// TestMergeSafety_S6b_CommitFailsAndTheRestoreIsRefused — the CAS refuses
// because the branch is no longer where the engine's reset left it.
//
// The injection is the real RealGitCommit edge, not a contrivance: `git
// commit` SUCCEEDS and the follow-up `git rev-parse --short HEAD` fails, so
// the engine returns an error for a commit that landed. A read-HEAD CAS
// oldSHA would rewind that real commit; keying on the merge base refuses.
//
// This scenario is also where S6's old "recovery is one update-ref" subtest
// now lives (QUM-1104 pin 2). After QUM-1100 the branch is auto-restored in
// the ordinary case, so that subtest would have recovered NOTHING and passed
// for the wrong reason — a green produced by fixing a bug. Here the branch
// genuinely was not restored, so the recovery genuinely recovers.
func TestMergeSafety_S6b_CommitFailsAndTheRestoreIsRefused(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/k2")
	r.commitFile(wt, "k2-work-1", "m.txt", "1\n")
	agentOrig := r.commitFile(wt, "k2-work-2", "m.txt", "1\n2\n")
	mainBefore := r.sha("main")

	deps := scenarioDeps()
	deps.GitCommit = func(worktree, message string) (string, error) {
		// The commit lands, moving the ref off the merge base...
		r.git(worktree, "commit", "-q", "-m", message)
		// ...but the engine never learns its hash.
		return "", fmt.Errorf("git rev-parse --short HEAD: simulated failure after a successful commit")
	}
	_, err := Merge(context.Background(), r.scenarioCfg("k2", "agent/k2", wt, ""), deps)

	if err == nil {
		t.Fatal("expected an error")
	}
	t.Run("the branch was NOT rewound to the pre-squash tip", func(t *testing.T) {
		if got := r.sha("agent/k2"); got == agentOrig {
			t.Errorf("agent/k2 = %s: the CAS must refuse when the ref moved off the merge base, not rewind a real commit", got[:8])
		}
	})
	t.Run("the failure is loud and does not claim a restore", func(t *testing.T) {
		if !strings.Contains(err.Error(), "WARNING") {
			t.Errorf("a refused restore must be louder than the restored case, got: %v", err)
		}
		if strings.Contains(err.Error(), premergeRestoredClaim) {
			t.Errorf("must not claim a restore that did not happen, got: %v", err)
		}
	})
	t.Run("the original commits are reachable from NO branch", func(t *testing.T) {
		// Arrival witness, re-sited here from S6. It lives on a case
		// QUM-1100 does NOT close, so unlike S6's version it cannot be
		// removed by the fix it is meant to witness.
		for _, label := range []string{"k2-work-1", "k2-work-2"} {
			if r.reachableFromBranches(r.commits[label]) {
				t.Errorf("%s is still branch-reachable; the recovery assertions below would be vacuous", label)
			}
		}
	})
	t.Run("both pre-merge tips stay ref-reachable", func(t *testing.T) {
		assertPremergePair(t, r, agentOrig, mainBefore)
	})
	t.Run("recovery is one update-ref from the saved ref", func(t *testing.T) {
		var agentRef string
		for _, ref := range r.premergeRefs() {
			if strings.HasSuffix(ref, "/agent") {
				agentRef = ref
			}
		}
		if agentRef == "" {
			t.Fatal("no /agent recovery ref to recover from")
		}
		r.git(r.root, "update-ref", "refs/heads/agent/k2", agentRef)
		if got := r.sha("agent/k2"); got != agentOrig {
			t.Errorf("after recovery agent/k2 = %s, want %s", got[:8], agentOrig[:8])
		}
		for _, label := range []string{"k2-work-1", "k2-work-2"} {
			if !r.reachableFromBranches(r.commits[label]) {
				t.Errorf("recovery did not make %s branch-reachable again", label)
			}
		}
	})
}

// TestMergeSafety_S6c_StaleAdvertisedBranchRestoresTheRealOne — the
// QUM-1088 retire shape, end to end in real git.
//
// Found in review of QUM-1100's first cut, which had a live defect here.
// After an agent has merged once, sprawl's own ff-merge makes its old
// branch's tip an ancestor of the parent, so merge-base(parent, staleBranch)
// EQUALS that stale tip. Keying the restore CAS on the merge base therefore
// SUCCEEDS on the stale branch — fast-forwarding a branch nobody asked
// about, leaving the real branch rewound, and reporting restored=true.
// Keying on "the branch HEAD is actually on" is what makes it safe.
func TestMergeSafety_S6c_StaleAdvertisedBranchRestoresTheRealOne(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/b1")
	r.commitFile(wt, "b1-work", "b1.txt", "1\n")
	// Land it, the way a first merge would, so main contains b1's tip.
	r.git(r.root, "merge", "-q", "--ff-only", "agent/b1")
	staleTip := r.sha("agent/b1")

	// Delegate reuse: the agent moves to a new branch; state.Branch still
	// advertises the old one.
	r.git(wt, "switch", "-q", "-c", "agent/b2")
	realOrig := r.commitFile(wt, "b2-work", "b2.txt", "2\n")

	// Precondition: this scenario only bites when the merge base coincides
	// with the stale branch's tip. Assert it rather than assuming it.
	mb := strings.TrimSpace(r.git(r.root, "merge-base", "main", "agent/b1"))
	if mb != staleTip {
		t.Fatalf("precondition: merge-base(main, agent/b1)=%s must equal the stale tip %s", mb[:8], staleTip[:8])
	}

	deps := scenarioDeps()
	deps.GitCommit = func(string, string) (string, error) {
		return "", fmt.Errorf("git commit: exit status 1: pre-commit hook rejected")
	}
	// cfg.AgentBranch is the STALE name, exactly as agentops/retire.go passes it.
	_, err := Merge(context.Background(), r.scenarioCfg("b1", "agent/b1", wt, ""), deps)
	if err == nil {
		t.Fatal("expected an error")
	}

	t.Run("the REAL branch is restored", func(t *testing.T) {
		if got := r.sha("agent/b2"); got != realOrig {
			t.Errorf("agent/b2 = %s, want its pre-merge tip %s", got[:8], realOrig[:8])
		}
	})
	t.Run("the stale branch is NOT touched", func(t *testing.T) {
		if got := r.sha("agent/b1"); got != staleTip {
			t.Errorf("agent/b1 = %s, want it untouched at %s — the restore moved a branch nobody asked about", got[:8], staleTip[:8])
		}
	})
	t.Run("the worktree is clean", func(t *testing.T) {
		out, _ := r.gitOK(wt, "status", "--porcelain")
		if out != "" {
			t.Errorf("worktree dirty after the restore:\n%s", out)
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

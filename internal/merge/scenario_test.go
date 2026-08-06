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

// TestMergeSafety_S6_CrashBetweenSoftResetAndCommit — the crash window. The
// process dies after `git reset --soft <mergeBase>` and before the squash
// commit, so the agent's work exists only as staged files in the worktree,
// reachable from no ref at all. Simulated with an injected failing GitCommit;
// the on-disk git state is identical to a SIGKILL there.
//
// This is the case the recovery refs exist for: nothing else — not the
// branch, not the squash, not `--ff-only` — holds the original tip.
func TestMergeSafety_S6_CrashBetweenSoftResetAndCommit(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/k")
	r.commitFile(wt, "k-work-1", "k.txt", "1\n")
	agentOrig := r.commitFile(wt, "k-work-2", "k.txt", "1\n2\n")
	mainBefore := r.sha("main")

	deps := scenarioDeps()
	deps.GitCommit = func(_, _ string) (string, error) {
		return "", fmt.Errorf("simulated crash between reset --soft and commit")
	}
	_, err := Merge(context.Background(), r.scenarioCfg("k", "agent/k", wt, ""), deps)

	if err == nil {
		t.Fatal("expected the injected crash to fail the merge")
	}
	t.Run("main is untouched", func(t *testing.T) {
		if got := r.sha("main"); got != mainBefore {
			t.Errorf("main = %s, want unchanged %s", got[:8], mainBefore[:8])
		}
	})
	t.Run("the window is real: work is branch-unreachable and only staged", func(t *testing.T) {
		if got := r.sha("agent/k"); got != r.commits["init"] {
			t.Errorf("agent/k = %s, want the merge base %s — the window did not open", got[:8], r.commits["init"][:8])
		}
		for _, label := range []string{"k-work-1", "k-work-2"} {
			if r.reachableFromBranches(r.commits[label]) {
				t.Errorf("%s is still branch-reachable; the window did not open and the next subtest would be vacuous", label)
			}
		}
		out, gerr := r.gitOK(wt, "status", "--porcelain")
		if gerr != nil || out == "" {
			t.Errorf("expected staged-but-uncommitted work in the worktree, got %q (err %v)", out, gerr)
		}
	})
	t.Run("both pre-merge tips stay ref-reachable", func(t *testing.T) {
		assertPremergePair(t, r, agentOrig, mainBefore)
	})
	t.Run("recovery is one update-ref from the saved ref", func(t *testing.T) {
		// The AC is "a crash mid-merge is recoverable by one update-ref".
		// Do the recovery through the ref by NAME — resolving it is the part
		// a human actually performs — rather than through the SHA the test
		// happens to remember.
		var agentRef string
		for _, ref := range r.premergeRefs() {
			if strings.HasSuffix(ref, "/agent") {
				agentRef = ref
			}
		}
		if agentRef == "" {
			t.Fatal("no /agent recovery ref to recover from")
		}
		r.git(r.root, "update-ref", "refs/heads/agent/k", agentRef)

		if got := r.sha("agent/k"); got != agentOrig {
			t.Errorf("after recovery agent/k = %s, want %s", got[:8], agentOrig[:8])
		}
		if !r.reachableFromBranches(r.commits["k-work-1"]) || !r.reachableFromBranches(r.commits["k-work-2"]) {
			t.Error("recovery did not make the original commits branch-reachable again")
		}
	})
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

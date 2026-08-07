package supervisor

// QUM-1088, folded into QUM-1087: `retire --merge` must go through the ONE merge
// engine instead of performing its own merge inline.
//
// The tests are split by what each CAN prove:
//
//   - the REAL-GIT ones install agentops.Merge as mergeFn and drive an actual
//     repository, because the defect was about WHICH BRANCH GIT RESOLVED. A mock
//     DoMerge can only report which string was passed, and the string being
//     wrong is exactly what was already known — so a mock-based test would have
//     been green for the whole life of the defect.
//   - the MOCK ones pin routing, ordering and serialisation, which are
//     properties of the call sequence and cannot be read off a repository.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentops"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/sprawlmcp/calllog"
	"github.com/dmotles/sprawl/internal/state"
)

// errTestRetireMerge is the injected merge failure, named so a test cannot pass
// on some other error.
var errTestRetireMerge = errTestSentinel("MERGE-VALIDATE-FAILED-SENTINEL: tests fail after rebasing")

type errTestSentinel string

func (e errTestSentinel) Error() string { return string(e) }

func retireMergeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@x",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@x",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func retireMergeCommit(t *testing.T, dir, msg, file, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	retireMergeGit(t, dir, "add", "--", file)
	retireMergeGit(t, dir, "commit", "-q", "-m", msg)
	return retireMergeGit(t, dir, "rev-parse", "HEAD")
}

// wireRealMergeEngine points a fake Real at a real git repo at tmpDir and
// installs the REAL agentops.Merge, so r.Merge exercises branch resolution, the
// preconditions and the merge engine for real.
func wireRealMergeEngine(t *testing.T, r *Real, tmpDir string) {
	t.Helper()
	r.mergeDeps = &agentops.MergeDeps{
		Getenv: func(k string) string {
			switch k {
			case "SPRAWL_ROOT":
				return tmpDir
			case "SPRAWL_AGENT_IDENTITY":
				return "weave"
			}
			return ""
		},
		LoadAgent:     state.LoadAgent,
		ListAgents:    state.ListAgents,
		GitStatus:     agentops.RealGitStatus,
		BranchExists:  agentops.RealBranchExists,
		CurrentBranch: agentops.GitCurrentBranch,
		// No validate command: these tests are about branch resolution and
		// routing, and running a real `make validate` inside a fixture repo
		// would be slow and prove nothing about either.
		LoadConfig:   func(string) (*config.Config, error) { return &config.Config{}, nil },
		DoMerge:      merge.Merge,
		NewMergeDeps: func() *merge.Deps { return merge.RealDeps(os.Stderr) },
		Stderr:       os.Stderr,
	}
	r.mergeFn = agentops.Merge
}

// TestRetire_MergeFirst_MergesWorktreeHeadBranch_NotStateBranch is the QUM-1088
// regression test, through the real engine and real git.
//
// The defect: agentops/retire.go built its merge config from agentState.Branch
// — the SPAWN-TIME name — while agentops.Merge had resolved the worktree's real
// HEAD since QUM-511. Once delegate reuse moved a worktree to a later branch the
// engine split across two branches and the merge REPORTED SUCCESS while the
// parent received none of the agent's current work.
//
// The shape that made it silent is reproduced exactly: the stale branch has
// already landed on main, so `--ff-only` against the stale NAME exits 0 with
// "Already up to date" and nothing fails.
func TestRetire_MergeFirst_MergesWorktreeHeadBranch_NotStateBranch(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	retireMergeGit(t, tmpDir, "init", "-q", "-b", "main")
	// The caller's worktree here IS the repo root, and agentops.Merge's
	// precondition 7 requires it clean. The fake supervisor's own scaffolding
	// (.sprawl/, agent worktrees) is untracked, so it must be ignored or the
	// merge is refused before it resolves any branch — which would make this
	// test pass/fail for a reason unrelated to QUM-1088.
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte(".sprawl/\nwt-*/\n"), 0o600); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	retireMergeCommit(t, tmpDir, "base", "README.md", "base\n")
	retireMergeGit(t, tmpDir, "add", "--", ".gitignore")
	retireMergeGit(t, tmpDir, "commit", "-q", "-m", "ignore scaffolding")
	if out := retireMergeGit(t, tmpDir, "status", "--porcelain"); out != "" {
		t.Fatalf("precondition: the caller worktree must be clean, got:\n%s", out)
	}

	wt := filepath.Join(tmpDir, "wt-b")
	retireMergeGit(t, tmpDir, "worktree", "add", "-q", "-b", "agent/b1", wt)
	retireMergeCommit(t, wt, "b1-work", "b1.txt", "1\n")
	retireMergeGit(t, tmpDir, "merge", "-q", "--ff-only", "agent/b1")
	staleTip := retireMergeGit(t, tmpDir, "rev-parse", "agent/b1")

	// Delegate reuse: the worktree moves on; state.Branch still says agent/b1.
	retireMergeGit(t, wt, "switch", "-q", "-c", "agent/b2")
	realWork := retireMergeCommit(t, wt, "b2-work", "b2.txt", "2\n")

	if mb := retireMergeGit(t, tmpDir, "merge-base", "main", "agent/b1"); mb != staleTip {
		t.Fatalf("precondition: merge-base(main, agent/b1)=%s must equal the stale tip %s, or the silent-no-op shape is not reproduced", mb[:8], staleTip[:8])
	}

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "b", Type: "engineer", Family: "engineering",
		Branch:   "agent/b1", // STALE
		Worktree: wt,
		Parent:   "weave",
		Status:   "active",
	})
	wireRealMergeEngine(t, r, tmpDir)
	var teardownRan bool
	r.retireFn = func(context.Context, *agentops.RetireDeps, string, bool, bool, bool, bool, bool) ([]string, error) {
		teardownRan = true
		return []string{"b"}, nil
	}

	if _, err := r.Retire(context.Background(), "weave", "b", true /* mergeFirst */, false, false, true); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	t.Run("the agent's CURRENT work reaches the parent", func(t *testing.T) {
		// By CONTENT, not by SHA: the rebase legitimately rewrites SHAs, so an
		// ancestor check on the original commit would fail for a merge that
		// worked correctly.
		out, showErr := exec.Command("git", "-C", tmpDir, "show", "main:b2.txt").Output()
		if showErr != nil || strings.TrimSpace(string(out)) != "2" {
			t.Errorf("QUM-1088: main does not contain the agent's current work.\n"+
				"  main after = %s\n  the agent's real commit (on agent/b2) = %s\n"+
				"  the engine resolved the STALE state.Branch (agent/b1) instead of the worktree HEAD branch (agent/b2)",
				retireMergeGit(t, tmpDir, "rev-parse", "main")[:8], realWork[:8])
		}
	})
	t.Run("the resolved branch was the worktree HEAD branch", func(t *testing.T) {
		// Independent of content, so the two subtests fail for different reasons.
		branchTip := retireMergeGit(t, tmpDir, "rev-parse", "agent/b2")
		if _, aerr := exec.Command("git", "-C", tmpDir, "merge-base", "--is-ancestor", "--", branchTip, "main").Output(); aerr != nil {
			t.Errorf("agent/b2's tip %s is not an ancestor of main", branchTip[:8])
		}
		if got := retireMergeGit(t, tmpDir, "rev-parse", "agent/b1"); got != staleTip {
			t.Errorf("the stale branch agent/b1 moved to %s; it must be left alone", got[:8])
		}
	})
	t.Run("teardown ran, because the merge succeeded", func(t *testing.T) {
		if !teardownRan {
			t.Error("retire teardown did not run after a successful merge")
		}
	})
}

// TestRetire_MergeFirst_DetachedHead_Refuses — the AC, through the real path.
//
// agentops.Merge refuses a detached HEAD because it cannot resolve a branch to
// merge. The point of routing retire through it is that retire INHERITS that
// refusal instead of guessing — where before it used the stale advertised name,
// which would have resolved and merged the wrong thing.
func TestRetire_MergeFirst_DetachedHead_Refuses(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	retireMergeGit(t, tmpDir, "init", "-q", "-b", "main")
	retireMergeCommit(t, tmpDir, "base", "README.md", "base\n")

	wt := filepath.Join(tmpDir, "wt-d")
	retireMergeGit(t, tmpDir, "worktree", "add", "-q", "-b", "agent/d", wt)
	tip := retireMergeCommit(t, wt, "d-work", "d.txt", "1\n")
	retireMergeGit(t, wt, "checkout", "-q", "--detach", tip)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "d", Type: "engineer", Family: "engineering",
		Branch: "agent/d", Worktree: wt, Parent: "weave", Status: "active",
	})
	wireRealMergeEngine(t, r, tmpDir)
	var teardownRan bool
	r.retireFn = func(context.Context, *agentops.RetireDeps, string, bool, bool, bool, bool, bool) ([]string, error) {
		teardownRan = true
		return []string{"d"}, nil
	}

	_, err := r.Retire(context.Background(), "weave", "d", true /* mergeFirst */, false, false, true)
	if err == nil {
		t.Fatal("expected retire --merge to refuse a detached-HEAD agent worktree")
	}
	if !strings.Contains(err.Error(), "detached HEAD") {
		t.Errorf("the refusal must name the cause, got: %v", err)
	}
	if teardownRan {
		t.Error("teardown ran despite the merge being refused: the agent would have been destroyed for a merge that never happened")
	}
	if _, serr := os.Stat(wt); serr != nil {
		t.Errorf("the agent worktree was removed: %v", serr)
	}
	if _, serr := state.LoadAgent(tmpDir, "d"); serr != nil {
		t.Errorf("the agent's state file was removed: %v", serr)
	}
}

// TestRetire_MergeFirst_MergeFailureAbortsBeforeAnyTeardown — the AC.
//
// SCOPE, stated so it does not imply more than it proves: this is a claim about
// the TARGET agent. With cascade, children are torn down before the merge — which
// is exactly why merge+cascade is refused on the runtime-less path — so "before
// any teardown" has never meant "before the descendants".
func TestRetire_MergeFirst_MergeFailureAbortsBeforeAnyTeardown(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	wt := filepath.Join(tmpDir, "wt-v")
	if err := os.MkdirAll(wt, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "v", Type: "engineer", Family: "engineering",
		Branch: "agent/v", Worktree: wt, Parent: "weave", Status: "active",
	})

	r.mergeFn = func(context.Context, *agentops.MergeDeps, string, string, bool, bool, bool) (*agentops.MergeOutcome, error) {
		return nil, errTestRetireMerge
	}
	var teardownRan bool
	r.retireFn = func(context.Context, *agentops.RetireDeps, string, bool, bool, bool, bool, bool) ([]string, error) {
		teardownRan = true
		return []string{"v"}, nil
	}

	_, err := r.Retire(context.Background(), "weave", "v", true /* mergeFirst */, false, false, false)
	if err == nil {
		t.Fatal("expected retire to fail when the merge fails")
	}
	// ARRIVAL first: without this, everything below is satisfied by a refusal
	// that happened for some unrelated reason and the abort ordering is untested.
	if !strings.Contains(err.Error(), errTestRetireMerge.Error()) {
		t.Fatalf("retire did not fail on the MERGE; the assertions below would be about the wrong path.\n got: %v", err)
	}
	if teardownRan {
		t.Fatal("teardown ran after a failed merge: the agent's work would be destroyed for a merge that did not land")
	}
	// The natural reading of "retire failed" is that it half-happened, so the
	// message has to say otherwise.
	if !strings.Contains(err.Error(), "intact") {
		t.Errorf("the error must state the agent was not retired and is intact, got: %v", err)
	}

	st, lerr := state.LoadAgent(tmpDir, "v")
	if lerr != nil {
		t.Fatalf("the agent's state file is gone: %v", lerr)
	}
	if st.Status == "retiring" {
		t.Error("the agent was left in the 'retiring' state by a retire that never began tearing down")
	}
	if _, serr := os.Stat(wt); serr != nil {
		t.Errorf("the agent worktree was removed: %v", serr)
	}
}

// TestRetire_MergeFirst_IsSerializedByMergeSem — the AC's named observable.
//
// Before this change Real.Retire(mergeFirst) reached the engine via
// retireFn -> agentops.Retire -> DoMerge and never touched mergeSem: 116 of
// ~570 historical merges took that path unserialised. Routing through r.Merge
// picks up the semaphore for free, and `merge.queued` naming the in-flight agent
// is the evidence.
func TestRetire_MergeFirst_IsSerializedByMergeSem(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "s", Type: "engineer", Family: "engineering",
		Branch: "agent/s", Worktree: filepath.Join(tmpDir, "wt-s"), Parent: "weave", Status: "active",
	})

	type ev struct{ callID, step, tail string }
	var mu sync.Mutex
	var events []ev
	r.SetProgressEmitter(func(callID, step, tail string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev{callID, step, tail})
	})

	enteredA := make(chan struct{})
	releaseA := make(chan struct{})
	r.mergeFn = func(_ context.Context, _ *agentops.MergeDeps, name, _ string, _, _, _ bool) (*agentops.MergeOutcome, error) {
		if name == "agent-a" {
			close(enteredA)
			<-releaseA
		}
		return &agentops.MergeOutcome{}, nil
	}
	r.retireFn = func(context.Context, *agentops.RetireDeps, string, bool, bool, bool, bool, bool) ([]string, error) {
		return []string{"s"}, nil
	}

	// A plain merge of a DIFFERENT agent occupies the semaphore.
	resA := make(chan error, 1)
	go func() {
		_, err := r.Merge(context.Background(), "", "agent-a", "", false)
		resA <- err
	}()
	select {
	case <-enteredA:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent-a's merge to acquire the semaphore")
	}

	// Now retire --merge of "s". It must QUEUE behind agent-a.
	const callID = "call-retire-s"
	ctx := calllog.WithCallID(context.Background(), callID)
	resS := make(chan error, 1)
	go func() {
		_, err := r.Retire(ctx, "weave", "s", true /* mergeFirst */, false, false, true)
		resS <- err
	}()

	// It must BLOCK. If retire's merge bypassed the semaphore — the QUM-1088
	// defect — it would complete here while agent-a still holds it.
	select {
	case err := <-resS:
		t.Fatalf("retire --merge completed while another merge held the semaphore (err=%v): it is NOT serialised", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(releaseA)
	if err := <-resA; err != nil {
		t.Fatalf("agent-a's merge: %v", err)
	}
	select {
	case err := <-resS:
		if err != nil {
			t.Fatalf("retire --merge after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retire --merge never completed after the semaphore was released")
	}

	mu.Lock()
	defer mu.Unlock()
	var sawQueued bool
	for _, e := range events {
		if e.step == "merge.queued" && e.callID == callID {
			sawQueued = true
			if !strings.Contains(e.tail, "agent-a") {
				t.Errorf("merge.queued must name the agent it queued behind, got tail %q", e.tail)
			}
		}
	}
	if !sawQueued {
		t.Errorf("no merge.queued checkpoint for the retire call: its merge did not go through Real.Merge's serialisation (events: %+v)", events)
	}
}

// TestMerge_OrdinaryPath_DoesNotSalvageTerminalAgents keeps the
// salvagingTerminalAgent escape hatch scoped to retire.
//
// One line, and it is the difference between a scoped exception and a policy
// fork: the parameter is invisible at the ordinary call site, so nothing else
// would notice it drifting true.
func TestMerge_OrdinaryPath_DoesNotSalvageTerminalAgents(t *testing.T) {
	r, _ := newFakeReal(t)
	var gotSalvage, called bool
	r.mergeFn = func(_ context.Context, _ *agentops.MergeDeps, _, _ string, _, _, salvage bool) (*agentops.MergeOutcome, error) {
		called, gotSalvage = true, salvage
		return &agentops.MergeOutcome{}, nil
	}
	if _, err := r.Merge(context.Background(), "weave", "alpha", "", false); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !called {
		t.Fatal("precondition: the merge must have run")
	}
	if gotSalvage {
		t.Error("the ordinary merge path must NOT salvage terminal agents: precondition 4 exists to refuse an agent that never got going, and only retire — where the agent is being torn down and its branch is the whole point — may relax it")
	}
}

// TestRetire_MergeFirst_SalvagesTerminalAgent is the other half: the retire path
// MUST pass salvagingTerminalAgent, or `retire --merge` of a dead agent is
// refused and there is no supported way to land its commits.
//
// This is the case retire --merge earns its keep in, and refusing it would push
// people to manual git — the thing this whole series exists to stop.
func TestRetire_MergeFirst_SalvagesTerminalAgent(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "dead", Type: "engineer", Family: "engineering",
		Branch: "agent/dead", Worktree: filepath.Join(tmpDir, "wt-dead"),
		Parent: "weave", Status: "faulted",
	})

	var gotSalvage, called bool
	r.mergeFn = func(_ context.Context, _ *agentops.MergeDeps, _, _ string, _, _, salvage bool) (*agentops.MergeOutcome, error) {
		called, gotSalvage = true, salvage
		return &agentops.MergeOutcome{}, nil
	}
	r.retireFn = func(context.Context, *agentops.RetireDeps, string, bool, bool, bool, bool, bool) ([]string, error) {
		return []string{"dead"}, nil
	}

	if _, err := r.Retire(context.Background(), "weave", "dead", true /* mergeFirst */, false, false, true); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if !called {
		t.Fatal("precondition: the merge must have run")
	}
	if !gotSalvage {
		t.Error("retire --merge must pass salvagingTerminalAgent: a faulted/died agent with commits on its branch is legitimately mergeable, and precondition 4 would otherwise refuse it with no supported alternative")
	}
}

// TestRetire_MergeFirstWithCascade_LivePath_MergesAfterChildrenAreGone is the
// test that was MISSING, and its absence hid a real defect.
//
// `Real.Retire` has two paths to teardown. On the runtime-backed one the cascade
// loop tears the children down and the merge is placed AFTER it, precisely so
// that agentops.Merge's precondition 5 (no live children) is satisfied and
// cascade+merge works. The first cut then passed `cascade` straight into
// `mergeForRetire`, whose guard refuses on cascade — so the live path refused
// too, throwing away the ordering the branch exists to arrange.
//
// Nothing caught it because the only cascade+merge test used `newFakeReal`
// WITHOUT a started runtime, so it exercised the runtime-less path exclusively.
// Two paths, one covered: the same shape as QUM-1088 itself, where
// agentops.Merge had the QUM-511 fix and agentops.Retire never got it.
//
// The guard's parameter is now named for the CONDITION (`childrenStillPending`)
// rather than for the flag that usually implies it, so a future reader cannot
// pass `cascade` here by reflex.
func TestRetire_MergeFirstWithCascade_LivePath_MergesAfterChildrenAreGone(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	parent := &state.AgentState{
		Name: "mgr", Type: "manager", Family: "engineering",
		Branch: "agent/mgr", Worktree: filepath.Join(tmpDir, "wt-mgr"),
		Parent: "weave", Status: "active",
	}
	child := &state.AgentState{
		Name: "kid", Type: "engineer", Family: "engineering",
		Branch: "agent/kid", Worktree: filepath.Join(tmpDir, "wt-kid"),
		Parent: "mgr", Status: "active",
	}
	saveTestAgent(t, tmpDir, parent)
	saveTestAgent(t, tmpDir, child)

	// A LIVE runtime, so Real.Retire takes the runtime-backed path and runs its
	// own cascade loop. This is the precondition the missing coverage was about.
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      parent,
		Starter: &runtimeTestStarter{session: &runtimeTestSession{
			sessionID: "sess-mgr",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		}},
	})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	// ONE log, written by BOTH stubs, because the claim this test makes is a
	// RELATION between a merge and a teardown. Two separate slices cannot
	// express it: the first cut recorded merges and retires apart and asserted
	// only `retiredOrder[0] == "kid"`, so hoisting the `if mergeFirst {
	// mergeForRetire(...) }` block above the cascade loop — reinstating the
	// exact ordering defect this branch is arranged to prevent — left every
	// TestRetire_MergeFirst* test GREEN, this subtest's title notwithstanding.
	//
	// No mutex. The cascade recursion, the merge and both teardowns all run
	// inline on this goroutine under r.Retire (the child has no started
	// runtime, so it takes the runtime-less path, also inline), so there is no
	// interleaving here for -race to witness. -race therefore does not confirm
	// today's claim; it guards a FUTURE move of any of these onto a goroutine,
	// which is when this would need a mutex.
	var events []string
	r.mergeFn = func(_ context.Context, _ *agentops.MergeDeps, name, _ string, _, _, _ bool) (*agentops.MergeOutcome, error) {
		events = append(events, "merge:"+name)
		return &agentops.MergeOutcome{}, nil
	}
	r.retireFn = func(_ context.Context, _ *agentops.RetireDeps, name string, _, _, _, _, _ bool) ([]string, error) {
		events = append(events, "retire:"+name)
		return []string{name}, nil
	}

	if _, err := r.Retire(context.Background(), "weave", "mgr",
		true /* mergeFirst */, false /* abandon */, true /* cascade */, true /* noValidate */); err != nil {
		t.Fatalf("retire --merge --cascade on the LIVE path must succeed (children are torn down before the merge): %v", err)
	}

	idx := func(want string) int {
		for i, e := range events {
			if e == want {
				return i
			}
		}
		return -1
	}

	t.Run("the target was merged, exactly once", func(t *testing.T) {
		if idx("merge:mgr") < 0 {
			t.Errorf("events = %v, want a merge:mgr", events)
		}
		n := 0
		for _, e := range events {
			if strings.HasPrefix(e, "merge:") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("events = %v, want exactly one merge", events)
		}
	})
	t.Run("the cascade CHILD was not merged", func(t *testing.T) {
		// Arrival guard on the MERGE specifically, not on the log being
		// non-empty: "kid was not merged" is equally vacuous when no merge
		// happened at all, and [retire:kid retire:mgr] is non-empty.
		if idx("merge:mgr") < 0 {
			t.Fatalf("events = %v, want a merge:mgr; with no merge at all this subtest asserts nothing", events)
		}
		if idx("merge:kid") >= 0 {
			t.Errorf("events = %v: a cascade child was merged; only the top-level target may be", events)
		}
	})
	t.Run("the child was torn down BEFORE the target's merge", func(t *testing.T) {
		// The ordering that makes precondition 5 satisfiable on this path. If
		// the child were retired after the merge, the merge would be refused
		// for having live children — which is the runtime-less path's problem.
		retireKid, mergeMgr := idx("retire:kid"), idx("merge:mgr")
		if retireKid < 0 || mergeMgr < 0 {
			t.Fatalf("events = %v, want both a retire:kid and a merge:mgr; without both, this subtest asserts nothing", events)
		}
		if retireKid > mergeMgr {
			t.Errorf("event order = %v; the child must be torn down BEFORE the target's merge, or agentops.Merge's precondition 5 (no live children) cannot be satisfied on this path", events)
		}
	})
	t.Run("the target was merged BEFORE its own agentops.Retire call", func(t *testing.T) {
		// Scoped deliberately to what these two stubs can see. The live order
		// is mergeForRetire → runtime.Stop → retireFn, and this pins only the
		// FIRST and THIRD of those.
		//
		// What it does NOT pin, stated because the tempting comment to write
		// here overclaims it: the runtime.Stop boundary. Stop stamps
		// StatusFaulted and precondition 4 refuses a faulted agent, so merging
		// after Stop would make the teardown itself cause the refusal — but
		// neither stub observes Stop, so moving mergeForRetire to sit BETWEEN
		// Stop and retireFn realises exactly that hazard and leaves this
		// subtest green. Covering it needs a stop event recorded off the
		// runtime, which is more seam than this test's finding warrants.
		mergeMgr, retireMgr := idx("merge:mgr"), idx("retire:mgr")
		if mergeMgr < 0 || retireMgr < 0 {
			t.Fatalf("events = %v, want both a merge:mgr and a retire:mgr", events)
		}
		if mergeMgr > retireMgr {
			t.Errorf("event order = %v; the target must be merged before it is torn down", events)
		}
	})
}

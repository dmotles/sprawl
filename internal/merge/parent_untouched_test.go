package merge

// QUM-1087 AC 2: "No code path in internal/merge resets, rewinds, or
// force-updates the parent branch."
//
// The weak way to check that is to assert `GitResetHard` was not called. That
// tests our WIRING, not the parent: it goes green if any other seam mutates
// the parent, and it goes green for the wrong reason once the seam is renamed.
// The instrument here instead asks a question about the parent worktree —
// "which MUTATING seams were invoked against it?" — and is total over the seam
// set by construction, so adding a seam without classifying it fails the build
// of the assertion rather than quietly shrinking it.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// errTestValidate is the injected validate failure. A named sentinel, so a
// test that accidentally passes on some OTHER error fails by name.
var errTestValidate = errors.New("VALIDATE-FAILED-SENTINEL: exit status 1")

// seamKind distinguishes a seam that only READS the repository from one that
// MUTATES it. The distinction is the whole point: the engine legitimately
// reads the parent (its tip, for the premerge ref and for the post-ff SHA
// equality predicate), and must mutate it exactly once, via GitFFMerge.
//
// TWO LIMITS OF THIS INSTRUMENT, both worth knowing before trusting it.
//
// First, it measures "seam aimed at the parent worktree PATH", while the
// invariant we actually care about is "refs/heads/<parent branch> was not
// moved backwards". Three seams pass something that is not a worktree in that
// position at all: LockAcquire receives a lock file path, and WritePoke and
// GitUpdateRef receive SprawlRoot. That matters because in production
// SprawlRoot and ParentWorktree are frequently THE SAME DIRECTORY (weave's
// parent worktree IS the main checkout), whereas this fixture keeps them
// distinct. So the premerge GitUpdateRef writes are aimed at the parent's
// directory in production and not in the fixture — the invariant holds in both
// because those writes create refs under refs/sprawl/premerge/ rather than
// touching the parent branch, but the instrument cannot see that distinction.
// Read a failure here as "something aimed a mutation at the parent's
// directory", then check which ref.
//
// Second, it is only as total as tracedDeps' wrapping, which is why
// TestTracedDepsWrapsEverySeam exists.
type seamKind int

const (
	seamRead seamKind = iota
	seamMutate
)

// seamKinds classifies every func-typed field of Deps except Checkpoint.
//
// Kept as a hand-written map on purpose. A derived-from-name rule ("anything
// containing Reset or Commit mutates") would silently misclassify the next
// seam someone adds, and the failure mode of a misclassification is a
// parent-mutation assertion that cannot fail.
var seamKinds = map[string]seamKind{
	"LockAcquire":        seamRead, // touches a lockfile, not the repo
	"GitMergeBase":       seamRead,
	"GitRevParseHead":    seamRead,
	"GitRevParseRef":     seamRead,
	"GitIsAncestor":      seamRead,
	"GitSymbolicRefHead": seamRead,
	// RunTestsStreaming is a MUTATION, and the classification is doing real
	// work. It runs an arbitrary shell command (`make validate`), so treating
	// it as a read would be generous on its own terms — but the decisive
	// reason is QUM-1087's actual subject: WHICH TREE validate runs in. As a
	// read, an implementation that validates the parent checkout leaves
	// OnlyFFMergeMutatesTheParent green, and the relocation would rest on a
	// single separate test. As a mutation, validating the parent VIOLATES the
	// parent-untouched invariant directly. Costs nothing on the correct
	// design, where it is aimed at the agent worktree.
	"RunTestsStreaming": seamMutate,
	"WritePoke":         seamRead, // writes .sprawl/agents, not git refs
	"Now":               seamRead,

	"GitRebase":       seamMutate,
	"GitRebaseAbort":  seamMutate,
	"GitFFMerge":      seamMutate,
	"GitUpdateRef":    seamMutate,
	"GitUpdateRefCAS": seamMutate,
}

// TestSeamClassificationIsTotal is the guard that keeps the assertions in this
// file honest as Deps changes. Without it, adding a seam would leave it
// unclassified, and an unclassified seam is treated as "not a mutation" by
// every parent assertion below — a silent hole precisely when the engine grows
// a new way to touch the parent.
func TestSeamClassificationIsTotal(t *testing.T) {
	var d Deps
	v := reflect.ValueOf(d)
	tp := v.Type()
	var unclassified []string
	seen := 0
	for i := 0; i < tp.NumField(); i++ {
		f := tp.Field(i)
		if f.Name == "Checkpoint" || v.Field(i).Kind() != reflect.Func {
			continue
		}
		seen++
		if _, ok := seamKinds[f.Name]; !ok {
			unclassified = append(unclassified, f.Name)
		}
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		t.Errorf("Deps seams are not classified in seamKinds: %v\n"+
			"Classify each as seamRead or seamMutate. An unclassified seam is treated as a\n"+
			"non-mutation by every parent-untouched assertion in this file, which is exactly\n"+
			"the hole those assertions exist to close.", unclassified)
	}
	// Assertion-count floor, same reasoning as MinDepsSeams: a refactor that
	// hides seams behind a struct or interface makes them invisible to the
	// Func-kind walk, and this file's coverage would silently drop to nothing
	// while every test still passed.
	if seen != len(seamKinds) {
		t.Errorf("walked %d seams but seamKinds classifies %d — the map has stale entries or the walk stopped seeing fields", seen, len(seamKinds))
	}
	if seen < MinDepsSeams {
		t.Errorf("walked only %d seams, want at least MinDepsSeams=%d", seen, MinDepsSeams)
	}
}

// TestDeps_HasNoRollbackSeam discharges AC 2 ("no code path in internal/merge
// resets, rewinds, or force-updates the parent") in its strongest available
// form: not by auditing callers, but by requiring the PRIMITIVE to be absent.
//
// TestSeamClassificationIsTotal cannot do this job — it is satisfied by
// CLASSIFYING GitResetHard rather than deleting it. And an audit of callers is
// a claim about today's call graph that the next edit invalidates silently.
// A seam that does not exist cannot acquire a caller.
func TestDeps_HasNoRollbackSeam(t *testing.T) {
	forbidden := []string{"GitResetHard", "GitResetSoft", "GitCommit", "GitLogRange"}
	var d Deps
	tp := reflect.TypeOf(d)
	for _, name := range forbidden {
		if _, ok := tp.FieldByName(name); ok {
			t.Errorf("Deps still carries the %q seam.\n"+
				"QUM-1087 removes the squash and the parent rollback entirely; while the seam exists\n"+
				"it can silently acquire a caller, and AC 2 becomes a claim about the call graph\n"+
				"rather than about the code.", name)
		}
	}
}

// seamTrace records every seam invocation as "Kind:Seam:dir", in call order.
type seamTrace struct {
	calls []string
}

func (s *seamTrace) note(kind seamKind, seam, dir string) {
	k := "READ"
	if kind == seamMutate {
		k = "MUTATE"
	}
	s.calls = append(s.calls, k+":"+seam+":"+dir)
}

// mutationsAgainst returns the MUTATING seams invoked against dir.
func (s *seamTrace) mutationsAgainst(dir string) []string {
	var out []string
	for _, c := range s.calls {
		parts := strings.SplitN(c, ":", 3)
		if parts[0] == "MUTATE" && parts[2] == dir {
			out = append(out, parts[1])
		}
	}
	return out
}

// tracedDeps wraps every seam of newTestDeps so each invocation is recorded
// with the directory it was aimed at. It also returns the SET OF SEAM NAMES it
// wrapped, recorded at wrap time rather than at call time — several seams
// (GitRebaseAbort, GitUpdateRefCAS) are not invoked on the happy path, so a
// call-time set could never prove coverage.
//
// The wrapping must be exhaustive over seamKinds, and that is NOT implied by
// TestSeamClassificationIsTotal — that test compares seamKinds against Deps and
// says nothing about what is wrapped. A seam classified seamMutate but left
// unwrapped is invisible to mutationsAgainst, which then reports [] forever:
// the exact hole this file exists to close, in the exact place a reader would
// assume was covered. TestTracedDepsWrapsEverySeam closes it.
func tracedDeps(tr *seamTrace) (*Deps, map[string]bool) {
	d := newTestDeps()
	wrapped := map[string]bool{}
	// reg records that a seam is wrapped and returns a note func bound to it.
	reg := func(seam string) func(string) {
		wrapped[seam] = true
		kind := seamKinds[seam]
		return func(dir string) { tr.note(kind, seam, dir) }
	}

	lock, noteLock := d.LockAcquire, reg("LockAcquire")
	d.LockAcquire = func(p string) (func(), error) { noteLock(p); return lock(p) }

	mb, noteMB := d.GitMergeBase, reg("GitMergeBase")
	d.GitMergeBase = func(root, a, b string) (string, error) { noteMB(root); return mb(root, a, b) }

	rph, noteRPH := d.GitRevParseHead, reg("GitRevParseHead")
	d.GitRevParseHead = func(wt string) (string, error) { noteRPH(wt); return rph(wt) }

	rpr, noteRPR := d.GitRevParseRef, reg("GitRevParseRef")
	d.GitRevParseRef = func(wt, rev string) (string, error) { noteRPR(wt); return rpr(wt, rev) }

	anc, noteAnc := d.GitIsAncestor, reg("GitIsAncestor")
	d.GitIsAncestor = func(wt, a, b string) (bool, error) { noteAnc(wt); return anc(wt, a, b) }

	sym, noteSym := d.GitSymbolicRefHead, reg("GitSymbolicRefHead")
	d.GitSymbolicRefHead = func(wt string) (string, error) { noteSym(wt); return sym(wt) }

	rts, noteRTS := d.RunTestsStreaming, reg("RunTestsStreaming")
	d.RunTestsStreaming = func(ctx context.Context, dir, cmd string, sink func(string)) (string, error) {
		noteRTS(dir)
		return rts(ctx, dir, cmd, sink)
	}

	poke, notePoke := d.WritePoke, reg("WritePoke")
	d.WritePoke = func(root, name, content string) error { notePoke(root); return poke(root, name, content) }

	now, noteNow := d.Now, reg("Now")
	d.Now = func() time.Time { noteNow(""); return now() }

	reb, noteReb := d.GitRebase, reg("GitRebase")
	d.GitRebase = func(wt, onto string) error { noteReb(wt); return reb(wt, onto) }

	abort, noteAbort := d.GitRebaseAbort, reg("GitRebaseAbort")
	d.GitRebaseAbort = func(wt string) error { noteAbort(wt); return abort(wt) }

	ff, noteFF := d.GitFFMerge, reg("GitFFMerge")
	d.GitFFMerge = func(wt, br string) error { noteFF(wt); return ff(wt, br) }

	upd, noteUpd := d.GitUpdateRef, reg("GitUpdateRef")
	d.GitUpdateRef = func(wt, ref, sha string) error { noteUpd(wt); return upd(wt, ref, sha) }

	cas, noteCAS := d.GitUpdateRefCAS, reg("GitUpdateRefCAS")
	d.GitUpdateRefCAS = func(wt, ref, newSHA, oldSHA string) error {
		noteCAS(wt)
		return cas(wt, ref, newSHA, oldSHA)
	}

	return d, wrapped
}

// TestTracedDepsWrapsEverySeam is the F3 guard: the trace is only as total as
// its wrapping, and an unwrapped mutating seam silently empties every
// parent-untouched assertion in this file.
func TestTracedDepsWrapsEverySeam(t *testing.T) {
	_, wrapped := tracedDeps(&seamTrace{})
	var missing []string
	for seam := range seamKinds {
		if !wrapped[seam] {
			missing = append(missing, seam)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("seams classified in seamKinds but NOT wrapped by tracedDeps: %v\n"+
			"An unwrapped seam is invisible to mutationsAgainst, so every parent-untouched\n"+
			"assertion in this file would silently stop covering it.", missing)
	}
	for seam := range wrapped {
		if _, ok := seamKinds[seam]; !ok {
			t.Errorf("tracedDeps wraps %q, which seamKinds does not classify", seam)
		}
	}
}

// TestMerge_HappyPath_OnlyFFMergeMutatesTheParent is the invariant that
// survives someone re-adding a reset in 2027. It does not name the seams that
// must not be called — it names the ONE that may be.
func TestMerge_HappyPath_OnlyFFMergeMutatesTheParent(t *testing.T) {
	tr := &seamTrace{}
	deps, _ := tracedDeps(tr)
	cfg := newTestConfig()

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("merge: %v", err)
	}

	got := tr.mutationsAgainst(cfg.ParentWorktree)
	if len(got) != 1 || got[0] != "GitFFMerge" {
		t.Errorf("mutating seams against the parent worktree = %v, want exactly [GitFFMerge]\n"+
			"full trace: %v", got, tr.calls)
	}
}

// TestMerge_ValidateFailure_NeverMutatesTheParent is AC 1 at the unit level:
// ZERO mutations of the parent, not "the parent was put back".
//
// The absence assertions alone are NOT enough to identify this path, and that
// was a real defect in the first cut of this test. "No parent mutation" and "no
// ff" are equally true of the ff-precondition-false path and the parent-moved
// path — so all three could swap error returns and every test would stay green.
// Worse, they are true of ANY early refusal, including one where validation
// never ran at all. Hence the two arrival assertions: the failure must BE the
// validate failure, and validate must actually have executed.
func TestMerge_ValidateFailure_NeverMutatesTheParent(t *testing.T) {
	tr := &seamTrace{}
	deps, _ := tracedDeps(tr)
	cfg := newTestConfig()
	var validateCalls []string
	inner := deps.RunTestsStreaming
	deps.RunTestsStreaming = func(ctx context.Context, dir, cmd string, sink func(string)) (string, error) {
		_, _ = inner(ctx, dir, cmd, sink) // keep the trace entry
		validateCalls = append(validateCalls, dir)
		return "FAILED: boom", errTestValidate
	}

	_, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected the merge to fail on validation")
	}

	// ARRIVAL, asserted before anything else: without these, everything below
	// is satisfied by a refusal that happened for an unrelated reason.
	if !errors.Is(err, errTestValidate) {
		t.Fatalf("the merge did not fail on VALIDATION; every assertion below would be about the wrong path.\n got: %v", err)
	}
	if len(validateCalls) != 1 {
		t.Fatalf("validate ran %d times, want exactly once: %v", len(validateCalls), validateCalls)
	}

	if got := tr.mutationsAgainst(cfg.ParentWorktree); len(got) != 0 {
		t.Errorf("the parent worktree was mutated by %v during a merge that failed validation; want none\n"+
			"full trace: %v", got, tr.calls)
	}
	for _, c := range tr.calls {
		if strings.Contains(c, "GitFFMerge") {
			t.Errorf("GitFFMerge was invoked despite validation failing: %v", tr.calls)
		}
	}
	// MUTUAL EXCLUSION, the other half of "distinguishable at the assertion
	// level": this path must not borrow either sibling's diagnosis. Asserted
	// in both directions here and in refmove_test.go, because a shared phrase
	// is what makes two paths indistinguishable in practice.
	for _, foreign := range []string{ffPredicatePhrase, parentMovedPhrase} {
		if strings.Contains(err.Error(), foreign) {
			t.Errorf("the validate-failure error carries another path's diagnosis %q: %v", foreign, err)
		}
	}
}

// TestMerge_ValidateRunsInTheAgentWorktree pins the relocation itself.
func TestMerge_ValidateRunsInTheAgentWorktree(t *testing.T) {
	cfg := newTestConfig()
	if cfg.AgentWorktree == cfg.ParentWorktree {
		t.Fatal("fixture precondition: the two worktrees must differ, or this test is vacuous")
	}

	var dirs []string
	deps := newTestDeps()
	deps.RunTestsStreaming = func(ctx context.Context, dir, cmd string, sink func(string)) (string, error) {
		dirs = append(dirs, dir)
		return "ok", nil
	}
	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("merge: %v", err)
	}

	if len(dirs) != 1 {
		t.Fatalf("validate ran %d times, want once: %v", len(dirs), dirs)
	}
	// Both halves, so a fixture where the paths coincide cannot make this pass.
	if dirs[0] != cfg.AgentWorktree {
		t.Errorf("validate ran in %q, want the AGENT worktree %q", dirs[0], cfg.AgentWorktree)
	}
	if dirs[0] == cfg.ParentWorktree {
		t.Errorf("validate ran in the PARENT worktree %q: the parent must not be validated, it is not yet the tree being merged", cfg.ParentWorktree)
	}
}

// TestMerge_ValidateStrictlyPrecedesFFMerge is the assertion a reordering
// regression cannot get past by editing an expected slice. The guards live
// INSIDE the seams and fire on the wrong interleaving, in both directions.
func TestMerge_ValidateStrictlyPrecedesFFMerge(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	var validateRan, ffCalled bool
	deps.RunTestsStreaming = func(ctx context.Context, dir, cmd string, sink func(string)) (string, error) {
		if ffCalled {
			t.Error("validate ran AFTER the ff-merge: the parent was mutated before the tree was known good")
		}
		validateRan = true
		return "ok", nil
	}
	innerFF := deps.GitFFMerge
	deps.GitFFMerge = func(wt, br string) error {
		if !validateRan {
			t.Error("ff-merge ran BEFORE validation: this is the ordering QUM-1087 exists to invert")
		}
		ffCalled = true
		// Chained, not replaced: the fixture's GitFFMerge is what models the
		// ref move, and without it the engine's post-ff equality check fails
		// and this test dies on an unrelated error.
		return innerFF(wt, br)
	}

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !validateRan || !ffCalled {
		t.Fatalf("precondition: both steps must run (validateRan=%v ffCalled=%v)", validateRan, ffCalled)
	}
}

// TestMerge_ParentMutationRecorderCanFire is the POSITIVE CONTROL for the two
// mutationsAgainst assertions above. They are absence claims, and an absence
// claim made with a broken recorder passes for free.
func TestMerge_ParentMutationRecorderCanFire(t *testing.T) {
	tr := &seamTrace{}
	deps, _ := tracedDeps(tr)
	cfg := newTestConfig()

	// Deliberately wire a mutating seam at the parent, the way a re-added
	// rollback would.
	inner := deps.GitUpdateRef
	deps.GitUpdateRef = func(wt, ref, sha string) error {
		if err := inner(wt, ref, sha); err != nil {
			return err
		}
		return deps.GitUpdateRefCAS(cfg.ParentWorktree, "refs/heads/main", "dead", "beef")
	}

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("merge: %v", err)
	}
	got := tr.mutationsAgainst(cfg.ParentWorktree)
	if len(got) == 0 {
		t.Error("the recorder saw NO parent mutation despite one being wired in — every parent-untouched assertion in this file is vacuous")
	}
}

// ---------------------------------------------------------------------------
// Checkpoint coverage.
//
// QUM-1087 deleted internal/merge's ENTIRE checkpoint test suite as collateral
// while removing the squash — TestMerge_EmitsCheckpointSequence,
// _CheckpointEmitsValidateEndedOnFailure, _NilCheckpointSafe,
// _EmptyValidateCmd_SkipsWithWarning and _ValidateCmd_PassedToRunTests all went
// with tests that named squash seams. Eleven cpMerge sites remained with zero
// coverage, and the commit message claimed the checkpoint sequence as one of
// three ordering layers — a claim that was false when written. Restored here,
// retargeted at the new flow.
// ---------------------------------------------------------------------------

// TestMerge_EmitsCheckpointSequence is the third ordering layer (with
// TestMerge_StepOrdering's deps trace and TestMerge_ValidateStrictlyPrecedesFFMerge's
// in-seam guards). Ordered equality, so a reordering shows up as a diff and not
// merely as a missing name.
func TestMerge_EmitsCheckpointSequence(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	var steps []string
	deps.Checkpoint = func(step string, kv ...any) { steps = append(steps, step) }
	// Emit one line, so merge.validate-line is exercised rather than assumed
	// absent. That checkpoint is how live validate output reaches the TUI's
	// validate popup (QUM-588); a sequence assertion that never sees it would
	// not notice it disappearing.
	deps.RunTestsStreaming = func(ctx context.Context, dir, cmd string, sink func(string)) (string, error) {
		sink("VALIDATE-OUTPUT-LINE")
		return "ok", nil
	}

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("merge: %v", err)
	}

	want := []string{
		"merge.lock-acquired",
		"merge.premerge-refs-written",
		"merge.rebased",
		"merge.ff-precondition-ok",
		"merge.validate-started",
		"merge.validate-line",
		"merge.validate-ended",
		"merge.ff-merged",
		"merge.ff-verified",
		"merge.poke-written",
	}
	if len(steps) != len(want) {
		t.Fatalf("got %d checkpoints, want %d:\n got: %v\nwant: %v", len(steps), len(want), steps, want)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("checkpoint %d = %q, want %q (full: %v)", i, steps[i], want[i], steps)
		}
	}
	// The ordering claim, stated as its own assertion so it cannot be lost by
	// someone regenerating the slice above from actual output.
	vi, fi := indexOfStep(steps, "merge.validate-ended"), indexOfStep(steps, "merge.ff-merged")
	if vi < 0 || fi < 0 || vi > fi {
		t.Errorf("validate-ended (%d) must precede ff-merged (%d): %v", vi, fi, steps)
	}
	// And no squash checkpoint may reappear.
	for _, s := range steps {
		if strings.Contains(s, "squash") {
			t.Errorf("a squash checkpoint was emitted (%q); the engine creates no commit", s)
		}
	}
}

func indexOfStep(steps []string, want string) int {
	for i, s := range steps {
		if s == want {
			return i
		}
	}
	return -1
}

// TestMerge_CheckpointEmitsValidateEndedOnFailure — the failure leg must be
// observable too, with its exit recorded. This is what lets an operator tell a
// validate failure from an ff refusal in the call log, and it is the
// assertion-level distinction between those two paths at the checkpoint layer.
func TestMerge_CheckpointEmitsValidateEndedOnFailure(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	deps.RunTestsStreaming = func(ctx context.Context, dir, cmd string, sink func(string)) (string, error) {
		return "boom", errTestValidate
	}

	var pairs []string
	deps.Checkpoint = func(step string, kv ...any) {
		entry := step
		for i := 0; i+1 < len(kv); i += 2 {
			if k, ok := kv[i].(string); ok && k == "exit" {
				entry += " exit=" + fmt.Sprint(kv[i+1])
			}
		}
		pairs = append(pairs, entry)
	}

	if _, err := Merge(context.Background(), cfg, deps); err == nil {
		t.Fatal("expected validation to fail the merge")
	}

	var sawEndedNonzero bool
	for _, p := range pairs {
		if p == "merge.validate-ended exit=nonzero" {
			sawEndedNonzero = true
		}
		if strings.HasPrefix(p, "merge.ff-merged") {
			t.Errorf("ff-merged was checkpointed despite validation failing: %v", pairs)
		}
	}
	if !sawEndedNonzero {
		t.Errorf("want a merge.validate-ended checkpoint with exit=nonzero, got: %v", pairs)
	}
}

// TestMerge_NilCheckpointSafe — Checkpoint is documented-optional and cpMerge
// nil-guards it. With 11 call sites, one unguarded site would panic a real merge.
func TestMerge_NilCheckpointSafe(t *testing.T) {
	deps := newTestDeps()
	deps.Checkpoint = nil
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil Checkpoint panicked: %v", r)
		}
	}()
	if _, err := Merge(context.Background(), newTestConfig(), deps); err != nil {
		t.Fatalf("merge: %v", err)
	}
}

// TestMerge_EmptyValidateCmd_StillFFMergesAndWarns covers the branch that had NO
// test at all after QUM-1087: no validate command configured, so the engine
// fast-forwards with NO VALIDATION.
//
// That is the QUM-997 shape — a fallback that proceeds without asserting
// anything — and it is legitimate here only because it WARNS. So the warning is
// asserted, not just the ff. A silent version of this branch would land
// unvalidated work while looking identical to a validated merge.
func TestMerge_EmptyValidateCmd_StillFFMergesAndWarns(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	cfg.ValidateCmd = "" // configured nowhere
	cfg.NoValidate = false
	var stderr bytes.Buffer
	deps.Stderr = &stderr

	var validateRan, ffCalled bool
	deps.RunTestsStreaming = func(ctx context.Context, dir, cmd string, sink func(string)) (string, error) {
		validateRan = true
		return "", nil
	}
	inner := deps.GitFFMerge
	deps.GitFFMerge = func(wt, rev string) error { ffCalled = true; return inner(wt, rev) }

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if validateRan {
		t.Error("no validate command is configured; nothing should have been run")
	}
	if !ffCalled {
		t.Error("the merge must still land when no validate command is configured")
	}
	if !strings.Contains(stderr.String(), "WARNING") {
		t.Errorf("skipping validation must WARN — a silent skip lands unvalidated work looking like a validated merge. stderr: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "config set validate") {
		t.Errorf("the warning must say how to fix it, got: %q", stderr.String())
	}
}

// TestMerge_ValidateCmdAndDirArePassedThrough pins what the engine hands the
// validate runner: the configured command, and the AGENT worktree.
func TestMerge_ValidateCmdAndDirArePassedThrough(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	cfg.ValidateCmd = "VALIDATE-CMD-SENTINEL"

	var gotCmd, gotDir string
	deps.RunTestsStreaming = func(ctx context.Context, dir, cmd string, sink func(string)) (string, error) {
		gotCmd, gotDir = cmd, dir
		sink("a line")
		return "ok", nil
	}
	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if gotCmd != "VALIDATE-CMD-SENTINEL" {
		t.Errorf("validate command = %q, want the configured one", gotCmd)
	}
	if gotDir != cfg.AgentWorktree {
		t.Errorf("validate dir = %q, want the agent worktree %q", gotDir, cfg.AgentWorktree)
	}
}

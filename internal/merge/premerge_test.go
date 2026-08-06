package merge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// premergeRestoredPhrase is the claim the rebase-failure path makes when it
// actually restored the branch. Asserted PRESENT on the CAS-success path and
// ABSENT on the CAS-refused path, so neither assertion can pass vacuously —
// a message reused across both legs turns one of them red.
const premergeRestoredPhrase = "was restored to its pre-merge tip"

// refWrite records one GitUpdateRef call.
type refWrite struct{ worktree, ref, newSHA string }

// casWrite records one GitUpdateRefCAS call.
type casWrite struct{ worktree, ref, newSHA, oldSHA string }

// recordRefWrites installs recording fakes for the two ref seams and returns
// pointers to the slices they append to.
func recordRefWrites(deps *Deps) (*[]refWrite, *[]casWrite) {
	var refs []refWrite
	var cas []casWrite
	deps.GitUpdateRef = func(worktree, ref, newSHA string) error {
		refs = append(refs, refWrite{worktree, ref, newSHA})
		return nil
	}
	deps.GitUpdateRefCAS = func(worktree, ref, newSHA, oldSHA string) error {
		cas = append(cas, casWrite{worktree, ref, newSHA, oldSHA})
		return nil
	}
	return &refs, &cas
}

// TestMerge_PremergeRefsWrittenBeforeFirstMutation is the QUM-1090 ordering
// gate: both recovery refs must exist before the merge mutates anything.
// GitResetSoft is the first mutation, so "before reset-soft" is the whole
// claim. Negative control: moving the ref-write block below GitResetSoft (or
// deleting it) must turn this red.
func TestMerge_PremergeRefsWrittenBeforeFirstMutation(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()

	var order []string
	deps.GitUpdateRef = func(worktree, ref, newSHA string) error {
		order = append(order, "update-ref:"+ref)
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

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resetIdx := -1
	var refIdx []int
	for i, op := range order {
		switch {
		case op == "reset-soft":
			resetIdx = i
		case strings.HasPrefix(op, "update-ref:"):
			refIdx = append(refIdx, i)
		}
	}
	if resetIdx < 0 {
		t.Fatalf("reset-soft never called; order=%v", order)
	}
	if len(refIdx) != 2 {
		t.Fatalf("want exactly 2 premerge ref writes, got %d; order=%v", len(refIdx), order)
	}
	for _, i := range refIdx {
		if i > resetIdx {
			t.Errorf("premerge ref write at index %d is AFTER the first mutation (reset-soft at %d); order=%v", i, resetIdx, order)
		}
	}
}

// TestMerge_PremergeRefNames_AgentClockAndTips pins the ref names and the
// SHA each one records. The agent ref must get the AGENT tip and the parent
// ref the PARENT tip — newTestDeps returns different SHAs per worktree so
// swapping them in the impl turns this red.
func TestMerge_PremergeRefNames_AgentClockAndTips(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	refs, cas := recordRefWrites(deps)

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Compared as a set keyed on ref name: the ORDER of the two writes is
	// not a property worth pinning, the names and SHAs are.
	want := map[string]refWrite{
		testPremergeBase + "/agent":  {cfg.SprawlRoot, testPremergeBase + "/agent", "bbb222"},
		testPremergeBase + "/parent": {cfg.SprawlRoot, testPremergeBase + "/parent", "ppp444"},
	}
	if len(*refs) != len(want) {
		t.Fatalf("got %d ref writes, want %d: %+v", len(*refs), len(want), *refs)
	}
	for _, got := range *refs {
		w, ok := want[got.ref]
		if !ok {
			t.Errorf("unexpected ref write %+v", got)
			continue
		}
		if got != w {
			t.Errorf("ref write %+v, want %+v", got, w)
		}
		delete(want, got.ref)
	}
	for name := range want {
		t.Errorf("missing ref write for %q", name)
	}
	// A successful merge must never CAS the agent branch — doing so would
	// rewind it to its pre-merge tip after the merge landed.
	if len(*cas) != 0 {
		t.Errorf("successful merge must not CAS the agent branch, got %+v", *cas)
	}
}

// TestMerge_NoOp_WritesNoPremergeRefs — a merge with nothing to merge mutates
// nothing, so it must not leave a recovery ref behind either. Negative
// control: hoisting the ref block above the no-op return turns this red.
func TestMerge_NoOp_WritesNoPremergeRefs(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	deps.GitMergeBase = func(repoRoot, a, b string) (string, error) { return "same-sha", nil }
	deps.GitRevParseHead = func(worktree string) (string, error) { return "same-sha", nil }
	refs, _ := recordRefWrites(deps)

	res, err := Merge(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.WasNoOp {
		t.Fatal("precondition: expected a no-op merge")
	}
	if len(*refs) != 0 {
		t.Errorf("no-op merge must write no premerge refs, got %+v", *refs)
	}
}

// TestMerge_DryRun_WritesNoPremergeRefs — dry-run mutates nothing and must
// not write refs either.
func TestMerge_DryRun_WritesNoPremergeRefs(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	cfg.DryRun = true
	refs, _ := recordRefWrites(deps)

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*refs) != 0 {
		t.Errorf("dry-run must write no premerge refs, got %+v", *refs)
	}
}

// TestMerge_PremergeRefWriteFailure_AbortsBeforeMutation — if the safety net
// cannot be written, the merge must not proceed without it. Negative
// control: swallowing the ref-write error turns this red.
func TestMerge_PremergeRefWriteFailure_AbortsBeforeMutation(t *testing.T) {
	for _, failOn := range []int{1, 2} {
		t.Run(fmt.Sprintf("failOnWrite%d", failOn), func(t *testing.T) {
			deps := newTestDeps()
			cfg := newTestConfig()

			n := 0
			deps.GitUpdateRef = func(worktree, ref, newSHA string) error {
				n++
				if n == failOn {
					return fmt.Errorf("disk full")
				}
				return nil
			}
			var mutated bool
			deps.GitResetSoft = func(worktree, ref string) error { mutated = true; return nil }
			deps.GitCommit = func(worktree, message string) (string, error) { mutated = true; return "ccc333", nil }

			_, err := Merge(context.Background(), cfg, deps)
			if err == nil {
				t.Fatal("expected an error when the premerge ref write fails")
			}
			if mutated {
				t.Error("merge must not mutate the agent branch when the premerge ref write failed")
			}
			if !strings.Contains(err.Error(), "refs/sprawl/premerge/") {
				t.Errorf("error should name the ref it failed to write, got: %v", err)
			}
		})
	}
}

// TestMerge_RebaseFailure_RestoresAgentBranchViaCAS — QUM-1090 part B: the
// tool restores the branch itself instead of printing a manual reset for a
// human to run. Negative control: deleting the CAS call turns this red.
func TestMerge_RebaseFailure_RestoresAgentBranchViaCAS(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	_, cas := recordRefWrites(deps)

	var order []string
	deps.GitRebase = func(worktree, onto string) error {
		return fmt.Errorf("CONFLICT (content): merge conflict in main.go")
	}
	deps.GitUpdateRefCAS = func(worktree, ref, newSHA, oldSHA string) error {
		order = append(order, "cas")
		*cas = append(*cas, casWrite{worktree, ref, newSHA, oldSHA})
		return nil
	}
	// After the abort the agent branch sits at the squash commit. Modelled
	// as a state transition rather than a call counter so the fixture does
	// not silently pin how many times (or in what order) the impl reads
	// HEAD.
	aborted := false
	deps.GitRebaseAbort = func(worktree string) error {
		order = append(order, "rebase-abort")
		aborted = true
		return nil
	}
	deps.GitRevParseHead = func(worktree string) (string, error) {
		if worktree == "/worktree/parent" {
			return "ppp444", nil
		}
		if aborted {
			return "squash999", nil // post-abort tip
		}
		return "bbb222", nil
	}

	_, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected error from rebase conflict")
	}
	if len(*cas) != 1 {
		t.Fatalf("want exactly 1 CAS restore, got %d: %+v", len(*cas), *cas)
	}
	got := (*cas)[0]
	if got.ref != "refs/heads/sprawl/test-agent" {
		t.Errorf("CAS ref = %q, want refs/heads/sprawl/test-agent", got.ref)
	}
	if got.newSHA != "bbb222" {
		t.Errorf("CAS newSHA = %q, want the pre-squash tip bbb222", got.newSHA)
	}
	if got.oldSHA != "squash999" {
		t.Errorf("CAS oldSHA = %q, want the observed post-abort tip squash999", got.oldSHA)
	}
	if len(order) != 2 || order[0] != "rebase-abort" || order[1] != "cas" {
		t.Errorf("want rebase-abort then cas, got %v", order)
	}
	if !strings.Contains(err.Error(), "bbb222") {
		t.Errorf("error should name the restored tip, got: %v", err)
	}
	if !strings.Contains(err.Error(), premergeRestoredPhrase) {
		t.Errorf("error must state the branch was restored, got: %v", err)
	}
	// The whole point of part B: the tool did it, so it must no longer tell
	// a human to run a manual reset. Without this, the test cannot tell
	// "restored" from "printed instructions" — both contain bbb222.
	if strings.Contains(err.Error(), "reset --hard") {
		t.Errorf("restored path must not print a manual reset --hard instruction, got: %v", err)
	}
}

// TestMerge_RebaseFailure_CASRefused_PrintsPremergeRef — when the branch is
// not where we expect (concurrent mutation, or the QUM-1088 stale-branch
// shape), we must NOT force the ref. Negative control: making the refused
// branch reuse the restored message turns this red.
func TestMerge_RebaseFailure_CASRefused_PrintsPremergeRef(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	refs, _ := recordRefWrites(deps)
	deps.GitRebase = func(worktree, onto string) error { return fmt.Errorf("CONFLICT") }
	deps.GitUpdateRefCAS = func(worktree, ref, newSHA, oldSHA string) error {
		return fmt.Errorf("cannot lock ref %q: is at deadbeef but expected %s", ref, oldSHA)
	}

	_, err := Merge(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected error from rebase conflict")
	}
	msg := err.Error()
	if !strings.Contains(msg, testPremergeBase+"/agent") {
		t.Errorf("CAS-refused error must name the premerge agent ref, got: %v", err)
	}
	if strings.Contains(msg, premergeRestoredPhrase) {
		t.Errorf("CAS-refused error must NOT claim the branch was restored, got: %v", err)
	}
	// "print the ref INSTEAD OF mutating": a non-CAS force-update fallback
	// would satisfy every assertion above, so pin the write count.
	if len(*refs) != 2 {
		t.Errorf("CAS refusal must not force the branch ref; unconditional ref writes = %+v", *refs)
	}
}

// TestMerge_ValidateFailure_DoesNotRestoreAgentBranch pins the deliberate
// asymmetry: a rebase conflict means the merge did not happen (restore), a
// validate failure means it happened and was rejected on quality — the
// squashed+rebased branch is a legitimate state to iterate on, and the
// premerge refs already make recovery a one-liner. Negative control: adding
// a CAS restore on the validate path turns this red.
func TestMerge_ValidateFailure_DoesNotRestoreAgentBranch(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	refs, cas := recordRefWrites(deps)
	deps.RunTestsStreaming = func(ctx context.Context, dir, command string, sink func(string)) (string, error) {
		return "FAIL", fmt.Errorf("tests failed")
	}

	if _, err := Merge(context.Background(), cfg, deps); err == nil {
		t.Fatal("expected error from validate failure")
	}
	if len(*cas) != 0 {
		t.Errorf("validate failure must not CAS the agent branch, got %+v", *cas)
	}
	// A non-CAS force-update restore would also violate the decision.
	if len(*refs) != 2 {
		t.Errorf("validate failure must write only the 2 premerge refs, got %+v", *refs)
	}
}

// TestMerge_FailureErrorsNameRecoveryRefs — AC: merge output names the refs
// on the failure paths. Negative control: dropping the suffix from either
// error turns the matching subtest red.
func TestMerge_FailureErrorsNameRecoveryRefs(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(*Deps)
	}{
		{"rebase-failure", func(d *Deps) {
			d.GitRebase = func(worktree, onto string) error { return fmt.Errorf("CONFLICT") }
		}},
		{"validate-failure", func(d *Deps) {
			d.RunTestsStreaming = func(ctx context.Context, dir, command string, sink func(string)) (string, error) {
				return "FAIL", fmt.Errorf("tests failed")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newTestDeps()
			cfg := newTestConfig()
			tc.breakIt(deps)

			_, err := Merge(context.Background(), cfg, deps)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range []string{testPremergeBase + "/agent", testPremergeBase + "/parent"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must name %q, got: %v", want, err)
				}
			}
		})
	}
}

// TestMerge_SuccessStderrNamesPremergeRefs — AC: merge output names the refs
// on success too. Negative control: dropping the Fprintf turns this red.
func TestMerge_SuccessStderrNamesPremergeRefs(t *testing.T) {
	deps := newTestDeps()
	cfg := newTestConfig()
	var stderr bytes.Buffer
	deps.Stderr = &stderr

	if _, err := Merge(context.Background(), cfg, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := stderr.String()
	for _, want := range []string{testPremergeBase + "/agent", testPremergeBase + "/parent"} {
		if !strings.Contains(out, want) {
			t.Errorf("success stderr must name %q, got: %q", want, out)
		}
	}
}

// TestRealDeps_NowIsTheRealClock — Now is a mandatory seam (there is no
// nil-tolerant default; Merge dereferences it), so the production binding
// must be a real clock. A time.Time{} zero value would also format and parse
// cleanly under PremergeTSLayout while making `sprawl gc` classify every ref
// as stale on its first run, so bound recency rather than just parseability.
// Negative control: bind Now to `func() time.Time { return time.Time{} }` in
// RealDeps and this goes red.
func TestRealDeps_NowIsTheRealClock(t *testing.T) {
	d := RealDeps(io.Discard)
	if d.Now == nil {
		t.Fatal("RealDeps left Now nil")
	}
	got := d.Now()
	if age := time.Since(got); age < 0 || age > time.Minute {
		t.Errorf("RealDeps.Now() = %v (%v from now); want the real clock", got, age)
	}
	// Round-trips through the ref name without losing recency.
	ref, _ := premergeRefs("finn", got)
	parsed, err := time.Parse(PremergeTSLayout, strings.Split(ref, "/")[4])
	if err != nil {
		t.Fatalf("ref timestamp does not parse with PremergeTSLayout: %v", err)
	}
	if age := time.Since(parsed); age < 0 || age > time.Minute {
		t.Errorf("ref timestamp %v is %v from now; want the real clock", parsed, age)
	}
}

// TestPremergeRefs_UsesUTC — PremergeTSLayout's trailing "Z" is a LITERAL,
// not a zone specifier, so dropping .UTC() still yields a Z-suffixed name
// carrying local wall-clock time and `sprawl gc` would age the ref by the
// offset. Negative control: removing .UTC() from premergeRefs turns this red.
func TestPremergeRefs_UsesUTC(t *testing.T) {
	loc := time.FixedZone("UTC+9", 9*60*60)
	ts := time.Date(2026, 8, 6, 14, 0, 0, 0, loc) // == 05:00:00Z
	agentRef, _ := premergeRefs("finn", ts)
	if want := "refs/sprawl/premerge/finn/20260806T050000.000Z/agent"; agentRef != want {
		t.Errorf("agent ref = %q, want %q (timestamp must be normalised to UTC)", agentRef, want)
	}
}

// TestPremergeRefs_MillisecondsAvoidSameSecondCollision gates the only
// justification for PremergeTSLayout's sub-second precision: two merges of
// one agent inside the same second must not overwrite each other's recovery
// pair. Negative control: changing the layout to second precision turns this
// red.
func TestPremergeRefs_MillisecondsAvoidSameSecondCollision(t *testing.T) {
	t0 := time.Date(2026, 8, 6, 5, 33, 32, 100000000, time.UTC)
	t1 := t0.Add(time.Millisecond)
	a0, p0 := premergeRefs("finn", t0)
	a1, p1 := premergeRefs("finn", t1)

	seen := map[string]bool{}
	for _, r := range []string{a0, p0, a1, p1} {
		if seen[r] {
			t.Errorf("ref %q collides — two merges in the same second overwrite one another's recovery pair", r)
		}
		seen[r] = true
	}
	if len(seen) != 4 {
		t.Errorf("want 4 distinct refs, got %d: %v", len(seen), seen)
	}
}

// TestPremergeRefs_RoundTrip — the ref name is the only age source gc has,
// so it must parse back exactly. Negative control: changing the layout in
// one place turns this red.
func TestPremergeRefs_RoundTrip(t *testing.T) {
	ts := time.Date(2026, 8, 6, 5, 33, 32, 760000000, time.UTC)
	agentRef, parentRef := premergeRefs("finn", ts)

	if agentRef != "refs/sprawl/premerge/finn/20260806T053332.760Z/agent" {
		t.Errorf("agent ref = %q", agentRef)
	}
	if parentRef != "refs/sprawl/premerge/finn/20260806T053332.760Z/parent" {
		t.Errorf("parent ref = %q", parentRef)
	}
	got, err := time.Parse(PremergeTSLayout, strings.Split(agentRef, "/")[4])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.Equal(ts) {
		t.Errorf("round-trip = %v, want %v", got, ts)
	}
}

func TestRealDeps_NoNilSeams(t *testing.T) {
	d := RealDeps(io.Discard)
	missing, checked := NilSeams(d)
	if len(missing) != 0 {
		t.Errorf("RealDeps left seams nil: %v", missing)
	}
	if checked < MinDepsSeams {
		t.Errorf("NilSeams examined %d seams, want >= %d; the walk looks broken", checked, MinDepsSeams)
	}
	if d.Stderr == nil {
		t.Error("RealDeps left Stderr nil")
	}
}

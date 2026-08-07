package githooks

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// QUM-1156 — the leak guard must be self-checking: it may not report clean
// unless its own positive controls fired in the SAME invocation, and it may not
// report clean when it silently failed to load part of the forbidden-terms list.
//
// These tests live in their own file so leak_guard_test.go stays an untouched
// regression baseline for the pre-QUM-1156 contract (notably
// TestLeakGuard_NoListNoOp, which pins the documented :68 fail-open that AC5
// keeps).
//
// Every term used here is a SYNTHETIC placeholder. This repo is public.
//
// One design constraint is imposed here rather than discovered: the controls
// must run against a SYNTHETIC subject that the guard supplies itself, not
// against the real diff or the real tree. Three tests pin it from different
// sides — an empty staged diff must still fire the diff classes, a clean tree
// must still fire the tree classes, and user content that happens to contain a
// control placeholder must not trip a control's negative leg. Deriving the
// controls from the scan subject would make them vacuous exactly when the
// subject is empty, which is when a vacuous scan is most dangerous.

// Exit-code contract under test (mirrors the script header).
const (
	exitClean     = 0
	exitViolation = 1
	exitUsage     = 2
	exitSelfCheck = 3
)

// Control terms the guard plants for its own in-run probes. These are DERIVED
// from the guard rather than guessed: the guard names them in its success
// report, and TestLeakGuard_ControlTermsAreTheOnesTheGuardUses pins that these
// constants are the strings it actually uses. Without that coupling the
// contamination test below would stage two irrelevant strings and pass forever.
const (
	controlCI    = "SPRAWL-LEAKPROBE-CONTROL-CI"
	controlExact = "SPRAWL-LEAKPROBE-CONTROL-EXACT"
)

// Probe classes the guard must report a nonzero fired-count for, per mode. A
// control that exercises a code path this invocation did not take is not
// evidence about this invocation, so the required set is mode-scoped.
var (
	stagedClasses = []string{
		"list-parse",
		"diff-linewise-ci", "diff-linewise-exact",
		"diff-dewrapped-ci", "diff-dewrapped-exact",
	}
	allClasses = []string{
		"list-parse",
		"tree-linewise-ci", "tree-linewise-exact",
		"tree-dewrapped-ci", "tree-dewrapped-exact",
	}
)

// classFired is the per-class ledger line the success report must print. Both
// legs are pinned: pos=1 proves the control is aimed at the right subject, and
// neg=0 proves it can still DISCRIMINATE. A control that matches everything
// satisfies the positive leg forever, which is the broken-probe failure this
// whole issue catalogues.
//
// pos is pinned at exactly 1, not >=1, on purpose: it forces each class to run
// exactly one control on its own synthetic subject, independent of how many
// real terms the list happens to hold.
func classFired(cls string) string { return cls + ": pos=1 neg=0" }

// termsLoaded is the AC2 success line. Defined once so the format is pinned in
// one place rather than spelled out in eight assertions.
func termsLoaded(loaded, total int) string {
	return fmt.Sprintf("terms loaded: %d/%d", loaded, total)
}

// wantTermsLoaded asserts the AC2 line with a right word boundary, so
// "terms loaded: 1/1" cannot be satisfied by "terms loaded: 1/10".
func wantTermsLoaded(t *testing.T, where, out string, loaded, total int) {
	t.Helper()
	re := regexp.MustCompile(fmt.Sprintf(`terms loaded: %d/%d\b`, loaded, total))
	if !re.MatchString(out) {
		t.Errorf("want %q; %s: %q", termsLoaded(loaded, total), where, out)
	}
}

// wantOffendingLines asserts AC1's line numbers without pinning cosmetic
// choices: singular vs plural label, or the exact separator.
func wantOffendingLines(t *testing.T, out string, nums ...int) {
	t.Helper()
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf(`%d`, n)
	}
	re := regexp.MustCompile(`lines?:\s*` + strings.Join(parts, `,\s*`) + `\b`)
	if !re.MatchString(out) {
		t.Errorf("want the offending physical line number(s) %v named; output: %q", nums, out)
	}
}

// runGuardCode execs the guard and returns combined output plus the exit code.
// Unlike runGuard it distinguishes exit 1 from exit 3, which is the whole point
// of the AC9 contract: "controls did not fire" must never be confusable with
// either "clean" or "violation found".
func runGuardCode(t *testing.T, repo string, env []string, args ...string) (string, int) {
	t.Helper()
	out, _, code := runGuardSplit(t, repo, env, args...)
	return out, code
}

// runGuardSplit keeps stdout and stderr separate. The success report prints on
// EVERY commit (scripts/pre-commit) and on every `make validate` (leak-scan),
// so which stream it lands on is part of the contract, not an accident.
func runGuardSplit(t *testing.T, repo string, env []string, args ...string) (combined, stdout string, code int) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	cmd := exec.Command(leakGuardPath(t), args...)
	cmd.Dir = repo
	cmd.Env = env
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code = 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("guard failed to run: %v; stderr: %s", err, errBuf.String())
		}
		code = ee.ExitCode()
	}
	return outBuf.String() + errBuf.String(), outBuf.String(), code
}

// listEnv is the common case: a forbidden-terms list plus a hermetic env.
func listEnv(list string, extra ...string) []string {
	return baseEnv(append([]string{"SPRAWL_FORBIDDEN_TERMS_FILE=" + list}, extra...)...)
}

// -----------------------------------------------------------------------------
// AC1 — terms-loaded count must match the list, and a shortfall must FAIL.
// -----------------------------------------------------------------------------

// shortfallList is the issue's worked example: 7 non-blank, non-comment entries
// of which 3 are malformed, so 4 load. Physical line numbers of the malformed
// entries are 4, 5 and 6 (line 1 is a comment, line 2 is blank).
func shortfallList(t *testing.T) string {
	t.Helper()
	return writeList(t,
		"# leading comment",   // 1 — not counted
		"",                    // 2 — not counted
		"a:ci:AAAPLACEHOLDER", // 3 — loads
		"MALFORMEDNOCOLON",    // 4 — malformed: no ':' at all
		"c:ci",                // 5 — malformed: only two fields
		"d:ci:",               // 6 — malformed: empty term
		"e:ci:EEEPLACEHOLDER", // 7 — loads
		"f:exact:FFFPLACE",    // 8 — loads
		"g:ci:GGGPLACE",       // 9 — loads
	)
}

// TestLeakGuard_MalformedEntryShortfallIsFatal is the load-bearing red. Today a
// 4-of-7 load is byte-identical to a 7-of-7 load: empty output, exit 0. That is
// the "dishonest pass" — coverage claimed that was never had.
func TestLeakGuard_MalformedEntryShortfallIsFatal(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	stageFile(t, repo, "clean.txt", "nothing to see here\n")

	out, code := runGuardCode(t, repo, listEnv(shortfallList(t)))
	if code != exitSelfCheck {
		t.Fatalf("want exit %d on a partially-malformed list, got %d; output: %q", exitSelfCheck, code, out)
	}
	// The separators and the singular/plural label are formatting; the NUMBERS
	// are the safety.
	wantTermsLoaded(t, "output", out, 4, 7)
	wantOffendingLines(t, out, 4, 5, 6)
}

// TestLeakGuard_ShortfallNeverPrintsLineContents pins the constraint that makes
// AC1 report line NUMBERS and not line contents: a malformed entry may still
// contain a real forbidden term, so echoing it would turn the guard's own
// diagnostic into the leak.
func TestLeakGuard_ShortfallNeverPrintsLineContents(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	stageFile(t, repo, "clean.txt", "nothing to see here\n")
	const secretish = "MALFORMEDBUTSTILLSECRET"

	list := writeList(t, "a:ci:AAAPLACEHOLDER", secretish)
	out, code := runGuardCode(t, repo, listEnv(list))
	if code != exitSelfCheck {
		t.Fatalf("want exit %d, got %d; output: %q", exitSelfCheck, code, out)
	}
	if strings.Contains(out, secretish) {
		t.Errorf("guard printed the contents of a malformed list line: %q", out)
	}
	// The line number is the only usable handle the operator gets, so it has to
	// be there — otherwise "contents withheld" degrades into "no information".
	wantOffendingLines(t, out, 2)
}

// TestLeakGuard_MalformedShapes pins exactly which shapes count against the
// numerator.
//
// The two "well-formed" rows are load-bearing in the other direction: an empty
// matchtype and a miscased matchtype must keep falling back to the BROADER
// case-insensitive match (TestLeakGuard_MiscasedMatchtypeFailsSafe), so
// treating them as malformed would trade a fail-safe for a fail-open.
//
// "empty category" and "padded term" go BEYOND AC1's literal arithmetic — both
// load today. They are included because both are silent protection losses of
// exactly the class AC1 exists to kill: an empty category makes the violation
// message unattributable, and a term with surrounding whitespace can never
// match anything, so it is an entry that looks live and is not. A count alone
// cannot see either. Flagged to the issue rather than folded in quietly.
func TestLeakGuard_MalformedShapes(t *testing.T) {
	cases := []struct {
		name      string
		entry     string
		malformed bool
	}{
		{"no colon", "MALFORMEDNOCOLON", true},
		{"two fields only", "cat:ci", true},
		{"empty term", "cat:ci:", true},
		{"empty category", ":ci:PLACEHOLDERX", true},
		{"padded term", "cat:ci:PLACEHOLDERX ", true},
		// The strongest case for the rule, and the reason "trim and load" is the
		// WRONG counter-proposal: today `[[ -z "$term" ]]` passes this, and
		// match_line then substring-matches three spaces — i.e. every line in the
		// tree containing three consecutive spaces becomes a violation. Trimming
		// would turn it into an empty term instead. Reject.
		{"whitespace-only term", "cat:ci:   ", true},
		{"empty matchtype", "cat::PLACEHOLDERX", false},
		{"miscased matchtype", "cat:CI:PLACEHOLDERX", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := initRepoOnBranch(t, "feature", true)
			stageFile(t, repo, "clean.txt", "nothing to see here\n")
			list := writeList(t, "keep:ci:KEEPPLACEHOLDER", tc.entry)

			out, code := runGuardCode(t, repo, listEnv(list))
			if tc.malformed {
				if code != exitSelfCheck {
					t.Fatalf("entry %q must be fatal; want exit %d, got %d; output: %q", tc.entry, exitSelfCheck, code, out)
				}
				wantTermsLoaded(t, "output", out, 1, 2)
				wantOffendingLines(t, out, 2)
				return
			}
			if code != exitClean {
				t.Fatalf("entry %q must load cleanly; want exit %d, got %d; output: %q", tc.entry, exitClean, code, out)
			}
			wantTermsLoaded(t, "output", out, 2, 2)
		})
	}
}

// -----------------------------------------------------------------------------
// AC2 — the scanner reports what it loaded on SUCCESS, unprompted.
// -----------------------------------------------------------------------------

// TestLeakGuard_SuccessReportsTermsLoaded exists because instance 6 of the
// issue's table proved a positive control is itself a probe that can be
// silently broken. The only remedy that does not recurse is the tool printing
// its own denominator, so a clean run is distinguishable from a vacuous one by
// READING it rather than by building another probe.
//
// It asserts per-class COUNTS (pos=1 neg=0), not bare class names: a static
// banner listing every class name would satisfy the name check in both modes
// while measuring nothing.
func TestLeakGuard_SuccessReportsTermsLoaded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		classes []string
	}{
		{"staged", nil, stagedClasses},
		{"all", []string{"--all"}, allClasses},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initRepoOnBranch(t, "feature", true)
			list := writeList(t, "a:ci:AAAPLACEHOLDER", "b:exact:BBBPLACEHOLDER")
			stageFile(t, repo, "clean.txt", "nothing to see here\n")

			out, stdout, code := runGuardSplit(t, repo, listEnv(list), tc.args...)
			if code != exitClean {
				t.Fatalf("want exit %d, got %d; output: %q", exitClean, code, out)
			}
			wantTermsLoaded(t, "stdout", stdout, 2, 2)
			for _, cls := range tc.classes {
				if !strings.Contains(stdout, classFired(cls)) {
					t.Errorf("want %q on stdout; stdout: %q", classFired(cls), stdout)
				}
			}
		})
	}
}

// TestLeakGuard_ControlTermsAreTheOnesTheGuardUses couples the constants in
// this file to the implementation. Without it, an implementer picking different
// control strings would leave TestLeakGuard_ControlTermsNeverContaminate
// staging two irrelevant strings — green, and testing nothing, forever.
// Printing them is safe by construction: they are placeholders, which is
// exactly why the issue mandates placeholders for controls.
func TestLeakGuard_ControlTermsAreTheOnesTheGuardUses(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")
	stageFile(t, repo, "clean.txt", "nothing to see here\n")

	out, stdout, code := runGuardSplit(t, repo, listEnv(list))
	if code != exitClean {
		t.Fatalf("want exit %d, got %d; output: %q", exitClean, code, out)
	}
	for _, ct := range []string{controlCI, controlExact} {
		if !strings.Contains(stdout, ct) {
			t.Errorf("guard must name its control term %q so a divergence from this test is loud; stdout: %q", ct, stdout)
		}
	}
}

// -----------------------------------------------------------------------------
// AC3 / AC4 — per-class fired counts, in-run, with a floor that can fire.
// -----------------------------------------------------------------------------

// TestLeakGuard_SuppressedControlExitsThree is the mutation that proves the
// floor itself can fire — the "bump the required count and watch it fail" step,
// done per class instead of in aggregate so a sum cannot hide a zero.
//
// SPRAWL_LEAK_GUARD_DEBUG_SUPPRESS is safe by construction: it can only remove
// a control's positive leg, i.e. only ever force exit 3. It cannot suppress a
// real scan and cannot downgrade an exit code — both asserted below rather than
// merely claimed in this comment.
func TestLeakGuard_SuppressedControlExitsThree(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		args    []string
		classes []string
	}{
		{"staged", nil, stagedClasses},
		{"all", []string{"--all"}, allClasses},
	} {
		for _, cls := range tc.classes {
			t.Run(tc.mode+"/"+cls, func(t *testing.T) {
				repo := initRepoOnBranch(t, "feature", true)
				list := writeList(t, "a:ci:AAAPLACEHOLDER", "b:exact:BBBPLACEHOLDER")
				stageFile(t, repo, "clean.txt", "nothing to see here\n")

				env := listEnv(list, "SPRAWL_LEAK_GUARD_DEBUG_SUPPRESS="+cls)
				out, _, code := runGuardSplit(t, repo, env, tc.args...)
				if code != exitSelfCheck {
					t.Fatalf("a clean tree with control %q suppressed must NOT report clean; want exit %d, got %d; output: %q",
						cls, exitSelfCheck, code, out)
				}
				// The ledger must show the zero, not just the class name — so the
				// diagnostic corroborates the report format rather than replacing it.
				if !strings.Contains(out, cls+": pos=0") {
					t.Errorf("want the suppressed class reported as %q; output: %q", cls+": pos=0", out)
				}
			})
		}
	}
}

// TestLeakGuard_SuppressUnknownClassIsUsageError forces the guard to own a real
// class registry. Without it, `echo "$SUPPRESS"; exit 3` — eight lines, no
// controls whatsoever — passes every subtest above, because the only string the
// assertions look for is the one the test itself supplied.
func TestLeakGuard_SuppressUnknownClassIsUsageError(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")
	stageFile(t, repo, "clean.txt", "nothing to see here\n")

	env := listEnv(list, "SPRAWL_LEAK_GUARD_DEBUG_SUPPRESS=no-such-class")
	out, code := runGuardCode(t, repo, env)
	if code != exitUsage {
		t.Fatalf("an unknown probe class must be a usage error, not a self-check failure; want exit %d, got %d; output: %q",
			exitUsage, code, out)
	}
}

// TestLeakGuard_SuppressCannotDowngradeAViolation pins the safety claim made
// for the debug seam: it can only ever ESCALATE to 3. If it could turn a real
// violation into a pass it would be a hole in the very gate it tests.
func TestLeakGuard_SuppressCannotDowngradeAViolation(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")
	stageFile(t, repo, "leak.txt", "has AAAPLACEHOLDER inside\n")

	env := listEnv(list, "SPRAWL_LEAK_GUARD_DEBUG_SUPPRESS=diff-linewise-ci")
	out, code := runGuardCode(t, repo, env)
	if code != exitSelfCheck {
		t.Fatalf("want exit %d (self-check beats violation), got %d; output: %q", exitSelfCheck, code, out)
	}
	// Escalating to 3 must not SWALLOW the violation: the operator still needs to
	// see what was found, they just may not treat the list as complete.
	if !strings.Contains(out, "leak.txt") {
		t.Errorf("the violation must still be surfaced alongside exit 3; output: %q", out)
	}
}

// TestLeakGuard_ControlNegativeLeg proves the controls can DISCRIMINATE, not
// just fire. A control that matches unconditionally passes the positive leg
// forever and is exactly the broken-probe failure this issue catalogues, so
// each class also asserts its control term is absent from a clean subject.
// SPRAWL_LEAK_GUARD_DEBUG_NEGLEAK plants the control term in that negative
// subject; the negative leg must then fail. Like SUPPRESS it can only force
// exit 3.
func TestLeakGuard_ControlNegativeLeg(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		args    []string
		classes []string
	}{
		{"staged", nil, stagedClasses},
		{"all", []string{"--all"}, allClasses},
	} {
		for _, cls := range tc.classes {
			if cls == "list-parse" {
				continue // the list-parse control has no content subject
			}
			t.Run(tc.mode+"/"+cls, func(t *testing.T) {
				repo := initRepoOnBranch(t, "feature", true)
				list := writeList(t, "a:ci:AAAPLACEHOLDER")
				stageFile(t, repo, "clean.txt", "nothing to see here\n")

				env := listEnv(list, "SPRAWL_LEAK_GUARD_DEBUG_NEGLEAK="+cls)
				out, code := runGuardCode(t, repo, env, tc.args...)
				if code != exitSelfCheck {
					t.Fatalf("control %q fired on its NEGATIVE subject and the guard still passed; want exit %d, got %d; output: %q",
						cls, exitSelfCheck, code, out)
				}
				if !strings.Contains(out, cls+": pos=1 neg=1") {
					t.Errorf("want the discriminating failure reported as %q; output: %q", cls+": pos=1 neg=1", out)
				}
			})
		}
	}
}

// TestLeakGuard_NegLeakUnknownClassIsUsageError — same registry argument as for
// SUPPRESS; without it a bare `exit 3` on any NEGLEAK value passes the table.
func TestLeakGuard_NegLeakUnknownClassIsUsageError(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")
	stageFile(t, repo, "clean.txt", "nothing to see here\n")

	env := listEnv(list, "SPRAWL_LEAK_GUARD_DEBUG_NEGLEAK=no-such-class")
	if out, code := runGuardCode(t, repo, env); code != exitUsage {
		t.Fatalf("want exit %d for an unknown class, got %d; output: %q", exitUsage, code, out)
	}
}

// TestLeakGuard_ControlTermsNeverContaminate proves the in-run controls cannot
// leak into the real scan's verdict: a file containing the control placeholders
// is not a violation under a real list that does not name them. The positive
// leg (controls actually ran in this same invocation) is asserted too —
// otherwise "no controls exist" satisfies it identically.
func TestLeakGuard_ControlTermsNeverContaminate(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")
	stageFile(t, repo, "ctl.txt", controlCI+"\n"+controlExact+"\n")

	out, stdout, code := runGuardSplit(t, repo, listEnv(list))
	if code != exitClean {
		t.Fatalf("control placeholders must never be a violation; want exit %d, got %d; output: %q", exitClean, code, out)
	}
	if !strings.Contains(stdout, classFired("diff-linewise-ci")) {
		t.Errorf("the controls must have run in THIS invocation; stdout: %q", stdout)
	}
}

// TestLeakGuard_ControlsAreModeScoped pins that the required class set follows
// the paths the invocation actually took. Suppressing a tree control cannot
// fail a staged run, because a control for a code path the run did not take is
// not evidence about that run — counting it would manufacture coverage.
func TestLeakGuard_ControlsAreModeScoped(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")
	stageFile(t, repo, "clean.txt", "nothing to see here\n")

	env := listEnv(list, "SPRAWL_LEAK_GUARD_DEBUG_SUPPRESS=tree-linewise-ci")
	out, stdout, code := runGuardSplit(t, repo, env)
	if code != exitClean {
		t.Fatalf("a tree-class control must not be required in staged mode; want exit %d, got %d; output: %q", exitClean, code, out)
	}
	// Positive leg: the staged classes DID run and are reported...
	for _, cls := range stagedClasses {
		if !strings.Contains(stdout, classFired(cls)) {
			t.Errorf("want staged class %q reported; stdout: %q", cls, stdout)
		}
	}
	// ...and the tree class is not claimed as coverage this run never had.
	// The intent is "must not claim coverage", not "must not utter the name":
	// acknowledging an inactive class is legitimate operator output.
	if strings.Contains(stdout, classFired("tree-linewise-ci")) {
		t.Errorf("staged mode must not claim a tree class fired; stdout: %q", stdout)
	}
}

// TestLeakGuard_ControlIsDownstreamOfTheRealMatcher closes the last gap the
// ledger assertions leave open, and it is this issue's own thesis pointed at
// the fix: every other test verifies the ledger as OUTPUT, so an implementation
// with a real scan and a hand-written printf ledger — or one whose control has
// drifted off the code path it certifies — passes all of them.
//
// SPRAWL_LEAK_GUARD_DEBUG_BREAK breaks the REAL matcher for one mechanism. The
// corresponding control must then report pos=0 and the run must exit 3. That is
// the only assertion here proving a control is genuinely downstream of the code
// it claims to cover, rather than a claim printed beside it.
func TestLeakGuard_ControlIsDownstreamOfTheRealMatcher(t *testing.T) {
	for _, tc := range []struct {
		mechanism string
		args      []string
		class     string
	}{
		{"dewrap", nil, "diff-dewrapped-ci"},
		{"dewrap", []string{"--all"}, "tree-dewrapped-ci"},
		{"linewise", nil, "diff-linewise-ci"},
		{"linewise", []string{"--all"}, "tree-linewise-ci"},
	} {
		name := tc.mechanism + "/" + tc.class
		t.Run(name, func(t *testing.T) {
			repo := initRepoOnBranch(t, "feature", true)
			list := writeList(t, "a:ci:AAAPLACEHOLDER")
			stageFile(t, repo, "clean.txt", "nothing to see here\n")

			env := listEnv(list, "SPRAWL_LEAK_GUARD_DEBUG_BREAK="+tc.mechanism)
			out, _, code := runGuardSplit(t, repo, env, tc.args...)
			if code != exitSelfCheck {
				t.Fatalf("breaking the real %s matcher must be caught by control %q; want exit %d, got %d; output: %q",
					tc.mechanism, tc.class, exitSelfCheck, code, out)
			}
			if !strings.Contains(out, tc.class+": pos=0") {
				t.Errorf("want %q — the control must sit downstream of the broken code; output: %q", tc.class+": pos=0", out)
			}
		})
	}
}

// TestLeakGuard_BreakUnknownMechanismIsUsageError — same registry argument as
// for the other two seams.
func TestLeakGuard_BreakUnknownMechanismIsUsageError(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")
	stageFile(t, repo, "clean.txt", "nothing to see here\n")

	env := listEnv(list, "SPRAWL_LEAK_GUARD_DEBUG_BREAK=no-such-mechanism")
	if out, code := runGuardCode(t, repo, env); code != exitUsage {
		t.Fatalf("want exit %d for an unknown mechanism, got %d; output: %q", exitUsage, code, out)
	}
}

// -----------------------------------------------------------------------------
// AC5 — the :68 missing-list fail-open stays OPEN, but stops being silent.
// -----------------------------------------------------------------------------

// TestLeakGuard_NoListAnnouncesItself covers BOTH modes deliberately. tower
// retracted a claim that TestLeakGuard_NoListNoOp constrained the --all path:
// it does not (it execs with no --all, and every --all test passes a real
// list), so --all-with-no-list was unconstrained in both directions and two
// Urgents prescribed opposite remedies for it without either hitting a red.
// This pins it: the documented installability fail-open holds in both modes,
// and both announce that they measured nothing.
func TestLeakGuard_NoListAnnouncesItself(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"staged", nil},
		{"all", []string{"--all"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initRepoOnBranch(t, "feature", true)
			stageFile(t, repo, "leak.txt", synthTerm+"\n")
			env := baseEnv("SPRAWL_FORBIDDEN_TERMS_FILE=" + filepath.Join(t.TempDir(), "does-not-exist"))

			out, stdout, code := runGuardSplit(t, repo, env, tc.args...)
			if code != exitClean {
				t.Fatalf("missing list must stay a no-op pass (installability, L32/L67); want exit %d, got %d; output: %q",
					exitClean, code, out)
			}
			wantTermsLoaded(t, "stdout", stdout, 0, 0)
			if !strings.Contains(stdout, "no list found") {
				t.Errorf("want the no-op announced with %q; stdout: %q", "no list found", stdout)
			}
		})
	}
}

// TestLeakGuard_RequireListFlagIsFatal pins the opt-in half of AC5: a repo that
// wants a hard requirement asks for it explicitly, rather than the default
// being inverted (which would break installing the guard in a foreign repo).
// The `--all --require-list` combination is the one that matters — both the
// Makefile's leak-scan target and scripts/pre-commit already pass a mode flag,
// so an arg parser that only reads $1 would silently drop one of them.
func TestLeakGuard_RequireListFlagIsFatal(t *testing.T) {
	for _, args := range [][]string{
		{"--require-list"},
		{"--all", "--require-list"},
		{"--require-list", "--all"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			repo := initRepoOnBranch(t, "feature", true)
			env := baseEnv("SPRAWL_FORBIDDEN_TERMS_FILE=" + filepath.Join(t.TempDir(), "does-not-exist"))

			out, code := runGuardCode(t, repo, env, args...)
			if code != exitSelfCheck {
				t.Fatalf("--require-list with no list must fail; want exit %d, got %d; output: %q", exitSelfCheck, code, out)
			}
		})
	}

	// The discriminating leg: without it, `--require-list` => exit 3
	// unconditionally passes every case above while asserting the flag nothing.
	t.Run("list present passes", func(t *testing.T) {
		repo := initRepoOnBranch(t, "feature", true)
		list := writeList(t, "a:ci:AAAPLACEHOLDER")
		stageFile(t, repo, "clean.txt", "nothing to see here\n")
		if out, code := runGuardCode(t, repo, listEnv(list), "--require-list"); code != exitClean {
			t.Fatalf("--require-list with a list present must pass; want exit %d, got %d; output: %q", exitClean, code, out)
		}
	})
}

// -----------------------------------------------------------------------------
// AC6 — every content probe runs de-wrapped as well as line-wise.
// -----------------------------------------------------------------------------

// TestLeakGuard_WrappedTermStaged is the second load-bearing red: today both
// scan modes are strictly line-wise, so a term split across a newline is
// invisible — the "NOT a squash commit" failure mode reproduced inside our own
// guard.
//
// A de-wrapped hit reports the FILE and no line number: the de-wrapped stream
// has no lines, and reconstructing one would be a guess presented as a fact.
func TestLeakGuard_WrappedTermStaged(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "wrapped:ci:WRAPPEDPLACEHOLDER")
	stageFile(t, repo, "b.txt", "hello WRAPPED\nPLACEHOLDER world\n")

	out, code := runGuardCode(t, repo, listEnv(list))
	if code != exitViolation {
		t.Fatalf("want exit %d for a term split across a newline, got %d; output: %q", exitViolation, code, out)
	}
	if !strings.Contains(out, "b.txt: wrapped (wrapped match)") {
		t.Errorf("want %q; output: %q", "b.txt: wrapped (wrapped match)", out)
	}
	if strings.Contains(out, "WRAPPEDPLACEHOLDER") {
		t.Errorf("guard leaked the term into its output: %q", out)
	}
}

// TestLeakGuard_WrappedTermWholeTree is the same blindness on the --all path,
// which uses a completely different mechanism (git grep, which structurally
// cannot match across a line break) and therefore needs its own red.
func TestLeakGuard_WrappedTermWholeTree(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "wrapped:ci:WRAPPEDPLACEHOLDER")
	stageFile(t, repo, "b.txt", "hello WRAPPED\nPLACEHOLDER world\n")
	if out, err := gitRun(t, repo, baseEnv(), "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit: %s: %v", out, err)
	}

	out, code := runGuardCode(t, repo, listEnv(list), "--all")
	if code != exitViolation {
		t.Fatalf("want exit %d for a wrapped term in --all, got %d; output: %q", exitViolation, code, out)
	}
	if !strings.Contains(out, "b.txt: wrapped (wrapped match)") {
		t.Errorf("want %q; output: %q", "b.txt: wrapped (wrapped match)", out)
	}
}

// TestLeakGuard_WrappedExactMatchtype closes the gap where only `ci` terms were
// ever exercised de-wrapped. `exact` is the matchtype used for GUIDs and
// resource names, and the floor requires a *-dewrapped-exact class to fire, so
// leaving its behaviour untested would let the class exist without working.
func TestLeakGuard_WrappedExactMatchtype(t *testing.T) {
	t.Run("matches exact case wrapped", func(t *testing.T) {
		repo := initRepoOnBranch(t, "feature", true)
		list := writeList(t, "wid:exact:WRAPPEDEXACTPLACEHOLDER")
		stageFile(t, repo, "e.txt", "id WRAPPEDEXACT\nPLACEHOLDER here\n")
		if out, code := runGuardCode(t, repo, listEnv(list)); code != exitViolation {
			t.Fatalf("want exit %d, got %d; output: %q", exitViolation, code, out)
		}
	})

	// The discriminating half: `exact` must stay case-SENSITIVE de-wrapped too,
	// or the de-wrap pass has quietly broadened every exact term into a ci term.
	t.Run("does not match lowercase wrapped", func(t *testing.T) {
		repo := initRepoOnBranch(t, "feature", true)
		list := writeList(t, "wid:exact:WRAPPEDEXACTPLACEHOLDER")
		stageFile(t, repo, "e.txt", "id wrappedexact\nplaceholder here\n")
		if out, code := runGuardCode(t, repo, listEnv(list)); code != exitClean {
			t.Fatalf("want exit %d, got %d; output: %q", exitClean, code, out)
		}
	})
}

// TestLeakGuard_WrappedCRLF proves the de-wrap strips the CR too. Without that,
// a CRLF file can never produce a wrapped match and the whole pass is silently
// inert on exactly the files most likely to be pasted in from elsewhere.
func TestLeakGuard_WrappedCRLF(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	// Pin autocrlf: on a host with core.autocrlf=input git strips the CR at
	// `git add` and this case silently degenerates into WrappedTermStaged —
	// green, and no longer testing CRLF at all.
	if out, err := gitRun(t, repo, baseEnv(), "config", "core.autocrlf", "false"); err != nil {
		t.Fatalf("git config: %s: %v", out, err)
	}
	list := writeList(t, "wrapped:ci:WRAPPEDPLACEHOLDER")
	stageFile(t, repo, "crlf.txt", "hello WRAPPED\r\nPLACEHOLDER world\r\n")

	blob, err := gitRun(t, repo, baseEnv(), "show", ":crlf.txt")
	if err != nil {
		t.Fatalf("git show: %s: %v", blob, err)
	}
	if !strings.Contains(blob, "\r\n") {
		t.Fatalf("fixture precondition failed: CR was stripped at staging, so this is not a CRLF test")
	}

	out, code := runGuardCode(t, repo, listEnv(list))
	if code != exitViolation {
		t.Fatalf("want exit %d for a CRLF-split term, got %d; output: %q", exitViolation, code, out)
	}
	if !strings.Contains(out, "crlf.txt: wrapped (wrapped match)") {
		t.Errorf("want %q; output: %q", "crlf.txt: wrapped (wrapped match)", out)
	}
}

// TestLeakGuard_WrappedAcrossHunkBoundaryNotReported is the de-wrap's NEGATIVE
// control, and the assertion most likely to be omitted. Under --unified=0 the
// added lines of two distant hunks are NOT adjacent in the file, so joining
// across a hunk boundary would manufacture a term that does not exist anywhere
// in the tree — a false alarm on a gate that blocks every commit.
//
// Verified: this fixture produces exactly two hunks (@@ -2,0 +3 @@ and
// @@ -25,0 +27 @@) whose only added lines are HALFONE and HALFTWO, so a naive
// whole-diff join yields exactly HALFONEHALFTWO.
func TestLeakGuard_WrappedAcrossHunkBoundaryNotReported(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "wrapped:ci:HALFONEHALFTWO")

	var seed strings.Builder
	for i := 0; i < 30; i++ {
		seed.WriteString("filler line\n")
	}
	stageFile(t, repo, "doc.txt", seed.String())
	if out, err := gitRun(t, repo, baseEnv(), "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit: %s: %v", out, err)
	}

	lines := strings.SplitAfter(seed.String(), "\n")
	lines[2] = "HALFONE\n" + lines[2]
	lines[25] = "HALFTWO\n" + lines[25]
	stageFile(t, repo, "doc.txt", strings.Join(lines, ""))

	out, stdout, code := runGuardSplit(t, repo, listEnv(list))
	if code != exitClean {
		t.Fatalf("halves in separate hunks are not adjacent in the file and must NOT be joined; want exit %d, got %d; output: %q",
			exitClean, code, out)
	}
	// Positive leg: without this, "the de-wrap pass does not exist" satisfies
	// the assertion above exactly as well as "the de-wrap pass declined".
	if !strings.Contains(stdout, classFired("diff-dewrapped-ci")) {
		t.Errorf("the de-wrap pass must have run in THIS invocation; stdout: %q", stdout)
	}
	wantTermsLoaded(t, "stdout", stdout, 1, 1)
}

// TestLeakGuard_LinewiseStillReportsFileLine proves the de-wrap pass did not
// swallow or duplicate the line-wise pass: an unwrapped occurrence keeps its
// exact file:line and is reported exactly ONCE.
func TestLeakGuard_LinewiseStillReportsFileLine(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "plain:ci:PLAINPLACEHOLDER")
	stageFile(t, repo, "p.txt", "first line clean\nsecond has PLAINPLACEHOLDER here\n")

	out, stdout, code := runGuardSplit(t, repo, listEnv(list))
	if code != exitViolation {
		t.Fatalf("want exit %d, got %d; output: %q", exitViolation, code, out)
	}
	if !strings.Contains(out, "p.txt:2: plain") {
		t.Errorf("want %q; output: %q", "p.txt:2: plain", out)
	}
	// Dedupe, pinned numerically: the de-wrapped stream also contains this term,
	// so without dedupe the operator sees the same finding twice.
	if n := len(regexp.MustCompile(`(?m)^\s+p\.txt[:\s]`).FindAllString(out, -1)); n != 1 {
		t.Errorf("want p.txt reported on exactly one violation line (line-wise), got %d; output: %q", n, out)
	}
	// Positive leg: the de-wrap pass ran and simply had nothing extra to add.
	if !strings.Contains(stdout, classFired("diff-dewrapped-ci")) {
		t.Errorf("the de-wrap pass must have run in THIS invocation; stdout: %q", stdout)
	}
}

// -----------------------------------------------------------------------------
// AC7 — rename detection asserted, not assumed.
// -----------------------------------------------------------------------------

// TestLeakGuard_RenameDetectionPinned is the false-ALARM direction, which the
// issue makes a first-class AC because it is loud and expensive rather than
// invisible: it burns operator trust and stalls a correct push.
//
// Note the mechanism differs from the issue's account. Rename detection is ON
// by default at git 2.9+, and this script passes no pathspec, so the pathspec
// defect the issue describes has no subject here. The live hazard is the
// mirror image: a user's `diff.renames=false` config turns a rename into a
// whole-file re-add, so every grandfathered line is re-presented as an addition
// and the guard blocks a commit that introduced nothing. Same class as the
// existing diff.mnemonicPrefix / diff.noprefix tests; same remedy — pin it.
func TestLeakGuard_RenameDetectionPinned(t *testing.T) {
	// The seed is deliberately long. With a one-line file, adding a second line
	// puts similarity at 51% — one point over git's default 50% rename
	// threshold — and four more characters silently destroys rename detection,
	// reverting the diff to a whole-file add. Both shapes exit 1, so the margin
	// failure would be invisible. 20 identical lines put it far clear.
	grandfathered := strings.Repeat("grandfathered "+synthTerm+" line\n", 20)

	newRepo := func(t *testing.T) (string, string) {
		t.Helper()
		repo := initRepoOnBranch(t, "feature", true)
		list := writeList(t, "secret-name:ci:"+synthTerm)
		stageFile(t, repo, "old.txt", grandfathered)
		if out, err := gitRun(t, repo, baseEnv(), "commit", "-m", "seed"); err != nil {
			t.Fatalf("commit: %s: %v", out, err)
		}
		if out, err := gitRun(t, repo, baseEnv(), "config", "diff.renames", "false"); err != nil {
			t.Fatalf("git config: %s: %v", out, err)
		}
		if out, err := gitRun(t, repo, baseEnv(), "mv", "old.txt", "new.txt"); err != nil {
			t.Fatalf("git mv: %s: %v", out, err)
		}
		return repo, list
	}

	// False-alarm direction: a pure rename introduces no new content.
	t.Run("pure rename passes", func(t *testing.T) {
		repo, list := newRepo(t)
		if out, code := runGuardCode(t, repo, listEnv(list)); code != exitClean {
			t.Fatalf("a pure rename must pass even under diff.renames=false; want exit %d, got %d; output: %q",
				exitClean, code, out)
		}
	})

	// The miss direction, which stops the fix from over-suppressing: a rename
	// that ALSO adds a line carrying a term is still a violation.
	t.Run("rename plus a new leak still fails", func(t *testing.T) {
		repo, list := newRepo(t)
		stageFile(t, repo, "new.txt", grandfathered+"newly added "+synthTerm+" line\n")
		out, code := runGuardCode(t, repo, listEnv(list))
		if code != exitViolation {
			t.Fatalf("a line ADDED during a rename must still be caught; want exit %d, got %d; output: %q",
				exitViolation, code, out)
		}
		// The exit code alone cannot tell the two worlds apart: a whole-file
		// re-add also exits 1. Line ATTRIBUTION can — under rename detection only
		// the appended line 21 is a violation; without it, every carried-over
		// line is re-flagged too. That difference IS the over-suppression check.
		if !strings.Contains(out, "new.txt:21:") {
			t.Errorf("want the newly added line 21 flagged; output: %q", out)
		}
		if strings.Contains(out, "new.txt:1:") {
			t.Errorf("carried-over line 1 must not be re-flagged (rename detection lost); output: %q", out)
		}
	})
}

// -----------------------------------------------------------------------------
// AC8 — the report states what it did NOT examine.
// -----------------------------------------------------------------------------

// TestLeakGuard_ReportsWhatItSkipped exists so an absence in the output is not
// read as an absence in the tree. The guard is structurally blind to binaries
// (git grep -I) and never looks at untracked or ignored paths; saying nothing
// about that is how "clean" gets over-read.
func TestLeakGuard_ReportsWhatItSkipped(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")

	if err := os.WriteFile(filepath.Join(repo, "blob.bin"), []byte{0x00, 0x01, 0x02, 0x00, 0xff}, 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if out, err := gitRun(t, repo, baseEnv(), "add", "blob.bin"); err != nil {
		t.Fatalf("git add: %s: %v", out, err)
	}
	stageFile(t, repo, "text.txt", "clean content\n")
	if out, err := gitRun(t, repo, baseEnv(), "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit: %s: %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "loose.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	t.Run("all", func(t *testing.T) {
		out, stdout, code := runGuardSplit(t, repo, listEnv(list), "--all")
		if code != exitClean {
			t.Fatalf("want exit %d, got %d; output: %q", exitClean, code, out)
		}
		if !strings.Contains(stdout, "not examined") {
			t.Errorf("want a %q block; stdout: %q", "not examined", stdout)
		}
		if !strings.Contains(stdout, "1 binary") {
			t.Errorf("want the skipped binary counted; stdout: %q", stdout)
		}
		if !strings.Contains(stdout, "1 untracked") {
			t.Errorf("want the untracked file counted; stdout: %q", stdout)
		}
	})

	// Staged mode has its own, different blind spots — the whole pre-existing
	// tree, plus anything unstaged — and reporting the --all list there would be
	// worse than reporting nothing.
	// NOTE: the subtests share `repo` and run in declaration order — "all" runs
	// against the committed tree, then "staged" adds to the index. Do not add
	// t.Parallel() here without giving each its own fixture.
	t.Run("staged", func(t *testing.T) {
		// Stage real content, plus an unstaged edit to it: an empty index would
		// make this assert against a diff with nothing in it.
		stageFile(t, repo, "staged.txt", "clean staged content\n")
		if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("clean staged content\nunstaged edit\n"), 0o644); err != nil {
			t.Fatalf("write unstaged edit: %v", err)
		}
		out, stdout, code := runGuardSplit(t, repo, listEnv(list))
		if code != exitClean {
			t.Fatalf("want exit %d, got %d; output: %q", exitClean, code, out)
		}
		if !strings.Contains(stdout, "not examined") {
			t.Errorf("want a %q block in staged mode too; stdout: %q", "not examined", stdout)
		}
		if !strings.Contains(stdout, "untracked") {
			t.Errorf("want untracked content named as unexamined; stdout: %q", stdout)
		}
		if !strings.Contains(stdout, "unstaged") {
			t.Errorf("want unstaged changes named as unexamined; stdout: %q", stdout)
		}
	})
}

// -----------------------------------------------------------------------------
// AC9 — the exit-code contract, including the precedence rule.
// -----------------------------------------------------------------------------

// TestLeakGuard_ExitCodeContract pins all four codes in one place, and in
// particular that 3 BEATS 1. If the self-checks did not fire, the violation
// list is incomplete by construction, so reporting 1 would assert a
// completeness the run cannot back — and reporting 0 is the collapse AC9
// forbids outright.
func TestLeakGuard_ExitCodeContract(t *testing.T) {
	t.Run("usage", func(t *testing.T) {
		repo := initRepoOnBranch(t, "feature", true)
		list := writeList(t, "a:ci:AAAPLACEHOLDER")
		if out, code := runGuardCode(t, repo, listEnv(list), "--bogus"); code != exitUsage {
			t.Fatalf("want exit %d, got %d; output: %q", exitUsage, code, out)
		}
	})

	t.Run("clean", func(t *testing.T) {
		repo := initRepoOnBranch(t, "feature", true)
		list := writeList(t, "a:ci:AAAPLACEHOLDER")
		stageFile(t, repo, "clean.txt", "nothing here\n")
		if out, code := runGuardCode(t, repo, listEnv(list)); code != exitClean {
			t.Fatalf("want exit %d, got %d; output: %q", exitClean, code, out)
		}
	})

	t.Run("violation", func(t *testing.T) {
		repo := initRepoOnBranch(t, "feature", true)
		list := writeList(t, "a:ci:AAAPLACEHOLDER")
		stageFile(t, repo, "leak.txt", "has AAAPLACEHOLDER inside\n")
		if out, code := runGuardCode(t, repo, listEnv(list)); code != exitViolation {
			t.Fatalf("want exit %d, got %d; output: %q", exitViolation, code, out)
		}
	})

	t.Run("shortfall beats violation", func(t *testing.T) {
		repo := initRepoOnBranch(t, "feature", true)
		list := writeList(t, "a:ci:AAAPLACEHOLDER", "MALFORMEDNOCOLON")
		stageFile(t, repo, "leak.txt", "has AAAPLACEHOLDER inside\n")
		out, code := runGuardCode(t, repo, listEnv(list))
		if code != exitSelfCheck {
			t.Fatalf("a shortfall makes the violation list incomplete by construction; want exit %d, got %d; output: %q",
				exitSelfCheck, code, out)
		}
	})

	// A violation is an ERROR: it must reach stderr, and it must not be diluted
	// into the stdout success report.
	t.Run("violation goes to stderr", func(t *testing.T) {
		repo := initRepoOnBranch(t, "feature", true)
		list := writeList(t, "a:ci:AAAPLACEHOLDER")
		stageFile(t, repo, "leak.txt", "has AAAPLACEHOLDER inside\n")
		_, stdout, code := runGuardSplit(t, repo, listEnv(list))
		if code != exitViolation {
			t.Fatalf("want exit %d, got %d", exitViolation, code)
		}
		if strings.Contains(stdout, "leak.txt") {
			t.Errorf("violations must go to stderr, not stdout; stdout: %q", stdout)
		}
		// Positive leg: "the guard prints nothing to stdout at all" must not
		// satisfy the assertion above.
		if !strings.Contains(stdout, classFired("diff-linewise-ci")) {
			t.Errorf("the ledger must still print on stdout during a violation run; stdout: %q", stdout)
		}
	})
}

// -----------------------------------------------------------------------------
// Review findings (code review of e2aec18). Each of these was REPRODUCED as an
// exit-0 "clean" verdict over a tree that contained a forbidden term, which is
// precisely the defect class QUM-1156 exists to make impossible. They are the
// second red-first round on this change.
// -----------------------------------------------------------------------------

// TestLeakGuard_AddedLineLookingLikeADiffHeader — under --unified=0 an added
// line is emitted as "+" + content, so staged content beginning with "++ "
// arrives at the diff parser as "+++ ...". A naive parser reads that as a file
// header: "++ /dev/null" sets the current file to none, and EVERY subsequent
// added line in the whole diff is skipped. Two staged leaks, verdict clean.
//
// Header lines are only meaningful in header position, so the parser has to
// track where it is rather than pattern-match the prefix anywhere.
func TestLeakGuard_AddedLineLookingLikeADiffHeader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		evasion string
	}{
		{"dev null", "++ /dev/null"},
		{"fake path", "++ b/evil.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initRepoOnBranch(t, "feature", true)
			list := writeList(t, "a:ci:AAAPLACEHOLDER")
			stageFile(t, repo, "a.txt", "seed\n"+tc.evasion+"\nhas AAAPLACEHOLDER here\n")

			out, code := runGuardCode(t, repo, listEnv(list))
			if code != exitViolation {
				t.Fatalf("a staged line that merely LOOKS like a diff header must not blind the scan; want exit %d, got %d; output: %q",
					exitViolation, code, out)
			}
			// And it must be attributed to the real file, not to the path the
			// content spoofed.
			if !strings.Contains(out, "a.txt:3: a") {
				t.Errorf("want the leak reported at %q; output: %q", "a.txt:3: a", out)
			}
		})
	}
}

// TestLeakGuard_EmptyButPresentListIsFatal — AC1 applies to :90 as well as :84,
// and the issue is explicit: "Implementers should treat :90 with :84, not with
// :68." A list that exists, is readable, and parses to zero terms is a SIGNAL
// (someone wrote a list and it produced nothing), not an absence. The
// fully-malformed case is already caught as 0/7; the genuinely-empty one was
// not, because 0/0 is not a shortfall.
func TestLeakGuard_EmptyButPresentListIsFatal(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"staged", nil},
		{"all", []string{"--all"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initRepoOnBranch(t, "feature", true)
			list := writeList(t, "# only a comment", "")
			stageFile(t, repo, "leak.txt", "anything at all\n")

			out, code := runGuardCode(t, repo, listEnv(list), tc.args...)
			if code != exitSelfCheck {
				t.Fatalf("a present list that parses to zero terms must not render a clean verdict; want exit %d, got %d; output: %q",
					exitSelfCheck, code, out)
			}
		})
	}
}

// TestLeakGuard_NonRegularListIsFatal — :68's installability rationale covers a
// list that is ABSENT. It does not cover "you named a file and I ignored it":
// silently demoting an explicitly requested but unusable list to "no list
// found" is the fail-open wearing the fail-open's clothes.
func TestLeakGuard_NonRegularListIsFatal(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	// A directory exists but is not a regular file — same shape as /dev/null,
	// without depending on a device node.
	env := baseEnv("SPRAWL_FORBIDDEN_TERMS_FILE=" + t.TempDir())

	out, code := runGuardCode(t, repo, env)
	if code != exitSelfCheck {
		t.Fatalf("a named-but-unusable list must fail, not be demoted to no-op; want exit %d, got %d; output: %q",
			exitSelfCheck, code, out)
	}
}

// TestLeakGuard_DewrapFailureIsFatal — the de-wrap pass ran `rc=$?` INSIDE a
// process substitution, which is a subshell, so the outer rc stayed 0 and the
// die_selfcheck guarding it was unreachable dead code. A de-wrap pass that
// failed over the real tree therefore produced a clean verdict.
//
// The class controls cannot catch this: they scan a handful of tiny files in
// the scratch dir, so any failure that is DATA-DEPENDENT on the real tree
// (xargs splitting, ARG_MAX, one unreadable path) passes the control and voids
// the real pass. Hence the shim below fails only when handed the real tree.
func TestLeakGuard_DewrapFailureIsFatal(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")
	stageFile(t, repo, "dewrap-canary.txt", "clean content\n")
	if out, err := gitRun(t, repo, baseEnv(), "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit: %s: %v", out, err)
	}

	realAwk, err := exec.LookPath("awk")
	if err != nil {
		t.Fatalf("awk not found: %v", err)
	}
	shimDir := t.TempDir()
	shim := "#!/bin/sh\nfor a in \"$@\"; do\n  case \"$a\" in *dewrap-canary*) exit 9 ;; esac\ndone\nexec " + realAwk + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "awk"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	env := listEnv(list, "PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, code := runGuardCode(t, repo, env, "--all")
	if code != exitSelfCheck {
		t.Fatalf("a de-wrap pass that FAILED over the real tree must not render a clean verdict; want exit %d, got %d; output: %q",
			exitSelfCheck, code, out)
	}
}

// TestLeakGuard_UnreadableTrackedFileIsFatal — `git grep` reports an unreadable
// tracked file on stderr and still exits 0 or 1, so the rc>1 guard does not see
// it. A file the scanner could not read is a file it did not scan, and saying
// nothing about that is how a clean verdict over-claims.
func TestLeakGuard_UnreadableTrackedFileIsFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not make a file unreadable, so this fixture cannot hold")
	}
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")
	stageFile(t, repo, "locked.txt", "clean content\n")
	if out, err := gitRun(t, repo, baseEnv(), "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit: %s: %v", out, err)
	}
	locked := filepath.Join(repo, "locked.txt")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	out, code := runGuardCode(t, repo, listEnv(list), "--all")
	if code != exitSelfCheck {
		t.Fatalf("a tracked file the scan could not read must not be silently skipped; want exit %d, got %d; output: %q",
			exitSelfCheck, code, out)
	}
}

// TestLeakGuard_ScratchFailureIsNotMistakenForAViolation — under `set -e` an
// assignment whose command substitution fails aborts with THAT command's
// status. mktemp exits 1, so the scratch-dir failure surfaced as exit 1, which
// every caller reads as EXIT_VIOLATION — the worst available misreading, since
// it is a scan that never happened wearing the mask of a scan that found
// something.
func TestLeakGuard_ScratchFailureIsNotMistakenForAViolation(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")
	stageFile(t, repo, "clean.txt", "nothing here\n")

	env := listEnv(list, "TMPDIR=/nonexistent/finn-no-such-dir")
	out, code := runGuardCode(t, repo, env)
	if code != exitSelfCheck {
		t.Fatalf("an unusable TMPDIR is a scan that did not happen, not a violation; want exit %d, got %d; output: %q",
			exitSelfCheck, code, out)
	}
}

// TestLeakGuard_TmpdirIsNotEvaluatedAsShell — the EXIT trap was installed with
// double quotes, interpolating $SCRATCH into a string bash re-parses at trap
// time. TMPDIR is attacker-influenced wherever the guard runs under someone
// else's environment, which makes this command injection in the repo's own
// security gate.
func TestLeakGuard_TmpdirIsNotEvaluatedAsShell(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "a:ci:AAAPLACEHOLDER")
	stageFile(t, repo, "clean.txt", "nothing here\n")

	base := t.TempDir()
	sentinel := filepath.Join(base, "PWNED")
	hostile := filepath.Join(base, "q';touch \""+sentinel+"\";'")
	if err := os.MkdirAll(hostile, 0o755); err != nil {
		t.Fatalf("mkdir hostile TMPDIR: %v", err)
	}

	out, stdout, _ := runGuardSplit(t, repo, listEnv(list, "TMPDIR="+hostile))
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("TMPDIR was evaluated as shell — command injection via the EXIT trap; output: %q", out)
	}
	// The exit code is deliberately NOT asserted: the hostile path is a valid
	// directory, so once the trap stops re-parsing it the scan legitimately
	// succeeds. Asserting a failure here would pin the wrong property. What must
	// be pinned instead is that the run actually RAN — otherwise the injection
	// check above is satisfied by the guard dying early for some unrelated
	// reason, which is a negative control with no positive leg.
	if !strings.Contains(stdout, classFired("diff-linewise-ci")) {
		t.Errorf("the scan must still have run under a hostile TMPDIR; stdout: %q", stdout)
	}
}

package hooks

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The filesystem here is REAL (t.TempDir): symlinks, dangling links and mode
// bits are the subject of this check, so mocking them would test the mock. Only
// the git answers are injected — each one maps 1:1 onto a real git command, so
// these tests never reimplement git's config precedence rules.

type verifyFixture struct {
	deps     *VerifyDeps
	hooksDir string
	topLevel string
	origins  []ConfigOrigin
	originer func() ([]ConfigOrigin, error)
}

// newVerifyFixture builds a correctly-installed hooks dir: both hook points
// present, executable, each dispatching to an executable helper beside it.
// That is the NEGATIVE CONTROL baseline — every positive-control test below
// mutates exactly one thing away from it, so a failure is attributable.
func newVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	top := t.TempDir()
	hooksDir := filepath.Join(top, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	f := &verifyFixture{hooksDir: hooksDir, topLevel: top}
	for point, helpers := range guardHelpers {
		helper := helpers[0]
		writeExec(t, filepath.Join(hooksDir, point), "#!/bin/sh\n\"$SPRAWL_HOOKS_DIR\"/"+helper+"\n")
		writeExec(t, filepath.Join(hooksDir, helper), "#!/bin/sh\nexit 0\n")
	}
	f.deps = &VerifyDeps{
		HooksPathOrigins: func() ([]ConfigOrigin, error) {
			if f.originer != nil {
				return f.originer()
			}
			return f.origins, nil
		},
		ResolvedHooksDir: func() (string, error) { return f.hooksDir, nil },
		CommonDir:        func() (string, error) { return filepath.Join(top, ".git"), nil },
		GitDir:           func() (string, error) { return filepath.Join(top, ".git"), nil },
		TopLevel:         func() (string, error) { return top, nil },
		Getwd:            func() (string, error) { return top, nil },
		Getenv:           func(string) string { return "" },
		Lstat:            os.Lstat,
		Stat:             os.Stat,
		Readlink:         os.Readlink,
		EvalSymlinks:     filepath.EvalSymlinks,
		ReadFile:         os.ReadFile,
	}
	return f
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func mustVerify(t *testing.T, f *verifyFixture) Report {
	t.Helper()
	r, err := Verify(f.deps)
	if err != nil {
		t.Fatalf("Verify returned an unexpected error: %v", err)
	}
	return r
}

// reasonSet renders the report's reason codes for comparison.
func reasonSet(r Report) string { return strings.Join(r.Reasons, ",") }

func hookByPoint(t *testing.T, r Report, point string) HookLink {
	t.Helper()
	for _, h := range r.Hooks {
		if h.Point == point {
			return h
		}
	}
	t.Fatalf("report has no entry for hook point %q — a hook point that is silently omitted reads as fine", point)
	return HookLink{}
}

// --- NEGATIVE CONTROL -------------------------------------------------------

func TestVerify_CorrectInstall_IsArmedAndSilent(t *testing.T) {
	f := newVerifyFixture(t)
	r := mustVerify(t, f)

	if r.Verdict != VerdictArmed {
		t.Errorf("negative control (correct install): Verdict = %q, want ARMED (reasons: %v)", r.Verdict, r.Reasons)
	}
	if len(r.Reasons) != 0 {
		t.Errorf("negative control: Reasons = %v, want none", r.Reasons)
	}
	if len(r.Fixes) != 0 {
		t.Errorf("negative control: Fixes = %v, want none — a remedy for a healthy tree is noise", r.Fixes)
	}
	// Both hook points must be present. Reporting on one and omitting the other
	// is the failure mode that lets reference-transaction rot unnoticed.
	if len(r.Hooks) != 2 {
		t.Fatalf("Hooks = %d entries, want 2 (pre-commit and reference-transaction)", len(r.Hooks))
	}
	for _, h := range r.Hooks {
		if h.Status != StatusOK {
			t.Errorf("negative control: %s status = %q, want OK", h.Point, h.Status)
		}
	}
}

// --- POSITIVE CONTROLS, per hook point --------------------------------------

// Each mutation is applied to BOTH hook points in turn: pre-commit and
// reference-transaction are separate guards, and an implementation that
// inspects only the first must not survive.
func TestVerify_BrokenHook_IsDisarmed_BothPoints(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, f *verifyFixture, point string)
		want    HookStatus
		wantFix bool
	}{
		{
			name: "hook file deleted",
			mutate: func(t *testing.T, f *verifyFixture, point string) {
				if err := os.Remove(filepath.Join(f.hooksDir, point)); err != nil {
					t.Fatalf("remove: %v", err)
				}
			},
			want:    StatusMissing,
			wantFix: true,
		},
		{
			name: "hook present but not executable",
			mutate: func(t *testing.T, f *verifyFixture, point string) {
				if err := os.Chmod(filepath.Join(f.hooksDir, point), 0o644); err != nil {
					t.Fatalf("chmod: %v", err)
				}
			},
			want:    StatusNotExecutable,
			wantFix: true,
		},
		{
			name: "hook is a dangling symlink",
			mutate: func(t *testing.T, f *verifyFixture, point string) {
				p := filepath.Join(f.hooksDir, point)
				if err := os.Remove(p); err != nil {
					t.Fatalf("remove: %v", err)
				}
				if err := os.Symlink(filepath.Join(f.hooksDir, "gone-"+point), p); err != nil {
					t.Fatalf("symlink: %v", err)
				}
			},
			want:    StatusDangling,
			wantFix: true,
		},
		{
			name: "hook runs, but reaches no guard at all",
			mutate: func(t *testing.T, f *verifyFixture, point string) {
				writeExec(t, filepath.Join(f.hooksDir, point), "#!/bin/sh\nexit 0\n")
			},
			want:    StatusNoGuard,
			wantFix: true,
		},
		{
			name: "hook names its guard, but the helper was deleted",
			mutate: func(t *testing.T, f *verifyFixture, point string) {
				if err := os.Remove(filepath.Join(f.hooksDir, guardHelpers[point][0])); err != nil {
					t.Fatalf("remove helper: %v", err)
				}
			},
			want:    StatusGuardUnreach,
			wantFix: true,
		},
		{
			name: "hook names its guard, but the helper is not executable",
			mutate: func(t *testing.T, f *verifyFixture, point string) {
				if err := os.Chmod(filepath.Join(f.hooksDir, guardHelpers[point][0]), 0o644); err != nil {
					t.Fatalf("chmod helper: %v", err)
				}
			},
			want:    StatusGuardUnreach,
			wantFix: true,
		},
		{
			name: "the hook path is a directory",
			mutate: func(t *testing.T, f *verifyFixture, point string) {
				p := filepath.Join(f.hooksDir, point)
				if err := os.Remove(p); err != nil {
					t.Fatalf("remove: %v", err)
				}
				if err := os.Mkdir(p, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			},
			want:    StatusNotAFile,
			wantFix: true,
		},
	}

	for _, tc := range cases {
		for _, point := range []string{"pre-commit", "reference-transaction"} {
			t.Run(tc.name+"/"+point, func(t *testing.T) {
				f := newVerifyFixture(t)
				tc.mutate(t, f, point)
				r := mustVerify(t, f)

				if r.Verdict != VerdictDisarmed {
					t.Errorf("positive control (%s at %s): Verdict = %q, want DISARMED", tc.name, point, r.Verdict)
				}
				got := hookByPoint(t, r, point)
				if got.Status != tc.want {
					t.Errorf("positive control (%s at %s): status = %q, want %q", tc.name, point, got.Status, tc.want)
				}
				wantReason := point + ":" + string(tc.want)
				if !containsStr(r.Reasons, wantReason) {
					t.Errorf("Reasons = %v, want to contain %q — a defect without a machine-readable reason cannot be told apart from any other", r.Reasons, wantReason)
				}
				if tc.wantFix && len(r.Fixes) == 0 {
					t.Errorf("positive control (%s at %s): no FIX offered; a disarmed guard with no remedy leaves the caller stuck", tc.name, point)
				}
				// The OTHER hook must stay OK. Condemning every hook whenever
				// one breaks would satisfy a weaker assertion and hide which
				// guard actually failed.
				for _, h := range r.Hooks {
					if h.Point != point && h.Status != StatusOK {
						t.Errorf("unrelated hook %s = %q, want OK: a per-hook defect must not be reported as a blanket failure", h.Point, h.Status)
					}
				}
			})
		}
	}
}

func TestVerify_DistinctFailureModes_HaveDistinctReasons(t *testing.T) {
	mutations := map[string]func(t *testing.T, f *verifyFixture){
		"missing":       func(t *testing.T, f *verifyFixture) { os.Remove(filepath.Join(f.hooksDir, "pre-commit")) },
		"notExecutable": func(t *testing.T, f *verifyFixture) { os.Chmod(filepath.Join(f.hooksDir, "pre-commit"), 0o644) },
		"noGuard": func(t *testing.T, f *verifyFixture) {
			writeExec(t, filepath.Join(f.hooksDir, "pre-commit"), "#!/bin/sh\nexit 0\n")
		},
		"guardUnreached": func(t *testing.T, f *verifyFixture) { os.Remove(filepath.Join(f.hooksDir, HelperCommitGuard)) },
	}

	seen := map[string]string{}
	for name, mutate := range mutations {
		f := newVerifyFixture(t)
		mutate(t, f)
		got := reasonSet(mustVerify(t, f))
		if got == "" {
			t.Errorf("%s: emitted no reason code at all", name)
			continue
		}
		if prior, dup := seen[got]; dup {
			t.Errorf("%s and %s both report reasons %q — distinct failure modes must be distinguishable, or the report cannot tell an operator what to fix", name, prior, got)
		}
		seen[got] = name
	}
}

// --- the hooks directory itself ---------------------------------------------

func TestVerify_HooksDirNotUsable_ReportsBothPointsUnreachable(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *verifyFixture)
		want  HooksDirState
	}{
		{
			name: "core.hooksPath points at a directory that does not exist",
			setup: func(t *testing.T, f *verifyFixture) {
				f.hooksDir = filepath.Join(f.topLevel, "nope")
				f.origins = []ConfigOrigin{{Scope: "local", Origin: "file:.git/config", Value: "nope"}}
			},
			want: HooksDirMissing,
		},
		{
			name: "core.hooksPath points at a FILE (the worktree .git/hooks case)",
			setup: func(t *testing.T, f *verifyFixture) {
				p := filepath.Join(f.topLevel, "hooksfile")
				if err := os.WriteFile(p, nil, 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
				f.hooksDir = p
				f.origins = []ConfigOrigin{{Scope: "local", Origin: "file:.git/config", Value: "hooksfile"}}
			},
			want: HooksDirNotADir,
		},
		{
			name: "core.hooksPath points at a real but EMPTY directory",
			setup: func(t *testing.T, f *verifyFixture) {
				p := filepath.Join(f.topLevel, "emptyhooks")
				if err := os.Mkdir(p, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				f.hooksDir = p
			},
			want: HooksDirOK, // the dir is fine; the hooks inside it are not
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newVerifyFixture(t)
			tc.setup(t, f)
			r := mustVerify(t, f)

			if r.Verdict != VerdictDisarmed {
				t.Errorf("positive control: Verdict = %q, want DISARMED", r.Verdict)
			}
			if r.HooksDirState != tc.want {
				t.Errorf("HooksDirState = %q, want %q", r.HooksDirState, tc.want)
			}
			// Neither hook point may be silently omitted — an absent field
			// reads as fine, which is the failure mode this check exists for.
			if len(r.Hooks) != 2 {
				t.Fatalf("Hooks = %d entries, want 2", len(r.Hooks))
			}
			for _, h := range r.Hooks {
				if h.Status == StatusOK {
					t.Errorf("%s = OK, but the hooks directory is unusable", h.Point)
				}
				if !containsStr(r.Reasons, h.Point+":"+string(h.Status)) {
					t.Errorf("Reasons = %v, missing an entry for %s", r.Reasons, h.Point)
				}
			}
		})
	}
}

// --- core.hooksPath attribution ---------------------------------------------

func TestVerify_NamesTheWinningScope_NotTheFirstListed(t *testing.T) {
	f := newVerifyFixture(t)
	// git lists scopes in precedence order and obeys the LAST. Reporting the
	// first would blame the wrong file during a fleet-wide incident.
	f.origins = []ConfigOrigin{
		{Scope: "global", Origin: "file:/home/someone/.gitconfig", Value: "/broken"},
		{Scope: "local", Origin: "file:.git/config", Value: f.hooksDir},
	}
	r := mustVerify(t, f)

	if r.Verdict != VerdictArmed {
		t.Fatalf("Verdict = %q, want ARMED: local overrides the broken global (reasons %v)", r.Verdict, r.Reasons)
	}
	if r.HooksPath != f.hooksDir {
		t.Errorf("HooksPath = %q, want the LAST scope's value %q", r.HooksPath, f.hooksDir)
	}
	if len(r.HooksPathOrigin) != 2 {
		t.Fatalf("HooksPathOrigin = %d entries, want both scopes listed", len(r.HooksPathOrigin))
	}
	if got := r.HooksPathOrigin[1].Scope; got != "local" {
		t.Errorf("winning scope = %q, want %q", got, "local")
	}
}

func TestVerify_AbsolutizesRelativeOriginAgainstTopLevel(t *testing.T) {
	f := newVerifyFixture(t)
	// git prints `file:.git/config` in the main checkout — relative to the
	// top-level, and unchanged by cwd. Absolutizing against cwd would name a
	// path that does not exist. cwd is set to a subdirectory here so the two
	// resolutions genuinely differ.
	sub := filepath.Join(f.topLevel, "sub", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f.deps.Getwd = func() (string, error) { return sub, nil }
	f.origins = []ConfigOrigin{{Scope: "local", Origin: "file:.git/config", Value: f.hooksDir}}

	r := mustVerify(t, f)

	want := filepath.Join(f.topLevel, ".git", "config")
	if got := r.HooksPathOrigin[0].Origin; got != want {
		t.Errorf("origin = %q, want %q (absolutized against the top-level, not cwd)", got, want)
	}
}

func TestVerify_NonFileOriginIsPassedThroughVerbatim(t *testing.T) {
	f := newVerifyFixture(t)
	// A `git -c core.hooksPath=...` one-shot has no config file behind it.
	// Rewriting "command line:" into a filesystem path would be a lie.
	f.origins = []ConfigOrigin{{Scope: "command", Origin: "command line:", Value: f.hooksDir}}
	r := mustVerify(t, f)
	if got := r.HooksPathOrigin[0].Origin; got != "command line:" {
		t.Errorf("origin = %q, want it passed through verbatim", got)
	}
}

// --- worktree awareness -----------------------------------------------------

func TestVerify_LinkedWorktree_IsFlagged(t *testing.T) {
	f := newVerifyFixture(t)
	// In a linked worktree the per-worktree git dir differs from the shared
	// common dir, and the hooks live in the common one.
	f.deps.GitDir = func() (string, error) {
		return filepath.Join(f.topLevel, ".git", "worktrees", "wt"), nil
	}
	if r := mustVerify(t, f); !r.LinkedWorktree {
		t.Error("LinkedWorktree = false in a linked worktree, want true")
	}
	// NEGATIVE CONTROL: in the main checkout the two are equal.
	g := newVerifyFixture(t)
	if r := mustVerify(t, g); r.LinkedWorktree {
		t.Error("LinkedWorktree = true in the main checkout, want false")
	}
}

// --- degrade loudly ---------------------------------------------------------

func TestVerify_GitProbeFails_IsUnknownNeverArmed(t *testing.T) {
	cases := []struct {
		name     string
		sabotage func(f *verifyFixture)
	}{
		{
			name: "core.hooksPath is unreadable",
			sabotage: func(f *verifyFixture) {
				f.originer = func() ([]ConfigOrigin, error) { return nil, errors.New("git exploded") }
			},
		},
		{
			name: "not a git repository",
			sabotage: func(f *verifyFixture) {
				f.deps.ResolvedHooksDir = func() (string, error) { return "", errors.New("not a git repository") }
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newVerifyFixture(t)
			tc.sabotage(f)

			r, err := Verify(f.deps)
			if err == nil {
				t.Fatal("Verify returned nil error for a chain it could not resolve; the caller cannot distinguish this from success")
			}
			if r.Verdict != VerdictUnknown {
				t.Errorf("Verdict = %q, want UNKNOWN", r.Verdict)
			}
			if r.Verdict == VerdictArmed {
				t.Error("an undetermined chain was reported ARMED — 'could not determine' must never collapse into 'fine'")
			}
			if r.UnknownReason == "" {
				t.Error("UnknownReason is empty; the check must say WHY it could not run")
			}
		})
	}
}

// --- report rendering -------------------------------------------------------

func TestPrintReport_PrintsSubjectBeforeVerdict(t *testing.T) {
	f := newVerifyFixture(t)
	var buf bytes.Buffer
	PrintReport(&buf, mustVerify(t, f))
	out := buf.String()

	chain := strings.Index(out, "resolved hooks dir:")
	verdict := strings.Index(out, "VERDICT:")
	if chain < 0 {
		t.Fatalf("report never prints the hooks dir it resolved:\n%s", out)
	}
	if verdict < 0 {
		t.Fatalf("report never prints a verdict:\n%s", out)
	}
	if chain > verdict {
		t.Error("the verdict is printed before the subject; a reader cannot check the probe's aim before reading its conclusion")
	}
	// The subject includes the specific paths, not just a verdict.
	for _, want := range []string{f.hooksDir, "linked worktree:", "core.hooksPath:"} {
		if !strings.Contains(out, want) {
			t.Errorf("report omits %q:\n%s", want, out)
		}
	}
}

func TestPrintReport_UnknownSaysSoAndNeverClaimsArmed(t *testing.T) {
	f := newVerifyFixture(t)
	f.deps.ResolvedHooksDir = func() (string, error) { return "", errors.New("not a git repository") }
	r, _ := Verify(f.deps)

	var buf bytes.Buffer
	PrintReport(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "VERDICT: UNKNOWN") {
		t.Errorf("report does not state UNKNOWN:\n%s", out)
	}
	if strings.Contains(out, "VERDICT: ARMED") {
		t.Errorf("report claims ARMED for a chain it could not resolve:\n%s", out)
	}
	if !strings.Contains(out, "not a git repository") {
		t.Errorf("report omits the reason it could not run:\n%s", out)
	}
}

func TestPrintReport_EachStatusRendersItsOwnPathAndCode(t *testing.T) {
	f := newVerifyFixture(t)
	if err := os.Chmod(filepath.Join(f.hooksDir, "pre-commit"), 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	var buf bytes.Buffer
	PrintReport(&buf, mustVerify(t, f))
	out := buf.String()

	want := fmt.Sprintf("  pre-commit: NOT-EXECUTABLE %s (mode 0644)", filepath.Join(f.hooksDir, "pre-commit"))
	if !strings.Contains(out, want) {
		t.Errorf("report omits %q — a status unbound from its subject is unactionable:\n%s", want, out)
	}
	if !strings.Contains(out, "REASON: pre-commit:NOT-EXECUTABLE") {
		t.Errorf("report omits the machine-readable reason code:\n%s", out)
	}
	if !strings.Contains(out, "FIX: ") {
		t.Errorf("report offers no remedy:\n%s", out)
	}
}

// A guard MENTIONED is not a guard DISPATCHED. Reported by review of 9c20e47:
// a hook gutted down to `# disabled: guard-main-commit` + `exit 0` was reported
// ARMED, which is the exact false-clean this whole check exists to prevent.
func TestVerify_GuardNamedOnlyInAComment_IsNotArmed(t *testing.T) {
	for _, point := range []string{"pre-commit", "reference-transaction"} {
		t.Run(point, func(t *testing.T) {
			f := newVerifyFixture(t)
			// The helper is still present and executable — only the DISPATCH is
			// gone, so nothing but the body check can catch this.
			writeExec(t, filepath.Join(f.hooksDir, point),
				"#!/bin/sh\n# disabled: "+guardHelpers[point][0]+"\nexit 0\n")

			r := mustVerify(t, f)
			if r.Verdict != VerdictDisarmed {
				t.Errorf("positive control (guard only mentioned in a comment at %s): Verdict = %q, want DISARMED — a hook that runs nothing must never report armed", point, r.Verdict)
			}
			if got := hookByPoint(t, r, point); got.Status != StatusNoGuard {
				t.Errorf("status = %q, want %q", got.Status, StatusNoGuard)
			}
		})
	}
}

// Under `make hooks` the reference-transaction hook IS the guard script, so
// there is no dispatch to find and the only occurrence of the guard name in the
// file is a header comment. Keying on that comment made `make validate` fail
// fleet-wide when the comment was reworded, with a FIX that could not fix it.
// Armament here is structural: the hook resolves TO the guard.
func TestVerify_HookIsItselfTheGuard_IsArmedWithoutMatchingProse(t *testing.T) {
	f := newVerifyFixture(t)
	helper := guardHelpers["reference-transaction"][1] // the bare `make hooks` name
	scripts := filepath.Join(f.topLevel, "scripts")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	guard := filepath.Join(scripts, helper)
	// Deliberately NO mention of its own name anywhere in the body.
	writeExec(t, guard, "#!/bin/sh\nexit 0\n")

	hook := filepath.Join(f.hooksDir, "reference-transaction")
	if err := os.Remove(hook); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(guard, hook); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	r := mustVerify(t, f)
	if r.Verdict != VerdictArmed {
		t.Errorf("negative control (hook symlinked directly to the guard): Verdict = %q, want ARMED (reasons %v) — this is the shipped `make hooks` arrangement", r.Verdict, r.Reasons)
	}
	if got := hookByPoint(t, r, "reference-transaction"); got.Status != StatusOK {
		t.Errorf("status = %q, want OK", got.Status)
	}
}

// The verdict must not depend on prose in a tracked script: rewording a comment
// is a zero-behaviour change and must not disarm anything.
func TestVerify_RewordingAComment_DoesNotChangeTheVerdict(t *testing.T) {
	f := newVerifyFixture(t)
	helper := guardHelpers["reference-transaction"][1]
	scripts := filepath.Join(f.topLevel, "scripts")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	guard := filepath.Join(scripts, helper)
	writeExec(t, guard, "#!/bin/sh\n# "+helper+" (QUM-837)\nexit 0\n")
	hook := filepath.Join(f.hooksDir, "reference-transaction")
	if err := os.Remove(hook); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(guard, hook); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	before := mustVerify(t, f).Verdict

	writeExec(t, guard, "#!/bin/sh\n# main-ref guard (QUM-837)\nexit 0\n")
	after := mustVerify(t, f).Verdict

	if before != after {
		t.Errorf("rewording a comment flipped the verdict %q -> %q; validate must not be coupled to prose in a tracked script", before, after)
	}
	if after != VerdictArmed {
		t.Errorf("Verdict = %q, want ARMED", after)
	}
}

func TestMatchedHelper_RequiresADispatchNotAMention(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"sprawl install dispatch", `"$SPRAWL_HOOKS_DIR"/sprawl-guard-main-commit "$branch"`, HelperCommitGuard},
		{"make hooks dispatch", `"$here/guard-main-commit"`, "guard-main-commit"},
		{"bare mention in a comment", "# disabled: guard-main-commit", ""},
		{"commented-out dispatch", `# "$here/guard-main-commit"`, ""},
		{"prose mention with a path", "# see scripts/guard-main-commit for details", ""},
		{"no guard at all", "#!/bin/sh\nexit 0\n", ""},
	}
	for _, tc := range cases {
		if got := matchedHelper("pre-commit", tc.body); got != tc.want {
			t.Errorf("%s: matchedHelper(%q) = %q, want %q", tc.name, tc.body, got, tc.want)
		}
	}
}

func TestMatchedHelper_PrefersTheSprawlPrefixedName(t *testing.T) {
	// "sprawl-guard-main-commit" CONTAINS "guard-main-commit", so a naive
	// ordering resolves the helper to a filename that does not exist.
	if got := matchedHelper("pre-commit", `"$SPRAWL_HOOKS_DIR"/sprawl-guard-main-commit`); got != HelperCommitGuard {
		t.Errorf("matchedHelper = %q, want %q", got, HelperCommitGuard)
	}
	// The `make hooks` arrangement uses the bare name.
	if got := matchedHelper("pre-commit", `"$here/guard-main-commit"`); got != "guard-main-commit" {
		t.Errorf("matchedHelper = %q, want %q", got, "guard-main-commit")
	}
	if got := matchedHelper("pre-commit", "#!/bin/sh\nexit 0\n"); got != "" {
		t.Errorf("matchedHelper = %q for a hook with no guard, want %q", got, "")
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

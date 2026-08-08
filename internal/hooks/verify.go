package hooks

import (
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Verify answers one question: is the guard chain armed for THIS working tree,
// right now, and what exactly will run?
//
// It exists because git treats a misconfigured hooks path as "no hooks" and
// exits 0. `git -c core.hooksPath=/nonexistent commit` runs nothing, warns
// nothing, and fails nothing — which silently voids the QUM-808 pre-commit
// guard, the QUM-837 reference-transaction backstop (the one class git does not
// skip under --no-verify), and the pre-commit `make validate` gate, all at once.
// A dangling symlink, a lost executable bit, or a deleted guard helper disarm
// the same way. None of those states is distinguishable from a healthy one
// without resolving the whole chain and looking, so that is what this does
// (QUM-951).
//
// Two properties are deliberate:
//
//   - The report prints its SUBJECT — the hooks dir it resolved, which config
//     scope set it, the symlink targets it followed, the modes it found — before
//     it prints any verdict, so a reader can check the probe's aim rather than
//     trusting it.
//   - An undetermined chain is VerdictUnknown, never VerdictArmed. "Could not
//     determine" collapsing into "fine" is the defect class this check is for.

// Verdict is the overall armament state of the guard chain.
type Verdict string

const (
	VerdictArmed    Verdict = "ARMED"
	VerdictDisarmed Verdict = "DISARMED"
	VerdictUnknown  Verdict = "UNKNOWN"
)

// HookStatus classifies one hook point. Each value renders a distinct message
// and a distinct REASON code, because "the hooks are broken" costs an operator
// the whole hunt.
type HookStatus string

const (
	StatusOK              HookStatus = "OK"
	StatusHooksDirMissing HookStatus = "HOOKS-DIR-MISSING"
	StatusMissing         HookStatus = "MISSING"
	StatusDangling        HookStatus = "DANGLING"
	StatusNotExecutable   HookStatus = "NOT-EXECUTABLE"
	StatusNotAFile        HookStatus = "NOT-A-FILE"
	StatusNoGuard         HookStatus = "NO-GUARD"
	StatusGuardUnreach    HookStatus = "GUARD-UNREACHABLE"
	StatusUnreadable      HookStatus = "UNREADABLE"
)

// HooksDirState describes the directory the hooks are expected to live in.
type HooksDirState string

const (
	HooksDirOK        HooksDirState = "OK"
	HooksDirMissing   HooksDirState = "MISSING"
	HooksDirNotADir   HooksDirState = "NOT-A-DIRECTORY"
	hooksPathUnsetOut               = "(unset)"
)

// guardHelpers lists, per hook point, the helper basenames a hook may dispatch
// to. Both install arrangements invoke a helper sitting beside the hook's real
// path: `sprawl hooks install` writes sprawl-guard-main-* into the hooks dir,
// and `make hooks` symlinks to scripts/pre-commit, which runs
// "$here/guard-main-commit". The sprawl-prefixed name is checked first because
// it *contains* the bare name as a substring.
var guardHelpers = map[string][]string{
	"pre-commit":            {HelperCommitGuard, "guard-main-commit"},
	"reference-transaction": {HelperRefGuard, "guard-main-ref"},
}

// ConfigOrigin is one config scope that sets core.hooksPath. git obeys the
// LAST one, so the whole list is reported and the winner is named.
type ConfigOrigin struct {
	Scope  string // "system", "global", "local", "worktree", "command"
	Origin string // absolutized config file path, or git's raw label
	Value  string
}

// HookLink is the fully-resolved chain for one hook point.
type HookLink struct {
	Point      string // "pre-commit" | "reference-transaction"
	Path       string // <hooksDir>/<Point>
	SymlinkTo  string // raw readlink target; "" when not a symlink
	RealPath   string // fully resolved; "" when it does not resolve
	Mode       fs.FileMode
	Helper     string // resolved guard helper path, when one was referenced
	HelperName string // the basename that matched
	Status     HookStatus
	Detail     string // the parenthetical / trailing clause, per status
}

// Report is the resolved chain plus the verdict derived from it.
type Report struct {
	Cwd            string
	TopLevel       string
	CommonDir      string
	GitDir         string
	LinkedWorktree bool
	GitEnv         map[string]string

	HooksPath       string
	HooksPathOrigin []ConfigOrigin
	HooksDir        string
	HooksDirState   HooksDirState

	Hooks   []HookLink
	Verdict Verdict
	Reasons []string
	Fixes   []string

	UnknownReason string
}

// VerifyDeps injects the git answers and the filesystem probes. The git config
// resolution is deliberately NOT modelled in Go — each function maps 1:1 onto a
// real git command, so tests inject real git's answers rather than a
// reimplementation of its precedence rules. The filesystem probes are real:
// symlinks, dangling links and mode bits are the subject of this check, and
// mocking them would test the mock.
type VerifyDeps struct {
	HooksPathOrigins func() ([]ConfigOrigin, error)
	ResolvedHooksDir func() (string, error)
	CommonDir        func() (string, error)
	GitDir           func() (string, error)
	TopLevel         func() (string, error)
	Getwd            func() (string, error)
	Getenv           func(string) string

	Lstat        func(string) (fs.FileInfo, error)
	Stat         func(string) (fs.FileInfo, error)
	Readlink     func(string) (string, error)
	EvalSymlinks func(string) (string, error)
	ReadFile     func(string) ([]byte, error)
}

// gitEnvVars are the overrides that can redirect git's idea of the repo out
// from under a caller. Reported when set, because they change what the rest of
// the chain even refers to.
var gitEnvVars = []string{
	"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY", "GIT_NAMESPACE", "GIT_PREFIX",
}

// Verify resolves the chain and classifies it. An unresolvable chain is
// VerdictUnknown with a non-nil error — never a silently-empty ARMED.
func Verify(deps *VerifyDeps) (Report, error) {
	r := Report{GitEnv: map[string]string{}}

	if cwd, err := deps.Getwd(); err == nil {
		r.Cwd = cwd
	}
	for _, k := range gitEnvVars {
		if v := deps.Getenv(k); v != "" {
			r.GitEnv[k] = v
		}
	}

	r.TopLevel, _ = deps.TopLevel()

	origins, err := deps.HooksPathOrigins()
	if err != nil {
		return unknown(r, fmt.Sprintf("could not read core.hooksPath: %v", err))
	}
	for _, o := range origins {
		o.Origin = absolutizeOrigin(o.Origin, r.TopLevel)
		r.HooksPathOrigin = append(r.HooksPathOrigin, o)
	}
	if n := len(r.HooksPathOrigin); n > 0 {
		// git obeys the LAST scope, so that is the effective value.
		r.HooksPath = r.HooksPathOrigin[n-1].Value
	}

	hooksDir, err := deps.ResolvedHooksDir()
	if err != nil {
		return unknown(r, fmt.Sprintf("could not resolve the hooks directory — not a git repository, or git is unavailable: %v", err))
	}
	r.HooksDir = hooksDir

	r.CommonDir, _ = deps.CommonDir()
	r.GitDir, _ = deps.GitDir()
	r.LinkedWorktree = r.CommonDir != "" && r.GitDir != "" && r.CommonDir != r.GitDir

	switch fi, serr := deps.Stat(hooksDir); {
	case serr != nil:
		r.HooksDirState = HooksDirMissing
	case !fi.IsDir():
		r.HooksDirState = HooksDirNotADir
	default:
		r.HooksDirState = HooksDirOK
	}

	for _, hp := range hookPoints {
		r.Hooks = append(r.Hooks, inspectHook(deps, r, hp.file))
	}

	classify(&r)
	return r, nil
}

func unknown(r Report, reason string) (Report, error) {
	r.Verdict = VerdictUnknown
	r.UnknownReason = reason
	return r, fmt.Errorf("%s", reason)
}

// absolutizeOrigin turns git's origin label into an absolute path where it
// names one. In the MAIN checkout git prints `file:.git/config` — relative to
// the repo top-level, and unchanged by cwd — so it must be joined against the
// top-level, NOT the process cwd. (From a linked worktree git already prints an
// absolute path.) Non-file origins ("command line:", "standard input:") have no
// path and are passed through verbatim.
func absolutizeOrigin(origin, topLevel string) string {
	path, ok := strings.CutPrefix(origin, "file:")
	if !ok {
		return origin
	}
	if filepath.IsAbs(path) || topLevel == "" {
		return path
	}
	return filepath.Join(topLevel, path)
}

// inspectHook resolves one hook point end to end: symlink target, real path,
// mode, executability, and whether a guard is genuinely reachable from it.
// Existence is not armament, so neither the content check nor the helper check
// is optional.
func inspectHook(deps *VerifyDeps, r Report, point string) HookLink {
	h := HookLink{Point: point, Path: filepath.Join(r.HooksDir, point)}

	if r.HooksDirState != HooksDirOK {
		h.Status = StatusHooksDirMissing
		return h
	}

	li, err := deps.Lstat(h.Path)
	if err != nil {
		h.Status = StatusMissing
		return h
	}
	if li.Mode()&fs.ModeSymlink != 0 {
		if tgt, lerr := deps.Readlink(h.Path); lerr == nil {
			h.SymlinkTo = tgt
		}
	}

	resolved, err := deps.EvalSymlinks(h.Path)
	if err != nil {
		h.Status = StatusDangling
		h.Detail = h.SymlinkTo
		return h
	}
	h.RealPath = resolved

	fi, err := deps.Stat(h.Path)
	if err != nil {
		h.Status = StatusUnreadable
		h.Detail = err.Error()
		return h
	}
	h.Mode = fi.Mode().Perm()
	if !fi.Mode().IsRegular() {
		h.Status = StatusNotAFile
		return h
	}
	if h.Mode&0o111 == 0 {
		h.Status = StatusNotExecutable
		return h
	}

	body, err := deps.ReadFile(h.Path)
	if err != nil {
		h.Status = StatusUnreadable
		h.Detail = err.Error()
		return h
	}

	// A hook can name a guard that has since been deleted or chmod-ed and still
	// look perfectly healthy in `ls`, so "reachable" means the helper is really
	// there, beside the hook's real path, and really executable.
	h.HelperName = matchedHelper(point, string(body))
	if h.HelperName == "" {
		h.Status = StatusNoGuard
		return h
	}
	h.Helper = filepath.Join(filepath.Dir(h.RealPath), h.HelperName)
	hfi, err := deps.Stat(h.Helper)
	switch {
	case err != nil:
		h.Status = StatusGuardUnreach
		h.Detail = "missing"
	case hfi.Mode().Perm()&0o111 == 0:
		h.Status = StatusGuardUnreach
		h.Detail = "not executable"
	default:
		h.Status = StatusOK
	}
	return h
}

// matchedHelper returns the guard helper basename the hook body references, or
// "" when it references none.
func matchedHelper(point, body string) string {
	for _, name := range guardHelpers[point] {
		if strings.Contains(body, name) {
			return name
		}
	}
	return ""
}

// classify is the pure core: a populated Report in, verdict + reasons + fixes
// out. No I/O, so the whole decision table is unit-testable directly.
func classify(r *Report) {
	if r.HooksDirState != HooksDirOK {
		r.Reasons = append(r.Reasons, "hooks-dir:"+string(r.HooksDirState))
	}
	needsInstall := false
	for _, h := range r.Hooks {
		if h.Status == StatusOK {
			continue
		}
		r.Reasons = append(r.Reasons, h.Point+":"+string(h.Status))
		if h.Status != StatusHooksDirMissing {
			needsInstall = true
		}
	}

	if len(r.Reasons) == 0 {
		r.Verdict = VerdictArmed
		return
	}
	r.Verdict = VerdictDisarmed

	// Clearing a bad core.hooksPath comes first: while it is set, reinstalling
	// hooks would only write them into the wrong directory.
	if n := len(r.HooksPathOrigin); n > 0 && r.HooksDirState != HooksDirOK {
		w := r.HooksPathOrigin[n-1]
		r.Fixes = append(r.Fixes, fmt.Sprintf("git config --%s --unset core.hooksPath   # set in %s", w.Scope, w.Origin))
	}
	if needsInstall || r.HooksDirState != HooksDirOK {
		r.Fixes = append(r.Fixes, "make hooks   # run this from the MAIN checkout, not a worktree")
	}
	sort.Strings(r.Reasons)
}

// PrintReport writes the resolved chain first and the verdict last, so a reader
// can audit what the probe actually looked at before reading what it concluded.
func PrintReport(w io.Writer, r Report) {
	fmt.Fprintf(w, "sprawl hooks verify — resolving the guard chain for this working tree\n\n")
	fmt.Fprintf(w, "cwd: %s\n", r.Cwd)
	fmt.Fprintf(w, "git top-level: %s\n", r.TopLevel)
	fmt.Fprintf(w, "git common dir: %s\n", r.CommonDir)
	fmt.Fprintf(w, "linked worktree: %s\n", yesNo(r.LinkedWorktree))
	fmt.Fprintf(w, "git env overrides: %s\n", formatGitEnv(r.GitEnv))

	if len(r.HooksPathOrigin) == 0 {
		fmt.Fprintf(w, "core.hooksPath: %s\n", hooksPathUnsetOut)
	} else {
		fmt.Fprintf(w, "core.hooksPath: %s\n", r.HooksPath)
		for _, o := range r.HooksPathOrigin {
			fmt.Fprintf(w, "  set by: %s %s\n", o.Scope, o.Origin)
		}
		win := r.HooksPathOrigin[len(r.HooksPathOrigin)-1]
		fmt.Fprintf(w, "  winning scope: %s %s\n", win.Scope, win.Origin)
	}

	if r.UnknownReason != "" {
		fmt.Fprintf(w, "\nVERDICT: %s\n", VerdictUnknown)
		fmt.Fprintf(w, "This check could not run: %s\n", r.UnknownReason)
		fmt.Fprintf(w, "Treat the guard as UNVERIFIED, not as armed — nothing here says your hooks work.\n")
		return
	}

	fmt.Fprintf(w, "resolved hooks dir: %s\n", r.HooksDir)
	fmt.Fprintf(w, "hooks dir state: %s\n", r.HooksDirState)
	for _, h := range r.Hooks {
		fmt.Fprintf(w, "  %s\n", formatHook(h))
	}

	fmt.Fprintln(w)
	for _, reason := range r.Reasons {
		fmt.Fprintf(w, "REASON: %s\n", reason)
	}
	fmt.Fprintf(w, "VERDICT: %s\n", r.Verdict)
	if r.Verdict == VerdictDisarmed {
		fmt.Fprintf(w, "git will not run every guard for this tree. A commit to the protected branch\n")
		fmt.Fprintf(w, "by a non-root agent may SUCCEED, silently.\n")
	}
	for _, fix := range r.Fixes {
		fmt.Fprintf(w, "FIX: %s\n", fix)
	}
}

// formatHook renders one hook point's line. Every variant names the exact path
// it is talking about — a status unbound from its subject is unactionable.
func formatHook(h HookLink) string {
	switch h.Status {
	case StatusOK:
		return fmt.Sprintf("%s: OK -> %s (mode %04o, guard %s reachable)", h.Point, h.RealPath, h.Mode, h.HelperName)
	case StatusDangling:
		return fmt.Sprintf("%s: DANGLING %s -> %s", h.Point, h.Path, h.Detail)
	case StatusNotExecutable:
		return fmt.Sprintf("%s: NOT-EXECUTABLE %s (mode %04o)", h.Point, h.Path, h.Mode)
	case StatusGuardUnreach:
		return fmt.Sprintf("%s: GUARD-UNREACHABLE %s (helper %s %s)", h.Point, h.Path, h.Helper, h.Detail)
	case StatusUnreadable:
		return fmt.Sprintf("%s: UNREADABLE %s (%s)", h.Point, h.Path, h.Detail)
	default:
		return fmt.Sprintf("%s: %s %s", h.Point, h.Status, h.Path)
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func formatGitEnv(env map[string]string) string {
	if len(env) == 0 {
		return "(none set)"
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+env[k])
	}
	return strings.Join(parts, " ")
}

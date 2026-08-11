package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// gcProcInfo describes a process candidate for sandbox-gc to inspect.
// Cmdline is the argv split, e.g. ["claude","--system-prompt-file=/tmp/sprawl-x/...",...].
type gcProcInfo struct {
	Pid     int
	Ppid    int
	UID     int
	Cmdline []string
}

// sandboxGCDeps holds dependencies for `sprawl sandbox-gc`. DI per CLAUDE.md.
type sandboxGCDeps struct {
	listSockets  func() ([]string, error)
	sessionsOn   func(socket string) (int, error)
	unlinkSocket func(path string) error
	listTmpDirs  func() ([]string, error)
	dirInfo      func(path string) (mtime time.Time, err error)
	removeAll    func(path string) error
	listProcs    func() ([]gcProcInfo, error)
	killProc     func(pid int) error
	currentUID   func() int
	now          func() time.Time
	out          io.Writer
}

var defaultSandboxGCDeps *sandboxGCDeps

var (
	sandboxGCDryRun bool
	sandboxGCMaxAge time.Duration
)

func init() {
	sandboxGCCmd.Flags().BoolVar(&sandboxGCDryRun, "dry-run", false, "Report what would be reaped without modifying anything")
	sandboxGCCmd.Flags().DurationVar(&sandboxGCMaxAge, "max-age", 2*time.Hour, "Reap sandbox tmp dirs older than this")
	rootCmd.AddCommand(sandboxGCCmd)
}

var sandboxGCCmd = &cobra.Command{
	Use:   "sandbox-gc",
	Short: "Reap leaked sandbox tmux sockets, /tmp dirs, and orphan claude processes",
	Long: `Janitor for QUM-458 e2e sandbox leaks. Sweeps:
  - /tmp/tmux-*/sprawl-* sockets with no live sessions
  - /tmp/sprawl-* dirs containing a ".sprawl" marker directly inside them
    (QUM-1119: any prefix a row picks, not an enumerated list), older than
    --max-age
  - claude processes whose --system-prompt-file is under /tmp/sprawl-* and
    whose ppid is 1 (orphaned by host death)

Idempotent. Only kills processes owned by the current UID.`,
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := resolveSandboxGCDeps()
		return runSandboxGC(deps, sandboxGCDryRun, sandboxGCMaxAge)
	},
}

func resolveSandboxGCDeps() *sandboxGCDeps {
	if defaultSandboxGCDeps != nil {
		return defaultSandboxGCDeps
	}
	return &sandboxGCDeps{
		listSockets:  defaultListSockets,
		sessionsOn:   defaultSessionsOn,
		unlinkSocket: os.Remove,
		listTmpDirs:  defaultListTmpDirs,
		dirInfo: func(path string) (time.Time, error) {
			fi, err := os.Stat(path)
			if err != nil {
				return time.Time{}, err
			}
			return fi.ModTime(), nil
		},
		removeAll: os.RemoveAll,
		listProcs: defaultListProcs,
		killProc: func(pid int) error {
			p, err := os.FindProcess(pid)
			if err != nil {
				return err
			}
			return p.Kill()
		},
		currentUID: os.Getuid,
		now:        time.Now,
		out:        os.Stdout,
	}
}

// runSandboxGC sweeps stale sockets, dirs, and orphan claude processes.
// QUM-458 layer 4 implementation.
func runSandboxGC(deps *sandboxGCDeps, dryRun bool, maxAge time.Duration) error {
	prefix := ""
	verb := "Swept"
	if dryRun {
		prefix = "[dry-run] "
		verb = "Would sweep"
	}

	socketsSwept := 0
	dirsSwept := 0
	procsKilled := 0
	pathsRefused := 0

	// 1. Sweep stale sockets.
	sockets, err := deps.listSockets()
	if err == nil {
		for _, sock := range sockets {
			n, sErr := deps.sessionsOn(sock)
			if sErr != nil || n == 0 {
				fmt.Fprintf(deps.out, "%ssocket %s: stale (sessions=%d)\n", prefix, sock, n)
				if !dryRun {
					_ = deps.unlinkSocket(sock)
				}
				socketsSwept++
			}
		}
	}

	// 2. Sweep stale tmpdirs.
	dirs, err := deps.listTmpDirs()
	if err == nil {
		now := deps.now()
		procs, _ := deps.listProcs()
		for _, dir := range dirs {
			// QUM-1119 safety net: refuse LOUDLY rather than silently skip.
			// listTmpDirs is production-populated by discoverSandboxTmpDirs
			// today, but it is an injected dependency (tests replace it
			// directly), so this is the backstop if discovery ever regresses
			// to something broader than "/tmp/sprawl-<something>". Never
			// silent: a reaper that quietly skips an unexpected path is
			// indistinguishable from one that swept it, to anyone reading
			// the output.
			if !isSandboxRootPath(dir) {
				fmt.Fprintf(deps.out, "%sREFUSING to remove unexpected path %s (not a direct /tmp/sprawl-* child)\n", prefix, dir)
				pathsRefused++
				continue
			}
			mtime, dErr := deps.dirInfo(dir)
			if dErr != nil {
				continue
			}
			if now.Sub(mtime) < maxAge {
				continue
			}
			referenced := false
			for _, p := range procs {
				for _, arg := range p.Cmdline {
					if strings.Contains(arg, dir) {
						referenced = true
						break
					}
				}
				if referenced {
					break
				}
			}
			if referenced {
				continue
			}
			fmt.Fprintf(deps.out, "%sdir %s: stale (mtime=%s)\n", prefix, dir, mtime.Format(time.RFC3339))
			if !dryRun {
				_ = deps.removeAll(dir)
			}
			dirsSwept++
		}
	}

	// 3. Kill orphan claude procs.
	procs, err := deps.listProcs()
	if err == nil {
		uid := deps.currentUID()
		for _, p := range procs {
			if p.Ppid != 1 {
				continue
			}
			if p.UID != uid {
				continue
			}
			if !isOrphanClaude(p.Cmdline) {
				continue
			}
			fmt.Fprintf(deps.out, "%sproc pid=%d: orphan claude under /tmp/sprawl-*\n", prefix, p.Pid)
			if !dryRun {
				_ = deps.killProc(p.Pid)
			}
			procsKilled++
		}
	}

	fmt.Fprintf(deps.out, "%s%s %d stale tmux socket(s), %d stale dir(s), %d orphan claude proc(s).\n",
		prefix, verb, socketsSwept, dirsSwept, procsKilled)
	// QUM-1119 code review: a refusal must be visible to a caller that only
	// reads the one-line summary (the Makefile invokes this as `... || true`,
	// so rc alone tells it nothing either). Folded into the summary rather
	// than only the per-path lines above it, and only when non-zero so the
	// common all-clean case keeps its existing shape.
	if pathsRefused > 0 {
		fmt.Fprintf(deps.out, "%sREFUSED %d unexpected path(s) — see lines above; nothing outside /tmp/sprawl-* was touched.\n", prefix, pathsRefused)
	}
	if dryRun {
		fmt.Fprintf(deps.out, "Next: re-run without --dry-run to apply, or 'sprawl sandbox-gc --max-age=10m' for tighter window.\n")
	} else {
		fmt.Fprintf(deps.out, "Next: sandbox state is now safe; re-run sandbox tests when ready.\n")
	}
	return nil
}

// isOrphanClaude reports whether the cmdline corresponds to a claude
// subprocess whose --system-prompt-file value points under /tmp/sprawl-.
func isOrphanClaude(cmdline []string) bool {
	if len(cmdline) == 0 {
		return false
	}
	hasClaude := false
	for _, a := range cmdline {
		if strings.Contains(a, "claude") {
			hasClaude = true
			break
		}
	}
	if !hasClaude {
		return false
	}
	for i, a := range cmdline {
		// --system-prompt-file=/tmp/sprawl-...
		if strings.HasPrefix(a, "--system-prompt-file=") {
			val := strings.TrimPrefix(a, "--system-prompt-file=")
			if strings.HasPrefix(val, "/tmp/sprawl-") {
				return true
			}
		}
		// --system-prompt-file /tmp/sprawl-...
		if a == "--system-prompt-file" && i+1 < len(cmdline) {
			if strings.HasPrefix(cmdline[i+1], "/tmp/sprawl-") {
				return true
			}
		}
	}
	return false
}

// defaultListSockets globs /tmp/tmux-*/sprawl-* for production deps.
func defaultListSockets() ([]string, error) {
	matches, err := filepath.Glob("/tmp/tmux-*/sprawl-*")
	if err != nil {
		return nil, err
	}
	return matches, nil
}

// defaultSessionsOn runs `tmux -L <socket> ls` with a short timeout. Returns
// session count (1 if non-empty output) or 0.
func defaultSessionsOn(socket string) (int, error) {
	// socket here is a full path like /tmp/tmux-1000/sprawl-foo; tmux's -L
	// expects a basename. Extract.
	name := filepath.Base(socket)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", "-L", name, "ls")
	out, err := cmd.Output()
	if err != nil {
		return 0, nil
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, nil
}

// defaultListTmpDirs discovers candidate sandbox roots under /tmp.
func defaultListTmpDirs() ([]string, error) {
	return discoverSandboxTmpDirs("/tmp")
}

// discoverSandboxTmpDirs finds sandbox roots directly under base by
// STRUCTURAL MARKER rather than by an enumerated prefix list (QUM-1119). The
// list this replaced — "sprawl-*-e2e-*", "sprawl-test-*", "sprawl-rb-*" —
// matched only a handful of the ~40 prefixes real e2e rows actually pass to
// e2e_make_sandbox_root (mostly "sprawl-qum<NNN>", with no "-e2e-"/"-test-"
// substring), so it silently reaped almost nothing while reporting success.
//
// Every real sandbox root is created by e2e_make_sandbox_root (which always
// names it "sprawl-<prefix>-XXXXXX" under /tmp) and then, for rows that boot
// the TUI, e2e_init_sandbox_repo (which does `mkdir -p "$SPRAWL_ROOT/.sprawl"`
// immediately inside it). A "sprawl-*" directory with a ".sprawl"
// subdirectory directly inside it is therefore true for every prefix a row
// picks, present or future, without enumerating any of them.
//
// This does NOT catch sandbox roots from rows that shell out to a bare
// `claude` binary directly rather than through `sprawl enter` (attach-blocks,
// capture-pane-liveness, replay-echo, recall-sendnow, as of QUM-1119) — those
// never create the marker. That is a known, narrower gap than the prefix list
// it replaces covered, not a claimed total fix; see QUM-1119's tracking notes.
//
// It also only globs DIRECT children of base. e2e_make_sandbox_root accepts
// any path matching `case /tmp/*`, so an agent (or a future row) with a
// non-default TMPDIR nested under /tmp (e.g. TMPDIR=/tmp/foo) produces a root
// at /tmp/foo/sprawl-x-XXXXXX that this glob — and isSandboxRootPath's
// direct-child check below — both refuse to see at all, leaking it silently.
// The production default (TMPDIR unset, defaulting to /tmp) is unaffected.
//
// Retained backup artifacts observed in the QUM-1119 incident
// (sprawl-*.bundle, sprawl-*.git, sprawl-agents-backup-*,
// sprawl-v0.2.7-known-good.bin) are excluded because none of them has a
// ".sprawl" marker directly inside — NOT by name, since a name denylist only
// protects artifacts whose name is known today.
//
// Symlinks are never followed, at either level: Lstat (not Stat) is used on
// both the candidate and its marker, so a symlinked "sprawl-*" name — or a
// symlinked ".sprawl" inside a real one — can never smuggle an out-of-tree
// path into the reap list.
func discoverSandboxTmpDirs(base string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(base, "sprawl-*"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range matches {
		lst, err := os.Lstat(m)
		if err != nil || lst.Mode()&os.ModeSymlink != 0 || !lst.IsDir() {
			continue // files (backup bundles, known-good binaries) and symlinks are never sandbox roots
		}
		marker, err := os.Lstat(filepath.Join(m, ".sprawl"))
		if err != nil || marker.Mode()&os.ModeSymlink != 0 || !marker.IsDir() {
			continue // no genuine .sprawl marker directly inside => not a root we created
		}
		out = append(out, m)
	}
	return out, nil
}

// isSandboxRootPath is the QUM-1119 safety net consulted immediately before
// any removal: a path must be a DIRECT child of /tmp named "sprawl-*", and
// must never be (or be inside) /tmp/coder-script-data — asserted explicitly
// rather than relying on the name-prefix check alone, since that path is host
// tooling state (a symlink into the developer's home dir) and deleting it
// would silently break `claude` PATH resolution for every needs_claude row.
// A pure path predicate: no disk access, so it is safe to call on a path that
// does not exist (or no longer exists) on disk.
func isSandboxRootPath(dir string) bool {
	clean := filepath.Clean(dir)
	if clean == "/tmp/coder-script-data" || strings.HasPrefix(clean, "/tmp/coder-script-data/") {
		return false
	}
	if filepath.Dir(clean) != "/tmp" {
		return false
	}
	return strings.HasPrefix(filepath.Base(clean), "sprawl-")
}

// defaultListProcs walks /proc/*/cmdline + /proc/*/status to gather pid/ppid/uid/argv.
func defaultListProcs() ([]gcProcInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []gcProcInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdlineBytes, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		if len(cmdlineBytes) == 0 {
			continue
		}
		// argv is NUL-separated, with a trailing NUL.
		raw := strings.TrimRight(string(cmdlineBytes), "\x00")
		argv := strings.Split(raw, "\x00")

		statusBytes, err := os.ReadFile("/proc/" + e.Name() + "/status")
		if err != nil {
			continue
		}
		ppid, uid := parseStatusPPidUID(string(statusBytes))
		out = append(out, gcProcInfo{
			Pid:     pid,
			Ppid:    ppid,
			UID:     uid,
			Cmdline: argv,
		})
	}
	return out, nil
}

func parseStatusPPidUID(s string) (ppid, uid int) {
	for _, line := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(line, "PPid:"):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				ppid, _ = strconv.Atoi(fields[1])
			}
		case strings.HasPrefix(line, "Uid:"):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				uid, _ = strconv.Atoi(fields[1])
			}
		}
	}
	return
}

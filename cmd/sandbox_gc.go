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
  - /tmp/sprawl-*-e2e-* and /tmp/sprawl-test-* dirs older than --max-age
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

// defaultListTmpDirs globs all known sandbox tmp-dir patterns.
func defaultListTmpDirs() ([]string, error) {
	var out []string
	for _, pat := range []string{
		"/tmp/sprawl-*-e2e-*",
		"/tmp/sprawl-test-*",
		"/tmp/sprawl-rb-*",
	} {
		m, err := filepath.Glob(pat)
		if err != nil {
			return nil, err
		}
		out = append(out, m...)
	}
	return out, nil
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

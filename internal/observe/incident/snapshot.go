// Package incident produces forensic incident-bundle directories from a
// running Sprawl process. Invoked from the TUI (QUM-728) via Ctrl+\ to
// dump goroutine stacks, fd snapshots, CPU and heap pprof profiles
// (QUM-934), the executable's path/version for symbolization, supervisor
// status, /proc state, recent MCP calls, per-agent activity rates, and host
// memory/load into `<SprawlRoot>/.sprawl/incidents/<ISO8601>-tui-snapshot/`.
//
// Capture is best-effort: per-artifact errors are recorded into the
// bundle's README.md instead of aborting the run, so a partial bundle is
// still useful when one collector fails. Capture only returns a non-nil
// error if the bundle directory cannot be created or the index cannot be
// written.
package incident

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/observe/sigdump"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// defaultTailLines caps the mcp-calls.jsonl excerpt.
const defaultTailLines = 10000

// DefaultProfileDuration is the CPU-profile sampling window used when
// Snapshotter.ProfileDuration is zero. Long enough to catch a hot loop or a
// spin, short enough to fit inside the 30s Capture budget at the Ctrl+\ call
// site alongside every other artifact.
const DefaultProfileDuration = 5 * time.Second

// profileFileFlags opens a profile artifact. O_EXCL matters: the bundle dir is
// second-granular, so two captures in the same wall second share it. Failing
// rather than truncating means a concurrent capture can never clobber a
// profile the other one is still writing, and it guarantees that a path we
// clean up after a failure is a path we created.
const profileFileFlags = os.O_CREATE | os.O_EXCL | os.O_WRONLY

// Snapshotter captures an incident bundle. All fields are injected so the
// helper is unit-testable without touching the host process or filesystem.
type Snapshotter struct {
	// SprawlRoot is the repo root; the incident dir is created beneath
	// SprawlRoot/.sprawl/incidents/.
	SprawlRoot string
	// Now returns the wall-clock time used for the bundle timestamp. If
	// nil, time.Now is used.
	Now func() time.Time
	// FDSource produces the open-fd snapshot for sigdump.Dump.
	FDSource sigdump.FDSource
	// StatusFn returns the supervisor's view of all agents.
	StatusFn func(ctx context.Context) ([]supervisor.AgentInfo, error)
	// WeavePid is the pid of the in-process weave runtime — used to read
	// /proc/<pid>/status and count its open fds.
	WeavePid int
	// Runner executes a host command (e.g. "ps", "free") and returns its
	// combined stdout/err bytes. Injection allows the unit test to stub
	// out OS calls.
	Runner func(ctx context.Context, name string, args ...string) ([]byte, error)
	// MCPLogPath points at .sprawl/logs/mcp-calls.jsonl.
	MCPLogPath string
	// ActivityRoot points at .sprawl/agents (each agent has activity.ndjson).
	ActivityRoot string
	// TailLines caps the mcp-calls.jsonl excerpt. 0 means defaultTailLines.
	TailLines int
	// ProfileDuration is the CPU-profile sampling window. 0 means
	// DefaultProfileDuration; a negative value disables CPU profiling
	// entirely. The window is opened before the other collectors run and
	// closed after them, so it samples the capture itself for free.
	//
	// runtime/pprof permits one active CPU profile per process, so a second
	// Ctrl+\ during a live window — or a concurrent --pprof scrape
	// (QUM-678) — degrades to a recorded error and a bundle without a CPU
	// profile.
	ProfileDuration time.Duration
	// StartCPUProfile begins CPU profiling into w. nil means
	// pprof.StartCPUProfile.
	StartCPUProfile func(w io.Writer) error
	// StopCPUProfile ends the window opened by StartCPUProfile. nil means
	// pprof.StopCPUProfile. Injected alongside StartCPUProfile so a fake
	// start never lets the real stop reach a genuinely active profile.
	StopCPUProfile func()
	// WriteHeapProfile writes a heap profile to w. nil means
	// pprof.WriteHeapProfile.
	WriteHeapProfile func(w io.Writer) error
	// Version is the sprawl version stamped into binary.txt. Empty means
	// derive it from runtime/debug build info.
	Version string
}

// Capture writes a bundle and returns its absolute path. Per-artifact
// failures are recorded into README.md's "## Errors" section; only mkdir
// / README write failures abort the call.
func (s *Snapshotter) Capture(ctx context.Context) (string, error) {
	nowFn := s.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	tailCap := s.TailLines
	if tailCap == 0 {
		tailCap = defaultTailLines
	}

	ts := now.UTC().Format("20060102T150405Z")
	dir := filepath.Join(s.SprawlRoot, ".sprawl", "incidents", ts+"-tui-snapshot")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("incident: mkdir %s: %w", dir, err)
	}

	var capErrs []string
	record := func(label string, err error) {
		if err == nil {
			return
		}
		capErrs = append(capErrs, fmt.Sprintf("%s: %v", label, err))
	}

	// cpu-<ts>.pprof — opened first so the window overlaps every other
	// collector; closed just before the README is written.
	//
	// cpuStarted uses the real clock, not nowFn: nowFn is a frozen fake in
	// tests and would make the remaining-window math meaningless.
	var cpuFile *os.File
	var cpuStarted time.Time
	profDur := s.ProfileDuration
	if profDur == 0 {
		profDur = DefaultProfileDuration
	}
	if profDur > 0 {
		if ctxErr := ctx.Err(); ctxErr != nil {
			record("cpu profile", ctxErr)
		} else {
			path := filepath.Join(dir, "cpu-"+ts+".pprof")
			f, err := os.OpenFile(path, profileFileFlags, 0o600)
			if err != nil {
				record("cpu profile create", err)
			} else if sErr := s.startCPUProfile(f); sErr != nil {
				record("cpu profile start", sErr)
				_ = f.Close()
				// Leave no empty artifact behind.
				_ = os.Remove(path)
			} else {
				cpuFile = f
				cpuStarted = time.Now()
			}
		}
	}
	// Panic safety net: any collector below could panic — this is a forensic
	// tool pointed at an already-sick process. Without this, the
	// process-global CPU profile would stay active forever, permanently
	// breaking every later Ctrl+\ and any --pprof scrape (QUM-678). The
	// normal path clears cpuFile after stopping, making this a no-op.
	defer func() {
		if cpuFile != nil {
			s.stopCPUProfile()
			_ = cpuFile.Close()
		}
	}()

	// heap-<ts>.pprof — instantaneous, so taken as close to the incident as
	// possible. No runtime.GC() first: forcing a collection perturbs a
	// possibly-wedged process, so the numbers are inuse as of the last GC.
	{
		path := filepath.Join(dir, "heap-"+ts+".pprof")
		f, err := os.OpenFile(path, profileFileFlags, 0o600)
		if err != nil {
			record("heap profile create", err)
		} else {
			wErr := s.writeHeapProfile(f)
			cErr := f.Close()
			switch {
			case wErr != nil:
				record("heap profile", wErr)
				_ = os.Remove(path)
			case cErr != nil:
				record("heap profile close", cErr)
			}
		}
	}

	// binary.txt — the executable path and version, so the profiles above
	// can be symbolized later.
	{
		exe, err := os.Executable()
		if err != nil {
			record("executable path", err)
			exe = "(unknown)"
		}
		info := buildBinaryInfo(exe, s.Version)
		if wErr := os.WriteFile(filepath.Join(dir, "binary.txt"), []byte(info), 0o600); wErr != nil {
			record("binary info write", wErr)
		}
	}

	// sprawl-status.json
	if s.StatusFn != nil {
		agents, err := s.StatusFn(ctx)
		if err != nil {
			record("status", err)
			payload := map[string]string{"error": err.Error()}
			if b, mErr := json.MarshalIndent(payload, "", "  "); mErr == nil {
				_ = os.WriteFile(filepath.Join(dir, "sprawl-status.json"), b, 0o600)
			}
		} else {
			b, mErr := json.MarshalIndent(agents, "", "  ")
			if mErr != nil {
				record("status marshal", mErr)
			} else if wErr := os.WriteFile(filepath.Join(dir, "sprawl-status.json"), b, 0o600); wErr != nil {
				record("status write", wErr)
			}
		}
	}

	// sigdump (goroutines + fds)
	if s.FDSource != nil {
		if _, _, err := sigdump.Dump(dir, now, s.FDSource); err != nil {
			record("sigdump", err)
		}
	}

	// ps auxf
	if s.Runner != nil {
		out, err := s.Runner(ctx, "ps", "auxf")
		if err != nil {
			record("ps", err)
		}
		if wErr := os.WriteFile(filepath.Join(dir, "ps-auxf.txt"), out, 0o600); wErr != nil {
			record("ps write", wErr)
		}
	}

	// /proc/<pid>/status + fd_count
	if s.WeavePid > 0 {
		procPath := filepath.Join(dir, fmt.Sprintf("proc-status-%d.txt", s.WeavePid))
		statusBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", s.WeavePid))
		if err != nil {
			record("proc status", err)
			statusBytes = []byte(fmt.Sprintf("(read /proc/%d/status error: %v)\n", s.WeavePid, err))
		}
		fdEntries, fdErr := os.ReadDir(fmt.Sprintf("/proc/%d/fd", s.WeavePid))
		if fdErr != nil {
			record("proc fd count", fdErr)
		}
		var b strings.Builder
		b.Write(statusBytes)
		if !strings.HasSuffix(string(statusBytes), "\n") {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "fd_count: %d\n", len(fdEntries))
		if wErr := os.WriteFile(procPath, []byte(b.String()), 0o600); wErr != nil {
			record("proc write", wErr)
		}
	}

	// mcp-calls-tail.jsonl
	if s.MCPLogPath != "" {
		if err := writeMCPTail(filepath.Join(dir, "mcp-calls-tail.jsonl"), s.MCPLogPath, tailCap); err != nil {
			record("mcp log", err)
		}
	}

	// activity-rates.txt
	if s.ActivityRoot != "" {
		if err := writeActivityRates(filepath.Join(dir, "activity-rates.txt"), s.ActivityRoot, now); err != nil {
			record("activity rates", err)
		}
	}

	// mem-load.txt
	{
		var b strings.Builder
		b.WriteString("## free -h\n")
		if s.Runner != nil {
			out, err := s.Runner(ctx, "free", "-h")
			if err != nil {
				record("free", err)
			}
			b.Write(out)
			if len(out) > 0 && !strings.HasSuffix(string(out), "\n") {
				b.WriteByte('\n')
			}
		}
		b.WriteString("## /proc/loadavg\n")
		loadBytes, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			record("loadavg", err)
		} else {
			b.Write(loadBytes)
			if len(loadBytes) > 0 && !strings.HasSuffix(string(loadBytes), "\n") {
				b.WriteByte('\n')
			}
		}
		if wErr := os.WriteFile(filepath.Join(dir, "mem-load.txt"), []byte(b.String()), 0o600); wErr != nil {
			record("mem-load write", wErr)
		}
	}

	// Close the CPU window last, so it covers all of the above.
	if cpuFile != nil {
		if remaining := profDur - time.Since(cpuStarted); remaining > 0 {
			timer := time.NewTimer(remaining)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				// A short profile is still valid and useful — record the
				// truncation but keep the file.
				record("cpu profile window", ctx.Err())
			}
		}
		s.stopCPUProfile()
		cErr := cpuFile.Close()
		cpuFile = nil // disarm the panic-safety defer above
		if cErr != nil {
			record("cpu profile close", cErr)
		}
	}

	// README.md
	readme := buildReadme(ts, capErrs)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o600); err != nil {
		return "", fmt.Errorf("incident: write README: %w", err)
	}

	return dir, nil
}

// startCPUProfile / stopCPUProfile / writeHeapProfile resolve the injectable
// profiler hooks to their runtime/pprof defaults.
func (s *Snapshotter) startCPUProfile(w io.Writer) error {
	if s.StartCPUProfile != nil {
		return s.StartCPUProfile(w)
	}
	return pprof.StartCPUProfile(w)
}

func (s *Snapshotter) stopCPUProfile() {
	if s.StopCPUProfile != nil {
		s.StopCPUProfile()
		return
	}
	pprof.StopCPUProfile()
}

func (s *Snapshotter) writeHeapProfile(w io.Writer) error {
	if s.WriteHeapProfile != nil {
		return s.WriteHeapProfile(w)
	}
	return pprof.WriteHeapProfile(w)
}

// buildBinaryInfo renders binary.txt: everything needed to symbolize the
// bundle's profiles after the running process is gone.
func buildBinaryInfo(execPath, version string) string {
	info, haveInfo := debug.ReadBuildInfo()
	if version == "" && haveInfo {
		version = info.Main.Version
	}
	if version == "" {
		version = "(unknown)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "executable: %s\n", execPath)
	fmt.Fprintf(&b, "version: %s\n", version)
	fmt.Fprintf(&b, "go: %s\n", runtime.Version())
	fmt.Fprintf(&b, "platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if haveInfo {
		fmt.Fprintf(&b, "module: %s\n", info.Main.Path)
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision", "vcs.time", "vcs.modified":
				fmt.Fprintf(&b, "%s: %s\n", setting.Key, setting.Value)
			}
		}
	}
	return b.String()
}

func writeMCPTail(dst, src string, tailCap int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	ring := make([]string, 0, tailCap)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if len(ring) < tailCap {
			ring = append(ring, line)
		} else {
			// Shift left by one — small N typical, KISS.
			copy(ring, ring[1:])
			ring[len(ring)-1] = line
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	var b strings.Builder
	for _, l := range ring {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return os.WriteFile(dst, []byte(b.String()), 0o600)
}

func writeActivityRates(dst, activityRoot string, now time.Time) error {
	entries, err := os.ReadDir(activityRoot)
	if err != nil {
		return err
	}
	cutoff := now.Add(-60 * time.Second)
	var b strings.Builder
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		agent := e.Name()
		path := filepath.Join(activityRoot, agent, "activity.ndjson")
		f, err := os.Open(path)
		if err != nil {
			// Missing activity file for this agent — skip silently; not fatal.
			continue
		}
		count := 0
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			var rec struct {
				TS time.Time `json:"ts"`
			}
			if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
				continue
			}
			if !rec.TS.Before(cutoff) && !rec.TS.After(now) {
				count++
			}
		}
		_ = f.Close()
		fmt.Fprintf(&b, "%s\t%d\n", agent, count)
	}
	return os.WriteFile(dst, []byte(b.String()), 0o600)
}

func buildReadme(ts string, capErrs []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Incident snapshot — %s\n\n", ts)
	b.WriteString("Captured via TUI hotkey (QUM-728).\n\n")
	b.WriteString("| file | what |\n")
	b.WriteString("|---|---|\n")
	b.WriteString("| sprawl-status.json | mcp__sprawl__status payload |\n")
	b.WriteString("| goroutines-*.txt | in-process goroutine dump |\n")
	b.WriteString("| fds-*.txt | open fd snapshot |\n")
	b.WriteString("| cpu-*.pprof | CPU profile, sampled across the capture (runtime/pprof) |\n")
	b.WriteString("| heap-*.pprof | heap profile, inuse as of the last GC (runtime/pprof) |\n")
	b.WriteString("| binary.txt | executable path, version, build info (for symbolization) |\n")
	b.WriteString("| ps-auxf.txt | `ps auxf` |\n")
	b.WriteString("| proc-status-<pid>.txt | /proc/<pid>/status + fd_count |\n")
	b.WriteString("| mcp-calls-tail.jsonl | last N lines of .sprawl/logs/mcp-calls.jsonl |\n")
	b.WriteString("| activity-rates.txt | per-agent activity.ndjson 60s-window counts |\n")
	b.WriteString("| mem-load.txt | `free -h` + /proc/loadavg |\n")
	b.WriteString("\nThe table is a fixed legend of what a bundle can hold; a given\n")
	b.WriteString("capture may omit artifacts whose collector failed (see Errors below).\n")
	b.WriteString("\n## How to read the profiles\n\n")
	b.WriteString("    go tool pprof <binary> cpu-*.pprof\n")
	b.WriteString("    go tool pprof <binary> heap-*.pprof\n\n")
	b.WriteString("`<binary>` is the `executable:` path from binary.txt. If that binary has\n")
	b.WriteString("since been rebuilt or removed, rebuild at the revision recorded in\n")
	b.WriteString("binary.txt — symbolization needs the matching binary.\n")
	b.WriteString("\n## Errors\n\n")
	if len(capErrs) == 0 {
		b.WriteString("none\n")
	} else {
		for _, e := range capErrs {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	}
	return b.String()
}

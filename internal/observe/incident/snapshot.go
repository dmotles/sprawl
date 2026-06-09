// Package incident produces forensic incident-bundle directories from a
// running Sprawl process. Invoked from the TUI (QUM-728) via Ctrl+\ to
// dump goroutine stacks, fd snapshots, supervisor status, /proc state,
// recent MCP calls, per-agent activity rates, and host memory/load into
// `<SprawlRoot>/.sprawl/incidents/<ISO8601>-tui-snapshot/`.
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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/observe/sigdump"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// defaultTailLines caps the mcp-calls.jsonl excerpt.
const defaultTailLines = 10000

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

	// README.md
	readme := buildReadme(ts, capErrs)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o600); err != nil {
		return "", fmt.Errorf("incident: write README: %w", err)
	}

	return dir, nil
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
	b.WriteString("| ps-auxf.txt | `ps auxf` |\n")
	b.WriteString("| proc-status-<pid>.txt | /proc/<pid>/status + fd_count |\n")
	b.WriteString("| mcp-calls-tail.jsonl | last N lines of .sprawl/logs/mcp-calls.jsonl |\n")
	b.WriteString("| activity-rates.txt | per-agent activity.ndjson 60s-window counts |\n")
	b.WriteString("| mem-load.txt | `free -h` + /proc/loadavg |\n")
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

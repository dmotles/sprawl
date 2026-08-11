package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// gcMockState records all mutating calls so tests can assert what happened
// (and what did NOT happen, esp. under --dry-run).
type gcMockState struct {
	mu               sync.Mutex
	unlinkedSockets  []string
	removedDirs      []string
	killedPids       []int
	sockets          []string
	sessionsBySocket map[string]int
	tmpDirs          []string
	dirMtimes        map[string]time.Time
	procs            []gcProcInfo
	uid              int
	nowT             time.Time
}

func newGCMockState() *gcMockState {
	return &gcMockState{
		sessionsBySocket: map[string]int{},
		dirMtimes:        map[string]time.Time{},
		uid:              1000,
		nowT:             time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	}
}

func newTestSandboxGCDeps(t *testing.T, st *gcMockState) (*sandboxGCDeps, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	deps := &sandboxGCDeps{
		listSockets: func() ([]string, error) {
			return append([]string(nil), st.sockets...), nil
		},
		sessionsOn: func(s string) (int, error) {
			return st.sessionsBySocket[s], nil
		},
		unlinkSocket: func(p string) error {
			st.mu.Lock()
			defer st.mu.Unlock()
			st.unlinkedSockets = append(st.unlinkedSockets, p)
			return nil
		},
		listTmpDirs: func() ([]string, error) {
			return append([]string(nil), st.tmpDirs...), nil
		},
		dirInfo: func(p string) (time.Time, error) {
			if t, ok := st.dirMtimes[p]; ok {
				return t, nil
			}
			return time.Time{}, errors.New("not found")
		},
		removeAll: func(p string) error {
			st.mu.Lock()
			defer st.mu.Unlock()
			st.removedDirs = append(st.removedDirs, p)
			return nil
		},
		listProcs: func() ([]gcProcInfo, error) {
			return append([]gcProcInfo(nil), st.procs...), nil
		},
		killProc: func(pid int) error {
			st.mu.Lock()
			defer st.mu.Unlock()
			st.killedPids = append(st.killedPids, pid)
			return nil
		},
		currentUID: func() int { return st.uid },
		now:        func() time.Time { return st.nowT },
		out:        buf,
	}
	return deps, buf
}

func TestSandboxGC_DryRunMutatesNothing(t *testing.T) {
	st := newGCMockState()
	st.sockets = []string{"/tmp/tmux-1000/sprawl-handoff-e2e-123"}
	st.tmpDirs = []string{"/tmp/sprawl-handoff-e2e-123"}
	st.dirMtimes["/tmp/sprawl-handoff-e2e-123"] = st.nowT.Add(-3 * time.Hour)
	st.procs = []gcProcInfo{{
		Pid: 9001, Ppid: 1, UID: 1000,
		Cmdline: []string{"claude", "--system-prompt-file=/tmp/sprawl-handoff-e2e-123/sp.md"},
	}}
	deps, _ := newTestSandboxGCDeps(t, st)

	if err := runSandboxGC(deps, true, time.Hour); err != nil {
		t.Fatalf("dry-run returned error: %v", err)
	}
	if len(st.unlinkedSockets) != 0 {
		t.Errorf("dry-run unlinked sockets: %v", st.unlinkedSockets)
	}
	if len(st.removedDirs) != 0 {
		t.Errorf("dry-run removed dirs: %v", st.removedDirs)
	}
	if len(st.killedPids) != 0 {
		t.Errorf("dry-run killed pids: %v", st.killedPids)
	}
}

func TestSandboxGC_SweepsStaleSockets(t *testing.T) {
	st := newGCMockState()
	st.sockets = []string{
		"/tmp/tmux-1000/sprawl-stale",
		"/tmp/tmux-1000/sprawl-active",
	}
	st.sessionsBySocket["/tmp/tmux-1000/sprawl-stale"] = 0
	st.sessionsBySocket["/tmp/tmux-1000/sprawl-active"] = 2
	deps, _ := newTestSandboxGCDeps(t, st)

	if err := runSandboxGC(deps, false, time.Hour); err != nil {
		t.Fatalf("runSandboxGC: %v", err)
	}
	if len(st.unlinkedSockets) != 1 || st.unlinkedSockets[0] != "/tmp/tmux-1000/sprawl-stale" {
		t.Errorf("expected stale socket unlinked, got %v", st.unlinkedSockets)
	}
}

func TestSandboxGC_SweepsStaleTmpDirs(t *testing.T) {
	st := newGCMockState()
	st.tmpDirs = []string{
		"/tmp/sprawl-handoff-e2e-old",
		"/tmp/sprawl-handoff-e2e-newish",
		"/tmp/sprawl-handoff-e2e-referenced",
	}
	st.dirMtimes["/tmp/sprawl-handoff-e2e-old"] = st.nowT.Add(-3 * time.Hour)
	st.dirMtimes["/tmp/sprawl-handoff-e2e-newish"] = st.nowT.Add(-10 * time.Minute)
	st.dirMtimes["/tmp/sprawl-handoff-e2e-referenced"] = st.nowT.Add(-3 * time.Hour)
	st.procs = []gcProcInfo{{
		Pid: 4242, Ppid: 4000, UID: 1000,
		Cmdline: []string{"claude", "--system-prompt-file=/tmp/sprawl-handoff-e2e-referenced/sp.md"},
	}}
	deps, _ := newTestSandboxGCDeps(t, st)

	if err := runSandboxGC(deps, false, 2*time.Hour); err != nil {
		t.Fatalf("runSandboxGC: %v", err)
	}
	if len(st.removedDirs) != 1 || st.removedDirs[0] != "/tmp/sprawl-handoff-e2e-old" {
		t.Errorf("expected only -old removed, got %v", st.removedDirs)
	}
}

func TestSandboxGC_KillsOrphanClaudeProcs(t *testing.T) {
	tests := []struct {
		name     string
		proc     gcProcInfo
		uid      int
		wantKill bool
	}{
		{
			name: "orphan_under_tmp_sprawl_same_uid",
			proc: gcProcInfo{
				Pid: 100, Ppid: 1, UID: 1000,
				Cmdline: []string{"claude", "--system-prompt-file=/tmp/sprawl-handoff-e2e-x/sp.md"},
			},
			uid: 1000, wantKill: true,
		},
		{
			name: "different_uid_skipped",
			proc: gcProcInfo{
				Pid: 101, Ppid: 1, UID: 0,
				Cmdline: []string{"claude", "--system-prompt-file=/tmp/sprawl-handoff-e2e-x/sp.md"},
			},
			uid: 1000, wantKill: false,
		},
		{
			name: "non_orphan_skipped",
			proc: gcProcInfo{
				Pid: 102, Ppid: 4000, UID: 1000,
				Cmdline: []string{"claude", "--system-prompt-file=/tmp/sprawl-handoff-e2e-x/sp.md"},
			},
			uid: 1000, wantKill: false,
		},
		{
			name: "argv_outside_tmp_sprawl_skipped",
			proc: gcProcInfo{
				Pid: 103, Ppid: 1, UID: 1000,
				Cmdline: []string{"claude", "--system-prompt-file=/home/u/.sprawl/sp.md"},
			},
			uid: 1000, wantKill: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newGCMockState()
			st.uid = tt.uid
			st.procs = []gcProcInfo{tt.proc}
			deps, _ := newTestSandboxGCDeps(t, st)
			if err := runSandboxGC(deps, false, time.Hour); err != nil {
				t.Fatalf("runSandboxGC: %v", err)
			}
			killed := len(st.killedPids) > 0
			if killed != tt.wantKill {
				t.Errorf("killed=%v want=%v (killedPids=%v)", killed, tt.wantKill, st.killedPids)
			}
		})
	}
}

func TestSandboxGC_Idempotent(t *testing.T) {
	st := newGCMockState()
	st.sockets = []string{"/tmp/tmux-1000/sprawl-stale"}
	st.sessionsBySocket["/tmp/tmux-1000/sprawl-stale"] = 0
	deps, _ := newTestSandboxGCDeps(t, st)

	if err := runSandboxGC(deps, false, time.Hour); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Simulate the unlinked socket being gone.
	st.sockets = nil
	prevUnlinks := len(st.unlinkedSockets)

	if err := runSandboxGC(deps, false, time.Hour); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(st.unlinkedSockets) != prevUnlinks {
		t.Errorf("idempotency violated: extra unlinks on second run %v", st.unlinkedSockets[prevUnlinks:])
	}
}

func TestSandboxGC_OutputFormat(t *testing.T) {
	st := newGCMockState()
	st.sockets = []string{"/tmp/tmux-1000/sprawl-stale"}
	st.sessionsBySocket["/tmp/tmux-1000/sprawl-stale"] = 0
	st.tmpDirs = []string{"/tmp/sprawl-test-x"}
	st.dirMtimes["/tmp/sprawl-test-x"] = st.nowT.Add(-3 * time.Hour)
	deps, buf := newTestSandboxGCDeps(t, st)

	if err := runSandboxGC(deps, false, time.Hour); err != nil {
		t.Fatalf("runSandboxGC: %v", err)
	}
	out := buf.String()
	lower := strings.ToLower(out)
	for _, want := range []string{"socket", "dir", "swept"} {
		if !strings.Contains(lower, want) {
			t.Errorf("output missing summary token %q; got: %s", want, out)
		}
	}
	// Per /cli-ux-best-practices: every command should hint at next action.
	// Require an actionable phrase (not just any token like "running"). The
	// implementer should print one of these specific hints — pick one that
	// fits the GC output (e.g. "re-run with --dry-run", "now safe to run").
	gotHint := false
	for _, hint := range []string{"re-run", "now safe", "next:", "you can now", "sprawl sandbox"} {
		if strings.Contains(lower, hint) {
			gotHint = true
			break
		}
	}
	if !gotHint {
		t.Errorf("output missing actionable next-action hint per /cli-ux-best-practices; want one of [re-run|now safe|next:|you can now|sprawl sandbox]; got: %s", out)
	}
}

// QUM-1119: defaultListTmpDirs used to glob only "/tmp/sprawl-*-e2e-*",
// "/tmp/sprawl-test-*" and "/tmp/sprawl-rb-*" — a hardcoded, enumerated list
// that none of the real e2e rows' sandbox-root prefixes match (they pass
// their own prefix to e2e_make_sandbox_root, mostly "sprawl-qum<NNN>", with
// no "-e2e-" or "-test-" substring). discoverSandboxTmpDirs replaces the
// prefix list with a structural marker: a directory directly under base
// named "sprawl-*" that itself contains a ".sprawl" subdirectory — the same
// marker e2e_make_sandbox_root + e2e_init_sandbox_repo leave on every real
// sandbox root, regardless of which prefix a given row picked.
func TestDiscoverSandboxTmpDirs_FindsMarkedDirsRegardlessOfPrefix(t *testing.T) {
	base := t.TempDir()

	// A real leaked sandbox root, using a prefix NOT in the old hardcoded
	// list. This is the positive control: if the discovery still keyed on
	// "-e2e-"/"-test-"/"-rb-" it would miss this entirely.
	leaked := filepath.Join(base, "sprawl-qum1186-busy-QMmd7o")
	if err := os.MkdirAll(filepath.Join(leaked, ".sprawl"), 0o755); err != nil {
		t.Fatalf("mkdir leaked root: %v", err)
	}

	// Retained artifacts observed in the real QUM-1119 incident. None has a
	// ".sprawl" marker directly inside it, which is what must exclude them —
	// NOT their name, since a name denylist only protects artifacts whose
	// name is known today.
	backupDated := filepath.Join(base, "sprawl-agents-backup-20260527-153542")
	if err := os.MkdirAll(filepath.Join(backupDated, "weave"), 0o755); err != nil {
		t.Fatalf("mkdir backupDated: %v", err)
	}
	backupGit := filepath.Join(base, "sprawl-backup.git")
	if err := os.MkdirAll(backupGit, 0o755); err != nil {
		t.Fatalf("mkdir backupGit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "sprawl-allrefs-backup.bundle"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "sprawl-v0.2.7-known-good.bin"), []byte("x"), 0o755); err != nil {
		t.Fatalf("write known-good bin: %v", err)
	}

	// A symlink whose target DOES have a .sprawl marker — must still be
	// excluded. Reaping must never follow a symlink into deletion territory.
	symlinkTarget := filepath.Join(base, "real-target-with-marker")
	if err := os.MkdirAll(filepath.Join(symlinkTarget, ".sprawl"), 0o755); err != nil {
		t.Fatalf("mkdir symlink target: %v", err)
	}
	if err := os.Symlink(symlinkTarget, filepath.Join(base, "sprawl-evil-symlink")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := discoverSandboxTmpDirs(base)
	if err != nil {
		t.Fatalf("discoverSandboxTmpDirs: %v", err)
	}

	if len(got) != 1 || got[0] != leaked {
		t.Fatalf("discoverSandboxTmpDirs(%s) = %v, want exactly [%s]", base, got, leaked)
	}
}

// QUM-1119 AC: "/tmp/coder-script-data is never touched. Assert it
// explicitly." discoverSandboxTmpDirs itself cannot return it (its glob is
// "sprawl-*" and that name does not match), but the safety net inside
// runSandboxGC must ALSO refuse it by name, in case discovery ever regresses
// to something broader. This is the explicit assertion the AC calls for.
func TestIsSandboxRootPath_RefusesCoderScriptData(t *testing.T) {
	if isSandboxRootPath("/tmp/coder-script-data") {
		t.Fatal("isSandboxRootPath(/tmp/coder-script-data) = true, want false — this path is host tooling state and must never be reaped")
	}
	if isSandboxRootPath("/tmp/coder-script-data/bin/claude") {
		t.Fatal("isSandboxRootPath(/tmp/coder-script-data/bin/claude) = true, want false")
	}
}

// QUM-1119 AC: "The reaper cannot delete outside /tmp/<known-prefix>; a unit
// test asserts it refuses an unexpected path rather than silently skipping
// it." Injects an unexpected path directly through listTmpDirs (as if
// discovery itself regressed) and asserts runSandboxGC REFUSES it loudly
// (prints a refusal) rather than quietly passing it through to removeAll.
func TestSandboxGC_RefusesUnexpectedTmpDirPath(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"outside_tmp", "/etc/passwd-shaped-dir"},
		{"coder_script_data", "/tmp/coder-script-data"},
		{"coder_script_data_child", "/tmp/coder-script-data/bin/claude"},
		{"not_sprawl_prefixed", "/tmp/not-sprawl-prefixed"},
		{"nested_not_direct_child", "/tmp/sprawl-ok/sprawl-nested"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newGCMockState()
			st.tmpDirs = []string{tt.path}
			st.dirMtimes[tt.path] = st.nowT.Add(-3 * time.Hour)
			deps, buf := newTestSandboxGCDeps(t, st)

			if err := runSandboxGC(deps, false, time.Hour); err != nil {
				t.Fatalf("runSandboxGC: %v", err)
			}
			if len(st.removedDirs) != 0 {
				t.Errorf("removeAll called on unexpected path %s: %v", tt.path, st.removedDirs)
			}
			if !strings.Contains(strings.ToLower(buf.String()), "refus") {
				t.Errorf("expected a loud refusal message for unexpected path %s, got: %s", tt.path, buf.String())
			}
		})
	}
}

// Compile-time check: deps struct satisfies the io.Writer field shape.
var _ io.Writer = (*bytes.Buffer)(nil)

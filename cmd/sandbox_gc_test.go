package cmd

import (
	"bytes"
	"errors"
	"io"
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

// Compile-time check: deps struct satisfies the io.Writer field shape.
var _ io.Writer = (*bytes.Buffer)(nil)

package agentops_test

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/state"
)

// gcEnv sets up an agents dir with the requested orphan/non-orphan structure.
// orphans: name -> map of relative file path -> mtime (relative to now).
// withJSON: names that have a sibling <name>.json (i.e. NOT orphans).
type gcEnv struct {
	root string
	now  time.Time
}

func newGCEnv(t *testing.T) *gcEnv {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(state.AgentsDir(root), 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	return &gcEnv{root: root, now: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)}
}

func (g *gcEnv) mkOrphanDir(t *testing.T, name string, files map[string]time.Time) string {
	t.Helper()
	dir := filepath.Join(state.AgentsDir(g.root), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir orphan %s: %v", name, err)
	}
	for rel, mtime := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir parent: %v", err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
		if err := os.Chtimes(full, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", full, err)
		}
	}
	return dir
}

func (g *gcEnv) mkJSON(t *testing.T, name string) {
	t.Helper()
	path := filepath.Join(state.AgentsDir(g.root), name+".json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

func (g *gcEnv) deps() agentops.GCDeps {
	return agentops.GCDeps{
		Now:            func() time.Time { return g.now },
		ListWorktrees:  func() (map[string]bool, error) { return map[string]bool{}, nil },
		RemoveWorktree: func(string) error { return nil },
		RemoveAll:      func(string) error { return nil },
		SprawlRoot:     g.root,
	}
}

func TestScanOrphans_NoOrphans(t *testing.T) {
	g := newGCEnv(t)
	g.mkOrphanDir(t, "alice", map[string]time.Time{"f": g.now.Add(-30 * 24 * time.Hour)})
	g.mkJSON(t, "alice")

	got, err := agentops.ScanOrphans(g.deps())
	if err != nil {
		t.Fatalf("ScanOrphans err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 orphans, got %d: %+v", len(got), got)
	}
}

func TestScanOrphans_DirWithoutJSON_DetectedAsOrphan(t *testing.T) {
	g := newGCEnv(t)
	dir := g.mkOrphanDir(t, "bob", map[string]time.Time{"a": g.now.Add(-30 * 24 * time.Hour)})

	got, err := agentops.ScanOrphans(g.deps())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 orphan, got %d", len(got))
	}
	if got[0].Name != "bob" {
		t.Errorf("name = %q, want bob", got[0].Name)
	}
	if got[0].DirPath != dir {
		t.Errorf("DirPath = %q, want %q", got[0].DirPath, dir)
	}
}

func TestScanOrphans_FreshDirRefused(t *testing.T) {
	g := newGCEnv(t)
	g.mkOrphanDir(t, "carol", map[string]time.Time{"f": g.now.Add(-1 * time.Hour)})

	got, err := agentops.ScanOrphans(g.deps())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || !got[0].Fresh {
		t.Fatalf("expected fresh=true, got %+v", got)
	}
}

func TestScanOrphans_OldDirEligible(t *testing.T) {
	g := newGCEnv(t)
	g.mkOrphanDir(t, "dave", map[string]time.Time{"f": g.now.Add(-30 * 24 * time.Hour)})

	got, err := agentops.ScanOrphans(g.deps())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || got[0].Fresh {
		t.Fatalf("expected fresh=false, got %+v", got)
	}
}

func TestScanOrphans_NestedFreshFileBlocks(t *testing.T) {
	g := newGCEnv(t)
	g.mkOrphanDir(t, "ed", map[string]time.Time{
		"old.txt":            g.now.Add(-30 * 24 * time.Hour),
		"sub/deep/fresh.txt": g.now.Add(-1 * time.Hour),
	})

	got, err := agentops.ScanOrphans(g.deps())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || !got[0].Fresh {
		t.Fatalf("expected nested fresh -> Fresh=true, got %+v", got)
	}
}

func TestScanOrphans_WorktreeCrossReference(t *testing.T) {
	g := newGCEnv(t)
	g.mkOrphanDir(t, "frank", map[string]time.Time{"f": g.now.Add(-30 * 24 * time.Hour)})
	wantWT := filepath.Join(g.root, ".sprawl", "worktrees", "frank")
	deps := g.deps()
	deps.ListWorktrees = func() (map[string]bool, error) {
		return map[string]bool{wantWT: true, "/other/path": true}, nil
	}

	got, err := agentops.ScanOrphans(deps)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || got[0].WorktreePath != wantWT {
		t.Fatalf("WorktreePath = %q, want %q (got=%+v)", got[0].WorktreePath, wantWT, got)
	}
}

func TestScanOrphans_WorktreeListError_TreatedAsEmpty(t *testing.T) {
	g := newGCEnv(t)
	g.mkOrphanDir(t, "gina", map[string]time.Time{"f": g.now.Add(-30 * 24 * time.Hour)})
	deps := g.deps()
	deps.ListWorktrees = func() (map[string]bool, error) { return nil, errors.New("boom") }

	got, err := agentops.ScanOrphans(deps)
	if err != nil {
		t.Fatalf("ScanOrphans should swallow ListWorktrees err, got %v", err)
	}
	if len(got) != 1 || got[0].WorktreePath != "" {
		t.Fatalf("expected empty WorktreePath on listerr, got %+v", got)
	}
}

func TestScanOrphans_AgentsDirMissing(t *testing.T) {
	root := t.TempDir()
	deps := agentops.GCDeps{
		Now:            time.Now,
		ListWorktrees:  func() (map[string]bool, error) { return nil, nil },
		RemoveWorktree: func(string) error { return nil },
		RemoveAll:      func(string) error { return nil },
		SprawlRoot:     root,
	}
	got, err := agentops.ScanOrphans(deps)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}

func TestScanOrphans_JsonFileWithoutDir_NotAnOrphan(t *testing.T) {
	g := newGCEnv(t)
	g.mkJSON(t, "henry")

	got, err := agentops.ScanOrphans(g.deps())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0, got %+v", got)
	}
}

func TestApplyGC_RemovesNonFreshOrphans(t *testing.T) {
	var removedAll, removedWT []string
	deps := agentops.GCDeps{
		Now:           time.Now,
		ListWorktrees: func() (map[string]bool, error) { return nil, nil },
		RemoveWorktree: func(p string) error {
			removedWT = append(removedWT, p)
			return nil
		},
		RemoveAll: func(p string) error {
			removedAll = append(removedAll, p)
			return nil
		},
		SprawlRoot: t.TempDir(),
	}
	orphans := []agentops.OrphanRecord{
		{Name: "a", DirPath: "/d/a", WorktreePath: "/wt/a", Fresh: false},
		{Name: "b", DirPath: "/d/b", WorktreePath: "/wt/b", Fresh: false},
	}
	res := agentops.ApplyGC(deps, orphans)
	sort.Strings(removedAll)
	sort.Strings(removedWT)
	if len(res.Removed) != 2 || len(res.WorktreesGone) != 2 {
		t.Errorf("Removed=%v WorktreesGone=%v", res.Removed, res.WorktreesGone)
	}
	if len(res.Skipped) != 0 || len(res.Errors) != 0 {
		t.Errorf("Skipped=%v Errors=%v", res.Skipped, res.Errors)
	}
	if len(removedAll) != 2 || len(removedWT) != 2 {
		t.Errorf("calls: all=%v wt=%v", removedAll, removedWT)
	}
}

func TestApplyGC_WorktreeRemovedBeforeDir(t *testing.T) {
	var calls []string
	deps := agentops.GCDeps{
		Now:           time.Now,
		ListWorktrees: func() (map[string]bool, error) { return nil, nil },
		RemoveWorktree: func(p string) error {
			calls = append(calls, "wt:"+p)
			return nil
		},
		RemoveAll: func(p string) error {
			calls = append(calls, "dir:"+p)
			return nil
		},
	}
	orphans := []agentops.OrphanRecord{
		{Name: "a", DirPath: "/d/a", WorktreePath: "/wt/a", Fresh: false},
	}
	_ = agentops.ApplyGC(deps, orphans)
	if len(calls) != 2 {
		t.Fatalf("want 2 calls, got %v", calls)
	}
	if calls[0] != "wt:/wt/a" {
		t.Errorf("RemoveWorktree must be called first, got order: %v", calls)
	}
	if calls[1] != "dir:/d/a" {
		t.Errorf("RemoveAll must be called second, got order: %v", calls)
	}
}

func TestApplyGC_SkipsFresh(t *testing.T) {
	calledRA, calledRW := 0, 0
	deps := agentops.GCDeps{
		Now:            time.Now,
		ListWorktrees:  func() (map[string]bool, error) { return nil, nil },
		RemoveWorktree: func(string) error { calledRW++; return nil },
		RemoveAll:      func(string) error { calledRA++; return nil },
	}
	orphans := []agentops.OrphanRecord{
		{Name: "a", DirPath: "/d/a", WorktreePath: "/wt/a", Fresh: true},
	}
	res := agentops.ApplyGC(deps, orphans)
	if len(res.Skipped) != 1 {
		t.Errorf("want 1 skipped, got %+v", res.Skipped)
	}
	if calledRA != 0 || calledRW != 0 {
		t.Errorf("removal called on fresh: RA=%d RW=%d", calledRA, calledRW)
	}
	if len(res.Removed) != 0 {
		t.Errorf("Removed should be empty, got %+v", res.Removed)
	}
}

func TestApplyGC_WorktreeRemoveErrorContinues(t *testing.T) {
	var removedAll []string
	deps := agentops.GCDeps{
		Now:            time.Now,
		ListWorktrees:  func() (map[string]bool, error) { return nil, nil },
		RemoveWorktree: func(string) error { return errors.New("git wt fail") },
		RemoveAll: func(p string) error {
			removedAll = append(removedAll, p)
			return nil
		},
	}
	orphans := []agentops.OrphanRecord{
		{Name: "a", DirPath: "/d/a", WorktreePath: "/wt/a", Fresh: false},
	}
	res := agentops.ApplyGC(deps, orphans)
	if len(res.Errors) == 0 {
		t.Errorf("expected error recorded")
	}
	if len(removedAll) != 1 {
		t.Errorf("RemoveAll should still be called, got %v", removedAll)
	}
	if len(res.Removed) != 1 {
		t.Errorf("dir should be in Removed: %v", res.Removed)
	}
}

func TestApplyGC_NoWorktreePath_OnlyDirRemoved(t *testing.T) {
	calledRW := 0
	var removedAll []string
	deps := agentops.GCDeps{
		Now:            time.Now,
		ListWorktrees:  func() (map[string]bool, error) { return nil, nil },
		RemoveWorktree: func(string) error { calledRW++; return nil },
		RemoveAll: func(p string) error {
			removedAll = append(removedAll, p)
			return nil
		},
	}
	orphans := []agentops.OrphanRecord{
		{Name: "a", DirPath: "/d/a", WorktreePath: "", Fresh: false},
	}
	res := agentops.ApplyGC(deps, orphans)
	if calledRW != 0 {
		t.Errorf("RemoveWorktree called %d times, want 0", calledRW)
	}
	if len(removedAll) != 1 || len(res.Removed) != 1 {
		t.Errorf("dir remove: %v / %v", removedAll, res.Removed)
	}
}

func TestApplyGC_RemoveAllError(t *testing.T) {
	deps := agentops.GCDeps{
		Now:            time.Now,
		ListWorktrees:  func() (map[string]bool, error) { return nil, nil },
		RemoveWorktree: func(string) error { return nil },
		RemoveAll:      func(string) error { return errors.New("rm fail") },
	}
	orphans := []agentops.OrphanRecord{
		{Name: "a", DirPath: "/d/a", WorktreePath: "", Fresh: false},
	}
	res := agentops.ApplyGC(deps, orphans)
	if len(res.Errors) == 0 {
		t.Errorf("expected Errors")
	}
	if len(res.Removed) != 0 {
		t.Errorf("Removed should be empty, got %v", res.Removed)
	}
}

// --- QUM-632: session wire-log retention pass ---

// seedSessionLog writes <root>/.sprawl/logs/sessions/<agent>/<name> with the
// given mtime and returns the absolute path.
func seedSessionLog(t *testing.T, root, agent, name string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, ".sprawl", "logs", "sessions", agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := os.Chtimes(full, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return full
}

func TestScanSessionLogs_StaleAndFreshAndIgnored(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	oldPath := seedSessionLog(t, root, "agentA", "old.ndjson", now.Add(-40*24*time.Hour))
	freshPath := seedSessionLog(t, root, "agentB", "fresh.ndjson", now)
	// A stray non-.ndjson file must be ignored.
	seedSessionLog(t, root, "agentA", "notes.txt", now.Add(-40*24*time.Hour))

	deps := agentops.GCDeps{
		Now:        func() time.Time { return now },
		RemoveAll:  func(string) error { return nil },
		SprawlRoot: root,
	}
	recs, err := agentops.ScanSessionLogs(deps, agentops.DefaultLogRetention)
	if err != nil {
		t.Fatalf("ScanSessionLogs err: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 ndjson records (txt ignored), got %d: %+v", len(recs), recs)
	}
	byPath := map[string]agentops.SessionLogRecord{}
	for _, r := range recs {
		byPath[r.Path] = r
	}
	if r, ok := byPath[oldPath]; !ok || !r.Stale {
		t.Errorf("old log should be Stale: %+v (present=%v)", r, ok)
	}
	if r, ok := byPath[freshPath]; !ok || r.Stale {
		t.Errorf("fresh log should not be Stale: %+v (present=%v)", r, ok)
	}
}

func TestScanSessionLogs_SessionsDirMissing_ReturnsNil(t *testing.T) {
	deps := agentops.GCDeps{
		Now:        time.Now,
		RemoveAll:  func(string) error { return nil },
		SprawlRoot: t.TempDir(),
	}
	recs, err := agentops.ScanSessionLogs(deps, agentops.DefaultLogRetention)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if recs != nil {
		t.Errorf("want nil for absent sessions dir, got %+v", recs)
	}
}

func TestApplyLogGC_RemovesOnlyStale(t *testing.T) {
	var removed []string
	deps := agentops.GCDeps{
		Now: time.Now,
		RemoveAll: func(p string) error {
			removed = append(removed, p)
			return nil
		},
	}
	recs := []agentops.SessionLogRecord{
		{Path: "/logs/stale.ndjson", Stale: true},
		{Path: "/logs/fresh.ndjson", Stale: false},
	}
	res := agentops.ApplyLogGC(deps, recs)
	if len(removed) != 1 || removed[0] != "/logs/stale.ndjson" {
		t.Errorf("RemoveAll calls = %v, want only the stale path", removed)
	}
	if len(res.Removed) != 1 || res.Removed[0] != "/logs/stale.ndjson" {
		t.Errorf("res.Removed = %v, want only the stale path", res.Removed)
	}
	if len(res.Errors) != 0 {
		t.Errorf("Errors should be empty, got %v", res.Errors)
	}
}

func TestApplyLogGC_RemoveErrorCaptured(t *testing.T) {
	deps := agentops.GCDeps{
		Now:       time.Now,
		RemoveAll: func(string) error { return errors.New("rm fail") },
	}
	recs := []agentops.SessionLogRecord{
		{Path: "/logs/stale.ndjson", Stale: true},
	}
	res := agentops.ApplyLogGC(deps, recs)
	if len(res.Errors) == 0 {
		t.Errorf("expected RemoveAll error captured in Errors")
	}
	if len(res.Removed) != 0 {
		t.Errorf("Removed should be empty on failure, got %v", res.Removed)
	}
}

func TestParseWorktreeListPorcelain_MultipleEntries(t *testing.T) {
	in := []byte(`worktree /repo/main
HEAD abc123
branch refs/heads/main

worktree /repo/.sprawl/worktrees/finn
HEAD def456
branch refs/heads/dmotles/foo

`)
	m := agentops.ParseWorktreeListPorcelain(in)
	if !m["/repo/main"] || !m["/repo/.sprawl/worktrees/finn"] {
		t.Errorf("missing entries: %+v", m)
	}
	if len(m) != 2 {
		t.Errorf("want 2, got %d: %+v", len(m), m)
	}
}

func TestParseWorktreeListPorcelain_Empty(t *testing.T) {
	m := agentops.ParseWorktreeListPorcelain(nil)
	if len(m) != 0 {
		t.Errorf("want empty, got %+v", m)
	}
}

func TestParseWorktreeListPorcelain_DetachedAndBare(t *testing.T) {
	in := []byte(`worktree /repo/main
bare

worktree /repo/detached
HEAD abc123
detached

`)
	m := agentops.ParseWorktreeListPorcelain(in)
	if !m["/repo/main"] || !m["/repo/detached"] {
		t.Errorf("missing entries: %+v", m)
	}
}

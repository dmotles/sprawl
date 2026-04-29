package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

func newTestRetireDeps(t *testing.T) (*RetireDeps, string) {
	t.Helper()
	tmpDir := t.TempDir()

	deps := &RetireDeps{
		WorktreeRemove: func(repoRoot, worktreePath string, force bool) error {
			return os.RemoveAll(worktreePath)
		},
		GitStatus: func(worktreePath string) (string, error) {
			return "", nil
		},
		RemoveAll: os.RemoveAll,
		ReadDir:   os.ReadDir,
		ArchiveMessage: func(sprawlRoot, agent, msgID string) error {
			srcNew := filepath.Join(sprawlRoot, ".sprawl", "messages", agent, "new", msgID+".json")
			srcCur := filepath.Join(sprawlRoot, ".sprawl", "messages", agent, "cur", msgID+".json")
			dstDir := filepath.Join(sprawlRoot, ".sprawl", "messages", agent, "archive")
			if err := os.MkdirAll(dstDir, 0o755); err != nil {
				return err
			}
			switch {
			case fileExists(srcNew):
				return os.Rename(srcNew, filepath.Join(dstDir, msgID+".json"))
			case fileExists(srcCur):
				return os.Rename(srcCur, filepath.Join(dstDir, msgID+".json"))
			default:
				return nil
			}
		},
		Stderr: &bytes.Buffer{},
	}

	return deps, tmpDir
}

func saveRetireTestAgent(t *testing.T, sprawlRoot string, agent *state.AgentState) {
	t.Helper()
	if err := state.SaveAgent(sprawlRoot, agent); err != nil {
		t.Fatalf("saving agent state: %v", err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestRetireAgent_HappyPath(t *testing.T) {
	deps, tmpDir := newTestRetireDeps(t)
	worktree := filepath.Join(tmpDir, ".sprawl", "worktrees", "alice")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	agent := &state.AgentState{
		Name:     "alice",
		Status:   "active",
		Branch:   "sprawl/alice",
		Worktree: worktree,
		Parent:   "weave",
	}
	saveRetireTestAgent(t, tmpDir, agent)

	if err := RetireAgent(deps, tmpDir, agent, false, true); err != nil {
		t.Fatalf("RetireAgent() error: %v", err)
	}

	if _, err := state.LoadAgent(tmpDir, "alice"); err == nil {
		t.Fatal("expected agent state to be deleted")
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("expected worktree to be removed, stat err = %v", err)
	}
}

func TestRetireAgent_DirtyWorktree_RefusesWithoutForce(t *testing.T) {
	deps, tmpDir := newTestRetireDeps(t)
	deps.GitStatus = func(string) (string, error) { return "M file.go", nil }

	agent := &state.AgentState{
		Name:     "alice",
		Status:   "active",
		Branch:   "sprawl/alice",
		Worktree: filepath.Join(tmpDir, "worktree"),
	}
	saveRetireTestAgent(t, tmpDir, agent)

	err := RetireAgent(deps, tmpDir, agent, false, true)
	if err == nil {
		t.Fatal("expected dirty worktree error")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error = %q, want uncommitted changes", err)
	}
	if _, loadErr := state.LoadAgent(tmpDir, "alice"); loadErr != nil {
		t.Fatalf("expected state file to remain after refused retire: %v", loadErr)
	}
}

func TestRetireAgent_DirtyWorktree_ForceOverrides(t *testing.T) {
	deps, tmpDir := newTestRetireDeps(t)
	deps.GitStatus = func(string) (string, error) { return "M file.go", nil }

	var gotForce bool
	deps.WorktreeRemove = func(repoRoot, worktreePath string, force bool) error {
		gotForce = force
		return nil
	}

	agent := &state.AgentState{
		Name:     "alice",
		Status:   "active",
		Branch:   "sprawl/alice",
		Worktree: filepath.Join(tmpDir, "worktree"),
	}
	saveRetireTestAgent(t, tmpDir, agent)

	if err := RetireAgent(deps, tmpDir, agent, true, true); err != nil {
		t.Fatalf("RetireAgent() error: %v", err)
	}
	if !gotForce {
		t.Fatal("expected force=true to reach WorktreeRemove")
	}
}

func TestRetireAgent_ArchivesMessagesFromNewAndCur(t *testing.T) {
	deps, tmpDir := newTestRetireDeps(t)
	worktree := filepath.Join(tmpDir, ".sprawl", "worktrees", "alice")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	agent := &state.AgentState{
		Name:     "alice",
		Status:   "active",
		Branch:   "sprawl/alice",
		Worktree: worktree,
	}
	saveRetireTestAgent(t, tmpDir, agent)

	for _, subdir := range []string{"new", "cur"} {
		dir := filepath.Join(tmpDir, ".sprawl", "messages", "alice", subdir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", subdir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, subdir+".json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("write %s message: %v", subdir, err)
		}
	}

	if err := RetireAgent(deps, tmpDir, agent, false, true); err != nil {
		t.Fatalf("RetireAgent() error: %v", err)
	}

	for _, name := range []string{"new", "cur"} {
		archived := filepath.Join(tmpDir, ".sprawl", "messages", "alice", "archive", name+".json")
		if _, err := os.Stat(archived); err != nil {
			t.Fatalf("expected archived message %s: %v", archived, err)
		}
	}
}

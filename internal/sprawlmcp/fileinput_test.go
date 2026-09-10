package sprawlmcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
)

// File-path input for the goal tools (QUM-1347).
//
// The tools take EITHER inline text OR a path, and on a path the CONTENT is what
// is persisted — a path is a claim about one host's disk at one moment, and the
// ledger has to answer for a result long after that. So every failure mode here
// has to be a refusal: a missing file, a directory, or a path outside the
// agent's own worktree must produce an error and NOTHING appended.

// fileInputEnv is a sprawl root holding one agent whose worktree is a real
// directory, wired the way cmd/enter.go wires the production server.
type fileInputEnv struct {
	server   *Server
	worktree string
	root     string
}

func newFileInputEnv(t *testing.T) fileInputEnv {
	t.Helper()
	root := t.TempDir()
	worktree := filepath.Join(root, "worktrees", "finn")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := state.SaveAgent(root, &state.AgentState{Name: "finn", Type: "engineer", Worktree: worktree}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return fileInputEnv{server: New(nil).WithConfig(cfg), worktree: worktree, root: root}
}

func (e fileInputEnv) write(t *testing.T, rel, content string) string {
	t.Helper()
	path := filepath.Join(e.worktree, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestReadInputFile_ReadsRelativeAndAbsolutePathsInsideTheWorktree.
//
// Both shapes are accepted because an agent writes both: a relative path is
// what it typed, and an absolute one is what its own tools hand back.
func TestReadInputFile_ReadsRelativeAndAbsolutePathsInsideTheWorktree(t *testing.T) {
	e := newFileInputEnv(t)
	abs := e.write(t, "notes/result.md", "the whole result\n")

	for _, path := range []string{"notes/result.md", "./notes/result.md", abs} {
		got, err := e.server.readInputFile(callerCtx("finn"), "summary_file", path)
		if err != nil {
			t.Fatalf("readInputFile(%q): %v", path, err)
		}
		if got != "the whole result\n" {
			t.Errorf("readInputFile(%q) = %q, want the file's contents", path, got)
		}
	}
}

// TestReadInputFile_Refusals. Every case must refuse: the caller is about to
// append a contract event, and a path it cannot read must not become an empty
// or partial one.
func TestReadInputFile_Refusals(t *testing.T) {
	e := newFileInputEnv(t)
	e.write(t, "notes/result.md", "content")
	outside := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(outside, []byte("someone else's file"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	for _, tc := range []struct {
		name    string
		path    string
		wantErr string
	}{
		{"missing file", "notes/gone.md", "does not exist"},
		{"escapes via ..", "../../etc/passwd", "outside"},
		{"absolute path outside the worktree", outside, "outside"},
		{"a directory, not a file", "notes", "not a file"},
		{"empty file", "notes/empty.md", "is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "empty file" {
				e.write(t, "notes/empty.md", "")
			}
			_, err := e.server.readInputFile(callerCtx("finn"), "summary_file", tc.path)
			if err == nil {
				t.Fatalf("readInputFile(%q) was accepted", tc.path)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should say %q; got: %v", tc.wantErr, err)
			}
			if !strings.Contains(err.Error(), "summary_file") {
				t.Errorf("the error does not name the argument at fault: %v", err)
			}
		})
	}
}

// TestReadInputFile_RefusesAFileTooBigToStore. The boundary is an arbitrary
// file on disk, so the bound is enforced here rather than discovered as a
// database error after the agent believes it has reported.
func TestReadInputFile_RefusesAFileTooBigToStore(t *testing.T) {
	e := newFileInputEnv(t)
	e.write(t, "huge.md", strings.Repeat("x", maxInputFileBytes+1))

	_, err := e.server.readInputFile(callerCtx("finn"), "text_file", "huge.md")
	if err == nil {
		t.Fatal("a file over the size bound was accepted")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("the error does not say the file is too large: %v", err)
	}

	// Control: a file at exactly the bound IS accepted, so the check is a
	// bound and not a blanket refusal.
	e.write(t, "big.md", strings.Repeat("x", maxInputFileBytes))
	if _, err := e.server.readInputFile(callerCtx("finn"), "text_file", "big.md"); err != nil {
		t.Errorf("a file at exactly the bound was refused: %v", err)
	}
}

// TestReadInputFile_RefusesWhenTheCallersWorktreeIsUnknown.
//
// The containment check IS the path safety, so a caller whose worktree cannot
// be resolved has no root to be contained in — accepting the path anyway would
// silently turn the feature into "read any file on the host".
func TestReadInputFile_RefusesWhenTheCallersWorktreeIsUnknown(t *testing.T) {
	e := newFileInputEnv(t)
	e.write(t, "notes/result.md", "content")

	if _, err := e.server.readInputFile(callerCtx("nobody"), "summary_file", "notes/result.md"); err == nil {
		t.Fatal("a file was read for an agent with no state record; there was no worktree to contain it")
	}
	// No config at all is the other shape of the same hole.
	if _, err := New(nil).readInputFile(callerCtx("finn"), "summary_file", "notes/result.md"); err == nil {
		t.Fatal("a file was read with no sprawl root configured")
	}
}

// TestExactlyOneTextInput pins the mutual exclusion, which is the whole reason
// this is not two independent optional arguments: given both, a tool that
// preferred one would silently discard the other.
func TestExactlyOneTextInput(t *testing.T) {
	for _, tc := range []struct {
		name, inline, path, wantErr string
	}{
		{"neither", "", "", "exactly one"},
		{"both", "inline", "file.md", "not both"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := exactlyOneTextInput("summary", "summary_file", tc.inline, tc.path)
			if err == nil {
				t.Fatal("the argument pair was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should say %q; got: %v", tc.wantErr, err)
			}
		})
	}
	// Both controls: either one alone is fine.
	if err := exactlyOneTextInput("summary", "summary_file", "inline", ""); err != nil {
		t.Errorf("inline-only was refused: %v", err)
	}
	if err := exactlyOneTextInput("summary", "summary_file", "", "file.md"); err != nil {
		t.Errorf("file-only was refused: %v", err)
	}
}

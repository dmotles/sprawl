package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestEnterDeps(t *testing.T) *enterDeps {
	t.Helper()
	return &enterDeps{
		getenv: func(string) string { return "" },
		runProgram: func(tea.Model) error {
			return nil
		},
	}
}

func TestEnter_NoSprawlRoot(t *testing.T) {
	deps := newTestEnterDeps(t)
	deps.getenv = func(key string) string { return "" }

	err := runEnter(deps)
	if err == nil {
		t.Fatal("expected error when SPRAWL_ROOT is empty")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error = %q, want it to mention SPRAWL_ROOT", err.Error())
	}
}

func TestEnter_Success(t *testing.T) {
	tmpDir := t.TempDir()

	// Write accent-color file so it can be read by state.ReadAccentColor().
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "accent-color"), []byte("colour212"), 0o644); err != nil {
		t.Fatalf("setup write accent-color: %v", err)
	}

	var programCalled bool
	deps := &enterDeps{
		getenv: func(key string) string {
			switch key {
			case "SPRAWL_ROOT":
				return tmpDir
			default:
				return ""
			}
		},
		runProgram: func(m tea.Model) error {
			programCalled = true
			return nil
		},
	}

	err := runEnter(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !programCalled {
		t.Error("runProgram should have been called")
	}
}

func TestEnter_ProgramError(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "accent-color"), []byte("colour212"), 0o644); err != nil {
		t.Fatalf("setup write accent-color: %v", err)
	}

	programErr := errors.New("program failed")
	deps := &enterDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(tea.Model) error {
			return programErr
		},
	}

	err := runEnter(deps)
	if err == nil {
		t.Fatal("expected error when runProgram fails")
	}
	if !strings.Contains(err.Error(), "program failed") {
		t.Errorf("error = %q, want it to contain 'program failed'", err.Error())
	}
}

func TestEnter_DefaultAccentColor(t *testing.T) {
	tmpDir := t.TempDir()

	// Create state dir but no accent-color file.
	stateDir := filepath.Join(tmpDir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}

	var programCalled bool
	deps := &enterDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(tea.Model) error {
			programCalled = true
			return nil
		},
	}

	err := runEnter(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !programCalled {
		t.Error("runProgram should have been called even without accent-color file")
	}
}

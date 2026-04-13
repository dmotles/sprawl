package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
)

func newTestColorDeps(t *testing.T) (*colorDeps, *mockRunner, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	runner := &mockRunner{}
	deps := &colorDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		stdout:     &stdout,
		stderr:     &stderr,
		tmuxRunner: runner,
	}
	return deps, runner, tmpDir, &stdout, &stderr
}

func TestColorShow_WithSavedColor(t *testing.T) {
	deps, _, tmpDir, stdout, _ := newTestColorDeps(t)

	if err := state.WriteAccentColor(tmpDir, "colour39"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := runColorShow(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "colour39") {
		t.Errorf("output should contain colour name, got: %s", out)
	}
	if !strings.Contains(out, "cyan") {
		t.Errorf("output should contain alias, got: %s", out)
	}
}

func TestColorShow_NoColor(t *testing.T) {
	deps, _, _, _, _ := newTestColorDeps(t)

	err := runColorShow(deps)
	if err == nil {
		t.Fatal("expected error when no color is set")
	}
	if !strings.Contains(err.Error(), "no accent color set") {
		t.Errorf("error = %q, want it to contain 'no accent color set'", err.Error())
	}
}

func TestColorList_MarksCurrent(t *testing.T) {
	deps, _, tmpDir, stdout, _ := newTestColorDeps(t)

	if err := state.WriteAccentColor(tmpDir, "colour39"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := runColorList(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := stdout.String()
	// All colors should be listed
	for _, c := range tmux.AccentColors {
		if !strings.Contains(out, c.Name) {
			t.Errorf("output missing color %q", c.Name)
		}
	}
	// Current color should be marked
	lines := strings.Split(out, "\n")
	found := false
	for _, line := range lines {
		if strings.Contains(line, "colour39") && strings.Contains(line, "*") {
			found = true
			break
		}
	}
	if !found {
		t.Error("current color (colour39) should be marked with *")
	}
}

func TestColorRotate_PicksNew(t *testing.T) {
	deps, runner, tmpDir, _, _ := newTestColorDeps(t)

	if err := state.WriteAccentColor(tmpDir, "colour39"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := state.WriteNamespace(tmpDir, "⚡"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := state.WriteVersion(tmpDir, "v0.1.0"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := runColorRotate(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newColor := state.ReadAccentColor(tmpDir)
	if newColor == "colour39" {
		t.Error("rotate should pick a different color")
	}
	if newColor == "" {
		t.Error("rotate should persist the new color")
	}
	if !runner.sourceFileCalled {
		t.Error("expected SourceFile to be called to apply config")
	}
}

func TestColorSet_ByName(t *testing.T) {
	deps, runner, tmpDir, _, _ := newTestColorDeps(t)

	if err := state.WriteNamespace(tmpDir, "⚡"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := runColorSet(deps, "colour198")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := state.ReadAccentColor(tmpDir)
	if got != "colour198" {
		t.Errorf("accent color = %q, want %q", got, "colour198")
	}
	if !runner.sourceFileCalled {
		t.Error("expected SourceFile to be called")
	}
}

func TestColorSet_ByAlias(t *testing.T) {
	deps, _, tmpDir, _, _ := newTestColorDeps(t)

	if err := state.WriteNamespace(tmpDir, "⚡"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := runColorSet(deps, "cyan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := state.ReadAccentColor(tmpDir)
	if got != "colour39" {
		t.Errorf("accent color = %q, want %q", got, "colour39")
	}
}

func TestColorSet_Invalid(t *testing.T) {
	deps, _, _, _, _ := newTestColorDeps(t)

	err := runColorSet(deps, "nonexistent")
	if err == nil {
		t.Fatal("expected error for invalid color")
	}
	if !strings.Contains(err.Error(), "unknown color") {
		t.Errorf("error = %q, want it to contain 'unknown color'", err.Error())
	}
	// Error should list available colors
	if !strings.Contains(err.Error(), "colour39") {
		t.Errorf("error should list available colors, got: %q", err.Error())
	}
}

func TestColorRotate_NoExistingColor(t *testing.T) {
	deps, _, tmpDir, _, _ := newTestColorDeps(t)

	if err := state.WriteNamespace(tmpDir, "⚡"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := runColorRotate(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := state.ReadAccentColor(tmpDir)
	if got == "" {
		t.Error("rotate should persist a color even with no previous color")
	}
}

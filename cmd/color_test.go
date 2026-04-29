package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/runtimecfg"
	"github.com/dmotles/sprawl/internal/state"
)

func newTestColorDeps(t *testing.T) (*colorDeps, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	deps := &colorDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		stdout: &stdout,
		stderr: &stderr,
	}
	return deps, tmpDir, &stdout, &stderr
}

func TestColorShow_WithSavedColor(t *testing.T) {
	deps, tmpDir, stdout, _ := newTestColorDeps(t)
	if err := state.WriteAccentColor(tmpDir, "colour39"); err != nil {
		t.Fatalf("WriteAccentColor: %v", err)
	}

	if err := runColorShow(deps); err != nil {
		t.Fatalf("runColorShow() error: %v", err)
	}
	if out := stdout.String(); !strings.Contains(out, "colour39") || !strings.Contains(out, "cyan") {
		t.Fatalf("output = %q, want colour39 + cyan alias", out)
	}
}

func TestColorList_MarksCurrent(t *testing.T) {
	deps, tmpDir, stdout, _ := newTestColorDeps(t)
	if err := state.WriteAccentColor(tmpDir, "colour39"); err != nil {
		t.Fatalf("WriteAccentColor: %v", err)
	}

	if err := runColorList(deps); err != nil {
		t.Fatalf("runColorList() error: %v", err)
	}
	out := stdout.String()
	for _, color := range runtimecfg.AccentColors {
		if !strings.Contains(out, color.Name) {
			t.Fatalf("output missing color %q", color.Name)
		}
	}
	if !strings.Contains(out, "* colour39") {
		t.Fatalf("output should mark current color, got %q", out)
	}
}

func TestColorRotate_PersistsWithoutApplyingLiveTmux(t *testing.T) {
	deps, tmpDir, stdout, stderr := newTestColorDeps(t)
	if err := state.WriteAccentColor(tmpDir, "colour39"); err != nil {
		t.Fatalf("WriteAccentColor: %v", err)
	}

	if err := runColorRotate(deps); err != nil {
		t.Fatalf("runColorRotate() error: %v", err)
	}
	if got := state.ReadAccentColor(tmpDir); got == "" || got == "colour39" {
		t.Fatalf("accent color = %q, want rotated persisted color", got)
	}
	if !strings.Contains(stdout.String(), "Accent color changed: colour39 ->") {
		t.Fatalf("stdout = %q, want rotate confirmation", stdout.String())
	}
	if !strings.Contains(stderr.String(), "next time you run `sprawl enter`") {
		t.Fatalf("stderr = %q, want deferred apply guidance", stderr.String())
	}
}

func TestColorSet_PersistsWithoutApplyingLiveTmux(t *testing.T) {
	deps, tmpDir, stdout, stderr := newTestColorDeps(t)

	if err := runColorSet(deps, "colour198"); err != nil {
		t.Fatalf("runColorSet() error: %v", err)
	}
	if got := state.ReadAccentColor(tmpDir); got != "colour198" {
		t.Fatalf("accent color = %q, want colour198", got)
	}
	if !strings.Contains(stdout.String(), "Accent color set to colour198") {
		t.Fatalf("stdout = %q, want set confirmation", stdout.String())
	}
	if !strings.Contains(stderr.String(), "next time you run `sprawl enter`") {
		t.Fatalf("stderr = %q, want deferred apply guidance", stderr.String())
	}
}

func TestColorSet_ByAlias(t *testing.T) {
	deps, tmpDir, _, _ := newTestColorDeps(t)

	if err := runColorSet(deps, "cyan"); err != nil {
		t.Fatalf("runColorSet() error: %v", err)
	}
	if got := state.ReadAccentColor(tmpDir); got != "colour39" {
		t.Fatalf("accent color = %q, want colour39", got)
	}
}

func TestColorSet_Invalid(t *testing.T) {
	deps, _, _, _ := newTestColorDeps(t)
	err := runColorSet(deps, "nonexistent")
	if err == nil {
		t.Fatal("expected invalid color error")
	}
	if !strings.Contains(err.Error(), "unknown color") {
		t.Fatalf("error = %q, want unknown color", err)
	}
}

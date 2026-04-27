package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// withDeprecationCapture wires up a buffer to deprecationStderr and a stub
// getenv that returns the given quiet value. The returned cleanup restores
// the package globals; callers should also defer t.Cleanup(resetDeprecationOnce).
func withDeprecationCapture(t *testing.T, quiet string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevW, prevG := deprecationStderr, deprecationGetenv
	deprecationStderr = &buf
	deprecationGetenv = func(k string) string {
		if k == "SPRAWL_QUIET_DEPRECATIONS" {
			return quiet
		}
		return ""
	}
	t.Cleanup(func() {
		deprecationStderr = prevW
		deprecationGetenv = prevG
	})
	t.Cleanup(resetDeprecationOnce)
	resetDeprecationOnce()
	return &buf
}

func TestDeprecationWarning_EmitsToStderr(t *testing.T) {
	buf := withDeprecationCapture(t, "")

	deprecationWarning("spawn", "sprawl_spawn")

	out := buf.String()
	for _, want := range []string{
		"warning:",
		"`sprawl spawn` is deprecated",
		"Use the sprawl_spawn MCP tool",
		"removed in a future release",
		"SPRAWL_QUIET_DEPRECATIONS=1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestDeprecationWarning_QuietEnvSuppresses(t *testing.T) {
	buf := withDeprecationCapture(t, "1")

	deprecationWarning("spawn", "sprawl_spawn")

	if buf.Len() != 0 {
		t.Errorf("expected no output with SPRAWL_QUIET_DEPRECATIONS=1, got: %q", buf.String())
	}
}

func TestDeprecationWarning_QuietEnvAnyNonEmpty(t *testing.T) {
	// Spec: "any non-empty value" suppresses. Verify we don't accidentally
	// require "1" specifically.
	buf := withDeprecationCapture(t, "yes")

	deprecationWarning("spawn", "sprawl_spawn")

	if buf.Len() != 0 {
		t.Errorf("expected no output with SPRAWL_QUIET_DEPRECATIONS=yes, got: %q", buf.String())
	}
}

func TestDeprecationWarning_OnlyOncePerProcess(t *testing.T) {
	buf := withDeprecationCapture(t, "")

	deprecationWarning("spawn", "sprawl_spawn")
	deprecationWarning("retire", "sprawl_retire")
	deprecationWarning("kill", "sprawl_kill")

	got := strings.Count(buf.String(), "warning:")
	if got != 1 {
		t.Errorf("expected exactly one warning, got %d:\n%s", got, buf.String())
	}
}

func TestDeprecationWarningCustom_FreeFormBody(t *testing.T) {
	buf := withDeprecationCapture(t, "")

	deprecationWarningCustom("init", "tmux mode is being removed; use `sprawl enter` for TUI mode")

	out := buf.String()
	for _, want := range []string{
		"warning:",
		"`sprawl init` is deprecated",
		"tmux mode is being removed",
		"`sprawl enter`",
		"SPRAWL_QUIET_DEPRECATIONS=1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestDeprecationWarningCustom_QuietEnvSuppresses(t *testing.T) {
	buf := withDeprecationCapture(t, "1")

	deprecationWarningCustom("poke", "this CLI form will be removed in a future release")

	if buf.Len() != 0 {
		t.Errorf("expected no output, got: %q", buf.String())
	}
}

func TestDeprecationWarning_CustomAndPlainShareGate(t *testing.T) {
	// Both helpers share the once-per-process gate so a process that hits
	// e.g. a deprecated flag in a custom path AND the plain helper still
	// only logs once.
	buf := withDeprecationCapture(t, "")

	deprecationWarning("spawn", "sprawl_spawn")
	deprecationWarningCustom("init", "tmux mode is being removed")

	got := strings.Count(buf.String(), "warning:")
	if got != 1 {
		t.Errorf("expected one warning across both helpers, got %d:\n%s", got, buf.String())
	}
}

// TestMain silences deprecation output during the cmd-package test run so that
// tests touching deprecated runX helpers do not pollute test output. Per-test
// capture is opt-in via withDeprecationCapture. Because of the package-global
// once-per-process gate, suppression here also keeps test ordering from
// affecting which test happens to "win" the lone real-stderr emission.
func TestMain(m *testing.M) {
	deprecationStderr = io.Discard
	os.Exit(m.Run())
}

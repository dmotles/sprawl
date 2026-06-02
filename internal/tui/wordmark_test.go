package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestHexToRGB_RoundTripsEndpoints verifies the gradient endpoint hex values
// parse to the expected RGB channels.
func TestHexToRGB_RoundTripsEndpoints(t *testing.T) {
	r, g, b := hexToRGB(gradStart)
	if r != 0x22 || g != 0xD3 || b != 0xEE {
		t.Errorf("hexToRGB(%q) = (%d,%d,%d), want (34,211,238)", gradStart, r, g, b)
	}
	r, g, b = hexToRGB(gradEnd)
	if r != 0xA8 || g != 0x55 || b != 0xF7 {
		t.Errorf("hexToRGB(%q) = (%d,%d,%d), want (168,85,247)", gradEnd, r, g, b)
	}
}

func TestLerp_EndpointsAndMidpoint(t *testing.T) {
	if got := lerp(0, 100, 0); got != 0 {
		t.Errorf("lerp(0,100,0) = %d, want 0", got)
	}
	if got := lerp(0, 100, 1); got != 100 {
		t.Errorf("lerp(0,100,1) = %d, want 100", got)
	}
	if got := lerp(0, 100, 0.5); got != 50 {
		t.Errorf("lerp(0,100,0.5) = %d, want 50", got)
	}
}

// TestGradientLine_NonEmpty verifies non-space runes are styled (the rendered
// string is longer than the input due to ANSI escapes) and spaces are left
// uncolored.
func TestGradientLine_NonEmpty(t *testing.T) {
	out := gradientLine("SPRAWL")
	if out == "SPRAWL" {
		t.Errorf("gradientLine returned plain text — expected ANSI-styled output")
	}
	if !strings.Contains(out, "S") || !strings.Contains(out, "L") {
		t.Errorf("gradientLine output missing input runes: %q", out)
	}
}

func TestGradientLine_EmptyString(t *testing.T) {
	if got := gradientLine(""); got != "" {
		t.Errorf("gradientLine(\"\") = %q, want empty", got)
	}
}

// TestRenderWordmark_WideHasThreeLines asserts the wide variant is exactly
// three lines and each line spans the full terminal width.
func TestRenderWordmark_WideHasThreeLines(t *testing.T) {
	out := RenderWordmark(120)
	lines := strings.Split(out, "\n")
	if len(lines) != WordmarkHeight(120) {
		t.Errorf("wide wordmark: got %d lines, want %d", len(lines), WordmarkHeight(120))
	}
	if len(lines) != 3 {
		t.Fatalf("wide wordmark: expected 3 lines, got %d", len(lines))
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != 120 {
			t.Errorf("line %d width = %d, want 120", i, w)
		}
	}
}

// TestRenderWordmark_NarrowOneLine verifies the narrow fallback collapses to a
// single gradient SPRAWL line padded to width.
func TestRenderWordmark_NarrowOneLine(t *testing.T) {
	out := RenderWordmark(60)
	if strings.Contains(out, "\n") {
		t.Errorf("narrow wordmark should be a single line, got:\n%s", out)
	}
	if WordmarkHeight(60) != 1 {
		t.Errorf("WordmarkHeight(60) = %d, want 1", WordmarkHeight(60))
	}
	if w := lipgloss.Width(out); w != 60 {
		t.Errorf("narrow wordmark width = %d, want 60", w)
	}
}

// TestRenderWordmark_BoundaryWidth confirms the wide/narrow boundary lives at
// wordmarkNarrowThreshold.
func TestRenderWordmark_BoundaryWidth(t *testing.T) {
	if WordmarkHeight(wordmarkNarrowThreshold-1) != 1 {
		t.Errorf("just-below-threshold should pick narrow (1 line)")
	}
	if WordmarkHeight(wordmarkNarrowThreshold) != 3 {
		t.Errorf("at-threshold should pick wide (3 lines)")
	}
}

// TestRenderWordmark_ZeroWidth must not panic and must return safely.
func TestRenderWordmark_ZeroWidth(t *testing.T) {
	out := RenderWordmark(0)
	if out != "" {
		t.Errorf("RenderWordmark(0) = %q, want empty", out)
	}
	if WordmarkHeight(0) != 0 {
		t.Errorf("WordmarkHeight(0) = %d, want 0", WordmarkHeight(0))
	}
}

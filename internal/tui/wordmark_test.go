package tui

import (
	"strings"
	"testing"
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

// TestWordmarkHeight covers the row-count contract RenderHeader depends on:
// zero rows at degenerate width, one row below the narrow threshold, and the
// full glyph height at or above it.
func TestWordmarkHeight(t *testing.T) {
	if WordmarkHeight(0) != 0 {
		t.Errorf("WordmarkHeight(0) = %d, want 0", WordmarkHeight(0))
	}
	if WordmarkHeight(60) != 1 {
		t.Errorf("WordmarkHeight(60) = %d, want 1", WordmarkHeight(60))
	}
	if WordmarkHeight(wordmarkNarrowThreshold-1) != 1 {
		t.Errorf("just-below-threshold should pick narrow (1 line), got %d", WordmarkHeight(wordmarkNarrowThreshold-1))
	}
	if WordmarkHeight(wordmarkNarrowThreshold) != 3 {
		t.Errorf("at-threshold should pick wide (3 lines), got %d", WordmarkHeight(wordmarkNarrowThreshold))
	}
	if WordmarkHeight(120) != 3 {
		t.Errorf("WordmarkHeight(120) = %d, want 3", WordmarkHeight(120))
	}
}

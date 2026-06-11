package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestSparkleGlyphs_Slice pins the exact glyph cycle so sparkleGlyph and the
// sparkleGlyphs slice (used by containsSparkle) cannot silently drift apart.
func TestSparkleGlyphs_Slice(t *testing.T) {
	want := []string{"✶", "✢", "✽"}
	if len(sparkleGlyphs) != len(want) {
		t.Fatalf("len(sparkleGlyphs) = %d, want %d", len(sparkleGlyphs), len(want))
	}
	for i, g := range want {
		if sparkleGlyphs[i] != g {
			t.Errorf("sparkleGlyphs[%d] = %q, want %q", i, sparkleGlyphs[i], g)
		}
	}
}

// TestSparkleGlyph_Cycles pins the three-frame glyph cycle and guards the
// modulo against negative frame counters.
func TestSparkleGlyph_Cycles(t *testing.T) {
	cases := []struct {
		frame int
		want  string
	}{
		{0, "✶"},
		{1, "✢"},
		{2, "✽"},
		{3, "✶"},
		{4, "✢"},
		{-1, "✽"},
	}
	for _, tc := range cases {
		if got := sparkleGlyph(tc.frame); got != tc.want {
			t.Errorf("sparkleGlyph(%d) = %q, want %q", tc.frame, got, tc.want)
		}
	}
}

// TestSparkleWordForTurn pins the dim status word for each turn state.
func TestSparkleWordForTurn(t *testing.T) {
	cases := []struct {
		state TurnState
		want  string
	}{
		{TurnThinking, "thinking…"},
		{TurnStreaming, "running…"},
		{TurnIdle, ""},
		{TurnComplete, ""},
	}
	for _, tc := range cases {
		if got := sparkleWordForTurn(tc.state); got != tc.want {
			t.Errorf("sparkleWordForTurn(%v) = %q, want %q", tc.state, got, tc.want)
		}
	}
}

// TestRenderSparkle_FaintPrimaryGlyph asserts the glyph is styled with the
// accent (Primary) foreground at Faint, and the dim status word is appended.
func TestRenderSparkle_FaintPrimaryGlyph(t *testing.T) {
	theme := NewTheme("colour212")
	got := renderSparkle(&theme, 0, "thinking…")

	ref := lipgloss.NewStyle().Foreground(theme.Palette.Primary).Faint(true).Render("✶")
	if !strings.Contains(got, ref) {
		t.Errorf("renderSparkle output should contain the faint-Primary styled glyph %q;\n got: %q", ref, got)
	}
	plain := stripANSI(got)
	if !strings.Contains(plain, "✶") {
		t.Errorf("renderSparkle plain output missing glyph; got %q", plain)
	}
	if !strings.Contains(plain, "thinking…") {
		t.Errorf("renderSparkle plain output missing status word; got %q", plain)
	}
}

// TestRenderSparkle_NoWord renders glyph-only when the status word is empty.
func TestRenderSparkle_NoWord(t *testing.T) {
	theme := NewTheme("colour212")
	plain := stripANSI(renderSparkle(&theme, 1, ""))
	if strings.TrimSpace(plain) != "✢" {
		t.Errorf("renderSparkle(frame=1, word=\"\") = %q, want bare glyph %q", plain, "✢")
	}
}

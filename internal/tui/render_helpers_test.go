package tui

import (
	"strings"
	"testing"
)

// QUM-684 — minimal table tests for the lifted helpers. Byte-exact parity
// with the legacy call sites is validated by viewport_test.go and
// items_test.go remaining green; these tests cover the helpers' new
// signatures (default-glyph fallback, leading-newline contract,
// empty-result short-circuit) directly.

func TestToolIndicator(t *testing.T) {
	theme := NewTheme(defaultAccentColor)
	cases := []struct {
		name         string
		pending      bool
		failed       bool
		spinnerFrame string
		defaultGlyph string
		wantContains string
	}{
		{"pending-with-frame", true, false, "⠋", "⠿", "⠋"},
		{"pending-empty-frame-falls-back", true, false, "", "⠿", "⠿"},
		{"failed", false, true, "", "⠿", "✗"},
		{"success", false, false, "", "⠿", "✓"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolIndicator(&theme, tc.pending, tc.failed, tc.spinnerFrame, tc.defaultGlyph)
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("toolIndicator: want contains %q, got %q", tc.wantContains, got)
			}
		})
	}
}

func TestRenderResultPreviewLines(t *testing.T) {
	theme := NewTheme(defaultAccentColor)
	t.Run("empty-result-returns-empty", func(t *testing.T) {
		got := renderResultPreviewLines(&theme, "", false, false, 80)
		if got != "" {
			t.Errorf("want empty, got %q", got)
		}
	})
	t.Run("whitespace-only-returns-empty", func(t *testing.T) {
		got := renderResultPreviewLines(&theme, "\n  \n\n", false, false, 80)
		if got != "" {
			t.Errorf("want empty, got %q", got)
		}
	})
	t.Run("3-line-cap-with-trailer", func(t *testing.T) {
		got := renderResultPreviewLines(&theme, "a\nb\nc\nd\ne", false, false, 80)
		if !strings.HasPrefix(got, "\n") {
			t.Errorf("want leading newline, got %q", got)
		}
		if !strings.Contains(got, "+ 2 more lines") {
			t.Errorf("want trailer for 2 more lines, got %q", got)
		}
	})
	t.Run("expanded-lifts-cap", func(t *testing.T) {
		got := renderResultPreviewLines(&theme, "a\nb\nc\nd\ne", false, true, 80)
		if strings.Contains(got, "more lines") {
			t.Errorf("want no trailer when expanded, got %q", got)
		}
	})
}

func TestRenderUserPromptBlock(t *testing.T) {
	theme := NewTheme(defaultAccentColor)
	t.Run("single-line-has-chevron", func(t *testing.T) {
		got := renderUserPromptBlock(&theme, "hello")
		if !strings.Contains(got, "›") {
			t.Errorf("want chevron, got %q", got)
		}
		if !strings.Contains(got, "hello") {
			t.Errorf("want body, got %q", got)
		}
	})
	t.Run("continuation-lines-hang-indent", func(t *testing.T) {
		got := renderUserPromptBlock(&theme, "line1\nline2")
		lines := strings.Split(got, "\n")
		if len(lines) != 2 {
			t.Fatalf("want 2 lines, got %d: %q", len(lines), got)
		}
		if !strings.HasPrefix(lines[1], "  ") {
			t.Errorf("continuation line should start with two spaces, got %q", lines[1])
		}
	})
}

func TestRenderToolInputBody(t *testing.T) {
	theme := NewTheme(defaultAccentColor)
	t.Run("empty-body-returns-empty", func(t *testing.T) {
		got := renderToolInputBody(&theme, "", 80)
		if got != "" {
			t.Errorf("want empty, got %q", got)
		}
	})
	t.Run("multi-line-each-prefixed-with-newline", func(t *testing.T) {
		got := renderToolInputBody(&theme, "alpha\nbeta", 80)
		if !strings.HasPrefix(got, "\n") {
			t.Errorf("want leading newline, got %q", got)
		}
		// two lines means two leading newlines (one per line).
		if strings.Count(got, "\n") != 2 {
			t.Errorf("want 2 newlines (one per body line), got %d: %q", strings.Count(got, "\n"), got)
		}
		if !strings.Contains(got, "│ alpha") || !strings.Contains(got, "│ beta") {
			t.Errorf("want gutter-prefixed lines, got %q", got)
		}
	})
}

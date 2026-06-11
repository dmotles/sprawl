package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
		{"pending-with-frame-suppresses-default", true, false, "⠋", "⠿", "⠋"},
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

func TestToolIndicator_PendingFrameSuppressesDefaultGlyph(t *testing.T) {
	theme := NewTheme(defaultAccentColor)
	got := toolIndicator(&theme, true, false, "⠋", "⠿")
	if strings.Contains(got, "⠿") {
		t.Errorf("non-empty spinner frame must suppress default glyph; got %q", got)
	}
	if !strings.Contains(got, "⠋") {
		t.Errorf("expected spinner frame ⠋ in output, got %q", got)
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
		// QUM-796 #5 — the elision trailer uses the ⎿ corner glyph, not a
		// `│ ` gutter.
		if !strings.Contains(got, "⎿") {
			t.Errorf("want ⎿ trailer glyph, got %q", got)
		}
		if strings.Contains(stripANSI(got), "│") {
			t.Errorf("preview lines must not use `│` gutter, got %q", stripANSI(got))
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
		got := renderUserPromptBlock(&theme, "hello", 80)
		if !strings.Contains(got, "›") {
			t.Errorf("want chevron, got %q", got)
		}
		if !strings.Contains(got, "hello") {
			t.Errorf("want body, got %q", got)
		}
	})
	t.Run("continuation-lines-hang-indent", func(t *testing.T) {
		got := renderUserPromptBlock(&theme, "line1\nline2", 80)
		lines := strings.Split(got, "\n")
		if len(lines) != 2 {
			t.Fatalf("want 2 lines, got %d: %q", len(lines), got)
		}
		if !strings.HasPrefix(lines[1], "  ") {
			t.Errorf("continuation line should start with two spaces, got %q", lines[1])
		}
	})
	// QUM-797 (a) — a long single source line wraps at word boundaries (not
	// mid-word) AND the wrap budget accounts for the 2-cell prefix. The
	// fixture is engineered so a prefix-unaware wrap (at full width) would
	// overflow: "1234567 abcdefg" is 15 cells — it fits one line at width 16
	// but, plus the chevron prefix, that line would be 17 > 16. A correct
	// (budget=width-2=14) impl must wrap it into two prefixed lines ≤16.
	t.Run("long-line-wraps-prefix-aware-word-boundaries", func(t *testing.T) {
		wordSet := map[string]bool{"1234567": true, "abcdefg": true}
		got := renderUserPromptBlock(&theme, "1234567 abcdefg", 16)
		lines := strings.Split(got, "\n")
		if len(lines) < 2 {
			t.Fatalf("content (15 cells) must wrap at budget width-2=14, got %q", got)
		}
		for _, ln := range lines {
			plain := stripANSI(ln)
			if w := ansi.StringWidth(plain); w > 16 {
				t.Errorf("line width %d exceeds 16 (prefix-unaware wrap?): %q", w, plain)
			}
			body := strings.TrimPrefix(plain, "› ")
			body = strings.TrimPrefix(body, "  ")
			for _, tok := range strings.Fields(body) {
				if !wordSet[tok] {
					t.Errorf("token %q is not a whole input word (mid-word break?): line %q", tok, plain)
				}
			}
		}
	})
	// QUM-797 (a, cont.) — a single token longer than the wrap budget is
	// hard-broken to fit the width (ansi.Wrap behavior); it must never
	// overflow the terminal width.
	t.Run("over-long-token-hard-breaks-to-fit", func(t *testing.T) {
		got := renderUserPromptBlock(&theme, strings.Repeat("x", 40), 16)
		for _, ln := range strings.Split(got, "\n") {
			if w := ansi.StringWidth(stripANSI(ln)); w > 16 {
				t.Errorf("over-long token line exceeds width 16 (got %d): %q", w, stripANSI(ln))
			}
		}
	})
	// QUM-797 (b) — wrapped continuation lines carry the 2-space hang indent
	// under the chevron; only the first visible line gets the chevron.
	t.Run("wrapped-continuation-has-hang-indent", func(t *testing.T) {
		got := renderUserPromptBlock(&theme, "one two three four five six seven eight", 14)
		lines := strings.Split(got, "\n")
		if len(lines) < 2 {
			t.Fatalf("expected wrapping, got %q", got)
		}
		if !strings.Contains(lines[0], "›") {
			t.Errorf("first visible line should carry the chevron, got %q", lines[0])
		}
		for i, ln := range lines[1:] {
			if !strings.HasPrefix(stripANSI(ln), "  ") {
				t.Errorf("continuation line %d should have 2-space hang indent, got %q", i+1, stripANSI(ln))
			}
			if strings.Contains(ln, "›") {
				t.Errorf("continuation line %d must not repeat the chevron, got %q", i+1, ln)
			}
		}
	})
	// QUM-797 (c) — very short widths fall back gracefully (no panic / no
	// infinite loop), still emitting the chevron + content.
	t.Run("narrow-width-fallback", func(t *testing.T) {
		// width 2 (budget 0) and width 1 (budget -1) both fall back to
		// unwrapped — must not panic / loop and must keep content intact.
		for _, w := range []int{2, 1} {
			got := renderUserPromptBlock(&theme, "hello world", w)
			if !strings.Contains(got, "›") {
				t.Errorf("w=%d: narrow fallback should still emit chevron, got %q", w, got)
			}
			if !strings.Contains(stripANSI(got), "hello world") {
				t.Errorf("w=%d: narrow fallback should keep content intact, got %q", w, stripANSI(got))
			}
		}
	})
	// QUM-797 — explicit newlines are preserved as separate wrap units; a
	// long second paragraph wraps independently with hang indent.
	t.Run("multi-paragraph-explicit-newline", func(t *testing.T) {
		got := renderUserPromptBlock(&theme, "short\nalpha bravo charlie delta echo", 16)
		lines := strings.Split(got, "\n")
		if len(lines) < 3 {
			t.Fatalf("expected explicit newline + wrapped second line (>=3 visible), got %q", got)
		}
		if !strings.HasPrefix(stripANSI(lines[1]), "  ") {
			t.Errorf("explicit-newline line should be a 2-space continuation, got %q", stripANSI(lines[1]))
		}
		// The chevron must appear only on the first visible line — explicit
		// newlines must NOT re-emit it per source line.
		for i, ln := range lines[1:] {
			if strings.Contains(ln, "›") {
				t.Errorf("line %d must not repeat the chevron, got %q", i+1, ln)
			}
		}
	})
	t.Run("empty-body-is-chevron-alone", func(t *testing.T) {
		got := renderUserPromptBlock(&theme, "", 80)
		if !strings.Contains(got, "›") {
			t.Errorf("empty body should still render the chevron, got %q", got)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("empty body should be a single line, got %q", got)
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
		// QUM-796 #1 — body lines indent with two spaces, no `│ ` gutter.
		if !strings.Contains(got, "  alpha") || !strings.Contains(got, "  beta") {
			t.Errorf("want two-space-indented lines, got %q", got)
		}
		if strings.Contains(stripANSI(got), "│") {
			t.Errorf("body lines must not use `│` gutter, got %q", stripANSI(got))
		}
	})
}

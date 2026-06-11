package tui

import (
	"testing"
)

func TestComputeLayout_StandardSize(t *testing.T) {
	l := ComputeLayout(80, 24, defaultInputHeight)

	// All dimensions should be positive.
	if l.ViewportWidth <= 0 {
		t.Errorf("ViewportWidth = %d, want > 0", l.ViewportWidth)
	}
	if l.ViewportHeight <= 0 {
		t.Errorf("ViewportHeight = %d, want > 0", l.ViewportHeight)
	}

	// Terminal dimensions stored.
	if l.TermWidth != 80 {
		t.Errorf("TermWidth = %d, want 80", l.TermWidth)
	}
	if l.TermHeight != 24 {
		t.Errorf("TermHeight = %d, want 24", l.TermHeight)
	}
}

func TestComputeLayout_WideTerminal(t *testing.T) {
	l := ComputeLayout(200, 50, defaultInputHeight)
	if l.ViewportWidth <= 0 {
		t.Errorf("ViewportWidth = %d, want > 0", l.ViewportWidth)
	}
}

func TestComputeLayout_MinimumSize(t *testing.T) {
	l := ComputeLayout(80, 24, defaultInputHeight)

	if l.ViewportWidth < 0 {
		t.Errorf("ViewportWidth = %d, want >= 0", l.ViewportWidth)
	}
	if l.ViewportHeight < 0 {
		t.Errorf("ViewportHeight = %d, want >= 0", l.ViewportHeight)
	}
	if l.InputWidth < 0 {
		t.Errorf("InputWidth = %d, want >= 0", l.InputWidth)
	}
	if l.InputHeight < 0 {
		t.Errorf("InputHeight = %d, want >= 0", l.InputHeight)
	}
	if l.StatusWidth < 0 {
		t.Errorf("StatusWidth = %d, want >= 0", l.StatusWidth)
	}
	if l.StatusHeight < 0 {
		t.Errorf("StatusHeight = %d, want >= 0", l.StatusHeight)
	}
}

func TestComputeLayout_TinyTerminal(t *testing.T) {
	// Should not panic on very small terminal.
	l := ComputeLayout(20, 8, defaultInputHeight)

	if l.ViewportWidth < 0 {
		t.Errorf("ViewportWidth = %d, want >= 0", l.ViewportWidth)
	}
	if l.ViewportHeight < 0 {
		t.Errorf("ViewportHeight = %d, want >= 0", l.ViewportHeight)
	}
	if l.InputWidth < 0 {
		t.Errorf("InputWidth = %d, want >= 0", l.InputWidth)
	}
	if l.InputHeight < 0 {
		t.Errorf("InputHeight = %d, want >= 0", l.InputHeight)
	}
	if l.StatusWidth < 0 {
		t.Errorf("StatusWidth = %d, want >= 0", l.StatusWidth)
	}
	if l.StatusHeight < 0 {
		t.Errorf("StatusHeight = %d, want >= 0", l.StatusHeight)
	}
}

func TestComputeLayout_DimensionsConsistent(t *testing.T) {
	l := ComputeLayout(120, 40, defaultInputHeight)

	// Viewport now claims the full terminal width.
	if l.ViewportWidth != l.TermWidth {
		t.Errorf("ViewportWidth(%d) != TermWidth(%d)", l.ViewportWidth, l.TermWidth)
	}

	// Status bar width should not exceed terminal width.
	if l.StatusWidth > l.TermWidth {
		t.Errorf("StatusWidth(%d) exceeds TermWidth(%d)", l.StatusWidth, l.TermWidth)
	}
}

func TestIsTooSmall(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		want          bool
	}{
		{"zero size", 0, 0, true},
		{"both below minimum", 20, 5, true},
		{"width just below minimum", 39, 10, true},
		{"height just below minimum", 40, 9, true},
		{"exact minimum", 40, 10, false},
		{"normal terminal", 80, 24, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTooSmall(tt.width, tt.height)
			if got != tt.want {
				t.Errorf("IsTooSmall(%d, %d) = %v, want %v", tt.width, tt.height, got, tt.want)
			}
		})
	}
}

func TestMinTermConstants(t *testing.T) {
	if MinTermWidth != 40 {
		t.Errorf("MinTermWidth = %d, want 40", MinTermWidth)
	}
	if MinTermHeight != 10 {
		t.Errorf("MinTermHeight = %d, want 10", MinTermHeight)
	}
}

func TestComputeLayout_ZeroSize(t *testing.T) {
	l := ComputeLayout(0, 0, defaultInputHeight)

	if l.ViewportWidth < 0 {
		t.Errorf("ViewportWidth = %d, want >= 0", l.ViewportWidth)
	}
	if l.ViewportHeight < 0 {
		t.Errorf("ViewportHeight = %d, want >= 0", l.ViewportHeight)
	}
	if l.InputWidth < 0 {
		t.Errorf("InputWidth = %d, want >= 0", l.InputWidth)
	}
	if l.InputHeight < 0 {
		t.Errorf("InputHeight = %d, want >= 0", l.InputHeight)
	}
	if l.StatusWidth < 0 {
		t.Errorf("StatusWidth = %d, want >= 0", l.StatusWidth)
	}
	if l.StatusHeight < 0 {
		t.Errorf("StatusHeight = %d, want >= 0", l.StatusHeight)
	}
}

// QUM-656: with the tree moved into the header, viewport must equal terminal
// width at every size — there is no left-column tree anymore.
func TestComputeLayout_ViewportReclaimsWidth_AllSizes(t *testing.T) {
	tests := []struct {
		name  string
		width int
	}{
		{"80", 80},
		{"120", 120},
		{"160", 160},
		{"240", 240},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := ComputeLayout(tt.width, 40, defaultInputHeight)
			if l.ViewportWidth != l.TermWidth {
				t.Errorf("viewport(%d) must equal term=%d",
					l.ViewportWidth, l.TermWidth)
			}
		})
	}
}

func TestComputeLayout_BelowMinimum(t *testing.T) {
	sizes := []struct {
		name          string
		width, height int
	}{
		{"small", 20, 5},
		{"tiny", 1, 1},
	}

	for _, sz := range sizes {
		t.Run(sz.name, func(t *testing.T) {
			l := ComputeLayout(sz.width, sz.height, defaultInputHeight)

			if l.ViewportWidth < 0 {
				t.Errorf("ComputeLayout(%d,%d): ViewportWidth = %d, want >= 0", sz.width, sz.height, l.ViewportWidth)
			}
			if l.ViewportHeight < 0 {
				t.Errorf("ComputeLayout(%d,%d): ViewportHeight = %d, want >= 0", sz.width, sz.height, l.ViewportHeight)
			}
			if l.InputWidth < 0 {
				t.Errorf("ComputeLayout(%d,%d): InputWidth = %d, want >= 0", sz.width, sz.height, l.InputWidth)
			}
			if l.InputHeight < 0 {
				t.Errorf("ComputeLayout(%d,%d): InputHeight = %d, want >= 0", sz.width, sz.height, l.InputHeight)
			}
			if l.StatusWidth < 0 {
				t.Errorf("ComputeLayout(%d,%d): StatusWidth = %d, want >= 0", sz.width, sz.height, l.StatusWidth)
			}
			if l.StatusHeight < 0 {
				t.Errorf("ComputeLayout(%d,%d): StatusHeight = %d, want >= 0", sz.width, sz.height, l.StatusHeight)
			}
		})
	}
}

func TestComputeLayout_DynamicInputHeight(t *testing.T) {
	small := ComputeLayout(80, 24, defaultInputHeight)
	large := ComputeLayout(80, 24, 8)

	if large.InputHeight != 8 {
		t.Errorf("InputHeight = %d, want 8", large.InputHeight)
	}
	if large.ViewportHeight >= small.ViewportHeight {
		t.Errorf("larger input should shrink viewport: small=%d, large=%d",
			small.ViewportHeight, large.ViewportHeight)
	}
}

func TestComputeLayout_InputHeightClampedToMax(t *testing.T) {
	l := ComputeLayout(80, 24, 20)
	if l.InputHeight != maxInputHeight {
		t.Errorf("InputHeight = %d, want %d (clamped to max)", l.InputHeight, maxInputHeight)
	}
}

func TestComputeLayout_InputHeightClampedToMin(t *testing.T) {
	l := ComputeLayout(80, 24, 0)
	if l.InputHeight != defaultInputHeight {
		t.Errorf("InputHeight = %d, want %d (clamped to default)", l.InputHeight, defaultInputHeight)
	}
}

// QUM-664: the short-help strip was removed from the chassis. Superseded by
// TestComputeLayout_NoShortHelpRow / TestComputeLayout_ReservesHeaderSpacerRow.

func TestComputeLayout_ShortHelpWidthMatchesTerm(t *testing.T) {
	l := ComputeLayout(120, 40, defaultInputHeight)
	if l.ShortHelpWidth != l.TermWidth {
		t.Errorf("ShortHelpWidth = %d, want %d (TermWidth)", l.ShortHelpWidth, l.TermWidth)
	}
}

// QUM-656: with the tree moved to the header, viewport claims the full
// terminal width — no left-column subtraction.
func TestComputeLayout_NoLeftTreeColumn(t *testing.T) {
	l := ComputeLayout(120, 40, defaultInputHeight)
	if l.ViewportWidth != l.TermWidth {
		t.Errorf("ViewportWidth = %d, want %d (TermWidth, no left tree)", l.ViewportWidth, l.TermWidth)
	}
}

// QUM-656: the header carves out a positive width for the orbital tree at
// wide terminal sizes.
func TestComputeLayout_HeaderTreeWidth_Positive_Wide(t *testing.T) {
	l := ComputeLayout(120, 40, defaultInputHeight)
	if l.HeaderTreeWidth <= 0 {
		t.Errorf("HeaderTreeWidth = %d, want > 0 at width=120", l.HeaderTreeWidth)
	}
}

// QUM-664: the short-help strip is removed from the chassis. ShortHelpHeight
// must report 0 (or its field must be gone — kept as 0 here to allow the
// stub to compile during red phase).
func TestComputeLayout_NoShortHelpRow(t *testing.T) {
	l := ComputeLayout(120, 40, defaultInputHeight)
	if l.ShortHelpHeight != 0 {
		t.Errorf("ShortHelpHeight = %d, want 0 (QUM-664 removed short-help row)", l.ShortHelpHeight)
	}
}

// QUM-664: ComputeLayout reserves a single spacer row between the header
// (wordmark) and the chat viewport so the wordmark visually breathes from
// the body content.
func TestComputeLayout_ReservesHeaderSpacerRow(t *testing.T) {
	w, h := 120, 40
	l := ComputeLayout(w, h, defaultInputHeight)
	// HeaderSpacerHeight is a first-class layout output — must be exactly 1.
	if l.HeaderSpacerHeight != 1 {
		t.Errorf("HeaderSpacerHeight = %d, want 1 (QUM-664)", l.HeaderSpacerHeight)
	}
	// mainHeight = termH - status - input - header - headerSpacer - sparkle
	// (no shortHelp anymore; QUM-796 reserves the sparkle row).
	want := h - l.StatusHeight - l.InputHeight - l.HeaderHeight - l.HeaderSpacerHeight - l.SparkleHeight
	if l.ViewportHeight != want {
		t.Errorf("ViewportHeight = %d, want %d (= termH(%d) - status(%d) - input(%d) - header(%d) - spacer(%d) - sparkle(%d))",
			l.ViewportHeight, want, h, l.StatusHeight, l.InputHeight, l.HeaderHeight, l.HeaderSpacerHeight, l.SparkleHeight)
	}
}

// QUM-656: HeaderHeight collapses from 3 (wide) to 1 (narrow) at the
// wordmark-narrow boundary.
func TestComputeLayout_HeaderHeight_Boundary(t *testing.T) {
	if got := ComputeLayout(60, 40, defaultInputHeight).HeaderHeight; got != 1 {
		t.Errorf("HeaderHeight at width=60 = %d, want 1", got)
	}
	if got := ComputeLayout(120, 40, defaultInputHeight).HeaderHeight; got != 3 {
		t.Errorf("HeaderHeight at width=120 = %d, want 3", got)
	}
}

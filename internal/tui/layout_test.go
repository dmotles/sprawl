package tui

import (
	"testing"
)

func TestComputeLayout_StandardSize(t *testing.T) {
	l := ComputeLayout(80, 24, defaultInputHeight)

	// Tree should be roughly 25% of width.
	if l.TreeWidth < 15 || l.TreeWidth > 30 {
		t.Errorf("TreeWidth = %d, want roughly 25%% of 80 (15-30)", l.TreeWidth)
	}

	// All dimensions should be positive.
	if l.TreeHeight <= 0 {
		t.Errorf("TreeHeight = %d, want > 0", l.TreeHeight)
	}
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

	// Tree width should be capped (not grow unbounded with terminal width).
	if l.TreeWidth > 60 {
		t.Errorf("TreeWidth = %d, want capped (<=60) for wide terminal", l.TreeWidth)
	}
	if l.ViewportWidth <= 0 {
		t.Errorf("ViewportWidth = %d, want > 0", l.ViewportWidth)
	}
}

func TestComputeLayout_MinimumSize(t *testing.T) {
	l := ComputeLayout(80, 24, defaultInputHeight)

	// Nothing should be negative.
	if l.TreeWidth < 0 {
		t.Errorf("TreeWidth = %d, want >= 0", l.TreeWidth)
	}
	if l.TreeHeight < 0 {
		t.Errorf("TreeHeight = %d, want >= 0", l.TreeHeight)
	}
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

	if l.TreeWidth < 0 {
		t.Errorf("TreeWidth = %d, want >= 0", l.TreeWidth)
	}
	if l.TreeHeight < 0 {
		t.Errorf("TreeHeight = %d, want >= 0", l.TreeHeight)
	}
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

	// Tree width + viewport width should not exceed terminal width.
	if l.TreeWidth+l.ViewportWidth > l.TermWidth {
		t.Errorf("TreeWidth(%d) + ViewportWidth(%d) = %d, exceeds TermWidth(%d)",
			l.TreeWidth, l.ViewportWidth, l.TreeWidth+l.ViewportWidth, l.TermWidth)
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

	if l.TreeWidth < 0 {
		t.Errorf("TreeWidth = %d, want >= 0", l.TreeWidth)
	}
	if l.TreeHeight < 0 {
		t.Errorf("TreeHeight = %d, want >= 0", l.TreeHeight)
	}
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

func TestComputeLayout_WideTerminalHasActivityPanel(t *testing.T) {
	l := ComputeLayout(160, 40, defaultInputHeight)
	if l.ActivityWidth <= 0 {
		t.Errorf("ActivityWidth = %d, want > 0 at width=160", l.ActivityWidth)
	}
	if l.ActivityHeight <= 0 {
		t.Errorf("ActivityHeight = %d, want > 0 at height=40", l.ActivityHeight)
	}
	// Three-column sum must not exceed terminal width.
	if l.TreeWidth+l.ViewportWidth+l.ActivityWidth > l.TermWidth {
		t.Errorf("tree(%d)+viewport(%d)+activity(%d)=%d exceeds term=%d",
			l.TreeWidth, l.ViewportWidth, l.ActivityWidth,
			l.TreeWidth+l.ViewportWidth+l.ActivityWidth, l.TermWidth)
	}
	if l.ActivityHeight != l.ViewportHeight {
		t.Errorf("ActivityHeight (%d) should equal ViewportHeight (%d)", l.ActivityHeight, l.ViewportHeight)
	}
}

func TestComputeLayout_NarrowTerminalHidesActivityPanel(t *testing.T) {
	l := ComputeLayout(80, 24, defaultInputHeight)
	if l.ActivityWidth != 0 {
		t.Errorf("ActivityWidth = %d, want 0 at narrow width=80 (activity panel hidden)", l.ActivityWidth)
	}
	// Viewport should still fill the remaining space (no activity reservation).
	if l.TreeWidth+l.ViewportWidth != l.TermWidth {
		t.Errorf("when activity panel hidden, tree(%d)+viewport(%d)=%d must equal term=%d",
			l.TreeWidth, l.ViewportWidth, l.TreeWidth+l.ViewportWidth, l.TermWidth)
	}
}

func TestComputeLayout_ActivityWidthClamped(t *testing.T) {
	l := ComputeLayout(400, 50, defaultInputHeight)
	if l.ActivityWidth > 60 {
		t.Errorf("ActivityWidth = %d, want capped (<=60) for very wide terminal", l.ActivityWidth)
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

			if l.TreeWidth < 0 {
				t.Errorf("ComputeLayout(%d,%d): TreeWidth = %d, want >= 0", sz.width, sz.height, l.TreeWidth)
			}
			if l.TreeHeight < 0 {
				t.Errorf("ComputeLayout(%d,%d): TreeHeight = %d, want >= 0", sz.width, sz.height, l.TreeHeight)
			}
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
	if large.TreeHeight >= small.TreeHeight {
		t.Errorf("larger input should shrink tree: small=%d, large=%d",
			small.TreeHeight, large.TreeHeight)
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

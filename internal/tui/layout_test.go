package tui

import (
	"testing"
)

func TestComputeLayout_StandardSize(t *testing.T) {
	l := ComputeLayout(80, 24)

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
	l := ComputeLayout(200, 50)

	// Tree width should be capped (not grow unbounded with terminal width).
	if l.TreeWidth > 60 {
		t.Errorf("TreeWidth = %d, want capped (<=60) for wide terminal", l.TreeWidth)
	}
	if l.ViewportWidth <= 0 {
		t.Errorf("ViewportWidth = %d, want > 0", l.ViewportWidth)
	}
}

func TestComputeLayout_MinimumSize(t *testing.T) {
	l := ComputeLayout(80, 24)

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
	l := ComputeLayout(20, 8)

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
	l := ComputeLayout(120, 40)

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

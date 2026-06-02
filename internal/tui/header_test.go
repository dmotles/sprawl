package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestRenderHeader_WideThreeLines(t *testing.T) {
	out := RenderHeader(120, []string{"a", "b", "c"})
	lines := strings.Split(out, "\n")
	if got, want := len(lines), 3; got != want {
		t.Fatalf("len(lines) = %d, want %d; raw=%q", got, want, out)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 120 {
			t.Errorf("lines[%d] width = %d, want 120", i, w)
		}
	}
	stripped := stripAnsi(out)
	// wordmark uses box-drawing chars; cheap proxy assertion.
	if !strings.Contains(stripped, "╮") {
		t.Errorf("expected wordmark box-drawing char '╮' in stripped header, got:\n%s", stripped)
	}
}

func TestRenderHeader_WideContainsTree(t *testing.T) {
	out := RenderHeader(120, []string{"myagent token line", "", ""})
	stripped := stripAnsi(out)
	if !strings.Contains(stripped, "myagent") {
		t.Errorf("expected 'myagent' from tree lines in header, got:\n%s", stripped)
	}
	if !strings.Contains(stripped, "│") {
		t.Errorf("expected dim '│' separator glyph between wordmark and tree, got:\n%s", stripped)
	}
}

func TestRenderHeader_NarrowSingleLine(t *testing.T) {
	if got, want := HeaderHeight(60), 1; got != want {
		t.Fatalf("HeaderHeight(60) = %d, want %d", got, want)
	}
	out := RenderHeader(60, []string{"weave → finn"})
	lines := strings.Split(out, "\n")
	if got, want := len(lines), 1; got != want {
		t.Fatalf("len(lines) = %d, want %d", got, want)
	}
	if w := lipgloss.Width(lines[0]); w != 60 {
		t.Errorf("width = %d, want 60", w)
	}
}

func TestRenderHeader_ZeroWidth(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RenderHeader(0, ...) panicked: %v", r)
		}
	}()
	out := RenderHeader(0, nil)
	if out != "" {
		t.Errorf("expected empty string for width=0, got %q", out)
	}
}

func TestHeaderHeight_Boundary(t *testing.T) {
	cases := []struct {
		width, want int
	}{
		{0, 0},
		{69, 1},
		{70, 3},
		{120, 3},
	}
	for _, tc := range cases {
		if got := HeaderHeight(tc.width); got != tc.want {
			t.Errorf("HeaderHeight(%d) = %d, want %d", tc.width, got, tc.want)
		}
	}
}

func TestHeaderTreeWidth_Wide(t *testing.T) {
	w := HeaderTreeWidth(120)
	// Wordmark width is an implementation detail; just check the tree gets a
	// positive sub-total budget less than the full container width.
	if w <= 0 || w >= 120 {
		t.Errorf("HeaderTreeWidth(120) = %d, want 0 < w < 120 (room for wordmark+separator)", w)
	}
}

func TestHeaderTreeWidth_NarrowReturnsBudget(t *testing.T) {
	w := HeaderTreeWidth(50)
	if w <= 0 {
		t.Errorf("HeaderTreeWidth(50) = %d, want > 0 (breadcrumb needs space)", w)
	}
}

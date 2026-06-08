package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// newToastModelForTest constructs a ToastModel sized for predictable
// overlay tests (40 cols x 14 rows).
func newToastModelForTest(t *testing.T) ToastModel {
	t.Helper()
	theme := NewTheme("")
	m := NewToastModel(&theme)
	m.SetSize(40, 14)
	return m
}

// baseLines returns a slice of `n` identical `width`-cell base lines composed
// of '.' characters, suitable for asserting overlay placement.
func baseLines(rows, width int) string {
	line := strings.Repeat(".", width)
	out := make([]string, rows)
	for i := range out {
		out[i] = line
	}
	return strings.Join(out, "\n")
}

func TestToastModel_SpawnAddsToList(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{Text: "hello there", Style: ToastInfo, DismissOn: UserOnlyDismiss()})
	got := m.Toasts()
	if len(got) != 1 {
		t.Fatalf("Toasts() len=%d, want 1", len(got))
	}
	if got[0].Text != "hello there" {
		t.Errorf("Toasts()[0].Text = %q, want %q", got[0].Text, "hello there")
	}
}

func TestToastModel_SpawnAssignsIDWhenEmpty(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{Text: "first", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{Text: "second", DismissOn: UserOnlyDismiss()})
	got := m.Toasts()
	if len(got) != 2 {
		t.Fatalf("Toasts() len=%d, want 2", len(got))
	}
	if got[0].ID == "" {
		t.Error("first toast: ID should be auto-assigned when empty")
	}
	if got[1].ID == "" {
		t.Error("second toast: ID should be auto-assigned when empty")
	}
	if got[0].ID == got[1].ID {
		t.Errorf("auto-assigned IDs collide: %q == %q", got[0].ID, got[1].ID)
	}
}

func TestToastModel_SpawnTimerReturnsCmd(t *testing.T) {
	m := newToastModelForTest(t)
	cmd := m.Spawn(Toast{Text: "timer", DismissOn: TimerDismiss(5 * time.Second)})
	if cmd == nil {
		t.Error("Spawn with TimerDismiss should return a non-nil tea.Cmd")
	}

	m2 := newToastModelForTest(t)
	if got := m2.Spawn(Toast{Text: "user", DismissOn: UserOnlyDismiss()}); got != nil {
		t.Error("Spawn with UserOnlyDismiss should return nil cmd")
	}

	m3 := newToastModelForTest(t)
	if got := m3.Spawn(Toast{Text: "cond", DismissOn: ConditionDismiss("x")}); got != nil {
		t.Error("Spawn with ConditionDismiss should return nil cmd")
	}
}

func TestToastModel_DismissByID(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{ID: "a", Text: "alpha", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{ID: "b", Text: "beta", DismissOn: UserOnlyDismiss()})
	m.Dismiss("a")
	got := m.Toasts()
	if len(got) != 1 {
		t.Fatalf("Toasts() len=%d, want 1 after Dismiss('a')", len(got))
	}
	if got[0].ID != "b" {
		t.Errorf("survivor ID = %q, want 'b'", got[0].ID)
	}
}

func TestToastModel_DismissUnknownIDNoop(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{ID: "a", Text: "alpha", DismissOn: UserOnlyDismiss()})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Dismiss of unknown ID panicked: %v", r)
		}
	}()
	m.Dismiss("nonexistent")
	if len(m.Toasts()) != 1 {
		t.Errorf("after Dismiss of unknown ID, toast count = %d, want 1", len(m.Toasts()))
	}
}

func TestToastModel_DismissAll(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{Text: "a", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{Text: "b", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{Text: "c", DismissOn: UserOnlyDismiss()})
	m.DismissAll()
	if !m.Empty() {
		t.Errorf("after DismissAll, Empty() = false, want true (toasts=%d)", len(m.Toasts()))
	}
}

func TestToastModel_ClearCondition(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{ID: "u", Text: "user-only", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{ID: "cx", Text: "cond-x", DismissOn: ConditionDismiss("x")})
	m.Spawn(Toast{ID: "cy", Text: "cond-y", DismissOn: ConditionDismiss("y")})
	m.ClearCondition("x")
	got := m.Toasts()
	if len(got) != 2 {
		t.Fatalf("after ClearCondition('x') toast count=%d, want 2", len(got))
	}
	for _, tt := range got {
		if tt.ID == "cx" {
			t.Errorf("ClearCondition('x') did not remove cond-x toast")
		}
	}
}

func TestToastModel_ClearConditionMatchesAll(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{ID: "a", Text: "first-x", DismissOn: ConditionDismiss("x")})
	m.Spawn(Toast{ID: "b", Text: "second-x", DismissOn: ConditionDismiss("x")})
	m.ClearCondition("x")
	if !m.Empty() {
		t.Errorf("ClearCondition('x') with two matching toasts left %d behind", len(m.Toasts()))
	}
}

// TestToastModel_RenderToastIsBordered confirms the new bordered box
// treatment (QUM-701): renderToast must emit a 3-line block whose top row
// starts with the rounded-border top-left glyph (╭).
func TestToastModel_RenderToastIsBordered(t *testing.T) {
	m := newToastModelForTest(t)
	out := m.renderToast(Toast{Text: "hi", Style: ToastInfo})
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("renderToast lines=%d, want 3 (bordered box):\n%s", len(lines), out)
	}
	if !strings.Contains(ansi.Strip(lines[0]), "╭") {
		t.Errorf("top row missing rounded-border top-left glyph (╭): %q", ansi.Strip(lines[0]))
	}
	if !strings.Contains(ansi.Strip(lines[2]), "╰") {
		t.Errorf("bottom row missing rounded-border bottom-left glyph (╰): %q", ansi.Strip(lines[2]))
	}
	if !strings.Contains(ansi.Strip(lines[1]), "hi") {
		t.Errorf("middle row missing toast text: %q", ansi.Strip(lines[1]))
	}
}

// TestToastModel_OverlayCenteredBelowHeader: with SetHeaderHeight(h), the
// toast box top row appears at index h+1 and is horizontally centered.
func TestToastModel_OverlayCenteredBelowHeader(t *testing.T) {
	m := newToastModelForTest(t)
	m.SetHeaderHeight(3)
	m.Spawn(Toast{Text: "hello", Style: ToastInfo, DismissOn: UserOnlyDismiss()})
	base := baseLines(14, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")

	// Anchor row = headerHeight + 1 = 4 — top border of the box.
	if !strings.Contains(ansi.Strip(lines[4]), "╭") {
		t.Fatalf("expected top border on row 4, got %q", ansi.Strip(lines[4]))
	}
	// Middle row = 5 — content "hello".
	mid := ansi.Strip(lines[5])
	idx := strings.Index(mid, "hello")
	if idx < 0 {
		t.Fatalf("'hello' not found on row 5: %q", mid)
	}
	// Centered: "│ hello │" is 9 chars wide; on a 40-col base, left padding
	// should be (40-9)/2 = 15, so 'h' lands at col ~17.
	if idx < 12 || idx > 22 {
		t.Errorf("'hello' at col %d on row 5, want roughly centered (12..22): %q", idx, mid)
	}
	// Bottom border on row 6.
	if !strings.Contains(ansi.Strip(lines[6]), "╰") {
		t.Errorf("expected bottom border on row 6, got %q", ansi.Strip(lines[6]))
	}
}

// TestToastModel_OverlayNoHeaderAnchorAtTop: when headerHeight=0, anchor
// falls back to row 0.
func TestToastModel_OverlayNoHeaderAnchorAtTop(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{Text: "x", DismissOn: UserOnlyDismiss()})
	base := baseLines(14, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")
	if !strings.Contains(ansi.Strip(lines[0]), "╭") {
		t.Errorf("with no header, expected top border on row 0: %q", ansi.Strip(lines[0]))
	}
}

func TestToastModel_OverlayEmptyReturnsBase(t *testing.T) {
	m := newToastModelForTest(t)
	base := baseLines(14, 40)
	out := m.Overlay(base)
	if out != base {
		t.Errorf("Overlay with empty model should return base unchanged.\nbase:\n%s\ngot:\n%s", base, out)
	}
}

// TestToastModel_OverlayStacksVertically: two toasts → second block's top
// border lands at headerHeight + 1 + 3 = headerHeight + 4.
func TestToastModel_OverlayStacksVertically(t *testing.T) {
	m := newToastModelForTest(t)
	m.SetHeaderHeight(2)
	m.Spawn(Toast{Text: "AAA", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{Text: "BBB", DismissOn: UserOnlyDismiss()})
	base := baseLines(14, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")

	// First block: rows 3,4,5. Second block: rows 6,7,8.
	if !strings.Contains(ansi.Strip(lines[3]), "╭") {
		t.Errorf("first block top border expected on row 3: %q", ansi.Strip(lines[3]))
	}
	if !strings.Contains(ansi.Strip(lines[4]), "AAA") {
		t.Errorf("first block content expected on row 4: %q", ansi.Strip(lines[4]))
	}
	if !strings.Contains(ansi.Strip(lines[5]), "╰") {
		t.Errorf("first block bottom border expected on row 5: %q", ansi.Strip(lines[5]))
	}
	if !strings.Contains(ansi.Strip(lines[6]), "╭") {
		t.Errorf("second block top border expected on row 6 (headerHeight+4=6): %q", ansi.Strip(lines[6]))
	}
	if !strings.Contains(ansi.Strip(lines[7]), "BBB") {
		t.Errorf("second block content expected on row 7: %q", ansi.Strip(lines[7]))
	}
	if !strings.Contains(ansi.Strip(lines[8]), "╰") {
		t.Errorf("second block bottom border expected on row 8: %q", ansi.Strip(lines[8]))
	}
}

// TestToastModel_OverlayDismissShiftsUp: after dismissing the first toast,
// the remaining toast renders at the anchor row (upward shift).
func TestToastModel_OverlayDismissShiftsUp(t *testing.T) {
	m := newToastModelForTest(t)
	m.SetHeaderHeight(2)
	m.Spawn(Toast{ID: "a", Text: "AAA", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{ID: "b", Text: "BBB", DismissOn: UserOnlyDismiss()})
	m.Dismiss("a")

	base := baseLines(14, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")
	// 'BBB' must now appear on row 4 (anchor+1 content row).
	if !strings.Contains(ansi.Strip(lines[4]), "BBB") {
		t.Errorf("after dismissing 'a', expected 'BBB' content on row 4 (shifted up): %q", ansi.Strip(lines[4]))
	}
	// Row 7 (where 'BBB' was previously) must now be base content again.
	if ansi.Strip(lines[7]) != strings.Repeat(".", 40) {
		t.Errorf("row 7 should be restored to base after upward shift, got %q", ansi.Strip(lines[7]))
	}
}

// TestToastModel_OverlayRespectsBottomReserve: a toast that wouldn't fit
// above the bottom-2-row reserve is skipped.
func TestToastModel_OverlayRespectsBottomReserve(t *testing.T) {
	m := newToastModelForTest(t)
	// Header at row 9 leaves rows 10,11 for toasts but the bottom-2-reserve
	// (rows 12,13 in a 14-row base) means no 3-line box fits.
	m.SetHeaderHeight(9)
	m.Spawn(Toast{Text: "too-low", DismissOn: UserOnlyDismiss()})
	base := baseLines(14, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")
	for i, ln := range lines {
		if strings.Contains(ansi.Strip(ln), "too-low") {
			t.Errorf("toast rendered on row %d but should have been skipped (insufficient room): %q", i, ln)
		}
	}
	// Bottom 2 rows must remain unchanged.
	baseLine := strings.Repeat(".", 40)
	for _, row := range []int{12, 13} {
		if lines[row] != baseLine {
			t.Errorf("bottom-reserve row %d altered: %q", row, lines[row])
		}
	}
}

func TestToastModel_OverlayPreservesBaseWidth(t *testing.T) {
	m := newToastModelForTest(t)
	m.SetHeaderHeight(2)
	m.Spawn(Toast{Text: "warn", Style: ToastWarning, DismissOn: UserOnlyDismiss()})
	base := baseLines(14, 40)
	out := m.Overlay(base)
	baseLineList := strings.Split(base, "\n")
	outLines := strings.Split(out, "\n")
	if len(outLines) != len(baseLineList) {
		t.Fatalf("overlay changed line count: base=%d out=%d", len(baseLineList), len(outLines))
	}
	for i := range baseLineList {
		bw := ansi.StringWidth(baseLineList[i])
		ow := ansi.StringWidth(outLines[i])
		if bw != ow {
			t.Errorf("row %d: width %d, want %d (base=%q, out=%q)", i, ow, bw, baseLineList[i], outLines[i])
		}
	}
}

// TestToastModel_OverlayDoesNotStompBottomTwoRows: regardless of header
// height, the bottom two rows of base are never altered.
func TestToastModel_OverlayDoesNotStompBottomTwoRows(t *testing.T) {
	m := newToastModelForTest(t)
	m.SetHeaderHeight(2)
	m.Spawn(Toast{Text: "one", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{Text: "two", DismissOn: UserOnlyDismiss()})
	base := baseLines(14, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")
	baseLine := strings.Repeat(".", 40)
	for _, row := range []int{12, 13} {
		if lines[row] != baseLine {
			t.Errorf("bottom reserve row %d altered: %q", row, lines[row])
		}
	}
}

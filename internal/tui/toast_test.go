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

// TestToastModel_OverlayAnchorsAboveBottomReserve: a single toast anchors
// just above the bottom-2-row reserve and is left-aligned with a 2-col left
// margin. On a 14-row base (maxRow = 12), the box occupies rows 9,10,11.
func TestToastModel_OverlayAnchorsAboveBottomReserve(t *testing.T) {
	m := newToastModelForTest(t)
	// Header height must NOT affect vertical placement anymore.
	m.SetHeaderHeight(3)
	m.Spawn(Toast{Text: "hello", Style: ToastInfo, DismissOn: UserOnlyDismiss()})
	base := baseLines(14, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")

	// Newest toast box bottom ends at maxRow-1 = 11; box is 3 lines → rows 9-11.
	if !strings.Contains(ansi.Strip(lines[9]), "╭") {
		t.Fatalf("expected top border on row 9, got %q", ansi.Strip(lines[9]))
	}
	mid := ansi.Strip(lines[10])
	// Left-aligned with 2-col margin: 2 base cols, then the box left border and
	// one pad space precede the text: "..│ hello │".
	if !strings.HasPrefix(mid, "..│ hello") {
		t.Errorf("row 10 not left-aligned at margin 2, got %q", mid)
	}
	// Top-left border glyph must sit at col index 2 (the left margin).
	top := ansi.Strip(lines[9])
	if gi := strings.Index(top, "╭"); gi != 2 {
		t.Errorf("top-left border glyph at col %d, want 2: %q", gi, top)
	}
	if !strings.Contains(ansi.Strip(lines[11]), "╰") {
		t.Errorf("expected bottom border on row 11, got %q", ansi.Strip(lines[11]))
	}
}

// TestToastModel_OverlayHeaderHeightInert: changing the header height does not
// move the toast vertically — placement is keyed off the bottom reserve only.
func TestToastModel_OverlayHeaderHeightInert(t *testing.T) {
	render := func(h int) []string {
		m := newToastModelForTest(t)
		m.SetHeaderHeight(h)
		m.Spawn(Toast{Text: "x", DismissOn: UserOnlyDismiss()})
		return strings.Split(m.Overlay(baseLines(14, 40)), "\n")
	}
	a := render(0)
	b := render(7)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("row %d differs between headerHeight 0 and 7 (header should be inert):\n0: %q\n7: %q", i, a[i], b[i])
		}
	}
	// Sanity: the single toast still lands at rows 9-11.
	if !strings.Contains(ansi.Strip(a[9]), "╭") {
		t.Errorf("expected top border on row 9 regardless of header: %q", ansi.Strip(a[9]))
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

// TestToastModel_OverlayStacksUpwardNewestNearestInput: two toasts → the
// newest (BBB, spawned last) sits nearest the input at rows 9-11; the older
// (AAA) stacks UPWARD above it at rows 6-8.
func TestToastModel_OverlayStacksUpwardNewestNearestInput(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{Text: "AAA", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{Text: "BBB", DismissOn: UserOnlyDismiss()})
	base := baseLines(14, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")

	// Newest block (BBB) nearest input: rows 9,10,11.
	if !strings.Contains(ansi.Strip(lines[9]), "╭") {
		t.Errorf("newest block top border expected on row 9: %q", ansi.Strip(lines[9]))
	}
	if !strings.Contains(ansi.Strip(lines[10]), "BBB") {
		t.Errorf("newest block content (BBB) expected on row 10: %q", ansi.Strip(lines[10]))
	}
	if !strings.Contains(ansi.Strip(lines[11]), "╰") {
		t.Errorf("newest block bottom border expected on row 11: %q", ansi.Strip(lines[11]))
	}
	// Older block (AAA) stacks upward: rows 6,7,8.
	if !strings.Contains(ansi.Strip(lines[6]), "╭") {
		t.Errorf("older block top border expected on row 6: %q", ansi.Strip(lines[6]))
	}
	if !strings.Contains(ansi.Strip(lines[7]), "AAA") {
		t.Errorf("older block content (AAA) expected on row 7: %q", ansi.Strip(lines[7]))
	}
	if !strings.Contains(ansi.Strip(lines[8]), "╰") {
		t.Errorf("older block bottom border expected on row 8: %q", ansi.Strip(lines[8]))
	}
}

// TestToastModel_OverlayDismissShiftsDown: after dismissing the older toast,
// the surviving toast (now newest+only) shifts DOWN to the input-anchored
// position (rows 9-11) and the rows it previously occupied are restored.
func TestToastModel_OverlayDismissShiftsDown(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{ID: "a", Text: "AAA", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{ID: "b", Text: "BBB", DismissOn: UserOnlyDismiss()})
	m.Dismiss("a")

	base := baseLines(14, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")
	// 'BBB' must now appear on row 10 (content row of the input-anchored box).
	if !strings.Contains(ansi.Strip(lines[10]), "BBB") {
		t.Errorf("after dismissing 'a', expected 'BBB' content on row 10 (anchored above input): %q", ansi.Strip(lines[10]))
	}
	// The dismissed toast ('AAA') must not survive on ANY row.
	for i, ln := range lines {
		if strings.Contains(ansi.Strip(ln), "AAA") {
			t.Errorf("dismissed toast 'AAA' still present on row %d: %q", i, ansi.Strip(ln))
		}
	}
	// Every row outside the survivor's box (rows 9-11) must be base content.
	baseLine := strings.Repeat(".", 40)
	for i, ln := range lines {
		if i >= 9 && i <= 11 {
			continue
		}
		if ansi.Strip(ln) != baseLine {
			t.Errorf("row %d should be restored to base after downward shift, got %q", i, ansi.Strip(ln))
		}
	}
}

// TestToastModel_OverlayOverflowSkipsOldestWhole: when more toasts are spawned
// than fit, the oldest overflowing toasts are skipped ENTIRELY (not clipped
// mid-box). A 14-row base (maxRow=12) fits exactly 4 boxes (rows 0-2, 3-5,
// 6-8, 9-11). Spawning 5 toasts must drop the oldest (T1) whole.
func TestToastModel_OverlayOverflowSkipsOldestWhole(t *testing.T) {
	m := newToastModelForTest(t)
	for _, txt := range []string{"T1", "T2", "T3", "T4", "T5"} {
		m.Spawn(Toast{Text: txt, DismissOn: UserOnlyDismiss()})
	}
	base := baseLines(14, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")

	// Newest 4 placed bottom-up: T5 rows 9-11, T4 6-8, T3 3-5, T2 0-2.
	if !strings.Contains(ansi.Strip(lines[10]), "T5") {
		t.Errorf("T5 (newest) content expected on row 10: %q", ansi.Strip(lines[10]))
	}
	if !strings.Contains(ansi.Strip(lines[7]), "T4") {
		t.Errorf("T4 content expected on row 7: %q", ansi.Strip(lines[7]))
	}
	if !strings.Contains(ansi.Strip(lines[4]), "T3") {
		t.Errorf("T3 content expected on row 4: %q", ansi.Strip(lines[4]))
	}
	if !strings.Contains(ansi.Strip(lines[1]), "T2") {
		t.Errorf("T2 content expected on row 1: %q", ansi.Strip(lines[1]))
	}
	// T1 (oldest) must not appear ANYWHERE — skipped whole, not clipped.
	for i, ln := range lines {
		if strings.Contains(ansi.Strip(ln), "T1") {
			t.Errorf("T1 (oldest, overflow) rendered on row %d but should be skipped whole: %q", i, ln)
		}
	}
	// Width must be preserved on every row (no panic / corruption from overflow).
	for i := range lines {
		if w := ansi.StringWidth(lines[i]); w != 40 {
			t.Errorf("row %d width %d, want 40", i, w)
		}
	}
}

// TestToastModel_OverlayRespectsBottomReserve: the bottom-2-row reserve is
// never touched; the newest toast's box bottom lands at maxRow-1 (row 11).
func TestToastModel_OverlayRespectsBottomReserve(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{Text: "hi", DismissOn: UserOnlyDismiss()})
	base := baseLines(14, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")
	baseLine := strings.Repeat(".", 40)
	for _, row := range []int{12, 13} {
		if lines[row] != baseLine {
			t.Errorf("bottom-reserve row %d altered: %q", row, lines[row])
		}
	}
	if !strings.Contains(ansi.Strip(lines[11]), "╰") {
		t.Errorf("newest toast bottom border expected on row 11 (maxRow-1): %q", ansi.Strip(lines[11]))
	}
}

// TestCompositeLeft_PreservesWidthAndMargin verifies the left-aligned
// composite keeps the base line's visible width and places the overlay at the
// requested left margin.
func TestCompositeLeft_PreservesWidthAndMargin(t *testing.T) {
	line := strings.Repeat(".", 40)
	box := "│ hi │" // visible width 6
	got := compositeLeft(line, box, 2)
	if w := ansi.StringWidth(got); w != 40 {
		t.Fatalf("compositeLeft width = %d, want 40: %q", w, got)
	}
	stripped := ansi.Strip(got)
	if !strings.HasPrefix(stripped, "..") {
		t.Errorf("expected 2-col left margin of base, got %q", stripped)
	}
	if gi := strings.Index(stripped, "│"); gi != 2 {
		t.Errorf("box left edge at col %d, want 2: %q", gi, stripped)
	}
	// Right segment must be base fill out to width 40: 2 + 6 box + 32 dots.
	if !strings.HasSuffix(stripped, strings.Repeat(".", 32)) {
		t.Errorf("expected right segment filled with base dots: %q", stripped)
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

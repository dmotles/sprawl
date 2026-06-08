package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// newToastModelForTest constructs a ToastModel sized for predictable
// overlay tests (40 cols x 10 rows).
func newToastModelForTest(t *testing.T) ToastModel {
	t.Helper()
	theme := NewTheme("")
	m := NewToastModel(&theme)
	m.SetSize(40, 10)
	return m
}

// baseLines returns a slice of `n` identical 40-cell base lines composed
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

func TestToastModel_OverlayPlacesToastTopRight(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{Text: "hello", Style: ToastInfo, DismissOn: UserOnlyDismiss()})
	base := baseLines(10, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")
	if len(lines) < 10 {
		t.Fatalf("Overlay output has %d lines, want >= 10", len(lines))
	}

	// "hello" must appear within the first few rows.
	foundRow := -1
	for i := 0; i < 5 && i < len(lines); i++ {
		if strings.Contains(ansi.Strip(lines[i]), "hello") {
			foundRow = i
			break
		}
	}
	if foundRow < 0 {
		t.Fatalf("Overlay output: 'hello' not found in top 5 rows:\n%s", out)
	}

	// Toast should be on the right side: the stripped row must end with
	// content where the "hello" text lives in the rightmost portion (i.e.
	// after the midpoint of the line).
	stripped := ansi.Strip(lines[foundRow])
	idx := strings.Index(stripped, "hello")
	if idx < 20 {
		t.Errorf("'hello' at column %d in row %q, want right-anchored (>= col 20)", idx, stripped)
	}

	// Bottom 2 rows of base must be unchanged byte-equal — toast does not
	// stomp on input or status-bar reserved area.
	baseLineStr := strings.Repeat(".", 40)
	for _, row := range []int{8, 9} {
		if lines[row] != baseLineStr {
			t.Errorf("bottom row %d altered by toast overlay: got %q, want %q", row, lines[row], baseLineStr)
		}
	}
}

func TestToastModel_OverlayEmptyReturnsBase(t *testing.T) {
	m := newToastModelForTest(t)
	base := baseLines(10, 40)
	out := m.Overlay(base)
	if out != base {
		t.Errorf("Overlay with empty model should return base unchanged.\nbase:\n%s\ngot:\n%s", base, out)
	}
}

func TestToastModel_MultipleStackVertically(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{Text: "AAA-first", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{Text: "BBB-second", DismissOn: UserOnlyDismiss()})
	m.Spawn(Toast{Text: "CCC-third", DismissOn: UserOnlyDismiss()})
	base := baseLines(10, 40)
	out := m.Overlay(base)
	lines := strings.Split(out, "\n")

	rowOf := func(needle string) int {
		for i, ln := range lines {
			if strings.Contains(ansi.Strip(ln), needle) {
				return i
			}
		}
		return -1
	}
	a, b, c := rowOf("AAA-first"), rowOf("BBB-second"), rowOf("CCC-third")
	if a < 0 || b < 0 || c < 0 {
		t.Fatalf("not all toast texts present: AAA=%d BBB=%d CCC=%d\nout:\n%s", a, b, c, out)
	}
	if a == b || b == c || a == c {
		t.Errorf("toasts share a row: AAA=%d BBB=%d CCC=%d", a, b, c)
	}
}

func TestToastModel_OverlayPreservesBaseWidth(t *testing.T) {
	m := newToastModelForTest(t)
	m.Spawn(Toast{Text: "warn", Style: ToastWarning, DismissOn: UserOnlyDismiss()})
	base := baseLines(10, 40)
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

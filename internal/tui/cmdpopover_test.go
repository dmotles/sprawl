package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/dmotles/sprawl/internal/tui/commands"
)

func TestPopoverVisible(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		escDismissed bool
		want         bool
	}{
		{name: "bare slash lists all", text: "/", want: true},
		{name: "matching prefix", text: "/he", want: true},
		{name: "no match hides", text: "/zzz", want: false},
		{name: "whitespace hides (arg entry)", text: "/attach ", want: false},
		{name: "switch with arg hides", text: "/switch weav", want: false},
		{name: "unregistered path prose hides", text: "/etc/hosts is broken", want: false},
		{name: "empty hides", text: "", want: false},
		{name: "non-slash hides", text: "hello", want: false},
		{name: "esc-dismissed hides even when matching", text: "/he", escDismissed: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (cmdPopover{escDismissed: tt.escDismissed}).visible(tt.text); got != tt.want {
				t.Errorf("popoverVisible(%q, %v) = %v, want %v", tt.text, tt.escDismissed, got, tt.want)
			}
		})
	}
}

func TestPopoverMatches_AlphabeticalAndFiltered(t *testing.T) {
	// Bare slash → all commands, alphabetical.
	all := (cmdPopover{}).matches("/")
	if len(all) == 0 {
		t.Fatal("popoverMatches(/) returned no commands")
	}
	names := make([]string, len(all))
	for i, c := range all {
		names[i] = c.Name
	}
	if !strings.HasPrefix(names[0], "/attach") {
		t.Errorf("first match = %q, want /attach (alphabetical)", names[0])
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("popoverMatches not alphabetical: %v", names)
			break
		}
	}
	// Prefix filter.
	h := (cmdPopover{}).matches("/h")
	for _, c := range h {
		if !strings.HasPrefix(c.Name, "/h") {
			t.Errorf("popoverMatches(/h) returned %q not prefixed /h", c.Name)
		}
	}
	if len(h) == 0 {
		t.Error("popoverMatches(/h) should match /help and /handoff")
	}
}

func TestPopoverMove_WrapsHighlight(t *testing.T) {
	var p cmdPopover
	n := 3
	p.move(-1, n) // from 0 up → last
	if p.highlight != 2 {
		t.Errorf("move(-1) from 0 = %d, want 2 (wrap to last)", p.highlight)
	}
	p.move(1, n) // from 2 down → 0
	if p.highlight != 0 {
		t.Errorf("move(+1) from 2 = %d, want 0 (wrap to first)", p.highlight)
	}
	// n==0 must not panic and clamps to 0.
	p.highlight = 5
	p.move(1, 0)
	if p.highlight != 0 {
		t.Errorf("move with n=0 = %d, want 0", p.highlight)
	}
}

func TestPopoverSelected(t *testing.T) {
	var p cmdPopover
	matches := (cmdPopover{}).matches("/")
	p.highlight = 1
	sel, ok := p.selected("/")
	if !ok {
		t.Fatal("selected(/) not ok")
	}
	if sel.Name != matches[1].Name {
		t.Errorf("selected highlight 1 = %q, want %q", sel.Name, matches[1].Name)
	}
	// Out-of-range highlight → clamped to the first element (reset-to-top), no panic.
	p.highlight = 999
	sel, ok = p.selected("/")
	if !ok {
		t.Fatal("selected with out-of-range highlight should still resolve (clamped)")
	}
	if sel.Name != matches[0].Name {
		t.Errorf("out-of-range selected = %q, want first %q (reset-to-top)", sel.Name, matches[0].Name)
	}
	// No matches → not ok.
	if _, ok := p.selected("/zzz"); ok {
		t.Error("selected(/zzz) should be not ok (no matches)")
	}
}

func TestPopoverView_CapsRowsAndKeepsHighlightVisible(t *testing.T) {
	theme := NewTheme("colour212")
	p := cmdPopover{theme: &theme, width: 120, highlight: 6}
	all := (cmdPopover{}).matches("/")
	if len(all) < 5 {
		t.Skipf("need ≥5 commands for cap test, have %d", len(all))
	}
	// Cap to 3 command rows: box = 3 rows + 2 border rows = 5 lines total.
	box := p.View("/", 3)
	if box == "" {
		t.Fatal("View(/, 3) returned empty")
	}
	if lines := strings.Count(box, "\n") + 1; lines > 5 {
		t.Errorf("capped box has %d lines, want ≤5 (3 rows + 2 borders)", lines)
	}
	// The highlighted command (last one) must remain visible in the window.
	last := all[len(all)-1]
	p.highlight = len(all) - 1
	box = p.View("/", 3)
	if !strings.Contains(box, last.Name) {
		t.Errorf("capped box should keep the highlighted command %q visible", last.Name)
	}
	// Negative maxRows renders nothing (no room above the input).
	if p.View("/", -1) != "" {
		t.Error("View(/, -1) should render nothing (no room)")
	}
}

// ---------------------------------------------------------------------------
// QUM-930 popover geometry. The bug these pin: the box was sized by lipgloss
// auto-fit and then hard-clipped with MaxWidth AFTER the border was composed,
// so every row lost its right glyph and descriptions were cut mid-word.
// ---------------------------------------------------------------------------

// popoverRows splits a rendered popover box into ANSI-stripped lines.
func popoverRows(t *testing.T, box string) []string {
	t.Helper()
	if box == "" {
		t.Fatal("popover box is empty")
	}
	lines := strings.Split(box, "\n")
	for i, l := range lines {
		lines[i] = ansi.Strip(l)
	}
	return lines
}

// rowContent strips the border and the 1-col padding from a content row,
// yielding the text cells the popover actually budgets for.
func rowContent(t *testing.T, row string) string {
	t.Helper()
	inner := strings.TrimSuffix(strings.TrimPrefix(row, "│"), "│")
	return strings.TrimPrefix(inner, " ")
}

// registryDesc returns a registered command's description, so geometry tests
// track the registry (which QUM-930 forbids editing) instead of duplicating
// its strings as literals.
func registryDesc(t *testing.T, name string) string {
	t.Helper()
	for _, c := range commands.AllSorted() {
		if c.Name == name {
			return c.Description
		}
	}
	t.Fatalf("command %q not registered", name)
	return ""
}

// assertPopoverBox is the geometry gate. It asserts, for a rendered box:
// every row is exactly rowW columns wide, carries both border glyphs, and the
// box has exactly wantRows rows (a wrapped row — the failure mode of using
// lipgloss Width() without truncating content first — shows up as extra rows).
// Structural failures are fatal: a width claim on a clipped box is meaningless.
func assertPopoverBox(t *testing.T, box string, rowW, wantRows int) {
	t.Helper()
	rows := popoverRows(t, box)
	if len(rows) != wantRows {
		t.Fatalf("box has %d rows, want %d (content rows + 2 borders; extra rows = wrapping):\n%s",
			len(rows), wantRows, strings.Join(rows, "\n"))
	}
	badWidth, badBorder := -1, -1
	for i, r := range rows {
		if ansi.StringWidth(r) != rowW && badWidth < 0 {
			badWidth = i
		}
		lead, trail := "│", "│"
		switch i {
		case 0:
			lead, trail = "╭", "╮"
		case len(rows) - 1:
			lead, trail = "╰", "╯"
		}
		if (!strings.HasPrefix(r, lead) || !strings.HasSuffix(r, trail)) && badBorder < 0 {
			badBorder = i
		}
	}
	if badWidth >= 0 {
		t.Errorf("row %d display width = %d, want %d (every row must be exactly the box width)",
			badWidth, ansi.StringWidth(rows[badWidth]), rowW)
	}
	if badBorder >= 0 {
		t.Errorf("row %d is missing a border glyph (border clipped away): %q", badBorder, rows[badBorder])
	}
	if t.Failed() {
		t.Logf("box:\n%s", strings.Join(rows, "\n"))
		t.FailNow()
	}
}

// descColumn returns the DISPLAY column at which a content row's description
// begins: past the 2-col cursor gutter, past the name token, past the name
// padding + gap. Rune- and width-safe (the cursor glyph `›` is 3 bytes, 1 cell),
// which byte offsets are not. ok=false when the row carries no description.
func descColumn(content string) (int, bool) {
	rs := []rune(content)
	col, i := 0, 0
	for i < len(rs) && col < 2 { // cursor gutter ("› " or "  ")
		col += ansi.StringWidth(string(rs[i]))
		i++
	}
	for i < len(rs) && rs[i] != ' ' { // name token
		col += ansi.StringWidth(string(rs[i]))
		i++
	}
	for i < len(rs) && rs[i] == ' ' { // name padding + gap
		col++
		i++
	}
	if i >= len(rs) {
		return 0, false
	}
	return col, true
}

// assertColumnsAligned pins the multi-suggestion alignment AC: the description
// column must start at the same display column on every content row. A fix that
// truncated `name+desc` as one blob would keep rows equal-width and fully
// bordered while destroying the column, so equal width alone is not enough.
func assertColumnsAligned(t *testing.T, box string) {
	t.Helper()
	rows := popoverRows(t, box)
	want, haveWant := -1, false
	for _, r := range rows[1 : len(rows)-1] {
		col, ok := descColumn(rowContent(t, r))
		if !ok {
			continue // description dropped entirely (floor widths)
		}
		if !haveWant {
			want, haveWant = col, true
			continue
		}
		if col != want {
			t.Errorf("description column starts at %d on row %q, want %d (rows must align)\nbox:\n%s",
				col, r, want, strings.Join(rows, "\n"))
		}
	}
}

// TestAssertColumnsAligned_CatchesMisalignment is the negative control for the
// alignment helper: it must flag a hand-built misaligned box, and pass an
// aligned one. Without this the alignment assertion is a claim, not a check —
// the geometry tests FailNow before reaching it while the box is still clipped.
func TestAssertColumnsAligned_CatchesMisalignment(t *testing.T) {
	aligned := "╭──────────────────────────╮\n" +
		"│ › /handoff  Consolidate  │\n" +
		"│   /help     Show keys    │\n" +
		"╰──────────────────────────╯"
	misaligned := "╭──────────────────────────╮\n" +
		"│ › /handoff  Consolidate  │\n" +
		"│   /help   Show keys      │\n" +
		"╰──────────────────────────╯"
	okCase := new(testing.T)
	assertColumnsAligned(okCase, aligned)
	if okCase.Failed() {
		t.Error("assertColumnsAligned flagged an aligned box")
	}
	fake := new(testing.T)
	assertColumnsAligned(fake, misaligned)
	if !fake.Failed() {
		t.Error("assertColumnsAligned did NOT flag a misaligned box (helper is vacuous)")
	}
}

// observedBoxWidth is the rendered width of a box.
func observedBoxWidth(t *testing.T, box string) int {
	t.Helper()
	return ansi.StringWidth(popoverRows(t, box)[0])
}

func TestPopoverView_BorderIntegrityAtEveryWidth(t *testing.T) {
	theme := NewTheme("colour212")
	// 24 is the narrowest width at which the box still fits the composite
	// gutter (see popoverFloorWidth's comment); MinTermWidth is 40, so
	// everything below that is only reachable from a direct unit call.
	for _, width := range []int{24, 40, 60, 80, 120, 200, 486} {
		for _, text := range []string{"/", "/h", "/usage"} {
			t.Run(fmt.Sprintf("w%d_%s", width, strings.TrimPrefix(text, "/")), func(t *testing.T) {
				p := cmdPopover{theme: &theme, width: width}
				box := p.View(text, 0)
				assertPopoverBox(t, box, observedBoxWidth(t, box), len(p.matches(text))+2)
				assertColumnsAligned(t, box)
			})
		}
	}
}

func TestPopoverView_GrowsToFitDescriptionOnWideTerminal(t *testing.T) {
	theme := NewTheme("colour212")
	p := cmdPopover{theme: &theme, width: 200}
	box := p.View("/", 0)
	assertPopoverBox(t, box, observedBoxWidth(t, box), len(p.matches("/"))+2)
	if w := observedBoxWidth(t, box); w <= 52 {
		t.Errorf("box width = %d on a 200-col terminal, want >52 (must grow past the old fixed cap)", w)
	}
	plain := strings.Join(popoverRows(t, box), "\n")
	// The widest registered description must render in full, un-elided.
	if want := registryDesc(t, "/usage"); !strings.Contains(plain, want) {
		t.Errorf("wide terminal should show the full /usage description %q; box:\n%s", want, plain)
	}
	if strings.Contains(plain, "…") {
		t.Errorf("nothing should be elided at width 200; box:\n%s", plain)
	}
}

// TestPopoverView_UsesSizingSeam makes popoverWidthFor load-bearing: without
// this, View could keep its own uncapped inline sizing while the cap AC is
// asserted only on a function the render path never calls.
func TestPopoverView_UsesSizingSeam(t *testing.T) {
	theme := NewTheme("colour212")
	for _, width := range []int{24, 60, 200, 486} {
		p := cmdPopover{theme: &theme, width: width}
		if got, want := observedBoxWidth(t, p.View("/", 0)), popoverWidthFor(p.matches("/"), width); got != want {
			t.Errorf("width=%d: rendered box width = %d, popoverWidthFor = %d — View must size via the seam", width, got, want)
		}
	}
}

func TestPopoverView_SizesToWidestEntry(t *testing.T) {
	theme := NewTheme("colour212")
	p := cmdPopover{theme: &theme, width: 200}
	all := observedBoxWidth(t, p.View("/", 0))
	// `/h` matches only /handoff + /help, both narrower than /usage, so its box
	// must be strictly narrower — i.e. sized to content, not always at the cap.
	h := observedBoxWidth(t, p.View("/h", 0))
	if h >= all {
		t.Errorf("box width for /h = %d, for / = %d; want /h strictly narrower (sized to the widest entry)", h, all)
	}
	// And it must still be wide enough for its own widest description in full.
	if want := registryDesc(t, "/handoff"); !strings.Contains(strings.Join(popoverRows(t, p.View("/h", 0)), "\n"), want) {
		t.Errorf("/h box should fit the full /handoff description %q", want)
	}
}

func TestPopoverView_BoundedByReadableMax(t *testing.T) {
	theme := NewTheme("colour212")
	// A readable upper bound: a 486-col popover is as wrong as a 52-col stub.
	// 110 is an AC-derived ceiling, deliberately NOT popoverMaxWidth — asserting
	// against the implementation's own constant would be tautological.
	const readableCeiling = 110
	// Checked FIRST: it is a pure seam call, and the render assertions below
	// FailNow while the box is clipped, which would leave this unexecuted.
	// The real registry's widest natural row sits UNDER the ceiling, so a render
	// assertion alone would also pass an implementation with no cap at all.
	synthetic := []commands.Command{{Name: "/x", Description: strings.Repeat("long ", 60)}}
	if got := popoverWidthFor(synthetic, 486); got > readableCeiling {
		t.Errorf("popoverWidthFor(300-char desc, 486 cols) = %d, want ≤%d — the readable bound must cap it", got, readableCeiling)
	} else if got < 60 {
		t.Errorf("popoverWidthFor(300-char desc, 486 cols) = %d, want ≥60 — the bound must still be readable", got)
	}
	widths := map[int]int{}
	for _, term := range []int{200, 300, 486} {
		p := cmdPopover{theme: &theme, width: term}
		box := p.View("/", 0)
		assertPopoverBox(t, box, observedBoxWidth(t, box), len(p.matches("/"))+2)
		w := observedBoxWidth(t, box)
		if w > readableCeiling {
			t.Errorf("box width = %d on a %d-col terminal, want ≤%d (readable bound)", w, term, readableCeiling)
		}
		widths[term] = w
	}
	// A cap is width-independent; a proportional (e.g. width/2) bound is not.
	if widths[200] != widths[300] || widths[300] != widths[486] {
		t.Errorf("box width varies with terminal width above the bound: %v, want identical", widths)
	}
}

func TestPopoverView_RespectsCompositeBudget(t *testing.T) {
	theme := NewTheme("colour212")
	for _, width := range []int{24, 40, 41, 60, 120} {
		t.Run(fmt.Sprintf("w%d", width), func(t *testing.T) {
			p := cmdPopover{theme: &theme, width: width}
			box := p.View("/", 0)
			assertPopoverBox(t, box, observedBoxWidth(t, box), len(p.matches("/"))+2)
			if w := observedBoxWidth(t, box); w > width-4 {
				t.Errorf("box width = %d, want ≤ width-4 (%d) to leave the composite gutter", w, width-4)
			}
			// Where the content demonstrably overflows the available width, the
			// box must fill it EXACTLY — otherwise a "floor everywhere" or
			// proportional-shrink implementation passes the ≤ bound above.
			if width >= 24 && popoverWidthFor(p.matches("/"), 1<<20) > width-4 {
				if w := observedBoxWidth(t, box); w != width-4 {
					t.Errorf("box width = %d at terminal width %d, want exactly %d (fill the available width when content overflows)", w, width, width-4)
				}
			}
			// Exercise the real composite path: the popover must survive
			// compositeLeft's indent-collapse and truncate guards (toast.go:248-253).
			base := strings.Repeat(" ", width)
			for i, row := range strings.Split(box, "\n") {
				got := ansi.Strip(compositeLeft(base, row, 2))
				if !strings.HasPrefix(got, "  ") {
					t.Errorf("row %d: composite dropped the 2-col indent: %q", i, got)
				}
				if plain := ansi.Strip(row); !strings.Contains(got, plain) {
					t.Errorf("row %d: composite clipped the row\n got: %q\nwant to contain: %q", i, got, plain)
				}
			}
		})
	}
}

func TestPopoverView_NarrowElidesWithMarker(t *testing.T) {
	theme := NewTheme("colour212")
	const width = 44
	p := cmdPopover{theme: &theme, width: width}
	matches := p.matches("/")
	// Precondition: at least one description must actually overflow this width,
	// otherwise "something was elided" would be vacuous.
	overflow := false
	for _, c := range matches {
		if len(c.Name)+len(c.Description)+6 > width-4 {
			overflow = true
		}
	}
	if !overflow {
		t.Fatalf("precondition failed: no registered description overflows width %d, so elision cannot be observed", width)
	}
	box := p.View("/", 0)
	assertPopoverBox(t, box, observedBoxWidth(t, box), len(matches)+2)
	assertColumnsAligned(t, box)
	rows := popoverRows(t, box)
	elided := 0
	for _, r := range rows[1 : len(rows)-1] {
		if strings.HasSuffix(strings.TrimRight(rowContent(t, r), " "), "…") {
			elided++
		}
	}
	if elided == 0 {
		t.Errorf("at width %d an overflowing description must be elided with a visible … marker; box:\n%s",
			width, strings.Join(rows, "\n"))
	}
}

func TestPopoverView_FloorAndZeroWidth(t *testing.T) {
	theme := NewTheme("colour212")
	// width==0 (before the first WindowSizeMsg) renders at the documented
	// fallback: intact, and narrow enough for any plausible terminal.
	zero := cmdPopover{theme: &theme, width: 0}
	zbox := zero.View("/h", 0)
	assertPopoverBox(t, zbox, 52, len(zero.matches("/h"))+2)
	// An absurdly narrow terminal: the floor holds, nothing panics, the box is
	// still a closed frame, and the highlight cursor survives truncation.
	for _, w := range []int{1, 10, 20, 23} {
		tiny := cmdPopover{theme: &theme, width: w}
		for _, text := range []string{"/", "/h"} {
			box := tiny.View(text, 0)
			assertPopoverBox(t, box, 20, len(tiny.matches(text))+2)
			cursors := 0
			for _, r := range popoverRows(t, box) {
				if strings.Contains(r, "›") {
					cursors++
				}
			}
			if cursors != 1 {
				t.Errorf("width=%d text=%q: %d rows carry the › cursor, want exactly 1; box:\n%s",
					w, text, cursors, box)
			}
		}
	}
}

func TestTruncateRow_WideRuneAndBudgetSafe(t *testing.T) {
	theme := NewTheme("colour212")
	styledPrefix := theme.AccentText.Render("› ")
	styledName := theme.AccentText.Render("/handoff")
	cjk := strings.Repeat("日本語テキストです", 4)
	for _, contentW := range []int{1, 2, 5, 8, 16, 40} {
		for _, tc := range []struct {
			name           string
			prefix, cmdStr string
		}{
			{"plain", "› ", "/handoff"},
			{"styled", styledPrefix, styledName},
		} {
			got := truncateRow(tc.prefix, tc.cmdStr, cjk, contentW)
			if w := ansi.StringWidth(got); w != contentW {
				t.Errorf("truncateRow(%s, contentW=%d) width = %d, want exactly %d: %q",
					tc.name, contentW, w, contentW, got)
			}
			// A width-based cut must never slice an escape sequence in half.
			// (This counts ESC vs SGR terminators; it does NOT prove the row
			// closes every style it opens — lipgloss Style.Render always
			// terminates, so at the real call site that cannot happen.)
			if strings.Count(got, "\x1b") != strings.Count(got, "m")-strings.Count(ansi.Strip(got), "m") {
				t.Errorf("truncateRow(%s, contentW=%d) has unbalanced escapes: %q", tc.name, contentW, got)
			}
		}
	}
	// A description that fits is preserved verbatim.
	if full := truncateRow("  ", "/x", "hello", 2+2+2+5); !strings.Contains(full, "hello") {
		t.Errorf("fitting description must be preserved verbatim, got %q", full)
	}
	// An over-long ASCII description elides with a marker, not a bare cut.
	cut := truncateRow("  ", "/x", "abcdefghijklmnop", 12)
	if !strings.HasSuffix(strings.TrimRight(cut, " "), "…") {
		t.Errorf("elided row should end with …, got %q", cut)
	}
}

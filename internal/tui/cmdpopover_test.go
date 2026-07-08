package tui

import (
	"strings"
	"testing"
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

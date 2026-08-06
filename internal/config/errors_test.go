package config

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// tableEnd is the last line Reference() emits. Ordering assertions anchor on it
// rather than on a token like "line 2": the table's `purpose` column is free
// text from struct tags, so any token could one day collide with table content
// and silently resolve INSIDE the table, leaving the assertion green forever.
const tableEnd = "all keys are flat scalars"

// The terminal floor the message has to survive. `surviveRows` is the whole
// screen minus the shell prompt: the message alone is 36 physical rows (before
// cobra's usage block), so it overflows and the usage block has ALREADY
// scrolled off — nothing but the prompt can be subtracted.
//
// Do not over-read this budget: the tail is 6 rows against 23 for the 2-problem
// fixture here, so it will not notice a moderate regression, and the surviving
// window is unbounded in len(Problems) — an 8-problem message measures 17 rows.
// It pins "the detail is at the bottom", not "the message always fits".
const (
	termCols    = 80
	termRows    = 24
	surviveRows = termRows - 1
)

// physicalLines is how many terminal rows s occupies at cols columns — the unit
// the defect is actually measured in. Reference() is 15 logical lines but wraps
// to 26 physical ones at 80.
//
// Counts runes, not display cells: correct while every character in the table
// is single-width, which is true today because the `purpose` cells are ASCII
// struct tags. A wide char there would undercount.
func physicalLines(s string, cols int) int {
	total := 0
	for _, l := range strings.Split(s, "\n") {
		n := utf8.RuneCountInString(l)
		rows := (n + cols - 1) / cols
		if rows < 1 {
			rows = 1 // an empty line still occupies a row
		}
		total += rows
	}
	return total
}

// assertSurvivesTerminalFloor checks that everything from the first actionable
// token to the end of the message fits the rows a user still has on screen.
// This is the one assertion that encodes the physical-line claim rather than a
// proxy for it, so it is measured as DISTANCE FROM THE END of the message — the
// surviving window is the message's tail, not a region relative to the table.
func assertSurvivesTerminalFloor(t *testing.T, msg string, firstActionable int) {
	t.Helper()
	if got := physicalLines(msg[firstActionable:], termCols); got > surviveRows {
		t.Errorf("the actionable block starts %d physical rows from the end at %dx%d, "+
			"but only the last %d rows survive — it scrolls off; got:\n%s",
			got, termCols, termRows, surviveRows, msg)
	}
}

// TestUnknownKeysError_ActionableDetailComesLast pins the ORDER of the rendered
// message, which is load-bearing and was not previously asserted anywhere.
//
// Reference() wraps to 26 physical lines at 80 columns, and cobra prints its
// usage block above the error, so on the most common terminal floor (80x24)
// whatever renders FIRST scrolls off the top. QA verified with tmux
// capture-pane at a real 80x24 that the offending keys — the only part the user
// can act on — were the part that disappeared, while the reference table
// survived. So the actionable block must come LAST, nearest the prompt.
//
// The first-line assertion at the end is a regression PIN rather than a
// red-phase assertion (it passes today). Mutation-checked: rendering the table
// first with no lead-in makes line 1 "recognized keys:" and it fails.
func TestUnknownKeysError_ActionableDetailComesLast(t *testing.T) {
	body := "validate: make test\n" + // line 1 - known
		"vlaidate: oops\n" + // line 2 - typo
		"hub_url: http://x\n" + // line 3 - known
		"totally_unknown: 3\n" // line 4 - unknown

	// Normalized: the raw message embeds a random tempdir, so the "edit ..."
	// line's width — and therefore any physical-line count — would otherwise be
	// nondeterministic and far wider than production's.
	root, err := loadErrIn(t, body)
	msg := renderNormalized(err, root)

	idx := func(s string) int {
		i := strings.Index(msg, s)
		if i < 0 {
			t.Fatalf("error text must contain %q; got:\n%s", s, msg)
		}
		return i
	}

	// Every actionable token must come after the table's LAST line. Anchoring on
	// the table terminator (not the "recognized keys:" header) together with the
	// exactly-once count rejects a duplicate-block "fix" that renders the detail
	// both before and after.
	end := strings.LastIndex(msg, tableEnd)
	if end < 0 {
		t.Fatalf("error text must carry the reference table; got:\n%s", msg)
	}
	for _, tok := range []string{"line 2: vlaidate", "line 4: totally_unknown", `did you mean "validate"?`} {
		if idx(tok) < end {
			t.Errorf("%q must be rendered AFTER the reference table so it survives at 80x24; got:\n%s", tok, msg)
		}
		if n := strings.Count(msg, tok); n != 1 {
			t.Errorf("%q must appear exactly once, got %d — duplicating the detail block is not the fix; got:\n%s", tok, n, msg)
		}
	}
	// ...but the detail still precedes the closing next-step line.
	if idx("line 4: totally_unknown") > idx("or run: sprawl config --help") {
		t.Errorf("the offending keys must precede the next-step line; got:\n%s", msg)
	}

	// The surviving window must be self-contained: it has to SAY what is wrong
	// and name the file, because the headline that does both is what scrolls
	// off. Mutation-checked: deleting the "unrecognized keys:" lead-in fails the
	// first, and dropping the path from the "edit ..." line fails the second.
	tail := msg[end:]
	if !strings.Contains(tail, "unrecognized") {
		t.Errorf("the block after the table must say what is wrong, not just list keys; got tail:\n%s", tail)
	}
	if !strings.Contains(tail, "<ROOT>/.sprawl/config.yaml") {
		t.Errorf("the block after the table must name the config file; got tail:\n%s", tail)
	}

	assertSurvivesTerminalFloor(t, msg, idx("line 2: vlaidate"))

	// cmd/root.go sets SilenceErrors and prints the bare error, so the first line
	// of this string is the first line the user sees of the failure. It must name
	// the failure, never be the bare table header.
	if first := strings.Split(msg, "\n")[0]; !strings.Contains(first, "unrecognized") {
		t.Errorf("the first line must name the failure, got %q", first)
	}
}

// TestUnknownKeysError_FromSet_DetailComesLast_AndNeverNamesTheFile is the same
// ordering contract for the rejected-Set branch, plus the constraint that this
// branch must never name the config file (the argument is wrong, not the file —
// see TestSet_UnknownKey_BlamesTheArgumentNotTheFile). The two are asserted
// together because both branches now render ONE shared detail block, and the
// obvious way to write that block is via pathOrDefault(), which would break the
// second.
//
// This test also requires the bottom block to RESTATE the offending key even
// though the headline names it: once the headline is the thing that scrolls
// off, not repeating it leaves the surviving block unable to say which key was
// wrong.
//
// The blame check and the first-line check are regression PINS (they pass
// today). Mutation-checked: setting FromSet's header to use pathOrDefault()
// fails the blame check.
func TestUnknownKeysError_FromSet_DetailComesLast_AndNeverNamesTheFile(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	serr := cfg.Set("validat", "x")
	if serr == nil {
		t.Fatal("Set on an unrecognized key must fail")
	}
	// No normalization needed: the FromSet branch never renders a path, which is
	// itself asserted below.
	msg := serr.Error()

	if strings.Contains(msg, ".sprawl/config.yaml") {
		t.Errorf("a rejected Set must not blame the config file; got:\n%s", msg)
	}

	idx := func(s string) int {
		i := strings.Index(msg, s)
		if i < 0 {
			t.Fatalf("error text must contain %q; got:\n%s", s, msg)
		}
		return i
	}

	end := strings.LastIndex(msg, tableEnd)
	if end < 0 {
		t.Fatalf("error text must carry the reference table; got:\n%s", msg)
	}
	const hint = `did you mean "validate"?`
	if idx(hint) < end {
		t.Errorf("%q must be rendered AFTER the reference table; got:\n%s", hint, msg)
	}
	if n := strings.Count(msg, hint); n != 1 {
		t.Errorf("the did-you-mean must appear exactly once, got %d — duplicating the detail block is not the fix; got:\n%s", n, msg)
	}
	if idx(hint) > idx("re-run with a recognized key") {
		t.Errorf("the did-you-mean must precede the next-step line; got:\n%s", msg)
	}
	// The key must be restated below the table, not only in the scrolled-off
	// header. Matched as a whole rendered line, since bare "validat" is a prefix
	// of the "validate" in the did-you-mean.
	if !strings.Contains(msg[end:], "\n  validat\n") {
		t.Errorf("the block after the table must restate the offending key on its own line; got tail:\n%s", msg[end:])
	}
	// ...and it must say what is wrong, not just name a key.
	if !strings.Contains(msg[end:], "unrecognized") {
		t.Errorf("the block after the table must say what is wrong; got tail:\n%s", msg[end:])
	}

	assertSurvivesTerminalFloor(t, msg, idx(hint))

	if first := strings.Split(msg, "\n")[0]; !strings.Contains(first, "unrecognized config key") {
		t.Errorf("the first line must name the failure, got %q", first)
	}
}

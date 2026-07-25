package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/observe/perfstat"
)

// WHAT THIS FILE DOES NOT ASSERT — read this before adding a case.
//
// The /perf overlay is a VISUAL surface. It lands "ready for eyeball", and a
// human at a real terminal is the authority on whether it reads well. These
// tests cover only what a machine can be right about:
//
//   - that the DATA is correct and present,
//   - that nothing is displayed as a measurement when it was not measured,
//   - that the box survives extreme widths and uses the space it is given.
//
// Deliberately NOT asserted, because green here would be mistaken for design
// validation (QUM-930 shipped a popover rendering as a 52-column stub on a
// 486-column terminal with its border eaten — it presumably passed everything
// that was assertable about it):
//
//   - No golden files and no full-view snapshots.
//   - No assertion about centering, padding amounts, colour, or accent.
//   - No assertion about blank-line placement or vertical rhythm.
//   - No assertion about the exact box width at a given terminal width — only
//     a floor and a ceiling.
//   - No assertion about column order, header spelling, or label wording
//     beyond the specific substrings that carry meaning.
//   - Nothing that amounts to "this looks good".
//
// If you find yourself asserting a layout judgment, you are writing the test
// that lets the next stub ship green.

func perfTestModal(t *testing.T, snap perfstat.Snapshot) PerfModalModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewPerfModalModel(&theme).SetSize(120, 40).Install(snap).Show()
}

// healthySnapshot is a fully-measured snapshot: every field carries a real
// reading, so nothing should render as absent.
func healthySnapshot() perfstat.Snapshot {
	return perfstat.Snapshot{
		At: time.Date(2026, 7, 25, 6, 0, 0, 0, time.UTC),
		Frame: perfstat.FrameStats{
			Samples: 240, Count: 5000,
			P50: 490 * time.Microsecond,
			P95: 51300 * time.Microsecond,
			Max: 120 * time.Millisecond,
		},
		Cache: perfstat.CacheStats{
			Hits: 900, Misses: 100, Items: 214, Uncacheable: 7,
			Revision: 1188, Width: 132,
		},
		Detector:  perfstat.DetectorStatus{Enabled: true},
		Invariant: perfstat.InvariantStatus{Enabled: true, Checked: true, Orphans: 0},
	}
}

func TestPerfModal_VisibilityLifecycle(t *testing.T) {
	theme := NewTheme("colour212")
	m := NewPerfModalModel(&theme).SetSize(120, 40)
	if m.Visible() {
		t.Error("Visible() = true on a fresh modal, want false")
	}
	m = m.Show()
	if !m.Visible() {
		t.Error("Visible() = false after Show(), want true")
	}
	if strings.TrimSpace(m.View()) == "" {
		t.Error("View() is empty while visible, want rendered content")
	}
	m = m.Hide()
	if m.Visible() {
		t.Error("Visible() = true after Hide(), want false")
	}
	if got := strings.TrimSpace(m.View()); got != "" {
		t.Errorf("hidden View() = %q, want empty", got)
	}
}

func TestPerfModal_DismissKeys(t *testing.T) {
	for _, tc := range []struct {
		name        string
		key         tea.KeyPressMsg
		wantDismiss bool
	}{
		{"escape dismisses", tea.KeyPressMsg{Code: tea.KeyEscape}, true},
		{"q dismisses", tea.KeyPressMsg{Code: 'q'}, true},
		{"other keys do not", tea.KeyPressMsg{Code: 'x'}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := perfTestModal(t, healthySnapshot())
			_, cmd := m.Update(tc.key)
			if !tc.wantDismiss {
				if cmd != nil {
					t.Errorf("Update(%v) returned a cmd %T, want nil", tc.key, cmd())
				}
				return
			}
			if cmd == nil {
				t.Fatalf("Update(%v) returned no cmd, want one yielding DismissPerfMsg", tc.key)
			}
			if _, ok := cmd().(DismissPerfMsg); !ok {
				t.Errorf("Update(%v) cmd yielded %T, want DismissPerfMsg", tc.key, cmd())
			}
		})
	}
}

// TestPerfModal_RendersTheMeasurements is the data-correctness gate: every
// number the operator came here to read must actually appear.
func TestPerfModal_RendersTheMeasurements(t *testing.T) {
	m := perfTestModal(t, healthySnapshot())
	body := stripANSI(m.View())

	for _, want := range []string{
		"490µs",  // p50
		"51.3ms", // p95 — the pathology side of the boundary
		"120ms",  // max
		"90.0%",  // hit rate: 900 of 1000 lookups
		"214",    // items
		"7",      // uncacheable
		"1188",   // revision
	} {
		if !strings.Contains(body, want) {
			t.Errorf("View() is missing the measurement %q\n---\n%s", want, body)
		}
	}
}

// TestPerfModal_TimingColumnIsAlignedAcrossTheUnitFlip pins the reason
// FormatDurationCol exists. This is a data-shape assertion, not a layout
// judgment: p50 in µs and p95 in ms must occupy the same display width, or the
// two-orders-of-magnitude jump reads as jitter.
func TestPerfModal_TimingColumnIsAlignedAcrossTheUnitFlip(t *testing.T) {
	m := perfTestModal(t, healthySnapshot())
	body := stripANSI(m.View())

	p50Cell := perfCellFor(t, body, "490µs")
	p95Cell := perfCellFor(t, body, "51.3ms")
	if lipgloss.Width(p50Cell) != lipgloss.Width(p95Cell) {
		t.Errorf("timing cells disagree in width: p50 %q (%d cols) vs p95 %q (%d cols)",
			p50Cell, lipgloss.Width(p50Cell), p95Cell, lipgloss.Width(p95Cell))
	}
	// Guard the guard: if the fixture stops spanning the unit boundary this
	// test proves nothing, so fail rather than pass quietly.
	if strings.HasSuffix(strings.TrimSpace(p50Cell), "ms") {
		t.Fatal("fixture no longer spans the µs→ms boundary; this test cannot catch misalignment")
	}
}

// perfCellFor extracts the padded cell containing want from the rendered body.
func perfCellFor(t *testing.T, body, want string) string {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		idx := strings.Index(line, want)
		if idx < 0 {
			continue
		}
		// Walk left over the padding that FormatDurationCol added.
		start := idx
		for start > 0 && line[start-1] == ' ' {
			start--
		}
		return line[start : idx+len(want)]
	}
	t.Fatalf("no line in the rendered body contains %q\n---\n%s", want, body)
	return ""
}

// --- Honesty assertions. ---
//
// These are the highest-value cases in this file. A diagnostic that renders a
// plausible zero for something it did not measure is worse than one that
// renders nothing: the reader reasons from the zero. This is the same defect
// HaveFrameContext removed from the orphan report, guarded here at the UI.

func TestPerfModal_DoesNotFabricateMeasurements(t *testing.T) {
	for _, tc := range []struct {
		name       string
		snap       func(perfstat.Snapshot) perfstat.Snapshot
		wantAbsent []string // substrings that must NOT appear
		wantSaid   string   // the modal must say this instead
	}{
		{
			name: "invariant check disabled renders disabled, not zero orphans",
			snap: func(s perfstat.Snapshot) perfstat.Snapshot {
				s.Invariant = perfstat.InvariantStatus{Enabled: false}
				s.Detector.Enabled = false
				return s
			},
			wantAbsent: []string{"orphans: 0", "orphans 0"},
			wantSaid:   "disabled",
		},
		{
			name: "invariant enabled but never observed renders not-measured",
			snap: func(s perfstat.Snapshot) perfstat.Snapshot {
				s.Invariant = perfstat.InvariantStatus{Enabled: true, Checked: false}
				return s
			},
			wantAbsent: []string{"orphans: 0", "orphans 0"},
			wantSaid:   "not measured",
		},
		{
			name: "no frames observed renders not-measured, not 0µs",
			snap: func(s perfstat.Snapshot) perfstat.Snapshot {
				s.Frame = perfstat.FrameStats{}
				return s
			},
			wantAbsent: []string{"0ns", "0µs"},
			wantSaid:   "not measured",
		},
		{
			name: "no cache lookups renders not-measured, not a 0.0% hit rate",
			snap: func(s perfstat.Snapshot) perfstat.Snapshot {
				s.Cache.Hits, s.Cache.Misses = 0, 0
				return s
			},
			wantAbsent: []string{"0.0%"},
			wantSaid:   "not measured",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := perfTestModal(t, tc.snap(healthySnapshot()))
			body := strings.ToLower(stripANSI(m.View()))
			for _, absent := range tc.wantAbsent {
				if strings.Contains(body, strings.ToLower(absent)) {
					t.Errorf("View() renders %q for something it did not measure\n---\n%s", absent, body)
				}
			}
			if !strings.Contains(body, strings.ToLower(tc.wantSaid)) {
				t.Errorf("View() does not say %q\n---\n%s", tc.wantSaid, body)
			}
		})
	}
}

// TestPerfModal_StreamingOrphansAreNotAnAlarm: mid-turn the invariant does not
// hold, and a nonzero orphan count is expected rather than pathological. The
// mirror of the false-zero problem — alarming on a healthy streaming turn is
// just as dishonest as reassuring on an unmeasured one.
func TestPerfModal_StreamingOrphansAreNotAnAlarm(t *testing.T) {
	s := healthySnapshot()
	s.Invariant = perfstat.InvariantStatus{Enabled: true, Checked: true, Orphans: 3, Streaming: true}
	m := perfTestModal(t, s)
	body := strings.ToLower(stripANSI(m.View()))

	if !strings.Contains(body, "mid-turn") {
		t.Errorf("View() does not mark a streaming observation as mid-turn\n---\n%s", body)
	}
	if !strings.Contains(body, "3") {
		t.Errorf("View() drops the orphan count entirely; it should show it, framed\n---\n%s", body)
	}
}

// TestPerfModal_QuietFrameHalfIsNotHealth pins the boundary tower surfaced: a
// stranded item drives the render into a bypass that consults no cache, so the
// frame half sees a quiet frame and reports nothing. Silence there is not
// evidence of health, and the surface must say so rather than let a reader
// infer it.
func TestPerfModal_QuietFrameHalfIsNotHealth(t *testing.T) {
	m := perfTestModal(t, healthySnapshot())
	body := strings.ToLower(stripANSI(m.View()))
	if !strings.Contains(body, "not evidence") {
		t.Errorf("View() does not state that a quiet frame half is not evidence of health\n---\n%s", body)
	}
}

// TestPerfModal_NoDiagnosisLineWithoutAReport: an absent report must render as
// absent, not as an empty or zero-valued diagnosis.
func TestPerfModal_NoDiagnosisLineWithoutAReport(t *testing.T) {
	m := perfTestModal(t, healthySnapshot()) // Detector.HasReport is false
	body := stripANSI(m.View())
	if strings.Contains(body, "episode") {
		t.Errorf("View() renders diagnosis prose with no report latched\n---\n%s", body)
	}

	s := healthySnapshot()
	s.Detector.HasReport = true
	s.Detector.Tripped = true
	s.Detector.LastReport = perfstat.Report{
		Kind: perfstat.DefectIdleMisses, Episode: 4, IdleFrames: 30,
		IdleMisses: 29, Items: 214, Uncacheable: 7, Revision: 1188, Width: 132,
	}
	withReport := stripANSI(perfTestModal(t, s).View())
	if !strings.Contains(withReport, "episode 4") {
		t.Errorf("View() omits the latched report's diagnosis\n---\n%s", withReport)
	}
}

// --- Width behaviour: the QUM-930 guard. ---

func TestPerfModal_SurvivesExtremeWidths(t *testing.T) {
	for _, w := range []int{20, 40, 80, 120, 300, 486} {
		for _, h := range []int{10, 40} {
			t.Run(perfSizeName(w, h), func(t *testing.T) {
				theme := NewTheme("colour212")
				m := NewPerfModalModel(&theme).SetSize(w, h).Install(healthySnapshot()).Show()
				out := stripANSI(m.View())
				lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

				// The border must survive. QUM-930's stub had its border eaten.
				if !strings.Contains(out, "╭") || !strings.Contains(out, "╰") || !strings.Contains(out, "│") {
					t.Errorf("box border is incomplete at %dx%d\n---\n%s", w, h, out)
				}
				// Nothing may overflow the terminal.
				for i, ln := range lines {
					if got := lipgloss.Width(ln); got > w {
						t.Errorf("line %d is %d cols at width %d (overflows)\n%q", i, got, w, ln)
					}
				}
			})
		}
	}
}

// TestPerfModal_UsesTheSpaceOnAWideTerminal is the assertion that would have
// caught QUM-930. "Fits" is exactly what a 52-column stub on a 486-column
// terminal does; the box must be at least as wide as its own content.
func TestPerfModal_UsesTheSpaceOnAWideTerminal(t *testing.T) {
	theme := NewTheme("colour212")
	narrow := NewPerfModalModel(&theme).SetSize(80, 40).Install(healthySnapshot()).Show()
	wide := NewPerfModalModel(&theme).SetSize(486, 40).Install(healthySnapshot()).Show()

	narrowBox := perfBoxWidth(t, stripANSI(narrow.View()))
	wideBox := perfBoxWidth(t, stripANSI(wide.View()))

	// The floor is the content's own natural width, not a magic number: the box
	// must never be narrower than the widest row it has to hold.
	contentFloor := perfWidestContentLine(t, stripANSI(wide.View()))
	if wideBox < contentFloor {
		t.Errorf("box is %d cols at width 486 but its widest content row is %d — content is being squeezed",
			wideBox, contentFloor)
	}
	if wideBox < narrowBox {
		t.Errorf("box shrank on a wider terminal: %d cols at 486 vs %d cols at 80", wideBox, narrowBox)
	}
	if wideBox > 486 {
		t.Errorf("box is %d cols on a 486-col terminal (overflows)", wideBox)
	}
}

// perfBoxWidth measures the rendered box by its top border row, which
// lipgloss.Place pads with spaces on both sides.
func perfBoxWidth(t *testing.T, body string) int {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		if strings.Contains(line, "╭") {
			return lipgloss.Width(strings.TrimSpace(line))
		}
	}
	t.Fatalf("no top border row found\n---\n%s", body)
	return 0
}

// perfWidestContentLine returns the width of the widest row between the box
// borders, i.e. what the box actually has to hold.
func perfWidestContentLine(t *testing.T, body string) int {
	t.Helper()
	widest := 0
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "│") {
			continue
		}
		if got := lipgloss.Width(trimmed); got > widest {
			widest = got
		}
	}
	if widest == 0 {
		t.Fatalf("no content rows found between borders\n---\n%s", body)
	}
	return widest
}

func perfSizeName(w, h int) string {
	return strings.Join([]string{itoa(w), itoa(h)}, "x")
}

// TestPerfModal_DoesNotWrapWhenTheTerminalHasRoom guards a defect the rest of
// this file could not see. Every honesty and data assertion above stayed green
// while the frame row wrapped mid-sentence on a 120-column terminal, because
// "the number is present" remains true when the line holding it is folded in
// half. The cause was arithmetic: lipgloss's Width() is the TOTAL block width,
// border and padding included, and treating it as the content budget silently
// costs six columns.
//
// This is a content-integrity assertion, not a layout judgment: given a
// terminal wider than the content needs, no row may be split. How the rows are
// spaced or aligned remains the eyeball's call.
func TestPerfModal_DoesNotWrapWhenTheTerminalHasRoom(t *testing.T) {
	theme := NewTheme("colour212")
	m := NewPerfModalModel(&theme).SetSize(200, 40).Install(healthySnapshot()).Show()

	wantRows := len(strings.Split(m.renderBody(), "\n"))
	var gotRows int
	for line := range strings.SplitSeq(stripANSI(m.View()), "\n") {
		if strings.Contains(line, "│") {
			gotRows++
		}
	}
	// Padding(1, 2) contributes one blank row above and below the body.
	if want := wantRows + 2; gotRows != want {
		t.Errorf("box holds %d rows at width 200 but the body is %d rows (+2 padding = %d): "+
			"content is being wrapped despite having room", gotRows, wantRows, want)
	}
}

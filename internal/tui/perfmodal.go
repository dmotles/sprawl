// Package tui — PerfModalModel renders the /perf slash-command modal
// (QUM-934). It displays render-health measurements collected by
// internal/observe/perfstat: frame-time percentiles, chat render-cache
// effectiveness, and the cacheability-invariant check.
//
// The modal is owned by AppModel, which routes key events to it while showPerf
// is true and listens for DismissPerfMsg to drive the close path. It renders a
// perfstat.Snapshot VALUE — it never reads the collector — so it is testable
// without a running session.
//
// DESIGN RULE, and the reason half this file is conditionals: this surface must
// never render a number it did not measure. An unmeasured reading shows as
// "not measured", a disabled check as "disabled". A plausible zero is worse
// than a blank, because a reader reasons from it — the same defect
// perfstat.Report.HaveFrameContext removed from the orphan report, guarded here
// on the way in rather than on the way out.
package tui

import (
	"fmt"
	"strings"
	"text/tabwriter"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/observe/perfstat"
)

// ShowPerfMsg opens the /perf overlay; DismissPerfMsg closes it.
//
// These live here rather than in messages.go, departing from the /usage
// precedent, for two reasons: the modal is the only producer and consumer of
// the dismiss message, so the coupling is real; and it keeps this slice out of
// a shared file that another engineer is editing. If a later change gives them
// a second producer, messages.go is the right home and moving them is free.
type (
	ShowPerfMsg    struct{}
	DismissPerfMsg struct{}
)

// The modal's horizontal chrome. lipgloss's Width() covers both, so the content
// budget is Width - perfPaddingCols - perfBorderCols.
const (
	perfBorderCols  = 2 // one rounded-border column each side
	perfPaddingCols = 4 // Padding(1, 2): two columns each side
)

// perfTimingCol is the display width of the timing column. Every duration cell
// is right-aligned into it so a µs→ms flip across the pathology boundary reads
// as a magnitude change rather than as jitter — see perfstat.FormatDurationCol,
// which pads in runes because "µ" and "—" are multi-byte.
const perfTimingCol = 8

// perfNotMeasured is what an absent reading renders as. It is deliberately not
// a zero, an empty cell, or a dash that could be mistaken for one: the reader
// must be able to tell "measured, and it was nothing" from "never measured".
const perfNotMeasured = "not measured"

// perfQuietCaveat is the boundary statement this surface is obliged to carry.
//
// The frame half's only trip is DefectIdleMisses, and a stranded item does not
// produce one: the strand leaves the chat list's item-derived idle flag
// stale-true, so its render takes a bypass that consults no cache, which the
// detector sees as a quiet frame. Keeping that reading is deliberate (counting
// a bypass as a miss would report every streaming frame as a defeat), but it
// means silence in the frame half is not evidence the render path is healthy.
//
// NOT HYPOTHETICAL — QUM-990 is a filed instance. A ToolCallItem in flight when
// the session ends can never receive its MarkToolResult, because the process
// that would send it is gone; nothing sweeps those rows, so pendingTools never
// drains, Idle() is pinned false, and Render takes the bypass on every frame for
// the life of the restart window (~120s on the /handoff path, unbounded if the
// restart fails). Throughout that window this frame half reads quiet while a
// core spins. The issue number is in the rendered text on purpose: a reader who
// can go look at a filed case believes the caveat in a way they will not believe
// an abstract caution.
//
// This sentence lives HERE rather than in perfstat.Report.Diagnosis() because
// Diagnosis only renders when something has tripped, and the state being warned
// about is the one that emits nothing at all. A warning attached to reports
// cannot warn about the absence of reports.
const perfQuietCaveat = "a quiet frame half is not evidence of health: a stranded item bypasses\n" +
	"the cache and reads as quiet here (see QUM-990). orphans is the authority."

// PerfModalModel renders a centered modal showing render-health measurements.
type PerfModalModel struct {
	theme         *Theme
	width, height int
	visible       bool
	snap          perfstat.Snapshot
	installed     bool
}

// NewPerfModalModel constructs a hidden, empty modal.
func NewPerfModalModel(theme *Theme) PerfModalModel {
	return PerfModalModel{theme: theme}
}

// SetSize updates the centering dimensions.
func (m PerfModalModel) SetSize(w, h int) PerfModalModel {
	m.width = w
	m.height = h
	return m
}

// Install replaces the modal's snapshot. Called on each open, and again
// whenever a refresh lands while the modal is visible.
func (m PerfModalModel) Install(snap perfstat.Snapshot) PerfModalModel {
	m.snap = snap
	m.installed = true
	return m
}

// Show makes the modal visible.
func (m PerfModalModel) Show() PerfModalModel {
	m.visible = true
	return m
}

// Hide hides the modal.
func (m PerfModalModel) Hide() PerfModalModel {
	m.visible = false
	return m
}

// Visible reports whether the modal is currently visible.
func (m PerfModalModel) Visible() bool { return m.visible }

// Update handles key events while the modal is visible. It emits a
// DismissPerfMsg on Esc or 'q' and ignores everything else — this is a
// read-only diagnostic surface with no sub-views to switch between.
func (m PerfModalModel) Update(msg tea.Msg) (PerfModalModel, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.Code {
	case tea.KeyEscape, 'q':
		return m, func() tea.Msg { return DismissPerfMsg{} }
	}
	return m, nil
}

// View renders the modal centered in the available area. Returns empty when the
// modal is not visible.
func (m PerfModalModel) View() string {
	if !m.visible {
		return ""
	}

	accent := "212"
	if m.theme != nil && m.theme.AccentColor != "" {
		accent = m.theme.AccentColor
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(accent)).
		Padding(1, 2)

	body := m.renderBody()
	// Size the box to the terminal, then to the content — in that order.
	//
	// Capping at the terminal keeps the right border on-screen; without it the
	// box renders at its natural width regardless and overflows a narrow pane,
	// which is the QUM-930 shape. Capping at the content's natural width stops a
	// wide terminal from stretching a short table across 486 columns.
	//
	// NOTE lipgloss's Width() is the TOTAL width of the rendered block — border
	// and padding included — so the content budget is Width minus both. Measured
	// rather than assumed: a 40-column body needs Width(46) to render unwrapped
	// with Padding(1, 2) and a rounded border. Getting this wrong wraps rows that
	// had room, and no assertion in perfmodal_test.go catches it, because "the
	// data is all present" stays true when a row wraps. It was obvious the moment
	// the render was looked at, which is the argument for the eyeball pass.
	if m.width > 0 {
		boxW := lipgloss.Width(body) + perfPaddingCols + perfBorderCols
		if boxW > m.width {
			boxW = m.width
		}
		if boxW < 1 {
			boxW = 1
		}
		style = style.Width(boxW)
	}
	box := style.Render(body)

	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}

func (m PerfModalModel) renderBody() string {
	if !m.installed {
		return "render health has not been sampled yet.\n\n[esc/q] close"
	}

	var b strings.Builder
	b.WriteString("RENDER HEALTH\n\n")
	b.WriteString(m.renderFrameSection())
	b.WriteString("\n")
	b.WriteString(m.renderCacheSection())
	b.WriteString("\n")
	b.WriteString(m.renderInvariantSection())
	if diag := m.renderDiagnosis(); diag != "" {
		b.WriteString("\n")
		b.WriteString(diag)
	}
	b.WriteString("\n")
	b.WriteString(perfQuietCaveat)
	b.WriteString("\n\n[esc/q] close")
	return b.String()
}

// renderFrameSection renders the chat-region frame-time percentiles.
//
// Count == 0 means no frame has ever been timed, which is distinct from a
// session whose frames were all instant. Rendering "0ns" for the former would
// state a measurement that was never taken.
func (m PerfModalModel) renderFrameSection() string {
	f := m.snap.Frame
	if f.Count == 0 {
		return "frame time   " + perfNotMeasured + " (no frames observed)\n"
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "frame time\tp50 %s\tp95 %s\tmax %s\t(%d samples of %d frames)\n",
		perfstat.FormatDurationCol(f.P50, perfTimingCol),
		perfstat.FormatDurationCol(f.P95, perfTimingCol),
		perfstat.FormatDurationCol(f.Max, perfTimingCol),
		f.Samples, f.Count)
	_ = tw.Flush()
	return b.String()
}

// renderCacheSection renders whole-chat render-cache effectiveness.
//
// A hit rate over zero lookups is the specific fabricated zero this slice was
// warned about: until the ChatList cache counters are wired, Hits and Misses
// are both zero, and "0.0%" would read as a totally defeated cache rather than
// as an unwired one.
func (m PerfModalModel) renderCacheSection() string {
	c := m.snap.Cache
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)

	if c.Lookups() == 0 {
		fmt.Fprintf(tw, "cache\thit rate %s\t(no lookups recorded)\n", perfNotMeasured)
	} else {
		fmt.Fprintf(tw, "cache\thit rate %s\t(%d hits, %d rebuilds)\n",
			perfstat.FormatPercent(c.HitRate()), c.Hits, c.Misses)
	}
	// Items/Uncacheable/Revision/Width are plain state, always meaningful.
	fmt.Fprintf(tw, "entries\t%d total\t%d uncacheable\trevision %d at width %d\n",
		c.Items, c.Uncacheable, c.Revision, c.Width)
	_ = tw.Flush()
	return b.String()
}

// renderInvariantSection renders the cacheability-invariant check, which is the
// authority for the stranded-item class.
//
// Three states are distinguished on purpose, and conflating any two of them
// produces a reassuring zero: disabled (nothing is watching), enabled but never
// observed (watching, no reading yet), and observed. A mid-turn observation is
// labelled rather than alarmed on — the invariant legitimately does not hold
// while a turn is in flight, so a nonzero count there is expected.
func (m PerfModalModel) renderInvariantSection() string {
	inv := m.snap.Invariant
	switch {
	case !inv.Enabled:
		return "invariant    disabled (set SPRAWL_PERF_INVARIANT=1 to enable the orphan check)\n"
	case !inv.Checked:
		return "invariant    " + perfNotMeasured + " (enabled, no observation recorded yet)\n"
	case inv.Streaming:
		return fmt.Sprintf("invariant    orphans %d — observed mid-turn, when the invariant "+
			"legitimately does not hold\n", inv.Orphans)
	case inv.Orphans > 0:
		return fmt.Sprintf("invariant    orphans %d — VIOLATED: items stranded unfinished away "+
			"from the tail\n", inv.Orphans)
	default:
		return "invariant    orphans 0 — holding\n"
	}
}

// renderDiagnosis renders the detector's latched report, or nothing at all.
//
// "Nothing at all" is load-bearing: an empty diagnosis line would imply a
// report exists and says nothing.
func (m PerfModalModel) renderDiagnosis() string {
	d := m.snap.Detector
	if !d.HasReport {
		return ""
	}
	status := "recovered"
	if d.Tripped || d.OrphanTripped {
		status = "TRIPPED"
	}
	return fmt.Sprintf("detector     %s, %d episode(s)\n%s\n",
		status, d.Episodes, d.LastReport.Diagnosis())
}

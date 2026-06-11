package tui

// QUM-788: TUI pill / tree-modal styling for the new lifecycle status
// `complete` (dormant-revivable). These tests pin the projection from disk
// `Status == "complete"` → a distinct dim pill that does NOT collapse into
// `StateIdle` (gray) or `StateFailure` (red).

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// TestDeriveIconState_DormantWhenStatusComplete: a TreeNode whose disk Status
// is "complete" must project to the new "dormant" icon state regardless of
// other signals (no LastReportState/ProcessAlive/InTurn set).
func TestDeriveIconState_DormantWhenStatusComplete(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	n := TreeNode{Status: "complete"}
	if got := DeriveIconState(n, now); got != "dormant" {
		t.Errorf("DeriveIconState(Status=complete) = %q, want %q", got, "dormant")
	}
}

// TestDeriveIconState_DormantBeatsProcessAliveAndReport: dormant projection
// wins even when ProcessAlive=false and LastReportState is set, because the
// disk Status is the durable terminal-but-revivable signal.
func TestDeriveIconState_DormantBeatsProcessAliveAndReport(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	alive := false
	n := TreeNode{
		Status:          "complete",
		ProcessAlive:    &alive,
		LastReportState: "complete",
	}
	if got := DeriveIconState(n, now); got != "dormant" {
		t.Errorf("DeriveIconState(Status=complete, ProcessAlive=false, report=complete) = %q, want %q",
			got, "dormant")
	}
}

// TestDeriveIconState_PausedAndDiedBeatDormant: paused/died Liveness are
// explicit operator/observer signals and must still win over a stale disk
// Status=complete. (Defensive guard — in practice the supervisor should not
// emit both, but the precedence is load-bearing per the oracle plan.)
func TestDeriveIconState_PausedAndDiedBeatDormant(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		liveness string
		want     string
	}{
		{"paused", "paused"},
		{"died", "died"},
	} {
		t.Run(tc.liveness, func(t *testing.T) {
			n := TreeNode{Status: "complete", Liveness: tc.liveness}
			if got := DeriveIconState(n, now); got != tc.want {
				t.Errorf("DeriveIconState(Status=complete, Liveness=%s) = %q, want %q",
					tc.liveness, got, tc.want)
			}
		})
	}
}

// TestTreeNodeAgentState_DormantForStatusComplete: orbital pill projection
// maps Status=complete → StateDormant (distinct from StateIdle/StateFailure/
// StateDone).
func TestTreeNodeAgentState_DormantForStatusComplete(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	n := TreeNode{Status: "complete"}
	if got := TreeNodeAgentState(n, now); got != StateDormant {
		t.Errorf("TreeNodeAgentState(Status=complete) = %v, want StateDormant", got)
	}
}

// TestTreeNodeAgentState_FaultStillBeatsDormant: FaultClass non-empty is the
// highest-priority signal and must still project to StateFailure even when
// Status==complete.
func TestTreeNodeAgentState_FaultStillBeatsDormant(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	n := TreeNode{Status: "complete", FaultClass: "HangTimeout"}
	if got := TreeNodeAgentState(n, now); got != StateFailure {
		t.Errorf("TreeNodeAgentState(Status=complete, FaultClass=...) = %v, want StateFailure", got)
	}
}

// TestStateGlyph_DormantIsDistinct: glyph for StateDormant must not collide
// with idle/done/failure/blocked glyphs.
func TestStateGlyph_DormantIsDistinct(t *testing.T) {
	g := stateGlyph(StateDormant)
	if g == "" {
		t.Fatalf("stateGlyph(StateDormant) returned empty string")
	}
	for _, s := range []AgentState{StateIdle, StateDone, StateFailure, StateBlocked, StateWorking, StateRoot} {
		if stateGlyph(s) == g {
			t.Errorf("stateGlyph(StateDormant)=%q collides with stateGlyph(%v)=%q", g, s, stateGlyph(s))
		}
	}
}

// TestStateStyle_DormantDistinctFromIdleAndFailure: rendered foreground for
// StateDormant must differ from StateIdle (gray) and StateFailure (red), so
// operators can tell the pill apart at a glance.
func TestStateStyle_DormantDistinctFromIdleAndFailure(t *testing.T) {
	dormant := stateStyle(StateDormant).Render("x")
	idle := stateStyle(StateIdle).Render("x")
	failure := stateStyle(StateFailure).Render("x")
	if dormant == idle {
		t.Errorf("stateStyle(StateDormant).Render(x) = %q collides with StateIdle render %q", dormant, idle)
	}
	if dormant == failure {
		t.Errorf("stateStyle(StateDormant).Render(x) = %q collides with StateFailure render %q", dormant, failure)
	}
}

// TestTheme_ReportDot_Dormant: theme.ReportDot("dormant") must render a
// distinctive glyph (◌) using a style distinct from the idle and failure
// dots. Mirrors TestTheme_ReportDot_PausedAndDied.
func TestTheme_ReportDot_Dormant(t *testing.T) {
	theme := NewTheme("")
	dormant := theme.ReportDot("dormant")
	if !strings.Contains(dormant, "◌") {
		t.Errorf("ReportDot(dormant) = %q, want it to contain ◌", dormant)
	}
	idle := theme.ReportDot("")
	failure := theme.ReportDot("failure")
	if dormant == idle {
		t.Errorf("ReportDot(dormant) = %q collides with ReportDot(idle) %q", dormant, idle)
	}
	if dormant == failure {
		t.Errorf("ReportDot(dormant) = %q collides with ReportDot(failure) %q", dormant, failure)
	}
}

// TestReportDotDormant_UsesInfoFaintRole: pin the dormant dot foreground to
// the Info palette role with Faint(true). Mirrors TestReportDots_UsePaletteRoles
// pattern.
func TestReportDotDormant_UsesInfoFaintRole(t *testing.T) {
	theme := NewTheme("39")
	control := lipgloss.NewStyle().Foreground(theme.Palette.Info).Faint(true)
	got := theme.ReportDotDormant.Render("◌")
	want := control.Render("◌")
	if got != want {
		t.Errorf("ReportDotDormant render mismatch:\n got:  %q\n want: %q (Info + Faint)", got, want)
	}
}

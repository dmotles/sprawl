package tui

import (
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/state"
)

// QUM-1186 (D2): an agent whose runtime was reclaimed for inactivity rests at
// state.StatusIdle, and the TUI must say so DISTINCTLY.
//
// Idle-and-reclaimed is a genuine third thing, and both neighbouring buckets
// would tell the operator something false:
//   - "dormant" (◌) means Status==complete, i.e. the agent FINISHED. A reaped
//     agent may be mid-task and merely quiet.
//   - "idle" (grey ●) is the generic catch-all that already absorbs
//     ProcessAlive==false and the no-signal default, so routing StatusIdle
//     there would make it indistinguishable from "we know nothing".
//
// Hence a dedicated "reclaimed" icon state. The operator needs to read, at a
// glance: the process is gone, the agent is NOT done, and it comes back on the
// next message.

// TestDeriveIconState_StatusIdleIsReclaimed is the load-bearing case. The node
// deliberately has ProcessAlive == nil (the projection has no opinion) AND
// InTurn == true (a stale pre-reclaim signal). Without an explicit StatusIdle
// branch this node falls through to the InTurn heuristic and renders
// "working" — the single most misleading answer available, since the process
// no longer exists.
//
// A version of this test with ProcessAlive=&false would pass even with no
// implementation at all (it would hit the ProcessAlive shortcut and return
// "idle"), which is why the nil case is the one asserted.
func TestDeriveIconState_StatusIdleIsReclaimed(t *testing.T) {
	n := TreeNode{
		Name:         "reaped",
		Status:       state.StatusIdle,
		ProcessAlive: nil,
		InTurn:       true,
	}
	if got := DeriveIconState(n, time.Now()); got != "reclaimed" {
		t.Errorf("DeriveIconState(StatusIdle, ProcessAlive=nil, InTurn=true) = %q, want %q", got, "reclaimed")
	}
}

// TestDeriveIconState_StatusIdleBeatsRecentActivity pins the other stale
// signal. LastActivityAt inside RecentActivityWindow also routes to "working";
// reclamation happens precisely after a quiet period, but the window and the
// reaper threshold are independently configurable, so the two can overlap.
func TestDeriveIconState_StatusIdleBeatsRecentActivity(t *testing.T) {
	now := time.Now()
	n := TreeNode{
		Name:           "reaped",
		Status:         state.StatusIdle,
		LastActivityAt: now.Add(-time.Second),
	}
	if got := DeriveIconState(n, now); got != "reclaimed" {
		t.Errorf("DeriveIconState(StatusIdle, recent activity) = %q, want %q", got, "reclaimed")
	}
}

// TestDeriveIconState_PausedAndDiedStillBeatIdle is a NEGATIVE control,
// direction: must stay quiet. Paused and Died are higher-priority OPERATOR
// signals; a reclaimed agent that was subsequently paused must still read as
// paused, or the pause becomes invisible.
func TestDeriveIconState_PausedAndDiedStillBeatIdle(t *testing.T) {
	for _, tc := range []struct{ liveness, want string }{
		{"paused", "paused"},
		{"died", "died"},
	} {
		n := TreeNode{Name: "reaped", Status: state.StatusIdle, Liveness: tc.liveness}
		if got := DeriveIconState(n, time.Now()); got != tc.want {
			t.Errorf("DeriveIconState(StatusIdle, Liveness=%q) = %q, want %q", tc.liveness, got, tc.want)
		}
	}
}

// TestDeriveIconState_CompleteStillDormant is a NEGATIVE control, direction:
// must stay quiet. Adding the StatusIdle branch must not disturb the QUM-788
// dormant projection for Status==complete.
func TestDeriveIconState_CompleteStillDormant(t *testing.T) {
	n := TreeNode{Name: "donezo", Status: state.StatusComplete, InTurn: true}
	if got := DeriveIconState(n, time.Now()); got != "dormant" {
		t.Errorf("DeriveIconState(StatusComplete) = %q, want %q", got, "dormant")
	}
}

// TestReportDot_ReclaimedIsVisuallyDistinct enforces tower's requirement at
// the level the operator actually experiences: the rendered glyph. Comparing
// against every neighbouring state catches the failure mode where "reclaimed"
// is added to the switch but mapped to an existing glyph/style pair, which
// would satisfy DeriveIconState and still be indistinguishable on screen.
func TestReportDot_ReclaimedIsVisuallyDistinct(t *testing.T) {
	theme := NewTheme("colour212")
	reclaimed := theme.ReportDot("reclaimed")
	if reclaimed == "" {
		t.Fatalf("ReportDot(\"reclaimed\") returned empty")
	}
	for _, other := range []string{"idle", "dormant", "complete", "working", "blocked", "failure", "paused", "died", ""} {
		if got := theme.ReportDot(other); got == reclaimed {
			t.Errorf("ReportDot(%q) == ReportDot(\"reclaimed\") (%q); reclaimed must be visually distinct", other, reclaimed)
		}
	}
}

// TestTreeNodeAgentState_ReclaimedIsDistinct carries the same requirement into
// the orbital view, which projects icon states onto its own AgentState enum.
// Without an arm there, "reclaimed" falls through to the StateIdle default and
// the distinction is lost in that view only — a partial fix that is easy to
// ship and hard to notice.
func TestTreeNodeAgentState_ReclaimedIsDistinct(t *testing.T) {
	n := TreeNode{Name: "reaped", Status: state.StatusIdle}
	got := TreeNodeAgentState(n, time.Now())
	if got == StateIdle {
		t.Errorf("TreeNodeAgentState(StatusIdle) = StateIdle; must be distinct from the generic idle bucket")
	}
	if got == StateDormant {
		t.Errorf("TreeNodeAgentState(StatusIdle) = StateDormant; reclaimed is not finished")
	}
	if got == StateWorking {
		t.Errorf("TreeNodeAgentState(StatusIdle) = StateWorking; the process is gone")
	}
}

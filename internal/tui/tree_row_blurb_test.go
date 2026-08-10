package tui

import (
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// QUM-1186 — DELIBERATE CHOICE for the tree row's trailing text.
//
// The row used to interpolate node.LastReportMessage: the agent's own most
// recent one-line summary of what it was doing. That field is deleted with
// report_status, and leaving the row to fall through to "(status)" would be a
// silent downgrade — the operator would lose the only per-row answer to "what
// is this agent actually doing?" and get a word they can already read from the
// dot colour.
//
// The replacement is the agent's BLURB (supervisor.AgentInfo.Blurb, QUM-899).
// Reasons it is the right substitute rather than merely an available one:
//   - It answers the same question the row was answering.
//   - It is already the headline of the `status` MCP tool, so the TUI and the
//     tool agree instead of diverging.
//   - It is DERIVED (generated from observed activity and git diff) rather
//     than ASSERTED by the agent about itself, which is the direction this
//     whole slice is moving liveness in.
//
// Fallback is unchanged: no blurb -> "(status)".

func treeModelWithAgents(t *testing.T, agents []supervisor.AgentInfo) TreeModel {
	t.Helper()
	m := newTestTreeModel(t)
	m.SetNodes(buildTreeNodes(agents, nil))
	m.SetSize(200, 20)
	return m
}

func TestTreeRow_ShowsBlurbInsteadOfReportMessage(t *testing.T) {
	m := treeModelWithAgents(t, []supervisor.AgentInfo{{
		Name:   "finn",
		Type:   "engineer",
		Status: "active",
		Blurb:  "wiring the idle reaper",
	}})

	out := m.View()
	if !strings.Contains(out, "wiring the idle reaper") {
		t.Errorf("tree row does not show the agent's blurb\n--- view ---\n%s", out)
	}
	// The blurb must REPLACE the status parenthetical, not sit alongside it —
	// otherwise the row grows and the substitution was not actually made.
	if strings.Contains(out, "(active)") {
		t.Errorf("tree row shows both the blurb and the status parenthetical; the blurb should replace it\n--- view ---\n%s", out)
	}
}

// TestTreeRow_FallsBackToStatusWhenNoBlurb pins the other half of the choice.
// A freshly-spawned agent has no blurb yet; the row must still say something,
// and a bare name with nothing after it is the "left blank by accident"
// outcome this test exists to rule out.
func TestTreeRow_FallsBackToStatusWhenNoBlurb(t *testing.T) {
	m := treeModelWithAgents(t, []supervisor.AgentInfo{{
		Name:   "finn",
		Type:   "engineer",
		Status: "active",
	}})

	out := m.View()
	if !strings.Contains(out, "(active)") {
		t.Errorf("tree row with no blurb does not fall back to the status parenthetical\n--- view ---\n%s", out)
	}
}

// TestTreeRow_UnknownStatusFallback preserves the QUM-623 behaviour that an
// empty status renders as "(unknown)" rather than a bare "()".
func TestTreeRow_UnknownStatusFallback(t *testing.T) {
	m := treeModelWithAgents(t, []supervisor.AgentInfo{{
		Name: "finn",
		Type: "engineer",
	}})

	out := m.View()
	if !strings.Contains(out, "(unknown)") {
		t.Errorf("tree row with empty status and no blurb should render (unknown)\n--- view ---\n%s", out)
	}
}

// TestTreeRow_BlurbIsClipped keeps clipTreeRow load-bearing. A report message
// was one line by construction; a blurb is 2-3 sentences and may contain
// newlines, so the clip that used to be defensive is now the thing standing
// between a long blurb and a broken panel border.
func TestTreeRow_BlurbIsClipped(t *testing.T) {
	m := treeModelWithAgents(t, []supervisor.AgentInfo{{
		Name:   "finn",
		Type:   "engineer",
		Status: "active",
		Blurb:  "line one of the blurb\nline two should never reach the panel\nline three either",
	}})
	m.SetSize(60, 20)

	out := m.View()
	if strings.Contains(out, "line two should never reach the panel") {
		t.Errorf("multi-line blurb bled past the row clip\n--- view ---\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 400 {
			t.Errorf("rendered row is %d chars, far past the panel width", len(line))
		}
	}
}

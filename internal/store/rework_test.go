package store

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestReworkPrompt_CarriesTheReportingDirective (QUM-1346).
//
// The rework path is the second producer of spawn_requested, and it is the
// SAME delivery mode as a goal: the agent is spawned onto a contract that
// report_result closes, and closing it already notifies the owner. It
// therefore carries the same duplicate-report hazard, and until this it still
// ended with a bare "Report to <owner>." — the exact phrasing QUM-1346
// replaced on the goal path.
func TestReworkPrompt_CarriesTheReportingDirective(t *testing.T) {
	got := reworkPrompt(GoalResearch, goalOpenedPayload{Text: "find out why", Owner: "weave"},
		reworkRequestedPayload{Reason: "shallow", Owner: "weave"},
		DispatchedEvent{ID: uuid.New(), WorkflowInstanceID: uuid.New()}, uuid.New())

	for _, want := range []string{"send_message", "ENTIRE", "duplicate"} {
		if !strings.Contains(got, want) {
			t.Errorf("the rework brief does not carry %q, so a reworking agent will close the contract AND send its owner a duplicate summary:\n%s", want, got)
		}
	}
}

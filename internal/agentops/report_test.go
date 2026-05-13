package agentops

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
)

func setupReportTest(t *testing.T, agent *state.AgentState) (string, *ReportDeps) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(state.AgentsDir(root), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := state.SaveAgent(root, agent); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	deps := &ReportDeps{
		Now: func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	}
	return root, deps
}

func TestReport_WorkingUpdatesState(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{
		Name: "alice", Parent: "bob", Status: "active",
	})

	res, err := Report(deps, root, "alice", "working", "halfway done")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if res.ReportedAt != "2026-04-21T10:00:00Z" {
		t.Errorf("ReportedAt = %q", res.ReportedAt)
	}
	// QUM-559: MessageID is always empty in the state-only contract.
	if res.MessageID != "" {
		t.Errorf("MessageID = %q, want empty (QUM-559)", res.MessageID)
	}

	st, _ := state.LoadAgent(root, "alice")
	if st.LastReportState != "working" {
		t.Errorf("LastReportState = %q", st.LastReportState)
	}
	if st.LastReportType != "status" {
		t.Errorf("LastReportType = %q (want back-compat 'status')", st.LastReportType)
	}
	if st.LastReportMessage != "halfway done" {
		t.Errorf("LastReportMessage = %q", st.LastReportMessage)
	}
	if st.Status != "active" {
		t.Errorf("Status should not change for working, got %q", st.Status)
	}
}

func TestReport_CompleteSetsStatusDone(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{
		Name: "alice", Parent: "bob", Status: "active",
	})

	_, err := Report(deps, root, "alice", "complete", "done")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	st, _ := state.LoadAgent(root, "alice")
	if st.Status != "done" {
		t.Errorf("Status = %q, want done", st.Status)
	}
	if st.LastReportType != "done" {
		t.Errorf("LastReportType = %q, want done (back-compat)", st.LastReportType)
	}
}

func TestReport_FailureSetsStatusProblem(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{
		Name: "alice", Parent: "bob", Status: "active",
	})

	_, err := Report(deps, root, "alice", "failure", "blocked on API")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	st, _ := state.LoadAgent(root, "alice")
	if st.Status != "problem" {
		t.Errorf("Status = %q, want problem", st.Status)
	}
	if st.LastReportType != "problem" {
		t.Errorf("LastReportType = %q, want problem", st.LastReportType)
	}
}

func TestReport_BlockedDoesNotChangeStatus(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{
		Name: "alice", Parent: "bob", Status: "active",
	})

	_, err := Report(deps, root, "alice", "blocked", "need review")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	st, _ := state.LoadAgent(root, "alice")
	if st.Status != "active" {
		t.Errorf("Status = %q, want active (unchanged)", st.Status)
	}
	if st.LastReportState != "blocked" {
		t.Errorf("LastReportState = %q", st.LastReportState)
	}
}

func TestReport_NoParentStillUpdatesState(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{
		Name: "solo", Parent: "", Status: "active",
	})

	res, err := Report(deps, root, "solo", "complete", "done")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if res.MessageID != "" {
		t.Errorf("MessageID = %q, want empty", res.MessageID)
	}

	st, _ := state.LoadAgent(root, "solo")
	if st.Status != "done" {
		t.Errorf("Status = %q", st.Status)
	}
}

func TestReport_InvalidState(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{Name: "alice", Status: "active"})
	_, err := Report(deps, root, "alice", "bogus", "x")
	if err == nil || !strings.Contains(err.Error(), "invalid report state") {
		t.Errorf("err = %v, want invalid report state", err)
	}
}

func TestReport_EmptySummary(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{Name: "alice", Status: "active"})
	_, err := Report(deps, root, "alice", "working", "   ")
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Errorf("err = %v, want summary error", err)
	}
}

// TestReport_DoesNotSendMessage — QUM-559: pins the new state-only contract.
// After Report finishes, the reporter's state.LastReport* is persisted, but
// the parent's maildir + harness queue are completely untouched. The
// supervisor owns parent-notification via the in-process ephemeral ring.
func TestReport_DoesNotSendMessage(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{
		Name: "alice", Parent: "bob", Status: "active",
	})

	if _, err := Report(deps, root, "alice", "working", "halfway done"); err != nil {
		t.Fatalf("Report: %v", err)
	}

	st, _ := state.LoadAgent(root, "alice")
	if st.LastReportState != "working" || st.LastReportMessage != "halfway done" {
		t.Errorf("state-only path must still persist LastReport*; got state=%q msg=%q",
			st.LastReportState, st.LastReportMessage)
	}

	for _, filter := range []string{"all", "unread", "read", "archived"} {
		msgs, err := messages.List(root, "bob", filter)
		if err != nil {
			t.Fatalf("messages.List(bob, %q): %v", filter, err)
		}
		if len(msgs) != 0 {
			t.Errorf("messages.List(bob, %q) = %d entries, want 0 (QUM-559)", filter, len(msgs))
		}
	}

	pending, err := agentloop.ListPending(root, "bob")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending len = %d, want 0 (QUM-559)", len(pending))
	}
}

func TestReport_AgentNotFound(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{Name: "alice", Status: "active"})
	_, err := Report(deps, root, "nobody", "working", "x")
	if err == nil || !strings.Contains(err.Error(), "loading agent state") {
		t.Errorf("err = %v", err)
	}
}

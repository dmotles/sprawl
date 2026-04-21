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

func TestReport_WorkingUpdatesStateAndNotifiesParent(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{
		Name: "alice", Parent: "bob", Status: "active",
	})

	res, err := Report(deps, root, "alice", "working", "halfway done", "")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if res.ReportedAt != "2026-04-21T10:00:00Z" {
		t.Errorf("ReportedAt = %q", res.ReportedAt)
	}
	if res.MessageID == "" {
		t.Error("MessageID should be populated when parent is set")
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

	inbox, _ := messages.Inbox(root, "bob")
	if len(inbox) != 1 {
		t.Fatalf("inbox len = %d, want 1", len(inbox))
	}
	if !strings.Contains(inbox[0].Subject, "[STATUS]") {
		t.Errorf("subject = %q, want [STATUS]", inbox[0].Subject)
	}
	if !strings.Contains(inbox[0].Subject, "alice →") {
		t.Errorf("subject should contain 'alice →', got %q", inbox[0].Subject)
	}

	pending, _ := agentloop.ListPending(root, "bob")
	if len(pending) != 1 {
		t.Fatalf("pending len = %d, want 1", len(pending))
	}
	if pending[0].Class != agentloop.ClassAsync {
		t.Errorf("class = %q, want async", pending[0].Class)
	}
	if pending[0].From != "alice" {
		t.Errorf("from = %q", pending[0].From)
	}
}

func TestReport_CompleteSetsStatusDone(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{
		Name: "alice", Parent: "bob", Status: "active",
	})

	_, err := Report(deps, root, "alice", "complete", "done", "all tests green")
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
	if st.LastReportDetail != "all tests green" {
		t.Errorf("LastReportDetail = %q", st.LastReportDetail)
	}

	inbox, _ := messages.Inbox(root, "bob")
	if len(inbox) != 1 {
		t.Fatalf("inbox len = %d", len(inbox))
	}
	if !strings.Contains(inbox[0].Subject, "[COMPLETE]") {
		t.Errorf("subject = %q, want [COMPLETE]", inbox[0].Subject)
	}
	if !strings.Contains(inbox[0].Body, "all tests green") {
		t.Errorf("body should include detail, got %q", inbox[0].Body)
	}
}

func TestReport_FailureSetsStatusProblem(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{
		Name: "alice", Parent: "bob", Status: "active",
	})

	_, err := Report(deps, root, "alice", "failure", "blocked on API", "")
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

	inbox, _ := messages.Inbox(root, "bob")
	if len(inbox) != 1 || !strings.Contains(inbox[0].Subject, "[FAILURE]") {
		t.Errorf("subject = %v", inbox)
	}
}

func TestReport_BlockedDoesNotChangeStatus(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{
		Name: "alice", Parent: "bob", Status: "active",
	})

	_, err := Report(deps, root, "alice", "blocked", "need review", "")
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
	inbox, _ := messages.Inbox(root, "bob")
	if len(inbox) != 1 || !strings.Contains(inbox[0].Subject, "[BLOCKED]") {
		t.Errorf("subject = %v", inbox)
	}
}

func TestReport_NoParentSkipsNotification(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{
		Name: "solo", Parent: "", Status: "active",
	})

	res, err := Report(deps, root, "solo", "complete", "done", "")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if res.MessageID != "" {
		t.Errorf("MessageID = %q, want empty when no parent", res.MessageID)
	}

	st, _ := state.LoadAgent(root, "solo")
	if st.Status != "done" {
		t.Errorf("Status = %q", st.Status)
	}
}

func TestReport_InvalidState(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{Name: "alice", Status: "active"})
	_, err := Report(deps, root, "alice", "bogus", "x", "")
	if err == nil || !strings.Contains(err.Error(), "invalid report state") {
		t.Errorf("err = %v, want invalid report state", err)
	}
}

func TestReport_EmptySummary(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{Name: "alice", Status: "active"})
	_, err := Report(deps, root, "alice", "working", "   ", "")
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Errorf("err = %v, want summary error", err)
	}
}

func TestReport_AgentNotFound(t *testing.T) {
	root, deps := setupReportTest(t, &state.AgentState{Name: "alice", Status: "active"})
	_, err := Report(deps, root, "nobody", "working", "x", "")
	if err == nil || !strings.Contains(err.Error(), "loading agent state") {
		t.Errorf("err = %v", err)
	}
}

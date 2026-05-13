package cmd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
)

func newTestReportDeps(t *testing.T) (*reportDeps, string) {
	t.Helper()
	tmpDir := t.TempDir()

	deps := &reportDeps{
		getenv: func(key string) string {
			switch key {
			case "SPRAWL_ROOT":
				return tmpDir
			case "SPRAWL_AGENT_IDENTITY":
				return "alice"
			}
			return ""
		},
		nowFunc: func() time.Time {
			return time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
		},
		loadAgent: state.LoadAgent,
		saveAgent: state.SaveAgent,
	}

	os.MkdirAll(state.AgentsDir(tmpDir), 0o755)
	return deps, tmpDir
}

// assertNoParentMaildir asserts QUM-559: the CLI runReport path never
// writes to the parent's maildir or harness queue.
func assertNoParentMaildir(t *testing.T, tmpDir, parent string) {
	t.Helper()
	for _, filter := range []string{"all", "unread", "read", "archived"} {
		msgs, err := messages.List(tmpDir, parent, filter)
		if err != nil {
			t.Fatalf("messages.List(%s, %q): %v", parent, filter, err)
		}
		if len(msgs) != 0 {
			t.Errorf("messages.List(%s, %q) = %d, want 0 (QUM-559: CLI report must not write maildir)", parent, filter, len(msgs))
		}
	}
	pending, err := agentloop.ListPending(tmpDir, parent)
	if err != nil {
		t.Fatalf("ListPending(%s): %v", parent, err)
	}
	if len(pending) != 0 {
		t.Errorf("pending(%s) len = %d, want 0", parent, len(pending))
	}
}

func TestReportStatus_HappyPath(t *testing.T) {
	deps, tmpDir := newTestReportDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Parent: "root",
		Status: "active",
	})

	err := runReport(deps, "status", "working on tests")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify state updated
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.LastReportType != "status" {
		t.Errorf("LastReportType = %q, want %q", agentState.LastReportType, "status")
	}
	if agentState.LastReportMessage != "working on tests" {
		t.Errorf("LastReportMessage = %q, want %q", agentState.LastReportMessage, "working on tests")
	}
	if agentState.LastReportAt != "2026-03-31T12:00:00Z" {
		t.Errorf("LastReportAt = %q, want %q", agentState.LastReportAt, "2026-03-31T12:00:00Z")
	}
	// Status should NOT change for "status" report type
	if agentState.Status != "active" {
		t.Errorf("Status = %q, want %q (should not change for status report)", agentState.Status, "active")
	}

	// QUM-559: report_status / sprawl report status must not write parent maildir.
	assertNoParentMaildir(t, tmpDir, "root")
}

func TestReportDone_HappyPath(t *testing.T) {
	deps, tmpDir := newTestReportDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Parent: "root",
		Status: "active",
	})

	err := runReport(deps, "done", "finished implementing feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.LastReportType != "done" {
		t.Errorf("LastReportType = %q, want %q", agentState.LastReportType, "done")
	}
	if agentState.LastReportMessage != "finished implementing feature" {
		t.Errorf("LastReportMessage = %q, want %q", agentState.LastReportMessage, "finished implementing feature")
	}
	if agentState.Status != "done" {
		t.Errorf("Status = %q, want %q", agentState.Status, "done")
	}

	assertNoParentMaildir(t, tmpDir, "root")
}

func TestReportProblem_HappyPath(t *testing.T) {
	deps, tmpDir := newTestReportDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Parent: "root",
		Status: "active",
	})

	err := runReport(deps, "problem", "blocked on API access")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.LastReportType != "problem" {
		t.Errorf("LastReportType = %q, want %q", agentState.LastReportType, "problem")
	}
	if agentState.Status != "problem" {
		t.Errorf("Status = %q, want %q", agentState.Status, "problem")
	}

	assertNoParentMaildir(t, tmpDir, "root")
}

func TestReportDone_NonRootParent_NoMaildir(t *testing.T) {
	deps, tmpDir := newTestReportDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Parent: "bob",
		Status: "active",
	})

	err := runReport(deps, "done", "task complete")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Status != "done" {
		t.Errorf("Status = %q, want %q", agentState.Status, "done")
	}

	// QUM-559: non-root parent maildir must also stay empty.
	assertNoParentMaildir(t, tmpDir, "bob")
}

func TestReport_MissingAgentIdentity(t *testing.T) {
	deps, _ := newTestReportDeps(t)
	deps.getenv = func(key string) string {
		if key == "SPRAWL_ROOT" {
			return "/tmp/test"
		}
		return ""
	}

	err := runReport(deps, "status", "test")
	if err == nil {
		t.Fatal("expected error for missing SPRAWL_AGENT_IDENTITY")
	}
	if !strings.Contains(err.Error(), "SPRAWL_AGENT_IDENTITY") {
		t.Errorf("error should mention SPRAWL_AGENT_IDENTITY, got: %v", err)
	}
}

func TestReport_MissingSprawlRoot(t *testing.T) {
	deps, _ := newTestReportDeps(t)
	deps.getenv = func(key string) string {
		if key == "SPRAWL_AGENT_IDENTITY" {
			return "alice"
		}
		return ""
	}

	err := runReport(deps, "status", "test")
	if err == nil {
		t.Fatal("expected error for missing SPRAWL_ROOT")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should mention SPRAWL_ROOT, got: %v", err)
	}
}

func TestReport_AgentNotFound(t *testing.T) {
	deps, _ := newTestReportDeps(t)

	err := runReport(deps, "status", "test")
	if err == nil {
		t.Fatal("expected error for agent not found")
	}
	if !strings.Contains(err.Error(), "loading agent state") {
		t.Errorf("error should mention loading agent state, got: %v", err)
	}
}

func TestReportStatus_PreservesExistingFields(t *testing.T) {
	deps, tmpDir := newTestReportDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:      "alice",
		Type:      "engineer",
		Family:    "engineering",
		Parent:    "root",
		Prompt:    "build something",
		Branch:    "sprawl/alice",
		Worktree:  "/path/to/worktree",
		Status:    "active",
		CreatedAt: "2026-01-01T00:00:00Z",
	})

	err := runReport(deps, "status", "halfway done")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}

	if agentState.Type != "engineer" {
		t.Errorf("Type = %q, want %q", agentState.Type, "engineer")
	}
	if agentState.Family != "engineering" {
		t.Errorf("Family = %q, want %q", agentState.Family, "engineering")
	}
	if agentState.Branch != "sprawl/alice" {
		t.Errorf("Branch = %q, want %q", agentState.Branch, "sprawl/alice")
	}
	if agentState.Worktree != "/path/to/worktree" {
		t.Errorf("Worktree = %q, want %q", agentState.Worktree, "/path/to/worktree")
	}
	if agentState.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("CreatedAt = %q, want %q", agentState.CreatedAt, "2026-01-01T00:00:00Z")
	}
}

func TestReport_NoParent_NoMessage(t *testing.T) {
	deps, tmpDir := newTestReportDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Parent: "",
		Status: "active",
	})

	err := runReport(deps, "done", "all done")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("loading agent state: %v", err)
	}
	if agentState.Status != "done" {
		t.Errorf("Status = %q, want %q", agentState.Status, "done")
	}

	// No messages directory should exist at all since no messages were sent.
	msgDir := messages.MessagesDir(tmpDir)
	entries, err := os.ReadDir(msgDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("unexpected error reading messages dir: %v", err)
	}
	if len(entries) > 0 {
		t.Errorf("expected no message directories, got %d entries", len(entries))
	}
}

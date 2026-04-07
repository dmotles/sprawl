package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
)

var fixedClock = func() time.Time {
	return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
}

func TestBuildContextBlob_FullContext(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "zone", Type: "engineer", Family: "engineering", Status: "active", LastReportType: "status"},
		{Name: "birch", Type: "researcher", Family: "research", Status: "working", LastReportType: "done"},
		// done/retired should be excluded
		{Name: "ratz", Type: "engineer", Family: "engineering", Status: "done", LastReportType: "done"},
		{Name: "chip", Type: "engineer", Family: "engineering", Status: "retired", LastReportType: "status"},
	}

	msgs := []*messages.Message{
		{From: "zone", Subject: "build failed", Timestamp: "2026-04-02T11:00:00Z"},
		{From: "birch", Subject: "research complete", Timestamp: "2026-04-02T11:30:00Z"},
	}

	sessions := []Session{
		{SessionID: "sess-001", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
		{SessionID: "sess-002", Timestamp: time.Date(2026, 4, 2, 8, 0, 0, 0, time.UTC)},
	}
	bodies := []string{"First session summary.", "Second session summary."}

	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(root string) ([]*state.AgentState, error) {
			if root != "fake-root" {
				t.Errorf("agent lister got root %q, want %q", root, "fake-root")
			}
			return agents, nil
		}),
		WithMessageLister(func(root, agent, filter string) ([]*messages.Message, error) {
			if agent != "root-agent" {
				t.Errorf("message lister got agent %q, want %q", agent, "root-agent")
			}
			if filter != "unread" {
				t.Errorf("message lister got filter %q, want %q", filter, "unread")
			}
			return msgs, nil
		}),
		WithSessionLister(func(root string, n int) ([]Session, []string, error) {
			if n != 3 {
				t.Errorf("session lister got n=%d, want 3", n)
			}
			return sessions, bodies, nil
		}),
		WithClock(fixedClock),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Active agents present (excluding done/retired)
	if !strings.Contains(blob, "zone") {
		t.Error("expected blob to contain agent 'oak'")
	}
	if !strings.Contains(blob, "birch") {
		t.Error("expected blob to contain agent 'birch'")
	}
	// Done/retired excluded
	if strings.Contains(blob, "ratz") {
		t.Error("expected blob to NOT contain done agent 'elm'")
	}
	if strings.Contains(blob, "chip") {
		t.Error("expected blob to NOT contain retired agent 'pine'")
	}

	// Messages present
	if !strings.Contains(blob, "build failed") {
		t.Error("expected blob to contain message subject 'build failed'")
	}
	if !strings.Contains(blob, "research complete") {
		t.Error("expected blob to contain message subject 'research complete'")
	}

	// Sessions present
	if !strings.Contains(blob, "sess-001") {
		t.Error("expected blob to contain session 'sess-001'")
	}
	if !strings.Contains(blob, "sess-002") {
		t.Error("expected blob to contain session 'sess-002'")
	}
	if !strings.Contains(blob, "First session summary.") {
		t.Error("expected blob to contain first session body")
	}
	if !strings.Contains(blob, "Second session summary.") {
		t.Error("expected blob to contain second session body")
	}

	// Sessions oldest first: sess-001 should appear before sess-002
	idx1 := strings.Index(blob, "sess-001")
	idx2 := strings.Index(blob, "sess-002")
	if idx1 >= idx2 {
		t.Error("expected sess-001 (older) to appear before sess-002 (newer)")
	}

	// Timestamp footer
	if !strings.Contains(blob, "2026-04-02") {
		t.Error("expected blob to contain timestamp date")
	}
	if !strings.Contains(blob, "generated at") {
		t.Error("expected blob to contain 'generated at' footer")
	}
}

func TestBuildContextBlob_EmptyState(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return nil, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return nil, nil, nil
		}),
		WithClock(fixedClock),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(blob, "No active agents.") {
		t.Error("expected blob to contain 'No active agents.'")
	}
	if !strings.Contains(blob, "No pending messages.") {
		t.Error("expected blob to contain 'No pending messages.'")
	}
	if !strings.Contains(blob, "No previous sessions.") {
		t.Error("expected blob to contain 'No previous sessions.'")
	}
}

func TestBuildContextBlob_AgentFiltering(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "active-1", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
		{Name: "done-1", Type: "engineer", Family: "eng", Status: "done", LastReportType: "done"},
		{Name: "retired-1", Type: "engineer", Family: "eng", Status: "retired", LastReportType: "status"},
		{Name: "problem-1", Type: "engineer", Family: "eng", Status: "problem", LastReportType: "problem"},
		{Name: "working-1", Type: "researcher", Family: "research", Status: "working", LastReportType: "status"},
	}

	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return agents, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return nil, nil, nil
		}),
		WithClock(fixedClock),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Active, problem, working should appear
	if !strings.Contains(blob, "active-1") {
		t.Error("expected blob to contain 'active-1'")
	}
	if !strings.Contains(blob, "problem-1") {
		t.Error("expected blob to contain 'problem-1'")
	}
	if !strings.Contains(blob, "working-1") {
		t.Error("expected blob to contain 'working-1'")
	}

	// Done and retired should NOT appear
	if strings.Contains(blob, "done-1") {
		t.Error("expected blob to NOT contain 'done-1'")
	}
	if strings.Contains(blob, "retired-1") {
		t.Error("expected blob to NOT contain 'retired-1'")
	}
}

func TestBuildContextBlob_SessionOrdering(t *testing.T) {
	// Provide sessions already oldest-first; verify they remain in that order.
	sessions := []Session{
		{SessionID: "oldest", Timestamp: time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC)},
		{SessionID: "middle", Timestamp: time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC)},
		{SessionID: "newest", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
	}
	bodies := []string{"Oldest body.", "Middle body.", "Newest body."}

	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return nil, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return sessions, bodies, nil
		}),
		WithClock(fixedClock),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	idxOldest := strings.Index(blob, "oldest")
	idxMiddle := strings.Index(blob, "middle")
	idxNewest := strings.Index(blob, "newest")

	if idxOldest < 0 || idxMiddle < 0 || idxNewest < 0 {
		t.Fatal("expected all three sessions to be present in blob")
	}
	if !(idxOldest < idxMiddle && idxMiddle < idxNewest) { //nolint:staticcheck // QF1001: direct form is more readable
		t.Errorf("expected oldest < middle < newest ordering, got %d, %d, %d", idxOldest, idxMiddle, idxNewest)
	}
}

func TestBuildContextBlob_PartialFailure_SessionError(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
			}, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return []*messages.Message{
				{From: "zone", Subject: "hello", Timestamp: "2026-04-02T11:00:00Z"},
			}, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return nil, nil, fmt.Errorf("session db corrupt")
		}),
		WithClock(fixedClock),
	)

	if err == nil {
		t.Fatal("expected non-nil error when session lister fails")
	}

	// Other sections should still render
	if !strings.Contains(blob, "zone") {
		t.Error("expected blob to contain agent 'oak' despite session error")
	}
	if !strings.Contains(blob, "hello") {
		t.Error("expected blob to contain message 'hello' despite session error")
	}

	// Error marker in sessions section
	if !strings.Contains(blob, "[Error reading sessions:") {
		t.Error("expected blob to contain error marker in sessions section")
	}
}

func TestBuildContextBlob_PartialFailure_AgentError(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return nil, fmt.Errorf("agents dir unreadable")
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return []*messages.Message{
				{From: "zone", Subject: "hello", Timestamp: "2026-04-02T11:00:00Z"},
			}, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return []Session{
				{SessionID: "sess-1", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
			}, []string{"Body."}, nil
		}),
		WithClock(fixedClock),
	)

	if err == nil {
		t.Fatal("expected non-nil error when agent lister fails")
	}

	// Other sections render
	if !strings.Contains(blob, "hello") {
		t.Error("expected blob to contain message 'hello' despite agent error")
	}
	if !strings.Contains(blob, "sess-1") {
		t.Error("expected blob to contain session 'sess-1' despite agent error")
	}

	// Error marker in agents section
	if !strings.Contains(blob, "[Error listing agents:") {
		t.Error("expected blob to contain error marker in agents section")
	}
}

func TestBuildContextBlob_PartialFailure_InboxError(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
			}, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, fmt.Errorf("maildir locked")
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return []Session{
				{SessionID: "sess-1", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
			}, []string{"Body."}, nil
		}),
		WithClock(fixedClock),
	)

	if err == nil {
		t.Fatal("expected non-nil error when message lister fails")
	}

	// Other sections render
	if !strings.Contains(blob, "zone") {
		t.Error("expected blob to contain agent 'oak' despite inbox error")
	}
	if !strings.Contains(blob, "sess-1") {
		t.Error("expected blob to contain session 'sess-1' despite inbox error")
	}

	// Error marker in inbox section
	if !strings.Contains(blob, "[Error reading inbox:") {
		t.Error("expected blob to contain error marker in inbox section")
	}
}

func TestBuildContextBlob_AllFailures(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return nil, fmt.Errorf("agent fail")
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, fmt.Errorf("message fail")
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return nil, nil, fmt.Errorf("session fail")
		}),
		WithClock(fixedClock),
	)

	if err == nil {
		t.Fatal("expected non-nil error when all listers fail")
	}

	// Should still return a non-empty blob
	if blob == "" {
		t.Error("expected non-empty blob even when all listers fail")
	}

	// All error markers present
	errCount := strings.Count(strings.ToLower(blob), "error")
	if errCount < 3 {
		t.Errorf("expected at least 3 error markers in blob, got %d", errCount)
	}

	// Structural headers should still be present
	if !strings.Contains(blob, "Active State") {
		t.Error("expected blob to contain 'Active State' header")
	}
	if !strings.Contains(blob, "Recent Sessions") {
		t.Error("expected blob to contain 'Recent Sessions' header")
	}
}

func TestBuildContextBlob_TimelineBetweenSections(t *testing.T) {
	entries := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "Initial project setup"},
		{Timestamp: time.Date(2026, 4, 1, 14, 30, 0, 0, time.UTC), Summary: "Implemented messaging system"},
	}

	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
			}, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithTimelineLister(func(root string) ([]TimelineEntry, error) {
			if root != "fake-root" {
				t.Errorf("timeline lister got root %q, want %q", root, "fake-root")
			}
			return entries, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return []Session{
				{SessionID: "sess-1", Timestamp: time.Date(2026, 4, 2, 8, 0, 0, 0, time.UTC)},
			}, []string{"Session body."}, nil
		}),
		WithClock(fixedClock),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Timeline content present
	if !strings.Contains(blob, "## Session Timeline") {
		t.Error("expected blob to contain '## Session Timeline' header")
	}
	if !strings.Contains(blob, "Initial project setup") {
		t.Error("expected blob to contain timeline entry 'Initial project setup'")
	}
	if !strings.Contains(blob, "Implemented messaging system") {
		t.Error("expected blob to contain timeline entry 'Implemented messaging system'")
	}

	// Verify ordering: Active State < Session Timeline < Recent Sessions
	idxActive := strings.Index(blob, "## Active State")
	idxTimeline := strings.Index(blob, "## Session Timeline")
	idxRecent := strings.Index(blob, "## Recent Sessions")

	if idxActive < 0 || idxTimeline < 0 || idxRecent < 0 {
		t.Fatal("expected all three section headers to be present")
	}
	if !(idxActive < idxTimeline && idxTimeline < idxRecent) { //nolint:staticcheck // QF1001: direct form is more readable
		t.Errorf("expected Active State (%d) < Session Timeline (%d) < Recent Sessions (%d)",
			idxActive, idxTimeline, idxRecent)
	}

	// Entries formatted correctly
	if !strings.Contains(blob, "- 2026-04-01T10:00:00Z: Initial project setup") {
		t.Error("expected timeline entry with correct format")
	}
}

func TestBuildContextBlob_TimelineMissing(t *testing.T) {
	// Empty slice (file doesn't exist) → section omitted
	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return nil, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithTimelineLister(func(string) ([]TimelineEntry, error) {
			return []TimelineEntry{}, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return nil, nil, nil
		}),
		WithClock(fixedClock),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(blob, "Session Timeline") {
		t.Error("expected blob to NOT contain 'Session Timeline' when timeline is empty")
	}
}

func TestBuildContextBlob_TimelineNil(t *testing.T) {
	// Nil slice (also empty) → section omitted
	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return nil, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithTimelineLister(func(string) ([]TimelineEntry, error) {
			return nil, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return nil, nil, nil
		}),
		WithClock(fixedClock),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(blob, "Session Timeline") {
		t.Error("expected blob to NOT contain 'Session Timeline' when timeline is nil")
	}
}

func TestBuildContextBlob_TimelineError(t *testing.T) {
	// Timeline read fails → partial failure, other sections still render
	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
			}, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithTimelineLister(func(string) ([]TimelineEntry, error) {
			return nil, fmt.Errorf("timeline unreadable")
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return []Session{
				{SessionID: "sess-1", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
			}, []string{"Body."}, nil
		}),
		WithClock(fixedClock),
	)

	if err == nil {
		t.Fatal("expected non-nil error when timeline lister fails")
	}

	// Other sections still render
	if !strings.Contains(blob, "zone") {
		t.Error("expected blob to contain agent 'oak' despite timeline error")
	}
	if !strings.Contains(blob, "sess-1") {
		t.Error("expected blob to contain session despite timeline error")
	}

	// Error marker present
	if !strings.Contains(blob, "[Error reading timeline:") {
		t.Error("expected blob to contain error marker for timeline section")
	}
}

func TestBuildContextBlob_TimelinePartialData(t *testing.T) {
	// Some valid entries - verify they all render correctly
	entries := []TimelineEntry{
		{Timestamp: time.Date(2026, 3, 30, 9, 0, 0, 0, time.UTC), Summary: "Project kickoff"},
		{Timestamp: time.Date(2026, 3, 31, 15, 0, 0, 0, time.UTC), Summary: "API design complete"},
		{Timestamp: time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC), Summary: "First PR merged"},
	}

	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return nil, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithTimelineLister(func(string) ([]TimelineEntry, error) {
			return entries, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return nil, nil, nil
		}),
		WithClock(fixedClock),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All entries present
	if !strings.Contains(blob, "Project kickoff") {
		t.Error("expected blob to contain 'Project kickoff'")
	}
	if !strings.Contains(blob, "API design complete") {
		t.Error("expected blob to contain 'API design complete'")
	}
	if !strings.Contains(blob, "First PR merged") {
		t.Error("expected blob to contain 'First PR merged'")
	}

	// Entries in chronological order
	idx1 := strings.Index(blob, "Project kickoff")
	idx2 := strings.Index(blob, "API design complete")
	idx3 := strings.Index(blob, "First PR merged")
	if !(idx1 < idx2 && idx2 < idx3) { //nolint:staticcheck // QF1001: direct form is more readable
		t.Errorf("expected chronological ordering: %d < %d < %d", idx1, idx2, idx3)
	}
}

func TestBuildContextBlob_DefaultDeps(t *testing.T) {
	// Call with no options, using a real but empty temp dir as sprawlRoot.
	tmpDir := t.TempDir()

	blob, err := BuildContextBlob(tmpDir, "test-root",
		WithClock(fixedClock),
	)
	if err != nil {
		t.Fatalf("unexpected error with empty temp dir: %v", err)
	}

	if !strings.Contains(blob, "No active agents.") {
		t.Error("expected blob to contain 'No active agents.'")
	}
	if !strings.Contains(blob, "No pending messages.") {
		t.Error("expected blob to contain 'No pending messages.'")
	}
	if !strings.Contains(blob, "No previous sessions.") {
		t.Error("expected blob to contain 'No previous sessions.'")
	}
}

// --- Budget enforcement tests ---

// budgetTestDeps returns common build options for budget tests with the given
// agents, timeline entries, sessions, and bodies. All listers are injected
// so tests are deterministic.
func budgetTestDeps(
	agents []*state.AgentState,
	timeline []TimelineEntry,
	sessions []Session,
	bodies []string,
) []BuildOption {
	return []BuildOption{
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return agents, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithTimelineLister(func(string) ([]TimelineEntry, error) {
			return timeline, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return sessions, bodies, nil
		}),
		WithPersistentKnowledgeReader(func(string) (string, error) {
			return "", nil
		}),
		WithClock(fixedClock),
	}
}

func TestBuildContextBlob_BudgetGenerousNoDifference(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
	}
	timeline := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "Setup complete"},
	}
	sessions := []Session{
		{SessionID: "sess-001", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
	}
	bodies := []string{"Session one body."}

	deps := budgetTestDeps(agents, timeline, sessions, bodies)

	// Without budget
	noBudget, err := BuildContextBlob("fake-root", "root-agent", deps...)
	if err != nil {
		t.Fatalf("unexpected error (no budget): %v", err)
	}

	// With very generous budget
	withBudget, err := BuildContextBlob("fake-root", "root-agent",
		append(deps, WithBudgetConfig(BudgetConfig{
			MaxTotalChars:   100000,
			MaxSessionChars: 100000,
		}))...,
	)
	if err != nil {
		t.Fatalf("unexpected error (with budget): %v", err)
	}

	if noBudget != withBudget {
		t.Errorf("generous budget should produce identical output to no budget\n--- no budget ---\n%s\n--- with budget ---\n%s", noBudget, withBudget)
	}
}

func TestBuildContextBlob_BudgetOmitsOldestSessions(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
	}
	timeline := []TimelineEntry{}
	sessions := []Session{
		{SessionID: "sess-oldest", Timestamp: time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC)},
		{SessionID: "sess-middle", Timestamp: time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC)},
		{SessionID: "sess-newest", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
	}
	bodies := []string{
		strings.Repeat("A", 200), // oldest: 200 bytes body
		strings.Repeat("B", 200), // middle: 200 bytes body
		strings.Repeat("C", 200), // newest: 200 bytes body
	}

	// Build without budget, then build with only the newest session to find the
	// size of the non-session portions + 1 session. This lets us set a budget
	// that reliably fits everything except the 2 oldest sessions.
	deps := budgetTestDeps(agents, timeline, sessions, bodies)
	onlyNewest := budgetTestDeps(agents, timeline, sessions[2:], bodies[2:])
	oneSessionBlob, _ := BuildContextBlob("fake-root", "root-agent", onlyNewest...)
	// Add margin for the omission note (~30 bytes).
	budget := MeasureBytes(oneSessionBlob) + 30

	blob, err := BuildContextBlob("fake-root", "root-agent",
		append(deps, WithBudgetConfig(BudgetConfig{
			MaxTotalChars:   budget,
			MaxSessionChars: 5000,
		}))...,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(blob, "sess-newest") {
		t.Error("expected newest session to be present")
	}
	if strings.Contains(blob, "sess-oldest") {
		t.Error("expected oldest session to be omitted")
	}
	if strings.Contains(blob, "sess-middle") {
		t.Error("expected middle session to be omitted")
	}
	if !strings.Contains(blob, "2 older sessions omitted") {
		t.Error("expected note about 2 older sessions omitted")
	}
	if MeasureBytes(blob) > budget {
		t.Errorf("blob size %d exceeds budget %d", MeasureBytes(blob), budget)
	}
}

func TestBuildContextBlob_BudgetTruncatesTimeline(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
	}
	// Create a long timeline to ensure it gets truncated.
	var timeline []TimelineEntry
	for i := range 50 {
		timeline = append(timeline, TimelineEntry{
			Timestamp: time.Date(2026, 3, 1+i%28, 10, 0, 0, 0, time.UTC),
			Summary:   fmt.Sprintf("Timeline entry number %d with some additional detail text", i),
		})
	}
	sessions := []Session{}
	bodies := []string{}

	deps := budgetTestDeps(agents, timeline, sessions, bodies)

	// Build without budget to measure full size.
	fullBlob, _ := BuildContextBlob("fake-root", "root-agent", deps...)
	fullSize := MeasureBytes(fullBlob)

	// Set budget to roughly half: enough for active state but only partial timeline.
	budget := fullSize / 2

	blob, err := BuildContextBlob("fake-root", "root-agent",
		append(deps, WithBudgetConfig(BudgetConfig{
			MaxTotalChars:   budget,
			MaxSessionChars: 2000,
		}))...,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(blob, "## Session Timeline") {
		t.Error("expected timeline section header to be present")
	}
	// The timeline should be truncated with a budget note.
	if !strings.Contains(blob, "truncated") {
		t.Error("expected 'truncated' note when timeline exceeds budget")
	}
	if MeasureBytes(blob) > budget {
		t.Errorf("blob size %d exceeds budget %d", MeasureBytes(blob), budget)
	}
}

func TestBuildContextBlob_BudgetActiveStateOnly(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
	}
	timeline := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "Setup complete"},
	}
	sessions := []Session{
		{SessionID: "sess-001", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
	}
	bodies := []string{"Session body."}

	deps := budgetTestDeps(agents, timeline, sessions, bodies)

	// Build without budget, then measure the active-state portion to set a
	// budget that fits active state + footer but nothing else.
	noBudgetBlob, _ := BuildContextBlob("fake-root", "root-agent", deps...)

	// Find where timeline or sessions start to estimate active-state size.
	activeEnd := strings.Index(noBudgetBlob, "\n## Session Timeline")
	if activeEnd < 0 {
		activeEnd = strings.Index(noBudgetBlob, "\n## Recent Sessions")
	}
	if activeEnd < 0 {
		t.Fatal("could not find section boundary in blob")
	}
	// Budget = active state + some margin for the footer (~120 bytes)
	budget := activeEnd + 130

	blob, err := BuildContextBlob("fake-root", "root-agent",
		append(deps, WithBudgetConfig(BudgetConfig{
			MaxTotalChars:   budget,
			MaxSessionChars: 2000,
		}))...,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = noBudgetBlob

	// Active state should be present.
	if !strings.Contains(blob, "## Active State") {
		t.Error("expected '## Active State' header to be present")
	}

	// Timeline and sessions sections should be completely omitted or empty.
	hasTimeline := strings.Contains(blob, "## Session Timeline") &&
		strings.Contains(blob, "Setup complete")
	hasSessions := strings.Contains(blob, "## Recent Sessions") &&
		strings.Contains(blob, "sess-001")
	if hasTimeline {
		t.Error("expected timeline content to be omitted under tight budget")
	}
	if hasSessions {
		t.Error("expected sessions content to be omitted under tight budget")
	}
	if MeasureBytes(blob) > budget {
		t.Errorf("blob size %d exceeds budget %d", MeasureBytes(blob), budget)
	}
}

func TestBuildContextBlob_BudgetTruncatesActiveState(t *testing.T) {
	// Many agents to make active state large.
	var agents []*state.AgentState
	for i := range 20 {
		agents = append(agents, &state.AgentState{
			Name:           fmt.Sprintf("agent-%d", i),
			Type:           "engineer",
			Family:         "eng",
			Status:         "active",
			LastReportType: "status",
		})
	}
	timeline := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "Entry"},
	}
	sessions := []Session{
		{SessionID: "sess-001", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
	}
	bodies := []string{"Body."}

	deps := budgetTestDeps(agents, timeline, sessions, bodies)

	// Extremely tight budget.
	budget := 50

	blob, err := BuildContextBlob("fake-root", "root-agent",
		append(deps, WithBudgetConfig(BudgetConfig{
			MaxTotalChars:   budget,
			MaxSessionChars: 2000,
		}))...,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(blob, "[...truncated]") {
		t.Error("expected '[...truncated]' note when active state is truncated")
	}
	// Timeline and sessions should be omitted.
	if strings.Contains(blob, "## Session Timeline") {
		t.Error("expected timeline to be omitted under extremely tight budget")
	}
	if strings.Contains(blob, "sess-001") {
		t.Error("expected sessions to be omitted under extremely tight budget")
	}
	if MeasureBytes(blob) > budget {
		t.Errorf("blob size %d exceeds budget %d", MeasureBytes(blob), budget)
	}
}

func TestBuildContextBlob_BudgetSingleSessionTruncated(t *testing.T) {
	agents := []*state.AgentState{}
	timeline := []TimelineEntry{}
	sessions := []Session{
		{SessionID: "sess-001", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
	}
	bodies := []string{"This is a very long session body that should definitely exceed the per-session character limit when it is set to a very low value like thirty bytes."}

	deps := budgetTestDeps(agents, timeline, sessions, bodies)

	blob, err := BuildContextBlob("fake-root", "root-agent",
		append(deps, WithBudgetConfig(BudgetConfig{
			MaxTotalChars:   100000,
			MaxSessionChars: 30,
		}))...,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(blob, "[...truncated]") {
		t.Error("expected '[...truncated]' note when session body exceeds MaxSessionChars")
	}
	// The full body should NOT be present.
	if strings.Contains(blob, "when it is set to a very low value like thirty bytes.") {
		t.Error("expected session body to be truncated, but full body found")
	}
}

func TestBuildContextBlob_BudgetSessionsNewestFirstAllocation(t *testing.T) {
	agents := []*state.AgentState{}
	timeline := []TimelineEntry{}
	sessions := []Session{
		{SessionID: "sess-oldest", Timestamp: time.Date(2026, 3, 29, 10, 0, 0, 0, time.UTC)},
		{SessionID: "sess-middle", Timestamp: time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC)},
		{SessionID: "sess-newest", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
	}
	bodies := []string{
		strings.Repeat("X", 200), // oldest: 200 bytes body
		"Middle session body text.",
		"Newest session body text.",
	}

	deps := budgetTestDeps(agents, timeline, sessions, bodies)

	// Build without budget to measure full output, then subtract enough to
	// force the oldest session (with its 200-byte body) to be dropped.
	fullBlob, _ := BuildContextBlob("fake-root", "root-agent", deps...)
	budget := MeasureBytes(fullBlob) - 150

	blob, err := BuildContextBlob("fake-root", "root-agent",
		append(deps, WithBudgetConfig(BudgetConfig{
			MaxTotalChars:   budget,
			MaxSessionChars: 5000,
		}))...,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Newest 2 should be present.
	if !strings.Contains(blob, "sess-newest") {
		t.Error("expected newest session to be present")
	}
	if !strings.Contains(blob, "sess-middle") {
		t.Error("expected middle session to be present")
	}
	// Oldest should be dropped.
	if strings.Contains(blob, "sess-oldest") {
		t.Error("expected oldest session to be omitted")
	}
	// Omission note.
	if !strings.Contains(blob, "1 older session omitted") {
		t.Error("expected note about 1 older session omitted")
	}
	// Display order: middle before newest (oldest-first display order).
	idxMiddle := strings.Index(blob, "sess-middle")
	idxNewest := strings.Index(blob, "sess-newest")
	if idxMiddle >= idxNewest {
		t.Errorf("expected middle (%d) to appear before newest (%d) in oldest-first display order", idxMiddle, idxNewest)
	}
	if MeasureBytes(blob) > budget {
		t.Errorf("blob size %d exceeds budget %d", MeasureBytes(blob), budget)
	}
}

func TestBuildContextBlob_BudgetZeroValueUsesDefault(t *testing.T) {
	// Create enough data to exceed the default 10000 char budget.
	var agents []*state.AgentState
	for i := range 10 {
		agents = append(agents, &state.AgentState{
			Name:           fmt.Sprintf("agent-%d-with-a-long-name-padding", i),
			Type:           "engineer",
			Family:         "engineering",
			Status:         "active",
			LastReportType: "status",
		})
	}
	var timeline []TimelineEntry
	for i := range 100 {
		timeline = append(timeline, TimelineEntry{
			Timestamp: time.Date(2026, 3, 1+i%28, 10, 0, 0, 0, time.UTC),
			Summary:   fmt.Sprintf("Timeline entry %d with enough text to take up space in the context blob", i),
		})
	}
	var sessions []Session
	var sessionBodies []string
	for i := range 3 {
		sessions = append(sessions, Session{
			SessionID: fmt.Sprintf("sess-%03d", i),
			Timestamp: time.Date(2026, 4, 1, i, 0, 0, 0, time.UTC),
		})
		sessionBodies = append(sessionBodies, strings.Repeat(fmt.Sprintf("Session %d body content. ", i), 50))
	}

	deps := budgetTestDeps(agents, timeline, sessions, sessionBodies)

	// Verify that without budget, output exceeds 10000 chars.
	fullBlob, _ := BuildContextBlob("fake-root", "root-agent", deps...)
	if MeasureBytes(fullBlob) <= DefaultBudgetConfig().MaxTotalChars {
		t.Fatalf("test setup: full blob (%d chars) should exceed default budget (%d) for this test to be meaningful",
			MeasureBytes(fullBlob), DefaultBudgetConfig().MaxTotalChars)
	}

	// Pass zero-value BudgetConfig — should use DefaultBudgetConfig().
	blob, err := BuildContextBlob("fake-root", "root-agent",
		append(deps, WithBudgetConfig(BudgetConfig{}))...,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if MeasureBytes(blob) > DefaultBudgetConfig().MaxTotalChars {
		t.Errorf("zero-value BudgetConfig: blob size %d exceeds default budget %d",
			MeasureBytes(blob), DefaultBudgetConfig().MaxTotalChars)
	}
}

func TestBuildContextBlob_BudgetIncludesFooter(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
	}
	timeline := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "Setup complete"},
	}
	sessions := []Session{
		{SessionID: "sess-001", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
	}
	bodies := []string{"Session body."}

	deps := budgetTestDeps(agents, timeline, sessions, bodies)

	// Tight budget — but footer must still be present.
	blob, err := BuildContextBlob("fake-root", "root-agent",
		append(deps, WithBudgetConfig(BudgetConfig{
			MaxTotalChars:   400,
			MaxSessionChars: 2000,
		}))...,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(blob, "generated at") {
		t.Error("expected footer with 'generated at' to be present even under tight budget")
	}
	if !strings.Contains(blob, "---") {
		t.Error("expected footer separator '---' to be present even under tight budget")
	}
}

func TestBuildContextBlob_PersistentKnowledge_Rendered(t *testing.T) {
	pkContent := "The project uses cobra for CLI commands.\nAlways run tests before committing."

	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
			}, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithTimelineLister(func(string) ([]TimelineEntry, error) {
			return []TimelineEntry{
				{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "Setup complete"},
			}, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return []Session{
				{SessionID: "sess-001", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
			}, []string{"Session body."}, nil
		}),
		WithPersistentKnowledgeReader(func(root string) (string, error) {
			if root != "fake-root" {
				t.Errorf("PK reader got root %q, want %q", root, "fake-root")
			}
			return pkContent, nil
		}),
		WithClock(fixedClock),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Section header present
	if !strings.Contains(blob, "## Persistent Knowledge") {
		t.Error("expected blob to contain '## Persistent Knowledge' header")
	}

	// Content present
	if !strings.Contains(blob, "The project uses cobra for CLI commands.") {
		t.Error("expected blob to contain persistent knowledge content")
	}
	if !strings.Contains(blob, "Always run tests before committing.") {
		t.Error("expected blob to contain persistent knowledge content (second line)")
	}

	// Ordering: Active State < Persistent Knowledge < Session Timeline < Recent Sessions
	idxActive := strings.Index(blob, "## Active State")
	idxPK := strings.Index(blob, "## Persistent Knowledge")
	idxTimeline := strings.Index(blob, "## Session Timeline")
	idxRecent := strings.Index(blob, "## Recent Sessions")

	if idxActive < 0 || idxPK < 0 || idxTimeline < 0 || idxRecent < 0 {
		t.Fatalf("expected all four section headers to be present; got Active=%d PK=%d Timeline=%d Recent=%d",
			idxActive, idxPK, idxTimeline, idxRecent)
	}
	if !(idxActive < idxPK && idxPK < idxTimeline && idxTimeline < idxRecent) { //nolint:staticcheck // QF1001: direct form is more readable
		t.Errorf("expected ordering Active(%d) < PK(%d) < Timeline(%d) < Recent(%d)",
			idxActive, idxPK, idxTimeline, idxRecent)
	}
}

func TestBuildContextBlob_PersistentKnowledge_Empty_OmitsSection(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return nil, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return nil, nil, nil
		}),
		WithPersistentKnowledgeReader(func(root string) (string, error) {
			return "", nil
		}),
		WithClock(fixedClock),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(blob, "## Persistent Knowledge") {
		t.Error("expected blob to NOT contain '## Persistent Knowledge' when PK is empty")
	}
}

func TestBuildContextBlob_PersistentKnowledge_Error_ContinuesWithOtherSections(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
			}, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		}),
		WithSessionLister(func(string, int) ([]Session, []string, error) {
			return []Session{
				{SessionID: "sess-001", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
			}, []string{"Session body."}, nil
		}),
		WithPersistentKnowledgeReader(func(root string) (string, error) {
			return "", fmt.Errorf("knowledge file corrupt")
		}),
		WithClock(fixedClock),
	)

	// Error should be returned
	if err == nil {
		t.Fatal("expected non-nil error when PK reader fails")
	}

	// Other sections still render
	if !strings.Contains(blob, "zone") {
		t.Error("expected blob to contain agent 'oak' despite PK error")
	}
	if !strings.Contains(blob, "sess-001") {
		t.Error("expected blob to contain session despite PK error")
	}

	// Error marker present in blob
	if !strings.Contains(blob, "[Error reading persistent knowledge:") {
		t.Error("expected blob to contain error marker for persistent knowledge section")
	}
}

func TestBuildContextBlob_BudgetPersistentKnowledge_SecondPriority(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "zone", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
	}
	timeline := []TimelineEntry{}
	sessions := []Session{
		{SessionID: "sess-oldest", Timestamp: time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC)},
		{SessionID: "sess-newest", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
	}
	sessionBodies := []string{
		strings.Repeat("A", 300),
		strings.Repeat("B", 300),
	}
	pkContent := "Important: always use dependency injection for testability."

	deps := budgetTestDeps(agents, timeline, sessions, sessionBodies)

	// Build with PK but no budget to find full size.
	fullBlob, _ := BuildContextBlob("fake-root", "root-agent",
		append(deps, WithPersistentKnowledgeReader(func(string) (string, error) {
			return pkContent, nil
		}))...,
	)
	fullSize := MeasureBytes(fullBlob)

	// Set budget tight enough that PK fits but at least one session must be dropped.
	budget := fullSize - 350

	blob, err := BuildContextBlob("fake-root", "root-agent",
		append(deps,
			WithPersistentKnowledgeReader(func(string) (string, error) {
				return pkContent, nil
			}),
			WithBudgetConfig(BudgetConfig{
				MaxTotalChars:   budget,
				MaxSessionChars: 5000,
			}),
		)...,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// PK should be present (higher priority than sessions)
	if !strings.Contains(blob, "## Persistent Knowledge") {
		t.Error("expected persistent knowledge section to be present under tight budget")
	}
	if !strings.Contains(blob, "dependency injection") {
		t.Error("expected persistent knowledge content to be present under tight budget")
	}

	// At least one session should be omitted
	hasOldest := strings.Contains(blob, "sess-oldest")
	hasNewest := strings.Contains(blob, "sess-newest")
	if hasOldest && hasNewest {
		t.Error("expected at least one session to be omitted to make room for persistent knowledge")
	}

	if MeasureBytes(blob) > budget {
		t.Errorf("blob size %d exceeds budget %d", MeasureBytes(blob), budget)
	}
}

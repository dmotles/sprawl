package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/dendra/internal/messages"
	"github.com/dmotles/dendra/internal/state"
)

var fixedClock = func() time.Time {
	return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
}

func TestBuildContextBlob_FullContext(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "oak", Type: "engineer", Family: "engineering", Status: "active", LastReportType: "status"},
		{Name: "birch", Type: "researcher", Family: "research", Status: "working", LastReportType: "done"},
		// done/retired should be excluded
		{Name: "elm", Type: "engineer", Family: "engineering", Status: "done", LastReportType: "done"},
		{Name: "pine", Type: "engineer", Family: "engineering", Status: "retired", LastReportType: "status"},
	}

	msgs := []*messages.Message{
		{From: "oak", Subject: "build failed", Timestamp: "2026-04-02T11:00:00Z"},
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
	if !strings.Contains(blob, "oak") {
		t.Error("expected blob to contain agent 'oak'")
	}
	if !strings.Contains(blob, "birch") {
		t.Error("expected blob to contain agent 'birch'")
	}
	// Done/retired excluded
	if strings.Contains(blob, "elm") {
		t.Error("expected blob to NOT contain done agent 'elm'")
	}
	if strings.Contains(blob, "pine") {
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
	if !(idxOldest < idxMiddle && idxMiddle < idxNewest) {
		t.Errorf("expected oldest < middle < newest ordering, got %d, %d, %d", idxOldest, idxMiddle, idxNewest)
	}
}

func TestBuildContextBlob_PartialFailure_SessionError(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "root-agent",
		WithAgentLister(func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "oak", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
			}, nil
		}),
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) {
			return []*messages.Message{
				{From: "oak", Subject: "hello", Timestamp: "2026-04-02T11:00:00Z"},
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
	if !strings.Contains(blob, "oak") {
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
				{From: "oak", Subject: "hello", Timestamp: "2026-04-02T11:00:00Z"},
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
				{Name: "oak", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
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
	if !strings.Contains(blob, "oak") {
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
				{Name: "oak", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
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
	if !(idxActive < idxTimeline && idxTimeline < idxRecent) {
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
				{Name: "oak", Type: "engineer", Family: "eng", Status: "active", LastReportType: "status"},
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
	if !strings.Contains(blob, "oak") {
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
	if !(idx1 < idx2 && idx2 < idx3) {
		t.Errorf("expected chronological ordering: %d < %d < %d", idx1, idx2, idx3)
	}
}

func TestBuildContextBlob_DefaultDeps(t *testing.T) {
	// Call with no options, using a real but empty temp dir as dendraRoot.
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

package supervisor

import (
	"context"
	"testing"

	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
)

func newTestSupervisor(t *testing.T) (*Real, string) {
	t.Helper()
	tmpDir := t.TempDir()

	sup := NewReal(Config{
		SprawlRoot: tmpDir,
		CallerName: "weave",
	})

	return sup, tmpDir
}

func saveTestAgent(t *testing.T, sprawlRoot string, a *state.AgentState) {
	t.Helper()
	if err := state.SaveAgent(sprawlRoot, a); err != nil {
		t.Fatalf("SaveAgent(%s): %v", a.Name, err)
	}
}

func TestStatus_ReturnsAllAgents(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ratz",
		Type:   "engineer",
		Family: "engineering",
		Parent: "weave",
		Status: "active",
		Branch: "dmotles/feature-a",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ghost",
		Type:   "researcher",
		Family: "engineering",
		Parent: "weave",
		Status: "active",
		Branch: "dmotles/research-b",
	})

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}

	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}

	nameMap := make(map[string]AgentInfo)
	for _, a := range agents {
		nameMap[a.Name] = a
	}

	ratz, ok := nameMap["ratz"]
	if !ok {
		t.Fatal("missing agent ratz")
	}
	if ratz.Type != "engineer" {
		t.Errorf("ratz.Type = %q, want engineer", ratz.Type)
	}
	if ratz.Status != "active" {
		t.Errorf("ratz.Status = %q, want active", ratz.Status)
	}

	ghost, ok := nameMap["ghost"]
	if !ok {
		t.Fatal("missing agent ghost")
	}
	if ghost.Type != "researcher" {
		t.Errorf("ghost.Type = %q, want researcher", ghost.Type)
	}
}

func TestStatus_EmptyReturnsEmpty(t *testing.T) {
	sup, _ := newTestSupervisor(t)

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}

	if len(agents) != 0 {
		t.Errorf("got %d agents, want 0", len(agents))
	}
}

func TestDelegate_EnqueuesTask(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ratz",
		Type:   "engineer",
		Family: "engineering",
		Parent: "weave",
		Status: "active",
	})

	err := sup.Delegate(context.Background(), "ratz", "implement feature X")
	if err != nil {
		t.Fatalf("Delegate() error: %v", err)
	}

	// Verify task was enqueued
	tasks, err := state.ListTasks(tmpDir, "ratz")
	if err != nil {
		t.Fatalf("ListTasks() error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Prompt != "implement feature X" {
		t.Errorf("task prompt = %q, want 'implement feature X'", tasks[0].Prompt)
	}
	if tasks[0].Status != "queued" {
		t.Errorf("task status = %q, want queued", tasks[0].Status)
	}
}

func TestDelegate_AgentNotFound(t *testing.T) {
	sup, _ := newTestSupervisor(t)

	err := sup.Delegate(context.Background(), "nonexistent", "do something")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestDelegate_KilledAgent(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ratz",
		Type:   "engineer",
		Family: "engineering",
		Parent: "weave",
		Status: "killed",
	})

	err := sup.Delegate(context.Background(), "ratz", "do something")
	if err == nil {
		t.Fatal("expected error for killed agent")
	}
}

func TestMessage_SendsMessage(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ghost",
		Type:   "researcher",
		Family: "engineering",
		Parent: "weave",
		Status: "active",
	})

	err := sup.Message(context.Background(), "ghost", "status update", "all done")
	if err != nil {
		t.Fatalf("Message() error: %v", err)
	}

	// Verify message was delivered
	msgs, err := messages.Inbox(tmpDir, "ghost")
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Subject != "status update" {
		t.Errorf("message subject = %q, want 'status update'", msgs[0].Subject)
	}
	if msgs[0].Body != "all done" {
		t.Errorf("message body = %q, want 'all done'", msgs[0].Body)
	}
	if msgs[0].From != "weave" {
		t.Errorf("message from = %q, want weave", msgs[0].From)
	}
}

func TestMessage_AgentNotFound(t *testing.T) {
	sup, _ := newTestSupervisor(t)

	err := sup.Message(context.Background(), "nonexistent", "hello", "world")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

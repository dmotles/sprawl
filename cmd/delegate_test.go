package cmd

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dmotles/dendra/internal/messages"
	"github.com/dmotles/dendra/internal/state"
)

func newTestDelegateDeps(t *testing.T) (*delegateDeps, string) {
	t.Helper()
	tmpDir := t.TempDir()

	deps := &delegateDeps{
		getenv: func(key string) string {
			switch key {
			case "DENDRA_ROOT":
				return tmpDir
			case "DENDRA_AGENT_IDENTITY":
				return "root"
			}
			return ""
		},
		loadAgent:   state.LoadAgent,
		enqueueTask: state.EnqueueTask,
		sendMessage: messages.Send,
	}

	// Ensure agents dir exists
	os.MkdirAll(state.AgentsDir(tmpDir), 0755)

	return deps, tmpDir
}

func TestDelegate_HappyPath(t *testing.T) {
	deps, tmpDir := newTestDelegateDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Status: "active",
		Parent: "root",
	})

	err := runDelegate(deps, "alice", "build login feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify task was enqueued
	tasks, err := state.ListTasks(tmpDir, "alice")
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Status != "queued" {
		t.Errorf("task status = %q, want %q", tasks[0].Status, "queued")
	}
	if tasks[0].Prompt != "build login feature" {
		t.Errorf("task prompt = %q, want %q", tasks[0].Prompt, "build login feature")
	}

	// Verify wake message was sent
	inbox, err := messages.Inbox(tmpDir, "alice")
	if err != nil {
		t.Fatalf("reading inbox: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("expected 1 message in inbox, got %d", len(inbox))
	}
	if inbox[0].Subject != "[TASK] wake" {
		t.Errorf("message subject = %q, want %q", inbox[0].Subject, "[TASK] wake")
	}

	// Wake message body should NOT contain the full task text
	if strings.Contains(inbox[0].Body, "build login feature") {
		t.Error("wake message body should not contain the full task text (task content belongs in the task file only)")
	}
}

func TestDelegate_EmptyPrompt(t *testing.T) {
	deps, tmpDir := newTestDelegateDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Status: "active",
		Parent: "root",
	})

	err := runDelegate(deps, "alice", "")
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention 'empty', got: %v", err)
	}
}

func TestDelegate_MultipleTasks_FIFO(t *testing.T) {
	deps, tmpDir := newTestDelegateDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Status: "active",
		Parent: "root",
	})

	if err := runDelegate(deps, "alice", "first task"); err != nil {
		t.Fatalf("unexpected error on first delegate: %v", err)
	}
	if err := runDelegate(deps, "alice", "second task"); err != nil {
		t.Fatalf("unexpected error on second delegate: %v", err)
	}
	if err := runDelegate(deps, "alice", "third task"); err != nil {
		t.Fatalf("unexpected error on third delegate: %v", err)
	}

	tasks, err := state.ListTasks(tmpDir, "alice")
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	if tasks[0].Prompt != "first task" {
		t.Errorf("tasks[0].Prompt = %q, want %q", tasks[0].Prompt, "first task")
	}
	if tasks[1].Prompt != "second task" {
		t.Errorf("tasks[1].Prompt = %q, want %q", tasks[1].Prompt, "second task")
	}
	if tasks[2].Prompt != "third task" {
		t.Errorf("tasks[2].Prompt = %q, want %q", tasks[2].Prompt, "third task")
	}
}

func TestDelegate_AgentNotFound(t *testing.T) {
	deps, _ := newTestDelegateDeps(t)

	err := runDelegate(deps, "ghost", "some task")
	if err == nil {
		t.Fatal("expected error for non-existent agent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestDelegate_AgentKilled(t *testing.T) {
	deps, tmpDir := newTestDelegateDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Status: "killed",
		Parent: "root",
	})

	err := runDelegate(deps, "alice", "some task")
	if err == nil {
		t.Fatal("expected error for killed agent")
	}
	if !strings.Contains(err.Error(), "cannot delegate") {
		t.Errorf("error should mention 'cannot delegate', got: %v", err)
	}
	if !strings.Contains(err.Error(), "killed") {
		t.Errorf("error should mention the status 'killed', got: %v", err)
	}
}

func TestDelegate_AgentRetired(t *testing.T) {
	deps, tmpDir := newTestDelegateDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Status: "retired",
		Parent: "root",
	})

	err := runDelegate(deps, "alice", "some task")
	if err == nil {
		t.Fatal("expected error for retired agent")
	}
	if !strings.Contains(err.Error(), "cannot delegate") {
		t.Errorf("error should mention 'cannot delegate', got: %v", err)
	}
}

func TestDelegate_AgentRetiring(t *testing.T) {
	deps, tmpDir := newTestDelegateDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Status: "retiring",
		Parent: "root",
	})

	err := runDelegate(deps, "alice", "some task")
	if err == nil {
		t.Fatal("expected error for retiring agent")
	}
	if !strings.Contains(err.Error(), "cannot delegate") {
		t.Errorf("error should mention 'cannot delegate', got: %v", err)
	}
}

func TestDelegate_MissingDendraRoot(t *testing.T) {
	deps, _ := newTestDelegateDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_AGENT_IDENTITY" {
			return "root"
		}
		return ""
	}

	err := runDelegate(deps, "alice", "some task")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

func TestDelegate_MissingAgentIdentity(t *testing.T) {
	deps, _ := newTestDelegateDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_ROOT" {
			return "/tmp/test"
		}
		return ""
	}

	err := runDelegate(deps, "alice", "some task")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_AGENT_IDENTITY")
	}
	if !strings.Contains(err.Error(), "DENDRA_AGENT_IDENTITY") {
		t.Errorf("error should mention DENDRA_AGENT_IDENTITY, got: %v", err)
	}
}

func TestDelegate_EnqueueFailure(t *testing.T) {
	deps, tmpDir := newTestDelegateDeps(t)
	deps.enqueueTask = func(dendraRoot, agentName, prompt string) (*state.Task, error) {
		return nil, errors.New("disk full")
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Status: "active",
		Parent: "root",
	})

	err := runDelegate(deps, "alice", "some task")
	if err == nil {
		t.Fatal("expected error when enqueue fails")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("error should propagate enqueue failure, got: %v", err)
	}
}

func TestDelegate_MessageFailure_NonFatal(t *testing.T) {
	deps, tmpDir := newTestDelegateDeps(t)
	deps.sendMessage = func(dendraRoot, from, to, subject, body string, opts ...messages.SendOption) error {
		return errors.New("message delivery failed")
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Status: "active",
		Parent: "root",
	})

	err := runDelegate(deps, "alice", "build login feature")
	if err != nil {
		t.Fatalf("expected no error (message failure is non-fatal), got: %v", err)
	}

	// Task should still have been enqueued successfully
	tasks, err := state.ListTasks(tmpDir, "alice")
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task despite message failure, got %d", len(tasks))
	}
	if tasks[0].Prompt != "build login feature" {
		t.Errorf("task prompt = %q, want %q", tasks[0].Prompt, "build login feature")
	}
}

func TestDelegate_DoneStatus_CanDelegate(t *testing.T) {
	deps, tmpDir := newTestDelegateDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Status: "done",
		Parent: "root",
	})

	err := runDelegate(deps, "alice", "another task")
	if err != nil {
		t.Fatalf("unexpected error: delegation to 'done' agent should succeed, got: %v", err)
	}

	// Verify task was enqueued
	tasks, err := state.ListTasks(tmpDir, "alice")
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
}

func TestDelegate_ProblemStatus_CanDelegate(t *testing.T) {
	deps, tmpDir := newTestDelegateDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "alice",
		Status: "problem",
		Parent: "root",
	})

	err := runDelegate(deps, "alice", "fix the issue and try again")
	if err != nil {
		t.Fatalf("unexpected error: delegation to 'problem' agent should succeed, got: %v", err)
	}

	// Verify task was enqueued
	tasks, err := state.ListTasks(tmpDir, "alice")
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
}

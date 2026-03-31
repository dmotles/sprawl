package state

import (
	"testing"
)

func TestSaveAndLoadAgent(t *testing.T) {
	dir := t.TempDir()
	agent := &AgentState{
		Name:        "frank",
		Type:        "engineer",
		Family:      "engineering",
		Parent:      "root",
		Prompt:      "implement the login page",
		Branch:      "dendra/frank",
		Worktree:    "/tmp/worktrees/frank",
		TmuxSession: "dendra-root-children",
		TmuxWindow:  "frank",
		Status:      "active",
		CreatedAt:   "2026-03-30T12:00:00Z",
		SessionID:   "dendra-frank",
		Subagent:    true,
	}

	if err := SaveAgent(dir, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	loaded, err := LoadAgent(dir, "frank")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}

	if loaded.Name != agent.Name {
		t.Errorf("Name = %q, want %q", loaded.Name, agent.Name)
	}
	if loaded.Type != agent.Type {
		t.Errorf("Type = %q, want %q", loaded.Type, agent.Type)
	}
	if loaded.Family != agent.Family {
		t.Errorf("Family = %q, want %q", loaded.Family, agent.Family)
	}
	if loaded.Parent != agent.Parent {
		t.Errorf("Parent = %q, want %q", loaded.Parent, agent.Parent)
	}
	if loaded.Prompt != agent.Prompt {
		t.Errorf("Prompt = %q, want %q", loaded.Prompt, agent.Prompt)
	}
	if loaded.Branch != agent.Branch {
		t.Errorf("Branch = %q, want %q", loaded.Branch, agent.Branch)
	}
	if loaded.Worktree != agent.Worktree {
		t.Errorf("Worktree = %q, want %q", loaded.Worktree, agent.Worktree)
	}
	if loaded.TmuxSession != agent.TmuxSession {
		t.Errorf("TmuxSession = %q, want %q", loaded.TmuxSession, agent.TmuxSession)
	}
	if loaded.TmuxWindow != agent.TmuxWindow {
		t.Errorf("TmuxWindow = %q, want %q", loaded.TmuxWindow, agent.TmuxWindow)
	}
	if loaded.Status != agent.Status {
		t.Errorf("Status = %q, want %q", loaded.Status, agent.Status)
	}
	if loaded.CreatedAt != agent.CreatedAt {
		t.Errorf("CreatedAt = %q, want %q", loaded.CreatedAt, agent.CreatedAt)
	}
	if loaded.SessionID != agent.SessionID {
		t.Errorf("SessionID = %q, want %q", loaded.SessionID, agent.SessionID)
	}
	if loaded.Subagent != agent.Subagent {
		t.Errorf("Subagent = %v, want %v", loaded.Subagent, agent.Subagent)
	}
}

func TestSaveAndLoadAgent_OmitemptyDefaults(t *testing.T) {
	dir := t.TempDir()
	agent := &AgentState{
		Name:   "bob",
		Type:   "engineer",
		Status: "active",
	}

	if err := SaveAgent(dir, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	loaded, err := LoadAgent(dir, "bob")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}

	if loaded.SessionID != "" {
		t.Errorf("SessionID = %q, want empty string", loaded.SessionID)
	}
	if loaded.Subagent != false {
		t.Errorf("Subagent = %v, want false", loaded.Subagent)
	}
}

func TestLoadAgent_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadAgent(dir, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent, got nil")
	}
}

func TestListAgents_Empty(t *testing.T) {
	dir := t.TempDir()
	agents, err := ListAgents(dir)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func TestListAgents_Multiple(t *testing.T) {
	dir := t.TempDir()
	names := []string{"alice", "bob", "carol"}
	for _, name := range names {
		agent := &AgentState{Name: name, Type: "engineer", Status: "active"}
		if err := SaveAgent(dir, agent); err != nil {
			t.Fatalf("SaveAgent(%q): %v", name, err)
		}
	}

	agents, err := ListAgents(dir)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(agents))
	}
}

func TestUsedNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alice", "bob"} {
		agent := &AgentState{Name: name, Type: "engineer", Status: "active"}
		if err := SaveAgent(dir, agent); err != nil {
			t.Fatalf("SaveAgent(%q): %v", name, err)
		}
	}

	used, err := UsedNames(dir)
	if err != nil {
		t.Fatalf("UsedNames: %v", err)
	}
	if !used["alice"] {
		t.Error("expected alice to be used")
	}
	if !used["bob"] {
		t.Error("expected bob to be used")
	}
	if used["carol"] {
		t.Error("expected carol to not be used")
	}
}

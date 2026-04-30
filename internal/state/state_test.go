package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAndLoadAgent(t *testing.T) {
	dir := t.TempDir()
	agent := &AgentState{
		Name:      "frank",
		Type:      "engineer",
		Family:    "engineering",
		Parent:    "root",
		Prompt:    "implement the login page",
		Branch:    "sprawl/frank",
		Worktree:  "/tmp/worktrees/frank",
		Status:    "active",
		CreatedAt: "2026-03-30T12:00:00Z",
		SessionID: "sprawl-frank",
		Subagent:  true,
		TreePath:  "weave├frank",
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
	if loaded.TreePath != agent.TreePath {
		t.Errorf("TreePath = %q, want %q", loaded.TreePath, agent.TreePath)
	}

	raw, err := os.ReadFile(filepath.Join(AgentsDir(dir), "frank.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), "\"tmux_session\"") || strings.Contains(string(raw), "\"tmux_window\"") {
		t.Fatalf("agent state JSON should not persist tmux fields:\n%s", raw)
	}
}

func TestWriteAndReadNamespace(t *testing.T) {
	dir := t.TempDir()

	// Before writing, should return empty
	if ns := ReadNamespace(dir); ns != "" {
		t.Errorf("ReadNamespace before write = %q, want empty", ns)
	}

	if err := WriteNamespace(dir, "🌳"); err != nil {
		t.Fatalf("WriteNamespace: %v", err)
	}

	ns := ReadNamespace(dir)
	if ns != "🌳" {
		t.Errorf("ReadNamespace = %q, want %q", ns, "🌳")
	}

	// Overwrite
	if err := WriteNamespace(dir, "🌲"); err != nil {
		t.Fatalf("WriteNamespace overwrite: %v", err)
	}
	ns = ReadNamespace(dir)
	if ns != "🌲" {
		t.Errorf("ReadNamespace after overwrite = %q, want %q", ns, "🌲")
	}
}

func TestWriteAndReadRootName(t *testing.T) {
	dir := t.TempDir()

	// Before writing, should return empty
	if rn := ReadRootName(dir); rn != "" {
		t.Errorf("ReadRootName before write = %q, want empty", rn)
	}

	if err := WriteRootName(dir, "weave"); err != nil {
		t.Fatalf("WriteRootName: %v", err)
	}

	rn := ReadRootName(dir)
	if rn != "weave" {
		t.Errorf("ReadRootName = %q, want %q", rn, "weave")
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

func TestSaveAndLoadAgent_CostFields(t *testing.T) {
	dir := t.TempDir()
	agent := &AgentState{
		Name:             "alice",
		Type:             "engineer",
		Status:           "active",
		TotalCostUsd:     0.0523,
		LastCostUpdateAt: "2026-04-30T12:00:00Z",
	}

	if err := SaveAgent(dir, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	loaded, err := LoadAgent(dir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}

	if loaded.TotalCostUsd != 0.0523 {
		t.Errorf("TotalCostUsd = %f, want 0.0523", loaded.TotalCostUsd)
	}
	if loaded.LastCostUpdateAt != "2026-04-30T12:00:00Z" {
		t.Errorf("LastCostUpdateAt = %q, want %q", loaded.LastCostUpdateAt, "2026-04-30T12:00:00Z")
	}
}

func TestSaveAndLoadAgent_CostFieldsOmittedWhenZero(t *testing.T) {
	dir := t.TempDir()
	agent := &AgentState{
		Name:   "bob",
		Type:   "engineer",
		Status: "active",
	}

	if err := SaveAgent(dir, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(AgentsDir(dir), "bob.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), "total_cost_usd") {
		t.Errorf("agent state JSON should not contain total_cost_usd when zero:\n%s", raw)
	}
	if strings.Contains(string(raw), "last_cost_update_at") {
		t.Errorf("agent state JSON should not contain last_cost_update_at when empty:\n%s", raw)
	}
}

func TestWriteAndReadAccentColor(t *testing.T) {
	dir := t.TempDir()

	// Before writing, should return empty
	if c := ReadAccentColor(dir); c != "" {
		t.Errorf("ReadAccentColor before write = %q, want empty", c)
	}

	if err := WriteAccentColor(dir, "colour39"); err != nil {
		t.Fatalf("WriteAccentColor: %v", err)
	}

	c := ReadAccentColor(dir)
	if c != "colour39" {
		t.Errorf("ReadAccentColor = %q, want %q", c, "colour39")
	}

	// Overwrite
	if err := WriteAccentColor(dir, "colour198"); err != nil {
		t.Fatalf("WriteAccentColor overwrite: %v", err)
	}
	c = ReadAccentColor(dir)
	if c != "colour198" {
		t.Errorf("ReadAccentColor after overwrite = %q, want %q", c, "colour198")
	}
}

func TestWriteAndReadVersion(t *testing.T) {
	dir := t.TempDir()

	// Before writing, should return empty
	if v := ReadVersion(dir); v != "" {
		t.Errorf("ReadVersion before write = %q, want empty", v)
	}

	if err := WriteVersion(dir, "0.1.3"); err != nil {
		t.Fatalf("WriteVersion: %v", err)
	}

	v := ReadVersion(dir)
	if v != "0.1.3" {
		t.Errorf("ReadVersion = %q, want %q", v, "0.1.3")
	}
}

func TestWriteSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	content := "You are a helpful agent.\nDo good work."

	path, err := WriteSystemPrompt(dir, "finn", content)
	if err != nil {
		t.Fatalf("WriteSystemPrompt: %v", err)
	}

	expected := filepath.Join(dir, ".sprawl", "agents", "finn", "SYSTEM.md")
	if path != expected {
		t.Errorf("path = %q, want %q", path, expected)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(data) != content {
		t.Errorf("content = %q, want %q", string(data), content)
	}
}

// TestDeleteAgent_RemovesDirectory pins the contract that DeleteAgent fully
// cleans up an agent's footprint: both the <name>.json state file AND the
// <name>/ directory under .sprawl/agents/. Without removing the directory,
// AllocateName can leak names (QUM-404).
func TestDeleteAgent_RemovesDirectory(t *testing.T) {
	t.Run("removes both json and directory", func(t *testing.T) {
		dir := t.TempDir()
		agent := &AgentState{
			Name:   "foo",
			Type:   "engineer",
			Status: "active",
		}
		if err := SaveAgent(dir, agent); err != nil {
			t.Fatalf("SaveAgent: %v", err)
		}
		if _, err := WriteSystemPrompt(dir, "foo", "system prompt body"); err != nil {
			t.Fatalf("WriteSystemPrompt: %v", err)
		}

		jsonPath := filepath.Join(AgentsDir(dir), "foo.json")
		dirPath := filepath.Join(AgentsDir(dir), "foo")

		// Sanity: both should exist before delete
		if _, err := os.Stat(jsonPath); err != nil {
			t.Fatalf("expected json file to exist before delete: %v", err)
		}
		if _, err := os.Stat(dirPath); err != nil {
			t.Fatalf("expected agent directory to exist before delete: %v", err)
		}

		if err := DeleteAgent(dir, "foo"); err != nil {
			t.Fatalf("DeleteAgent: %v", err)
		}

		if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
			t.Errorf("expected json file to be removed, stat err = %v", err)
		}
		if _, err := os.Stat(dirPath); !os.IsNotExist(err) {
			t.Errorf("expected agent directory to be removed, stat err = %v", err)
		}
	})

	t.Run("dir without json", func(t *testing.T) {
		dir := t.TempDir()
		dirPath := filepath.Join(AgentsDir(dir), "orphan")
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		if err := DeleteAgent(dir, "orphan"); err != nil {
			t.Errorf("DeleteAgent should succeed when only directory exists, got: %v", err)
		}
		if _, err := os.Stat(dirPath); !os.IsNotExist(err) {
			t.Errorf("expected orphan directory to be removed, stat err = %v", err)
		}
	})
}

func TestWriteSystemPrompt_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()

	path, err := WriteSystemPrompt(dir, "newagent", "prompt")
	if err != nil {
		t.Fatalf("WriteSystemPrompt: %v", err)
	}

	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected agent directory to be created")
	}
}

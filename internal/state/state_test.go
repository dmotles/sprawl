package state

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestStatusConstants_Values pins the string values of the Status* constants
// introduced for QUM-372. Other packages reference these by name, but the
// values must remain stable: agent state JSON files on disk record the raw
// string, and existing tools (and existing on-disk files) use the lowercase
// form.
func TestStatusConstants_Values(t *testing.T) {
	cases := map[string]string{
		"StatusActive":       StatusActive,
		"StatusRunning":      StatusRunning,
		"StatusSuspended":    StatusSuspended,
		"StatusKilled":       StatusKilled,
		"StatusRetired":      StatusRetired,
		"StatusRetiring":     StatusRetiring,
		"StatusDone":         StatusDone,
		"StatusResumeFailed": StatusResumeFailed,
		"StatusFaulted":      StatusFaulted,
		"StatusStopped":      StatusStopped,
	}
	wants := map[string]string{
		"StatusActive":       "active",
		"StatusRunning":      "running",
		"StatusSuspended":    "suspended",
		"StatusKilled":       "killed",
		"StatusRetired":      "retired",
		"StatusRetiring":     "retiring",
		"StatusDone":         "done",
		"StatusResumeFailed": "resume_failed",
		"StatusFaulted":      "faulted",
		"StatusStopped":      "stopped",
	}
	for name, got := range cases {
		if got != wants[name] {
			t.Errorf("%s = %q, want %q", name, got, wants[name])
		}
	}
}

// TestSaveAgent_AtomicRename_NoPartialFile guards QUM-372 step 1: SaveAgent
// must write via a `<name>.json.tmp` then `os.Rename` to the final path. A
// pre-existing junk tmp file must not leak into the final state, and the
// final file must contain valid JSON. If SaveAgent uses a direct
// os.WriteFile, an interrupted (or partially-written) prior call could leave
// truncated bytes at the final path.
func TestSaveAgent_AtomicRename_NoPartialFile(t *testing.T) {
	dir := t.TempDir()

	agentsDir := AgentsDir(dir)
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir agents dir: %v", err)
	}
	tmpPath := filepath.Join(agentsDir, "frank.json.tmp")
	if err := os.WriteFile(tmpPath, []byte("garbage{{not json"), 0o644); err != nil {
		t.Fatalf("seed junk tmp file: %v", err)
	}

	agent := &AgentState{
		Name:   "frank",
		Type:   "engineer",
		Status: StatusActive,
	}
	if err := SaveAgent(dir, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	finalPath := filepath.Join(agentsDir, "frank.json")
	data, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		t.Errorf("final state file does not look like JSON:\n%s", data)
	}
	loaded, err := LoadAgent(dir, "frank")
	if err != nil {
		t.Fatalf("LoadAgent after atomic save: %v", err)
	}
	if loaded.Name != "frank" || loaded.Status != StatusActive {
		t.Errorf("loaded = %+v, want frank/active", loaded)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("tmp file should be removed after rename, stat err = %v", err)
	}
}

// TestSaveAgent_ConcurrentWriters_NoCorruption hammers SaveAgent from many
// goroutines updating the same agent name with distinct status strings. The
// atomic-rename contract guarantees the final file always parses cleanly to
// one of the written status values — never a truncated read or a parse error.
func TestSaveAgent_ConcurrentWriters_NoCorruption(t *testing.T) {
	dir := t.TempDir()

	const writers = 16
	statuses := []string{
		StatusActive, StatusRunning, StatusSuspended,
		StatusKilled, StatusRetired, StatusRetiring,
		StatusDone, StatusResumeFailed,
		StatusFaulted, StatusStopped,
	}
	allowed := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		allowed[s] = true
	}

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			st := statuses[i%len(statuses)]
			agent := &AgentState{
				Name:   "racer",
				Type:   "engineer",
				Status: st,
			}
			if err := SaveAgent(dir, agent); err != nil {
				t.Errorf("SaveAgent (writer %d): %v", i, err)
			}
		}()
	}
	wg.Wait()

	loaded, err := LoadAgent(dir, "racer")
	if err != nil {
		t.Fatalf("LoadAgent after concurrent SaveAgent: %v (truncated or partial write?)", err)
	}
	if !allowed[loaded.Status] {
		t.Errorf("loaded.Status = %q, want one of %v", loaded.Status, statuses)
	}
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

// TestAgentState_LegacyCostFieldsTolerated locks in graceful upgrade from old
// state files that carried `total_cost_usd` and `last_cost_update_at` at the
// top level (QUM-368: cost tracking is being moved out of AgentState into
// the per-turn usage NDJSON logs). LoadAgent must continue to parse these
// blobs without error and preserve the other AgentState fields.
func TestAgentState_LegacyCostFieldsTolerated(t *testing.T) {
	dir := t.TempDir()
	agentsDir := AgentsDir(dir)
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Hand-craft a legacy JSON blob with cost fields populated.
	blob := `{
  "name": "legacy",
  "type": "engineer",
  "family": "engineering",
  "parent": "tower",
  "prompt": "do the thing",
  "branch": "dmotles/legacy",
  "worktree": "/tmp/w/legacy",
  "status": "active",
  "created_at": "2026-01-01T00:00:00Z",
  "session_id": "sess-legacy",
  "total_cost_usd": 4.20,
  "last_cost_update_at": "2026-01-02T03:04:05Z",
  "schema_version": 1
}`
	if err := os.WriteFile(filepath.Join(agentsDir, "legacy.json"), []byte(blob), 0o644); err != nil {
		t.Fatalf("write legacy blob: %v", err)
	}

	loaded, err := LoadAgent(dir, "legacy")
	if err != nil {
		t.Fatalf("LoadAgent on legacy blob errored: %v", err)
	}
	if loaded.Name != "legacy" {
		t.Errorf("Name = %q, want legacy", loaded.Name)
	}
	if loaded.Type != "engineer" {
		t.Errorf("Type = %q, want engineer", loaded.Type)
	}
	if loaded.Family != "engineering" {
		t.Errorf("Family = %q, want engineering", loaded.Family)
	}
	if loaded.Parent != "tower" {
		t.Errorf("Parent = %q, want tower", loaded.Parent)
	}
	if loaded.Branch != "dmotles/legacy" {
		t.Errorf("Branch = %q, want dmotles/legacy", loaded.Branch)
	}
	if loaded.SessionID != "sess-legacy" {
		t.Errorf("SessionID = %q, want sess-legacy", loaded.SessionID)
	}
	if loaded.Status != "active" {
		t.Errorf("Status = %q, want active", loaded.Status)
	}
}

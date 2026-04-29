package observe

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor"
)

func TestLoadAll_SynthesizesRoot(t *testing.T) {
	deps := Deps{
		ListAgents: func(string) ([]*state.AgentState, error) { return nil, nil },
		ReadRootName: func(string) string {
			return "weave"
		},
	}

	agents, err := LoadAll(context.Background(), deps, t.TempDir())
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("len(agents) = %d, want 1", len(agents))
	}
	if agents[0].Name != "weave" || !agents[0].IsRoot {
		t.Fatalf("root = %+v, want synthesized weave root", agents[0])
	}
}

func TestLoadAll_StateFallbackIgnoresLegacyTmuxFields(t *testing.T) {
	sprawlRoot := t.TempDir()
	if err := os.MkdirAll(state.AgentsDir(sprawlRoot), 0o755); err != nil {
		t.Fatalf("mkdir agents dir: %v", err)
	}
	raw := `{
  "name": "agent1",
  "status": "active",
  "parent": "weave",
  "tmux_session": "legacy-children",
  "tmux_window": "agent1"
}`
	if err := os.WriteFile(filepath.Join(state.AgentsDir(sprawlRoot), "agent1.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write agent state: %v", err)
	}
	if err := state.WriteRootName(sprawlRoot, "weave"); err != nil {
		t.Fatalf("WriteRootName: %v", err)
	}

	agents, err := LoadAll(context.Background(), Deps{
		ListAgents:   state.ListAgents,
		ReadRootName: state.ReadRootName,
	}, sprawlRoot)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}

	for _, agent := range agents {
		if agent.Name != "agent1" {
			continue
		}
		if agent.ProcessAlive != nil {
			t.Fatalf("ProcessAlive = %+v, want nil", agent.ProcessAlive)
		}
		return
	}
	t.Fatal("agent1 not found")
}

func TestLoadAll_UsesSupervisorStatusProcessAlive(t *testing.T) {
	alive := true
	agents, err := LoadAll(context.Background(), Deps{
		Status: func(context.Context) ([]supervisor.AgentInfo, error) {
			return []supervisor.AgentInfo{{
				Name:         "alice",
				Type:         "engineer",
				Family:       "engineering",
				Parent:       "weave",
				Status:       "active",
				ProcessAlive: &alive,
			}}, nil
		},
		ReadRootName: func(string) string { return "weave" },
	}, t.TempDir())
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}

	for _, agent := range agents {
		if agent.Name != "alice" {
			continue
		}
		if agent.ProcessAlive == nil || !*agent.ProcessAlive {
			t.Fatalf("ProcessAlive = %+v, want true", agent.ProcessAlive)
		}
		return
	}
	t.Fatal("alice not found")
}

func TestBuildTree_SortsChildrenAndCapturesOrphans(t *testing.T) {
	root, orphans := BuildTree([]*AgentInfo{
		{AgentState: state.AgentState{Name: "weave"}, IsRoot: true},
		{AgentState: state.AgentState{Name: "cedar", Parent: "weave"}},
		{AgentState: state.AgentState{Name: "alice", Parent: "weave"}},
		{AgentState: state.AgentState{Name: "lost", Parent: "ghost"}},
	}, "weave")

	if root == nil || root.Agent == nil || root.Agent.Name != "weave" {
		t.Fatalf("root = %+v, want weave", root)
	}
	if len(root.Children) != 2 {
		t.Fatalf("len(root.Children) = %d, want 2", len(root.Children))
	}
	if root.Children[0].Agent.Name != "alice" || root.Children[1].Agent.Name != "cedar" {
		t.Fatalf("children order = [%s %s], want [alice cedar]", root.Children[0].Agent.Name, root.Children[1].Agent.Name)
	}
	if orphans == nil || len(orphans.Children) != 1 || orphans.Children[0].Agent.Name != "lost" {
		t.Fatalf("orphans = %+v, want lost", orphans)
	}
}

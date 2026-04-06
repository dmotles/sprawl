package observe

import (
	"testing"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
)

// mockRunner implements tmux.Runner with configurable HasSession/HasWindow results.
type mockRunner struct {
	hasSessionResults map[string]bool
	hasWindowResults  map[string]bool // key: "session:window"
}

func (m *mockRunner) HasSession(name string) bool {
	if m.hasSessionResults == nil {
		return false
	}
	return m.hasSessionResults[name]
}

func (m *mockRunner) HasWindow(sessionName, windowName string) bool {
	if m.hasWindowResults == nil {
		return false
	}
	return m.hasWindowResults[sessionName+":"+windowName]
}

func (m *mockRunner) NewSession(string, map[string]string, string) error { return nil }
func (m *mockRunner) NewSessionWithWindow(string, string, map[string]string, string) error {
	return nil
}
func (m *mockRunner) NewWindow(string, string, map[string]string, string) error { return nil }
func (m *mockRunner) KillWindow(string, string) error                           { return nil }
func (m *mockRunner) ListWindowPIDs(string, string) ([]int, error)              { return nil, nil }
func (m *mockRunner) ListSessionNames() ([]string, error)                       { return nil, nil }
func (m *mockRunner) SendKeys(string, string, string) error                     { return nil }
func (m *mockRunner) Attach(string) error                                       { return nil }

// Compile-time check that mockRunner satisfies tmux.Runner.
var _ tmux.Runner = (*mockRunner)(nil)

// ---------------------------------------------------------------------------
// LoadAll tests
// ---------------------------------------------------------------------------

func TestLoadAll_SynthesizesRoot(t *testing.T) {
	deps := Deps{
		TmuxRunner: &mockRunner{},
		ListAgents: func(string) ([]*state.AgentState, error) {
			return nil, nil
		},
		ReadRootName: func(string) string {
			return "neo"
		},
		ReadNamespace: func(string) string {
			return "test"
		},
	}

	agents, err := LoadAll(deps, "/fake")
	if err != nil {
		t.Fatalf("LoadAll returned error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (synthesized root), got %d", len(agents))
	}
	if agents[0].Name != "neo" {
		t.Errorf("expected root name %q, got %q", "neo", agents[0].Name)
	}
	if !agents[0].IsRoot {
		t.Errorf("expected root agent to have IsRoot=true")
	}
}

func TestLoadAll_NoRootName(t *testing.T) {
	deps := Deps{
		TmuxRunner: &mockRunner{},
		ListAgents: func(string) ([]*state.AgentState, error) {
			return nil, nil
		},
		ReadRootName: func(string) string {
			return ""
		},
		ReadNamespace: func(string) string {
			return "test"
		},
	}

	agents, err := LoadAll(deps, "/fake")
	if err != nil {
		t.Fatalf("LoadAll returned error: %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("expected 0 agents when root name is empty, got %d", len(agents))
	}
}

func TestLoadAll_LivenessActiveAlive(t *testing.T) {
	deps := Deps{
		TmuxRunner: &mockRunner{
			hasWindowResults: map[string]bool{
				"childsession:childwindow": true,
			},
		},
		ListAgents: func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{
					Name:        "agent1",
					Status:      "active",
					TmuxSession: "childsession",
					TmuxWindow:  "childwindow",
					Parent:      "neo",
				},
			}, nil
		},
		ReadRootName: func(string) string {
			return "neo"
		},
		ReadNamespace: func(string) string {
			return "test"
		},
	}

	agents, err := LoadAll(deps, "/fake")
	if err != nil {
		t.Fatalf("LoadAll returned error: %v", err)
	}

	var found *AgentInfo
	for _, a := range agents {
		if a.Name == "agent1" {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatalf("agent1 not found in results")
	}
	if found.ProcessAlive == nil {
		t.Fatalf("expected ProcessAlive to be non-nil for active agent")
	}
	if *found.ProcessAlive != true {
		t.Errorf("expected ProcessAlive=true, got false")
	}
}

func TestLoadAll_LivenessActiveDead(t *testing.T) {
	deps := Deps{
		TmuxRunner: &mockRunner{
			hasWindowResults: map[string]bool{
				"childsession:childwindow": false,
			},
		},
		ListAgents: func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{
					Name:        "agent1",
					Status:      "active",
					TmuxSession: "childsession",
					TmuxWindow:  "childwindow",
					Parent:      "neo",
				},
			}, nil
		},
		ReadRootName: func(string) string {
			return "neo"
		},
		ReadNamespace: func(string) string {
			return "test"
		},
	}

	agents, err := LoadAll(deps, "/fake")
	if err != nil {
		t.Fatalf("LoadAll returned error: %v", err)
	}

	var found *AgentInfo
	for _, a := range agents {
		if a.Name == "agent1" {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatalf("agent1 not found in results")
	}
	if found.ProcessAlive == nil {
		t.Fatalf("expected ProcessAlive to be non-nil for active agent")
	}
	if *found.ProcessAlive != false {
		t.Errorf("expected ProcessAlive=false, got true")
	}
}

func TestLoadAll_LivenessTerminalDone(t *testing.T) {
	deps := Deps{
		TmuxRunner: &mockRunner{},
		ListAgents: func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "agent1", Status: "done", Parent: "neo"},
			}, nil
		},
		ReadRootName: func(string) string {
			return "neo"
		},
		ReadNamespace: func(string) string {
			return "test"
		},
	}

	agents, err := LoadAll(deps, "/fake")
	if err != nil {
		t.Fatalf("LoadAll returned error: %v", err)
	}

	var found *AgentInfo
	for _, a := range agents {
		if a.Name == "agent1" {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatalf("agent1 not found in results")
	}
	if found.ProcessAlive != nil {
		t.Errorf("expected ProcessAlive=nil for done agent, got %v", *found.ProcessAlive)
	}
}

func TestLoadAll_LivenessTerminalProblem(t *testing.T) {
	deps := Deps{
		TmuxRunner: &mockRunner{},
		ListAgents: func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "agent1", Status: "problem", Parent: "neo"},
			}, nil
		},
		ReadRootName: func(string) string {
			return "neo"
		},
		ReadNamespace: func(string) string {
			return "test"
		},
	}

	agents, err := LoadAll(deps, "/fake")
	if err != nil {
		t.Fatalf("LoadAll returned error: %v", err)
	}

	var found *AgentInfo
	for _, a := range agents {
		if a.Name == "agent1" {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatalf("agent1 not found in results")
	}
	if found.ProcessAlive != nil {
		t.Errorf("expected ProcessAlive=nil for problem agent, got %v", *found.ProcessAlive)
	}
}

func TestLoadAll_LivenessTerminalRetiring(t *testing.T) {
	deps := Deps{
		TmuxRunner: &mockRunner{},
		ListAgents: func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "agent1", Status: "retiring", Parent: "neo"},
			}, nil
		},
		ReadRootName: func(string) string {
			return "neo"
		},
		ReadNamespace: func(string) string {
			return "test"
		},
	}

	agents, err := LoadAll(deps, "/fake")
	if err != nil {
		t.Fatalf("LoadAll returned error: %v", err)
	}

	var found *AgentInfo
	for _, a := range agents {
		if a.Name == "agent1" {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatalf("agent1 not found in results")
	}
	if found.ProcessAlive != nil {
		t.Errorf("expected ProcessAlive=nil for retiring agent, got %v", *found.ProcessAlive)
	}
}

func TestLoadAll_LivenessNoTmux(t *testing.T) {
	deps := Deps{
		TmuxRunner: nil,
		ListAgents: func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "agent1", Status: "active", Parent: "neo", TmuxSession: "s", TmuxWindow: "w"},
			}, nil
		},
		ReadRootName: func(string) string {
			return "neo"
		},
		ReadNamespace: func(string) string {
			return "test"
		},
	}

	agents, err := LoadAll(deps, "/fake")
	if err != nil {
		t.Fatalf("LoadAll returned error: %v", err)
	}

	var found *AgentInfo
	for _, a := range agents {
		if a.Name == "agent1" {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatalf("agent1 not found in results")
	}
	if found.ProcessAlive != nil {
		t.Errorf("expected ProcessAlive=nil when TmuxRunner is nil, got %v", *found.ProcessAlive)
	}
}

func TestLoadAll_RootLiveness(t *testing.T) {
	rootSession := tmux.RootSessionName("test", "neo")
	deps := Deps{
		TmuxRunner: &mockRunner{
			hasSessionResults: map[string]bool{
				rootSession: true,
			},
		},
		ListAgents: func(string) ([]*state.AgentState, error) {
			return nil, nil
		},
		ReadRootName: func(string) string {
			return "neo"
		},
		ReadNamespace: func(string) string {
			return "test"
		},
	}

	agents, err := LoadAll(deps, "/fake")
	if err != nil {
		t.Fatalf("LoadAll returned error: %v", err)
	}

	var root *AgentInfo
	for _, a := range agents {
		if a.IsRoot {
			root = a
			break
		}
	}
	if root == nil {
		t.Fatalf("root agent not found")
	}
	if root.ProcessAlive == nil {
		t.Fatalf("expected root ProcessAlive to be non-nil")
	}
	if *root.ProcessAlive != true {
		t.Errorf("expected root ProcessAlive=true, got false")
	}
}

func TestLoadAll_RootLivenessDead(t *testing.T) {
	rootSession := tmux.RootSessionName("test", "neo")
	deps := Deps{
		TmuxRunner: &mockRunner{
			hasSessionResults: map[string]bool{
				rootSession: false,
			},
		},
		ListAgents: func(string) ([]*state.AgentState, error) {
			return nil, nil
		},
		ReadRootName: func(string) string {
			return "neo"
		},
		ReadNamespace: func(string) string {
			return "test"
		},
	}

	agents, err := LoadAll(deps, "/fake")
	if err != nil {
		t.Fatalf("LoadAll returned error: %v", err)
	}

	var root *AgentInfo
	for _, a := range agents {
		if a.IsRoot {
			root = a
			break
		}
	}
	if root == nil {
		t.Fatalf("root agent not found")
	}
	if root.ProcessAlive == nil {
		t.Fatalf("expected root ProcessAlive to be non-nil")
	}
	if *root.ProcessAlive != false {
		t.Errorf("expected root ProcessAlive=false, got true")
	}
}

func TestLoadAll_SortedByName(t *testing.T) {
	deps := Deps{
		TmuxRunner: &mockRunner{},
		ListAgents: func(string) ([]*state.AgentState, error) {
			return []*state.AgentState{
				{Name: "cherry", Status: "active", Parent: "neo"},
				{Name: "apple", Status: "active", Parent: "neo"},
				{Name: "banana", Status: "active", Parent: "neo"},
			}, nil
		},
		ReadRootName: func(string) string {
			return "neo"
		},
		ReadNamespace: func(string) string {
			return "test"
		},
	}

	agents, err := LoadAll(deps, "/fake")
	if err != nil {
		t.Fatalf("LoadAll returned error: %v", err)
	}

	// Expect root ("neo") first (sorted), then apple, banana, cherry.
	expectedOrder := []string{"apple", "banana", "cherry", "neo"}
	if len(agents) != len(expectedOrder) {
		t.Fatalf("expected %d agents, got %d", len(expectedOrder), len(agents))
	}
	for i, name := range expectedOrder {
		if agents[i].Name != name {
			t.Errorf("agents[%d].Name = %q, want %q", i, agents[i].Name, name)
		}
	}
}

// ---------------------------------------------------------------------------
// BuildTree tests
// ---------------------------------------------------------------------------

func TestBuildTree_SingleRoot(t *testing.T) {
	agents := []*AgentInfo{
		{AgentState: state.AgentState{Name: "neo"}, IsRoot: true},
	}

	root, orphans := BuildTree(agents, "neo")
	if root == nil {
		t.Fatalf("expected root node, got nil")
	}
	if root.Agent.Name != "neo" {
		t.Errorf("root name = %q, want %q", root.Agent.Name, "neo")
	}
	if len(root.Children) != 0 {
		t.Errorf("expected 0 children, got %d", len(root.Children))
	}
	if orphans != nil {
		t.Errorf("expected no orphans, got %v", orphans)
	}
}

func TestBuildTree_SimpleHierarchy(t *testing.T) {
	agents := []*AgentInfo{
		{AgentState: state.AgentState{Name: "neo"}, IsRoot: true},
		{AgentState: state.AgentState{Name: "bravo", Parent: "neo"}},
		{AgentState: state.AgentState{Name: "alpha", Parent: "neo"}},
	}

	root, orphans := BuildTree(agents, "neo")
	if root == nil {
		t.Fatalf("expected root node, got nil")
	}
	if len(root.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(root.Children))
	}
	if root.Children[0].Agent.Name != "alpha" {
		t.Errorf("first child = %q, want %q", root.Children[0].Agent.Name, "alpha")
	}
	if root.Children[1].Agent.Name != "bravo" {
		t.Errorf("second child = %q, want %q", root.Children[1].Agent.Name, "bravo")
	}
	if orphans != nil {
		t.Errorf("expected no orphans, got %v", orphans)
	}
}

func TestBuildTree_DeepHierarchy(t *testing.T) {
	agents := []*AgentInfo{
		{AgentState: state.AgentState{Name: "neo"}, IsRoot: true},
		{AgentState: state.AgentState{Name: "child", Parent: "neo"}},
		{AgentState: state.AgentState{Name: "grandchild", Parent: "child"}},
	}

	root, orphans := BuildTree(agents, "neo")
	if root == nil {
		t.Fatalf("expected root node, got nil")
	}
	if len(root.Children) != 1 {
		t.Fatalf("expected 1 child of root, got %d", len(root.Children))
	}
	child := root.Children[0]
	if child.Agent.Name != "child" {
		t.Errorf("child name = %q, want %q", child.Agent.Name, "child")
	}
	if len(child.Children) != 1 {
		t.Fatalf("expected 1 grandchild, got %d", len(child.Children))
	}
	grandchild := child.Children[0]
	if grandchild.Agent.Name != "grandchild" {
		t.Errorf("grandchild name = %q, want %q", grandchild.Agent.Name, "grandchild")
	}
	if orphans != nil {
		t.Errorf("expected no orphans, got %v", orphans)
	}
}

func TestBuildTree_Orphans(t *testing.T) {
	agents := []*AgentInfo{
		{AgentState: state.AgentState{Name: "neo"}, IsRoot: true},
		{AgentState: state.AgentState{Name: "lost", Parent: "nonexistent"}},
	}

	root, orphans := BuildTree(agents, "neo")
	if root == nil {
		t.Fatalf("expected root node, got nil")
	}
	if len(root.Children) != 0 {
		t.Errorf("expected 0 children on root, got %d", len(root.Children))
	}
	if orphans == nil {
		t.Fatalf("expected orphans node, got nil")
	}
	if len(orphans.Children) != 1 {
		t.Fatalf("expected 1 orphan, got %d", len(orphans.Children))
	}
	if orphans.Children[0].Agent.Name != "lost" {
		t.Errorf("orphan name = %q, want %q", orphans.Children[0].Agent.Name, "lost")
	}
}

func TestBuildTree_NoOrphans(t *testing.T) {
	agents := []*AgentInfo{
		{AgentState: state.AgentState{Name: "neo"}, IsRoot: true},
		{AgentState: state.AgentState{Name: "child", Parent: "neo"}},
	}

	_, orphans := BuildTree(agents, "neo")
	if orphans != nil {
		t.Errorf("expected no orphans, got orphans with %d children", len(orphans.Children))
	}
}

func TestBuildTree_ChildrenSortedByName(t *testing.T) {
	agents := []*AgentInfo{
		{AgentState: state.AgentState{Name: "neo"}, IsRoot: true},
		{AgentState: state.AgentState{Name: "charlie", Parent: "neo"}},
		{AgentState: state.AgentState{Name: "alice", Parent: "neo"}},
		{AgentState: state.AgentState{Name: "bob", Parent: "neo"}},
	}

	root, _ := BuildTree(agents, "neo")
	if root == nil {
		t.Fatalf("expected root node, got nil")
	}
	if len(root.Children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(root.Children))
	}
	expected := []string{"alice", "bob", "charlie"}
	for i, name := range expected {
		if root.Children[i].Agent.Name != name {
			t.Errorf("children[%d].Name = %q, want %q", i, root.Children[i].Agent.Name, name)
		}
	}
}

func TestBuildTree_EmptyList(t *testing.T) {
	root, orphans := BuildTree(nil, "neo")
	if root != nil {
		t.Errorf("expected nil root for empty list, got %v", root)
	}
	if orphans != nil {
		t.Errorf("expected nil orphans for empty list, got %v", orphans)
	}
}

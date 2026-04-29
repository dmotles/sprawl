package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/observe"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/tmux"
)

// treeMockRunner implements tmux.Runner for tree tests.
type treeMockRunner struct {
	sessions map[string]bool
	windows  map[string]bool // key = "session:window"
}

func (m *treeMockRunner) HasSession(name string) bool {
	return m.sessions[name]
}

func (m *treeMockRunner) HasWindow(sessionName, windowName string) bool {
	return m.windows[sessionName+":"+windowName]
}

func (m *treeMockRunner) NewSession(string, map[string]string, string) error { return nil }
func (m *treeMockRunner) NewSessionWithWindow(string, string, map[string]string, string) error {
	return nil
}
func (m *treeMockRunner) NewWindow(string, string, map[string]string, string) error { return nil }
func (m *treeMockRunner) KillWindow(string, string) error                           { return nil }
func (m *treeMockRunner) ListWindowPIDs(string, string) ([]int, error)              { return nil, nil }
func (m *treeMockRunner) ListSessionNames() ([]string, error)                       { return nil, nil }
func (m *treeMockRunner) SendKeys(string, string, string) error                     { return nil }
func (m *treeMockRunner) Attach(string) error                                       { return nil }
func (m *treeMockRunner) SourceFile(string, string) error                           { return nil }
func (m *treeMockRunner) SetEnvironment(string, string, string) error               { return nil }

func newTestTreeDeps(t *testing.T, agents []*state.AgentState, rootName, namespace string) *treeDeps {
	t.Helper()
	runner := &treeMockRunner{
		sessions: map[string]bool{
			tmux.RootSessionName(namespace): true,
		},
		windows: make(map[string]bool),
	}
	// Mark all active agents' windows as alive.
	for _, a := range agents {
		if a.TmuxSession != "" && a.TmuxWindow != "" {
			runner.windows[a.TmuxSession+":"+a.TmuxWindow] = true
		}
	}

	return &treeDeps{
		observeDeps: observe.Deps{
			TmuxRunner: runner,
			ListAgents: func(sprawlRoot string) ([]*state.AgentState, error) {
				return agents, nil
			},
			ReadRootName: func(sprawlRoot string) string {
				return rootName
			},
			ReadNamespace: func(sprawlRoot string) string {
				return namespace
			},
		},
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return "/fake/sprawl/root"
			}
			return ""
		},
	}
}

func newStatusBackedTreeDeps(t *testing.T, agents []supervisor.AgentInfo, rootName string) *treeDeps {
	t.Helper()
	return &treeDeps{
		listAgents: func(context.Context) ([]supervisor.AgentInfo, error) {
			return agents, nil
		},
		readRootName: func(string) string { return rootName },
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return "/fake/sprawl/root"
			}
			return ""
		},
	}
}

func TestTree_MissingSprawlRoot(t *testing.T) {
	deps := &treeDeps{
		getenv: func(string) string { return "" },
	}
	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err == nil {
		t.Fatal("expected error for missing SPRAWL_ROOT")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should mention SPRAWL_ROOT, got: %v", err)
	}
}

func TestTree_EmptyTree(t *testing.T) {
	deps := newTestTreeDeps(t, nil, "", tmux.DefaultNamespace)
	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	// With no agents and no root name, output should be empty or contain a message.
	if out != "" && !strings.Contains(out, "No agent tree found") {
		t.Errorf("expected empty output or 'No agent tree found', got: %q", out)
	}
}

func TestTree_RootOnly(t *testing.T) {
	deps := newTestTreeDeps(t, nil, "weave", tmux.DefaultNamespace)
	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	expected := "weave (root, active, alive)\n"
	if out != expected {
		t.Errorf("output mismatch\ngot:  %q\nwant: %q", out, expected)
	}
}

func TestTree_RootWithChildren(t *testing.T) {
	childSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, "weave")
	agents := []*state.AgentState{
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "finn"},
		{Name: "ratz", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "ratz"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)

	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()

	// Expect box-drawing tree with children sorted alphabetically.
	if !strings.Contains(out, "weave") {
		t.Errorf("output should contain root 'weave', got:\n%s", out)
	}
	if !strings.Contains(out, "├── finn") {
		t.Errorf("output should contain '├── finn', got:\n%s", out)
	}
	if !strings.Contains(out, "└── ratz") {
		t.Errorf("output should contain '└── ratz', got:\n%s", out)
	}
}

func TestTree_DeepNesting(t *testing.T) {
	childSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, "weave")
	grandchildSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, "weave├finn")
	agents := []*state.AgentState{
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "finn", TreePath: "weave├finn"},
		{Name: "bud", Type: "engineer", Family: "engineering", Parent: "finn", Status: "active", TmuxSession: grandchildSession, TmuxWindow: "bud", TreePath: "weave├finn├bud"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)

	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()

	// 3 levels: root → finn → bud. Verify prefix propagation with │.
	if !strings.Contains(out, "weave") {
		t.Errorf("output should contain 'weave', got:\n%s", out)
	}
	if !strings.Contains(out, "finn") {
		t.Errorf("output should contain 'finn', got:\n%s", out)
	}
	if !strings.Contains(out, "bud") {
		t.Errorf("output should contain 'bud', got:\n%s", out)
	}
	// The grandchild line should be indented with │ continuation.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	foundPipe := false
	for _, line := range lines {
		if strings.Contains(line, "bud") && strings.Contains(line, "│") {
			foundPipe = true
			break
		}
	}
	// If finn is the last (only) child of weave, the prefix for bud would use spaces, not │.
	// But if finn is the only child, └── is used, and bud's prefix has spaces.
	// Either way, bud should be further indented than finn.
	budLine := ""
	finnLine := ""
	for _, line := range lines {
		if strings.Contains(line, "bud") {
			budLine = line
		}
		if strings.Contains(line, "finn") {
			finnLine = line
		}
	}
	if budLine == "" {
		t.Fatalf("bud line not found in output:\n%s", out)
	}
	if finnLine == "" {
		t.Fatalf("finn line not found in output:\n%s", out)
	}
	_ = foundPipe
	// Verify bud is indented deeper than finn.
	budIndent := len(budLine) - len(strings.TrimLeft(budLine, " │├└─"))
	finnIndent := len(finnLine) - len(strings.TrimLeft(finnLine, " │├└─"))
	if budIndent <= finnIndent {
		t.Errorf("bud should be indented deeper than finn.\nfinn: %q\nbud: %q", finnLine, budLine)
	}
}

func TestTree_Orphans(t *testing.T) {
	childSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, "weave")
	agents := []*state.AgentState{
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "finn"},
		{Name: "zone", Type: "engineer", Family: "engineering", Parent: "ghost", Status: "active", TmuxSession: childSession, TmuxWindow: "zone"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)

	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "orphan") {
		t.Errorf("output should contain orphan section, got:\n%s", out)
	}
	if !strings.Contains(out, "zone") {
		t.Errorf("output should contain orphan agent 'zone', got:\n%s", out)
	}
}

func TestTree_TerminalOmitsLiveness(t *testing.T) {
	childSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, "weave")
	agents := []*state.AgentState{
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "done", TmuxSession: childSession, TmuxWindow: "finn"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)

	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()

	// Find the finn line — it should NOT contain "alive" or "DEAD".
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "finn") {
			if strings.Contains(line, "alive") || strings.Contains(line, "DEAD") {
				t.Errorf("terminal agent should not show liveness, got line: %q", line)
			}
			if !strings.Contains(line, "done") {
				t.Errorf("terminal agent should show 'done' status, got line: %q", line)
			}
			break
		}
	}
}

func TestTree_DeadAgent(t *testing.T) {
	childSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, "weave")
	agents := []*state.AgentState{
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "finn"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)
	// Mark finn's window as NOT alive.
	deps.observeDeps.TmuxRunner.(*treeMockRunner).windows[childSession+":finn"] = false

	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "finn") {
			if !strings.Contains(line, "DEAD") {
				t.Errorf("dead agent should show 'DEAD', got line: %q", line)
			}
			break
		}
	}
}

func TestTree_TmuxUnavailable(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: "whatever", TmuxWindow: "finn"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)
	// Set TmuxRunner to nil to simulate tmux unavailable.
	deps.observeDeps.TmuxRunner = nil

	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "finn") {
			if !strings.Contains(line, "?") {
				t.Errorf("agent with no tmux should show '?', got line: %q", line)
			}
			break
		}
	}
}

func TestTree_JSON(t *testing.T) {
	childSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, "weave")
	agents := []*state.AgentState{
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "finn"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)

	var buf bytes.Buffer
	err := runTree(deps, &buf, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, buf.String())
	}

	// Verify expected top-level fields.
	for _, field := range []string{"name", "type", "family", "status", "process_alive", "children"} {
		if _, ok := result[field]; !ok {
			t.Errorf("JSON output missing field %q", field)
		}
	}

	// Verify root name.
	if name, ok := result["name"].(string); !ok || name != "weave" {
		t.Errorf("JSON root name = %v, want 'weave'", result["name"])
	}

	// Verify children.
	children, ok := result["children"].([]interface{})
	if !ok {
		t.Fatalf("children should be an array, got: %T", result["children"])
	}
	if len(children) != 1 {
		t.Errorf("expected 1 child, got %d", len(children))
	}
}

func TestTree_JSON_WithOrphans(t *testing.T) {
	childSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, "weave")
	agents := []*state.AgentState{
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "finn"},
		{Name: "zone", Type: "engineer", Family: "engineering", Parent: "ghost", Status: "active", TmuxSession: childSession, TmuxWindow: "zone"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)

	var buf bytes.Buffer
	err := runTree(deps, &buf, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, buf.String())
	}

	// Should have orphans field.
	orphans, ok := result["orphans"]
	if !ok {
		t.Fatal("JSON output missing 'orphans' field")
	}
	orphanArr, ok := orphans.([]interface{})
	if !ok {
		t.Fatalf("orphans should be an array, got: %T", orphans)
	}
	if len(orphanArr) != 1 {
		t.Errorf("expected 1 orphan, got %d", len(orphanArr))
	}
}

func TestTree_RootFlag(t *testing.T) {
	childSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, "weave")
	agents := []*state.AgentState{
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "finn"},
		{Name: "ratz", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "ratz"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)

	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "finn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "finn") {
		t.Errorf("output should contain 'finn', got:\n%s", out)
	}
	if strings.Contains(out, "ratz") {
		t.Errorf("output should NOT contain 'ratz' when subtree root is 'finn', got:\n%s", out)
	}
	if strings.Contains(out, "weave") {
		t.Errorf("output should NOT contain 'weave' when subtree root is 'finn', got:\n%s", out)
	}
}

func TestTree_RootFlag_NotFound(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)

	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent subtree root")
	}
	if !strings.Contains(err.Error(), `agent "nonexistent" not found`) {
		t.Errorf("error should mention agent not found, got: %v", err)
	}
}

func TestTree_ChildrenSortedByName(t *testing.T) {
	childSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, "weave")
	agents := []*state.AgentState{
		{Name: "ratz", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "ratz"},
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "finn"},
		{Name: "cedar", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "cedar"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)

	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	// Find positions of child names in output.
	finnIdx, cedarIdx, ratzIdx := -1, -1, -1
	for i, line := range lines {
		if strings.Contains(line, "finn") {
			finnIdx = i
		}
		if strings.Contains(line, "cedar") {
			cedarIdx = i
		}
		if strings.Contains(line, "ratz") {
			ratzIdx = i
		}
	}
	if finnIdx == -1 || cedarIdx == -1 || ratzIdx == -1 {
		t.Fatalf("could not find all children in output:\n%s", out)
	}
	if !(cedarIdx < finnIdx && finnIdx < ratzIdx) { //nolint:staticcheck // QF1001: direct form is more readable
		t.Errorf("children should be sorted alphabetically: cedar(%d) < finn(%d) < ratz(%d)", cedarIdx, finnIdx, ratzIdx)
	}
}

func TestTree_TypeFamilyLabel(t *testing.T) {
	childSession := tmux.ChildrenSessionName(tmux.DefaultNamespace, "weave")
	agents := []*state.AgentState{
		{Name: "finn", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: childSession, TmuxWindow: "finn"},
	}
	deps := newTestTreeDeps(t, agents, "weave", tmux.DefaultNamespace)

	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()

	// Non-root agent should show type/family in label.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "finn") {
			if !strings.Contains(line, "engineer/engineering") {
				t.Errorf("non-root agent should show type/family, got line: %q", line)
			}
			if !strings.Contains(line, "active") {
				t.Errorf("non-root agent should show status, got line: %q", line)
			}
			if !strings.Contains(line, "alive") {
				t.Errorf("active non-root agent should show liveness, got line: %q", line)
			}
			break
		}
	}
}

func TestTree_UsesSupervisorProcessAliveWithoutTmux(t *testing.T) {
	alive := true
	deps := newStatusBackedTreeDeps(t, []supervisor.AgentInfo{
		{
			Name:         "finn",
			Type:         "engineer",
			Family:       "engineering",
			Parent:       "weave",
			Status:       "active",
			ProcessAlive: &alive,
		},
	}, "weave")

	var buf bytes.Buffer
	err := runTree(deps, &buf, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "alive") {
		t.Fatalf("expected supervisor ProcessAlive to drive tree output, got:\n%s", out)
	}
}

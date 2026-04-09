package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/observe"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
)

// statusMockRunner implements tmux.Runner for status tests.
type statusMockRunner struct {
	hasSessionResults map[string]bool
	hasWindowResults  map[string]bool // key: "session:window"
}

func (m *statusMockRunner) HasSession(name string) bool {
	if m.hasSessionResults == nil {
		return false
	}
	return m.hasSessionResults[name]
}

func (m *statusMockRunner) HasWindow(sessionName, windowName string) bool {
	if m.hasWindowResults == nil {
		return false
	}
	return m.hasWindowResults[sessionName+":"+windowName]
}

func (m *statusMockRunner) NewSession(string, map[string]string, string) error { return nil }
func (m *statusMockRunner) NewSessionWithWindow(string, string, map[string]string, string) error {
	return nil
}
func (m *statusMockRunner) NewWindow(string, string, map[string]string, string) error { return nil }
func (m *statusMockRunner) KillWindow(string, string) error                           { return nil }
func (m *statusMockRunner) ListWindowPIDs(string, string) ([]int, error)              { return nil, nil }
func (m *statusMockRunner) ListSessionNames() ([]string, error)                       { return nil, nil }
func (m *statusMockRunner) SendKeys(string, string, string) error                     { return nil }
func (m *statusMockRunner) Attach(string) error                                       { return nil }

// Compile-time check that statusMockRunner satisfies tmux.Runner.
var _ tmux.Runner = (*statusMockRunner)(nil)

// makeStatusTestDeps constructs a statusDeps with the given agents, root name,
// and tmux runner. It returns the deps and the output buffer.
func makeStatusTestDeps(agents []*state.AgentState, rootName string, runner tmux.Runner) (*statusDeps, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return &statusDeps{
		observeDeps: observe.Deps{
			TmuxRunner:    runner,
			ListAgents:    func(string) ([]*state.AgentState, error) { return agents, nil },
			ReadRootName:  func(string) string { return rootName },
			ReadNamespace: func(string) string { return "test" },
		},
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return "/fake"
			}
			return ""
		},
		stdout: buf,
		stderr: io.Discard,
	}, buf
}

func TestRunStatus_MissingSprawlRoot(t *testing.T) {
	buf := &bytes.Buffer{}
	deps := &statusDeps{
		observeDeps: observe.Deps{},
		getenv:      func(string) string { return "" },
		stdout:      buf,
		stderr:      io.Discard,
	}

	err := runStatus(deps, false, "", "", "", "")
	if err == nil {
		t.Fatal("expected error when SPRAWL_ROOT is empty")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should mention SPRAWL_ROOT, got: %v", err)
	}
}

func TestRunStatus_RootOnly(t *testing.T) {
	rootSession := tmux.RootSessionName("test")
	runner := &statusMockRunner{
		hasSessionResults: map[string]bool{
			rootSession: true,
		},
	}

	deps, buf := makeStatusTestDeps(nil, "weave", runner)

	err := runStatus(deps, false, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()

	// Should contain a header row.
	if !strings.Contains(out, "AGENT") {
		t.Error("expected table header with AGENT")
	}
	// Root agent row.
	if !strings.Contains(out, "weave") {
		t.Error("expected weave in output")
	}
	// Root type/family/parent/status should show "-".
	// The row should contain "alive" since tmux session is present.
	if !strings.Contains(out, "alive") {
		t.Error("expected 'alive' for root process column")
	}
}

func TestRunStatus_MultipleAgents(t *testing.T) {
	rootSession := tmux.RootSessionName("test")
	runner := &statusMockRunner{
		hasSessionResults: map[string]bool{
			rootSession: true,
		},
		hasWindowResults: map[string]bool{
			"sess:win-a": true,
			"sess:win-b": true,
		},
	}

	agents := []*state.AgentState{
		{Name: "alpha", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active", TmuxSession: "sess", TmuxWindow: "win-a"},
		{Name: "bravo", Type: "researcher", Family: "research", Parent: "weave", Status: "active", TmuxSession: "sess", TmuxWindow: "win-b"},
	}

	deps, buf := makeStatusTestDeps(agents, "weave", runner)

	err := runStatus(deps, false, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "weave") {
		t.Error("expected weave in output")
	}
	if !strings.Contains(out, "alpha") {
		t.Error("expected alpha in output")
	}
	if !strings.Contains(out, "bravo") {
		t.Error("expected bravo in output")
	}
	if !strings.Contains(out, "engineer") {
		t.Error("expected engineer type in output")
	}
	if !strings.Contains(out, "researcher") {
		t.Error("expected researcher type in output")
	}
}

func TestRunStatus_ProcessAlive(t *testing.T) {
	runner := &statusMockRunner{
		hasWindowResults: map[string]bool{
			"sess:win": true,
		},
	}

	agents := []*state.AgentState{
		{Name: "alpha", Status: "active", TmuxSession: "sess", TmuxWindow: "win", Parent: "weave"},
	}

	deps, buf := makeStatusTestDeps(agents, "", runner)

	err := runStatus(deps, false, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "alive") {
		t.Errorf("expected 'alive' in output for active agent with tmux window, got:\n%s", out)
	}
}

func TestRunStatus_ProcessDead(t *testing.T) {
	runner := &statusMockRunner{
		hasWindowResults: map[string]bool{
			"sess:win": false,
		},
	}

	agents := []*state.AgentState{
		{Name: "alpha", Status: "active", TmuxSession: "sess", TmuxWindow: "win", Parent: "weave"},
	}

	deps, buf := makeStatusTestDeps(agents, "", runner)

	err := runStatus(deps, false, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "DEAD") {
		t.Errorf("expected 'DEAD' in output for dead agent, got:\n%s", out)
	}
}

func TestRunStatus_ProcessTerminal(t *testing.T) {
	runner := &statusMockRunner{}

	agents := []*state.AgentState{
		{Name: "alpha", Status: "done", Parent: "weave"},
	}

	deps, buf := makeStatusTestDeps(agents, "", runner)

	err := runStatus(deps, false, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	// Terminal status agents should show "-" in process column.
	// The row for alpha should contain "done" and "-" for process.
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "alpha") {
			if !strings.Contains(line, "DEAD") {
				t.Errorf("expected 'DEAD' for done agent without tmux window, got line: %s", line)
			}
			break
		}
	}
}

func TestRunStatus_ProcessTerminalButAlive(t *testing.T) {
	runner := &statusMockRunner{
		hasWindowResults: map[string]bool{
			"sess:win": true,
		},
	}

	agents := []*state.AgentState{
		{Name: "alpha", Status: "done", Parent: "weave", TmuxSession: "sess", TmuxWindow: "win"},
	}

	deps, buf := makeStatusTestDeps(agents, "", runner)

	err := runStatus(deps, false, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "alpha") {
			if !strings.Contains(line, "alive") {
				t.Errorf("expected 'alive' for done agent with live tmux window, got line: %s", line)
			}
			break
		}
	}
}

func TestRunStatus_ProcessNoTmux(t *testing.T) {
	agents := []*state.AgentState{
		{Name: "alpha", Status: "active", TmuxSession: "sess", TmuxWindow: "win", Parent: "weave"},
	}

	// nil runner means tmux is unavailable.
	deps, buf := makeStatusTestDeps(agents, "", nil)

	err := runStatus(deps, false, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "?") {
		t.Errorf("expected '?' in output for active agent without tmux, got:\n%s", out)
	}
}

func TestRunStatus_LastReportDone(t *testing.T) {
	runner := &statusMockRunner{}

	agents := []*state.AgentState{
		{Name: "alpha", Status: "done", Parent: "weave", LastReportType: "done", LastReportMessage: "task completed"},
	}

	deps, buf := makeStatusTestDeps(agents, "", runner)

	err := runStatus(deps, false, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "[DONE]") {
		t.Errorf("expected '[DONE]' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "task completed") {
		t.Errorf("expected 'task completed' in output, got:\n%s", out)
	}
}

func TestRunStatus_LastReportNone(t *testing.T) {
	runner := &statusMockRunner{}

	agents := []*state.AgentState{
		{Name: "alpha", Status: "done", Parent: "weave"},
	}

	deps, buf := makeStatusTestDeps(agents, "", runner)

	err := runStatus(deps, false, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "alpha") {
			// Last report column should show "-" when no report exists.
			// This is a basic check: the line should not contain a bracketed report type.
			if strings.Contains(line, "[DONE]") || strings.Contains(line, "[STATUS]") || strings.Contains(line, "[PROBLEM]") {
				t.Errorf("expected no report prefix for agent with no report, got line: %s", line)
			}
			break
		}
	}
}

func TestRunStatus_LastReportTypeOnly(t *testing.T) {
	runner := &statusMockRunner{}

	agents := []*state.AgentState{
		{Name: "alpha", Status: "problem", Parent: "weave", LastReportType: "problem"},
	}

	deps, buf := makeStatusTestDeps(agents, "", runner)

	err := runStatus(deps, false, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "[PROBLEM]") {
		t.Errorf("expected '[PROBLEM]' in output, got:\n%s", out)
	}
}

func TestRunStatus_FilterFamily(t *testing.T) {
	runner := &statusMockRunner{}

	agents := []*state.AgentState{
		{Name: "alpha", Family: "engineering", Type: "engineer", Parent: "weave", Status: "active"},
		{Name: "bravo", Family: "research", Type: "researcher", Parent: "weave", Status: "active"},
	}

	deps, buf := makeStatusTestDeps(agents, "weave", runner)

	err := runStatus(deps, false, "engineering", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "alpha") {
		t.Error("expected alpha (engineering family) in filtered output")
	}
	if strings.Contains(out, "bravo") {
		t.Error("expected bravo (research family) to be filtered out")
	}
}

func TestRunStatus_FilterType(t *testing.T) {
	runner := &statusMockRunner{}

	agents := []*state.AgentState{
		{Name: "alpha", Family: "engineering", Type: "engineer", Parent: "weave", Status: "active"},
		{Name: "bravo", Family: "research", Type: "researcher", Parent: "weave", Status: "active"},
	}

	deps, buf := makeStatusTestDeps(agents, "weave", runner)

	err := runStatus(deps, false, "", "researcher", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "alpha") {
		t.Error("expected alpha (engineer type) to be filtered out")
	}
	if !strings.Contains(out, "bravo") {
		t.Error("expected bravo (researcher type) in filtered output")
	}
}

func TestRunStatus_FilterParent(t *testing.T) {
	runner := &statusMockRunner{}

	agents := []*state.AgentState{
		{Name: "alpha", Parent: "weave", Status: "active"},
		{Name: "bravo", Parent: "alpha", Status: "active"},
	}

	deps, buf := makeStatusTestDeps(agents, "weave", runner)

	err := runStatus(deps, false, "", "", "alpha", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "bravo") {
		t.Error("expected bravo (parent=alpha) in filtered output")
	}
	// weave should be filtered out (parent is empty, not "alpha").
	if strings.Contains(out, "weave") {
		t.Error("expected weave to be filtered out (parent is not alpha)")
	}
}

func TestRunStatus_FilterStatus(t *testing.T) {
	runner := &statusMockRunner{}

	agents := []*state.AgentState{
		{Name: "alpha", Parent: "weave", Status: "active"},
		{Name: "bravo", Parent: "weave", Status: "done"},
	}

	deps, buf := makeStatusTestDeps(agents, "weave", runner)

	err := runStatus(deps, false, "", "", "", "done")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "alpha") {
		t.Error("expected alpha (active status) to be filtered out")
	}
	if !strings.Contains(out, "bravo") {
		t.Error("expected bravo (done status) in filtered output")
	}
}

func TestRunStatus_FilterMultiple(t *testing.T) {
	runner := &statusMockRunner{}

	agents := []*state.AgentState{
		{Name: "alpha", Family: "engineering", Type: "engineer", Parent: "weave", Status: "active"},
		{Name: "bravo", Family: "engineering", Type: "researcher", Parent: "weave", Status: "done"},
		{Name: "charlie", Family: "research", Type: "engineer", Parent: "weave", Status: "active"},
	}

	deps, buf := makeStatusTestDeps(agents, "weave", runner)

	// Filter by family=engineering AND status=active. Only alpha should match.
	err := runStatus(deps, false, "engineering", "", "", "active")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "alpha") {
		t.Error("expected alpha in filtered output (engineering + active)")
	}
	if strings.Contains(out, "bravo") {
		t.Error("expected bravo filtered out (engineering but done)")
	}
	if strings.Contains(out, "charlie") {
		t.Error("expected charlie filtered out (research not engineering)")
	}
}

func TestRunStatus_FilterNoMatch(t *testing.T) {
	runner := &statusMockRunner{}

	agents := []*state.AgentState{
		{Name: "alpha", Family: "engineering", Status: "active", Parent: "weave"},
	}

	deps, buf := makeStatusTestDeps(agents, "weave", runner)

	err := runStatus(deps, false, "nonexistent", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	// Should still have the header but no agent rows (except maybe root if it passes filter).
	if strings.Contains(out, "alpha") {
		t.Error("expected alpha to be filtered out")
	}
	// Header should be present.
	if !strings.Contains(out, "AGENT") {
		t.Error("expected table header even when no agents match filter")
	}
}

func TestRunStatus_JSON(t *testing.T) {
	rootSession := tmux.RootSessionName("test")
	runner := &statusMockRunner{
		hasSessionResults: map[string]bool{
			rootSession: true,
		},
		hasWindowResults: map[string]bool{
			"sess:win": true,
		},
	}

	agents := []*state.AgentState{
		{
			Name: "alpha", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active",
			TmuxSession: "sess", TmuxWindow: "win", LastReportType: "status", LastReportMessage: "working",
		},
	}

	deps, buf := makeStatusTestDeps(agents, "weave", runner)

	err := runStatus(deps, true, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var entries []statusEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("failed to unmarshal JSON output: %v\nraw output:\n%s", err, buf.String())
	}

	if len(entries) < 2 {
		t.Fatalf("expected at least 2 entries (root + agent), got %d", len(entries))
	}

	// Find the alpha entry.
	var alpha *statusEntry
	var root *statusEntry
	for i := range entries {
		if entries[i].Name == "alpha" {
			alpha = &entries[i]
		}
		if entries[i].Name == "weave" {
			root = &entries[i]
		}
	}

	if alpha == nil {
		t.Fatal("expected alpha in JSON output")
	}
	if alpha.Type != "engineer" {
		t.Errorf("alpha type = %q, want %q", alpha.Type, "engineer")
	}
	if alpha.Family != "engineering" {
		t.Errorf("alpha family = %q, want %q", alpha.Family, "engineering")
	}
	if alpha.Parent != "weave" {
		t.Errorf("alpha parent = %q, want %q", alpha.Parent, "weave")
	}
	if alpha.Status != "active" {
		t.Errorf("alpha status = %q, want %q", alpha.Status, "active")
	}
	if alpha.Process != "alive" {
		t.Errorf("alpha process = %q, want %q", alpha.Process, "alive")
	}

	if root == nil {
		t.Fatal("expected weave in JSON output")
	}
	if !root.IsRoot {
		t.Error("expected weave to have is_root=true")
	}
}

func TestRunStatus_JSONFiltered(t *testing.T) {
	runner := &statusMockRunner{}

	agents := []*state.AgentState{
		{Name: "alpha", Family: "engineering", Type: "engineer", Parent: "weave", Status: "active"},
		{Name: "bravo", Family: "research", Type: "researcher", Parent: "weave", Status: "done"},
	}

	deps, buf := makeStatusTestDeps(agents, "weave", runner)

	err := runStatus(deps, true, "engineering", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var entries []statusEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v\nraw:\n%s", err, buf.String())
	}

	for _, e := range entries {
		if e.Name == "bravo" {
			t.Error("expected bravo to be filtered out of JSON output")
		}
	}

	found := false
	for _, e := range entries {
		if e.Name == "alpha" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected alpha in filtered JSON output")
	}
}

func TestRunStatus_LastReportTruncation(t *testing.T) {
	runner := &statusMockRunner{}

	longMsg := strings.Repeat("a", 100)
	agents := []*state.AgentState{
		{Name: "alpha", Status: "active", Parent: "weave", LastReportType: "status", LastReportMessage: longMsg},
	}

	deps, buf := makeStatusTestDeps(agents, "", runner)

	err := runStatus(deps, false, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	// The full 100-char message should not appear in table output.
	if strings.Contains(out, longMsg) {
		t.Error("expected long report message to be truncated in table output")
	}
	// But the beginning should still be there.
	if !strings.Contains(out, "[STATUS]") {
		t.Error("expected [STATUS] prefix in truncated report")
	}
	// Truncated text should have "..." or similar indicator.
	if !strings.Contains(out, "...") {
		t.Error("expected truncation indicator '...' in output")
	}
}

func TestTolerantListAgents_SkipsCorruptFile(t *testing.T) {
	// Create a temp directory simulating .sprawl/agents/ with one good and one corrupt file.
	tmpDir := t.TempDir()
	agentsDir := tmpDir + "/.sprawl/agents"
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a valid agent state file.
	validJSON := `{"name":"good","type":"engineer","family":"engineering","parent":"weave","status":"active"}`
	if err := os.WriteFile(agentsDir+"/good.json", []byte(validJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a corrupt agent state file.
	if err := os.WriteFile(agentsDir+"/bad.json", []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	stderr := &bytes.Buffer{}
	listFn := tolerantListAgents(stderr)

	agents, err := listFn(tmpDir)
	if err != nil {
		t.Fatalf("tolerantListAgents returned error: %v", err)
	}

	// Should have loaded the good agent and skipped the bad one.
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "good" {
		t.Errorf("expected agent name 'good', got %q", agents[0].Name)
	}

	// Stderr should contain a warning about the bad file.
	if !strings.Contains(stderr.String(), "bad") {
		t.Errorf("expected warning about corrupt file 'bad' in stderr, got: %s", stderr.String())
	}
}

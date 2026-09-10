package sprawlmcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/store"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// File-backed arguments on the two goal WRITE tools (QUM-1347).
//
// The property under test is that the CONTENT reaches the store, never the
// path, and that a path that cannot be read stops the call BEFORE the append.
// The call-count assertions are the load-bearing half: a tool that read the
// file after calling the store would still return an error and would still have
// closed the goal permanently, and an error-only test would not notice.

// goalFileEnv is a sprawl root holding one agent with a real worktree, plus the
// goal-source fake the tools write through.
type goalFileEnv struct {
	fileInputEnv
	src *fakeGoalSource
}

func newGoalFileEnv(t *testing.T, agentType string) goalFileEnv {
	t.Helper()
	e := newFileInputEnv(t)
	if err := state.SaveAgent(e.root, &state.AgentState{
		Name: "finn", Type: agentType, Worktree: e.worktree,
	}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	cfg, err := config.Load(e.root)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	src := &fakeGoalSource{
		enabled: true,
		closeID: uuid.New(),
		openedGoal: store.OpenedGoal{
			GoalEventID: uuid.New(), WorkflowID: uuid.New(), GoalType: store.GoalResearch,
		},
	}
	srv := New(&mockSupervisor{statusResult: []supervisor.AgentInfo{{Name: "finn", Type: agentType}}}).
		WithGoals(src).WithConfig(cfg)
	e.server = srv
	return goalFileEnv{fileInputEnv: e, src: src}
}

func TestReportResult_ReadsTheSummaryFromAFile(t *testing.T) {
	e := newGoalFileEnv(t, "engineer")
	body := strings.Repeat("the whole finding, at length.\n", 400)
	e.write(t, "out/result.md", body)
	goal := uuid.New()

	args, _ := json.Marshal(map[string]any{
		"goal_event_id": goal.String(), "outcome": "success", "summary_file": "out/result.md",
	})
	if _, err := e.server.toolReportResult(callerCtx("finn"), args); err != nil {
		t.Fatalf("toolReportResult with summary_file: %v", err)
	}
	if e.src.closedSummary != body {
		t.Errorf("the store got %q, want the file's %d bytes — the CONTENT is persisted, never the path", truncateForMsg(e.src.closedSummary), len(body))
	}
}

func TestReportResult_FileRefusalsNeverReachTheStore(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"missing file", map[string]any{"summary_file": "out/gone.md"}, "does not exist"},
		{"escaping path", map[string]any{"summary_file": "../../etc/passwd"}, "outside"},
		{"both forms", map[string]any{"summary": "inline", "summary_file": "out/result.md"}, "not both"},
		{"neither form", map[string]any{}, "exactly one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newGoalFileEnv(t, "engineer")
			e.write(t, "out/result.md", "content")
			args := map[string]any{"goal_event_id": uuid.New().String(), "outcome": "success"}
			for k, v := range tc.args {
				args[k] = v
			}
			raw, _ := json.Marshal(args)
			_, err := e.server.toolReportResult(callerCtx("finn"), raw)
			if err == nil {
				t.Fatal("report_result accepted the call")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should say %q; got: %v", tc.wantErr, err)
			}
			if e.src.closeCalls != 0 {
				t.Errorf("the goal was closed %d time(s) despite the refusal; a close is permanent", e.src.closeCalls)
			}
		})
	}
}

func TestCreateGoal_ReadsTheTextFromAFile(t *testing.T) {
	e := newGoalFileEnv(t, "manager")
	body := strings.Repeat("a long brief for the agent.\n", 400)
	e.write(t, "briefs/goal.md", body)

	args, _ := json.Marshal(map[string]any{"goal_type": "research", "text_file": "briefs/goal.md"})
	if _, err := e.server.toolCreateGoal(callerCtx("finn"), args); err != nil {
		t.Fatalf("toolCreateGoal with text_file: %v", err)
	}
	if e.src.openedText != body {
		t.Errorf("the store got %q, want the file's %d bytes", truncateForMsg(e.src.openedText), len(body))
	}
}

func TestCreateGoal_FileRefusalsNeverReachTheStore(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"missing file", map[string]any{"text_file": "briefs/gone.md"}, "does not exist"},
		{"escaping path", map[string]any{"text_file": "../../etc/passwd"}, "outside"},
		{"both forms", map[string]any{"text": "inline", "text_file": "briefs/goal.md"}, "not both"},
		{"neither form", map[string]any{}, "exactly one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newGoalFileEnv(t, "manager")
			e.write(t, "briefs/goal.md", "content")
			args := map[string]any{"goal_type": "research"}
			for k, v := range tc.args {
				args[k] = v
			}
			raw, _ := json.Marshal(args)
			_, err := e.server.toolCreateGoal(callerCtx("finn"), raw)
			if err == nil {
				t.Fatal("create_goal accepted the call")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should say %q; got: %v", tc.wantErr, err)
			}
			if e.src.openCalls != 0 {
				t.Errorf("a goal was opened %d time(s) despite the refusal", e.src.openCalls)
			}
		})
	}
}

// TestFileInputToolSchemas. The schema is the only place an agent learns the
// file form exists, and a `required` list still naming the inline field would
// make the client reject every file-backed call before the tool is even reached
// — a feature that is implemented and unreachable.
func TestFileInputToolSchemas(t *testing.T) {
	for _, tc := range []struct{ tool, inline, file string }{
		{"report_result", "summary", "summary_file"},
		{"create_goal", "text", "text_file"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			var def map[string]any
			for _, d := range baseToolDefinitions() {
				if d["name"] == tc.tool {
					def = d
				}
			}
			if def == nil {
				t.Fatalf("%s is not registered", tc.tool)
			}
			schema := def["inputSchema"].(map[string]any)
			props := schema["properties"].(map[string]any)
			for _, key := range []string{tc.inline, tc.file} {
				if _, ok := props[key]; !ok {
					t.Errorf("%s schema has no %q property", tc.tool, key)
				}
			}
			for _, r := range schema["required"].([]string) {
				if r == tc.inline || r == tc.file {
					t.Errorf("%s requires %q, so the other form of the same field can never be passed", tc.tool, r)
				}
			}
		})
	}
}

func truncateForMsg(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

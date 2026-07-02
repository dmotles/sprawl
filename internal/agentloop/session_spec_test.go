package agentloop

import (
	"io"
	"testing"

	"github.com/dmotles/sprawl/internal/claude"
	"github.com/dmotles/sprawl/internal/rootinit"
	"github.com/dmotles/sprawl/internal/state"
)

// TestBuildAgentSessionSpec_DisallowsLoopOnlyTools pins QUM-470: harness-only
// tools (ScheduleWakeup, Monitor, CronCreate, etc.) must be surfaced as
// SessionSpec.DisallowedTools for every child agent type, and must NOT appear
// in AllowedTools. These tools require an outer harness and have no meaning
// inside a child claude session.
func TestBuildAgentSessionSpec_DisallowsLoopOnlyTools(t *testing.T) {
	for _, agentType := range []string{"engineer", "researcher", "manager", "qa"} {
		t.Run(agentType, func(t *testing.T) {
			agentState := &state.AgentState{
				Name:      "test-agent",
				Type:      agentType,
				Worktree:  "/tmp/worktrees/test",
				SessionID: "sess-test",
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard)

			disallowed := make(map[string]bool, len(spec.DisallowedTools))
			for _, name := range spec.DisallowedTools {
				disallowed[name] = true
			}
			for _, want := range rootinit.ChildDisallowedTools {
				if !disallowed[want] {
					t.Errorf("SessionSpec.DisallowedTools missing %q for agent type %q (got %v)", want, agentType, spec.DisallowedTools)
				}
			}

			allowed := make(map[string]bool, len(spec.AllowedTools))
			for _, name := range spec.AllowedTools {
				allowed[name] = true
			}
			for _, name := range rootinit.ChildDisallowedTools {
				if allowed[name] {
					t.Errorf("SessionSpec.AllowedTools contains harness-only tool %q for agent type %q (allowed=%v)", name, agentType, spec.AllowedTools)
				}
			}
		})
	}
}

// TestBuildAgentSessionSpec_DisallowedRoundTripsToLaunchArgs verifies that
// the SessionSpec.DisallowedTools list, when threaded through
// claudecli.LaunchOpts, produces a `--disallowed-tools <name>` flag for each
// pinned name. Catches regressions where the field is set on SessionSpec but
// not propagated into the actual claude argv.
func TestBuildAgentSessionSpec_DisallowedRoundTripsToLaunchArgs(t *testing.T) {
	agentState := &state.AgentState{
		Name:      "engineer-agent",
		Type:      "engineer",
		Worktree:  "/tmp/worktrees/test",
		SessionID: "sess-engineer",
	}
	spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard)

	args := claude.LaunchOpts{DisallowedTools: spec.DisallowedTools}.BuildArgs()

	for _, name := range rootinit.ChildDisallowedTools {
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--disallowed-tools" && args[i+1] == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("claude argv missing `--disallowed-tools %s` (got %v)", name, args)
		}
	}
}

func TestBuildAgentSessionSpec_BaseFields(t *testing.T) {
	agentState := &state.AgentState{
		Name:      "finn",
		Type:      "engineer",
		Worktree:  "/tmp/worktrees/finn",
		TreePath:  "weave/finn",
		SessionID: "sess-finn",
	}
	spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard)

	// AllowedTools are set by the caller (runtime_launcher) via
	// RunnerDeps.AllowedTools, not by BuildAgentSessionSpec itself.
	// Verify base fields are correct.
	if spec.Identity != "finn" {
		t.Errorf("Identity = %q, want \"finn\"", spec.Identity)
	}
	if spec.SessionID != "sess-finn" {
		t.Errorf("SessionID = %q, want \"sess-finn\"", spec.SessionID)
	}
	if spec.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want \"bypassPermissions\"", spec.PermissionMode)
	}
}

func TestBuildAgentSessionSpec_ModelByAgentType(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		wantModel string
	}{
		{"engineer gets opus", "engineer", "opus"},
		{"researcher gets opus", "researcher", "opus"},
		{"manager gets opus[1m]", "manager", "opus[1m]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentState := &state.AgentState{
				Name:      "test-agent",
				Type:      tt.agentType,
				Worktree:  "/tmp/worktrees/test",
				SessionID: "sess-test",
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard)
			if spec.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q for agent type %q", spec.Model, tt.wantModel, tt.agentType)
			}
			if spec.Effort != "medium" {
				t.Errorf("Effort = %q, want \"medium\"", spec.Effort)
			}
		})
	}
}

// TestBuildAgentSessionSpec_ExplicitModelBeatsTypeDefault pins QUM-851: a
// non-empty AgentState.Model overrides the per-type default; an empty Model
// falls back to ModelForAgentType.
func TestBuildAgentSessionSpec_ExplicitModelBeatsTypeDefault(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		model     string
		wantModel string
	}{
		{"explicit model overrides engineer default", "engineer", "opus[1m]", "opus[1m]"},
		{"explicit fable overrides manager default", "manager", "fable", "fable"},
		{"empty model falls back to engineer default", "engineer", "", "opus"},
		{"empty model falls back to manager default", "manager", "", "opus[1m]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentState := &state.AgentState{
				Name:      "test-agent",
				Type:      tt.agentType,
				Model:     tt.model,
				Worktree:  "/tmp/worktrees/test",
				SessionID: "sess-test",
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard)
			if spec.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q (type=%q, explicit=%q)", spec.Model, tt.wantModel, tt.agentType, tt.model)
			}
		})
	}
}

// TestBuildAgentSessionSpec_NoAgentsArgv pins QUM-716 (#4.5): the `--agents`
// plumbing has been removed end-to-end. No agent type — including engineer —
// should produce a claude argv containing `--agents`.
func TestBuildAgentSessionSpec_NoAgentsArgv(t *testing.T) {
	for _, agentType := range []string{"engineer", "researcher", "manager", "weave", "qa"} {
		t.Run(agentType, func(t *testing.T) {
			agentState := &state.AgentState{
				Name:      "test-agent",
				Type:      agentType,
				Worktree:  "/tmp/worktrees/test",
				SessionID: "sess-test",
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard)
			args := claude.LaunchOpts{
				Model:           spec.Model,
				Effort:          spec.Effort,
				PermissionMode:  spec.PermissionMode,
				SessionID:       spec.SessionID,
				AllowedTools:    spec.AllowedTools,
				DisallowedTools: spec.DisallowedTools,
			}.BuildArgs()
			for _, a := range args {
				if a == "--agents" {
					t.Errorf("argv contains --agents for agent type %q (QUM-716 regression): %v", agentType, args)
				}
			}
		})
	}
}

// TestBuildAgentSessionSpec_EnablesReplayUserMessages pins QUM-817: every child
// agent must be launched with --replay-user-messages so the CLI echoes each
// consumed stdin user message back on stdout as an isReplay frame. That echo is
// the consumption ack the runtime keys delivery confirmation (MarkDelivered),
// task completion, and no-reinjection on. Without it, the entire Slice-2
// input-path contract silently breaks (messages re-inject every wake; tasks
// never mark done).
func TestBuildAgentSessionSpec_EnablesReplayUserMessages(t *testing.T) {
	agentState := &state.AgentState{
		Name:      "test-agent",
		Type:      "engineer",
		Worktree:  "/tmp/worktrees/test",
		SessionID: "sess-test",
	}
	spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard)
	if !spec.ReplayUserMessages {
		t.Fatal("SessionSpec.ReplayUserMessages = false, want true (QUM-817 consumption ack)")
	}
	// Round-trip to the actual claude argv.
	args := claude.LaunchOpts{
		InputFormat:        "stream-json",
		ReplayUserMessages: spec.ReplayUserMessages,
	}.BuildArgs()
	found := false
	for _, a := range args {
		if a == "--replay-user-messages" {
			found = true
		}
	}
	if !found {
		t.Errorf("claude argv missing --replay-user-messages: %v", args)
	}
}

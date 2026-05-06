package agentloop

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agent"
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
	for _, agentType := range []string{"engineer", "researcher", "manager"} {
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

// TestBuildAgentSessionSpec_AgentsByAgentType pins the QUM-408 wiring: only
// engineer agents must launch claude with `--agents <TDD JSON>`. Researchers,
// managers, and weave run without the TDD sub-agents. This test guards against
// re-regression during future spawn-path refactors (notably the unified
// runtime, QUM-396/QUM-398).
func TestBuildAgentSessionSpec_AgentsByAgentType(t *testing.T) {
	tests := []struct {
		name       string
		agentType  string
		wantAgents bool
	}{
		{"engineer gets TDD sub-agents", "engineer", true},
		{"researcher does not", "researcher", false},
		{"manager does not", "manager", false},
		{"weave does not", "weave", false},
	}
	expectedNames := []string{"oracle", "test-writer", "test-critic", "implementer", "code-reviewer", "qa-validator"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentState := &state.AgentState{
				Name:      "test-agent",
				Type:      tt.agentType,
				Worktree:  "/tmp/worktrees/test",
				SessionID: "sess-test",
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard)
			if tt.wantAgents {
				if spec.Agents == "" {
					t.Fatalf("Agents = %q, want non-empty for agent type %q", spec.Agents, tt.agentType)
				}
				var parsed map[string]agent.SubAgent
				if err := json.Unmarshal([]byte(spec.Agents), &parsed); err != nil {
					t.Fatalf("Agents not valid JSON: %v (got %q)", err, spec.Agents)
				}
				if len(parsed) != len(expectedNames) {
					t.Errorf("Agents map has %d entries, want %d", len(parsed), len(expectedNames))
				}
				for _, name := range expectedNames {
					if _, ok := parsed[name]; !ok {
						t.Errorf("Agents JSON missing sub-agent %q (got keys %v)", name, mapKeys(parsed))
					}
				}
				if spec.Agents != agent.TDDSubAgentsJSON() {
					t.Errorf("Agents JSON does not match agent.TDDSubAgentsJSON() output")
				}
			} else if spec.Agents != "" {
				t.Errorf("Agents = %q, want empty for agent type %q", spec.Agents, tt.agentType)
			}
		})
	}
}

func mapKeys(m map[string]agent.SubAgent) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBuildAgentSessionSpec_AgentsRoundTripsContainsExpectedFlag verifies that
// when SessionSpec.Agents is non-empty, the resulting claude argv contains
// `--agents <json>`. Catches the case where SessionSpec gains an Agents field
// but adapters fail to thread it into LaunchOpts.
func TestBuildAgentSessionSpec_AgentsRoundTripsToLaunchArgs(t *testing.T) {
	agentState := &state.AgentState{
		Name:      "engineer-agent",
		Type:      "engineer",
		Worktree:  "/tmp/worktrees/test",
		SessionID: "sess-engineer",
	}
	spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard)
	if spec.Agents == "" {
		t.Fatal("precondition: engineer SessionSpec.Agents is empty")
	}
	if !strings.Contains(spec.Agents, "oracle") {
		t.Fatalf("SessionSpec.Agents missing oracle sub-agent: %q", spec.Agents)
	}
}

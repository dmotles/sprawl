package sprawlmcp

import (
	"strings"
	"testing"
)

func TestSprawlSpawnToolDefinition_DescribesSameProcessWorktreeAgents(t *testing.T) {
	var spawn map[string]any
	for _, def := range toolDefinitions() {
		if def["name"] == "sprawl_spawn" {
			spawn = def
			break
		}
	}
	if spawn == nil {
		t.Fatal("sprawl_spawn tool definition not found")
	}

	desc, ok := spawn["description"].(string)
	if !ok {
		t.Fatalf("description type = %T, want string", spawn["description"])
	}
	if strings.Contains(desc, "subprocess") {
		t.Fatalf("sprawl_spawn description should not describe child runtimes as subprocesses: %q", desc)
	}
	if strings.Contains(desc, "subagent") {
		t.Fatalf("sprawl_spawn description should not advertise legacy subagent semantics: %q", desc)
	}

	schema, ok := spawn["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("inputSchema type = %T, want map[string]any", spawn["inputSchema"])
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("required type = %T, want []string", schema["required"])
	}
	foundBranch := false
	for _, field := range required {
		if field == "branch" {
			foundBranch = true
			break
		}
	}
	if !foundBranch {
		t.Fatal("sprawl_spawn should still require branch for worktree-backed child agents")
	}
}

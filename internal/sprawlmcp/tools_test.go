package sprawlmcp

import (
	"strings"
	"testing"
)

func TestSprawlSpawnToolDefinition_DescribesSameProcessWorktreeAgents(t *testing.T) {
	var spawn map[string]any
	for _, def := range toolDefinitions() {
		if def["name"] == "spawn" {
			spawn = def
			break
		}
	}
	if spawn == nil {
		t.Fatal("spawn tool definition not found")
	}

	desc, ok := spawn["description"].(string)
	if !ok {
		t.Fatalf("description type = %T, want string", spawn["description"])
	}
	if strings.Contains(desc, "subprocess") {
		t.Fatalf("spawn description should not describe child runtimes as subprocesses: %q", desc)
	}
	if strings.Contains(desc, "subagent") {
		t.Fatalf("spawn description should not advertise legacy subagent semantics: %q", desc)
	}

	schema, ok := spawn["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("inputSchema type = %T, want map[string]any", spawn["inputSchema"])
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("required type = %T, want []string", schema["required"])
	}
	// QUM-709: `branch` is no longer unconditionally required at the JSON
	// schema level (sub-agents reuse the parent's branch). The server-side
	// toolSpawn handler enforces the conditional requirement: branch is
	// required when subagent=false and forbidden when subagent=true.
	for _, field := range required {
		if field == "branch" {
			t.Fatal("spawn `branch` must not be unconditionally required; enforce in toolSpawn instead (QUM-709)")
		}
	}
}

// TestSprawlSpawnToolDefinition_TypeEnumIncludesQA pins QUM-707: the spawn
// MCP tool's `type` enum must include "qa" alongside engineer/researcher/manager.
func TestSprawlSpawnToolDefinition_TypeEnumIncludesQA(t *testing.T) {
	var spawn map[string]any
	for _, def := range toolDefinitions() {
		if def["name"] == "spawn" {
			spawn = def
			break
		}
	}
	if spawn == nil {
		t.Fatal("spawn tool definition not found")
	}
	schema, ok := spawn["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("inputSchema type = %T, want map[string]any", spawn["inputSchema"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", schema["properties"])
	}
	typeProp, ok := props["type"].(map[string]any)
	if !ok {
		t.Fatalf("properties.type type = %T, want map[string]any", props["type"])
	}
	enum, ok := typeProp["enum"].([]string)
	if !ok {
		t.Fatalf("properties.type.enum type = %T, want []string", typeProp["enum"])
	}

	wantTypes := []string{"engineer", "researcher", "manager", "qa"}
	have := make(map[string]bool, len(enum))
	for _, v := range enum {
		have[v] = true
	}
	for _, want := range wantTypes {
		if !have[want] {
			t.Errorf("spawn type enum missing %q (got %v)", want, enum)
		}
	}
}

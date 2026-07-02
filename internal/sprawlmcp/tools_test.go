package sprawlmcp

import (
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/rootinit"
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

// spawnSchemaProps is a test helper returning the spawn tool's inputSchema
// properties map.
func spawnSchemaProps(t *testing.T) map[string]any {
	t.Helper()
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
	return props
}

// TestSprawlSpawnToolDefinition_HasModelEnum pins QUM-851: the spawn schema
// exposes an optional `model` property whose enum matches rootinit.ValidSpawnModels.
func TestSprawlSpawnToolDefinition_HasModelEnum(t *testing.T) {
	props := spawnSchemaProps(t)
	modelProp, ok := props["model"].(map[string]any)
	if !ok {
		t.Fatalf("properties.model type = %T, want map[string]any", props["model"])
	}
	enum, ok := modelProp["enum"].([]string)
	if !ok {
		t.Fatalf("properties.model.enum type = %T, want []string", modelProp["enum"])
	}
	want := []string{"haiku", "sonnet", "opus", "fable", "opus[1m]", "sonnet[1m]"}
	if len(enum) != len(want) {
		t.Fatalf("model enum = %v, want %v", enum, want)
	}
	for i := range want {
		if enum[i] != want[i] {
			t.Errorf("model enum[%d] = %q, want %q", i, enum[i], want[i])
		}
	}
	// The schema enum must be the SAME source of truth as the resolver.
	if len(enum) != len(rootinit.ValidSpawnModels) {
		t.Fatalf("model enum %v must match rootinit.ValidSpawnModels %v", enum, rootinit.ValidSpawnModels)
	}
	for i := range rootinit.ValidSpawnModels {
		if enum[i] != rootinit.ValidSpawnModels[i] {
			t.Errorf("model enum[%d] = %q, want %q (rootinit.ValidSpawnModels)", i, enum[i], rootinit.ValidSpawnModels[i])
		}
	}
}

// TestSprawlSpawnToolDefinition_HasSystemPromptProp pins QUM-851: the spawn
// schema exposes an optional `system_prompt` string property, and neither new
// field is in the required list.
func TestSprawlSpawnToolDefinition_HasSystemPromptProp(t *testing.T) {
	props := spawnSchemaProps(t)
	sp, ok := props["system_prompt"].(map[string]any)
	if !ok {
		t.Fatalf("properties.system_prompt type = %T, want map[string]any", props["system_prompt"])
	}
	if sp["type"] != "string" {
		t.Errorf("system_prompt.type = %v, want string", sp["type"])
	}

	var spawn map[string]any
	for _, def := range toolDefinitions() {
		if def["name"] == "spawn" {
			spawn = def
			break
		}
	}
	schema := spawn["inputSchema"].(map[string]any)
	required, _ := schema["required"].([]string)
	for _, r := range required {
		if r == "model" || r == "system_prompt" {
			t.Errorf("required must not include %q (both new fields are optional): %v", r, required)
		}
	}
}

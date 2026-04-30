package sprawlmcp

import (
	"strings"
	"testing"
)

func TestMCPToolNames_ReturnsAllToolDefinitions(t *testing.T) {
	names := MCPToolNames()
	defs := toolDefinitions()
	if len(names) != len(defs) {
		t.Fatalf("MCPToolNames() returned %d names, want %d (one per toolDefinition)", len(names), len(defs))
	}

	// Every name must have the mcp__sprawl__ prefix.
	for _, name := range names {
		if !strings.HasPrefix(name, "mcp__sprawl__") {
			t.Errorf("MCPToolNames() entry %q missing mcp__sprawl__ prefix", name)
		}
	}

	// Every tool definition must be represented.
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	for _, def := range defs {
		rawName := def["name"].(string)
		want := "mcp__sprawl__" + rawName
		if !nameSet[want] {
			t.Errorf("MCPToolNames() missing entry for tool definition %q (expected %q)", rawName, want)
		}
	}
}

func TestMCPToolNames_NoDuplicates(t *testing.T) {
	names := MCPToolNames()
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n] {
			t.Errorf("MCPToolNames() contains duplicate: %q", n)
		}
		seen[n] = true
	}
}

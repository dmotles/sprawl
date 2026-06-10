package sprawlmcp

// QUM-724 — verify the MCP tool catalog has been migrated from `recover` to
// `wake`, and that `peek`/`status` descriptions explicitly call out that
// they do NOT wake the agent.
//
// These tests will fail until the catalog edits land in tools.go.

import (
	"strings"
	"testing"
)

// findTool returns the entry with name n from toolDefinitions(), or nil.
func findTool(t *testing.T, n string) map[string]any {
	t.Helper()
	for _, def := range toolDefinitions() {
		if name, _ := def["name"].(string); name == n {
			return def
		}
	}
	return nil
}

func TestPeekDescription_MentionsDoesNotWake(t *testing.T) {
	def := findTool(t, "peek")
	if def == nil {
		t.Fatal("peek tool missing from catalog")
	}
	desc, _ := def["description"].(string)
	if !strings.Contains(strings.ToLower(desc), "does not wake") &&
		!strings.Contains(desc, "Does NOT wake") {
		t.Errorf("peek description must explicitly state it does not wake the agent; got: %q", desc)
	}
}

func TestStatusDescription_MentionsDoesNotWake(t *testing.T) {
	def := findTool(t, "status")
	if def == nil {
		t.Fatal("status tool missing from catalog")
	}
	desc, _ := def["description"].(string)
	if !strings.Contains(strings.ToLower(desc), "does not wake") &&
		!strings.Contains(desc, "Does NOT wake") {
		t.Errorf("status description must explicitly state it does not wake the agent; got: %q", desc)
	}
}

func TestRecoverToolRemoved(t *testing.T) {
	if def := findTool(t, "recover"); def != nil {
		t.Errorf("recover tool must be removed from catalog (QUM-724 clean break); found: %v", def)
	}
	if def := findTool(t, "wake"); def == nil {
		t.Fatal("wake tool missing from catalog (QUM-724)")
	}
}

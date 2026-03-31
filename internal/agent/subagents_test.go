package agent

import (
	"encoding/json"
	"testing"
)

func TestTDDSubAgents_ReturnsAllAgents(t *testing.T) {
	agents := TDDSubAgents()

	expectedNames := []string{
		"oracle",
		"test-writer",
		"test-critic",
		"implementer",
		"code-reviewer",
		"qa-validator",
	}

	for _, name := range expectedNames {
		if _, ok := agents[name]; !ok {
			t.Errorf("TDDSubAgents missing agent %q", name)
		}
	}

	if len(agents) != len(expectedNames) {
		t.Errorf("expected %d agents, got %d", len(expectedNames), len(agents))
	}
}

func TestTDDSubAgents_AllHaveDescriptionAndPrompt(t *testing.T) {
	agents := TDDSubAgents()

	for name, a := range agents {
		if a.Description == "" {
			t.Errorf("agent %q has empty description", name)
		}
		if a.Prompt == "" {
			t.Errorf("agent %q has empty prompt", name)
		}
	}
}

func TestTDDSubAgentsJSON_ValidJSON(t *testing.T) {
	jsonStr := TDDSubAgentsJSON()

	var parsed map[string]SubAgent
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("TDDSubAgentsJSON returned invalid JSON: %v", err)
	}

	if len(parsed) != 6 {
		t.Errorf("expected 6 agents in JSON, got %d", len(parsed))
	}
}

func TestTDDSubAgentsJSON_ContainsAllAgentNames(t *testing.T) {
	jsonStr := TDDSubAgentsJSON()

	expectedNames := []string{
		"oracle",
		"test-writer",
		"test-critic",
		"implementer",
		"code-reviewer",
		"qa-validator",
	}

	var parsed map[string]SubAgent
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	for _, name := range expectedNames {
		if _, ok := parsed[name]; !ok {
			t.Errorf("JSON missing agent %q", name)
		}
	}
}

func TestTDDSubAgents_OracleIsReadOnly(t *testing.T) {
	agents := TDDSubAgents()
	oracle := agents["oracle"]

	if oracle.Prompt == "" {
		t.Fatal("oracle prompt is empty")
	}

	// Oracle should mention being read-only
	if !containsStr(oracle.Prompt, "READ-ONLY") {
		t.Error("oracle prompt should mention being READ-ONLY")
	}
}

func TestTDDSubAgents_TestWriterMentionsTDD(t *testing.T) {
	agents := TDDSubAgents()
	tw := agents["test-writer"]

	if !containsStr(tw.Prompt, "TDD") {
		t.Error("test-writer prompt should mention TDD")
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

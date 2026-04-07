package agent

import (
	"strings"
	"testing"
)

func TestBuildRootPrompt_ContainsKeyPhrases(t *testing.T) {
	phrases := []string{
		"sprawl spawn",
		"neo",
		"DO NOT edit code",
		"--type engineer",
		"--type researcher",
		"--family",
		"sprawl messages",
		"sprawl merge",
		"sprawl cleanup branches",
		"--no-validate",
		"--dry-run",
		"TaskCreate",
	}

	for _, phrase := range phrases {
		if !strings.Contains(BuildRootPrompt(PromptConfig{RootName: "neo", AgentCLI: "claude-code"}), phrase) {
			t.Errorf("root system prompt missing key phrase: %q", phrase)
		}
	}
}

func TestEngineerSystemPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildEngineerPrompt("frank", "root", "sprawl/frank", testEnvConfig())
	phrases := []string{
		"frank",
		"root",
		"sprawl/frank",
		"sprawl report done",
		"sprawl report problem",
		"sprawl messages send",
		"TDD WORKFLOW",
		"oracle",
		"test-writer",
		"test-critic",
		"implementer",
		"code-reviewer",
		"qa-validator",
		"sub-agents",
	}
	for _, phrase := range phrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("engineer system prompt missing key phrase: %q", phrase)
		}
	}
}

func TestBuildRootPrompt_DoesNotContainRemovedTypes(t *testing.T) {
	prompt := BuildRootPrompt(PromptConfig{RootName: "neo", AgentCLI: "claude-code"})
	for _, removed := range []string{"--type tester"} {
		if strings.Contains(prompt, removed) {
			t.Errorf("root system prompt should not contain removed type: %q", removed)
		}
	}
}

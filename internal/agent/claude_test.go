package agent

import (
	"strings"
	"testing"
)

func TestBuildRootPrompt_ContainsKeyPhrases(t *testing.T) {
	phrases := []string{
		"dendra spawn",
		"sensei",
		"DO NOT edit code",
		"--type engineer",
		"--type researcher",
		"--family",
		"dendra messages",
		"dendra merge",
		"dendra cleanup branches",
		"--no-validate",
		"--dry-run",
		"TaskCreate",
	}

	for _, phrase := range phrases {
		if !strings.Contains(BuildRootPrompt(PromptConfig{RootName: "sensei", AgentCLI: "claude-code"}), phrase) {
			t.Errorf("root system prompt missing key phrase: %q", phrase)
		}
	}
}

func TestEngineerSystemPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildEngineerPrompt("frank", "root", "dendra/frank", testEnvConfig())
	phrases := []string{
		"frank",
		"root",
		"dendra/frank",
		"dendra report done",
		"dendra report problem",
		"dendra messages send",
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
	prompt := BuildRootPrompt(PromptConfig{RootName: "sensei", AgentCLI: "claude-code"})
	for _, removed := range []string{"--type tester", "--type manager"} {
		if strings.Contains(prompt, removed) {
			t.Errorf("root system prompt should not contain removed type: %q", removed)
		}
	}
}

package agent

import (
	"strings"
	"testing"
)

func TestBuildEngineerPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", "implement login page")

	keyPhrases := []string{
		"Engineer agent",
		"oak",
		"root",
		"dendra/oak",
		"implement login page",
		"dendra report done",
		"dendra report problem",
		"dendra messages send root",
		"DENDRA_AGENT_IDENTITY",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("engineer prompt missing key phrase: %q", phrase)
		}
	}
}

func TestBuildResearcherPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "dendra/birch", "investigate auth libraries")

	keyPhrases := []string{
		"Researcher agent",
		"birch",
		"root",
		"dendra/birch",
		"investigate auth libraries",
		"dendra report done",
		"dendra report problem",
		"dendra messages send root",
		"DENDRA_AGENT_IDENTITY",
		"do NOT modify production code",
		"deep investigator",
		"document findings",
		"systematic analysis",
		"tradeoffs",
		".dendra/agents/birch/findings/",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("researcher prompt missing key phrase: %q", phrase)
		}
	}
}

func TestBuildResearcherPrompt_DoesNotContainEngineerRole(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "dendra/birch", "research task")

	if strings.Contains(prompt, "hands-on builder") {
		t.Error("researcher prompt should not contain engineer role 'hands-on builder'")
	}
}

func TestBuildEngineerPrompt_DoesNotContainResearcherRole(t *testing.T) {
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", "build task")

	if strings.Contains(prompt, "deep investigator") {
		t.Error("engineer prompt should not contain researcher role 'deep investigator'")
	}
}

func TestBuildEngineerPrompt_TDDWorkflowIsMandatory(t *testing.T) {
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", "implement feature")

	mandatoryPhrases := []string{
		// The workflow must be explicitly mandatory
		"MUST follow this TDD workflow",
		"not optional",
		"Do not skip steps",
		// Must prohibit jumping to implementation
		"Do NOT jump straight to implementation",
		"each step in order",
		// Oracle step must require stopping to plan first
		"STOP and plan FIRST",
		"Do not write any code until you have a complete plan",
		// Test-critic must enforce the loop
		"go back to test-writer",
		"Repeat until approved",
		// Each step must require verification before proceeding
		"verify the step is complete before moving on",
	}
	for _, phrase := range mandatoryPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("engineer prompt missing mandatory TDD phrase: %q", phrase)
		}
	}
}

func TestBuildEngineerPrompt_PreservesSubAgentNames(t *testing.T) {
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", "implement feature")

	subAgents := []string{
		"oracle",
		"test-writer",
		"test-critic",
		"implementer",
		"code-reviewer",
		"qa-validator",
	}
	for _, agent := range subAgents {
		if !strings.Contains(prompt, agent) {
			t.Errorf("engineer prompt missing sub-agent name: %q", agent)
		}
	}
}

func TestBuildEngineerPrompt_PreservesWorkflowOrder(t *testing.T) {
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", "implement feature")

	// Verify the workflow steps appear in order
	steps := []string{"oracle", "test-writer", "test-critic", "implementer", "code-reviewer", "qa-validator"}
	lastIdx := -1
	for _, step := range steps {
		idx := strings.Index(prompt, step)
		if idx == -1 {
			t.Fatalf("engineer prompt missing workflow step: %q", step)
		}
		if idx <= lastIdx {
			t.Errorf("workflow step %q appears out of order", step)
		}
		lastIdx = idx
	}
}

func TestBuildEngineerPrompt_ReflectionStep(t *testing.T) {
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", "implement feature")

	keyPhrases := []string{
		"Reflect",
		"out of scope",
		"edge cases",
		"Architectural observations",
		"future agents",
		"comment on the Linear issue",
		"done report",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("engineer prompt missing reflection phrase: %q", phrase)
		}
	}
}

func TestBuildResearcherPrompt_ReflectionStep(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "dendra/birch", "investigate auth libraries")

	keyPhrases := []string{
		"REFLECTION",
		"surprising",
		"open questions",
		"investigate next",
		"comment on the Linear issue",
		"done report",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("researcher prompt missing reflection phrase: %q", phrase)
		}
	}
}

func TestBuildEngineerPrompt_ReflectionBeforeDone(t *testing.T) {
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", "implement feature")

	qaIdx := strings.Index(prompt, "qa-validator")
	reflectIdx := strings.Index(prompt, "Reflect")
	doneIdx := strings.Index(prompt, "Report done")

	if qaIdx == -1 {
		t.Fatal("engineer prompt missing 'qa-validator'")
	}
	if reflectIdx == -1 {
		t.Fatal("engineer prompt missing 'Reflect'")
	}
	if doneIdx == -1 {
		t.Fatal("engineer prompt missing 'Report done'")
	}

	if reflectIdx <= qaIdx {
		t.Errorf("'Reflect' (idx %d) should appear after 'qa-validator' (idx %d)", reflectIdx, qaIdx)
	}
	if reflectIdx >= doneIdx {
		t.Errorf("'Reflect' (idx %d) should appear before 'Report done' (idx %d)", reflectIdx, doneIdx)
	}
}

func TestRootSystemPrompt_VerificationGuidance(t *testing.T) {
	keyPhrases := []string{
		"VERIFYING AGENT WORK",
		"git diff main..dendra/<name>",
		"go test ./...",
		".dendra/agents/<name>/findings/",
		"scope creep",
		"pass/fail summary",
		"Linear issue comments",
		"done report",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(RootSystemPrompt, phrase) {
			t.Errorf("RootSystemPrompt missing verification guidance phrase: %q", phrase)
		}
	}
}

func TestBuildResearcherPrompt_ReflectionBeforeDone(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "dendra/birch", "investigate auth libraries")

	reflectIdx := strings.Index(prompt, "REFLECTION")
	doneIdx := strings.Index(prompt, "dendra report done")

	if reflectIdx == -1 {
		t.Fatal("researcher prompt missing 'REFLECTION'")
	}
	if doneIdx == -1 {
		t.Fatal("researcher prompt missing 'dendra report done'")
	}

	if reflectIdx >= doneIdx {
		t.Errorf("'REFLECTION' (idx %d) should appear before 'dendra report done' (idx %d)", reflectIdx, doneIdx)
	}
}

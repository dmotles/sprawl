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

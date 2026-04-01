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

// Helper to build a default claude-code root prompt config for tests.
func defaultRootConfig(name string) PromptConfig {
	return PromptConfig{
		RootName: name,
		AgentCLI: "claude-code",
	}
}

func TestBuildRootPrompt_DoesNotMentionRespawn(t *testing.T) {
	if strings.Contains(BuildRootPrompt(defaultRootConfig("sensei")), "respawn") {
		t.Error("BuildRootPrompt should not mention 'respawn' — the command was canceled (QUM-46)")
	}
}

func TestBuildRootPrompt_PromptConfigStruct(t *testing.T) {
	cfg := PromptConfig{
		RootName: "sensei",
		AgentCLI: "claude-code",
	}
	prompt := BuildRootPrompt(cfg)
	if !strings.Contains(prompt, `Your identity is "sensei"`) {
		t.Error("BuildRootPrompt should interpolate RootName from config")
	}
}

func TestBuildRootPrompt_SubAgentGuidance_ClaudeCode(t *testing.T) {
	cfg := PromptConfig{
		RootName: "sensei",
		AgentCLI: "claude-code",
	}
	prompt := BuildRootPrompt(cfg)

	keyPhrases := []string{
		"AGENT TYPES: DENDRA AGENTS vs SUB-AGENTS",
		"Dendra agents",
		"dendra spawn",
		"Claude Code sub-agents",
		"Agent tool",
		"fire off an agent",
		"spawn an agent",
		"sub-agent",
		"Default to dendra agents for real work",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("root prompt (claude-code) missing sub-agent guidance phrase: %q", phrase)
		}
	}
}

func TestBuildRootPrompt_SubAgentGuidance_NotIncludedForUnknownCLI(t *testing.T) {
	cfg := PromptConfig{
		RootName: "sensei",
		AgentCLI: "codex",
	}
	prompt := BuildRootPrompt(cfg)

	// Sub-agent guidance should NOT be present for non-claude-code CLIs
	if strings.Contains(prompt, "AGENT TYPES: DENDRA AGENTS vs SUB-AGENTS") {
		t.Error("sub-agent guidance should not be included for non-claude-code CLI")
	}
	if strings.Contains(prompt, "Claude Code sub-agents") {
		t.Error("Claude Code sub-agent references should not be included for non-claude-code CLI")
	}
}

func TestBuildRootPrompt_SubAgentGuidance_EmptyCLI(t *testing.T) {
	cfg := PromptConfig{
		RootName: "sensei",
		AgentCLI: "",
	}
	prompt := BuildRootPrompt(cfg)

	// Sub-agent guidance should NOT be present when AgentCLI is empty
	if strings.Contains(prompt, "AGENT TYPES: DENDRA AGENTS vs SUB-AGENTS") {
		t.Error("sub-agent guidance should not be included when AgentCLI is empty")
	}
}

func TestBuildRootPrompt_SubAgentGuidanceSectionOrdering(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("sensei"))

	agentTypesIdx := strings.Index(prompt, "AGENT TYPES: DENDRA AGENTS vs SUB-AGENTS")
	verifyIdx := strings.Index(prompt, "VERIFYING AGENT WORK:")

	if agentTypesIdx == -1 {
		t.Fatal("BuildRootPrompt missing 'AGENT TYPES: DENDRA AGENTS vs SUB-AGENTS'")
	}
	if verifyIdx == -1 {
		t.Fatal("BuildRootPrompt missing 'VERIFYING AGENT WORK:'")
	}
	if agentTypesIdx >= verifyIdx {
		t.Errorf("AGENT TYPES section (idx %d) should appear before VERIFYING AGENT WORK (idx %d)", agentTypesIdx, verifyIdx)
	}
}

func TestBuildRootPrompt_VerificationGuidance(t *testing.T) {
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
		if !strings.Contains(BuildRootPrompt(defaultRootConfig("sensei")), phrase) {
			t.Errorf("BuildRootPrompt missing verification guidance phrase: %q", phrase)
		}
	}
}

func TestBuildRootPrompt_ParallelismGuidance(t *testing.T) {
	keyPhrases := []string{
		"PARALLELISM VS. SERIALIZATION",
		"overlapping files",
		"merge conflicts",
		"Parallelize freely",
		"different packages",
		"Serialize when",
		"refactor",
		"sequential execution",
		"merge order",
		"smaller and more isolated",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(BuildRootPrompt(defaultRootConfig("sensei")), phrase) {
			t.Errorf("BuildRootPrompt missing parallelism guidance phrase: %q", phrase)
		}
	}
}

func TestBuildRootPrompt_ParallelismSectionOrdering(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("sensei"))
	rulesIdx := strings.Index(prompt, "RULES:")
	parallelismIdx := strings.Index(prompt, "PARALLELISM VS. SERIALIZATION:")
	verifyIdx := strings.Index(prompt, "VERIFYING AGENT WORK:")

	if rulesIdx == -1 {
		t.Fatal("BuildRootPrompt missing 'RULES:'")
	}
	if parallelismIdx == -1 {
		t.Fatal("BuildRootPrompt missing 'PARALLELISM VS. SERIALIZATION:'")
	}
	if verifyIdx == -1 {
		t.Fatal("BuildRootPrompt missing 'VERIFYING AGENT WORK:'")
	}

	if parallelismIdx <= rulesIdx {
		t.Errorf("PARALLELISM (idx %d) should appear after RULES (idx %d)", parallelismIdx, rulesIdx)
	}
	if parallelismIdx >= verifyIdx {
		t.Errorf("PARALLELISM (idx %d) should appear before VERIFYING AGENT WORK (idx %d)", parallelismIdx, verifyIdx)
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

func TestBuildRootPrompt_KeyCommands_AllCommandsPresent(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("sensei"))

	// Spawning & Lifecycle commands
	spawnLifecycleCommands := []string{
		"dendra spawn --family",
		"dendra spawn subagent --family",
		"dendra delegate",
		"dendra kill",
		"dendra retire",
		"dendra logs",
	}
	for _, cmd := range spawnLifecycleCommands {
		if !strings.Contains(prompt, cmd) {
			t.Errorf("root prompt KEY COMMANDS missing spawn/lifecycle command: %q", cmd)
		}
	}

	// Messaging commands
	messagingCommands := []string{
		"dendra messages inbox",
		"dendra messages send",
		"dendra messages read",
		"dendra messages list",
		"dendra messages broadcast",
		"dendra messages archive",
	}
	for _, cmd := range messagingCommands {
		if !strings.Contains(prompt, cmd) {
			t.Errorf("root prompt KEY COMMANDS missing messaging command: %q", cmd)
		}
	}

	// Reporting commands
	reportingCommands := []string{
		"dendra report status",
		"dendra report done",
		"dendra report problem",
	}
	for _, cmd := range reportingCommands {
		if !strings.Contains(prompt, cmd) {
			t.Errorf("root prompt KEY COMMANDS missing reporting command: %q", cmd)
		}
	}
}

func TestBuildRootPrompt_KeyCommands_RetireDistinguishedFromKill(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("sensei"))

	if !strings.Contains(prompt, "dendra retire") {
		t.Error("root prompt should mention 'dendra retire'")
	}
	if !strings.Contains(prompt, "dendra kill") {
		t.Error("root prompt should mention 'dendra kill'")
	}
	// retire should be described as full teardown / preferred cleanup
	retireIdx := strings.Index(prompt, "dendra retire")
	killIdx := strings.Index(prompt, "dendra kill")
	if retireIdx == -1 || killIdx == -1 {
		t.Fatal("both retire and kill must be present")
	}
	// They should be distinct entries (different lines)
	if retireIdx == killIdx {
		t.Error("retire and kill should be separate entries")
	}
}

func TestBuildRootPrompt_KeyCommands_GroupedLogically(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("sensei"))

	// Verify KEY COMMANDS section exists
	if !strings.Contains(prompt, "KEY COMMANDS:") {
		t.Fatal("root prompt missing 'KEY COMMANDS:' section")
	}

	// Verify commands are grouped with section headers
	keyCommandsIdx := strings.Index(prompt, "KEY COMMANDS:")
	if keyCommandsIdx == -1 {
		t.Fatal("KEY COMMANDS section not found")
	}

	// The spawn commands should appear before messaging commands,
	// which should appear before reporting commands
	spawnIdx := strings.Index(prompt, "dendra spawn --family")
	inboxIdx := strings.Index(prompt, "dendra messages inbox")
	reportIdx := strings.Index(prompt, "dendra report status")

	if spawnIdx >= inboxIdx {
		t.Errorf("spawn commands (idx %d) should appear before messaging commands (idx %d)", spawnIdx, inboxIdx)
	}
	if inboxIdx >= reportIdx {
		t.Errorf("messaging commands (idx %d) should appear before reporting commands (idx %d)", inboxIdx, reportIdx)
	}
}

func TestBuildRootPrompt_InterpolatesIdentity(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("sensei"))
	if !strings.Contains(prompt, `Your identity is "sensei"`) {
		t.Error("BuildRootPrompt should interpolate the root name into identity line")
	}

	prompt2 := BuildRootPrompt(defaultRootConfig("kai"))
	if !strings.Contains(prompt2, `Your identity is "kai"`) {
		t.Error("BuildRootPrompt should interpolate custom root name")
	}
	if strings.Contains(prompt2, `Your identity is "root"`) {
		t.Error("BuildRootPrompt should not hardcode 'root' identity")
	}
}

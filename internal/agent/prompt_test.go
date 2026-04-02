package agent

import (
	"strings"
	"testing"
)

func testEnvConfig() EnvConfig {
	return EnvConfig{
		WorkDir:  "/tmp/worktrees/test",
		Platform: "linux",
		Shell:    "/bin/zsh",
	}
}

func TestBuildEngineerPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", testEnvConfig())

	keyPhrases := []string{
		"Engineer agent",
		"oak",
		"root",
		"dendra/oak",
		"dendra report done",
		"dendra report problem",
		"dendra messages send root",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("engineer prompt missing key phrase: %q", phrase)
		}
	}
}

func TestBuildEngineerPrompt_DoesNotContainTaskSection(t *testing.T) {
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", testEnvConfig())

	if strings.Contains(prompt, "YOUR TASK:") {
		t.Error("engineer prompt should not contain YOUR TASK section")
	}
	if strings.Contains(prompt, "implement login page") {
		t.Error("engineer prompt should not contain task-specific text")
	}
}

func TestBuildResearcherPrompt_DoesNotContainTaskSection(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "dendra/birch")

	if strings.Contains(prompt, "YOUR TASK:") {
		t.Error("researcher prompt should not contain YOUR TASK section")
	}
	if strings.Contains(prompt, "investigate auth libraries") {
		t.Error("researcher prompt should not contain task-specific text")
	}
}

func TestBuildResearcherPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "dendra/birch")

	keyPhrases := []string{
		"Researcher agent",
		"birch",
		"root",
		"dendra/birch",
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
	prompt := BuildResearcherPrompt("birch", "root", "dendra/birch")

	if strings.Contains(prompt, "hands-on builder") {
		t.Error("researcher prompt should not contain engineer role 'hands-on builder'")
	}
}

func TestBuildEngineerPrompt_DoesNotContainResearcherRole(t *testing.T) {
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", testEnvConfig())

	if strings.Contains(prompt, "deep investigator") {
		t.Error("engineer prompt should not contain researcher role 'deep investigator'")
	}
}

func TestBuildEngineerPrompt_TDDWorkflowIsMandatory(t *testing.T) {
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", testEnvConfig())

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
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", testEnvConfig())

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
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", testEnvConfig())

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
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", testEnvConfig())

	keyPhrases := []string{
		"Reflect",
		"code edits challenging",
		"unclear or confusing",
		"code quality issues",
		"Documentation gaps",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("engineer prompt missing reflection phrase: %q", phrase)
		}
	}
}

func TestBuildResearcherPrompt_ReflectionStep(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "dendra/birch")

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
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", testEnvConfig())

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

func TestBuildEngineerPrompt_EnvironmentSection(t *testing.T) {
	env := EnvConfig{
		WorkDir:  "/tmp/worktrees/oak",
		Platform: "linux",
		Shell:    "/bin/zsh",
	}
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", env)

	envPhrases := []string{
		"# Environment",
		"Working directory: /tmp/worktrees/oak",
		"Git repository: yes",
		"Git branch: dendra/oak",
		"Platform: linux",
		"Shell: /bin/zsh",
	}
	for _, phrase := range envPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("engineer prompt missing environment phrase: %q", phrase)
		}
	}
}

func TestBuildEngineerPrompt_EnvironmentOmitsEmptyFields(t *testing.T) {
	env := EnvConfig{} // all empty
	prompt := BuildEngineerPrompt("oak", "root", "dendra/oak", env)

	if strings.Contains(prompt, "Working directory:") {
		t.Error("should omit working directory when empty")
	}
	if strings.Contains(prompt, "Platform:") {
		t.Error("should omit platform when empty")
	}
	if strings.Contains(prompt, "Shell:") {
		t.Error("should omit shell when empty")
	}
	// These should always be present
	if !strings.Contains(prompt, "Git repository: yes") {
		t.Error("should always include git repository")
	}
	if !strings.Contains(prompt, "Git branch: dendra/oak") {
		t.Error("should always include git branch")
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

func TestBuildRootPrompt_ManagerTypeInAgentTypes(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("sensei"))

	keyPhrases := []string{
		"Manager",
		"--type manager",
		"3+ subtasks",
		"spawning an engineer directly",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("root prompt AGENT TYPES section missing phrase: %q", phrase)
		}
	}

	// Manager entry should appear after Engineer and Researcher but before AGENT FAMILIES
	managerIdx := strings.Index(prompt, "--type manager")
	engineerIdx := strings.Index(prompt, "--type engineer")
	researcherIdx := strings.Index(prompt, "--type researcher")
	familiesIdx := strings.Index(prompt, "AGENT FAMILIES")

	if managerIdx == -1 {
		t.Fatal("root prompt missing '--type manager'")
	}
	if managerIdx <= engineerIdx {
		t.Errorf("manager (idx %d) should appear after engineer (idx %d)", managerIdx, engineerIdx)
	}
	if managerIdx <= researcherIdx {
		t.Errorf("manager (idx %d) should appear after researcher (idx %d)", managerIdx, researcherIdx)
	}
	if managerIdx >= familiesIdx {
		t.Errorf("manager (idx %d) should appear before AGENT FAMILIES (idx %d)", managerIdx, familiesIdx)
	}
}

func TestBuildRootPrompt_PromptConfigStruct(t *testing.T) {
	cfg := PromptConfig{
		RootName: "sensei",
		AgentCLI: "claude-code",
	}
	prompt := BuildRootPrompt(cfg)
	if !strings.Contains(prompt, `Your name is "sensei"`) {
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
		"AGENT TYPES: DENDRA AGENTS vs CLAUDE SUB-AGENTS",
		"Dendra agents",
		"dendra spawn agent",
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
	if strings.Contains(prompt, "AGENT TYPES: DENDRA AGENTS vs CLAUDE SUB-AGENTS") {
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
	if strings.Contains(prompt, "AGENT TYPES: DENDRA AGENTS vs CLAUDE SUB-AGENTS") {
		t.Error("sub-agent guidance should not be included when AgentCLI is empty")
	}
}

func TestBuildRootPrompt_SubAgentGuidanceSectionOrdering(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("sensei"))

	agentTypesIdx := strings.Index(prompt, "AGENT TYPES: DENDRA AGENTS vs CLAUDE SUB-AGENTS")
	verifyIdx := strings.Index(prompt, "VERIFYING AGENT WORK:")

	if agentTypesIdx == -1 {
		t.Fatal("BuildRootPrompt missing 'AGENT TYPES: DENDRA AGENTS vs CLAUDE SUB-AGENTS'")
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
		"MUST verify",
		"run tests",
		"work tree",
		".dendra/agents/<name>/findings/",
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
	prompt := BuildResearcherPrompt("birch", "root", "dendra/birch")

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
		"dendra spawn agent --family",
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

	// The spawn commands should appear before messaging commands
	spawnIdx := strings.Index(prompt, "dendra spawn agent --family")
	inboxIdx := strings.Index(prompt, "dendra messages inbox")

	if spawnIdx == -1 {
		t.Fatal("root prompt missing 'dendra spawn agent --family'")
	}
	if inboxIdx == -1 {
		t.Fatal("root prompt missing 'dendra messages inbox'")
	}
	if spawnIdx >= inboxIdx {
		t.Errorf("spawn commands (idx %d) should appear before messaging commands (idx %d)", spawnIdx, inboxIdx)
	}
}

func TestBuildRootPrompt_InterpolatesIdentity(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("sensei"))
	if !strings.Contains(prompt, `Your name is "sensei"`) {
		t.Error("BuildRootPrompt should interpolate the root name")
	}

	prompt2 := BuildRootPrompt(defaultRootConfig("kai"))
	if !strings.Contains(prompt2, `Your name is "kai"`) {
		t.Error("BuildRootPrompt should interpolate custom root name")
	}
	if strings.Contains(prompt2, `Your name is "root"`) {
		t.Error("BuildRootPrompt should not hardcode 'root' name")
	}
}

// --- BuildManagerPrompt tests (TDD red phase — function does not exist yet) ---

func TestBuildManagerPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	keyPhrases := []string{
		"Manager agent",
		"cedar",
		"sensei",
		"dmotles/feature-x",
		"dendra report done",
		"dendra report problem",
		"dendra messages send sensei",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("manager prompt missing key phrase: %q", phrase)
		}
	}
}

func TestBuildManagerPrompt_ContainsIdentityWithFamily(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	if !strings.Contains(prompt, "engineering manager") {
		t.Errorf("manager prompt should contain 'engineering manager' (family interpolated into identity)")
	}
}

func TestBuildManagerPrompt_ContainsOrchestrationGuidance(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	orchestrationPhrases := []string{
		"orchestrate",
		"decompos",
		"dispatch",
		"verif",
		"integrat",
	}
	for _, phrase := range orchestrationPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("manager prompt missing orchestration phrase: %q", phrase)
		}
	}
}

func TestBuildManagerPrompt_ContainsMergeUsage(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	mergePhrases := []string{
		"dendra merge",
		"--dry-run",
		"--no-validate",
		"--force",
	}
	for _, phrase := range mergePhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("manager prompt missing merge phrase: %q", phrase)
		}
	}
}

func TestBuildManagerPrompt_ContainsParallelismGuidance(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	parallelismPhrases := []string{
		"PARALLELISM",
		"overlapping files",
		"merge conflicts",
		"Serialize when",
		"sequential execution",
	}
	for _, phrase := range parallelismPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("manager prompt missing parallelism phrase: %q", phrase)
		}
	}
}

func TestBuildManagerPrompt_ContainsFollowThroughGuidance(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	// Check that at least one alternative is present for each concept
	followThroughFound := strings.Contains(prompt, "FOLLOW THROUGH") || strings.Contains(prompt, "FOLLOW-THROUGH")
	if !followThroughFound {
		t.Errorf("manager prompt missing follow-through heading (expected 'FOLLOW THROUGH' or 'FOLLOW-THROUGH')")
	}

	scheduleFound := strings.Contains(prompt, "automatically schedule") || strings.Contains(prompt, "automatically fire off")
	if !scheduleFound {
		t.Errorf("manager prompt missing scheduling guidance (expected 'automatically schedule' or 'automatically fire off')")
	}

	waveFound := strings.Contains(prompt, "next wave") || strings.Contains(prompt, "next chunk")
	if !waveFound {
		t.Errorf("manager prompt missing wave/chunk guidance (expected 'next wave' or 'next chunk')")
	}
}

func TestBuildManagerPrompt_ContainsFailureHandling(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	abandonOrRespawn := strings.Contains(prompt, "abandon") || strings.Contains(prompt, "respawn")
	if !abandonOrRespawn {
		t.Errorf("manager prompt missing failure handling (expected 'abandon' or 'respawn')")
	}

	if !strings.Contains(prompt, "escalate") {
		t.Errorf("manager prompt missing failure handling phrase: %q", "escalate")
	}
}

func TestBuildManagerPrompt_ContainsIntegrationBranch(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	if !strings.Contains(prompt, "integration branch") {
		t.Errorf("manager prompt missing phrase: %q", "integration branch")
	}
}

func TestBuildManagerPrompt_ContainsSubAgentGuidance(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	if !strings.Contains(prompt, "Agent tool") {
		t.Errorf("manager prompt missing phrase: %q", "Agent tool")
	}

	investigationFound := strings.Contains(prompt, "investigation") || strings.Contains(prompt, "investigate")
	if !investigationFound {
		t.Errorf("manager prompt missing investigation guidance (expected 'investigation' or 'investigate')")
	}

	planningFound := strings.Contains(prompt, "planning") || strings.Contains(prompt, "plan")
	if !planningFound {
		t.Errorf("manager prompt missing planning guidance (expected 'planning' or 'plan')")
	}
}

func TestBuildManagerPrompt_DoesNotContainInteractiveLanguage(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	forbidden := []string{
		"ask the user",
		"the user will",
		"align with the user",
	}
	for _, phrase := range forbidden {
		if strings.Contains(prompt, phrase) {
			t.Errorf("manager prompt should NOT contain interactive language: %q", phrase)
		}
	}
}

func TestBuildManagerPrompt_DoesNotContainWrongRoles(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	forbidden := []string{
		"hands-on builder",
		"deep investigator",
	}
	for _, phrase := range forbidden {
		if strings.Contains(prompt, phrase) {
			t.Errorf("manager prompt should NOT contain wrong role: %q", phrase)
		}
	}
}

func TestBuildManagerPrompt_EnvironmentSection(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	envPhrases := []string{
		"# Environment",
		"Working directory: /tmp/worktrees/test",
		"Git repository: yes",
		"Git branch: dmotles/feature-x",
		"Platform: linux",
		"Shell: /bin/zsh",
	}
	for _, phrase := range envPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("manager prompt missing environment phrase: %q", phrase)
		}
	}
}

func TestBuildManagerPrompt_EnvironmentOmitsEmptyFields(t *testing.T) {
	env := EnvConfig{} // all empty
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", env)

	if strings.Contains(prompt, "Working directory:") {
		t.Error("should omit working directory when empty")
	}
	if strings.Contains(prompt, "Platform:") {
		t.Error("should omit platform when empty")
	}
	if strings.Contains(prompt, "Shell:") {
		t.Error("should omit shell when empty")
	}
	// These should always be present
	if !strings.Contains(prompt, "Git repository: yes") {
		t.Error("should always include git repository")
	}
	if !strings.Contains(prompt, "Git branch: dmotles/feature-x") {
		t.Error("should always include git branch")
	}
}

func TestBuildManagerPrompt_CannotEditCode(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	cannotEdit := strings.Contains(prompt, "You do NOT edit code") ||
		strings.Contains(prompt, "you don't implement") ||
		strings.Contains(prompt, "You orchestrate, you don't implement") ||
		strings.Contains(prompt, "do not edit code") ||
		strings.Contains(prompt, "do NOT edit code")
	if !cannotEdit {
		t.Errorf("manager prompt should make clear the manager does not edit code directly")
	}
}

func TestBuildManagerPrompt_ScopeManagement(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "sensei", "dmotles/feature-x", "engineering", testEnvConfig())

	if !strings.Contains(prompt, "scope") {
		t.Errorf("manager prompt should contain guidance about staying focused on scope")
	}
}

// --- BuildRootPrompt ContextBlob tests ---

func TestBuildRootPrompt_ContextBlob_Appended(t *testing.T) {
	cfg := PromptConfig{
		RootName:    "sensei",
		AgentCLI:    "claude-code",
		ContextBlob: "## Active State\n\nNo active agents.\n",
	}
	prompt := BuildRootPrompt(cfg)

	if !strings.Contains(prompt, "# Memory Context") {
		t.Error("prompt should contain '# Memory Context' heading when ContextBlob is set")
	}
	if !strings.Contains(prompt, "No active agents.") {
		t.Error("prompt should contain the context blob content")
	}

	// Context blob should appear after the main prompt content
	verifyIdx := strings.Index(prompt, "VERIFYING AGENT WORK")
	contextIdx := strings.Index(prompt, "# Memory Context")
	if verifyIdx == -1 || contextIdx == -1 {
		t.Fatal("expected both sections to exist")
	}
	if contextIdx < verifyIdx {
		t.Error("context blob should appear after VERIFYING AGENT WORK section")
	}
}

func TestBuildRootPrompt_ContextBlob_EmptyNotAppended(t *testing.T) {
	cfg := PromptConfig{
		RootName:    "sensei",
		AgentCLI:    "claude-code",
		ContextBlob: "",
	}
	prompt := BuildRootPrompt(cfg)

	if strings.Contains(prompt, "# Memory Context") {
		t.Error("prompt should NOT contain '# Memory Context' when ContextBlob is empty")
	}
}

func TestBuildRootPrompt_ContextBlob_WorksWithNonClaudeCodeCLI(t *testing.T) {
	cfg := PromptConfig{
		RootName:    "sensei",
		AgentCLI:    "codex",
		ContextBlob: "## Active State\n\nSome agents.\n",
	}
	prompt := BuildRootPrompt(cfg)

	if !strings.Contains(prompt, "# Memory Context") {
		t.Error("prompt should contain '# Memory Context' even with non-claude-code CLI")
	}
	if !strings.Contains(prompt, "Some agents.") {
		t.Error("prompt should contain context blob content with non-claude-code CLI")
	}
}

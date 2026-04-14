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
		TestMode: false,
	}
}

func TestBuildEngineerPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	keyPhrases := []string{
		"Engineer agent",
		"zone",
		"root",
		"sprawl/zone",
		"sprawl report done",
		"sprawl report problem",
		"sprawl messages send root",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("engineer prompt missing key phrase: %q", phrase)
		}
	}
}

func TestBuildEngineerPrompt_DoesNotContainTaskSection(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	if strings.Contains(prompt, "YOUR TASK:") {
		t.Error("engineer prompt should not contain YOUR TASK section")
	}
	if strings.Contains(prompt, "implement login page") {
		t.Error("engineer prompt should not contain task-specific text")
	}
}

func TestBuildResearcherPrompt_DoesNotContainTaskSection(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "sprawl/birch", testEnvConfig())

	if strings.Contains(prompt, "YOUR TASK:") {
		t.Error("researcher prompt should not contain YOUR TASK section")
	}
	if strings.Contains(prompt, "investigate auth libraries") {
		t.Error("researcher prompt should not contain task-specific text")
	}
}

func TestBuildResearcherPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "sprawl/birch", testEnvConfig())

	keyPhrases := []string{
		"Researcher agent",
		"birch",
		"root",
		"sprawl/birch",
		"sprawl report done",
		"sprawl report problem",
		"sprawl messages send root",
		"SPRAWL_AGENT_IDENTITY",
		"do NOT modify production code",
		"deep investigator",
		"document findings",
		"systematic analysis",
		"tradeoffs",
		".sprawl/agents/birch/findings/",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("researcher prompt missing key phrase: %q", phrase)
		}
	}
}

func TestBuildResearcherPrompt_DoesNotContainEngineerRole(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "sprawl/birch", testEnvConfig())

	if strings.Contains(prompt, "hands-on builder") {
		t.Error("researcher prompt should not contain engineer role 'hands-on builder'")
	}
}

func TestBuildEngineerPrompt_DoesNotContainResearcherRole(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	if strings.Contains(prompt, "deep investigator") {
		t.Error("engineer prompt should not contain researcher role 'deep investigator'")
	}
}

func TestBuildEngineerPrompt_TDDWorkflowIsMandatory(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

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
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

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
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

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
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

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
	prompt := BuildResearcherPrompt("birch", "root", "sprawl/birch", testEnvConfig())

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
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

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
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", env)

	envPhrases := []string{
		"# Environment",
		"Working directory: /tmp/worktrees/oak",
		"Git repository: yes",
		"Git branch: sprawl/zone",
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
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", env)

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
	if !strings.Contains(prompt, "Git branch: sprawl/zone") {
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
	if strings.Contains(BuildRootPrompt(defaultRootConfig("weave")), "respawn") {
		t.Error("BuildRootPrompt should not mention 'respawn' — the command was canceled (QUM-46)")
	}
}

func TestBuildRootPrompt_ManagerTypeInAgentTypes(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

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
		RootName: "weave",
		AgentCLI: "claude-code",
	}
	prompt := BuildRootPrompt(cfg)
	if !strings.Contains(prompt, `Your name is "weave"`) {
		t.Error("BuildRootPrompt should interpolate RootName from config")
	}
}

func TestBuildRootPrompt_SubAgentGuidance_ClaudeCode(t *testing.T) {
	cfg := PromptConfig{
		RootName: "weave",
		AgentCLI: "claude-code",
	}
	prompt := BuildRootPrompt(cfg)

	keyPhrases := []string{
		"AGENT TYPES: SPRAWL AGENTS vs CLAUDE SUB-AGENTS",
		"Sprawl agents",
		"sprawl spawn agent",
		"Claude Code sub-agents",
		"Agent tool",
		"fire off an agent",
		"spawn an agent",
		"sub-agent",
		"Default to sprawl agents for real work",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("root prompt (claude-code) missing sub-agent guidance phrase: %q", phrase)
		}
	}
}

func TestBuildRootPrompt_SubAgentGuidance_NotIncludedForUnknownCLI(t *testing.T) {
	cfg := PromptConfig{
		RootName: "weave",
		AgentCLI: "codex",
	}
	prompt := BuildRootPrompt(cfg)

	// Sub-agent guidance should NOT be present for non-claude-code CLIs
	if strings.Contains(prompt, "AGENT TYPES: SPRAWL AGENTS vs CLAUDE SUB-AGENTS") {
		t.Error("sub-agent guidance should not be included for non-claude-code CLI")
	}
	if strings.Contains(prompt, "Claude Code sub-agents") {
		t.Error("Claude Code sub-agent references should not be included for non-claude-code CLI")
	}
}

func TestBuildRootPrompt_SubAgentGuidance_EmptyCLI(t *testing.T) {
	cfg := PromptConfig{
		RootName: "weave",
		AgentCLI: "",
	}
	prompt := BuildRootPrompt(cfg)

	// Sub-agent guidance should NOT be present when AgentCLI is empty
	if strings.Contains(prompt, "AGENT TYPES: SPRAWL AGENTS vs CLAUDE SUB-AGENTS") {
		t.Error("sub-agent guidance should not be included when AgentCLI is empty")
	}
}

func TestBuildRootPrompt_SubAgentGuidanceSectionOrdering(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	agentTypesIdx := strings.Index(prompt, "AGENT TYPES: SPRAWL AGENTS vs CLAUDE SUB-AGENTS")
	verifyIdx := strings.Index(prompt, "VERIFYING AGENT WORK:")

	if agentTypesIdx == -1 {
		t.Fatal("BuildRootPrompt missing 'AGENT TYPES: SPRAWL AGENTS vs CLAUDE SUB-AGENTS'")
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
		".sprawl/agents/<name>/findings/",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(BuildRootPrompt(defaultRootConfig("weave")), phrase) {
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
		if !strings.Contains(BuildRootPrompt(defaultRootConfig("weave")), phrase) {
			t.Errorf("BuildRootPrompt missing parallelism guidance phrase: %q", phrase)
		}
	}
}

func TestBuildRootPrompt_ParallelismSectionOrdering(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))
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
	prompt := BuildResearcherPrompt("birch", "root", "sprawl/birch", testEnvConfig())

	reflectIdx := strings.Index(prompt, "REFLECTION")
	doneIdx := strings.Index(prompt, "sprawl report done")

	if reflectIdx == -1 {
		t.Fatal("researcher prompt missing 'REFLECTION'")
	}
	if doneIdx == -1 {
		t.Fatal("researcher prompt missing 'sprawl report done'")
	}

	if reflectIdx >= doneIdx {
		t.Errorf("'REFLECTION' (idx %d) should appear before 'sprawl report done' (idx %d)", reflectIdx, doneIdx)
	}
}

func TestBuildRootPrompt_KeyCommands_AllCommandsPresent(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	// Spawning & Lifecycle commands
	spawnLifecycleCommands := []string{
		"sprawl spawn agent --family",
		"sprawl spawn subagent --family",
		"sprawl delegate",
		"sprawl kill",
		"sprawl retire",
		"sprawl logs",
	}
	for _, cmd := range spawnLifecycleCommands {
		if !strings.Contains(prompt, cmd) {
			t.Errorf("root prompt KEY COMMANDS missing spawn/lifecycle command: %q", cmd)
		}
	}

	// Messaging commands
	messagingCommands := []string{
		"sprawl messages inbox",
		"sprawl messages send",
		"sprawl messages read",
		"sprawl messages list",
		"sprawl messages broadcast",
		"sprawl messages archive",
	}
	for _, cmd := range messagingCommands {
		if !strings.Contains(prompt, cmd) {
			t.Errorf("root prompt KEY COMMANDS missing messaging command: %q", cmd)
		}
	}
}

func TestBuildRootPrompt_KeyCommands_RetireDistinguishedFromKill(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	if !strings.Contains(prompt, "sprawl retire") {
		t.Error("root prompt should mention 'sprawl retire'")
	}
	if !strings.Contains(prompt, "sprawl kill") {
		t.Error("root prompt should mention 'sprawl kill'")
	}
	// retire should be described as full teardown / preferred cleanup
	retireIdx := strings.Index(prompt, "sprawl retire")
	killIdx := strings.Index(prompt, "sprawl kill")
	if retireIdx == -1 || killIdx == -1 {
		t.Fatal("both retire and kill must be present")
	}
	// They should be distinct entries (different lines)
	if retireIdx == killIdx {
		t.Error("retire and kill should be separate entries")
	}
}

func TestBuildRootPrompt_KeyCommands_GroupedLogically(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	// Verify KEY COMMANDS section exists
	if !strings.Contains(prompt, "KEY COMMANDS:") {
		t.Fatal("root prompt missing 'KEY COMMANDS:' section")
	}

	// The spawn commands should appear before messaging commands
	spawnIdx := strings.Index(prompt, "sprawl spawn agent --family")
	inboxIdx := strings.Index(prompt, "sprawl messages inbox")

	if spawnIdx == -1 {
		t.Fatal("root prompt missing 'sprawl spawn agent --family'")
	}
	if inboxIdx == -1 {
		t.Fatal("root prompt missing 'sprawl messages inbox'")
	}
	if spawnIdx >= inboxIdx {
		t.Errorf("spawn commands (idx %d) should appear before messaging commands (idx %d)", spawnIdx, inboxIdx)
	}
}

func TestBuildRootPrompt_InterpolatesIdentity(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))
	if !strings.Contains(prompt, `Your name is "weave"`) {
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
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	keyPhrases := []string{
		"Manager agent",
		"cedar",
		"weave",
		"dmotles/feature-x",
		"sprawl report done",
		"sprawl report problem",
		"sprawl messages send weave",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("manager prompt missing key phrase: %q", phrase)
		}
	}
}

func TestBuildManagerPrompt_ContainsIdentityWithFamily(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	if !strings.Contains(prompt, "engineering manager") {
		t.Errorf("manager prompt should contain 'engineering manager' (family interpolated into identity)")
	}
}

func TestBuildManagerPrompt_ContainsOrchestrationGuidance(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

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
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	mergePhrases := []string{
		"sprawl merge",
		"--dry-run",
		"--no-validate",
		"--message",
	}
	for _, phrase := range mergePhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("manager prompt missing merge phrase: %q", phrase)
		}
	}

	// --force flag on merge was removed in M12
	if strings.Contains(prompt, "--force") {
		t.Error("manager prompt should not reference --force flag (removed in M12)")
	}
}

func TestBuildManagerPrompt_ContainsRetireWorkflows(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	retirePhrases := []string{
		"retire --merge",
		"retire --abandon",
	}
	for _, phrase := range retirePhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("manager prompt missing retire workflow phrase: %q", phrase)
		}
	}
}

func TestBuildManagerPrompt_MergeDoesNotRetire(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	// Merge should describe pulling in work, not retiring
	if strings.Contains(prompt, "merge + retire + branch cleanup in one step") {
		t.Error("manager prompt should not describe merge as retire+cleanup in one step")
	}

	// Should mention agent stays alive
	if !strings.Contains(prompt, "stays alive") {
		t.Error("manager prompt should mention that agent stays alive after merge")
	}
}

func TestBuildManagerPrompt_ConflictRecovery(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	if !strings.Contains(prompt, "conflict") {
		t.Error("manager prompt should mention conflict recovery")
	}
}

func TestBuildManagerPrompt_ContainsParallelismGuidance(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

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
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

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
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	abandonOrRespawn := strings.Contains(prompt, "abandon") || strings.Contains(prompt, "respawn")
	if !abandonOrRespawn {
		t.Errorf("manager prompt missing failure handling (expected 'abandon' or 'respawn')")
	}

	if !strings.Contains(prompt, "escalate") {
		t.Errorf("manager prompt missing failure handling phrase: %q", "escalate")
	}
}

func TestBuildManagerPrompt_ContainsIntegrationBranch(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	if !strings.Contains(prompt, "integration branch") {
		t.Errorf("manager prompt missing phrase: %q", "integration branch")
	}
}

func TestBuildManagerPrompt_ContainsSubAgentGuidance(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

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
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

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
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

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
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

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
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", env)

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
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

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
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	if !strings.Contains(prompt, "scope") {
		t.Errorf("manager prompt should contain guidance about staying focused on scope")
	}
}

// --- delegate vs messages guidance tests ---

func TestBuildRootPrompt_DelegateVsMessagesGuidance(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	keyPhrases := []string{
		"sprawl delegate",
		"work assignments",
		"tracked task",
		"sprawl messages send",
		"coordination",
		"information sharing",
		"rule of thumb",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(phrase)) {
			t.Errorf("root prompt missing delegate vs messages guidance phrase: %q", phrase)
		}
	}
}

func TestBuildRootPrompt_DelegateVsMessagesGuidance_Ordering(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	guidanceIdx := strings.Index(prompt, "DELEGATE VS. MESSAGES")
	rulesIdx := strings.Index(prompt, "RULES:")

	if guidanceIdx == -1 {
		t.Fatal("root prompt missing 'DELEGATE VS. MESSAGES' section")
	}
	if rulesIdx == -1 {
		t.Fatal("root prompt missing 'RULES:'")
	}
	// Guidance should appear after KEY COMMANDS and before RULES
	keyCommandsIdx := strings.Index(prompt, "KEY COMMANDS:")
	if keyCommandsIdx == -1 {
		t.Fatal("root prompt missing 'KEY COMMANDS:'")
	}
	if guidanceIdx <= keyCommandsIdx {
		t.Errorf("DELEGATE VS. MESSAGES (idx %d) should appear after KEY COMMANDS (idx %d)", guidanceIdx, keyCommandsIdx)
	}
	if guidanceIdx >= rulesIdx {
		t.Errorf("DELEGATE VS. MESSAGES (idx %d) should appear before RULES (idx %d)", guidanceIdx, rulesIdx)
	}
}

func TestBuildManagerPrompt_DelegateVsMessagesGuidance(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	keyPhrases := []string{
		"sprawl delegate",
		"work assignments",
		"tracked task",
		"sprawl messages send",
		"coordination",
		"information sharing",
		"rule of thumb",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(phrase)) {
			t.Errorf("manager prompt missing delegate vs messages guidance phrase: %q", phrase)
		}
	}
}

func TestBuildManagerPrompt_DelegateVsMessagesGuidance_Ordering(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	guidanceIdx := strings.Index(prompt, "DELEGATE VS. MESSAGES")
	dispatchIdx := strings.Index(prompt, "# DISPATCHING:")

	if guidanceIdx == -1 {
		t.Fatal("manager prompt missing 'DELEGATE VS. MESSAGES' section")
	}
	if dispatchIdx == -1 {
		t.Fatal("manager prompt missing '# DISPATCHING:'")
	}
	// Guidance should appear after DISPATCHING
	if guidanceIdx <= dispatchIdx {
		t.Errorf("DELEGATE VS. MESSAGES (idx %d) should appear after DISPATCHING (idx %d)", guidanceIdx, dispatchIdx)
	}

	// And before PARALLELISM
	parallelismIdx := strings.Index(prompt, "# PARALLELISM")
	if parallelismIdx == -1 {
		t.Fatal("manager prompt missing '# PARALLELISM'")
	}
	if guidanceIdx >= parallelismIdx {
		t.Errorf("DELEGATE VS. MESSAGES (idx %d) should appear before PARALLELISM (idx %d)", guidanceIdx, parallelismIdx)
	}
}

// --- BuildRootPrompt ContextBlob tests ---

func TestBuildRootPrompt_ContextBlob_Appended(t *testing.T) {
	cfg := PromptConfig{
		RootName:    "weave",
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
		RootName:    "weave",
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
		RootName:    "weave",
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

// --- TestMode / SPRAWL_TEST_MODE tests ---

func TestBuildEngineerPrompt_TestMode_InjectsWarning(t *testing.T) {
	env := testEnvConfig()
	env.TestMode = true
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", env)

	if !strings.Contains(prompt, "TEST SANDBOX MODE") {
		t.Error("engineer prompt should contain 'TEST SANDBOX MODE' when TestMode is true")
	}
	if !strings.Contains(prompt, "$SPRAWL_ROOT") {
		t.Error("engineer prompt should reference $SPRAWL_ROOT in sandbox warning")
	}
	if !strings.Contains(prompt, "$SPRAWL_BIN") {
		t.Error("engineer prompt should reference $SPRAWL_BIN in sandbox warning")
	}
}

func TestBuildEngineerPrompt_TestMode_NoWarningWhenOff(t *testing.T) {
	env := testEnvConfig()
	env.TestMode = false
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", env)

	if strings.Contains(prompt, "TEST SANDBOX MODE") {
		t.Error("engineer prompt should NOT contain 'TEST SANDBOX MODE' when TestMode is false")
	}
}

func TestBuildManagerPrompt_TestMode_InjectsWarning(t *testing.T) {
	env := testEnvConfig()
	env.TestMode = true
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", env)

	if !strings.Contains(prompt, "TEST SANDBOX MODE") {
		t.Error("manager prompt should contain 'TEST SANDBOX MODE' when TestMode is true")
	}
	if !strings.Contains(prompt, "$SPRAWL_ROOT") {
		t.Error("manager prompt should reference $SPRAWL_ROOT in sandbox warning")
	}
	if !strings.Contains(prompt, "$SPRAWL_BIN") {
		t.Error("manager prompt should reference $SPRAWL_BIN in sandbox warning")
	}
}

func TestBuildManagerPrompt_TestMode_NoWarningWhenOff(t *testing.T) {
	env := testEnvConfig()
	env.TestMode = false
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", env)

	if strings.Contains(prompt, "TEST SANDBOX MODE") {
		t.Error("manager prompt should NOT contain 'TEST SANDBOX MODE' when TestMode is false")
	}
}

func TestBuildResearcherPrompt_TestMode_InjectsWarning(t *testing.T) {
	env := testEnvConfig()
	env.TestMode = true
	prompt := BuildResearcherPrompt("birch", "root", "sprawl/birch", env)

	if !strings.Contains(prompt, "TEST SANDBOX MODE") {
		t.Error("researcher prompt should contain 'TEST SANDBOX MODE' when TestMode is true")
	}
	if !strings.Contains(prompt, "$SPRAWL_ROOT") {
		t.Error("researcher prompt should reference $SPRAWL_ROOT in sandbox warning")
	}
	if !strings.Contains(prompt, "$SPRAWL_BIN") {
		t.Error("researcher prompt should reference $SPRAWL_BIN in sandbox warning")
	}
}

func TestBuildResearcherPrompt_TestMode_NoWarningWhenOff(t *testing.T) {
	env := testEnvConfig()
	env.TestMode = false
	prompt := BuildResearcherPrompt("birch", "root", "sprawl/birch", env)

	if strings.Contains(prompt, "TEST SANDBOX MODE") {
		t.Error("researcher prompt should NOT contain 'TEST SANDBOX MODE' when TestMode is false")
	}
}

func TestBuildRootPrompt_TestMode_InjectsWarning(t *testing.T) {
	cfg := PromptConfig{
		RootName: "weave",
		AgentCLI: "claude-code",
		TestMode: true,
	}
	prompt := BuildRootPrompt(cfg)

	if !strings.Contains(prompt, "TEST SANDBOX MODE") {
		t.Error("root prompt should contain 'TEST SANDBOX MODE' when TestMode is true")
	}
	if !strings.Contains(prompt, "$SPRAWL_ROOT") {
		t.Error("root prompt should reference $SPRAWL_ROOT in sandbox warning")
	}
	if !strings.Contains(prompt, "$SPRAWL_BIN") {
		t.Error("root prompt should reference $SPRAWL_BIN in sandbox warning")
	}
}

func TestBuildRootPrompt_TestMode_NoWarningWhenOff(t *testing.T) {
	cfg := PromptConfig{
		RootName: "weave",
		AgentCLI: "claude-code",
		TestMode: false,
	}
	prompt := BuildRootPrompt(cfg)

	if strings.Contains(prompt, "TEST SANDBOX MODE") {
		t.Error("root prompt should NOT contain 'TEST SANDBOX MODE' when TestMode is false")
	}
}

// M12 merge/retire workflow tests

func TestBuildRootPrompt_MergeRetireWorkflow(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	// Merge should not mention retiring or deleting branches
	if strings.Contains(prompt, "retire the agent, and delete the branch") {
		t.Error("root prompt should not describe merge as retiring+deleting")
	}

	// Should not reference --force flag on merge
	mergeIdx := strings.Index(prompt, "Merging & Branch Maintenance")
	if mergeIdx < 0 {
		t.Fatal("expected 'Merging & Branch Maintenance' section in prompt")
	}
	mergeSection := prompt[mergeIdx:]
	mergeEnd := strings.Index(mergeSection, "Messaging:")
	if mergeEnd > 0 {
		mergeSection = mergeSection[:mergeEnd]
	}
	if strings.Contains(mergeSection, "--force") {
		t.Error("root prompt merge section should not reference --force flag")
	}

	// Should describe merge as pulling in work
	if !strings.Contains(prompt, "stays alive") {
		t.Error("root prompt should mention agent stays alive after merge")
	}

	// Should have retire workflows
	retirePhrases := []string{
		"retire --merge",
		"retire --abandon",
	}
	for _, phrase := range retirePhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("root prompt missing retire workflow: %q", phrase)
		}
	}
}

func TestBuildRootPrompt_FlockSynchronization(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	// Should mention flock or lock-based synchronization
	if !strings.Contains(prompt, "lock") && !strings.Contains(prompt, "flock") {
		t.Error("root prompt should explain flock/lock synchronization during merge")
	}
}

func TestBuildRootPrompt_MergeConflictRecovery(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	if !strings.Contains(prompt, "conflict") {
		t.Error("root prompt should explain recovery from rebase conflicts")
	}
}

func TestBuildRootPrompt_NoStaleSquashMergeReferences(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	// The RULES section should not say merge handles "the full lifecycle"
	if strings.Contains(prompt, "squash-merge, retire the agent, and clean up in one step") {
		t.Error("root prompt RULES should not describe merge as full lifecycle cleanup")
	}
}

func TestBuildRootPrompt_SafeRetireGuidance(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	// Should guide toward safe retirement by default
	if !strings.Contains(prompt, "sprawl retire") || !strings.Contains(prompt, "Default to safe retirement") {
		t.Error("root prompt should include guidance to default to safe retirement")
	}

	// Should warn about researchers having committed artifacts
	if !strings.Contains(prompt, "retiring researchers") {
		t.Error("root prompt should warn about checking researcher artifacts before retiring")
	}
}

func TestBuildManagerPrompt_SafeRetireGuidance(t *testing.T) {
	prompt := BuildManagerPrompt("mgr1", "weave", "feature/mgr1", "engineering", testEnvConfig())

	// Should guide toward safe retirement by default
	if !strings.Contains(prompt, "Default to safe retirement") {
		t.Error("manager prompt should include guidance to default to safe retirement")
	}

	// Should warn about researchers having committed artifacts
	if !strings.Contains(prompt, "retiring researchers") {
		t.Error("manager prompt should warn about checking researcher artifacts before retiring")
	}
}

func TestBuildEngineerPrompt_BranchRebaseNotification(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "tower", "dmotles/feature-x", testEnvConfig())

	if !strings.Contains(prompt, "rebase") {
		t.Error("engineer prompt should mention that parent may rebase their branch")
	}
}

// --- Dual-mode prompt tests (tmux vs tui) ---

func TestResolveMode_DefaultsToTmux(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "tmux"},
		{"tmux", "tmux"},
		{"tui", "tui"},
	}
	for _, tt := range tests {
		got := resolveMode(tt.input)
		if got != tt.want {
			t.Errorf("resolveMode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildRootPrompt_DefaultMode_ContainsCLICommands(t *testing.T) {
	cfg := defaultRootConfig("weave")
	// Mode is empty (default)
	prompt := BuildRootPrompt(cfg)

	cliCommands := []string{
		"sprawl spawn agent",
		"sprawl messages send",
		"sprawl merge",
		"sprawl retire",
		"sprawl delegate",
	}
	for _, cmd := range cliCommands {
		if !strings.Contains(prompt, cmd) {
			t.Errorf("root prompt with default mode should contain CLI command %q", cmd)
		}
	}
}

func TestBuildRootPrompt_TuiMode_ContainsMCPTools(t *testing.T) {
	cfg := defaultRootConfig("weave")
	cfg.Mode = "tui"
	prompt := BuildRootPrompt(cfg)

	mcpTools := []string{
		"sprawl_spawn",
		"sprawl_message",
		"sprawl_merge",
		"sprawl_retire",
		"sprawl_delegate",
		"sprawl_kill",
		"sprawl_status",
	}
	for _, tool := range mcpTools {
		if !strings.Contains(prompt, tool) {
			t.Errorf("root prompt with tui mode should contain MCP tool %q", tool)
		}
	}
}

func TestBuildRootPrompt_TuiMode_NoCLISpawnCommand(t *testing.T) {
	cfg := defaultRootConfig("weave")
	cfg.Mode = "tui"
	prompt := BuildRootPrompt(cfg)

	if strings.Contains(prompt, "sprawl spawn agent") {
		t.Error("root prompt with tui mode should NOT contain CLI command 'sprawl spawn agent'")
	}
}

func TestBuildRootPrompt_TmuxMode_NoMCPToolNames(t *testing.T) {
	cfg := defaultRootConfig("weave")
	cfg.Mode = "tmux"
	prompt := BuildRootPrompt(cfg)

	if strings.Contains(prompt, "sprawl_spawn(") {
		t.Error("root prompt with tmux mode should NOT contain MCP tool call 'sprawl_spawn('")
	}
}

func TestBuildRootPrompt_SharedContent_BothModes(t *testing.T) {
	sharedPhrases := []string{
		"YOUR ROLE:",
		"orchestrator",
		"SPRAWL OVERVIEW",
		"AGENT FAMILIES",
		"PARALLELISM VS. SERIALIZATION",
		"VERIFYING AGENT WORK",
		"FOLLOW THROUGH",
	}

	for _, mode := range []string{"", "tmux", "tui"} {
		cfg := defaultRootConfig("weave")
		cfg.Mode = mode
		prompt := BuildRootPrompt(cfg)
		label := mode
		if label == "" {
			label = "(empty/default)"
		}

		for _, phrase := range sharedPhrases {
			if !strings.Contains(prompt, phrase) {
				t.Errorf("root prompt with mode %s should contain shared content %q", label, phrase)
			}
		}
	}
}

func TestBuildEngineerPrompt_DefaultMode_ContainsCLICommands(t *testing.T) {
	env := testEnvConfig()
	// Mode is empty (default)
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", env)

	cliCommands := []string{
		"sprawl report done",
		"sprawl messages send",
	}
	for _, cmd := range cliCommands {
		if !strings.Contains(prompt, cmd) {
			t.Errorf("engineer prompt with default mode should contain CLI command %q", cmd)
		}
	}
}

func TestBuildEngineerPrompt_TuiMode_ContainsMCPTools(t *testing.T) {
	env := testEnvConfig()
	env.Mode = "tui"
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", env)

	if !strings.Contains(prompt, "sprawl_message") {
		t.Error("engineer prompt with tui mode should contain MCP tool 'sprawl_message'")
	}
}

func TestBuildEngineerPrompt_TuiMode_NoCLIReportCommand(t *testing.T) {
	env := testEnvConfig()
	env.Mode = "tui"
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", env)

	if strings.Contains(prompt, "sprawl report done") {
		t.Error("engineer prompt with tui mode should NOT contain 'sprawl report done'")
	}
	if strings.Contains(prompt, "sprawl messages send") {
		t.Error("engineer prompt with tui mode should NOT contain 'sprawl messages send'")
	}
}

func TestBuildResearcherPrompt_TuiMode_ContainsMCPTools(t *testing.T) {
	env := testEnvConfig()
	env.Mode = "tui"
	prompt := BuildResearcherPrompt("birch", "root", "sprawl/birch", env)

	if !strings.Contains(prompt, "sprawl_message") {
		t.Error("researcher prompt with tui mode should contain MCP tool references")
	}
}

func TestBuildResearcherPrompt_TuiMode_NoCLICommands(t *testing.T) {
	env := testEnvConfig()
	env.Mode = "tui"
	prompt := BuildResearcherPrompt("birch", "root", "sprawl/birch", env)

	if strings.Contains(prompt, "sprawl report done") {
		t.Error("researcher prompt with tui mode should NOT contain CLI command 'sprawl report done'")
	}
	if strings.Contains(prompt, "sprawl messages send") {
		t.Error("researcher prompt with tui mode should NOT contain CLI command 'sprawl messages send'")
	}
}

func TestBuildManagerPrompt_DefaultMode_ContainsCLICommands(t *testing.T) {
	env := testEnvConfig()
	prompt := BuildManagerPrompt("mgr1", "weave", "feature/mgr1", "engineering", env)

	cliCommands := []string{
		"sprawl spawn agent",
		"sprawl merge",
		"sprawl retire",
		"sprawl delegate",
		"sprawl messages send",
	}
	for _, cmd := range cliCommands {
		if !strings.Contains(prompt, cmd) {
			t.Errorf("manager prompt with default mode should contain CLI command %q", cmd)
		}
	}
}

func TestBuildManagerPrompt_TuiMode_ContainsMCPTools(t *testing.T) {
	env := testEnvConfig()
	env.Mode = "tui"
	prompt := BuildManagerPrompt("mgr1", "weave", "feature/mgr1", "engineering", env)

	mcpTools := []string{
		"sprawl_spawn",
		"sprawl_merge",
		"sprawl_retire",
		"sprawl_delegate",
		"sprawl_message",
		"sprawl_status",
	}
	for _, tool := range mcpTools {
		if !strings.Contains(prompt, tool) {
			t.Errorf("manager prompt with tui mode should contain MCP tool %q", tool)
		}
	}
}

func TestBuildManagerPrompt_TuiMode_NoCLICommands(t *testing.T) {
	env := testEnvConfig()
	env.Mode = "tui"
	prompt := BuildManagerPrompt("mgr1", "weave", "feature/mgr1", "engineering", env)

	if strings.Contains(prompt, "sprawl spawn agent") {
		t.Error("manager prompt with tui mode should NOT contain 'sprawl spawn agent'")
	}
	if strings.Contains(prompt, "sprawl messages send") {
		t.Error("manager prompt with tui mode should NOT contain 'sprawl messages send'")
	}
}

func TestBuildManagerPrompt_SharedContent_BothModes(t *testing.T) {
	sharedPhrases := []string{
		"DECOMPOSITION:",
		"VERIFICATION:",
		"INTEGRATION:",
		"AGENT LIFECYCLE:",
		"PARALLELISM VS. SERIALIZATION",
	}

	for _, mode := range []string{"", "tui"} {
		env := testEnvConfig()
		env.Mode = mode
		prompt := BuildManagerPrompt("mgr1", "weave", "feature/mgr1", "engineering", env)
		label := mode
		if label == "" {
			label = "(empty/default)"
		}

		for _, phrase := range sharedPhrases {
			if !strings.Contains(prompt, phrase) {
				t.Errorf("manager prompt with mode %s should contain shared content %q", label, phrase)
			}
		}
	}
}

package agent

import (
	"os"
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

// TestBuildRootPrompt_NoAskUserQuestion pins QUM-528: the harness AskUserQuestion
// tool is deprecated; the root prompt must not instruct the agent to call it.
// The TUI-mode prompt should reference the replacement MCP tool name
// (`mcp__sprawl__ask_user_question`, QUM-527) instead.
func TestBuildRootPrompt_NoAskUserQuestion(t *testing.T) {
	cfg := PromptConfig{
		RootName: "weave",
		AgentCLI: "claude-code",
	}
	prompt := BuildRootPrompt(cfg)
	if strings.Contains(prompt, "AskUserQuestion") {
		t.Errorf("root prompt must not mention deprecated harness tool AskUserQuestion (QUM-528)")
	}
	if !strings.Contains(prompt, "mcp__sprawl__ask_user_question") {
		t.Errorf("root prompt must reference replacement tool mcp__sprawl__ask_user_question (QUM-527)")
	}
}

func TestBuildEngineerPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	keyPhrases := []string{
		"Engineer agent",
		"zone",
		"root",
		"sprawl/zone",
		"report_status",
		"send_message",
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
		"report_status",
		"send_message",
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
		"revise the test",
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

func TestBuildEngineerPrompt_PreservesSidechainNames(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	subAgents := []string{
		"oracle",
		"test-critic",
	}
	for _, agent := range subAgents {
		if !strings.Contains(prompt, agent) {
			t.Errorf("engineer prompt missing sidechain name: %q", agent)
		}
	}
}

// TestBuildEngineerPrompt_QAValidatorRemoved pins QUM-715: the engineer prompt
// must not invoke the old qa-validator Claude sidechain — QA is now a sprawl
// agent of type="qa" spawned by the manager after the engineer reports done.
func TestBuildEngineerPrompt_QAValidatorRemoved(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	// The phrase "qa-validator" must not appear as an action the engineer
	// invokes. We allow the literal token only in explanatory context that
	// names it as REMOVED — so guard by a stricter check: there should be no
	// numbered TDD step that calls qa-validator.
	if strings.Contains(prompt, "qa-validator — Validate") {
		t.Error("engineer prompt still invokes qa-validator sidechain (QUM-715: removed)")
	}
	// Engineer must be told manager spawns the QA sprawl agent.
	for _, phrase := range []string{
		`QA agent`,
		`type="qa"`,
	} {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("engineer prompt missing QA routing phrase %q (QUM-715)", phrase)
		}
	}
}

// TestBuildEngineerPrompt_CodeReviewerIsSubAgentSpawn pins QUM-714: the
// engineer's TDD step-5 code review is no longer a Claude sidechain; it is
// a sprawl sub-agent spawned directly by the engineer (subagent:true so the
// reviewer shares the engineer's worktree).
func TestBuildEngineerPrompt_CodeReviewerIsSubAgentSpawn(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	required := []string{
		"subagent: true",
		`type: "engineer"`,
		"send_message",
		"shares your worktree",
	}
	for _, phrase := range required {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("engineer prompt missing sub-agent spawn phrase: %q (QUM-714)", phrase)
		}
	}
}

// TestBuildEngineerPrompt_CodeReviewerNotInSidechainPreamble pins QUM-714:
// the TDD preamble must no longer list "code-reviewer" among the Claude
// sidechains, since it is now a sprawl sub-agent spawn.
func TestBuildEngineerPrompt_CodeReviewerNotInSidechainPreamble(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	if strings.Contains(prompt, `"oracle", "test-critic", "code-reviewer"`) {
		t.Errorf(`engineer prompt sidechain preamble still lists "code-reviewer" alongside oracle/test-critic (QUM-714)`)
	}
}

func TestBuildEngineerPrompt_DoesNotInvokeRemovedSidechains(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	// QUM-712: engineer IS the writer and implementer; these sidechains were removed.
	for _, removed := range []string{"test-writer", "implementer"} {
		if strings.Contains(prompt, removed) {
			t.Errorf("engineer prompt must not reference removed sidechain %q (QUM-712)", removed)
		}
	}
}

func TestBuildEngineerPrompt_InlineWriteAndImplementGuidance(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	// QUM-712: explicit inline "write failing test, then implement to green" guidance.
	required := []string{
		"Write a failing test FIRST",
		"You write the test yourself",
		"Implement to green",
		"You write the implementation yourself",
	}
	for _, phrase := range required {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("engineer prompt missing inline guidance phrase: %q", phrase)
		}
	}
}

func TestBuildEngineerPrompt_PreservesWorkflowOrder(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	// Verify the workflow steps appear in order. The "Code review sub-agent"
	// step replaced the code-reviewer sidechain (QUM-714); the qa-validator
	// step was removed entirely (QUM-715: now a sprawl QA agent).
	steps := []string{"oracle", "test-critic", "Code review sub-agent"}
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
		"comment on the tracking issue",
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

	// QUM-714 renamed the code-reviewer step to "Code review sub-agent".
	reviewerIdx := strings.Index(prompt, "Code review sub-agent")
	reflectIdx := strings.Index(prompt, "Reflect")
	doneIdx := strings.Index(prompt, "Report done via:")

	if reviewerIdx == -1 {
		t.Fatal("engineer prompt missing 'Code review sub-agent'")
	}
	if reflectIdx == -1 {
		t.Fatal("engineer prompt missing 'Reflect'")
	}
	if doneIdx == -1 {
		t.Fatal("engineer prompt missing 'Report done via:'")
	}

	if reflectIdx <= reviewerIdx {
		t.Errorf("'Reflect' (idx %d) should appear after 'Code review sub-agent' (idx %d)", reflectIdx, reviewerIdx)
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
		`type: "manager"`,
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("root prompt AGENT TYPES section missing phrase: %q", phrase)
		}
	}

	// Manager entry should appear before AGENT FAMILIES.
	managerIdx := strings.Index(prompt, `type: "manager"`)
	familiesIdx := strings.Index(prompt, "AGENT FAMILIES")

	if managerIdx == -1 {
		t.Fatal(`root prompt missing 'type: "manager"'`)
	}
	if familiesIdx == -1 {
		t.Fatal("root prompt missing 'AGENT FAMILIES'")
	}
	if managerIdx >= familiesIdx {
		t.Errorf("manager (idx %d) should appear before AGENT FAMILIES (idx %d)", managerIdx, familiesIdx)
	}
}

// TestBuildRootPrompt_OrchestrationStandardization (QUM-718) locks the
// weave → manager → (engineer + QA) orchestration shape in the root prompt.
func TestBuildRootPrompt_OrchestrationStandardization(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	if !strings.Contains(prompt, "spawn a manager") {
		t.Errorf("root prompt should mention 'spawn a manager' (QUM-718)")
	}
	if strings.Contains(prompt, "prefer spawning an engineer directly") {
		t.Errorf("root prompt must NOT contain deleted line 'prefer spawning an engineer directly' (QUM-718)")
	}
}

// TestBuildManagerPrompt_RequiresQADispatch (QUM-718) locks the
// manager-owned QA dispatch requirement.
func TestBuildManagerPrompt_RequiresQADispatch(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	if !strings.Contains(prompt, "MUST dispatch a QA pass") {
		t.Errorf("manager prompt should contain 'MUST dispatch a QA pass' (QUM-718)")
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

func TestBuildRootPrompt_SidechainGuidance_ClaudeCode(t *testing.T) {
	cfg := PromptConfig{
		RootName: "weave",
		AgentCLI: "claude-code",
	}
	prompt := BuildRootPrompt(cfg)

	keyPhrases := []string{
		"AGENT TYPES: SPRAWL AGENTS vs CLAUDE SIDECHAINS",
		"Sprawl agents",
		"spawn",
		"Claude Code sidechains",
		"Agent tool",
		"fire off an agent",
		"spawn an agent",
		"sidechain",
		"Default to sprawl agents for real work",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("root prompt (claude-code) missing sidechain guidance phrase: %q", phrase)
		}
	}
}

func TestBuildRootPrompt_SidechainGuidance_NotIncludedForUnknownCLI(t *testing.T) {
	cfg := PromptConfig{
		RootName: "weave",
		AgentCLI: "codex",
	}
	prompt := BuildRootPrompt(cfg)

	// Sidechain guidance should NOT be present for non-claude-code CLIs
	if strings.Contains(prompt, "AGENT TYPES: SPRAWL AGENTS vs CLAUDE SIDECHAINS") {
		t.Error("sidechain guidance should not be included for non-claude-code CLI")
	}
	if strings.Contains(prompt, "Claude Code sidechains") {
		t.Error("Claude Code sidechain references should not be included for non-claude-code CLI")
	}
}

func TestBuildRootPrompt_SidechainGuidance_EmptyCLI(t *testing.T) {
	cfg := PromptConfig{
		RootName: "weave",
		AgentCLI: "",
	}
	prompt := BuildRootPrompt(cfg)

	// Sidechain guidance should NOT be present when AgentCLI is empty
	if strings.Contains(prompt, "AGENT TYPES: SPRAWL AGENTS vs CLAUDE SIDECHAINS") {
		t.Error("sidechain guidance should not be included when AgentCLI is empty")
	}
}

func TestBuildRootPrompt_SidechainGuidanceSectionOrdering(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	agentTypesIdx := strings.Index(prompt, "AGENT TYPES: SPRAWL AGENTS vs CLAUDE SIDECHAINS")
	verifyIdx := strings.Index(prompt, "VERIFYING AGENT WORK:")

	if agentTypesIdx == -1 {
		t.Fatal("BuildRootPrompt missing 'AGENT TYPES: SPRAWL AGENTS vs CLAUDE SIDECHAINS'")
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
	doneIdx := strings.Index(prompt, `report_status({state: "complete"`)

	if reflectIdx == -1 {
		t.Fatal("researcher prompt missing 'REFLECTION'")
	}
	if doneIdx == -1 {
		t.Fatal("researcher prompt missing 'report_status({state: \"complete\"'")
	}

	if reflectIdx >= doneIdx {
		t.Errorf("'REFLECTION' (idx %d) should appear before report_status done line (idx %d)", reflectIdx, doneIdx)
	}
}

func TestBuildRootPrompt_KeyCommands_AllCommandsPresent(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	// Spawning & Lifecycle tools
	spawnLifecycleCommands := []string{
		"spawn({",
		"delegate({",
		"kill({",
		"retire({",
	}
	for _, cmd := range spawnLifecycleCommands {
		if !strings.Contains(prompt, cmd) {
			t.Errorf("root prompt KEY TOOLS missing spawn/lifecycle tool: %q", cmd)
		}
	}

	// Messaging tools
	messagingCommands := []string{
		"send_message({",
		"peek({",
		"report_status({",
	}
	for _, cmd := range messagingCommands {
		if !strings.Contains(prompt, cmd) {
			t.Errorf("root prompt KEY TOOLS missing messaging tool: %q", cmd)
		}
	}
}

func TestBuildRootPrompt_KeyCommands_RetireDistinguishedFromKill(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	if !strings.Contains(prompt, "retire({") {
		t.Error("root prompt should mention 'retire({'")
	}
	if !strings.Contains(prompt, "kill({") {
		t.Error("root prompt should mention 'kill({'")
	}
	retireIdx := strings.Index(prompt, "retire({")
	killIdx := strings.Index(prompt, "kill({")
	if retireIdx == -1 || killIdx == -1 {
		t.Fatal("both retire and kill must be present")
	}
	if retireIdx == killIdx {
		t.Error("retire and kill should be separate entries")
	}
}

func TestBuildRootPrompt_KeyCommands_GroupedLogically(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	// Verify KEY TOOLS section exists
	if !strings.Contains(prompt, "KEY TOOLS (MCP):") {
		t.Fatal("root prompt missing 'KEY TOOLS (MCP):' section")
	}

	// The spawn tool should appear before send_message
	spawnIdx := strings.Index(prompt, "spawn({")
	msgIdx := strings.Index(prompt, "send_message({")

	if spawnIdx == -1 {
		t.Fatal("root prompt missing 'spawn({'")
	}
	if msgIdx == -1 {
		t.Fatal("root prompt missing 'send_message({'")
	}
	if spawnIdx >= msgIdx {
		t.Errorf("spawn tool (idx %d) should appear before messaging tool (idx %d)", spawnIdx, msgIdx)
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
		"report_status",
		`send_message({to: "weave"`,
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
		"merge({agent:",
		"no_validate: true",
		`message: "<msg>"`,
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
		"merge: true",
		"abandon: true",
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

func TestBuildManagerPrompt_ContainsSidechainGuidance(t *testing.T) {
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
		"the user will primarily request",
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
		"delegate({",
		"work assignments",
		"tracked task",
		"send_message({",
		"coordination",
		"information sharing",
		"rules of thumb",
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
	rulesIdx := strings.LastIndex(prompt, "RULES:")

	if guidanceIdx == -1 {
		t.Fatal("root prompt missing 'DELEGATE VS. MESSAGES' section")
	}
	if rulesIdx == -1 {
		t.Fatal("root prompt missing 'RULES:'")
	}
	// Guidance should appear after KEY TOOLS and before RULES
	keyCommandsIdx := strings.Index(prompt, "KEY TOOLS (MCP):")
	if keyCommandsIdx == -1 {
		t.Fatal("root prompt missing 'KEY TOOLS (MCP):'")
	}
	if guidanceIdx <= keyCommandsIdx {
		t.Errorf("DELEGATE VS. MESSAGES (idx %d) should appear after KEY TOOLS (idx %d)", guidanceIdx, keyCommandsIdx)
	}
	if guidanceIdx >= rulesIdx {
		t.Errorf("DELEGATE VS. MESSAGES (idx %d) should appear before RULES (idx %d)", guidanceIdx, rulesIdx)
	}
}

func TestBuildManagerPrompt_DelegateVsMessagesGuidance(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	keyPhrases := []string{
		"delegate({",
		"work assignments",
		"tracked task",
		"send_message({",
		"coordination",
		"information sharing",
		"rules of thumb",
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
	mergeIdx := strings.Index(prompt, "Merging:")
	if mergeIdx < 0 {
		t.Fatal("expected 'Merging:' section in prompt")
	}
	mergeSection := prompt[mergeIdx:]
	mergeEnd := strings.Index(mergeSection, "Messaging")
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
		"merge: true",
		"abandon: true",
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
	if !strings.Contains(prompt, "retire({") || !strings.Contains(prompt, "Default to safe retirement") {
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

// --- TUI-mode prompt tests ---

func TestBuildRootPrompt_ContainsMCPTools(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	mcpTools := []string{
		"spawn",
		"send_message",
		"peek",
		"report_status",
		"merge",
		"retire",
		"delegate",
		"kill",
		"status",
		"handoff",
	}
	for _, tool := range mcpTools {
		if !strings.Contains(prompt, tool) {
			t.Errorf("root prompt should contain MCP tool %q", tool)
		}
	}
}

func TestBuildRootPrompt_NoCLISpawnCommand(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	if strings.Contains(prompt, "sprawl spawn agent") {
		t.Error("root prompt should NOT contain CLI command 'sprawl spawn agent'")
	}
}

func TestBuildRootPrompt_SharedContent(t *testing.T) {
	sharedPhrases := []string{
		"YOUR ROLE:",
		"orchestrator",
		"SPRAWL OVERVIEW",
		"AGENT FAMILIES",
		"PARALLELISM VS. SERIALIZATION",
		"VERIFYING AGENT WORK",
		"FOLLOW THROUGH",
	}

	prompt := BuildRootPrompt(defaultRootConfig("weave"))
	for _, phrase := range sharedPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("root prompt should contain shared content %q", phrase)
		}
	}
}

func TestBuildEngineerPrompt_ContainsMCPTools(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	required := []string{"send_message", "report_status"}
	for _, tool := range required {
		if !strings.Contains(prompt, tool) {
			t.Errorf("engineer prompt should contain MCP tool %q", tool)
		}
	}
}

func TestBuildEngineerPrompt_NoCLIReportCommand(t *testing.T) {
	prompt := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())

	if strings.Contains(prompt, "sprawl report done") {
		t.Error("engineer prompt should NOT contain 'sprawl report done'")
	}
	if strings.Contains(prompt, "sprawl messages send") {
		t.Error("engineer prompt should NOT contain 'sprawl messages send'")
	}
}

func TestBuildResearcherPrompt_ContainsMCPTools(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "sprawl/birch", testEnvConfig())

	required := []string{"send_message", "report_status"}
	for _, tool := range required {
		if !strings.Contains(prompt, tool) {
			t.Errorf("researcher prompt should contain MCP tool %q", tool)
		}
	}
}

func TestBuildResearcherPrompt_NoCLICommands(t *testing.T) {
	prompt := BuildResearcherPrompt("birch", "root", "sprawl/birch", testEnvConfig())

	if strings.Contains(prompt, "sprawl report done") {
		t.Error("researcher prompt should NOT contain CLI command 'sprawl report done'")
	}
	if strings.Contains(prompt, "sprawl messages send") {
		t.Error("researcher prompt should NOT contain CLI command 'sprawl messages send'")
	}
}

func TestBuildManagerPrompt_ContainsMCPTools(t *testing.T) {
	prompt := BuildManagerPrompt("mgr1", "weave", "feature/mgr1", "engineering", testEnvConfig())

	mcpTools := []string{
		"spawn",
		"merge",
		"retire",
		"delegate",
		"send_message",
		"peek",
		"report_status",
		"status",
	}
	for _, tool := range mcpTools {
		if !strings.Contains(prompt, tool) {
			t.Errorf("manager prompt should contain MCP tool %q", tool)
		}
	}
}

func TestBuildManagerPrompt_NoCLICommands(t *testing.T) {
	prompt := BuildManagerPrompt("mgr1", "weave", "feature/mgr1", "engineering", testEnvConfig())

	if strings.Contains(prompt, "sprawl spawn agent") {
		t.Error("manager prompt should NOT contain 'sprawl spawn agent'")
	}
	if strings.Contains(prompt, "sprawl messages send") {
		t.Error("manager prompt should NOT contain 'sprawl messages send'")
	}
}

// --- TUI mode regression: no tmux or CLI-only references ---

func TestBuildRootPrompt_NoTmuxReferences(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	if strings.Contains(prompt, "tmux") {
		t.Error("root prompt should NOT contain any 'tmux' references")
	}
}

func TestBuildRootPrompt_NoCLIOnlyCommandPatterns(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	cliOnlyPatterns := []string{
		"sprawl spawn agent",
		"sprawl spawn subagent",
		"sprawl messages send",
		"sprawl messages inbox",
		"sprawl messages read",
		"sprawl messages list",
		"sprawl messages broadcast",
		"sprawl messages archive",
		"sprawl report done",
		"sprawl retire --merge",
		"sprawl retire --abandon",
		"sprawl merge <agent-name>",
		"sprawl cleanup branches",
		"sprawl logs <agent",
		"(via --family)",
		"--type engineer",
		"--dry-run",
		"--no-validate",
		"--message/-m",
	}
	for _, pat := range cliOnlyPatterns {
		if strings.Contains(prompt, pat) {
			t.Errorf("root prompt should NOT contain CLI-only pattern %q", pat)
		}
	}
}

func TestBuildRootPrompt_DoesNotAdvertiseLegacySubagentSpawn(t *testing.T) {
	prompt := BuildRootPrompt(defaultRootConfig("weave"))

	for _, pat := range []string{
		"sprawl spawn subagent",
		`spawn({type: "<type>", family: "<family>", prompt: "<task>"})`,
		"omit branch for subagent",
	} {
		if strings.Contains(prompt, pat) {
			t.Errorf("root prompt should not advertise legacy subagent path %q", pat)
		}
	}
}

func TestBuildManagerPrompt_DoesNotAdvertiseLegacySubagentSpawn(t *testing.T) {
	prompt := BuildManagerPrompt("mgr1", "weave", "feature/mgr1", "engineering", testEnvConfig())

	for _, pat := range []string{
		"sprawl spawn subagent",
		`spawn({type: "<type>", family: "<family>", prompt: "<task>"})`,
	} {
		if strings.Contains(prompt, pat) {
			t.Errorf("manager prompt should not advertise legacy subagent path %q", pat)
		}
	}
}

// TestBuildRootPrompt_GoldenSnapshot_TuiMode locks down the exact TUI-mode root
// prompt output (with claude-code CLI).
func TestBuildRootPrompt_GoldenSnapshot_TuiMode(t *testing.T) {
	cfg := PromptConfig{
		RootName: "weave",
		AgentCLI: "claude-code",
	}
	got := BuildRootPrompt(cfg)

	golden, err := os.ReadFile("testdata/golden_tui_claude_code.txt")
	if err != nil {
		t.Fatalf("failed to read golden file: %v", err)
	}
	if got != string(golden) {
		g := string(golden)
		diffIdx := 0
		for diffIdx < len(got) && diffIdx < len(g) && got[diffIdx] == g[diffIdx] {
			diffIdx++
		}
		context := 80
		start := diffIdx - context
		if start < 0 {
			start = 0
		}
		end := diffIdx + context
		if end > len(got) {
			end = len(got)
		}
		endG := diffIdx + context
		if endG > len(g) {
			endG = len(g)
		}
		t.Fatalf("tui mode output differs from golden snapshot at byte %d\ngot context:    %q\ngolden context: %q",
			diffIdx, got[start:end], g[start:endG])
	}
}

// TestBuildRootPrompt_ComprehensiveNoCLIReferences exhaustively checks
// that the root prompt has absolutely zero tmux/CLI-only references.
func TestBuildRootPrompt_ComprehensiveNoCLIReferences(t *testing.T) {
	cfg := PromptConfig{
		RootName: "weave",
		AgentCLI: "claude-code",
	}
	got := BuildRootPrompt(cfg)

	// Must not contain tmux references
	if strings.Contains(got, "tmux") {
		t.Error("TUI mode must not contain 'tmux'")
	}

	// Must not contain CLI-only command patterns
	forbidden := []string{
		"sprawl spawn agent",
		"sprawl spawn subagent",
		"sprawl messages send",
		"sprawl messages inbox",
		"sprawl messages read",
		"sprawl messages list",
		"sprawl messages broadcast",
		"sprawl messages archive",
		"sprawl report done",
		"sprawl report problem",
		"sprawl retire --merge",
		"sprawl retire --abandon",
		"sprawl merge <agent-name>",
		"sprawl cleanup branches",
		"sprawl logs <agent",
		"(via --family)",
		"--type engineer",
		"--type researcher",
		"--type manager",
		"--dry-run",
		"--no-validate",
		"--message/-m",
		"--yes",
	}
	for _, pat := range forbidden {
		if strings.Contains(got, pat) {
			t.Errorf("TUI mode must not contain CLI-only pattern %q", pat)
		}
	}

	// Must contain MCP tool references
	required := []string{
		"spawn",
		"send_message",
		"peek",
		"report_status",
		"merge",
		"retire",
		"delegate",
		"kill",
		"status",
		"handoff",
	}
	for _, tool := range required {
		if !strings.Contains(got, tool) {
			t.Errorf("TUI mode must contain MCP tool %q", tool)
		}
	}
}

func TestBuildManagerPrompt_SharedContent(t *testing.T) {
	sharedPhrases := []string{
		"DECOMPOSITION:",
		"VERIFICATION:",
		"INTEGRATION:",
		"AGENT LIFECYCLE:",
		"PARALLELISM VS. SERIALIZATION",
	}

	prompt := BuildManagerPrompt("mgr1", "weave", "feature/mgr1", "engineering", testEnvConfig())
	for _, phrase := range sharedPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("manager prompt should contain shared content %q", phrase)
		}
	}
}

// --- Golden snapshot regression tests ---
// These capture the exact output of each child prompt builder and assert
// character-for-character identity. If you change prompt content intentionally,
// regenerate golden files: GENERATE_GOLDEN=1 go test ./internal/agent/ -run TestGenerateGoldenFiles

func readGolden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading golden file testdata/%s: %v", name, err)
	}
	return string(data)
}

func TestGenerateGoldenFiles(t *testing.T) {
	if os.Getenv("GENERATE_GOLDEN") != "1" {
		t.Skip("set GENERATE_GOLDEN=1 to regenerate golden files")
	}
	env := testEnvConfig()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile("testdata/"+name, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		t.Logf("wrote testdata/%s (%d bytes)", name, len(content))
	}
	write("engineer_tui.golden", BuildEngineerPrompt("zone", "root", "sprawl/zone", env))
	write("researcher_tui.golden", BuildResearcherPrompt("birch", "root", "sprawl/birch", env))
	write("manager_tui.golden", BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", env))
	write("qa_tui.golden", BuildQAPrompt("inspector", "tower", "dmotles/feature-x", env))
	// Root-prompt goldens.
	write("golden_tui_claude_code.txt", BuildRootPrompt(PromptConfig{RootName: "weave", AgentCLI: "claude-code"}))
}

func TestBuildEngineerPrompt_TuiGolden(t *testing.T) {
	got := BuildEngineerPrompt("zone", "root", "sprawl/zone", testEnvConfig())
	want := readGolden(t, "engineer_tui.golden")
	if got != want {
		t.Fatalf("engineer tui prompt does not match golden snapshot.\nGot length: %d, Want length: %d\nFirst diff at byte %d",
			len(got), len(want), firstDiffIndex(got, want))
	}
}

func TestBuildResearcherPrompt_TuiGolden(t *testing.T) {
	got := BuildResearcherPrompt("birch", "root", "sprawl/birch", testEnvConfig())
	want := readGolden(t, "researcher_tui.golden")
	if got != want {
		t.Fatalf("researcher tui prompt does not match golden snapshot.\nGot length: %d, Want length: %d\nFirst diff at byte %d",
			len(got), len(want), firstDiffIndex(got, want))
	}
}

func TestBuildManagerPrompt_TuiGolden(t *testing.T) {
	got := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())
	want := readGolden(t, "manager_tui.golden")
	if got != want {
		t.Fatalf("manager tui prompt does not match golden snapshot.\nGot length: %d, Want length: %d\nFirst diff at byte %d",
			len(got), len(want), firstDiffIndex(got, want))
	}
}

func firstDiffIndex(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}

// --- BuildQAPrompt tests (QUM-707, TDD red phase — function does not exist yet) ---

func TestBuildQAPrompt_DoesNotContainTaskSection(t *testing.T) {
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", testEnvConfig())

	if strings.Contains(prompt, "YOUR TASK:") {
		t.Error("qa prompt should not contain YOUR TASK section")
	}
}

func TestBuildQAPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", testEnvConfig())

	keyPhrases := []string{
		"inspector",
		"tower",
		"dmotles/feature-x",
		"report_status",
		"send_message",
		"verdict",
		"git fetch",
		"git diff",
		"make validate",
		"Linear",
		"VERIFICATION PROTOCOL",
	}
	// QA agent identity should be referenced (case-insensitive).
	if !strings.Contains(prompt, "QA agent") && !strings.Contains(prompt, "qa agent") {
		t.Error("qa prompt should identify itself as 'QA agent' or 'qa agent'")
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("qa prompt missing key phrase: %q", phrase)
		}
	}
}

func TestBuildQAPrompt_DoesNotContainEngineerRole(t *testing.T) {
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", testEnvConfig())

	if strings.Contains(prompt, "hands-on builder") {
		t.Error("qa prompt should not contain engineer role 'hands-on builder'")
	}
	if strings.Contains(prompt, "TDD WORKFLOW (MANDATORY)") {
		t.Error("qa prompt should not contain engineer-only TDD WORKFLOW section")
	}
}

func TestBuildQAPrompt_VerificationProtocol(t *testing.T) {
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", testEnvConfig())

	keyPhrases := []string{
		"VERIFICATION PROTOCOL",
		"acceptance criteria",
		"git fetch",
		"git diff",
		"make validate",
		"engineer-not-done",
		"pass",
		"fail",
		"needs-rework",
	}
	for _, phrase := range keyPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("qa prompt missing verification protocol phrase: %q", phrase)
		}
	}
}

func TestBuildQAPrompt_VerificationProtocolOrder(t *testing.T) {
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", testEnvConfig())

	peekIdx := strings.Index(prompt, "peek")
	gitDiffIdx := strings.Index(prompt, "git diff")
	makeValidateIdx := strings.Index(prompt, "make validate")
	linearIdx := strings.Index(prompt, "Linear")
	sendMsgIdx := strings.Index(prompt, "send_message")
	reportStatusIdx := strings.Index(prompt, "report_status")

	if peekIdx == -1 {
		t.Fatal("qa prompt missing 'peek'")
	}
	if gitDiffIdx == -1 {
		t.Fatal("qa prompt missing 'git diff'")
	}
	if makeValidateIdx == -1 {
		t.Fatal("qa prompt missing 'make validate'")
	}
	if linearIdx == -1 {
		t.Fatal("qa prompt missing 'Linear'")
	}
	if sendMsgIdx == -1 {
		t.Fatal("qa prompt missing 'send_message'")
	}
	if reportStatusIdx == -1 {
		t.Fatal("qa prompt missing 'report_status'")
	}

	type entry struct {
		name string
		idx  int
	}
	order := []entry{
		{"peek", peekIdx},
		{"git diff", gitDiffIdx},
		{"make validate", makeValidateIdx},
		{"Linear", linearIdx},
		{"send_message", sendMsgIdx},
		{"report_status", reportStatusIdx},
	}
	for i := 1; i < len(order); i++ {
		if order[i].idx <= order[i-1].idx {
			t.Errorf("expected %q (idx %d) to appear after %q (idx %d)",
				order[i].name, order[i].idx, order[i-1].name, order[i-1].idx)
		}
	}
}

func TestBuildQAPrompt_RulesForbidProductionEdits(t *testing.T) {
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", testEnvConfig())

	// Must explicitly forbid production code edits.
	if !strings.Contains(prompt, "production code") {
		t.Error("qa prompt should mention 'production code' (in forbidding edits)")
	}
	if !strings.Contains(prompt, "Do NOT") {
		t.Error("qa prompt should contain 'Do NOT' rule (forbidding production edits)")
	}

	// Must mention forbidden actions: spawn, merge, push.
	for _, phrase := range []string{"spawn", "merge", "push"} {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("qa prompt should mention forbidden action %q in rules", phrase)
		}
	}
}

func TestBuildQAPrompt_ReflectionStep(t *testing.T) {
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", testEnvConfig())

	// Reflection step is required (mirror researcher conventions).
	if !strings.Contains(prompt, "REFLECTION") && !strings.Contains(prompt, "Reflect") {
		t.Error("qa prompt should contain a reflection step (REFLECTION or Reflect)")
	}
}

func TestBuildQAPrompt_ReflectionBeforeDone(t *testing.T) {
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", testEnvConfig())

	reflectIdx := strings.Index(prompt, "REFLECTION")
	if reflectIdx == -1 {
		reflectIdx = strings.Index(prompt, "Reflect")
	}
	doneIdx := strings.Index(prompt, `report_status({state: "complete"`)
	if doneIdx == -1 {
		doneIdx = strings.Index(prompt, "Report done")
	}

	if reflectIdx == -1 {
		t.Fatal("qa prompt missing reflection step")
	}
	if doneIdx == -1 {
		t.Fatal("qa prompt missing done-report step")
	}
	if reflectIdx >= doneIdx {
		t.Errorf("reflection (idx %d) should appear before done-report (idx %d)", reflectIdx, doneIdx)
	}
}

func TestBuildQAPrompt_TestMode_InjectsWarning(t *testing.T) {
	env := testEnvConfig()
	env.TestMode = true
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", env)

	if !strings.Contains(prompt, "TEST SANDBOX MODE") {
		t.Error("qa prompt should contain 'TEST SANDBOX MODE' when TestMode is true")
	}
	if !strings.Contains(prompt, "$SPRAWL_ROOT") {
		t.Error("qa prompt should reference $SPRAWL_ROOT in sandbox warning")
	}
	if !strings.Contains(prompt, "$SPRAWL_BIN") {
		t.Error("qa prompt should reference $SPRAWL_BIN in sandbox warning")
	}
}

func TestBuildQAPrompt_TestMode_NoWarningWhenOff(t *testing.T) {
	env := testEnvConfig()
	env.TestMode = false
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", env)

	if strings.Contains(prompt, "TEST SANDBOX MODE") {
		t.Error("qa prompt should NOT contain 'TEST SANDBOX MODE' when TestMode is false")
	}
}

func TestBuildQAPrompt_NoCLICommands(t *testing.T) {
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", testEnvConfig())

	if strings.Contains(prompt, "sprawl report done") {
		t.Error("qa prompt should NOT contain CLI command 'sprawl report done'")
	}
	if strings.Contains(prompt, "sprawl messages send") {
		t.Error("qa prompt should NOT contain CLI command 'sprawl messages send'")
	}
}

func TestBuildQAPrompt_ContainsMCPTools(t *testing.T) {
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", testEnvConfig())

	required := []string{"send_message", "report_status", "peek"}
	for _, tool := range required {
		if !strings.Contains(prompt, tool) {
			t.Errorf("qa prompt should contain MCP tool %q", tool)
		}
	}
}

func TestBuildQAPrompt_EnvironmentSection(t *testing.T) {
	env := EnvConfig{
		WorkDir:  "/tmp/worktrees/inspector",
		Platform: "linux",
		Shell:    "/bin/zsh",
	}
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", env)

	envPhrases := []string{
		"# Environment",
		"Working directory: /tmp/worktrees/inspector",
		"Git repository: yes",
		"Git branch: dmotles/feature-x",
		"Platform: linux",
		"Shell: /bin/zsh",
	}
	for _, phrase := range envPhrases {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("qa prompt missing environment phrase: %q", phrase)
		}
	}
}

func TestBuildQAPrompt_EnvironmentOmitsEmptyFields(t *testing.T) {
	env := EnvConfig{} // all empty
	prompt := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", env)

	if strings.Contains(prompt, "Working directory:") {
		t.Error("should omit working directory when empty")
	}
	if strings.Contains(prompt, "Platform:") {
		t.Error("should omit platform when empty")
	}
	if strings.Contains(prompt, "Shell:") {
		t.Error("should omit shell when empty")
	}
	if !strings.Contains(prompt, "Git repository: yes") {
		t.Error("should always include git repository")
	}
	if !strings.Contains(prompt, "Git branch: dmotles/feature-x") {
		t.Error("should always include git branch")
	}
}

func TestBuildQAPrompt_TuiGolden(t *testing.T) {
	got := BuildQAPrompt("inspector", "tower", "dmotles/feature-x", testEnvConfig())
	want := readGolden(t, "qa_tui.golden")
	if got != want {
		t.Fatalf("qa tui prompt does not match golden snapshot.\nGot length: %d, Want length: %d\nFirst diff at byte %d",
			len(got), len(want), firstDiffIndex(got, want))
	}
}

// --- Cross-prompt regression: QA must be listed in AGENT TYPES section ---

// TestBuildRootPrompt_QATypeInAgentTypes was removed in QUM-718: the new
// root prompt intentionally drops QA from the spawn listing because manager
// owns QA dispatch (weave → manager → engineer + QA).

func TestBuildManagerPrompt_QATypeInAgentTypes(t *testing.T) {
	prompt := BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", testEnvConfig())

	if !strings.Contains(prompt, "QA") && !strings.Contains(prompt, "qa") {
		t.Error("manager prompt AGENT TYPES section should mention QA")
	}
	if !strings.Contains(prompt, `type: "qa"`) {
		t.Errorf(`manager prompt AGENT TYPES section missing 'type: "qa"'`)
	}
}

// TestPromptRenderers_NoResidualPlaceholderTokens (QUM-539) guards the
// `{{PLACEHOLDER}}` + strings.ReplaceAll templating idiom in prompt_mode.go
// against typoed placeholder tokens (e.g. `{{AGNETNAME}}` vs `{{AGENTNAME}}`).
// A typoed placeholder would silently leak through to the rendered prompt and
// ship to the agent without any signal. This test enumerates every prompt
// renderer across the full agent-type × mode matrix and asserts the rendered
// output contains no residual `{{` substring.
func TestPromptRenderers_NoResidualPlaceholderTokens(t *testing.T) {
	const (
		agentName  = "zone"
		parentName = "weave"
		branchName = "dmotles/zone-test"
	)

	env := EnvConfig{
		WorkDir:  "/tmp/worktrees/zone",
		Platform: "linux",
		Shell:    "/bin/zsh",
	}

	cases := []struct {
		name   string
		render func() string
	}{
		{"root", func() string {
			return BuildRootPrompt(PromptConfig{
				RootName:    "weave",
				AgentCLI:    "claude-code",
				ContextBlob: "context blob",
			})
		}},
		{"root-no-cli", func() string {
			return BuildRootPrompt(PromptConfig{
				RootName: "weave",
				AgentCLI: "",
			})
		}},
		{"engineer", func() string {
			return BuildEngineerPrompt(agentName, parentName, branchName, env)
		}},
		{"researcher", func() string {
			return BuildResearcherPrompt(agentName, parentName, branchName, env)
		}},
		{"manager", func() string {
			return BuildManagerPrompt(agentName, parentName, branchName, "engineering", env)
		}},
		{"qa", func() string {
			return BuildQAPrompt(agentName, parentName, branchName, env)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prompt := tc.render()
			if idx := strings.Index(prompt, "{{"); idx >= 0 {
				start := idx - 40
				if start < 0 {
					start = 0
				}
				end := idx + 80
				if end > len(prompt) {
					end = len(prompt)
				}
				t.Errorf("rendered %s prompt contains residual `{{` placeholder token at offset %d — typoed placeholder? context: %q",
					tc.name, idx, prompt[start:end])
			}
		})
	}
}

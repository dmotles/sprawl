package agent

import (
	"strings"
	"testing"
)

func TestBuildArgs_AllOptions(t *testing.T) {
	launcher := &RealLauncher{}
	opts := LaunchOpts{
		SystemPrompt:    "test prompt",
		InitialPrompt:   "start working",
		Tools:           []string{"Bash", "Read"},
		AllowedTools:    []string{"Bash"},
		DisallowedTools: []string{"Edit"},
		Name:            "test-session",
		Bare:            true,
	}

	args := launcher.BuildArgs(opts)

	assertContains(t, args, "--system-prompt", "test prompt")
	// InitialPrompt should be the last positional argument, not a -p flag
	lastArg := args[len(args)-1]
	if lastArg != "start working" {
		t.Errorf("expected last arg to be initial prompt, got %q", lastArg)
	}
	assertContains(t, args, "--tools", "Bash")
	assertContains(t, args, "--tools", "Read")
	assertContains(t, args, "--allowed-tools", "Bash")
	assertContains(t, args, "--disallowed-tools", "Edit")
	assertContains(t, args, "--name", "test-session")
	assertContainsFlag(t, args, "--bare")
}

func TestBuildArgs_Empty(t *testing.T) {
	launcher := &RealLauncher{}
	args := launcher.BuildArgs(LaunchOpts{})

	if len(args) != 0 {
		t.Errorf("expected no args for empty opts, got %v", args)
	}
}

func TestBuildArgs_InitialPrompt(t *testing.T) {
	launcher := &RealLauncher{}
	prompt := "You have been assigned a task. Read your system prompt and begin working immediately."
	args := launcher.BuildArgs(LaunchOpts{
		SystemPrompt:  "system",
		InitialPrompt: prompt,
		Name:          "test",
	})

	// InitialPrompt must be the LAST arg (positional), after all flags
	lastArg := args[len(args)-1]
	if lastArg != prompt {
		t.Errorf("expected last arg to be initial prompt, got %q", lastArg)
	}

	// Must NOT use -p/--print (that's non-interactive exit mode)
	for _, a := range args {
		if a == "-p" || a == "--print" {
			t.Errorf("InitialPrompt must not use -p/--print flag (non-interactive mode), got %v", args)
		}
	}
}

func TestBuildArgs_NoInitialPrompt(t *testing.T) {
	launcher := &RealLauncher{}
	args := launcher.BuildArgs(LaunchOpts{Name: "test"})

	// With no InitialPrompt, there should be no stray positional arg
	// and no -p flag
	for _, a := range args {
		if a == "-p" || a == "--print" {
			t.Error("expected no -p/--print flag when InitialPrompt is empty")
		}
	}
}

func TestBuildArgs_InitialPromptComesLast(t *testing.T) {
	launcher := &RealLauncher{}
	args := launcher.BuildArgs(LaunchOpts{
		SystemPrompt:  "sys",
		Name:          "agent",
		Bare:          true,
		InitialPrompt: "begin work",
	})

	// The positional prompt must come after all flags
	lastArg := args[len(args)-1]
	if lastArg != "begin work" {
		t.Errorf("InitialPrompt should be the last argument, got %q; full args: %v", lastArg, args)
	}

	// Verify flags are present before it
	assertContains(t, args, "--system-prompt", "sys")
	assertContains(t, args, "--name", "agent")
	assertContainsFlag(t, args, "--bare")
}

func TestBuildArgs_NoBare(t *testing.T) {
	launcher := &RealLauncher{}
	args := launcher.BuildArgs(LaunchOpts{Name: "test"})

	for _, a := range args {
		if a == "--bare" {
			t.Error("expected no --bare flag when Bare is false")
		}
	}
}

func TestBuildRootPrompt_ContainsKeyPhrases(t *testing.T) {
	phrases := []string{
		"dendra spawn",
		"sensei",
		"DO NOT edit code",
		"--type manager",
		"--type engineer",
		"--type researcher",
		"--family",
		"dendra messages",
	}

	for _, phrase := range phrases {
		if !strings.Contains(BuildRootPrompt("sensei"), phrase) {
			t.Errorf("root system prompt missing key phrase: %q", phrase)
		}
	}
}

func TestBuildArgs_DangerouslySkipPermissions(t *testing.T) {
	launcher := &RealLauncher{}
	args := launcher.BuildArgs(LaunchOpts{
		Name:                       "test",
		DangerouslySkipPermissions: true,
	})
	assertContainsFlag(t, args, "--dangerously-skip-permissions")
}

func TestBuildArgs_Agents(t *testing.T) {
	launcher := &RealLauncher{}
	agentsJSON := `{"oracle":{"description":"Plans","prompt":"You plan"}}`
	args := launcher.BuildArgs(LaunchOpts{
		Name:   "test",
		Agents: agentsJSON,
	})
	assertContains(t, args, "--agents", agentsJSON)
}

func TestBuildArgs_Agents_Empty(t *testing.T) {
	launcher := &RealLauncher{}
	args := launcher.BuildArgs(LaunchOpts{Name: "test"})
	for _, a := range args {
		if a == "--agents" {
			t.Error("expected no --agents flag when Agents is empty")
		}
	}
}

func TestBuildArgs_DangerouslySkipPermissions_False(t *testing.T) {
	launcher := &RealLauncher{}
	args := launcher.BuildArgs(LaunchOpts{
		Name:                       "test",
		DangerouslySkipPermissions: false,
	})
	for _, a := range args {
		if a == "--dangerously-skip-permissions" {
			t.Error("expected no --dangerously-skip-permissions flag when false")
		}
	}
}

func TestEngineerSystemPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildEngineerPrompt("frank", "root", "dendra/frank", "implement login page")
	phrases := []string{
		"frank",
		"root",
		"dendra/frank",
		"implement login page",
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

func TestBuildRootPrompt_ContainsTesterType(t *testing.T) {
	if !strings.Contains(BuildRootPrompt("sensei"), "--type tester") {
		t.Error("root system prompt missing --type tester")
	}
}

func assertContains(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return
		}
	}
	t.Errorf("args %v missing %s %s", args, flag, value)
}

func assertContainsFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, a := range args {
		if a == flag {
			return
		}
	}
	t.Errorf("args %v missing flag %s", args, flag)
}

package agent

import (
	"strings"
	"testing"
)

func TestBuildArgs_AllOptions(t *testing.T) {
	launcher := &RealLauncher{}
	opts := LaunchOpts{
		SystemPrompt:    "test prompt",
		Tools:           []string{"Bash", "Read"},
		AllowedTools:    []string{"Bash"},
		DisallowedTools: []string{"Edit"},
		Name:            "test-session",
		Bare:            true,
	}

	args := launcher.BuildArgs(opts)

	assertContains(t, args, "--system-prompt", "test prompt")
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

func TestBuildArgs_NoBare(t *testing.T) {
	launcher := &RealLauncher{}
	args := launcher.BuildArgs(LaunchOpts{Name: "test"})

	for _, a := range args {
		if a == "--bare" {
			t.Error("expected no --bare flag when Bare is false")
		}
	}
}

func TestRootSystemPrompt_ContainsKeyPhrases(t *testing.T) {
	phrases := []string{
		"dendra spawn",
		"root",
		"DO NOT edit code",
		"--type manager",
		"--type engineer",
		"--type researcher",
		"--family",
		"dendra messages",
	}

	for _, phrase := range phrases {
		if !strings.Contains(RootSystemPrompt, phrase) {
			t.Errorf("root system prompt missing key phrase: %q", phrase)
		}
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

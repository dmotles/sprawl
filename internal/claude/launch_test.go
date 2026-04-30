package claude

import (
	"slices"
	"testing"
)

func TestBuildArgs_AllOptions(t *testing.T) {
	opts := LaunchOpts{
		SystemPrompt:    "test prompt",
		InitialPrompt:   "start working",
		Tools:           []string{"Bash", "Read"},
		AllowedTools:    []string{"Bash"},
		DisallowedTools: []string{"Edit"},
		Name:            "test-session",
		Bare:            true,
	}

	args := opts.BuildArgs()

	assertContains(t, args, "--system-prompt", "test prompt")
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
	args := LaunchOpts{}.BuildArgs()

	if len(args) != 0 {
		t.Errorf("expected no args for empty opts, got %v", args)
	}
}

func TestBuildArgs_InitialPrompt(t *testing.T) {
	prompt := "You have been assigned a task. Read your system prompt and begin working immediately."
	opts := LaunchOpts{
		SystemPrompt:  "system",
		InitialPrompt: prompt,
		Name:          "test",
	}

	args := opts.BuildArgs()

	lastArg := args[len(args)-1]
	if lastArg != prompt {
		t.Errorf("expected last arg to be initial prompt, got %q", lastArg)
	}

	for _, a := range args {
		if a == "-p" || a == "--print" {
			t.Errorf("InitialPrompt must not use -p/--print flag (non-interactive mode), got %v", args)
		}
	}
}

func TestBuildArgs_NoInitialPrompt(t *testing.T) {
	args := LaunchOpts{Name: "test"}.BuildArgs()

	for _, a := range args {
		if a == "-p" || a == "--print" {
			t.Error("expected no -p/--print flag when InitialPrompt is empty")
		}
	}
}

func TestBuildArgs_InitialPromptComesLast(t *testing.T) {
	opts := LaunchOpts{
		SystemPrompt:  "sys",
		Name:          "agent",
		Bare:          true,
		InitialPrompt: "begin work",
	}

	args := opts.BuildArgs()

	lastArg := args[len(args)-1]
	if lastArg != "begin work" {
		t.Errorf("InitialPrompt should be the last argument, got %q; full args: %v", lastArg, args)
	}

	assertContains(t, args, "--system-prompt", "sys")
	assertContains(t, args, "--name", "agent")
	assertContainsFlag(t, args, "--bare")
}

func TestBuildArgs_NoBare(t *testing.T) {
	args := LaunchOpts{Name: "test"}.BuildArgs()

	for _, a := range args {
		if a == "--bare" {
			t.Error("expected no --bare flag when Bare is false")
		}
	}
}

func TestBuildArgs_DangerouslySkipPermissions(t *testing.T) {
	args := LaunchOpts{
		Name:                       "test",
		DangerouslySkipPermissions: true,
	}.BuildArgs()
	assertContainsFlag(t, args, "--dangerously-skip-permissions")
}

func TestBuildArgs_DangerouslySkipPermissions_False(t *testing.T) {
	args := LaunchOpts{
		Name:                       "test",
		DangerouslySkipPermissions: false,
	}.BuildArgs()
	for _, a := range args {
		if a == "--dangerously-skip-permissions" {
			t.Error("expected no --dangerously-skip-permissions flag when false")
		}
	}
}

func TestBuildArgs_Agents(t *testing.T) {
	agentsJSON := `{"oracle":{"description":"Plans","prompt":"You plan"}}`
	args := LaunchOpts{
		Name:   "test",
		Agents: agentsJSON,
	}.BuildArgs()
	assertContains(t, args, "--agents", agentsJSON)
}

func TestBuildArgs_Agents_Empty(t *testing.T) {
	args := LaunchOpts{Name: "test"}.BuildArgs()
	for _, a := range args {
		if a == "--agents" {
			t.Error("expected no --agents flag when Agents is empty")
		}
	}
}

func TestBuildArgs_SystemPromptFile(t *testing.T) {
	args := LaunchOpts{
		SystemPromptFile: "/tmp/SYSTEM.md",
		Name:             "test",
	}.BuildArgs()

	assertContains(t, args, "--system-prompt-file", "/tmp/SYSTEM.md")

	for _, a := range args {
		if a == "--system-prompt" {
			t.Error("expected no --system-prompt when SystemPromptFile is set")
		}
	}
}

func TestBuildArgs_SystemPromptFilePrecedence(t *testing.T) {
	args := LaunchOpts{
		SystemPrompt:     "inline prompt",
		SystemPromptFile: "/tmp/SYSTEM.md",
		Name:             "test",
	}.BuildArgs()

	assertContains(t, args, "--system-prompt-file", "/tmp/SYSTEM.md")

	for _, a := range args {
		if a == "--system-prompt" {
			t.Error("SystemPromptFile should take precedence over SystemPrompt")
		}
	}
}

func TestBuildArgs_StreamJsonMode(t *testing.T) {
	opts := LaunchOpts{
		Print:          true,
		InputFormat:    "stream-json",
		OutputFormat:   "stream-json",
		Verbose:        true,
		Model:          "sonnet",
		Effort:         "medium",
		PermissionMode: "bypassPermissions",
		SessionID:      "sess-1",
		SystemPrompt:   "you are helpful",
	}

	args := opts.BuildArgs()

	assertContainsFlag(t, args, "-p")
	assertContains(t, args, "--input-format", "stream-json")
	assertContains(t, args, "--output-format", "stream-json")
	assertContainsFlag(t, args, "--verbose")
	assertContains(t, args, "--model", "sonnet")
	assertContains(t, args, "--effort", "medium")
	assertContains(t, args, "--permission-mode", "bypassPermissions")
	assertContains(t, args, "--session-id", "sess-1")
	assertContains(t, args, "--system-prompt", "you are helpful")
}

func TestBuildArgs_ModelAndEffort(t *testing.T) {
	args := LaunchOpts{
		Model:  "sonnet",
		Effort: "medium",
	}.BuildArgs()

	assertContains(t, args, "--model", "sonnet")
	assertContains(t, args, "--effort", "medium")
}

func TestBuildArgs_Resume(t *testing.T) {
	args := LaunchOpts{
		SessionID:    "sess-42",
		SystemPrompt: "prompt",
		Resume:       true,
	}.BuildArgs()

	// --resume carries the session ID; --session-id must NOT be emitted
	// separately because Claude Code rejects the combination.
	assertContains(t, args, "--resume", "sess-42")
	for i, a := range args {
		if a == "--session-id" {
			t.Errorf("expected no --session-id flag when Resume=true, got args %v (index %d)", args, i)
		}
	}

	// --system-prompt SHOULD still be emitted alongside --resume.
	assertContains(t, args, "--system-prompt", "prompt")
}

func TestBuildArgs_Resume_KeepsSystemPromptFile(t *testing.T) {
	args := LaunchOpts{
		SessionID:        "sess-42",
		SystemPromptFile: "/tmp/SYSTEM.md",
		Resume:           true,
	}.BuildArgs()

	assertContains(t, args, "--resume", "sess-42")
	assertContains(t, args, "--system-prompt-file", "/tmp/SYSTEM.md")
}

func TestBuildArgs_Resume_KeepsSystemPrompt(t *testing.T) {
	args := LaunchOpts{
		SessionID:    "sess-42",
		SystemPrompt: "inline",
		Resume:       true,
	}.BuildArgs()

	assertContains(t, args, "--resume", "sess-42")
	assertContains(t, args, "--system-prompt", "inline")
}

func TestBuildArgs_NoResume_KeepsSessionIDAndPromptFile(t *testing.T) {
	args := LaunchOpts{
		SessionID:        "sess-42",
		SystemPromptFile: "/tmp/SYSTEM.md",
		Resume:           false,
	}.BuildArgs()

	assertContains(t, args, "--session-id", "sess-42")
	assertContains(t, args, "--system-prompt-file", "/tmp/SYSTEM.md")
	for _, a := range args {
		if a == "--resume" {
			t.Errorf("expected no --resume when Resume=false, got %v", args)
		}
	}
}

func TestBuildArgs_SettingSources(t *testing.T) {
	args := LaunchOpts{
		SettingSources: "project",
	}.BuildArgs()

	assertContains(t, args, "--setting-sources", "project")
}

func TestBuildArgs_VerboseAfterFormat(t *testing.T) {
	opts := LaunchOpts{
		Print:       true,
		InputFormat: "stream-json",
		Verbose:     true,
		Model:       "sonnet",
		SessionID:   "s1",
	}

	args := opts.BuildArgs()

	// Verify --verbose comes after --input-format and before --model
	verboseIdx := -1
	modelIdx := -1
	for i, arg := range args {
		if arg == "--verbose" {
			verboseIdx = i
		}
		if arg == "--model" {
			modelIdx = i
		}
	}
	if verboseIdx == -1 {
		t.Fatal("--verbose not found in args")
	}
	if modelIdx == -1 {
		t.Fatal("--model not found in args")
	}
	if modelIdx <= verboseIdx {
		t.Errorf("--model (index %d) should come after --verbose (index %d)", modelIdx, verboseIdx)
	}
}

func TestBuildArgs_ContainsExpectedFlags(t *testing.T) {
	opts := LaunchOpts{
		Print:          true,
		InputFormat:    "stream-json",
		OutputFormat:   "stream-json",
		Verbose:        true,
		Model:          "sonnet",
		Effort:         "medium",
		PermissionMode: "bypassPermissions",
		SessionID:      "sess-1",
		SystemPrompt:   "you are helpful",
		Resume:         true,
	}

	args := opts.BuildArgs()

	// Note: --session-id is intentionally NOT expected here — Resume=true
	// suppresses it. But --system-prompt IS emitted alongside --resume.
	expected := map[string]bool{
		"-p": false, "--input-format": false, "--output-format": false,
		"--verbose": false, "--model": false, "--effort": false,
		"--permission-mode": false,
		"--resume":          false,
		"--system-prompt":   false,
	}
	for _, arg := range args {
		if _, ok := expected[arg]; ok {
			expected[arg] = true
		}
	}
	for flag, found := range expected {
		if !found {
			t.Errorf("expected flag %q not found in args", flag)
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
	if slices.Contains(args, flag) {
		return
	}
	t.Errorf("args %v missing flag %s", args, flag)
}

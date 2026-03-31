package agentloop

import (
	"testing"
)

func TestBuildClaudeArgs_IncludesModelOpus(t *testing.T) {
	config := ProcessConfig{
		SessionID: "test-session",
	}

	args := buildClaudeArgs(config)

	// Verify --model opus is present and comes after --verbose.
	verboseIdx := -1
	modelIdx := -1
	for i, arg := range args {
		if arg == "--verbose" {
			verboseIdx = i
		}
		if arg == "--model" && i+1 < len(args) && args[i+1] == "opus[1m]" {
			modelIdx = i
		}
	}

	if verboseIdx == -1 {
		t.Fatal("--verbose not found in args")
	}
	if modelIdx == -1 {
		t.Fatal("--model opus not found in args")
	}
	if modelIdx <= verboseIdx {
		t.Errorf("--model (index %d) should come after --verbose (index %d)", modelIdx, verboseIdx)
	}
}

func TestBuildClaudeArgs_ContainsExpectedFlags(t *testing.T) {
	config := ProcessConfig{
		SessionID:    "sess-1",
		SystemPrompt: "you are helpful",
		Resume:       true,
	}

	args := buildClaudeArgs(config)

	// Check a few expected flags exist.
	expected := map[string]bool{
		"-p": false, "--input-format": false, "--output-format": false,
		"--verbose": false, "--model": false, "--permission-mode": false,
		"--session-id": false, "--system-prompt": false, "--resume": false,
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

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealLauncher_FindBinary_SprawlClaudeOverride(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "run-claude")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec claude \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	t.Setenv("SPRAWL_CLAUDE", shim)

	l := &RealLauncher{}
	got, err := l.FindBinary()
	if err != nil {
		t.Fatalf("FindBinary returned error: %v", err)
	}
	if got != shim {
		t.Errorf("FindBinary = %q, want %q", got, shim)
	}
}

func TestRealLauncher_FindBinary_SprawlClaudeMissing(t *testing.T) {
	t.Setenv("SPRAWL_CLAUDE", filepath.Join(t.TempDir(), "does-not-exist"))

	l := &RealLauncher{}
	if _, err := l.FindBinary(); err == nil {
		t.Fatal("FindBinary returned nil error for non-existent SPRAWL_CLAUDE path")
	}
}

func TestRealLauncher_FindBinary_EmptySprawlClaudeFallsBackToLookPath(t *testing.T) {
	// Stage a fake `claude` on PATH so LookPath succeeds deterministically.
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("SPRAWL_CLAUDE", "")
	t.Setenv("PATH", dir)

	l := &RealLauncher{}
	got, err := l.FindBinary()
	if err != nil {
		t.Fatalf("FindBinary returned error: %v", err)
	}
	if got != fake {
		t.Errorf("FindBinary = %q, want %q (from PATH lookup)", got, fake)
	}
}

func TestBuildRootPrompt_ContainsKeyPhrases(t *testing.T) {
	phrases := []string{
		"spawn",
		"weave",
		"DO NOT edit code",
		`type: "engineer"`,
		`type: "researcher"`,
		"family",
		"send_message",
		"merge",
		"no_validate: true",
		"TaskCreate",
	}

	for _, phrase := range phrases {
		if !strings.Contains(BuildRootPrompt(PromptConfig{RootName: "weave", AgentCLI: "claude-code"}), phrase) {
			t.Errorf("root system prompt missing key phrase: %q", phrase)
		}
	}
}

func TestEngineerSystemPrompt_ContainsKeyPhrases(t *testing.T) {
	prompt := BuildEngineerPrompt("frank", "root", "sprawl/frank", testEnvConfig())
	phrases := []string{
		"frank",
		"root",
		"sprawl/frank",
		"report_status",
		"send_message",
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

func TestBuildRootPrompt_DoesNotContainRemovedTypes(t *testing.T) {
	prompt := BuildRootPrompt(PromptConfig{RootName: "weave", AgentCLI: "claude-code"})
	for _, removed := range []string{`type: "tester"`, "--type tester"} {
		if strings.Contains(prompt, removed) {
			t.Errorf("root system prompt should not contain removed type: %q", removed)
		}
	}
}

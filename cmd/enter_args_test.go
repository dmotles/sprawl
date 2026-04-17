package cmd

import (
	"strings"
	"testing"
)

func TestBuildEnterClaudeArgs_IncludesRootToolsAllowlist(t *testing.T) {
	args := buildEnterClaudeArgs()

	// Every rootTools entry should appear as an --allowed-tools argument.
	for _, tool := range rootTools {
		if !argsContainPair(args, "--allowed-tools", tool) {
			t.Errorf("expected --allowed-tools %s in args; got %v", tool, args)
		}
	}
}

func TestBuildEnterClaudeArgs_DisallowsEditWriteNotebookEdit(t *testing.T) {
	args := buildEnterClaudeArgs()

	for _, tool := range []string{"Edit", "Write", "NotebookEdit"} {
		if !argsContainPair(args, "--disallowed-tools", tool) {
			t.Errorf("expected --disallowed-tools %s in args; got %v", tool, args)
		}
	}
}

func TestBuildEnterClaudeArgs_IncludesSprawlOpsMCPTools(t *testing.T) {
	args := buildEnterClaudeArgs()

	for _, tool := range sprawlOpsMCPTools() {
		if !argsContainPair(args, "--allowed-tools", tool) {
			t.Errorf("expected --allowed-tools %s in args; got %v", tool, args)
		}
	}
}

func TestBuildEnterClaudeArgs_StreamJSONFlags(t *testing.T) {
	args := buildEnterClaudeArgs()

	want := map[string]string{
		"--input-format":    "stream-json",
		"--output-format":   "stream-json",
		"--permission-mode": "bypassPermissions",
	}
	for flag, val := range want {
		if !argsContainPair(args, flag, val) {
			t.Errorf("expected %s %s in args; got %v", flag, val, args)
		}
	}
	if !argsContain(args, "-p") {
		t.Errorf("expected -p in args; got %v", args)
	}
	if !argsContain(args, "--verbose") {
		t.Errorf("expected --verbose in args; got %v", args)
	}
}

func TestSprawlOpsMCPTools_AllPrefixed(t *testing.T) {
	tools := sprawlOpsMCPTools()
	if len(tools) == 0 {
		t.Fatal("sprawlOpsMCPTools returned empty slice")
	}
	for _, tool := range tools {
		if !strings.HasPrefix(tool, "mcp__sprawl-ops__") {
			t.Errorf("tool %q lacks mcp__sprawl-ops__ prefix", tool)
		}
	}
}

func TestSprawlOpsMCPTools_CoversAllServerTools(t *testing.T) {
	// Pin the expected set — if sprawlmcp adds a tool, update both here and
	// sprawlOpsMCPTools(). This guards against silent drift.
	want := []string{
		"mcp__sprawl-ops__sprawl_spawn",
		"mcp__sprawl-ops__sprawl_status",
		"mcp__sprawl-ops__sprawl_delegate",
		"mcp__sprawl-ops__sprawl_message",
		"mcp__sprawl-ops__sprawl_merge",
		"mcp__sprawl-ops__sprawl_retire",
		"mcp__sprawl-ops__sprawl_kill",
	}
	got := sprawlOpsMCPTools()
	if len(got) != len(want) {
		t.Fatalf("sprawlOpsMCPTools length = %d, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("sprawlOpsMCPTools[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func argsContain(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func argsContainPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

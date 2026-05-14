package cmd

import "testing"

// TestInitCommandRemoved guards QUM-346: the tmux-mode parent entrypoint
// (`sprawl init` and the internal `_root-session` loop) was deleted as part
// of the M13 TUI cutover. If this test fails, someone has re-added the
// command — review the deletion intent before merging.
func TestInitCommandRemoved(t *testing.T) {
	for _, name := range []string{"init", "_root-session", "agent-loop"} {
		c, _, err := rootCmd.Find([]string{name})
		if err == nil && c != nil && c.Name() == name {
			t.Errorf("cobra command %q should not be registered (QUM-346)", name)
		}
	}
}

func TestSpawnSubagentCommandRemoved(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"spawn", "subagent"})
	if err == nil && c != nil && c.Name() == "subagent" {
		t.Error("cobra command \"spawn subagent\" should not be registered after the same-process cutover")
	}
}

// TestDeprecatedMCPSupersededCommandsRemoved guards QUM-566 (Phase 2.3b of
// M13): the legacy CLI surface that was superseded by MCP tools is deleted.
// If any of these commands re-appears, someone has resurrected a deprecated
// entry point — review the M13 deletion intent before merging.
func TestDeprecatedMCPSupersededCommandsRemoved(t *testing.T) {
	for _, name := range []string{
		"spawn",
		"delegate",
		"handoff",
		"kill",
		"messages",
		"report",
		"retire",
		"status",
		"tree",
	} {
		c, _, err := rootCmd.Find([]string{name})
		if err == nil && c != nil && c.Name() == name {
			t.Errorf("cobra command %q should not be registered (QUM-566)", name)
		}
	}
}

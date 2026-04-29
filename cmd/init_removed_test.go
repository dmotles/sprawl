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

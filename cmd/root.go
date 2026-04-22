package cmd

import (
	"fmt"
	"os"

	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "sprawl",
	Short: "Tree-governance for AI agents",
	Long:  "Sprawl — a self-organizing AI agent orchestration system built on Claude Code.",
}

// registerDefaultNotifier installs the process-level notifier used by every
// caller of messages.Send. Exposed as a function (rather than bare init())
// so tests can re-run it with different env, and so the registration is
// deferred until we know SPRAWL_ROOT.
//
// See QUM-310: the notify callback used to be assembled per-callsite at the
// CLI layer, so 5 of 6 callers (agentloop, report, supervisor) silently
// skipped it. Registering once here ensures uniform behavior.
func registerDefaultNotifier() {
	sprawlRoot := os.Getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return
	}
	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return
	}
	runner := &tmux.RealRunner{TmuxPath: tmuxPath}
	messages.SetDefaultNotifier(buildLegacyRootNotifier(os.Getenv, runner, sprawlRoot))
}

func Execute() {
	registerDefaultNotifier()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

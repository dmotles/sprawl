package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tui"
	"github.com/spf13/cobra"
)

// enterDeps holds dependencies for the enter command, enabling testability.
type enterDeps struct {
	getenv     func(string) string
	runProgram func(tea.Model) error
}

var defaultEnterDeps *enterDeps

func init() {
	rootCmd.AddCommand(enterCmd)
}

var enterCmd = &cobra.Command{
	Use:   "enter",
	Short: "Launch the TUI dashboard",
	Long:  "Launch a fullscreen terminal UI for monitoring and interacting with agents. Works in any terminal — no tmux required.",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := resolveEnterDeps()
		return runEnter(deps)
	},
}

func resolveEnterDeps() *enterDeps {
	if defaultEnterDeps != nil {
		return defaultEnterDeps
	}

	return &enterDeps{
		getenv: os.Getenv,
		runProgram: func(model tea.Model) error {
			p := tea.NewProgram(model)
			_, err := p.Run()
			return err
		},
	}
}

func runEnter(deps *enterDeps) error {
	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set — run 'sprawl init' first")
	}

	accentColor := state.ReadAccentColor(sprawlRoot)
	repoName := filepath.Base(sprawlRoot)
	version := state.ReadVersion(sprawlRoot)
	if version == "" {
		version = buildVersion
	}

	model := tui.NewAppModel(accentColor, repoName, version)
	if err := deps.runProgram(model); err != nil {
		return fmt.Errorf("TUI exited with error: %w", err)
	}

	fmt.Fprintln(os.Stderr, "TUI session ended.")
	return nil
}

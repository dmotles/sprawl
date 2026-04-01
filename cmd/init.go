package cmd

import (
	"fmt"
	"os"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/tmux"
	"github.com/spf13/cobra"
)

// initDeps holds the dependencies for the init command, enabling testability.
type initDeps struct {
	tmuxRunner     tmux.Runner
	claudeLauncher agent.Launcher
	getenv         func(string) string
}

var defaultDeps *initDeps

func init() {
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Launch the root agent",
	Long:  "Start a new Dendrarchy root agent session, or attach to an existing one.",
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := resolveDeps()
		if err != nil {
			return err
		}
		return runInit(deps)
	},
}

func resolveDeps() (*initDeps, error) {
	if defaultDeps != nil {
		return defaultDeps, nil
	}

	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found. Install tmux and try again")
	}

	claudeLauncher := &agent.RealLauncher{}
	if _, err := claudeLauncher.FindBinary(); err != nil {
		return nil, fmt.Errorf("claude CLI is required but not found")
	}

	return &initDeps{
		tmuxRunner:     &tmux.RealRunner{TmuxPath: tmuxPath},
		claudeLauncher: claudeLauncher,
		getenv:         os.Getenv,
	}, nil
}

func runInit(deps *initDeps) error {
	namespace := deps.getenv("DENDRA_NAMESPACE")
	if namespace == "" {
		namespace = tmux.DefaultNamespace
	}
	rootSession := tmux.RootSessionName(namespace)

	if deps.tmuxRunner.HasSession(rootSession) {
		fmt.Fprintln(os.Stderr, "Attaching to existing root agent session...")
		return deps.tmuxRunner.Attach(rootSession)
	}

	claudePath, err := deps.claudeLauncher.FindBinary()
	if err != nil {
		return fmt.Errorf("claude CLI is required but not found")
	}

	opts := agent.LaunchOpts{
		SystemPrompt:    agent.RootSystemPrompt,
		Tools:           []string{"Bash", "Read", "Glob", "Grep", "WebSearch", "WebFetch"},
		AllowedTools:    []string{"Bash", "Read", "Glob", "Grep", "WebSearch", "WebFetch"},
		DisallowedTools: []string{"Edit", "Write", "NotebookEdit"},
		Name:            rootSession,
	}

	claudeArgs := deps.claudeLauncher.BuildArgs(opts)
	shellCmd := tmux.BuildShellCmd(claudePath, claudeArgs)

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	env := map[string]string{
		"DENDRA_AGENT_IDENTITY": "root",
		"DENDRA_ROOT":           cwd,
		"DENDRA_NAMESPACE":      namespace,
	}

	fmt.Fprintln(os.Stderr, "Spawning root agent...")
	if err := deps.tmuxRunner.NewSessionWithWindow(rootSession, tmux.RootWindowName, env, shellCmd); err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	return deps.tmuxRunner.Attach(rootSession)
}

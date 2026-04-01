package cmd

import (
	"fmt"
	"os"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/state"
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

var (
	initName      string
	initNamespace string
)

func init() {
	initCmd.Flags().StringVar(&initName, "name", tmux.DefaultRootName, "root agent name")
	initCmd.Flags().StringVar(&initNamespace, "namespace", "", "namespace emoji (auto-selected if omitted)")
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
		return runInit(deps, initName, initNamespace)
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

func runInit(deps *initDeps, rootName, namespace string) error {
	// Determine namespace: explicit flag > env var > auto-pick
	if namespace == "" {
		namespace = deps.getenv("DENDRA_NAMESPACE")
	}
	if namespace == "" {
		namespace = tmux.PickNamespace(deps.tmuxRunner)
	}

	rootSession := tmux.RootSessionName(namespace, rootName)

	if deps.tmuxRunner.HasSession(rootSession) {
		fmt.Fprintln(os.Stderr, "Attaching to existing root agent session...")
		return deps.tmuxRunner.Attach(rootSession)
	}

	claudePath, err := deps.claudeLauncher.FindBinary()
	if err != nil {
		return fmt.Errorf("claude CLI is required but not found")
	}

	rootTools := []string{
		"Bash", "Read", "Glob", "Grep", "WebSearch", "WebFetch",
		"Agent", "Task", "TaskOutput", "TaskStop", "ToolSearch",
		"Skill", "TodoWrite", "AskUserQuestion", "EnterPlanMode", "ExitPlanMode",
	}

	opts := agent.LaunchOpts{
		SystemPrompt: agent.BuildRootPrompt(agent.PromptConfig{
			RootName: rootName,
			AgentCLI: "claude-code",
		}),
		Tools:           rootTools,
		AllowedTools:    rootTools,
		DisallowedTools: []string{"Edit", "Write", "NotebookEdit"},
		Name:            rootSession,
	}

	claudeArgs := deps.claudeLauncher.BuildArgs(opts)
	shellCmd := tmux.BuildShellCmd(claudePath, claudeArgs)

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// The root agent's tree path is just its name.
	treePath := rootName

	env := map[string]string{
		"DENDRA_AGENT_IDENTITY": rootName,
		"DENDRA_ROOT":           cwd,
		"DENDRA_NAMESPACE":      namespace,
		"DENDRA_TREE_PATH":      treePath,
	}

	// Persist namespace and root name for other commands to read.
	if err := state.WriteNamespace(cwd, namespace); err != nil {
		return fmt.Errorf("persisting namespace: %w", err)
	}
	if err := state.WriteRootName(cwd, rootName); err != nil {
		return fmt.Errorf("persisting root name: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Spawning root agent...")
	if err := deps.tmuxRunner.NewSessionWithWindow(rootSession, tmux.RootWindowName, env, shellCmd); err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	return deps.tmuxRunner.Attach(rootSession)
}

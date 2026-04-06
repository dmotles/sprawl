package cmd

import (
	"fmt"
	"os"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/spf13/cobra"
)

// initDeps holds the dependencies for the init command, enabling testability.
type initDeps struct {
	tmuxRunner     tmux.Runner
	claudeLauncher agent.Launcher
	findSprawl     func() (string, error)
	getenv         func(string) string
}

var defaultDeps *initDeps

var (
	initNamespace string
	initDetached  bool
)

func init() {
	initCmd.Flags().StringVar(&initNamespace, "namespace", "", "namespace emoji (auto-selected if omitted)")
	initCmd.Flags().BoolVar(&initDetached, "detached", false, "create session without attaching (returns immediately)")
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Launch the root agent",
	Long:  "Start a new Sprawl root agent session, or attach to an existing one.",
	RunE: func(_ *cobra.Command, _ []string) error {
		deps, err := resolveDeps()
		if err != nil {
			return err
		}
		return runInit(deps, initNamespace, initDetached)
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
		findSprawl:     FindSprawlBin,
		getenv:         os.Getenv,
	}, nil
}

func runInit(deps *initDeps, namespace string, detached bool) error {
	rootName := tmux.DefaultRootName
	// Determine namespace: explicit flag > env var > auto-pick
	if namespace == "" {
		namespace = deps.getenv("SPRAWL_NAMESPACE")
	}
	if namespace == "" {
		namespace = tmux.PickNamespace(deps.tmuxRunner)
	}

	rootSession := tmux.RootSessionName(namespace, rootName)

	if deps.tmuxRunner.HasSession(rootSession) {
		if detached {
			printDetachedInfo(namespace, rootSession)
			return nil
		}
		fmt.Fprintln(os.Stderr, "Attaching to existing root agent session...")
		return deps.tmuxRunner.Attach(rootSession)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	dendraPath, err := deps.findSprawl()
	if err != nil {
		return fmt.Errorf("finding sprawl binary: %w", err)
	}

	shellCmd := tmux.BuildShellCmd(dendraPath, []string{"root-loop"})

	// The root agent's tree path is just its name.
	treePath := rootName

	env := map[string]string{
		"SPRAWL_AGENT_IDENTITY": rootName,
		"SPRAWL_ROOT":           cwd,
		"SPRAWL_NAMESPACE":      namespace,
		"SPRAWL_TREE_PATH":      treePath,
	}
	if v := deps.getenv("SPRAWL_BIN"); v != "" {
		env["SPRAWL_BIN"] = v
	}
	if v := deps.getenv("SPRAWL_TEST_MODE"); v != "" {
		env["SPRAWL_TEST_MODE"] = v
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

	if detached {
		printDetachedInfo(namespace, rootSession)
		return nil
	}

	return deps.tmuxRunner.Attach(rootSession)
}

func printDetachedInfo(namespace, sessionName string) {
	fmt.Printf("Sprawl initialized (detached)\n")
	fmt.Printf("  Namespace: %s\n", namespace)
	fmt.Printf("  Session:   %s\n", sessionName)
	fmt.Printf("  Attach:    tmux attach-session -t %s\n", sessionName)
}

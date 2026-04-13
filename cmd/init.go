package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/spf13/cobra"
)

// initDeps holds the dependencies for the init command, enabling testability.
type initDeps struct {
	tmuxRunner     tmux.Runner
	claudeLauncher agent.Launcher
	getenv         func(string) string
	gitStatus      func(dir string) (string, error)
	readFile       func(path string) ([]byte, error)
	appendFile     func(path string, data []byte) error
	gitAdd         func(dir string, paths ...string) error
	gitCommit      func(dir, message string) error
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
		getenv:         os.Getenv,
		gitStatus:      realGitStatus,
		readFile:       os.ReadFile,
		appendFile: func(path string, data []byte) error {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = f.Write(data)
			return err
		},
		gitAdd: func(dir string, paths ...string) error {
			args := append([]string{"add", "--"}, paths...)
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("git add: %w: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		},
		gitCommit: func(dir, message string) error {
			cmd := exec.Command("git", "commit", "-m", message)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		},
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

	rootSession := tmux.RootSessionName(namespace)

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

	// Check that the repo working tree is clean.
	statusOut, err := deps.gitStatus(cwd)
	if err != nil {
		return fmt.Errorf("checking repo status: %w", err)
	}
	if statusOut != "" {
		return fmt.Errorf("repo has uncommitted changes — please start from a clean repo state — commit or stash your changes first")
	}

	// Ensure .sprawl/ is gitignored.
	if err := ensureSprawlGitignored(deps, cwd); err != nil {
		return err
	}

	shellCmd := buildRootLoopScript()

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

	// Reuse persisted accent color, or pick a new one if none exists.
	accentColor := state.ReadAccentColor(cwd)
	if accentColor == "" {
		accentColor = tmux.PickAccentColor()
		if err := state.WriteAccentColor(cwd, accentColor); err != nil {
			return fmt.Errorf("persisting accent color: %w", err)
		}
	}

	// Cache the version so the tmux status bar can read it cheaply.
	if err := state.WriteVersion(cwd, buildVersion); err != nil {
		return fmt.Errorf("persisting version: %w", err)
	}

	// Generate the tmux config with branding and accent color.
	confPath, err := tmux.WriteConfig(tmux.ConfigParams{
		AccentColor: accentColor,
		Namespace:   namespace,
		Version:     buildVersion,
		SprawlRoot:  cwd,
	})
	if err != nil {
		return fmt.Errorf("generating tmux config: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Spawning root agent...")
	if err := deps.tmuxRunner.NewSessionWithWindow(rootSession, tmux.RootWindowName, env, shellCmd); err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	// Apply the branded tmux config (best-effort — cosmetic only).
	if err := deps.tmuxRunner.SourceFile(rootSession, confPath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not apply tmux config: %v\n", err)
	}

	if detached {
		printDetachedInfo(namespace, rootSession)
		return nil
	}

	return deps.tmuxRunner.Attach(rootSession)
}

// buildRootLoopScript generates a bash restart loop that calls `sprawl _root-session`
// repeatedly. The script uses ${SPRAWL_BIN:-sprawl} so it picks up whatever
// sprawl binary is on PATH (or the SPRAWL_BIN override), enabling seamless
// binary upgrades without restarting the tmux session.
//
// Exit code contract:
//   - 0:  normal exit → restart immediately
//   - 42: explicit shutdown → break loop
//   - other: failure → retry after 5s delay
func buildRootLoopScript() string {
	return `bash -c 'trap '"'"'exit 42'"'"' TERM INT; while true; do "${SPRAWL_BIN:-sprawl}" _root-session; rc=$?; if [ $rc -eq 42 ]; then echo "[root-loop] explicit shutdown, exiting"; break; elif [ $rc -ne 0 ]; then echo "[root-loop] session failed (exit $rc), retrying in 5s..."; sleep 5; else echo "[root-loop] session ended, restarting..."; fi; done'`
}

// ensureSprawlGitignored checks if .sprawl/ is in .gitignore and adds the
// appropriate entries if not. If entries are added, it stages and commits.
func ensureSprawlGitignored(deps *initDeps, cwd string) error {
	gitignorePath := filepath.Join(cwd, ".gitignore")
	content, err := deps.readFile(gitignorePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading .gitignore: %w", err)
	}

	if strings.Contains(string(content), ".sprawl/*") {
		return nil
	}

	// Build the entries to append.
	var entries string
	if len(content) > 0 && content[len(content)-1] != '\n' {
		entries = "\n"
	}
	entries += ".sprawl/*\n!.sprawl/config.yaml\n"

	if err := deps.appendFile(gitignorePath, []byte(entries)); err != nil {
		return fmt.Errorf("updating .gitignore: %w", err)
	}

	if err := deps.gitAdd(cwd, ".gitignore"); err != nil {
		return fmt.Errorf("staging .gitignore: %w", err)
	}

	if err := deps.gitCommit(cwd, "Add .sprawl/ to .gitignore"); err != nil {
		return fmt.Errorf("committing .gitignore: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Added .sprawl/ to .gitignore and committed.")
	return nil
}

func printDetachedInfo(namespace, sessionName string) {
	fmt.Printf("Sprawl initialized (detached)\n")
	fmt.Printf("  Namespace: %s\n", namespace)
	fmt.Printf("  Session:   %s\n", sessionName)
	fmt.Printf("  Attach:    tmux attach-session -t %s\n", sessionName)
}

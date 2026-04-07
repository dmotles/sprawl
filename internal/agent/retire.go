package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
)

// ShutdownDeps holds the deps needed for graceful agent shutdown.
type ShutdownDeps struct {
	TmuxRunner tmux.Runner
	WriteFile  func(string, []byte, os.FileMode) error
	RemoveFile func(string) error
	SleepFunc  func(time.Duration)
}

// GracefulShutdown signals the agent-loop via sentinel file, waits for it to exit,
// and falls back to killing the tmux window if it doesn't exit in time.
func GracefulShutdown(deps *ShutdownDeps, sprawlRoot string, agentState *state.AgentState, force bool) {
	if force {
		_ = deps.TmuxRunner.KillWindow(agentState.TmuxSession, agentState.TmuxWindow)
		return
	}

	// Write sentinel file
	killPath := filepath.Join(sprawlRoot, ".sprawl", "agents", agentState.Name+".kill")
	_ = deps.WriteFile(killPath, []byte("kill"), 0o644)

	// Poll: wait for window to disappear
	graceful := false
	for range 10 {
		_, err := deps.TmuxRunner.ListWindowPIDs(agentState.TmuxSession, agentState.TmuxWindow)
		if err != nil {
			graceful = true
			break
		}
		deps.SleepFunc(500 * time.Millisecond)
	}

	if !graceful {
		_ = deps.TmuxRunner.KillWindow(agentState.TmuxSession, agentState.TmuxWindow)
	}

	// Clean up sentinel (may already be gone)
	_ = deps.RemoveFile(killPath)
}

// RetireDeps holds the dependencies for RetireAgent.
type RetireDeps struct {
	TmuxRunner     tmux.Runner
	WriteFile      func(string, []byte, os.FileMode) error
	RemoveFile     func(string) error
	SleepFunc      func(time.Duration)
	WorktreeRemove func(repoRoot, worktreePath string, force bool) error
	GitStatus      func(worktreePath string) (string, error)
	RemoveAll      func(string) error
	ReadDir        func(string) ([]os.DirEntry, error)
	ArchiveMessage func(sprawlRoot, agent, msgID string) error
	Stderr         io.Writer
}

// RetireAgent performs core teardown. If skipShutdown is true, skips graceful shutdown and tmux cleanup.
func RetireAgent(deps *RetireDeps, sprawlRoot string, agent *state.AgentState, force bool, skipShutdown bool) error {
	if !skipShutdown {
		sd := &ShutdownDeps{
			TmuxRunner: deps.TmuxRunner,
			WriteFile:  deps.WriteFile,
			RemoveFile: deps.RemoveFile,
			SleepFunc:  deps.SleepFunc,
		}
		GracefulShutdown(sd, sprawlRoot, agent, force)

		// Best-effort tmux window cleanup after graceful shutdown
		_ = deps.TmuxRunner.KillWindow(agent.TmuxSession, agent.TmuxWindow)
	}

	// Worktree check + removal (skip if empty worktree or subagent)
	if agent.Worktree != "" && !agent.Subagent {
		statusOutput, err := deps.GitStatus(agent.Worktree)
		if err == nil && statusOutput != "" && !force {
			return fmt.Errorf("agent %s has uncommitted changes in worktree; commit first or use --force to discard", agent.Name)
		}

		// Remove worktree
		forceRemove := force || statusOutput != ""
		err = deps.WorktreeRemove(sprawlRoot, agent.Worktree, forceRemove)
		if err != nil {
			// Worktree may already be gone — not fatal
			fmt.Fprintf(deps.Stderr, "Warning: could not remove worktree: %v\n", err)
		}
	}

	// Remove agent logs directory
	logsDir := filepath.Join(sprawlRoot, ".sprawl", "agents", agent.Name, "logs")
	if err := deps.RemoveAll(logsDir); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(deps.Stderr, "Warning: could not remove logs directory: %v\n", err)
	}

	// Archive messages from new/ and cur/ (leave sent/ untouched)
	msgsDir := filepath.Join(sprawlRoot, ".sprawl", "messages", agent.Name)
	for _, sub := range []string{"new", "cur"} {
		dirPath := filepath.Join(msgsDir, sub)
		entries, err := deps.ReadDir(dirPath)
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(deps.Stderr, "Warning: could not read messages %s/ directory: %v\n", sub, err)
			}
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			msgID := entry.Name()[:len(entry.Name())-len(".json")]
			if err := deps.ArchiveMessage(sprawlRoot, agent.Name, msgID); err != nil {
				fmt.Fprintf(deps.Stderr, "Warning: could not archive message %s: %v\n", msgID, err)
			}
		}
	}

	// Delete state file (name is now free)
	if err := state.DeleteAgent(sprawlRoot, agent.Name); err != nil {
		return fmt.Errorf("deleting agent state: %w", err)
	}

	return nil
}

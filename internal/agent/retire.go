package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dmotles/sprawl/internal/state"
)

// ShutdownDeps is intentionally empty in the same-process runtime model.
// Live child runtimes are stopped by supervisor-owned runtime handles.
type ShutdownDeps struct{}

// GracefulShutdown is retained as a compatibility no-op for same-process
// runtime cleanup paths. Offline lifecycle commands only operate on persisted
// state after the owning weave session has stopped.
func GracefulShutdown(_ *ShutdownDeps, _ string, _ *state.AgentState, _ bool) {}

// RetireDeps holds the dependencies for RetireAgent.
type RetireDeps struct {
	WorktreeRemove func(repoRoot, worktreePath string, force bool) error
	GitStatus      func(worktreePath string) (string, error)
	RemoveAll      func(string) error
	ReadDir        func(string) ([]os.DirEntry, error)
	ArchiveMessage func(sprawlRoot, agent, msgID string) error
	Stderr         io.Writer
}

// RetireAgent performs core teardown after the child runtime has already been
// stopped by the live weave session, or when an offline cleanup is running with
// no live weave session present.
func RetireAgent(deps *RetireDeps, sprawlRoot string, agent *state.AgentState, force bool, skipShutdown bool) error {
	_ = skipShutdown

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

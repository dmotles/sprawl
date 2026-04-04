package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/merge"
	"github.com/dmotles/dendra/internal/messages"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
	"github.com/spf13/cobra"
)

// retireDeps holds the dependencies for the retire command, enabling testability.
type retireDeps struct {
	tmuxRunner          tmux.Runner
	getenv              func(string) string
	writeFile           func(string, []byte, os.FileMode) error
	removeFile          func(string) error
	sleepFunc           func(time.Duration)
	worktreeRemove      func(repoRoot, worktreePath string, force bool) error
	gitStatus           func(worktreePath string) (string, error)
	removeAll           func(string) error
	gitBranchDelete     func(repoRoot, branchName string) error
	gitBranchIsMerged   func(repoRoot, branchName string) (bool, error)
	gitBranchSafeDelete func(repoRoot, branchName string) error
	doMerge             func(cfg *merge.Config, deps *merge.Deps) (*merge.Result, error)
	newMergeDeps        func() *merge.Deps
	loadAgent           func(dendraRoot, name string) (*state.AgentState, error)
	currentBranch       func(repoRoot string) (string, error)
}

var defaultRetireDeps *retireDeps

var (
	retireCascade bool
	retireForce   bool
	retireAbandon bool
	retireMerge   bool
)

func init() {
	retireCmd.Flags().BoolVar(&retireCascade, "cascade", false, "Retire agent and all descendants bottom-up")
	retireCmd.Flags().BoolVar(&retireForce, "force", false, "Skip dirty worktree check and orphan children")
	retireCmd.Flags().BoolVar(&retireAbandon, "abandon", false, "Discard the agent's work and delete its branch")
	retireCmd.Flags().BoolVar(&retireMerge, "merge", false, "Merge the agent's work into your branch before retiring")
	rootCmd.AddCommand(retireCmd)
}

var retireCmd = &cobra.Command{
	Use:   "retire <agent-name>",
	Short: "Full teardown: stop process, close tmux, remove worktree, delete state",
	Long:  "Full agent teardown. Three workflows:\n\n  dendra retire <agent>          preserve branch, warn if unmerged\n  dendra retire --merge <agent>   merge into your branch, then retire\n  dendra retire --abandon <agent> delete branch and all work",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps, err := resolveRetireDeps()
		if err != nil {
			return err
		}
		return runRetire(deps, args[0], retireCascade, retireForce, retireAbandon, retireMerge)
	},
}

func resolveRetireDeps() (*retireDeps, error) {
	if defaultRetireDeps != nil {
		return defaultRetireDeps, nil
	}

	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found")
	}

	return &retireDeps{
		tmuxRunner:          &tmux.RealRunner{TmuxPath: tmuxPath},
		getenv:              os.Getenv,
		writeFile:           os.WriteFile,
		removeFile:          os.Remove,
		sleepFunc:           time.Sleep,
		worktreeRemove:      realWorktreeRemove,
		gitStatus:           realGitStatus,
		removeAll:           os.RemoveAll,
		gitBranchDelete:     realGitBranchDelete,
		gitBranchIsMerged:   realGitBranchIsMerged,
		gitBranchSafeDelete: realGitBranchSafeDelete,
		doMerge:             merge.Merge,
		newMergeDeps: func() *merge.Deps {
			return &merge.Deps{
				LockAcquire:     merge.RealLockAcquire,
				GitMergeBase:    merge.RealGitMergeBase,
				GitRevParseHead: merge.RealGitRevParseHead,
				GitResetSoft:    merge.RealGitResetSoft,
				GitCommit:       merge.RealGitCommit,
				GitRebase:       merge.RealGitRebase,
				GitRebaseAbort:  merge.RealGitRebaseAbort,
				GitFFMerge:      merge.RealGitFFMerge,
				GitResetHard:    merge.RealGitResetHard,
				RunTests:        merge.RealRunTests,
				WritePoke:       merge.RealWritePoke,
				Stderr:          os.Stderr,
			}
		},
		loadAgent:     state.LoadAgent,
		currentBranch: gitCurrentBranch,
	}, nil
}

func runRetire(deps *retireDeps, agentName string, cascade, force, abandon, mergeFirst bool) error {
	if abandon && mergeFirst {
		return fmt.Errorf("--merge and --abandon are mutually exclusive")
	}

	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	// Load agent state
	agentState, err := state.LoadAgent(dendraRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	// Merge before retire if requested (must happen before "retiring" checkpoint)
	if mergeFirst {
		callerName := deps.getenv("DENDRA_AGENT_IDENTITY")
		if callerName == "" {
			return fmt.Errorf("--merge requires DENDRA_AGENT_IDENTITY to be set")
		}
		if agentState.Subagent {
			return fmt.Errorf("agent %q is a subagent and has no branch to merge", agentName)
		}
		if agentState.Parent != callerName {
			return fmt.Errorf("cannot merge %q: you are not its parent (parent is %q)", agentName, agentState.Parent)
		}
		callerWorktree := dendraRoot
		if a, err := deps.loadAgent(dendraRoot, callerName); err == nil {
			callerWorktree = a.Worktree
		}
		targetBranch, err := deps.currentBranch(callerWorktree)
		if err != nil {
			return fmt.Errorf("determining current branch: %w", err)
		}
		cfg := &merge.Config{
			DendraRoot:     dendraRoot,
			AgentName:      agentName,
			AgentBranch:    agentState.Branch,
			AgentWorktree:  agentState.Worktree,
			ParentBranch:   targetBranch,
			ParentWorktree: callerWorktree,
			AgentState:     agentState,
		}
		result, err := deps.doMerge(cfg, deps.newMergeDeps())
		if err != nil {
			return fmt.Errorf("merge before retire failed: %w", err)
		}
		if result.WasNoOp {
			fmt.Fprintf(os.Stderr, "Nothing to merge: %s has no new commits\n", agentName)
		} else {
			fmt.Fprintf(os.Stderr, "Merged %q into %s (%s)\n", agentName, targetBranch, result.CommitHash)
		}
	}

	// If already in "retiring" state, resume from where we left off (crash recovery)
	if agentState.Status == "retiring" {
		rd := buildRetireDeps(deps)
		if err := agent.RetireAgent(rd, dendraRoot, agentState, force, true); err != nil {
			return err
		}
		// Clean up lock and poke files
		lockPath := filepath.Join(dendraRoot, ".dendra", "locks", agentState.Name+".lock")
		_ = deps.removeFile(lockPath)
		pokePath := filepath.Join(dendraRoot, ".dendra", "agents", agentState.Name+".poke")
		_ = deps.removeFile(pokePath)
		printRetireSuccess(agentState, abandon, mergeFirst, deps, dendraRoot)
		return nil
	}

	// Check for children
	if !cascade && !force {
		children, err := findChildren(dendraRoot, agentName)
		if err != nil {
			return fmt.Errorf("checking children: %w", err)
		}
		if len(children) > 0 {
			names := make([]string, len(children))
			for i, c := range children {
				names[i] = c.Name
			}
			return fmt.Errorf("agent %s has %d active children: %s; use --cascade to retire %s and all descendants, or --force to retire %s only (children become orphans)",
				agentName, len(children), strings.Join(names, ", "), agentName, agentName)
		}
	}

	// Cascade: retire children first (depth-first, bottom-up)
	if cascade {
		children, err := findChildren(dendraRoot, agentName)
		if err != nil {
			return fmt.Errorf("checking children: %w", err)
		}
		for _, child := range children {
			if err := runRetire(deps, child.Name, true, force, abandon, false); err != nil {
				return fmt.Errorf("retiring child %s: %w", child.Name, err)
			}
		}
	}

	// Crash-safe checkpoint: mark as "retiring"
	agentState.Status = "retiring"
	if err := state.SaveAgent(dendraRoot, agentState); err != nil {
		return fmt.Errorf("updating agent state: %w", err)
	}

	rd := buildRetireDeps(deps)
	if err := agent.RetireAgent(rd, dendraRoot, agentState, force, false); err != nil {
		return err
	}

	// Clean up lock and poke files
	lockPath := filepath.Join(dendraRoot, ".dendra", "locks", agentState.Name+".lock")
	_ = deps.removeFile(lockPath)
	pokePath := filepath.Join(dendraRoot, ".dendra", "agents", agentState.Name+".poke")
	_ = deps.removeFile(pokePath)
	printRetireSuccess(agentState, abandon, mergeFirst, deps, dendraRoot)
	return nil
}

func printRetireSuccess(agentState *state.AgentState, abandon, mergeFirst bool, deps *retireDeps, dendraRoot string) {
	switch {
	case abandon && agentState.Branch != "":
		if err := deps.gitBranchDelete(dendraRoot, agentState.Branch); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not delete branch %s: %v\n", agentState.Branch, err)
			fmt.Fprintf(os.Stderr, "Retired agent %q (branch %s preserved)\n", agentState.Name, agentState.Branch)
		} else {
			fmt.Fprintf(os.Stderr, "Retired %q and deleted branch %s\n", agentState.Name, agentState.Branch)
		}
	case mergeFirst && agentState.Branch != "":
		if err := deps.gitBranchSafeDelete(dendraRoot, agentState.Branch); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not delete branch %s: %v\n", agentState.Branch, err)
			fmt.Fprintf(os.Stderr, "Merged and retired %q (branch %s preserved)\n", agentState.Name, agentState.Branch)
		} else {
			fmt.Fprintf(os.Stderr, "Merged and retired %q, deleted branch %s\n", agentState.Name, agentState.Branch)
		}
	default:
		if agentState.Branch != "" {
			merged, err := deps.gitBranchIsMerged(dendraRoot, agentState.Branch)
			if err == nil && merged {
				if delErr := deps.gitBranchSafeDelete(dendraRoot, agentState.Branch); delErr == nil {
					fmt.Fprintf(os.Stderr, "Retired %q, deleted branch %s (already merged)\n", agentState.Name, agentState.Branch)
					return
				}
			}
		}
		fmt.Fprintf(os.Stderr, "Retired agent %q (branch %s preserved)\n", agentState.Name, agentState.Branch)
		if agentState.Branch != "" {
			fmt.Fprintf(os.Stderr, "Warning: branch %s may contain unmerged commits. Use 'git branch -d %s' to delete if merged, or 'git branch -D %s' to force-delete.\n", agentState.Branch, agentState.Branch, agentState.Branch)
		}
	}
}

func buildRetireDeps(deps *retireDeps) *agent.RetireDeps {
	return &agent.RetireDeps{
		TmuxRunner:     deps.tmuxRunner,
		WriteFile:      deps.writeFile,
		RemoveFile:     deps.removeFile,
		SleepFunc:      deps.sleepFunc,
		WorktreeRemove: deps.worktreeRemove,
		GitStatus:      deps.gitStatus,
		RemoveAll:      deps.removeAll,
		ReadDir:        os.ReadDir,
		ArchiveMessage: messages.Archive,
		Stderr:         os.Stderr,
	}
}

// findChildren returns all agents that have the given name as their parent.
func findChildren(dendraRoot, parentName string) ([]*state.AgentState, error) {
	agents, err := state.ListAgents(dendraRoot)
	if err != nil {
		return nil, err
	}
	var children []*state.AgentState
	for _, a := range agents {
		if a.Parent == parentName {
			children = append(children, a)
		}
	}
	return children, nil
}

// realGitBranchDelete force-deletes a git branch using 'git branch -D'.
func realGitBranchDelete(repoRoot, branchName string) error {
	cmd := exec.Command("git", "branch", "-D", branchName)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch -D %s: %s: %w", branchName, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// realWorktreeRemove removes a git worktree.
func realWorktreeRemove(repoRoot, worktreePath string, force bool) error {
	args := []string{"worktree", "remove", worktreePath}
	if force {
		args = append(args, "--force")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// realGitBranchIsMerged checks if a branch is fully merged into the current branch.
func realGitBranchIsMerged(repoRoot, branchName string) (bool, error) {
	cmd := exec.Command("git", "branch", "--merged")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git branch --merged: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		name = strings.TrimPrefix(name, "* ")
		name = strings.TrimPrefix(name, "+ ")
		if name == branchName {
			return true, nil
		}
	}
	return false, nil
}

// realGitBranchSafeDelete deletes a branch using 'git branch -d' (safe delete, only if merged).
func realGitBranchSafeDelete(repoRoot, branchName string) error {
	cmd := exec.Command("git", "branch", "-d", branchName)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch -d %s: %s: %w", branchName, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// realGitStatus returns the porcelain status output for a worktree directory.
// Returns empty string if clean, non-empty if dirty.
func realGitStatus(worktreePath string) (string, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

package agentops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
)

// RetireDeps holds the injectable dependencies for Retire.
type RetireDeps struct {
	Getenv              func(string) string
	WorktreeRemove      func(repoRoot, worktreePath string, force bool) error
	GitStatus           func(worktreePath string) (string, error)
	RemoveAll           func(string) error
	GitBranchDelete     func(repoRoot, branchName string) error
	GitBranchIsMerged   func(repoRoot, branchName string) (bool, error)
	GitBranchSafeDelete func(repoRoot, branchName string) error
	DoMerge             func(cfg *merge.Config, deps *merge.Deps) (*merge.Result, error)
	NewMergeDeps        func() *merge.Deps
	LoadAgent           func(sprawlRoot, name string) (*state.AgentState, error)
	CurrentBranch       func(repoRoot string) (string, error)
	GitUnmergedCommits  func(repoRoot, branchName string) ([]string, error)
	LoadConfig          func(sprawlRoot string) (*config.Config, error)
	RunScript           func(script, workDir string, env map[string]string) ([]byte, error)
}

// Retire fully tears down an agent after its owning runtime has already been
// stopped by the live weave session, or during offline cleanup with no live
// weave session present.
func Retire(deps *RetireDeps, agentName string, cascade, force, abandon, mergeFirst, yes, noValidate bool) error {
	if err := agent.ValidateName(agentName); err != nil {
		return err
	}

	if abandon && mergeFirst {
		return fmt.Errorf("--merge and --abandon are mutually exclusive")
	}

	sprawlRoot := deps.Getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	// Load agent state
	agentState, err := state.LoadAgent(sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	// Merge before retire if requested (must happen before "retiring" checkpoint)
	if mergeFirst {
		callerName := deps.Getenv("SPRAWL_AGENT_IDENTITY")
		if callerName == "" {
			return fmt.Errorf("--merge requires SPRAWL_AGENT_IDENTITY to be set")
		}
		if agentState.Subagent {
			return fmt.Errorf("agent %q is a subagent and has no branch to merge", agentName)
		}
		if agentState.Parent != callerName {
			return fmt.Errorf("cannot merge %q: you are not its parent (parent is %q)", agentName, agentState.Parent)
		}
		callerWorktree := sprawlRoot
		if a, err := deps.LoadAgent(sprawlRoot, callerName); err == nil {
			callerWorktree = a.Worktree
		}
		targetBranch, err := deps.CurrentBranch(callerWorktree)
		if err != nil {
			return fmt.Errorf("determining current branch: %w", err)
		}
		sprawlCfg, err := deps.LoadConfig(sprawlRoot)
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		cfg := &merge.Config{
			SprawlRoot:     sprawlRoot,
			AgentName:      agentName,
			AgentBranch:    agentState.Branch,
			AgentWorktree:  agentState.Worktree,
			ParentBranch:   targetBranch,
			ParentWorktree: callerWorktree,
			NoValidate:     noValidate,
			ValidateCmd:    sprawlCfg.Validate,
			AgentState:     agentState,
		}
		result, err := deps.DoMerge(cfg, deps.NewMergeDeps())
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
		runTeardownScript(deps, sprawlRoot, agentState)
		rd := buildRetireDeps(deps)
		if err := agent.RetireAgent(rd, sprawlRoot, agentState, force, true); err != nil {
			return err
		}
		// Clean up lock and poke files
		lockPath := filepath.Join(sprawlRoot, ".sprawl", "locks", agentState.Name+".lock")
		_ = os.Remove(lockPath)
		pokePath := filepath.Join(sprawlRoot, ".sprawl", "agents", agentState.Name+".poke")
		_ = os.Remove(pokePath)
		printRetireSuccess(agentState, abandon, mergeFirst, deps, sprawlRoot)
		return nil
	}

	// Check for children
	if !cascade && !force {
		children, err := findChildren(sprawlRoot, agentName)
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

	// Abandon safety guard: warn about unmerged commits.
	if abandon && agentState.Branch != "" && !agentState.Subagent {
		var warnings []string

		// Guard 1: Check for unmerged commits.
		commits, commitErr := deps.GitUnmergedCommits(sprawlRoot, agentState.Branch)
		if commitErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not check unmerged commits: %v\n", commitErr)
		} else if len(commits) > 0 {
			fmt.Fprintf(os.Stderr, "WARNING: Agent %q has %d unmerged commit(s) on branch %s:\n", agentName, len(commits), agentState.Branch)
			for _, c := range commits {
				fmt.Fprintf(os.Stderr, "  %s\n", c)
			}
			warnings = append(warnings, "unmerged commits")
		}

		if len(warnings) > 0 && !yes {
			return fmt.Errorf("retire --abandon blocked: %s detected. Re-run with --yes to confirm, or use --merge instead", strings.Join(warnings, " and "))
		}
	}

	// Cascade: retire children first (depth-first, bottom-up)
	if cascade {
		children, err := findChildren(sprawlRoot, agentName)
		if err != nil {
			return fmt.Errorf("checking children: %w", err)
		}
		for _, child := range children {
			if err := Retire(deps, child.Name, true, force, abandon, false, yes, noValidate); err != nil {
				return fmt.Errorf("retiring child %s: %w", child.Name, err)
			}
		}
	}

	// Crash-safe checkpoint: mark as "retiring"
	agentState.Status = "retiring"
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		return fmt.Errorf("updating agent state: %w", err)
	}

	// Run worktree teardown script if configured (before worktree removal)
	runTeardownScript(deps, sprawlRoot, agentState)

	rd := buildRetireDeps(deps)
	if err := agent.RetireAgent(rd, sprawlRoot, agentState, force, false); err != nil {
		return err
	}

	// Clean up lock and poke files
	lockPath := filepath.Join(sprawlRoot, ".sprawl", "locks", agentState.Name+".lock")
	_ = os.Remove(lockPath)
	pokePath := filepath.Join(sprawlRoot, ".sprawl", "agents", agentState.Name+".poke")
	_ = os.Remove(pokePath)
	printRetireSuccess(agentState, abandon, mergeFirst, deps, sprawlRoot)
	return nil
}

// runTeardownScript runs the worktree.teardown script if configured.
// Failures are logged as warnings but do not stop retirement.
func runTeardownScript(deps *RetireDeps, sprawlRoot string, agentState *state.AgentState) {
	if agentState.Worktree == "" || agentState.Subagent {
		return
	}

	cfg, err := deps.LoadConfig(sprawlRoot)
	if err != nil {
		return
	}

	teardownScript, ok := cfg.Get("worktree.teardown")
	if !ok || teardownScript == "" {
		return
	}

	teardownEnv := map[string]string{
		"SPRAWL_AGENT_IDENTITY": agentState.Name,
		"SPRAWL_ROOT":           sprawlRoot,
	}
	fmt.Fprintf(os.Stderr, "Running worktree teardown script for %s...\n", agentState.Name)
	output, scriptErr := deps.RunScript(teardownScript, agentState.Worktree, teardownEnv)
	if scriptErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: worktree teardown script failed for %s:\n%s\nEscalate to your parent agent or the user — teardown wasn't clean and needs attention\n", agentState.Name, string(output))
	}
}

func printRetireSuccess(agentState *state.AgentState, abandon, mergeFirst bool, deps *RetireDeps, sprawlRoot string) {
	switch {
	case abandon && agentState.Branch != "":
		if err := deps.GitBranchDelete(sprawlRoot, agentState.Branch); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not delete branch %s: %v\n", agentState.Branch, err)
			fmt.Fprintf(os.Stderr, "Retired agent %q (branch %s preserved)\n", agentState.Name, agentState.Branch)
		} else {
			fmt.Fprintf(os.Stderr, "Retired %q and deleted branch %s\n", agentState.Name, agentState.Branch)
		}
	case mergeFirst && agentState.Branch != "":
		if err := deps.GitBranchSafeDelete(sprawlRoot, agentState.Branch); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not delete branch %s: %v\n", agentState.Branch, err)
			fmt.Fprintf(os.Stderr, "Merged and retired %q (branch %s preserved)\n", agentState.Name, agentState.Branch)
		} else {
			fmt.Fprintf(os.Stderr, "Merged and retired %q, deleted branch %s\n", agentState.Name, agentState.Branch)
		}
	default:
		if agentState.Branch != "" {
			merged, err := deps.GitBranchIsMerged(sprawlRoot, agentState.Branch)
			if err == nil && merged {
				if delErr := deps.GitBranchSafeDelete(sprawlRoot, agentState.Branch); delErr == nil {
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

func buildRetireDeps(deps *RetireDeps) *agent.RetireDeps {
	return &agent.RetireDeps{
		WorktreeRemove: deps.WorktreeRemove,
		GitStatus:      deps.GitStatus,
		RemoveAll:      deps.RemoveAll,
		ReadDir:        os.ReadDir,
		ArchiveMessage: messages.Archive,
		Stderr:         os.Stderr,
	}
}

// findChildren returns all agents that have the given name as their parent.
func findChildren(sprawlRoot, parentName string) ([]*state.AgentState, error) {
	agents, err := state.ListAgents(sprawlRoot)
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

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/dmotles/sprawl/internal/worktree"
	"github.com/gofrs/flock"
)

// Config holds configuration for the real supervisor.
type Config struct {
	SprawlRoot string
	CallerName string
}

// Real is the production implementation of Supervisor.
//
// Spawn/Merge/Retire/Kill delegate to internal/agentops, which contains the
// same logic used by the CLI `sprawl spawn|merge|retire|kill` commands.
//
// The *Fn fields are test seams: tests can swap them to exercise Real's
// wiring without touching the underlying agentops machinery (which is
// already covered by cmd/*_test.go and internal/agentops tests).
type Real struct {
	sprawlRoot string
	callerName string

	spawnDeps  *agentops.SpawnDeps
	mergeDeps  *agentops.MergeDeps
	retireDeps *agentops.RetireDeps
	killDeps   *agentops.KillDeps

	spawnFn  func(*agentops.SpawnDeps, string, string, string, string) (*state.AgentState, error)
	mergeFn  func(*agentops.MergeDeps, string, string, bool, bool) error
	retireFn func(*agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error
	killFn   func(*agentops.KillDeps, string, bool) error
}

// NewReal creates a new real supervisor. It returns an error if required
// tooling (tmux) is not available on PATH.
func NewReal(cfg Config) (*Real, error) {
	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found")
	}
	tmuxRunner := &tmux.RealRunner{TmuxPath: tmuxPath}

	// supervisorGetenv injects the supervisor's identity/root into env
	// lookups that agentops performs. Everything else passes through to
	// the process environment.
	supervisorGetenv := func(key string) string {
		switch key {
		case "SPRAWL_AGENT_IDENTITY":
			return cfg.CallerName
		case "SPRAWL_ROOT":
			return cfg.SprawlRoot
		default:
			return os.Getenv(key)
		}
	}

	newMergeDeps := func() *merge.Deps {
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
	}

	r := &Real{
		sprawlRoot: cfg.SprawlRoot,
		callerName: cfg.CallerName,

		spawnDeps: &agentops.SpawnDeps{
			TmuxRunner:      tmuxRunner,
			WorktreeCreator: &worktree.RealCreator{},
			Getenv:          supervisorGetenv,
			CurrentBranch:   agentops.GitCurrentBranch,
			FindSprawl:      agentops.FindSprawlBin,
			NewSpawnLock: func(lockPath string) (func() error, func() error) {
				fl := flock.New(lockPath)
				return fl.Lock, fl.Unlock
			},
			LoadConfig:     config.Load,
			RunScript:      agentops.RunBashScript,
			WorktreeRemove: agentops.RealWorktreeRemove,
		},
		mergeDeps: &agentops.MergeDeps{
			Getenv:        supervisorGetenv,
			LoadAgent:     state.LoadAgent,
			ListAgents:    state.ListAgents,
			GitStatus:     agentops.RealGitStatus,
			BranchExists:  agentops.RealBranchExists,
			CurrentBranch: agentops.GitCurrentBranch,
			LoadConfig:    config.Load,
			DoMerge:       merge.Merge,
			NewMergeDeps:  newMergeDeps,
			Stderr:        os.Stderr,
		},
		retireDeps: &agentops.RetireDeps{
			TmuxRunner:          tmuxRunner,
			Getenv:              supervisorGetenv,
			WriteFile:           os.WriteFile,
			RemoveFile:          os.Remove,
			SleepFunc:           time.Sleep,
			WorktreeRemove:      agentops.RealWorktreeRemove,
			GitStatus:           agentops.RealGitStatus,
			RemoveAll:           os.RemoveAll,
			GitBranchDelete:     agentops.RealGitBranchDelete,
			GitBranchIsMerged:   agentops.RealGitBranchIsMerged,
			GitBranchSafeDelete: agentops.RealGitBranchSafeDelete,
			DoMerge:             merge.Merge,
			NewMergeDeps:        newMergeDeps,
			LoadAgent:           state.LoadAgent,
			CurrentBranch:       agentops.GitCurrentBranch,
			GitUnmergedCommits:  agentops.RealGitUnmergedCommits,
			LoadConfig:          config.Load,
			RunScript:           agentops.RunBashScript,
		},
		killDeps: &agentops.KillDeps{
			TmuxRunner: tmuxRunner,
			Getenv:     supervisorGetenv,
			WriteFile:  os.WriteFile,
			RemoveFile: os.Remove,
			SleepFunc:  time.Sleep,
		},

		spawnFn:  agentops.Spawn,
		mergeFn:  agentops.Merge,
		retireFn: agentops.Retire,
		killFn:   agentops.Kill,
	}
	return r, nil
}

func (r *Real) Status(_ context.Context) ([]AgentInfo, error) {
	agents, err := state.ListAgents(r.sprawlRoot)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}

	result := make([]AgentInfo, 0, len(agents))
	for _, a := range agents {
		result = append(result, AgentInfo{
			Name:   a.Name,
			Type:   a.Type,
			Family: a.Family,
			Parent: a.Parent,
			Status: a.Status,
			Branch: a.Branch,
		})
	}
	return result, nil
}

func (r *Real) Delegate(_ context.Context, agentName, task string) error {
	agentState, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	switch agentState.Status {
	case "killed", "retired", "retiring":
		return fmt.Errorf("cannot delegate to agent %q: status is %q", agentName, agentState.Status)
	}

	if task == "" {
		return fmt.Errorf("task prompt must not be empty")
	}

	_, err = state.EnqueueTask(r.sprawlRoot, agentName, task)
	if err != nil {
		return fmt.Errorf("enqueuing task: %w", err)
	}
	return nil
}

func (r *Real) Message(_ context.Context, agentName, subject, body string) error {
	_, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	return messages.Send(r.sprawlRoot, r.callerName, agentName, subject, body)
}

func (r *Real) Spawn(_ context.Context, req SpawnRequest) (*AgentInfo, error) {
	st, err := r.spawnFn(r.spawnDeps, req.Family, req.Type, req.Prompt, req.Branch)
	if err != nil {
		return nil, err
	}
	return &AgentInfo{
		Name:   st.Name,
		Type:   st.Type,
		Family: st.Family,
		Parent: st.Parent,
		Status: st.Status,
		Branch: st.Branch,
	}, nil
}

func (r *Real) Merge(_ context.Context, agentName, message string, noValidate bool) error {
	return r.mergeFn(r.mergeDeps, agentName, message, noValidate, false)
}

func (r *Real) Retire(_ context.Context, agentName string, mergeFirst, abandon bool) error {
	return r.retireFn(r.retireDeps, agentName, false /* cascade */, false /* force */, abandon, mergeFirst, true /* yes */, false /* noValidate */)
}

// Kill is idempotent: if the agent is already gone (state file missing) or
// was already killed, it returns nil. Enter.go's graceful shutdown iterates
// every agent and calls Kill, so transient absence must not fail.
func (r *Real) Kill(_ context.Context, agentName string) error {
	if err := agent.ValidateName(agentName); err != nil {
		return err
	}

	// Swallow "agent not found" — idempotent shutdown contract.
	if _, err := state.LoadAgent(r.sprawlRoot, agentName); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		// Unknown error reading state — propagate.
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	return r.killFn(r.killDeps, agentName, false)
}

func (r *Real) Shutdown(_ context.Context) error {
	return nil
}

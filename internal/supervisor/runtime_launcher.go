package supervisor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/gofrs/flock"
)

type inProcessRuntimeStarter struct{}

var (
	startRunnerFn = agentloop.StartRunner
	runRunnerFn   = func(r *agentloop.Runner, ctx context.Context) error { return r.Run(ctx) }
	stopRunnerFn  = func(r *agentloop.Runner, ctx context.Context) error { return r.Stop(ctx) }
)

func newInProcessRuntimeStarter() RuntimeStarter {
	return &inProcessRuntimeStarter{}
}

func (s *inProcessRuntimeStarter) Start(_ context.Context, spec RuntimeStartSpec) (RuntimeHandle, error) {
	runCtx, cancel := context.WithCancel(context.Background())
	deps := buildRunnerDeps(spec)
	runner, err := startRunnerFn(runCtx, deps, spec.Name)
	if err != nil {
		cancel()
		return nil, err
	}

	handle := &runnerHandle{
		runner: runner,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go func() {
		handle.finish(runRunnerFn(runner, runCtx))
	}()
	return handle, nil
}

type runnerHandle struct {
	runner   *agentloop.Runner
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
	mu       sync.Mutex
	stopErr  error
	waitErr  error
}

func (h *runnerHandle) finish(err error) {
	h.mu.Lock()
	h.waitErr = err
	h.mu.Unlock()
	close(h.done)
}

func (h *runnerHandle) Interrupt(ctx context.Context) error {
	return h.runner.Interrupt(ctx)
}

func (h *runnerHandle) Stop(ctx context.Context) error {
	h.stopOnce.Do(func() {
		h.stopErr = stopRunnerFn(h.runner, ctx)
		if h.cancel != nil {
			h.cancel()
		}
	})
	if h.stopErr != nil {
		return h.stopErr
	}
	select {
	case <-h.done:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *runnerHandle) SessionID() string {
	return h.runner.SessionID()
}

func (h *runnerHandle) Capabilities() backendpkg.Capabilities {
	return h.runner.Capabilities()
}

func buildRunnerDeps(spec RuntimeStartSpec) *agentloop.RunnerDeps {
	namespace := state.ReadNamespace(spec.SprawlRoot)
	if namespace == "" {
		namespace = os.Getenv("SPRAWL_NAMESPACE")
	}
	return &agentloop.RunnerDeps{
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_AGENT_IDENTITY":
				return spec.Name
			case "SPRAWL_ROOT":
				return spec.SprawlRoot
			case "SPRAWL_TREE_PATH":
				return spec.TreePath
			case "SPRAWL_NAMESPACE":
				return namespace
			default:
				return os.Getenv(key)
			}
		},
		LoadAgent:    state.LoadAgent,
		NextTask:     state.NextTask,
		UpdateTask:   state.UpdateTask,
		ListMessages: messages.List,
		SendMessage: func(root, from, to, subject, body string) error {
			return messages.Send(root, from, to, subject, body)
		},
		FindClaude: func() (string, error) {
			return exec.LookPath("claude")
		},
		ReadFile:   os.ReadFile,
		RemoveFile: os.Remove,
		BuildPrompt: func(a *state.AgentState) string {
			testMode := os.Getenv("SPRAWL_TEST_MODE") == "1"
			switch a.Type {
			case "researcher":
				env := agent.DefaultEnvConfig()
				env.TestMode = testMode
				return agent.BuildResearcherPrompt(a.Name, a.Parent, a.Branch, env)
			case "manager":
				env := agent.DefaultEnvConfig()
				env.WorkDir = a.Worktree
				env.TestMode = testMode
				return agent.BuildManagerPrompt(a.Name, a.Parent, a.Branch, a.Family, env)
			default:
				env := agent.DefaultEnvConfig()
				env.WorkDir = a.Worktree
				env.TestMode = testMode
				return agent.BuildEngineerPrompt(a.Name, a.Parent, a.Branch, env)
			}
		},
		SleepFunc:  time.Sleep,
		MkdirAll:   os.MkdirAll,
		CreateFile: os.Create,
		Stdout:     io.Discard,
		NewProcess: func(config agentloop.ProcessConfig, observer agentloop.Observer) agentloop.ProcessManager {
			starter := &agentloop.RealCommandStarter{}
			return agentloop.NewProcess(config, starter, agentloop.WithObserver(observer))
		},
		NewBackendProcess: agentloop.NewClaudeBackendProcess,
		NewWorkLock: func(lockDir, agentName string) (*agentloop.WorkLock, error) {
			if err := os.MkdirAll(lockDir, 0o755); err != nil { //nolint:gosec // world-readable lock dir is intentional
				return nil, fmt.Errorf("creating locks directory: %w", err)
			}
			lockPath := filepath.Join(lockDir, agentName+".lock")
			fl := flock.New(lockPath)
			return &agentloop.WorkLock{
				Acquire: func() error { return fl.Lock() },
				Release: func() error { return fl.Unlock() },
			}, nil
		},
		Getpid: os.Getpid,
	}
}

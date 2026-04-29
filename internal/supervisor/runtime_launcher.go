package supervisor

import (
	"context"
	"errors"
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

type inProcessRuntimeStarter struct {
	initSpec     backendpkg.InitSpec
	allowedTools []string
}

var (
	startRunnerFn = agentloop.StartRunner
	runRunnerFn   = func(r *agentloop.Runner, ctx context.Context) error { return r.Run(ctx) }
	stopRunnerFn  = func(r *agentloop.Runner, ctx context.Context) error { return r.Stop(ctx) }
)

func newInProcessRuntimeStarter(initSpec backendpkg.InitSpec, allowedTools []string) RuntimeStarter {
	return &inProcessRuntimeStarter{initSpec: initSpec, allowedTools: allowedTools}
}

func (s *inProcessRuntimeStarter) Start(_ context.Context, spec RuntimeStartSpec) (RuntimeHandle, error) {
	runCtx, cancel := context.WithCancel(context.Background())
	deps := buildRunnerDeps(spec)
	if s.initSpec.ToolBridge != nil || len(s.initSpec.MCPServerNames) > 0 {
		deps.InitSpec = s.initSpec
		deps.AllowedTools = s.allowedTools
	}
	controlCh := make(chan agentloop.ControlSignal, 1)
	deps.ControlCh = controlCh
	runner, err := startRunnerFn(runCtx, deps, spec.Name)
	if err != nil {
		cancel()
		return nil, err
	}

	handle := &runnerHandle{
		runner:    runner,
		cancel:    cancel,
		done:      make(chan struct{}),
		controlCh: controlCh,
	}
	go func() {
		handle.finish(runRunnerFn(runner, runCtx))
	}()
	return handle, nil
}

type runnerHandle struct {
	runner    *agentloop.Runner
	cancel    context.CancelFunc
	done      chan struct{}
	controlCh chan agentloop.ControlSignal
	stopOnce  sync.Once
	mu        sync.Mutex
	stopErr   error
	waitErr   error
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

func (h *runnerHandle) Wake() error {
	return h.sendControl(agentloop.ControlSignalWake)
}

func (h *runnerHandle) InterruptDelivery() error {
	return h.sendControl(agentloop.ControlSignalInterrupt)
}

func (h *runnerHandle) Stop(ctx context.Context) error {
	h.stopOnce.Do(func() {
		h.stopErr = stopRunnerFn(h.runner, ctx)
		if h.cancel != nil {
			h.cancel()
		}
	})
	if h.stopErr != nil && !isExitError(h.stopErr) {
		return h.stopErr
	}
	select {
	case <-h.done:
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.waitErr != nil && !isExitError(h.waitErr) {
			return h.waitErr
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isExitError reports whether err wraps an *exec.ExitError. During intentional
// shutdown the child process typically exits non-zero (exit status 1, signal:
// killed); these are expected teardown noise, not real failures.
func isExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func (h *runnerHandle) sendControl(sig agentloop.ControlSignal) error {
	if h.controlCh == nil {
		return fmt.Errorf("runtime control channel not configured")
	}
	select {
	case <-h.done:
		return fmt.Errorf("runtime session not running")
	default:
	}

	select {
	case h.controlCh <- sig:
		return nil
	default:
	}

	// Collapse duplicate wake signals, but upgrade a buffered wake to an
	// interrupt when a stronger delivery signal arrives.
	if sig == agentloop.ControlSignalInterrupt {
		select {
		case <-h.controlCh:
		default:
		}
		select {
		case h.controlCh <- sig:
		default:
		}
	}
	return nil
}

func (h *runnerHandle) SessionID() string {
	return h.runner.SessionID()
}

func (h *runnerHandle) Done() <-chan struct{} {
	return h.done
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

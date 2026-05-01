package supervisor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	backendclaude "github.com/dmotles/sprawl/internal/backend/claude"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/protocol"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
)

// unifiedAdapterStartFn is the seam for the backend Claude adapter. Tests
// override it to inject a fake backend.Session without spawning subprocesses.
var unifiedAdapterStartFn = func(ctx context.Context, spec backendpkg.SessionSpec) (backendpkg.Session, error) {
	return backendclaude.NewAdapter(backendclaude.Config{}).Start(ctx, spec)
}

// unifiedRuntimeNewFn is the seam for constructing the UnifiedRuntime. Tests
// override it to swap in a doubles-friendly runtime.
var unifiedRuntimeNewFn = runtimepkg.New

type inProcessUnifiedStarter struct {
	initSpec     backendpkg.InitSpec
	allowedTools []string
}

func newInProcessUnifiedStarter(initSpec backendpkg.InitSpec, allowedTools []string) RuntimeStarter {
	return &inProcessUnifiedStarter{initSpec: initSpec, allowedTools: allowedTools}
}

func (s *inProcessUnifiedStarter) Start(ctx context.Context, spec RuntimeStartSpec) (RuntimeHandle, error) {
	agentState, err := state.LoadAgent(spec.SprawlRoot, spec.Name)
	if err != nil {
		return nil, err
	}

	systemPrompt := buildAgentSystemPrompt(agentState)
	promptPath, err := state.WriteSystemPrompt(spec.SprawlRoot, spec.Name, systemPrompt)
	if err != nil {
		return nil, err
	}

	sessionSpec := agentloop.BuildAgentSessionSpec(agentState, promptPath, spec.SprawlRoot, io.Discard)
	if len(s.allowedTools) > 0 {
		sessionSpec.AllowedTools = s.allowedTools
	}

	activityDir := filepath.Join(spec.SprawlRoot, ".sprawl", "agents", spec.Name)
	if err := os.MkdirAll(activityDir, 0o750); err != nil {
		return nil, err
	}
	activityFile, err := os.OpenFile(agentloop.ActivityPath(spec.SprawlRoot, spec.Name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G304: path is derived from trusted inputs
	if err != nil {
		return nil, err
	}
	ring := agentloop.NewActivityRing(agentloop.DefaultActivityCapacity, activityFile)
	observer := &agentloop.ObserverWriter{W: io.Discard, Ring: ring}

	// Per QUM-398 plan §4 risk #10: do NOT also assign sessionSpec.Observer
	// to the activity ObserverWriter — only the EventBus subscriber writes
	// activity, to avoid double-write.

	session, err := unifiedAdapterStartFn(ctx, sessionSpec)
	if err != nil {
		if activityFile != nil {
			_ = activityFile.Close()
		}
		return nil, err
	}

	if s.initSpec.ToolBridge != nil || len(s.initSpec.MCPServerNames) > 0 {
		if err := session.Initialize(ctx, s.initSpec); err != nil {
			_ = session.Close()
			_ = session.Wait()
			if activityFile != nil {
				_ = activityFile.Close()
			}
			return nil, err
		}
	}

	caps := session.Capabilities()
	rt := unifiedRuntimeNewFn(runtimepkg.RuntimeConfig{
		Name:          spec.Name,
		SprawlRoot:    spec.SprawlRoot,
		Session:       session,
		InitialPrompt: agentState.Prompt,
		Capabilities:  caps,
	})

	// Activity subscriber: forwards EventProtocolMessage to the
	// ObserverWriter (which writes activity.ndjson).
	stopActivity := runActivitySubscriber(rt.EventBus(), observer)

	if err := rt.Start(context.Background()); err != nil {
		stopActivity()
		_ = session.Close()
		_ = session.Wait()
		if activityFile != nil {
			_ = activityFile.Close()
		}
		return nil, err
	}

	return &unifiedHandle{
		rt:           rt,
		session:      session,
		capabilities: caps,
		sessionID:    session.SessionID(),
		activityFile: activityFile,
		stopActivity: stopActivity,
		sprawlRoot:   spec.SprawlRoot,
		name:         spec.Name,
	}, nil
}

// buildAgentSystemPrompt mirrors the BuildPrompt closure in
// runtime_launcher.go's buildRunnerDeps so the unified path produces the same
// system prompt as the legacy runner.
func buildAgentSystemPrompt(a *state.AgentState) string {
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
}

// runActivitySubscriber subscribes to bus and forwards EventProtocolMessage
// events to obs.OnMessage. The returned stop function unsubscribes (which
// closes the channel) and waits for the goroutine to drain. Exposed for
// testability.
func runActivitySubscriber(bus *runtimepkg.EventBus, obs interface {
	OnMessage(*protocol.Message)
},
) func() {
	ch, unsub := bus.Subscribe(64)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for ev := range ch {
			if ev.Type == runtimepkg.EventProtocolMessage && ev.Message != nil {
				obs.OnMessage(ev.Message)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			unsub()
			<-doneCh
		})
	}
}

type unifiedHandle struct {
	rt           *runtimepkg.UnifiedRuntime
	session      backendpkg.Session
	capabilities backendpkg.Capabilities
	sessionID    string
	activityFile *os.File
	stopActivity func()
	sprawlRoot   string
	name         string

	stopOnce sync.Once
	stopErr  error
}

func (h *unifiedHandle) Interrupt(ctx context.Context) error {
	// Delegates to UnifiedRuntime.Interrupt, which forwards to the backend
	// session unconditionally (QUM-435) and additionally drives runtime-state
	// bookkeeping when a turn is in flight.
	return h.rt.Interrupt(ctx)
}

func (h *unifiedHandle) Wake() error {
	h.rt.Queue().Wake()
	return nil
}

func (h *unifiedHandle) InterruptDelivery() error {
	pending, err := agentloop.ListPending(h.sprawlRoot, h.name)
	if err == nil && len(pending) > 0 {
		interrupts, asyncs := inboxprompt.SplitByClass(pending)
		if len(interrupts) > 0 {
			ids := make([]string, 0, len(interrupts))
			for _, e := range interrupts {
				ids = append(ids, e.ID)
			}
			h.rt.Queue().Enqueue(runtimepkg.QueueItem{
				Class:    runtimepkg.ClassInterrupt,
				Prompt:   inboxprompt.BuildInterruptFlushPrompt(interrupts),
				EntryIDs: ids,
			})
		}
		if len(asyncs) > 0 {
			ids := make([]string, 0, len(asyncs))
			for _, e := range asyncs {
				ids = append(ids, e.ID)
			}
			h.rt.Queue().Enqueue(runtimepkg.QueueItem{
				Class:    runtimepkg.ClassInbox,
				Prompt:   inboxprompt.BuildQueueFlushPrompt(asyncs),
				EntryIDs: ids,
			})
		}
	}
	return h.rt.InterruptDelivery(context.Background())
}

func (h *unifiedHandle) Stop(ctx context.Context) error {
	h.stopOnce.Do(func() {
		err := h.rt.Stop(ctx)
		if h.stopActivity != nil {
			h.stopActivity()
		}
		if h.session != nil {
			_ = h.session.Close()
			_ = h.session.Wait()
		}
		if h.activityFile != nil {
			_ = h.activityFile.Close()
		}
		if err != nil && !isExitError(err) {
			h.stopErr = err
		}
	})
	if h.stopErr != nil {
		return h.stopErr
	}
	return nil
}

func (h *unifiedHandle) SessionID() string {
	return h.sessionID
}

func (h *unifiedHandle) Capabilities() backendpkg.Capabilities {
	return h.capabilities
}

func (h *unifiedHandle) Done() <-chan struct{} {
	return h.rt.Done()
}

// isUnifiedHandle marks this handle as a UnifiedRuntime-backed handle for
// messages.RecipientResolver routing. See QUM-438.
func (h *unifiedHandle) isUnifiedHandle() {}

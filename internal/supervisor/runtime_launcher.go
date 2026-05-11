package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	backendclaude "github.com/dmotles/sprawl/internal/backend/claude"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/protocol"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
)

// isExitError reports whether err wraps an *exec.ExitError. During intentional
// shutdown the child process typically exits non-zero (exit status 1, signal:
// killed); these are expected teardown noise, not real failures.
func isExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

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
	sprawlRoot, name := spec.SprawlRoot, spec.Name
	rt := unifiedRuntimeNewFn(runtimepkg.RuntimeConfig{
		Name:          spec.Name,
		SprawlRoot:    spec.SprawlRoot,
		Session:       session,
		InitialPrompt: agentState.Prompt,
		Capabilities:  caps,
		OnQueueItemDelivered: func(it runtimepkg.QueueItem) {
			for _, id := range it.EntryIDs {
				if strings.HasPrefix(id, "task:") {
					taskID := strings.TrimPrefix(id, "task:")
					found, err := state.GetTask(sprawlRoot, name, taskID)
					if err != nil {
						slog.Default().Warn(
							"unified-runtime: get task on delivery failed",
							slog.String("agent", name),
							slog.String("task_id", taskID),
							slog.Any("err", err),
						)
						continue
					}
					found.Status = "done"
					found.DoneAt = time.Now().UTC().Format(time.RFC3339)
					if err := state.UpdateTask(sprawlRoot, name, found); err != nil {
						slog.Default().Warn(
							"unified-runtime: mark task done failed",
							slog.String("agent", name),
							slog.String("task_id", taskID),
							slog.Any("err", err),
						)
					}
					continue
				}
				if err := agentloop.MarkDelivered(sprawlRoot, name, id); err != nil {
					slog.Default().Warn(
						"unified-runtime: mark delivered failed",
						slog.String("agent", name),
						slog.String("entry_id", id),
						slog.Any("err", err),
					)
				}
			}
		},
	})

	// Activity subscriber: forwards EventProtocolMessage to the
	// ObserverWriter (which writes activity.ndjson).
	stopActivity := runActivitySubscriber(rt.EventBus(), observer, "activity")

	if err := rt.Start(context.Background()); err != nil {
		stopActivity()
		_ = session.Close()
		_ = session.Wait()
		if activityFile != nil {
			_ = activityFile.Close()
		}
		return nil, err
	}

	handle := &unifiedHandle{
		rt:           rt,
		session:      session,
		capabilities: caps,
		sessionID:    session.SessionID(),
		activityFile: activityFile,
		stopActivity: stopActivity,
		sprawlRoot:   spec.SprawlRoot,
		name:         spec.Name,
	}
	handle.feedTasks()
	return handle, nil
}

// buildAgentSystemPrompt renders the system prompt for a child agent based on
// its type.
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
}, name string,
) func() {
	ch, unsub := bus.SubscribeNamed(name, 64)
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

	tasksMu  sync.Mutex
	stopOnce sync.Once
	stopErr  error
}

// feedTasks drains queued tasks from on-disk state into the runtime queue,
// flipping each to in-progress as it is enqueued. Idempotent across concurrent
// callers via tasksMu and EntryID-based dedup in the runtime queue.
func (h *unifiedHandle) feedTasks() {
	if h.rt.State() == runtimepkg.StateStopped {
		return
	}
	h.tasksMu.Lock()
	defer h.tasksMu.Unlock()
	tasks, err := state.ListTasks(h.sprawlRoot, h.name)
	if err != nil {
		slog.Default().Warn(
			"unified-runtime: feedTasks list failed",
			slog.String("agent", h.name),
			slog.Any("err", err),
		)
		return
	}
	for _, tk := range tasks {
		if tk.Status != "queued" {
			continue
		}
		tk.Status = "in-progress"
		tk.StartedAt = time.Now().UTC().Format(time.RFC3339)
		if err := state.UpdateTask(h.sprawlRoot, h.name, tk); err != nil {
			slog.Default().Warn(
				"unified-runtime: feedTasks update failed",
				slog.String("agent", h.name),
				slog.String("task_id", tk.ID),
				slog.Any("err", err),
			)
			continue
		}
		prompt := tk.Prompt
		if tk.PromptFile != "" {
			prompt = "You have a new task. Read it from @" + tk.PromptFile + " and begin working."
		}
		h.rt.Queue().Enqueue(runtimepkg.QueueItem{
			Class:    runtimepkg.ClassTask,
			Prompt:   prompt,
			EntryIDs: []string{"task:" + tk.ID},
		})
	}
}

func (h *unifiedHandle) Interrupt(ctx context.Context) error {
	// Delegates to UnifiedRuntime.Interrupt, which forwards to the backend
	// session unconditionally (QUM-435) and additionally drives runtime-state
	// bookkeeping when a turn is in flight.
	return h.rt.Interrupt(ctx)
}

func (h *unifiedHandle) Wake() error {
	h.feedTasks()
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

// unifiedHandleStopWaitTimeout bounds the post-Kill session.Wait() inside
// unifiedHandle.Stop. QUM-542: a stuck Claude Code Task subshell can hold the
// child claude process's stdout pipe FD open even after SIGKILL of the parent,
// which makes exec.Cmd.Wait() block on pipe-drain for many minutes. Retire
// (Real.Retire → runtime.Stop → handle.Stop) was waiting synchronously on
// that drain and never reached its `retire.preflight` checkpoint, producing
// a multi-minute hang. Bounding the wait keeps retire snappy; the OS reaps
// the SIGKILL'd process eventually.
const unifiedHandleStopWaitTimeout = 5 * time.Second

func (h *unifiedHandle) Stop(ctx context.Context) error {
	h.stopOnce.Do(func() {
		err := h.rt.Stop(ctx)
		if h.stopActivity != nil {
			h.stopActivity()
		}
		if h.session != nil {
			// QUM-543: must SIGKILL the backend subprocess, not just close
			// stdin and Wait. claude mid-turn ignores stdin EOF, so without
			// Kill the process survives while handle.Stop returns success —
			// making mcp__sprawl__kill lie to its caller. Order: Close (EOF
			// stdin) → Kill (SIGKILL) → bounded Wait (reap). Mirrors the
			// long-standing pattern in WeaveRuntimeHandle.Stop (weave_handle.go).
			//
			// QUM-542: Wait is bounded. A stuck Claude Code Task subshell
			// holding the parent's stdout pipe FD open after SIGKILL can make
			// exec.Cmd.Wait block on pipe-drain for many minutes, which wedges
			// retire (Real.Retire → runtime.Stop → handle.Stop → session.Wait)
			// and prevents the `retire.preflight` checkpoint from emitting.
			// We run Wait in a goroutine and abandon it on timeout. SIGKILL
			// already landed; the OS will eventually reap the zombie.
			_ = h.session.Close()
			_ = h.session.Kill()
			waitDone := make(chan struct{})
			go func() {
				_ = h.session.Wait()
				close(waitDone)
			}()
			select {
			case <-waitDone:
			case <-time.After(unifiedHandleStopWaitTimeout):
				slog.Warn("unified-handle: session.Wait abandoned after SIGKILL — likely stuck child pipe FD (QUM-542)",
					"session_id", h.sessionID,
					"timeout", unifiedHandleStopWaitTimeout)
			}
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

// UnifiedRuntime returns the underlying UnifiedRuntime so the TUI viewport
// stream wiring (QUM-439) can subscribe to its EventBus.
func (h *unifiedHandle) UnifiedRuntime() *runtimepkg.UnifiedRuntime { return h.rt }

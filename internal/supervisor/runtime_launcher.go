package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	// statusDrainer, when non-nil, returns and clears the ephemeral
	// status-notification ring for the recipient agent (QUM-559). The
	// unified-runtime drain pipeline calls this for each agent so
	// report_status lines reach the recipient's next-turn prompt
	// without traversing the maildir.
	statusDrainer func(name string) []string
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

	if err := session.Start(context.Background()); err != nil {
		_ = session.Close()
		_ = session.Wait()
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

	// QUM-580: allocate the handle up-front so the OnQueueItemDelivered and
	// PostTurnSweep closures can capture it. The handle's runtime/session
	// fields are populated after unifiedRuntimeNewFn / rt.Start succeed.
	handle := &unifiedHandle{
		sprawlRoot:    spec.SprawlRoot,
		name:          spec.Name,
		statusDrainer: s.statusDrainer,
	}
	handle.wakeForDeliveryFn = handle.WakeForDelivery

	rt := unifiedRuntimeNewFn(runtimepkg.RuntimeConfig{
		Name:          spec.Name,
		SprawlRoot:    spec.SprawlRoot,
		Session:       session,
		InitialPrompt: agentState.Prompt,
		Capabilities:  caps,
		// Defends against wedged-SDK hangs (QUM-578/QUM-581). 30m is long
		// enough for long autonomous turns but bounded so an SDK that opens
		// system:init and never closes doesn't permanently freeze the agent.
		TurnTimeout:   30 * time.Minute,
		PostTurnSweep: func() { handle.postTurnSweep() },
		OnQueueItemDelivered: func(it runtimepkg.QueueItem) {
			if len(it.EntryIDs) > 0 {
				handle.sweepMu.Lock()
				handle.sweepDeliveredItems++
				handle.sweepMu.Unlock()
			}
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

	// QUM-580: delivery-confirmation subscriber tracks messages_read
	// tool_use blocks and resets sweep counters on EventTurnStarted.
	stopDelivery := runDeliveryConfirmationSubscriber(rt.EventBus(), handle, "delivery-confirmation")

	// Populate handle fields before rt.Start so the PostTurnSweep closure
	// (which calls handle.WakeForDelivery → handle.rt) observes a fully
	// constructed handle when the turn loop fires its first sweep.
	handle.rt = rt
	handle.session = session
	handle.capabilities = caps
	handle.sessionID = session.SessionID()
	handle.activityFile = activityFile
	handle.stopActivity = stopActivity
	handle.stopDelivery = stopDelivery

	if err := rt.Start(context.Background()); err != nil {
		stopDelivery()
		stopActivity()
		_ = session.Close()
		_ = session.Wait()
		if activityFile != nil {
			_ = activityFile.Close()
		}
		return nil, err
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
	rt            *runtimepkg.UnifiedRuntime
	session       backendpkg.Session
	capabilities  backendpkg.Capabilities
	sessionID     string
	activityFile  *os.File
	activityClose func() error
	stopActivity  func()
	sprawlRoot    string
	name          string
	// statusDrainer, when non-nil, returns and clears the ephemeral
	// status-notification ring for this agent (QUM-559). Lines are
	// prepended to the next async-class queue item so child managers
	// see their descendants' status reports in the next-turn prompt.
	statusDrainer func(name string) []string

	tasksMu  sync.Mutex
	stopOnce sync.Once
	stopErr  error

	stopWaitTimedOut atomic.Bool

	// QUM-580: defense-in-depth post-turn pending-envelope sweep state.
	// sweepDeliveredItems counts QueueItems-with-EntryIDs delivered during
	// the current turn (incremented once per QueueItem under sweepMu from
	// OnQueueItemDelivered, regardless of how many envelope EntryIDs the
	// item carried). sweepSawMessagesRead is set true by the
	// delivery-confirmation subscriber when it observes the agent invoking
	// the mcp__sprawl__messages_read tool, indicating the model actually
	// drained its inbox. postTurnSweep reads both, decides whether a wake
	// is needed, and resets both back to zero.
	sweepMu              sync.Mutex
	sweepDeliveredItems  int
	sweepSawMessagesRead bool
	// wakeForDeliveryFn is the seam invoked by postTurnSweep when it
	// decides a wake is needed. Production wires this to
	// (*unifiedHandle).WakeForDelivery; tests inject a counter.
	wakeForDeliveryFn func() error
	// stopDelivery tears down the delivery-confirmation subscriber.
	stopDelivery func()
}

// sweepMessagesReadToolName is the MCP tool name the
// delivery-confirmation subscriber watches for. Observing this tool_use
// block confirms the agent drained its inbox during the current turn,
// which suppresses the defense-in-depth wake from postTurnSweep.
const sweepMessagesReadToolName = "mcp__sprawl__messages_read"

// StopWaitTimedOut reports whether the bounded session.Wait() inside Stop hit
// its timeout (QUM-542). Used by Real.Retire/Kill to surface the fact via the
// retire.runtime-stop-done / kill.runtime-stop-done MCP-call checkpoints
// (QUM-546). Safe to call concurrently and after Stop returns.
func (h *unifiedHandle) StopWaitTimedOut() bool {
	return h.stopWaitTimedOut.Load()
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

// WakeForDelivery is the cooperative-wake variant. It drains pending entries
// into the runtime queue (so the next turn boundary observes them) and then
// calls the runtime's cooperative wake path — which never calls
// Session.Interrupt. See QUM-549/QUM-550.
func (h *unifiedHandle) WakeForDelivery() error {
	h.drainPendingToQueue()
	return h.rt.WakeForDelivery(context.Background())
}

// ForceInterruptDelivery is the unconditional-preempt variant. Drains
// pending entries and then forces an interrupt (even when idle). See
// QUM-549/QUM-550.
func (h *unifiedHandle) ForceInterruptDelivery() error {
	h.drainPendingToQueue()
	return h.rt.ForceInterruptForDelivery(context.Background())
}

func (h *unifiedHandle) drainPendingToQueue() {
	pending, err := agentloop.ListPending(h.sprawlRoot, h.name)
	if err != nil {
		slog.Default().Debug(
			"unified-runtime: drainPendingToQueue ListPending failed",
			slog.String("agent", h.name),
			slog.Any("err", err),
		)
	}
	var statusLines []string
	if h.statusDrainer != nil {
		statusLines = h.statusDrainer(h.name)
	}
	if len(pending) == 0 && len(statusLines) == 0 {
		return
	}
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
	// QUM-559: status lines ride along with the async batch, prepended
	// so they surface before any queued maildir messages. When only
	// status lines exist (no asyncs), emit them as their own ClassInbox
	// item with no entry IDs (nothing to MarkDelivered).
	if len(asyncs) > 0 || len(statusLines) > 0 {
		ids := make([]string, 0, len(asyncs))
		for _, e := range asyncs {
			ids = append(ids, e.ID)
		}
		var prompt strings.Builder
		for _, line := range statusLines {
			prompt.WriteString(line)
		}
		prompt.WriteString(inboxprompt.BuildQueueFlushPrompt(asyncs))
		h.rt.Queue().Enqueue(runtimepkg.QueueItem{
			Class:    runtimepkg.ClassInbox,
			Prompt:   prompt.String(),
			EntryIDs: ids,
		})
	}
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
		if h.stopDelivery != nil {
			joinWithTimeout(h.stopDelivery, stopActivityTimeout,
				"stopDelivery abandoned — likely wedged delivery-confirmation subscriber goroutine (QUM-580)",
				"handle", "unifiedHandle", "agent", h.name)
		}
		if h.stopActivity != nil {
			joinWithTimeout(h.stopActivity, stopActivityTimeout,
				"stopActivity abandoned — likely wedged activity subscriber goroutine (QUM-547)",
				"handle", "unifiedHandle", "agent", h.name)
		}
		// QUM-545: shared Close → Kill → bounded Wait helper. See
		// teardown_session.go for the canonical pattern + QUM-542/QUM-543
		// rationale (also mirrored in WeaveRuntimeHandle.Stop).
		// QUM-546: capture the bounded-Wait timeout signal so Real.Retire/Kill
		// can surface it via the retire.runtime-stop-done / kill.runtime-stop-done
		// MCP-call checkpoints.
		if teardownSession(h.session, unifiedHandleStopWaitTimeout, "handle", "unifiedHandle", "session_id", h.sessionID) {
			h.stopWaitTimedOut.Store(true)
		}
		if h.activityFile != nil || h.activityClose != nil {
			closer := h.activityClose
			if closer == nil {
				closer = h.activityFile.Close
			}
			joinWithTimeout(func() { _ = closer() }, activityCloseTimeout,
				"activityFile.Close abandoned — likely stuck FD on activity.ndjson (QUM-547)",
				"handle", "unifiedHandle", "agent", h.name)
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

// postTurnSweep is the defense-in-depth check invoked from the turn loop
// after every turn boundary (QUM-580). It decides whether to fire a
// cooperative wake based on two conditions:
//
//  1. deliveredCount > 0 && !sawMessagesRead — items were handed to the
//     model this turn, but the model never invoked messages_read to drain
//     them. The model may have ignored the inbox; nudge it on the next
//     boundary so a follow-up turn happens.
//  2. len(pending/) > 0 — the on-disk pending queue is non-empty,
//     regardless of in-memory counters. A pending file may have been
//     written by a peer mid-turn and never drained into the runtime
//     queue; this is the canonical defense-in-depth path.
//
// Counters are reset under sweepMu before any blocking I/O so the next
// turn starts clean. wakeForDeliveryFn is invoked without sweepMu held.
func (h *unifiedHandle) postTurnSweep() {
	h.sweepMu.Lock()
	delivered := h.sweepDeliveredItems
	sawRead := h.sweepSawMessagesRead
	h.sweepDeliveredItems = 0
	h.sweepSawMessagesRead = false
	h.sweepMu.Unlock()

	needWake := delivered > 0 && !sawRead
	if !needWake {
		pending, err := agentloop.ListPending(h.sprawlRoot, h.name)
		if err != nil {
			slog.Default().Debug(
				"unified-runtime: postTurnSweep ListPending failed",
				slog.String("agent", h.name),
				slog.Any("err", err),
			)
		}
		if len(pending) > 0 {
			needWake = true
		}
	}
	if !needWake {
		return
	}
	if h.wakeForDeliveryFn != nil {
		_ = h.wakeForDeliveryFn()
	}
}

// runDeliveryConfirmationSubscriber subscribes to bus and watches the
// agent's protocol-message stream for two signals:
//
//   - EventTurnStarted: resets both sweep counters on the handle so each
//     turn starts with a clean slate.
//   - EventProtocolMessage carrying an assistant tool_use block with
//     name == sweepMessagesReadToolName: sets sweepSawMessagesRead=true,
//     confirming the model drained its inbox this turn.
//
// The returned function unsubscribes and waits for the goroutine to drain.
// Parsing follows the same assistant tool_use shape used by
// internal/agentloop/activity.go. QUM-583 considered factoring this into a
// shared pre-decoded ParsedEvent fanned out by the EventBus, but rejected it:
// the two consumers extract different fields (activity needs text + tool
// input; this subscriber needs only the tool name) and sharing would require
// plumbing a new event type through the EventBus and both subscribers for a
// per-event saving of one small JSON unmarshal — not worth the coupling.
func runDeliveryConfirmationSubscriber(bus *runtimepkg.EventBus, h *unifiedHandle, name string) func() {
	ch, unsub := bus.SubscribeNamed(name, 64)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for ev := range ch {
			switch ev.Type {
			case runtimepkg.EventTurnStarted:
				h.sweepMu.Lock()
				h.sweepDeliveredItems = 0
				h.sweepSawMessagesRead = false
				h.sweepMu.Unlock()
			case runtimepkg.EventProtocolMessage:
				if ev.Message == nil || ev.Message.Type != "assistant" {
					continue
				}
				var outer struct {
					Message struct {
						Content []struct {
							Type string `json:"type"`
							Name string `json:"name,omitempty"`
						} `json:"content"`
					} `json:"message"`
				}
				if err := json.Unmarshal(ev.Message.Raw, &outer); err != nil {
					continue
				}
				for _, block := range outer.Message.Content {
					if block.Type == "tool_use" && block.Name == sweepMessagesReadToolName {
						h.sweepMu.Lock()
						h.sweepSawMessagesRead = true
						h.sweepMu.Unlock()
						break
					}
				}
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

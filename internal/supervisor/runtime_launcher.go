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
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
	"github.com/dmotles/sprawl/internal/usage"
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
	// faultEmitter, when non-nil, is invoked by the per-runtime fault
	// subscriber whenever EventBackendFaulted fires on the runtime's
	// EventBus. The host TUI uses this to surface a fault banner +
	// tree-row indicator. QUM-602.
	faultEmitter func(agent, class, reason, nextAction string)
}

func newInProcessUnifiedStarter(initSpec backendpkg.InitSpec, allowedTools []string) RuntimeStarter {
	return &inProcessUnifiedStarter{initSpec: initSpec, allowedTools: allowedTools}
}

// preparedLaunch is the immutable result of phase 1 (state load + on-disk
// preparation) plus the prepared session spec consumed by phase 2.
type preparedLaunch struct {
	agentState   *state.AgentState
	sessionSpec  backendpkg.SessionSpec
	activityFile *os.File
	observer     *agentloop.ObserverWriter
	ring         *agentloop.ActivityRing
}

// Start orchestrates the in-process runtime launch as a sequence of discrete
// phases with explicit ordering and rollback. The phases are:
//
//  1. prepareLaunch       — load state, write system prompt, open activity file
//  2. startBackendSession — spawn the backend session; Start + optional Initialize
//  3. newSweepCoordinator — allocate the QUM-580 sweep state owner
//  4. unifiedRuntimeNewFn — construct the runtime; callbacks capture only the
//     coordinator, never a partially-built handle
//  5. attachSubscribers   — wire EventBus subscribers to the now-built runtime
//  6. assembleHandle      — populate unifiedHandle in one linear block; no
//     closure already created points into a half-built handle
//  7. coord.Bind          — install the wake function captured against the
//     fully-built handle (must happen before rt.Start so the first sweep is
//     well-defined)
//  8. rt.Start            — start the turn loop; first PostTurnSweep / first
//     OnQueueItemDelivered fire only after this returns
//  9. handle.feedTasks    — drain queued tasks into the runtime queue
//
// Each phase's rollback unwinds only what it constructed. The
// closure-capture-race fragility that motivated QUM-584 is gone by
// construction: the only closures stored in RuntimeConfig (phase 4) capture
// the coordinator (built in phase 3, immutable thereafter). The handle
// pointer is never referenced from any closure created before phase 6.
func (s *inProcessUnifiedStarter) Start(spec RuntimeStartSpec) (RuntimeHandle, error) {
	// Phase 1: prepare on-disk state and session spec.
	prep, err := s.prepareLaunch(spec)
	if err != nil {
		return nil, err
	}

	// Phase 2: start the backend session. Rollback on error: close activity
	// file. We derive context.Background() here (QUM-612): a request-scoped
	// ctx must NEVER reach exec.CommandContext (which is downstream of the
	// adapter), because cancellation of that ctx — e.g. when an MCP request
	// returns — would SIGKILL the freshly-spawned subprocess. See QUM-606.
	session, err := s.startBackendSession(context.Background(), prep)
	if err != nil {
		_ = prep.activityFile.Close()
		return nil, err
	}

	// Phase 3: allocate the sweep coordinator. Holds all immutable state the
	// turn-loop callbacks (phase 4) need; constructed in full before any
	// closure that references it exists.
	coord := newSweepCoordinator(spec.SprawlRoot, spec.Name)

	caps := session.Capabilities()

	// Phase 4: construct the runtime. The closures stored in RuntimeConfig
	// capture only `coord` — there is no `handle` reference reachable from
	// the turn loop, so there is no way for a partially-built handle to be
	// observed by the first PostTurnSweep / OnQueueItemDelivered firing.
	rt := unifiedRuntimeNewFn(runtimepkg.RuntimeConfig{
		Name:          spec.Name,
		SprawlRoot:    spec.SprawlRoot,
		Session:       session,
		InitialPrompt: prep.agentState.Prompt,
		Capabilities:  caps,
		// TurnTimeout=0 DISABLES the wall-clock per-turn cap (QUM-618 verdict).
		// The 30-min cap (QUM-578/QUM-581) was REMOVED because it false-
		// guillotined healthy long autonomous turns (the finn M4 incident:
		// a legitimate 30-min sub-agent turn was cut 3s before its deadline,
		// pinning the backend currentTurn and wedging the agent). The real
		// no-progress guard is the D1 frame-based hang watchdog
		// (defaultHangTimeout=10m, gated on currentTurn != nil per QUM-599) —
		// it catches a wedged-open SDK stream that stops emitting frames,
		// which is the actual failure mode a wall-clock cap cannot distinguish
		// from healthy long work. Do NOT re-add a wall-clock cap here. Note:
		// 0 is a DISABLED sentinel — turnloop.go guards `TurnTimeout > 0`
		// before wrapping with context.WithTimeout (passing 0 there would
		// yield an already-expired ctx and instantly wedge every turn).
		TurnTimeout:          0,
		PostTurnSweep:        coord.PostTurnSweep,
		OnQueueItemDelivered: coord.OnQueueItemDelivered,
	})

	// Phase 5: attach EventBus subscribers. Safe to do now — bus exists; turn
	// loop is not yet running.
	stopActivity := runActivitySubscriber(rt.EventBus(), prep.observer, "activity")
	stopDelivery := runDeliveryConfirmationSubscriber(rt.EventBus(), coord, "delivery-confirmation")
	// QUM-602: per-runtime backend-fault subscriber. Forwards
	// EventBackendFaulted out to the supervisor-level fault emitter (the
	// TUI installs this via Real.SetBackendFaultEmitter). When no emitter
	// is registered the subscriber still drains the bus so the channel
	// doesn't back up.
	stopFault := runFaultSubscriber(rt.EventBus(), spec.Name, s.faultEmitter, "backend-fault")

	// QUM-368: per-runtime usage recorder. Constructed here (needs sprawlRoot
	// + agent name). Failure to construct is non-fatal — we skip the
	// subscriber and continue without usage logging.
	usageRec, _ := usage.NewRecorder(spec.SprawlRoot, spec.Name)
	stopUsage := runUsageSubscriber(rt.EventBus(), usageRec, "usage")

	// Phase 6: assemble the handle. Single linear block, no closures already
	// in flight observe partial state.
	handle := &unifiedHandle{
		rt:           rt,
		session:      session,
		capabilities: caps,
		sessionID:    session.SessionID(),
		activityFile: prep.activityFile,
		stopActivity: stopActivity,
		stopDelivery: stopDelivery,
		stopFault:    stopFault,
		stopUsage:    stopUsage,
		sprawlRoot:   spec.SprawlRoot,
		name:         spec.Name,
		coord:        coord,
		ring:         prep.ring,
	}

	// Phase 7: bind the coordinator's wake function. Closure captures the
	// fully-built handle (assembled in phase 6), so handle.rt is guaranteed
	// non-nil. Must precede phase 8 so the first PostTurnSweep firing has a
	// non-nil wake.
	coord.Bind(handle.WakeForDelivery)

	// Phase 8: start the runtime. Rollback on error: tear down subscribers,
	// close + reap session, close activity file.
	if err := rt.Start(context.Background()); err != nil {
		stopUsage()
		stopFault()
		stopDelivery()
		stopActivity()
		_ = session.Close()
		_ = session.Wait()
		_ = prep.activityFile.Close()
		return nil, err
	}

	// Phase 9: drain queued tasks from on-disk state into the runtime queue.
	handle.feedTasks()
	return handle, nil
}

// prepareLaunch loads the agent state, writes the system prompt, builds the
// session spec, and opens the activity-log file. On error it closes the
// activity file if it was opened before failure.
func (s *inProcessUnifiedStarter) prepareLaunch(spec RuntimeStartSpec) (*preparedLaunch, error) {
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
	// QUM-601: propagate the Resume flag from the RuntimeStartSpec into the
	// backend SessionSpec so AgentRuntime.Recover's restart actually instructs
	// claude to resume the prior conversation transcript.
	sessionSpec.Resume = spec.Resume
	sessionSpec.OnResumeFailure = spec.OnResumeFailure

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
	return &preparedLaunch{
		agentState:   agentState,
		sessionSpec:  sessionSpec,
		activityFile: activityFile,
		observer:     observer,
		ring:         ring,
	}, nil
}

// startBackendSession invokes the adapter seam, calls session.Start, and (if
// the starter has a non-empty InitSpec) calls session.Initialize. On any
// failure after the session is returned by the adapter, it closes + reaps the
// session before returning so callers only need to close the activity file.
func (s *inProcessUnifiedStarter) startBackendSession(ctx context.Context, prep *preparedLaunch) (backendpkg.Session, error) {
	session, err := unifiedAdapterStartFn(ctx, prep.sessionSpec)
	if err != nil {
		return nil, err
	}
	if err := session.Start(context.Background()); err != nil {
		_ = session.Close()
		_ = session.Wait()
		return nil, err
	}
	if s.initSpec.ToolBridge != nil || len(s.initSpec.MCPServerNames) > 0 {
		if err := session.Initialize(ctx, s.initSpec); err != nil {
			_ = session.Close()
			_ = session.Wait()
			return nil, err
		}
	}
	return session, nil
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

// runUsageSubscriber subscribes to bus and forwards every RuntimeEvent to the
// usage.Recorder (QUM-368). Buffer is 32 — if full, the EventBus drops
// events for this subscriber only (existing QUM-681 drop telemetry surfaces
// it). The returned stop function unsubscribes (which closes the channel),
// waits for the goroutine to drain, then closes the recorder so the last
// in-flight file is fsync'd. A nil recorder is tolerated — the subscriber
// still drains the bus so the channel doesn't back up.
func runUsageSubscriber(bus *runtimepkg.EventBus, rec *usage.Recorder, name string) func() {
	ch, unsub := bus.SubscribeNamed(name, 32)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for ev := range ch {
			if rec == nil {
				continue
			}
			rec.Handle(ev)
		}
		if rec != nil {
			_ = rec.Close()
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

// runFaultSubscriber subscribes to bus and forwards EventBackendFaulted
// events to emitter (terminal fault banner). It ALSO forwards per-turn
// deadline expiries — EventTurnFailed wrapping context.DeadlineExceeded —
// under the distinct "TurnDeadlineExceeded" class so the TUI can show a
// recoverable banner WITHOUT driving the terminal handler (QUM-618). The
// returned stop function unsubscribes (closing the channel) and waits for the
// goroutine to drain. A nil emitter is tolerated — the subscriber still drains
// the bus so the channel doesn't back up. QUM-602.
func runFaultSubscriber(bus *runtimepkg.EventBus, agentName string, emitter func(agent, class, reason, nextAction string), name string) func() {
	ch, unsub := bus.SubscribeNamed(name, 4)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for ev := range ch {
			if emitter == nil {
				continue
			}
			switch {
			case ev.Type == runtimepkg.EventBackendFaulted:
				reason := ""
				if ev.Error != nil {
					reason = ev.Error.Error()
				}
				emitter(agentName, ev.FaultClass, reason, ev.FaultNextAction)
			case ev.Type == runtimepkg.EventTurnFailed && errors.Is(ev.Error, context.DeadlineExceeded):
				// QUM-618: a per-turn deadline is recoverable, NOT a terminal
				// fault — the agent remains live. Raise a DISTINCT banner via
				// the same emitter, but do NOT trip any terminal rt.cancel
				// path (only EventBackendFaulted drives the terminal handler in
				// unified.go New()).
				reason := ""
				if ev.Error != nil {
					reason = ev.Error.Error()
				}
				emitter(agentName, "TurnDeadlineExceeded", reason,
					"turn exceeded the per-turn time cap; work was interrupted but the agent remains live — re-send or it will resume on next wake")
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
	stopFault     func()
	stopUsage     func()
	sprawlRoot    string
	name          string

	tasksMu  sync.Mutex
	stopOnce sync.Once
	stopErr  error

	stopWaitTimedOut atomic.Bool

	// coord owns the QUM-580 sweep state and the runtime callbacks that
	// touch it (OnQueueItemDelivered, PostTurnSweep). Extracted from the
	// handle in QUM-584 so the runtime callbacks no longer capture a
	// partially-built *unifiedHandle.
	coord *sweepCoordinator
	// stopDelivery tears down the delivery-confirmation subscriber.
	stopDelivery func()

	ring *agentloop.ActivityRing
}

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
	if h.rt.State().Liveness == liveness.Stopped {
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
	statusLines := inboxprompt.DrainStatusChangeLines(h.sprawlRoot, h.name)
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
	return h.stopOnceWith(ctx, func(ctx context.Context) error { return h.rt.Stop(ctx) })
}

// StopAbandon is the QUM-600 teardown-only variant of Stop. It tells the
// UnifiedRuntime to skip its polite Session.Interrupt (so a wedged stdin
// pipe cannot stall retire) and otherwise mirrors Stop's
// subscriber-teardown / session-teardown / activity-close sequence.
func (h *unifiedHandle) StopAbandon(ctx context.Context) error {
	return h.stopOnceWith(ctx, func(ctx context.Context) error {
		return h.rt.StopWithOptions(ctx, runtimepkg.StopOptions{SkipPoliteInterrupt: true})
	})
}

// stopOnceWith is the shared body for Stop / StopAbandon. The caller picks
// how the UnifiedRuntime is stopped; everything else (subscriber teardown,
// session teardown, activity close) is identical.
func (h *unifiedHandle) stopOnceWith(ctx context.Context, stopRuntime func(context.Context) error) error {
	h.stopOnce.Do(func() {
		err := stopRuntime(ctx)
		if h.stopFault != nil {
			joinWithTimeout(h.stopFault, stopActivityTimeout,
				"stopFault abandoned — likely wedged backend-fault subscriber goroutine (QUM-602)",
				"handle", "unifiedHandle", "agent", h.name)
		}
		if h.stopUsage != nil {
			joinWithTimeout(h.stopUsage, stopActivityTimeout,
				"stopUsage abandoned — likely wedged usage subscriber goroutine (QUM-368)",
				"handle", "unifiedHandle", "agent", h.name)
		}
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

// InTurn reports whether the underlying backend session is
// currently servicing an autonomous (SDK-initiated) turn frame. See
// QUM-585 — surfaced through the peek MCP tool's JSON payload.
func (h *unifiedHandle) InTurn() bool {
	return h.session.InTurn()
}

// LastActivityAt returns the timestamp of the most recently recorded
// activity-ring entry on this runtime. Zero time when the ring is empty.
// (QUM-665)
func (h *unifiedHandle) LastActivityAt() time.Time {
	if h.ring == nil {
		return time.Time{}
	}
	return h.ring.LastAt()
}

// IsTerminallyFaulted reports whether the underlying backend session has been
// poisoned with a sticky terminal error (QUM-601). AgentRuntime.Recover probes
// the handle via this method to decide whether in-place recovery is needed.
func (h *unifiedHandle) IsTerminallyFaulted() bool {
	return h.session.IsTerminallyFaulted()
}

// InduceTerminalFault forwards to the underlying backend session's
// test-seam fault injector. Used by the QUM-606 build-tag-gated
// `_test_induce_wedge` MCP tool to drive a deterministic terminal fault.
// Production callers MUST NOT invoke this.
func (h *unifiedHandle) InduceTerminalFault(err error) {
	h.session.InduceTerminalFault(err)
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

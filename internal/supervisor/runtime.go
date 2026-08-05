package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// ErrWakeNotNeeded is returned by AgentRuntime.Wake when the live handle's
// backend session is still healthy (IsTerminallyFaulted() == false), signaling
// to callers (Real.Wake / the MCP wake tool) that the request is a no-op
// success. QUM-724 (renamed from ErrRecoverNotNeeded, QUM-601).
var ErrWakeNotNeeded = errors.New("supervisor: session healthy, no wake needed")

// unifiedRuntimeProvider is implemented by RuntimeHandles backed by a
// UnifiedRuntime, so consumers (e.g. the TUI viewport stream wiring) can
// reach the underlying runtime's EventBus.
type unifiedRuntimeProvider interface {
	UnifiedRuntime() *runtimepkg.UnifiedRuntime
}

// UnifiedRuntime returns the underlying UnifiedRuntime when this AgentRuntime
// is currently backed by a unified-handle; otherwise nil. (QUM-439)
func (r *AgentRuntime) UnifiedRuntime() *runtimepkg.UnifiedRuntime {
	handle := r.currentHandle()
	if handle == nil {
		return nil
	}
	if p, ok := handle.(unifiedRuntimeProvider); ok {
		return p.UnifiedRuntime()
	}
	return nil
}

// livenessToLifecycleString renders the stored base liveness as the legacy
// lifecycle token consumed by liveness.From. Only the five base/resting
// livenesses reachable from a stored snapshot are produced; everything else
// (including the Unstarted zero value) maps to "registered".
func livenessToLifecycleString(l liveness.AgentLiveness) string {
	switch l {
	case liveness.Running:
		return "started"
	case liveness.Stopped:
		return "stopped"
	case liveness.Killed:
		return "killed"
	case liveness.Retired:
		return "retired"
	default:
		return "registered"
	}
}

// RuntimeEventKind labels the kind of runtime snapshot change that occurred.
type RuntimeEventKind string

const (
	RuntimeEventStarted     RuntimeEventKind = "started"
	RuntimeEventStopped     RuntimeEventKind = "stopped"
	RuntimeEventInterrupted RuntimeEventKind = "interrupted"
	RuntimeEventTaskQueued  RuntimeEventKind = "task_queued"
	RuntimeEventStateSynced RuntimeEventKind = "state_synced"
	// RuntimeEventWoken fires after AgentRuntime.Wake swaps in a fresh
	// handle for an offline session (faulted/paused/killed/died/resume_failed).
	// QUM-724 (renamed from RuntimeEventRecovered, QUM-601).
	RuntimeEventWoken RuntimeEventKind = "woken"
	// RuntimeEventPaused fires after a clean Pause flow completes. QUM-722.
	RuntimeEventPaused RuntimeEventKind = "paused"
)

// RuntimeStartSpec is the internal-only launch seam for same-process child runtimes.
// QUM-351 keeps it inside internal/supervisor; QUM-352 can refine it further.
type RuntimeStartSpec struct {
	Name       string
	Worktree   string
	SprawlRoot string
	SessionID  string
	TreePath   string
	// Resume, when true, causes the backend session spec built during
	// prepareLaunch to set Resume=true so the spawned claude subprocess is
	// instructed to resume the prior conversation transcript (QUM-601 in-
	// place recovery). Initial-start paths leave it false (fresh session).
	Resume bool
	// OnResumeFailure, when non-nil and Resume==true, is invoked by the
	// backend session's stderr scanner if the resume cookie is invalid (the
	// "No conversation found" marker fires). Plumbed through to
	// SessionSpec.OnResumeFailure in prepareLaunch so children get the same
	// fast-fail signal weave does. QUM-372.
	OnResumeFailure func()
	// RestartInjection, when non-empty, replaces the otherwise-used
	// AgentState.Prompt as the first turn fed to the spawned claude session.
	// This is the seam Real.RecoverAgents uses (QUM-723) to deliver the canonical
	// neutral restart-injection prompt after `sprawl enter` relaunch. Empty on
	// fresh-spawn paths (preserves spawn-prompt behavior) and on in-place
	// recovery (AgentRuntime.Recover — that is a mid-session fault recovery,
	// not a sprawl-startup recovery, so no injection is desired).
	RestartInjection string
}

// RuntimeHandle is the live controller for a started in-process child runtime.
type RuntimeHandle interface {
	Interrupt(ctx context.Context) error
	Wake() error
	// WakeForDelivery is the delivery poke for send_message (both interrupt=
	// false and interrupt=true). Must NEVER call Session.Interrupt: urgency for
	// interrupt=true is carried by writing the message at priority `now`, not by
	// a bare interrupt frame (QUM-549/QUM-550/QUM-821).
	WakeForDelivery() error
	Stop(ctx context.Context) error
	// StopAbandon is the abandon-mode teardown variant: skips the polite
	// Session.Interrupt issued by Stop and goes straight to Close + Kill
	// + bounded Wait. Used by Real.Retire when abandon=true so a wedged
	// backend Interrupt cannot stall retire. (QUM-600)
	StopAbandon(ctx context.Context) error
	SessionID() string
	Capabilities() backendpkg.Capabilities
}

// RuntimeStarter starts a child runtime and returns its live handle.
//
// Start takes no ctx by design (QUM-612). The ctx parameter was previously
// forwarded all the way down to `exec.CommandContext`, which made it possible
// for a short-lived MCP request ctx (e.g. `toolRecover`'s) to SIGKILL the
// freshly-spawned subprocess the instant the MCP call returned — see QUM-606.
// Subprocess teardown is the responsibility of the returned handle's
// `Stop` / `StopAbandon` / `watchHandleExit` path, so the ctx-cancel safety
// net was unused and dangerous. By dropping ctx from the signature, the
// QUM-606 bug class is impossible to reintroduce.
type RuntimeStarter interface {
	Start(spec RuntimeStartSpec) (RuntimeHandle, error)
}

type runtimeHandleDone interface {
	Done() <-chan struct{}
}

// RuntimeSnapshot is the internal-only live snapshot future status/tree/TUI
// consumers can bind to without depending on legacy terminal-container state.
type RuntimeSnapshot struct {
	Name           string
	Type           string
	Family         string
	Parent         string
	Status         string
	Branch         string
	Worktree       string
	SessionID      string
	TreePath       string
	CreatedAt      string
	Liveness       liveness.AgentLiveness
	QueueDepth     int
	WakeCount      int
	InterruptCount int
	LastReport     LastReport
	Capabilities   backendpkg.Capabilities
}

// RuntimeEvent is emitted to per-runtime subscribers after a snapshot mutation.
type RuntimeEvent struct {
	Kind     RuntimeEventKind
	Snapshot RuntimeSnapshot
}

// AgentRuntimeConfig configures a supervisor-owned AgentRuntime.
type AgentRuntimeConfig struct {
	SprawlRoot string
	Agent      *state.AgentState
	Starter    RuntimeStarter
}

// AgentRuntime is the in-memory container for same-process child lifecycles.
// Persisted state remains the durable source of truth for recovery/history,
// while live lifecycle ownership sits here.
type AgentRuntime struct {
	mu         sync.RWMutex
	sprawlRoot string
	starter    RuntimeStarter
	handle     RuntimeHandle
	snapshot   RuntimeSnapshot

	nextSubscriberID int
	subscribers      map[int]chan RuntimeEvent

	stopWaitTimedOut atomic.Bool

	// expectingExit is set true by Stop/StopAbandon BEFORE invoking the
	// underlying handle's stop fn. watchHandleExit reads it on done close to
	// classify the exit: true → Stopped, false → Died. QUM-722.
	expectingExit atomic.Bool

	// wakeMu serializes Wake. TryLock-based fail-fast so concurrent callers
	// get a "wake already in progress" error rather than queuing.
	// QUM-724 (renamed from recoverMu, QUM-601).
	wakeMu sync.Mutex
}

// StopWaitTimedOut reports whether the most recent Stop on this runtime's
// handle saw a bounded session.Wait() timeout. Returns false if the handle
// does not surface this signal or Stop has not been called. (QUM-546)
func (r *AgentRuntime) StopWaitTimedOut() bool {
	return r.stopWaitTimedOut.Load()
}

// NewAgentRuntime constructs a runtime snapshot from persisted agent metadata.
func NewAgentRuntime(cfg AgentRuntimeConfig) *AgentRuntime {
	rt := &AgentRuntime{
		sprawlRoot:  cfg.SprawlRoot,
		starter:     cfg.Starter,
		subscribers: make(map[int]chan RuntimeEvent),
	}
	if cfg.Agent != nil {
		rt.snapshot = snapshotFromAgentState(cfg.Agent)
	}
	return rt
}

func snapshotFromAgentState(agentState *state.AgentState) RuntimeSnapshot {
	snap := RuntimeSnapshot{
		Name:      agentState.Name,
		Type:      agentState.Type,
		Family:    agentState.Family,
		Parent:    agentState.Parent,
		Status:    agentState.Status,
		Branch:    agentState.Branch,
		Worktree:  agentState.Worktree,
		SessionID: agentState.SessionID,
		TreePath:  agentState.TreePath,
		CreatedAt: agentState.CreatedAt,
		LastReport: LastReport{
			Type:    agentState.LastReportType,
			Message: agentState.LastReportMessage,
			At:      agentState.LastReportAt,
			State:   agentState.LastReportState,
			Detail:  agentState.LastReportDetail,
		},
	}

	switch agentState.Status {
	case "killed":
		snap.Liveness = liveness.Killed
	case "retired":
		snap.Liveness = liveness.Retired
	default:
		snap.Liveness = liveness.Unstarted
	}
	return snap
}

// currentHandle returns the live runtime handle (or nil if not started or stopped).
func (r *AgentRuntime) currentHandle() RuntimeHandle {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handle
}

// InTurn reports whether the live RuntimeHandle's backend Session is
// currently between sprawl-initiated turns (QUM-582/585). Returns false when
// the runtime is not started, has been stopped, or the handle does not
// implement the optional InTurn() bool method.
func (r *AgentRuntime) InTurn() bool {
	h := r.currentHandle()
	if h == nil {
		return false
	}
	probe, ok := h.(turnProbe)
	if !ok {
		return false
	}
	return probe.InTurn()
}

// LastActivityAt returns the timestamp of the most recently recorded
// activity-ring entry on the live RuntimeHandle. Zero time when the
// runtime is not started, has been stopped, or the handle does not
// implement the optional LastActivityAt() time.Time method. (QUM-665)
func (r *AgentRuntime) LastActivityAt() time.Time {
	h := r.currentHandle()
	if h == nil {
		return time.Time{}
	}
	probe, ok := h.(lastActivityProbe)
	if !ok {
		return time.Time{}
	}
	return probe.LastActivityAt()
}

// IsTerminallyFaulted reports whether the live RuntimeHandle's backend session
// has been terminally faulted (terminalErr set). Returns false when the runtime
// is not started, has been stopped, or the handle does not implement the
// optional IsTerminallyFaulted() bool method. (QUM-622)
func (r *AgentRuntime) IsTerminallyFaulted() bool {
	h := r.currentHandle()
	if h == nil {
		return false
	}
	probe, ok := h.(terminalFaultProbe)
	if !ok {
		return false
	}
	return probe.IsTerminallyFaulted()
}

// SubprocessAlive reports whether a live RuntimeHandle is currently
// attached. Distinct from the projected liveness — a fault that has
// detached but not yet been disk-stamped reads false here. (QUM-727)
func (r *AgentRuntime) SubprocessAlive() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handle != nil
}

// EventBusSubscriberCount returns the live subscriber count on the
// underlying UnifiedRuntime's EventBus, or 0 if no handle is attached
// or the handle does not expose a UnifiedRuntime. (QUM-727)
func (r *AgentRuntime) EventBusSubscriberCount() int {
	handle := r.currentHandle()
	if handle == nil {
		return 0
	}
	p, ok := handle.(unifiedRuntimeProvider)
	if !ok {
		return 0
	}
	rt := p.UnifiedRuntime()
	if rt == nil {
		return 0
	}
	bus := rt.EventBus()
	if bus == nil {
		return 0
	}
	return bus.SubscriberCount()
}

// Snapshot returns the current runtime snapshot.
func (r *AgentRuntime) Snapshot() RuntimeSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot
}

// Subscribe returns a per-runtime event stream and a cancellation function.
func (r *AgentRuntime) Subscribe(buffer int) (<-chan RuntimeEvent, func()) {
	if buffer <= 0 {
		buffer = 1
	}

	r.mu.Lock()
	id := r.nextSubscriberID
	r.nextSubscriberID++
	ch := make(chan RuntimeEvent, buffer)
	r.subscribers[id] = ch
	r.mu.Unlock()

	cancel := func() {
		r.mu.Lock()
		delete(r.subscribers, id)
		r.mu.Unlock()
	}
	return ch, cancel
}

// Start attaches a backend session using the runtime's internal starter.
// Production wiring does not call this yet; QUM-351 needs it only so child
// runtimes can be exercised in tests without tmux or a child sprawl process.
func (r *AgentRuntime) Start() error {
	r.mu.RLock()
	spec := RuntimeStartSpec{
		Name:       r.snapshot.Name,
		Worktree:   r.snapshot.Worktree,
		SprawlRoot: r.sprawlRoot,
		SessionID:  r.snapshot.SessionID,
		TreePath:   r.snapshot.TreePath,
	}
	r.mu.RUnlock()
	return r.startWithSpec(spec)
}

// startWithSpec is the shared body for Start and StartResume.
func (r *AgentRuntime) startWithSpec(spec RuntimeStartSpec) error {
	r.mu.RLock()
	starter := r.starter
	r.mu.RUnlock()

	if starter == nil {
		return fmt.Errorf("runtime starter not configured")
	}
	handle, err := starter.Start(spec)
	if err != nil {
		return err
	}

	// QUM-722: a fresh start is an expected entry into Running; clear the
	// expectingExit bit so a later unexpected handle exit classifies as Died.
	r.expectingExit.Store(false)

	r.mu.Lock()
	r.handle = handle
	r.snapshot.Liveness = liveness.Running
	r.snapshot.Capabilities = handle.Capabilities()
	if sessionID := handle.SessionID(); sessionID != "" {
		r.snapshot.SessionID = sessionID
	}
	r.mu.Unlock()
	r.emit(RuntimeEventStarted)
	if doneAware, ok := handle.(runtimeHandleDone); ok && doneAware.Done() != nil {
		r.watchHandleExit(handle, doneAware.Done())
	}
	return nil
}

// StartResume launches the agent's backend session with the Resume flag set,
// so the spawned claude subprocess is instructed to resume the prior
// conversation transcript identified by the snapshot SessionID. Mirrors Start
// in every other respect; emits RuntimeEventStarted (NOT RuntimeEventRecovered
// — there is no live handle to swap, this is a fresh start of the same
// session). QUM-372.
//
// Production wiring: called by Real.RecoverAgents during sprawl-enter startup
// for every persisted child agent whose status is in {suspended, active,
// running} and whose worktree still exists. An OnResumeFailure closure can be
// installed via the matching field on RuntimeStartSpec — currently this method
// reads it from the runtime starter's bound state, but tests inject through
// AgentRuntimeConfig / RuntimeStartSpec seams as documented in
// runtime_test.go.
//
// restartInjection, when non-empty, is forwarded via RuntimeStartSpec to
// replace AgentState.Prompt as the first post-resume turn (QUM-723).
func (r *AgentRuntime) StartResume(restartInjection string, onResumeFailure ...func()) error {
	var cb func()
	for _, c := range onResumeFailure {
		if c != nil {
			cb = c
			break
		}
	}
	r.mu.RLock()
	spec := RuntimeStartSpec{
		Name:             r.snapshot.Name,
		Worktree:         r.snapshot.Worktree,
		SprawlRoot:       r.sprawlRoot,
		SessionID:        r.snapshot.SessionID,
		TreePath:         r.snapshot.TreePath,
		Resume:           true,
		OnResumeFailure:  cb,
		RestartInjection: restartInjection,
	}
	r.mu.RUnlock()
	return r.startWithSpec(spec)
}

// AttachHandle attaches a pre-built RuntimeHandle to this AgentRuntime
// without invoking a RuntimeStarter. Used by Supervisor.RegisterRootRuntime
// to register weave's UnifiedRuntime under the same registry as child
// runtimes (QUM-399). Sets lifecycle to Started, captures Capabilities and
// SessionID, emits RuntimeEventStarted, and watches the handle's Done()
// channel for unexpected exits when supported.
func (r *AgentRuntime) AttachHandle(handle RuntimeHandle) {
	if handle == nil {
		return
	}
	// QUM-722: attaching a fresh handle = expected Running state; reset the
	// expectingExit bit so a later unexpected exit classifies as Died.
	r.expectingExit.Store(false)
	r.mu.Lock()
	r.handle = handle
	r.snapshot.Liveness = liveness.Running
	r.snapshot.Capabilities = handle.Capabilities()
	if sessionID := handle.SessionID(); sessionID != "" {
		r.snapshot.SessionID = sessionID
	}
	r.mu.Unlock()
	r.emit(RuntimeEventStarted)
	if doneAware, ok := handle.(runtimeHandleDone); ok && doneAware.Done() != nil {
		r.watchHandleExit(handle, doneAware.Done())
	}
}

// Interrupt forwards an interrupt to the tracked backend session when one is attached.
func (r *AgentRuntime) Interrupt(ctx context.Context) error {
	r.mu.RLock()
	handle := r.handle
	r.mu.RUnlock()

	if handle == nil {
		return fmt.Errorf("runtime session not started")
	}
	if !handle.Capabilities().SupportsInterrupt {
		return fmt.Errorf("runtime session does not support interrupt")
	}
	if err := handle.Interrupt(ctx); err != nil {
		return err
	}

	r.mu.Lock()
	r.snapshot.InterruptCount++
	r.mu.Unlock()
	r.emit(RuntimeEventInterrupted)
	return nil
}

// NotifyWake notifies an idle runtime that persisted work is ready to be
// observed. Distinct from the lifecycle verb AgentRuntime.Wake(ctx) (QUM-724)
// — this is the lower-level "poke the handle" path used by delegate dispatch.
func (r *AgentRuntime) NotifyWake() error {
	r.mu.RLock()
	handle := r.handle
	r.mu.RUnlock()

	if handle == nil {
		return fmt.Errorf("runtime session not started")
	}
	if err := handle.Wake(); err != nil {
		return err
	}

	r.mu.Lock()
	r.snapshot.WakeCount++
	r.mu.Unlock()
	return nil
}

// WakeForDelivery notifies a runtime cooperatively that newly-persisted work
// is available. Updates the WakeCount snapshot counter. See QUM-549/QUM-550.
func (r *AgentRuntime) WakeForDelivery() error {
	r.mu.RLock()
	handle := r.handle
	r.mu.RUnlock()

	if handle == nil {
		return fmt.Errorf("runtime session not started")
	}
	if err := handle.WakeForDelivery(); err != nil {
		return err
	}

	r.mu.Lock()
	r.snapshot.WakeCount++
	r.mu.Unlock()
	return nil
}

// Stop stops the tracked runtime handle, if any.
func (r *AgentRuntime) Stop(ctx context.Context) error {
	// QUM-722: signal an expected exit BEFORE invoking the handle stop fn so
	// watchHandleExit observes expectingExit=true via the atomic happens-before
	// the done close (avoids classifying a polite stop as Died).
	r.expectingExit.Store(true)
	return r.stopWithFunc(ctx, func(h RuntimeHandle) error { return h.Stop(ctx) })
}

// StopAbandon stops the tracked runtime handle using the teardown-only
// path that skips the polite Session.Interrupt issued by Stop. Used by
// Real.Retire(abandon=true). See QUM-600.
func (r *AgentRuntime) StopAbandon(ctx context.Context) error {
	// QUM-722: same expected-exit signal as Stop, ahead of the handle call.
	r.expectingExit.Store(true)
	return r.stopWithFunc(ctx, func(h RuntimeHandle) error { return h.StopAbandon(ctx) })
}

// Pause politely stops the runtime at the next turn boundary, preserving the
// transcript so the agent can be woken later. QUM-722.
//
// Behavior:
//   - When the runtime is not in-turn, Pause skips the EventBus wait and goes
//     straight to a polite Stop.
//   - Otherwise it subscribes to the underlying UnifiedRuntime's EventBus and
//     waits for one of: EventTurnCompleted / EventInterrupted / EventTurnFailed
//     / EventBackendFaulted. The wait MUST NOT issue Session.Interrupt —
//     the in-flight turn drains naturally.
//   - If any of the above events fires before the timeout: clean = true.
//     Pause then calls r.Stop, writes disk Status=paused, and emits
//     RuntimeEventPaused.
//   - If the timeout fires first: clean = false. Pause calls r.StopAbandon
//     and writes disk Status=killed (escalation). RuntimeEventStopped is
//     emitted by the underlying StopAbandon flow.
//
// Returns (clean, nil) on success; (false, err) on a setup error. Currently
// the err return is reserved — never non-nil — so callers can treat a
// returned err as fatal.
func (r *AgentRuntime) Pause(ctx context.Context, timeout time.Duration) (clean bool, err error) {
	r.mu.RLock()
	name := r.snapshot.Name
	sprawlRoot := r.sprawlRoot
	r.mu.RUnlock()

	// Decide whether we need to wait. InTurn() == false short-circuits the
	// subscribe path — proving the "no-subscribe" test invariant. We probe
	// the InTurn first WITHOUT touching the EventBus.
	inTurn := r.InTurn()

	cleanExit := true
	if inTurn {
		// Resolve a UnifiedRuntime for the EventBus. When unavailable, we
		// have no signal for "turn boundary observed", so the wait collapses
		// to a pure timeout — which is what the escalation path tests assert.
		var ch <-chan runtimepkg.RuntimeEvent
		var unsub func()
		if urt := r.UnifiedRuntime(); urt != nil {
			ch, unsub = urt.EventBus().SubscribeNamed("pause-wait", 8)
		}
		waitTimer := time.NewTimer(timeout)
	waitLoop:
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					// Either ch is nil (no bus) — select will never fire this
					// case on a nil channel; or the bus closed. Treat closure
					// as a natural termination signal.
					if ch != nil {
						break waitLoop
					}
				}
				switch ev.Type {
				case runtimepkg.EventTurnCompleted,
					runtimepkg.EventInterrupted,
					runtimepkg.EventTurnFailed,
					runtimepkg.EventBackendFaulted:
					break waitLoop
				}
			case <-waitTimer.C:
				cleanExit = false
				break waitLoop
			case <-ctx.Done():
				cleanExit = false
				break waitLoop
			}
		}
		waitTimer.Stop()
		if unsub != nil {
			unsub()
		}
	}

	if cleanExit {
		if err := r.Stop(ctx); err != nil {
			return false, err
		}
		// Persist disk Status=paused so the projection rests at Paused.
		if name != "" {
			if cur, lErr := state.LoadAgent(sprawlRoot, name); lErr == nil && cur != nil {
				cur.Status = state.StatusPaused
				if sErr := state.SaveAgent(sprawlRoot, cur); sErr != nil {
					slog.Warn("supervisor: Pause durable status save", "agent", name, "err", sErr)
				}
				// Mirror in-memory snapshot.
				r.mu.Lock()
				r.snapshot.Status = state.StatusPaused
				r.snapshot.Liveness = liveness.Paused
				r.mu.Unlock()
			}
		}
		r.emit(RuntimeEventPaused)
		return true, nil
	}

	// Escalation: timeout fired. Hard-abandon and stamp killed on disk.
	if err := r.StopAbandon(ctx); err != nil {
		return false, err
	}
	if name != "" {
		if cur, lErr := state.LoadAgent(sprawlRoot, name); lErr == nil && cur != nil {
			cur.Status = state.StatusKilled
			if sErr := state.SaveAgent(sprawlRoot, cur); sErr != nil {
				slog.Warn("supervisor: Pause escalation durable status save", "agent", name, "err", sErr)
			}
			r.mu.Lock()
			r.snapshot.Status = state.StatusKilled
			r.snapshot.Liveness = liveness.Killed
			r.mu.Unlock()
		}
	}
	return false, nil
}

// StopAfterTurn defers the runtime teardown until the agent's current turn
// actually yields, instead of tearing down immediately. It is the reusable
// "defer self-teardown to turn-end" primitive (QUM-866): a mid-turn caller
// (e.g. report_status(complete/failure)) can finish emitting a follow-on
// send_message or trailing text before the subprocess + EventBus subscribers
// are released. drainInflight (invoked by Stop) cancels in-flight async MCP
// handlers, so stopping mid-turn silently drops anything the agent does after
// the triggering tool call — deferring to the genuine EndOfTurn closes that
// race.
//
// It is intentionally generic (no report_status coupling, no disk-Status
// writes, no RuntimeEvent emission of its own — teardown semantics all come
// from Stop/stopWithFunc) so a later issue can reuse it for handoff.
//
// Behavior:
//   - No UnifiedRuntime (test/non-unified handle) → Stop immediately. This
//     preserves the existing teardown unit tests whose fake session exposes no
//     EventBus / InTurn.
//   - Otherwise SUBSCRIBE to the EventBus FIRST, THEN check InTurn(). The
//     subscribe-before-check ordering closes the race where the turn ends
//     between the check and the subscribe (Pause has the opposite order — that
//     ordering is a latent bug and is deliberately NOT copied here).
//   - Not in-turn → Stop immediately (today's behavior, no regression).
//   - In-turn → wait for one of {EventTurnCompleted, EventInterrupted,
//     EventTurnFailed, EventBackendFaulted} on the bus (all derived from the
//     genuine EndOfTurn frame in unified.go routeFrame), or ctx cancellation,
//     or the timeout runaway guard — whichever first — then Stop. The timeout
//     guarantees RSS stays bounded even if the model keeps emitting past its
//     own terminal report and the turn never ends.
//
// ctx bounds only the turn-wait (external cancellation, e.g. process
// shutdown). timeout is the runaway guard on the turn-wait — the sole bound
// when ctx has no deadline (e.g. a unit test passing context.Background()).
// The final Stop always gets its OWN fresh runtimeStopTimeout budget: reusing
// a turn-wait ctx that has already expired (runaway path, or a turn ending
// near the deadline) would make UnifiedRuntime.Stop bail on ctx.Done() before
// draining, propagating an error that skips stopWithFunc's snapshot
// bookkeeping — i.e. the runaway guard, whose whole job is a reliable
// teardown, would degrade. (QUM-866)
func (r *AgentRuntime) StopAfterTurn(ctx context.Context, timeout time.Duration) error {
	stop := func() error {
		stopCtx, cancel := context.WithTimeout(context.Background(), runtimeStopTimeout)
		defer cancel()
		return r.Stop(stopCtx)
	}

	urt := r.UnifiedRuntime()
	if urt == nil {
		return stop()
	}

	// Subscribe BEFORE probing InTurn so a turn-end that fires between the
	// probe and the subscribe cannot be missed.
	ch, unsub := urt.EventBus().SubscribeNamed("stop-after-turn", 8)
	defer unsub()

	if !r.InTurn() {
		return stop()
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				// Bus closed → treat as terminal; tear down.
				return stop()
			}
			switch ev.Type {
			case runtimepkg.EventTurnCompleted,
				runtimepkg.EventInterrupted,
				runtimepkg.EventTurnFailed,
				runtimepkg.EventBackendFaulted:
				return stop()
			}
		case <-timer.C:
			return stop()
		case <-ctx.Done():
			return stop()
		}
	}
}

// Wake brings an offline backend session back online. Expanded from the
// QUM-601 in-place recover verb (QUM-724) — the accept-set is now the union
// of liveness states that present as "offline-but-recoverable":
// {Faulted, ResumeFailed, Paused, Killed, Died}.
//
// Behavior:
//   - If another Wake is already in progress, returns a "wake already in
//     progress" error immediately (TryLock fail-fast).
//   - If the live handle's session reports !IsTerminallyFaulted() AND the
//     snapshot projects to a healthy liveness, returns ErrWakeNotNeeded.
//   - Otherwise: builds a RuntimeStartSpec from the current snapshot with
//     Resume=true + SessionID set, tears down any abandoned handle, and
//     invokes starter.Start. If the initial --resume attempt's OnResumeFailure
//     fires (cookie rejected) OR the post-start health probe fails, falls
//     back ONCE with Resume=false and an empty SessionID (fresh session).
//   - On final success: clears disk Status → "active", transitions liveness →
//     Running, emits RuntimeEventWoken, and returns
//     WakeResult{Mode:"resumed"|"fresh", SessionRestored:Mode=="resumed"}.
//
// Handles that do not expose IsTerminallyFaulted() bool are treated as
// faulted (defensive default; production handles all expose it via the
// embedded backend.Session).
func (r *AgentRuntime) Wake(ctx context.Context, restartInjection string) (*WakeResult, error) {
	// QUM-726: restartInjection is forwarded to RuntimeStartSpec on both
	// the resume-attempt and fresh-fallback specs so the recipient's first
	// post-wake turn sees the wake-flavored prompt.
	if !r.wakeMu.TryLock() {
		return nil, fmt.Errorf("supervisor: wake already in progress")
	}
	defer r.wakeMu.Unlock()

	r.mu.RLock()
	starter := r.starter
	handle := r.handle
	lifecycle := r.snapshot.Liveness
	diskStatus := r.snapshot.Status
	agentName := r.snapshot.Name
	baseSpec := RuntimeStartSpec{
		Name:       r.snapshot.Name,
		Worktree:   r.snapshot.Worktree,
		SprawlRoot: r.sprawlRoot,
		SessionID:  r.snapshot.SessionID,
		TreePath:   r.snapshot.TreePath,
	}
	r.mu.RUnlock()

	// Healthy-live-handle short-circuit. A non-nil handle that reports
	// !IsTerminallyFaulted() is a no-op wake — only the probe-reports-healthy
	// case returns ErrWakeNotNeeded; the no-probe path treats as faulted so
	// it still wakes.
	faulted := false
	if handle != nil {
		if probe, ok := handle.(terminalFaultProbe); ok {
			faulted = probe.IsTerminallyFaulted()
		} else {
			faulted = true
		}
	}
	if handle != nil && !faulted {
		return nil, ErrWakeNotNeeded
	}

	// QUM-790: wake accepts any liveness EXCEPT {Retired, Retiring}. Retired
	// agents have had their worktree torn down and branch removed; retiring
	// agents are mid-teardown. Everything else — including StatusComplete
	// (QUM-787), Faulted, ResumeFailed, Paused, Killed, Died, and even
	// Unstarted snapshots — is revivable per the QUM-786 lifecycle arc.
	st := liveness.From(liveness.Snapshot{
		Lifecycle:   livenessToLifecycleString(lifecycle),
		TerminalErr: faulted,
		DiskStatus:  diskStatus,
	})
	switch st.Liveness {
	case liveness.Retired, liveness.Retiring:
		return nil, fmt.Errorf("supervisor: agent %q is %s, cannot wake (retired/retiring agents are terminal)", agentName, st.Liveness)
	}
	if starter == nil {
		return nil, fmt.Errorf("supervisor: agent %q has no runtime starter, cannot wake", agentName)
	}

	// Detach the watcher BEFORE StopAbandon so the watchHandleExit
	// goroutine's `if r.handle == handle` guard sees a stale match and
	// no-ops when the abandoned handle's Done() closes. This suppresses
	// the spurious RuntimeEventStopped that would otherwise race against
	// the post-restart RuntimeEventWoken emission. Skipped when there
	// is no live handle (post-fault Stopped/Died/etc lifecycle).
	if handle != nil {
		r.mu.Lock()
		r.handle = nil
		r.mu.Unlock()

		if err := handle.StopAbandon(ctx); err != nil {
			slog.Warn(
				"supervisor: Wake StopAbandon of abandoned handle returned error; continuing with restart",
				slog.String("agent", agentName),
				slog.Any("err", err),
			)
		}
	}

	// First attempt: --resume. Wire OnResumeFailure into a local atomic
	// flag so a synchronously-fired callback flips the fallback bit without
	// writing resume_failed to disk during the in-band wake flow (we don't
	// want to persist a transient signal; on success the disk Status flips
	// straight to "active").
	var resumeRejected atomic.Bool
	resumeSpec := baseSpec
	resumeSpec.Resume = true
	resumeSpec.RestartInjection = restartInjection
	resumeSpec.OnResumeFailure = func() {
		resumeRejected.Store(true)
	}

	mode := "resumed"
	newHandle, startErr := starter.Start(resumeSpec)
	needFallback := false
	if startErr != nil {
		// Initial Start error is treated as needing a fresh fallback only if
		// it's the OnResumeFailure signal in disguise; but starter.Start
		// errors are conservatively fatal — fall back once with fresh.
		needFallback = true
	} else if probeErr := probeNewHandleHealth(newHandle, wakeHealthProbeTimeout); probeErr != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), wakeStopAbandonTimeout)
		_ = newHandle.StopAbandon(stopCtx)
		cancel()
		newHandle = nil
		needFallback = true
		slog.Info("supervisor: Wake resume-path health probe failed; falling back to fresh",
			slog.String("agent", agentName), slog.Any("err", probeErr))
	} else if resumeRejected.Load() {
		// OnResumeFailure fired synchronously before/during Start return.
		stopCtx, cancel := context.WithTimeout(context.Background(), wakeStopAbandonTimeout)
		_ = newHandle.StopAbandon(stopCtx)
		cancel()
		newHandle = nil
		needFallback = true
		slog.Info("supervisor: Wake resume cookie rejected; falling back to fresh",
			slog.String("agent", agentName))
	}

	// OnResumeFailure may also fire shortly AFTER a seemingly-healthy probe,
	// but the typical signal lands inside the probe window (stderr "No
	// conversation found" marker hits before any frame). The above checks
	// cover the in-band cases the tests pin.

	if needFallback {
		mode = "fresh"
		freshSpec := baseSpec
		freshSpec.Resume = false
		// Mint a fresh session_id host-side so the backend session config
		// (and thus newHandle.SessionID()) carries the new id forward — the
		// snapshot + on-disk SessionID update below depends on this. Letting
		// the spec carry SessionID="" and trusting claude to self-generate
		// loses the id at the host (session.config.SessionID is set at
		// construction time and never re-populated from the init frame), so
		// a later sprawl-restart's RecoverAgents would try to --resume the
		// now-defunct old session_id. QUM-744.
		freshSID, sidErr := state.GenerateUUID()
		if sidErr != nil {
			r.stampDoublyFailedWake(agentName)
			return nil, fmt.Errorf("supervisor: wake fresh SessionID mint for %q: %w", agentName, sidErr)
		}
		freshSpec.SessionID = freshSID
		freshSpec.RestartInjection = restartInjection
		freshSpec.OnResumeFailure = nil
		var freshErr error
		newHandle, freshErr = starter.Start(freshSpec)
		if freshErr != nil {
			r.stampDoublyFailedWake(agentName)
			return nil, fmt.Errorf("supervisor: wake fresh Start for %q: %w", agentName, freshErr)
		}
		if probeErr := probeNewHandleHealth(newHandle, wakeHealthProbeTimeout); probeErr != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), wakeStopAbandonTimeout)
			_ = newHandle.StopAbandon(stopCtx)
			cancel()
			r.stampDoublyFailedWake(agentName)
			return nil, fmt.Errorf("supervisor: wake fresh health probe for %q: %w", agentName, probeErr)
		}
	}

	r.mu.Lock()
	r.handle = newHandle
	r.snapshot.Liveness = liveness.Running
	r.snapshot.Capabilities = newHandle.Capabilities()
	if sid := newHandle.SessionID(); sid != "" {
		r.snapshot.SessionID = sid
	}
	// QUM-625/QUM-724: a successful wake clears the durable resting Status
	// (e.g. "faulted"/"paused"/"killed"/"died") and projects back to Running.
	r.snapshot.Status = state.StatusActive
	wokenName := r.snapshot.Name
	wokenSID := r.snapshot.SessionID
	r.mu.Unlock()

	// QUM-722: re-arm expectingExit so a future unexpected exit classifies
	// as Died rather than inheriting a stale Pause/Stop intent.
	r.expectingExit.Store(false)

	// Best-effort durable persist OUTSIDE r.mu so disk Status tracks the
	// woken liveness (Running → "active") AND — critical for the fresh
	// fallback path — disk SessionID tracks the freshly-minted session so a
	// later sprawl-restart's RecoverAgents resumes the right transcript
	// rather than the now-defunct one. (QUM-744)
	if wokenName != "" {
		if cur, lErr := state.LoadAgent(r.sprawlRoot, wokenName); lErr != nil {
			slog.Warn("supervisor: Wake durable status load", "agent", wokenName, "err", lErr)
		} else {
			cur.Status = state.StatusActive
			if wokenSID != "" && cur.SessionID != wokenSID {
				cur.SessionID = wokenSID
			}
			if sErr := state.SaveAgent(r.sprawlRoot, cur); sErr != nil {
				slog.Warn("supervisor: Wake durable status save", "agent", wokenName, "err", sErr)
			}
		}
	}

	r.emit(RuntimeEventWoken)

	if doneAware, ok := newHandle.(runtimeHandleDone); ok && doneAware.Done() != nil {
		r.watchHandleExit(newHandle, doneAware.Done())
	}
	return &WakeResult{Mode: mode, SessionRestored: mode == "resumed"}, nil
}

// stampDoublyFailedWake records the terminal "wake fell back to fresh and
// fresh also failed" outcome on both the in-memory snapshot and disk. Both
// the resume-attempt and fresh-attempt have already been torn down by the
// time this is called, so there is no live handle whose watchHandleExit
// could race the disk Status — we stamp StatusResumeFailed directly here
// (QUM-744) rather than relying on watcher normalization.
func (r *AgentRuntime) stampDoublyFailedWake(agentName string) {
	r.mu.Lock()
	r.snapshot.Liveness = liveness.Stopped
	r.snapshot.Status = state.StatusResumeFailed
	r.mu.Unlock()
	if agentName != "" {
		if cur, lErr := state.LoadAgent(r.sprawlRoot, agentName); lErr != nil {
			slog.Warn("supervisor: Wake doubly-failed durable status load",
				slog.String("agent", agentName), slog.Any("err", lErr))
		} else if cur != nil {
			cur.Status = state.StatusResumeFailed
			if sErr := state.SaveAgent(r.sprawlRoot, cur); sErr != nil {
				slog.Warn("supervisor: Wake doubly-failed durable status save",
					slog.String("agent", agentName), slog.Any("err", sErr))
			}
		}
	}
	r.emit(RuntimeEventStopped)
}

// wakeHealthProbeTimeout bounds how long AgentRuntime.Wake waits for the
// freshly-started handle to demonstrate liveness (a non-init protocol frame
// on its UnifiedRuntime EventBus) before declaring the attempt a failure.
// See QUM-606 R4. QUM-724 (renamed from recoverHealthProbeTimeout).
var wakeHealthProbeTimeout = 5 * time.Second

// wakeStopAbandonTimeout bounds the StopAbandon call used to tear down
// a handle that failed the post-Start health probe.
// QUM-724 (renamed from recoverStopAbandonTimeout).
var wakeStopAbandonTimeout = 5 * time.Second

// probeNewHandleHealth waits up to timeout for the newly-started handle to
// either (a) emit a non-init protocol frame on its UnifiedRuntime EventBus,
// proving the backend subprocess is alive and serving, or (b) flip
// IsTerminallyFaulted() to true. Returns nil on (a), an error on (b) or on
// timeout. Handles that do not expose a UnifiedRuntime are treated as
// healthy (skipped probe). QUM-606 R4.
func probeNewHandleHealth(handle RuntimeHandle, timeout time.Duration) error {
	provider, ok := handle.(unifiedRuntimeProvider)
	if !ok {
		// No bus to probe; fall back to the cheaper sticky-fault check
		// (handles in tests that don't expose UnifiedRuntime still
		// implement IsTerminallyFaulted via the embedded session).
		return waitForTerminalFaultOrTimeout(handle, timeout)
	}
	rt := provider.UnifiedRuntime()
	if rt == nil {
		return waitForTerminalFaultOrTimeout(handle, timeout)
	}

	probe, hasProbe := handle.(terminalFaultProbe)
	if hasProbe && probe.IsTerminallyFaulted() {
		return fmt.Errorf("session faulted before health probe began")
	}

	ch, unsub := rt.EventBus().SubscribeNamed("wake-health-probe", 8)
	defer unsub()

	// Re-check fault state AFTER subscribing to close the race where the
	// fault fires between starter.Start return and our subscription.
	if hasProbe && probe.IsTerminallyFaulted() {
		return fmt.Errorf("session faulted before health probe completed")
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return fmt.Errorf("event bus closed before health probe completed")
			}
			if ev.Type == runtimepkg.EventBackendFaulted {
				return fmt.Errorf("session faulted during health probe: %w", ev.Error)
			}
			if ev.Type == runtimepkg.EventStopped {
				return fmt.Errorf("session stopped during health probe")
			}
			if ev.Type == runtimepkg.EventProtocolMessage && ev.Message != nil {
				m := ev.Message
				if m.Type != "system" || m.Subtype != "init" {
					return nil
				}
			}
		case <-tick.C:
			if hasProbe && probe.IsTerminallyFaulted() {
				return fmt.Errorf("session faulted during health probe")
			}
		case <-deadline.C:
			if hasProbe && probe.IsTerminallyFaulted() {
				return fmt.Errorf("session faulted during health probe")
			}
			return fmt.Errorf("no frames received within %s", timeout)
		}
	}
}

func waitForTerminalFaultOrTimeout(handle RuntimeHandle, timeout time.Duration) error {
	probe, ok := handle.(terminalFaultProbe)
	if !ok {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		if probe.IsTerminallyFaulted() {
			return fmt.Errorf("session faulted during health probe")
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// InduceTerminalFault forces the underlying backend session into the
// terminally-faulted state. Used by the QUM-606 live-recover e2e harness
// via a build-tag-gated MCP tool (`_test_induce_wedge`) to drive a
// deterministic SubscriberWedge / HangTimeout fault. Returns an error
// when no live handle is attached or when the handle does not expose
// the test seam. Production callers MUST NOT invoke this.
func (r *AgentRuntime) InduceTerminalFault(err error) error {
	r.mu.RLock()
	handle := r.handle
	r.mu.RUnlock()
	if handle == nil {
		return fmt.Errorf("supervisor: agent has no live handle")
	}
	injector, ok := handle.(terminalFaultInjectorProbe)
	if !ok {
		return fmt.Errorf("supervisor: handle does not expose InduceTerminalFault test seam")
	}
	injector.InduceTerminalFault(err)
	return nil
}

// stopWithFunc is the shared body for Stop / StopAbandon. The caller picks
// which handle method to invoke; bookkeeping (StopWaitTimedOut capture,
// lifecycle transition, RuntimeEventStopped emission) is identical for
// both paths.
func (r *AgentRuntime) stopWithFunc(_ context.Context, stop func(RuntimeHandle) error) error {
	r.mu.RLock()
	handle := r.handle
	r.mu.RUnlock()

	if handle == nil {
		return nil
	}
	stopErr := stop(handle)
	// Capture the bounded-Wait timeout flag (QUM-542/QUM-546) even when Stop
	// returns an error, so the retire.runtime-stop-done / kill.runtime-stop-done
	// checkpoints reflect the actual handle state.
	if probe, ok := handle.(stopWaitTimeoutProbe); ok {
		r.stopWaitTimedOut.Store(probe.StopWaitTimedOut())
	}
	if stopErr != nil {
		return stopErr
	}

	// QUM-786: prefer disk LastReportState over the in-memory snapshot.
	// Real.ReportStatus deliberately SKIPS syncRuntimeFromState on
	// terminal reports (complete/failure) — the in-memory snapshot's
	// LastReport.State is stale at this point but agentops.Report wrote
	// the canonical value to disk synchronously.
	diskLastReportStateForStop := ""
	if snapName := func() string {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.snapshot.Name
	}(); snapName != "" {
		if cur, lErr := state.LoadAgent(r.sprawlRoot, snapName); lErr == nil && cur != nil {
			diskLastReportStateForStop = cur.LastReportState
		}
	}

	emitStopped := false
	var name string
	var newStatus string
	r.mu.Lock()
	r.handle = nil
	if r.snapshot.Liveness == liveness.Running {
		r.snapshot.Liveness = liveness.Stopped
		emitStopped = true
	}
	// QUM-787 / QUM-786: a deliberate Stop is durable + distinguishable
	// from a fault. If the agent reported state=complete before teardown,
	// stamp StatusComplete (revivable per QUM-786 arc); otherwise the
	// clean exit without a completion report is treated as the unexpected
	// case, so stamp StatusFaulted. StatusStopped is no longer a write
	// target. Prefer disk LastReportState (see above) over the stale
	// in-memory snapshot.
	name = r.snapshot.Name
	lastReportStateForStop := diskLastReportStateForStop
	if lastReportStateForStop == "" {
		lastReportStateForStop = r.snapshot.LastReport.State
	}
	if lastReportStateForStop == "complete" {
		newStatus = state.StatusComplete
	} else {
		newStatus = state.StatusFaulted
	}
	r.snapshot.Status = newStatus
	r.mu.Unlock()

	// Best-effort durable persist OUTSIDE r.mu. A terminal-ish disk Status
	// (killed/retired/retiring) or a completed/faulted resting status must not
	// be relabeled — only an otherwise-live agent's deliberate Stop
	// becomes a durable complete/faulted resting state.
	if name != "" {
		cur, err := state.LoadAgent(r.sprawlRoot, name)
		if err != nil {
			slog.Warn("supervisor: stop durable status load", "agent", name, "err", err)
		} else {
			switch cur.Status {
			case state.StatusKilled, state.StatusRetired, state.StatusRetiring,
				state.StatusFaulted, state.StatusComplete, state.StatusDied:
				// QUM-787: leave terminal-ish / already-resolved resting
				// states as-is. Complete + Died are durable resting
				// states that mustn't be re-derived by a late Stop.
			default:
				// QUM-787: re-derive from disk LastReportState in case it
				// landed between the snapshot read above and this load.
				if cur.LastReportState == "complete" {
					cur.Status = state.StatusComplete
				} else {
					cur.Status = state.StatusFaulted
				}
				if sErr := state.SaveAgent(r.sprawlRoot, cur); sErr != nil {
					slog.Warn("supervisor: stop durable status save", "agent", name, "err", sErr)
				}
			}
		}
	}

	if emitStopped {
		r.emit(RuntimeEventStopped)
	}
	return nil
}

// RecordQueuedTask updates the passive in-memory queue depth after task persistence succeeds.
func (r *AgentRuntime) RecordQueuedTask() {
	r.mu.Lock()
	r.snapshot.QueueDepth++
	r.mu.Unlock()
	r.emit(RuntimeEventTaskQueued)
}

// SyncAgentState mirrors persisted agent state into the runtime snapshot.
func (r *AgentRuntime) SyncAgentState(agentState *state.AgentState) {
	if agentState == nil {
		return
	}

	r.mu.Lock()
	updated := snapshotFromAgentState(agentState)
	updated.QueueDepth = r.snapshot.QueueDepth
	updated.WakeCount = r.snapshot.WakeCount
	updated.InterruptCount = r.snapshot.InterruptCount
	updated.Capabilities = r.snapshot.Capabilities

	switch {
	case updated.Liveness == liveness.Killed:
	case updated.Liveness == liveness.Retired:
	// QUM-625 (slice M4) R7: when disk Status is a durable resting/fault state
	// AND there is no live handle, derive the resting liveness from it
	// (Unstarted) so liveness.From can decode DiskStatus. Gated on r.handle ==
	// nil so a live, just-recovered Running runtime (whose disk Status may still
	// be catching up) is never demoted — a live handle is the source of truth.
	case r.handle == nil &&
		(updated.Status == state.StatusFaulted ||
			updated.Status == state.StatusStopped ||
			updated.Status == state.StatusComplete ||
			updated.Status == state.StatusSuspended ||
			updated.Status == state.StatusResumeFailed):
		// QUM-787: include StatusComplete in the torn-down-but-revivable
		// projection alongside the legacy stopped sentinel.
		updated.Liveness = liveness.Unstarted
	case r.snapshot.Liveness == liveness.Running:
		updated.Liveness = liveness.Running
	case r.snapshot.Liveness == liveness.Stopped:
		updated.Liveness = liveness.Stopped
	default:
		updated.Liveness = liveness.Unstarted
	}

	r.snapshot = updated
	r.mu.Unlock()
	r.emit(RuntimeEventStateSynced)
}

func (r *AgentRuntime) watchHandleExit(handle RuntimeHandle, done <-chan struct{}) {
	go func() {
		<-done

		// QUM-625 (slice M4) AC a/b: probe whether the closing handle was
		// terminally faulted BEFORE taking the lock (no disk/probe I/O under
		// r.mu). A torn-down fault must survive teardown as a durable resting
		// Status so liveness.From projects Faulted (not Stopped) after restart.
		wasFaulted := false
		if p, ok := handle.(terminalFaultProbe); ok {
			wasFaulted = p.IsTerminallyFaulted()
		}
		// QUM-722: read expectingExit AFTER receiving <-done. The Store in
		// Stop/StopAbandon happens-before the close of done (channel close
		// synchronizes), so a paired Stop+close observes expectingExit=true.
		// An unexpected exit observes false → classify as Died.
		expectedExit := r.expectingExit.Load()

		// QUM-786: prefer disk LastReportState over the in-memory snapshot.
		// Real.ReportStatus deliberately SKIPS syncRuntimeFromState on
		// terminal reports (complete/failure) to avoid racing the async
		// Stop that fires immediately after — so the in-memory snapshot's
		// LastReport.State is stale at this point. agentops.Report wrote
		// the canonical value to disk synchronously, so disk is the source
		// of truth for the expected-exit classifier.
		diskLastReportState := ""
		if snapName := func() string {
			r.mu.RLock()
			defer r.mu.RUnlock()
			return r.snapshot.Name
		}(); snapName != "" {
			if cur, lErr := state.LoadAgent(r.sprawlRoot, snapName); lErr == nil && cur != nil {
				diskLastReportState = cur.LastReportState
			}
		}

		emitStopped := false
		matched := false
		var name string
		var durableStatus string
		var newLiveness liveness.AgentLiveness
		r.mu.Lock()
		if r.handle == handle {
			matched = true
			name = r.snapshot.Name
			r.handle = nil
			// QUM-722 / QUM-787 / QUM-786: four-way classifier. Faulted
			// beats Died beats the expected-exit branch. On an expected
			// exit we split on LastReportState: complete → StatusComplete
			// (revivable), otherwise → StatusFaulted (clean subprocess
			// exit without a completion report is treated as unexpected).
			// StatusStopped is no longer a write target. The
			// LastReportState read prefers disk (see above) over the
			// in-memory snapshot, which can be stale by design.
			lastReportState := diskLastReportState
			if lastReportState == "" {
				lastReportState = r.snapshot.LastReport.State
			}
			switch {
			case wasFaulted:
				newLiveness = liveness.Faulted
				durableStatus = state.StatusFaulted
			case expectedExit:
				if lastReportState == "complete" {
					newLiveness = liveness.Stopped
					durableStatus = state.StatusComplete
				} else {
					newLiveness = liveness.Faulted
					durableStatus = state.StatusFaulted
				}
			default:
				newLiveness = liveness.Died
				durableStatus = state.StatusDied
			}
			if r.snapshot.Liveness == liveness.Running {
				r.snapshot.Liveness = newLiveness
				emitStopped = true
			}
			r.snapshot.Status = durableStatus
		}
		r.mu.Unlock()

		// Best-effort durable persist OUTSIDE r.mu. Gated on `matched` so a
		// stale/already-swapped handle close (e.g. during Recover's
		// StopAbandon of the abandoned handle) does NOT clobber disk Status.
		if matched && name != "" {
			cur, err := state.LoadAgent(r.sprawlRoot, name)
			if err != nil {
				slog.Warn("supervisor: watchHandleExit durable status load", "agent", name, "err", err)
			} else {
				// Defensive (mirrors stopWithFunc): never overwrite a terminal-ish
				// disk Status with stopped/faulted. The `matched` guard already
				// prevents clobbering during Recover, and kill/retire write their
				// terminal Status after runtime.Stop returns, but guard here too so
				// a late watcher goroutine can't race a terminal write.
				switch cur.Status {
				case state.StatusKilled, state.StatusRetired, state.StatusRetiring, state.StatusPaused:
					// QUM-722: also preserve StatusPaused — a clean Pause flow
					// stamped paused on disk before tearing the handle down;
					// the watcher must not clobber it with stopped/died.
					// leave terminal / paused disk states as-is
				default:
					cur.Status = durableStatus
					if sErr := state.SaveAgent(r.sprawlRoot, cur); sErr != nil {
						slog.Warn("supervisor: watchHandleExit durable status save", "agent", name, "err", sErr)
					}
				}
			}
		}

		if emitStopped {
			r.emit(RuntimeEventStopped)
		}
	}()
}

func (r *AgentRuntime) emit(kind RuntimeEventKind) {
	r.mu.RLock()
	event := RuntimeEvent{
		Kind:     kind,
		Snapshot: r.snapshot,
	}
	subs := make([]chan RuntimeEvent, 0, len(r.subscribers))
	for _, ch := range r.subscribers {
		subs = append(subs, ch)
	}
	r.mu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- event:
		default:
		}
	}
}

// RuntimeRegistry stores same-process child runtime containers keyed by agent name.
type RuntimeRegistry struct {
	mu       sync.RWMutex
	runtimes map[string]*AgentRuntime
}

// NewRuntimeRegistry constructs an empty runtime registry.
func NewRuntimeRegistry() *RuntimeRegistry {
	return &RuntimeRegistry{
		runtimes: make(map[string]*AgentRuntime),
	}
}

// Ensure returns an existing runtime for the agent name or creates one.
func (r *RuntimeRegistry) Ensure(cfg AgentRuntimeConfig) *AgentRuntime {
	if cfg.Agent == nil || cfg.Agent.Name == "" {
		return NewAgentRuntime(cfg)
	}

	r.mu.RLock()
	existing := r.runtimes[cfg.Agent.Name]
	r.mu.RUnlock()
	if existing != nil {
		return existing
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing = r.runtimes[cfg.Agent.Name]; existing != nil {
		return existing
	}
	runtime := NewAgentRuntime(cfg)
	r.runtimes[cfg.Agent.Name] = runtime
	return runtime
}

// Get looks up a runtime by agent name.
func (r *RuntimeRegistry) Get(name string) (*AgentRuntime, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runtime, ok := r.runtimes[name]
	return runtime, ok
}

// Remove deletes a single runtime by agent name.
func (r *RuntimeRegistry) Remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runtimes[name]; !ok {
		return false
	}
	delete(r.runtimes, name)
	return true
}

// RemoveTree deletes the named runtime and any currently-tracked descendants.
func (r *RuntimeRegistry) RemoveTree(rootName string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.runtimes[rootName]; !ok {
		return nil
	}

	toRemove := map[string]bool{rootName: true}
	changed := true
	for changed {
		changed = false
		for name, runtime := range r.runtimes {
			if toRemove[name] {
				continue
			}
			if toRemove[runtime.Snapshot().Parent] {
				toRemove[name] = true
				changed = true
			}
		}
	}

	var removed []string
	for name := range toRemove {
		if _, ok := r.runtimes[name]; ok {
			delete(r.runtimes, name)
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	return removed
}

// List returns the currently tracked runtimes in name order.
func (r *RuntimeRegistry) List() []*AgentRuntime {
	r.mu.RLock()
	runtimes := make([]*AgentRuntime, 0, len(r.runtimes))
	for _, runtime := range r.runtimes {
		runtimes = append(runtimes, runtime)
	}
	r.mu.RUnlock()

	sort.Slice(runtimes, func(i, j int) bool {
		return runtimes[i].Snapshot().Name < runtimes[j].Snapshot().Name
	})
	return runtimes
}

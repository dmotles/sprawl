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

// ErrRecoverNotNeeded is returned by AgentRuntime.Recover when the live
// handle's backend session is still healthy (IsTerminallyFaulted() == false),
// signaling to callers (Real.Recover / the MCP recover tool) that the request
// is a no-op success. QUM-601.
var ErrRecoverNotNeeded = errors.New("supervisor: session healthy, no recovery needed")

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
	// RuntimeEventRecovered fires after AgentRuntime.Recover swaps in a
	// fresh handle for a faulted session. QUM-601.
	RuntimeEventRecovered RuntimeEventKind = "recovered"
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
}

// RuntimeHandle is the live controller for a started in-process child runtime.
type RuntimeHandle interface {
	Interrupt(ctx context.Context) error
	Wake() error
	// WakeForDelivery is the cooperative wake path used by send_message(
	// interrupt=false). Must NEVER call Session.Interrupt. See QUM-549/QUM-550.
	WakeForDelivery() error
	// ForceInterruptDelivery is the unconditional preempt path used by
	// send_message(interrupt=true). Calls Session.Interrupt even when the
	// recipient is idle. See QUM-549/QUM-550.
	ForceInterruptDelivery() error
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

	// recoverMu serializes Recover. TryLock-based fail-fast so concurrent
	// callers get a "recovery already in progress" error rather than queuing.
	// QUM-601.
	recoverMu sync.Mutex
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
func (r *AgentRuntime) StartResume(onResumeFailure ...func()) error {
	var cb func()
	for _, c := range onResumeFailure {
		if c != nil {
			cb = c
			break
		}
	}
	r.mu.RLock()
	spec := RuntimeStartSpec{
		Name:            r.snapshot.Name,
		Worktree:        r.snapshot.Worktree,
		SprawlRoot:      r.sprawlRoot,
		SessionID:       r.snapshot.SessionID,
		TreePath:        r.snapshot.TreePath,
		Resume:          true,
		OnResumeFailure: cb,
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

// Wake notifies an idle runtime that persisted work is ready to be observed.
func (r *AgentRuntime) Wake() error {
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

// ForceInterruptDelivery notifies a runtime that newly-persisted work must
// preempt any in-flight turn — unconditionally, even when idle. Updates the
// InterruptCount snapshot counter and emits RuntimeEventInterrupted.
// See QUM-549/QUM-550.
func (r *AgentRuntime) ForceInterruptDelivery() error {
	r.mu.RLock()
	handle := r.handle
	r.mu.RUnlock()

	if handle == nil {
		return fmt.Errorf("runtime session not started")
	}
	if err := handle.ForceInterruptDelivery(); err != nil {
		return err
	}

	r.mu.Lock()
	r.snapshot.InterruptCount++
	r.mu.Unlock()
	r.emit(RuntimeEventInterrupted)
	return nil
}

// Stop stops the tracked runtime handle, if any.
func (r *AgentRuntime) Stop(ctx context.Context) error {
	return r.stopWithFunc(ctx, func(h RuntimeHandle) error { return h.Stop(ctx) })
}

// StopAbandon stops the tracked runtime handle using the teardown-only
// path that skips the polite Session.Interrupt issued by Stop. Used by
// Real.Retire(abandon=true). See QUM-600.
func (r *AgentRuntime) StopAbandon(ctx context.Context) error {
	return r.stopWithFunc(ctx, func(h RuntimeHandle) error { return h.StopAbandon(ctx) })
}

// Recover performs in-place recovery on a faulted backend session (QUM-601).
//
// Behavior:
//   - If another Recover is already in progress, returns a "recovery already
//     in progress" error immediately (TryLock fail-fast).
//   - The precondition is expressed through the M0 liveness projection
//     (QUM-624/QUM-625): the request is accepted only when the snapshot
//     projects to liveness Faulted (live or durable torn-down fault) or
//     ResumeFailed (T19 manual-retry). A deliberately-Stopped agent is NOT a
//     legal recover source (invariant 3, enforced in M4 now that Faulted is
//     durable). Any other projected liveness returns a "cannot recover" error.
//   - If the live handle's session reports !IsTerminallyFaulted(), returns
//     ErrRecoverNotNeeded so callers can surface a "session healthy" ack.
//   - Otherwise: builds a RuntimeStartSpec from the current snapshot, calls
//     StopAbandon on the dead handle (errors logged but not fatal), invokes
//     starter.Start to build a fresh handle, swaps it in atomically, and
//     emits RuntimeEventRecovered.
//
// Handles that do not expose IsTerminallyFaulted() bool are treated as faulted
// (always recover) — defensive default; production handles all expose it via
// the embedded backend.Session.
func (r *AgentRuntime) Recover(ctx context.Context) error {
	if !r.recoverMu.TryLock() {
		return fmt.Errorf("supervisor: recovery already in progress")
	}
	defer r.recoverMu.Unlock()

	r.mu.RLock()
	starter := r.starter
	handle := r.handle
	lifecycle := r.snapshot.Liveness
	diskStatus := r.snapshot.Status
	agentName := r.snapshot.Name
	sprawlRoot := r.sprawlRoot
	spec := RuntimeStartSpec{
		Name:       r.snapshot.Name,
		Worktree:   r.snapshot.Worktree,
		SprawlRoot: r.sprawlRoot,
		SessionID:  r.snapshot.SessionID,
		TreePath:   r.snapshot.TreePath,
		// Recover instructs the new backend session to resume the prior
		// conversation transcript so history is preserved (QUM-601).
		Resume: true,
	}
	r.mu.RUnlock()

	// QUM-606 R3: propagate OnResumeFailure into the recover-path
	// RuntimeStartSpec so a rejected --resume cookie flips the agent to
	// StatusResumeFailed (mirrors Real.RecoverAgents).
	spec.OnResumeFailure = func() {
		cur, lErr := state.LoadAgent(sprawlRoot, agentName)
		if lErr != nil {
			slog.Warn("supervisor: Recover OnResumeFailure load", "agent", agentName, "err", lErr)
			return
		}
		cur.Status = state.StatusResumeFailed
		if sErr := state.SaveAgent(sprawlRoot, cur); sErr != nil {
			slog.Warn("supervisor: Recover OnResumeFailure save", "agent", agentName, "err", sErr)
			return
		}
		r.SyncAgentState(cur)
	}

	// QUM-624 (slice M2): the recover precondition is expressed through the
	// M0 liveness projection rather than a raw Lifecycle literal. Compute the
	// live-handle terminal-fault bit first — it feeds both the healthy-handle
	// short-circuit and the projection. Handles that don't expose the probe
	// are treated as faulted (defensive default; preserves prior behavior).
	faulted := false
	if handle != nil {
		if probe, ok := handle.(terminalFaultProbe); ok {
			faulted = probe.IsTerminallyFaulted()
		} else {
			faulted = true
		}
	}

	// Preserve the healthy-live-handle sentinel: a non-nil handle that
	// reports !IsTerminallyFaulted() is a no-op recover. Only the probe-
	// reports-healthy case returns ErrRecoverNotNeeded; the no-probe path
	// falls through (faulted == true) so it still recovers.
	if handle != nil && !faulted {
		return ErrRecoverNotNeeded
	}

	// Project the multi-source snapshot onto a unified liveness. Recover is
	// accepted iff the agent projects to a recoverable fault state:
	//   - Faulted: live-handle fault (Started + TerminalErr — the lie window),
	//     OR a torn-down fault (watchHandleExit stamped durable
	//     Status="faulted" → projects Faulted regardless of Lifecycle=Stopped).
	//   - ResumeFailed: T19 manual-retry (disk Status="resume_failed" →
	//     Lifecycle=Registered + snapshot.Status="resume_failed").
	// Everything else (Stopped/Running/Killed/Retired/Unstarted/...) is a hard
	// reject.
	//
	// QUM-625 (slice M4): invariant 3 is ENFORCED here. A *deliberately*
	// Stopped agent now carries durable Status="stopped" (stopWithFunc) and
	// projects Stopped, so it is no longer a legal recover source. A torn-down
	// fault carries durable Status="faulted" (watchHandleExit) and projects
	// Faulted, so it remains recoverable even though Lifecycle=Stopped. This is
	// why Stopped can safely drop out of the accept-set: the two cases are now
	// distinguishable by the durable disk Status.
	st := liveness.From(liveness.Snapshot{
		Lifecycle:   livenessToLifecycleString(lifecycle),
		TerminalErr: faulted,
		DiskStatus:  diskStatus,
	})
	if st.Liveness != liveness.Faulted &&
		st.Liveness != liveness.ResumeFailed {
		return fmt.Errorf("supervisor: agent %q is in liveness %q, cannot recover", spec.Name, st)
	}
	if starter == nil {
		return fmt.Errorf("supervisor: agent %q has no runtime starter, cannot recover", spec.Name)
	}
	// When lifecycle == Started, a live handle MUST be attached. When
	// lifecycle == Stopped (post-fault), the handle was already detached
	// by watchHandleExit, so it is expected to be nil — there is nothing
	// to tear down.
	if lifecycle == liveness.Running && handle == nil {
		return fmt.Errorf("supervisor: agent %q has no live handle, cannot recover", spec.Name)
	}

	// Detach the watcher BEFORE StopAbandon so the watchHandleExit
	// goroutine's `if r.handle == handle` guard sees a stale match and
	// no-ops when the abandoned handle's Done() closes. This suppresses
	// the spurious RuntimeEventStopped that would otherwise race against
	// the post-restart RuntimeEventRecovered emission. Skipped when there
	// is no live handle (post-R2-fault Stopped lifecycle).
	if handle != nil {
		r.mu.Lock()
		r.handle = nil
		r.mu.Unlock()

		if err := handle.StopAbandon(ctx); err != nil {
			slog.Warn(
				"supervisor: Recover StopAbandon of faulted handle returned error; continuing with restart",
				slog.String("agent", spec.Name),
				slog.Any("err", err),
			)
		}
	}

	// QUM-606 R1 / QUM-612: subprocess lifetime must outlive the MCP request
	// ctx. RuntimeStarter.Start no longer accepts a ctx — the QUM-606 bug
	// class (exec.CommandContext SIGKILLing the new claude as soon as
	// toolRecover returns) is now impossible by signature. The new handle's
	// teardown path (Stop / StopAbandon / watchHandleExit) owns subprocess
	// lifetime.
	newHandle, err := starter.Start(spec)
	if err != nil {
		// Recovery failed — the agent has no live handle. Flip lifecycle
		// to Stopped and emit so subscribers reflect the broken state.
		r.mu.Lock()
		r.snapshot.Liveness = liveness.Stopped
		r.mu.Unlock()
		r.emit(RuntimeEventStopped)
		return fmt.Errorf("supervisor: recover Start for %q: %w", spec.Name, err)
	}

	// QUM-606 R4: post-Start health probe. The starter may return a
	// handle whose backend session has already faulted (e.g. --resume
	// cookie rejected, or the transcript replays the wedge frame).
	// Wait up to recoverHealthProbeTimeout for either a healthy beat
	// (any non-init protocol frame on the handle's UnifiedRuntime
	// EventBus) OR an IsTerminallyFaulted flip. Treat timeout or fault
	// as recovery failure: tear the new handle down, return error, and
	// emit RuntimeEventStopped so the TUI fault banner re-fires.
	if probeErr := probeNewHandleHealth(newHandle, recoverHealthProbeTimeout); probeErr != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), recoverStopAbandonTimeout)
		_ = newHandle.StopAbandon(stopCtx)
		cancel()
		r.mu.Lock()
		r.snapshot.Liveness = liveness.Stopped
		r.mu.Unlock()
		r.emit(RuntimeEventStopped)
		return fmt.Errorf("supervisor: recover health probe for %q: %w", spec.Name, probeErr)
	}

	r.mu.Lock()
	r.handle = newHandle
	r.snapshot.Liveness = liveness.Running
	r.snapshot.Capabilities = newHandle.Capabilities()
	if sid := newHandle.SessionID(); sid != "" {
		r.snapshot.SessionID = sid
	}
	// QUM-625 (slice M4): a successful in-place recover clears the durable
	// resting Status (e.g. "faulted") and projects back to Running. Without
	// this, disk Status would stay "faulted" (invariant 7 violation): merge
	// would reject the healthy agent and a clean exit would leave it
	// non-auto-resumable. Mirror Real.RecoverAgents which writes active on
	// success.
	r.snapshot.Status = state.StatusActive
	recoveredName := r.snapshot.Name
	r.mu.Unlock()

	// Best-effort durable persist OUTSIDE r.mu so disk Status tracks the
	// recovered liveness (Running → "active").
	if recoveredName != "" {
		if cur, lErr := state.LoadAgent(r.sprawlRoot, recoveredName); lErr != nil {
			slog.Warn("supervisor: Recover durable status load", "agent", recoveredName, "err", lErr)
		} else {
			cur.Status = state.StatusActive
			if sErr := state.SaveAgent(r.sprawlRoot, cur); sErr != nil {
				slog.Warn("supervisor: Recover durable status save", "agent", recoveredName, "err", sErr)
			}
		}
	}

	r.emit(RuntimeEventRecovered)

	if doneAware, ok := newHandle.(runtimeHandleDone); ok && doneAware.Done() != nil {
		r.watchHandleExit(newHandle, doneAware.Done())
	}
	return nil
}

// recoverHealthProbeTimeout bounds how long AgentRuntime.Recover waits for
// the freshly-started handle to demonstrate liveness (a non-init protocol
// frame on its UnifiedRuntime EventBus) before declaring recovery a
// failure. See QUM-606 R4.
var recoverHealthProbeTimeout = 5 * time.Second

// recoverStopAbandonTimeout bounds the StopAbandon call used to tear down
// a handle that failed the post-Start health probe.
var recoverStopAbandonTimeout = 5 * time.Second

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

	ch, unsub := rt.EventBus().SubscribeNamed("recover-health-probe", 8)
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

	emitStopped := false
	var name string
	r.mu.Lock()
	r.handle = nil
	if r.snapshot.Liveness == liveness.Running {
		r.snapshot.Liveness = liveness.Stopped
		emitStopped = true
	}
	// QUM-625 (slice M4): a deliberate Stop is durable + distinguishable from a
	// fault. Stamp Status=stopped (never faulted) so the projection rests at
	// Stopped after teardown and the agent is no longer a legal recover source.
	name = r.snapshot.Name
	r.snapshot.Status = state.StatusStopped
	r.mu.Unlock()

	// Best-effort durable persist OUTSIDE r.mu. A terminal-ish disk Status
	// (killed/retired/retiring) or a completed/faulted resting status must not
	// be relabeled "stopped" — only an otherwise-live agent's deliberate Stop
	// becomes a durable Stopped resting state.
	if name != "" {
		cur, err := state.LoadAgent(r.sprawlRoot, name)
		if err != nil {
			slog.Warn("supervisor: stop durable status load", "agent", name, "err", err)
		} else {
			switch cur.Status {
			case state.StatusKilled, state.StatusRetired, state.StatusRetiring, state.StatusFaulted:
				// leave terminal-ish / faulted states as-is
			default:
				cur.Status = state.StatusStopped
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
			updated.Status == state.StatusSuspended ||
			updated.Status == state.StatusResumeFailed):
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

		emitStopped := false
		matched := false
		var name string
		var durableStatus string
		r.mu.Lock()
		if r.handle == handle {
			matched = true
			name = r.snapshot.Name
			r.handle = nil
			if r.snapshot.Liveness == liveness.Running {
				r.snapshot.Liveness = liveness.Stopped
				emitStopped = true
			}
			// Stamp a durable resting Status so the projection distinguishes a
			// torn-down fault (Faulted) from a clean stop (Stopped) after the
			// handle is gone — Lifecycle stays Stopped, From() decodes Status.
			if wasFaulted {
				durableStatus = state.StatusFaulted
			} else {
				durableStatus = state.StatusStopped
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
				case state.StatusKilled, state.StatusRetired, state.StatusRetiring:
					// leave terminal disk states as-is
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

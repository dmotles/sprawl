package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
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

// RuntimeLifecycle describes the in-memory lifecycle of a tracked child runtime.
type RuntimeLifecycle string

const (
	RuntimeLifecycleRegistered RuntimeLifecycle = "registered"
	RuntimeLifecycleStarted    RuntimeLifecycle = "started"
	RuntimeLifecycleStopped    RuntimeLifecycle = "stopped"
	RuntimeLifecycleKilled     RuntimeLifecycle = "killed"
	RuntimeLifecycleRetired    RuntimeLifecycle = "retired"
)

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
type RuntimeStarter interface {
	Start(ctx context.Context, spec RuntimeStartSpec) (RuntimeHandle, error)
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
	Lifecycle      RuntimeLifecycle
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
	if rt.snapshot.Lifecycle == "" {
		rt.snapshot.Lifecycle = RuntimeLifecycleRegistered
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
		snap.Lifecycle = RuntimeLifecycleKilled
	case "retired":
		snap.Lifecycle = RuntimeLifecycleRetired
	default:
		snap.Lifecycle = RuntimeLifecycleRegistered
	}
	return snap
}

// currentHandle returns the live runtime handle (or nil if not started or stopped).
func (r *AgentRuntime) currentHandle() RuntimeHandle {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handle
}

// InAutonomousTurn reports whether the live RuntimeHandle's backend Session is
// currently between sprawl-initiated turns (QUM-582/585). Returns false when
// the runtime is not started, has been stopped, or the handle does not
// implement the optional InAutonomousTurn() bool method.
func (r *AgentRuntime) InAutonomousTurn() bool {
	h := r.currentHandle()
	if h == nil {
		return false
	}
	probe, ok := h.(interface{ InAutonomousTurn() bool })
	if !ok {
		return false
	}
	return probe.InAutonomousTurn()
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
func (r *AgentRuntime) Start(ctx context.Context) error {
	r.mu.RLock()
	spec := RuntimeStartSpec{
		Name:       r.snapshot.Name,
		Worktree:   r.snapshot.Worktree,
		SprawlRoot: r.sprawlRoot,
		SessionID:  r.snapshot.SessionID,
		TreePath:   r.snapshot.TreePath,
	}
	r.mu.RUnlock()
	return r.startWithSpec(ctx, spec)
}

// startWithSpec is the shared body for Start and StartResume.
func (r *AgentRuntime) startWithSpec(ctx context.Context, spec RuntimeStartSpec) error {
	r.mu.RLock()
	starter := r.starter
	r.mu.RUnlock()

	if starter == nil {
		return fmt.Errorf("runtime starter not configured")
	}
	handle, err := starter.Start(ctx, spec)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.handle = handle
	r.snapshot.Lifecycle = RuntimeLifecycleStarted
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
func (r *AgentRuntime) StartResume(ctx context.Context, onResumeFailure ...func()) error {
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
	return r.startWithSpec(ctx, spec)
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
	r.snapshot.Lifecycle = RuntimeLifecycleStarted
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
//   - If the runtime is not in lifecycle Started, returns a "cannot recover"
//     error (no handle to swap).
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
	lifecycle := r.snapshot.Lifecycle
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

	if lifecycle != RuntimeLifecycleStarted {
		return fmt.Errorf("supervisor: agent %q is in lifecycle %q, cannot recover", spec.Name, lifecycle)
	}
	if handle == nil {
		return fmt.Errorf("supervisor: agent %q has no live handle, cannot recover", spec.Name)
	}
	if starter == nil {
		return fmt.Errorf("supervisor: agent %q has no runtime starter, cannot recover", spec.Name)
	}

	// Probe handle for terminal-fault state. Handles that don't expose the
	// probe are treated as faulted (always recover).
	if probe, ok := handle.(interface{ IsTerminallyFaulted() bool }); ok {
		if !probe.IsTerminallyFaulted() {
			return ErrRecoverNotNeeded
		}
	}

	// Detach the watcher BEFORE StopAbandon so the watchHandleExit
	// goroutine's `if r.handle == handle` guard sees a stale match and
	// no-ops when the abandoned handle's Done() closes. This suppresses
	// the spurious RuntimeEventStopped that would otherwise race against
	// the post-restart RuntimeEventRecovered emission.
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

	newHandle, err := starter.Start(ctx, spec)
	if err != nil {
		// Recovery failed — the agent has no live handle. Flip lifecycle
		// to Stopped and emit so subscribers reflect the broken state.
		r.mu.Lock()
		r.snapshot.Lifecycle = RuntimeLifecycleStopped
		r.mu.Unlock()
		r.emit(RuntimeEventStopped)
		return fmt.Errorf("supervisor: recover Start for %q: %w", spec.Name, err)
	}

	r.mu.Lock()
	r.handle = newHandle
	r.snapshot.Lifecycle = RuntimeLifecycleStarted
	r.snapshot.Capabilities = newHandle.Capabilities()
	if sid := newHandle.SessionID(); sid != "" {
		r.snapshot.SessionID = sid
	}
	r.mu.Unlock()

	r.emit(RuntimeEventRecovered)

	if doneAware, ok := newHandle.(runtimeHandleDone); ok && doneAware.Done() != nil {
		r.watchHandleExit(newHandle, doneAware.Done())
	}
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
	if probe, ok := handle.(interface{ StopWaitTimedOut() bool }); ok {
		r.stopWaitTimedOut.Store(probe.StopWaitTimedOut())
	}
	if stopErr != nil {
		return stopErr
	}

	emitStopped := false
	r.mu.Lock()
	r.handle = nil
	if r.snapshot.Lifecycle == RuntimeLifecycleStarted {
		r.snapshot.Lifecycle = RuntimeLifecycleStopped
		emitStopped = true
	}
	r.mu.Unlock()
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
	case updated.Lifecycle == RuntimeLifecycleKilled:
	case updated.Lifecycle == RuntimeLifecycleRetired:
	case r.snapshot.Lifecycle == RuntimeLifecycleStarted:
		updated.Lifecycle = RuntimeLifecycleStarted
	case r.snapshot.Lifecycle == RuntimeLifecycleStopped:
		updated.Lifecycle = RuntimeLifecycleStopped
	default:
		updated.Lifecycle = RuntimeLifecycleRegistered
	}

	r.snapshot = updated
	r.mu.Unlock()
	r.emit(RuntimeEventStateSynced)
}

func (r *AgentRuntime) watchHandleExit(handle RuntimeHandle, done <-chan struct{}) {
	go func() {
		<-done

		emitStopped := false
		r.mu.Lock()
		if r.handle == handle {
			r.handle = nil
			if r.snapshot.Lifecycle == RuntimeLifecycleStarted {
				r.snapshot.Lifecycle = RuntimeLifecycleStopped
				emitStopped = true
			}
		}
		r.mu.Unlock()
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

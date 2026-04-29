package supervisor

import (
	"context"
	"fmt"
	"sort"
	"sync"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
)

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
)

// RuntimeStartSpec is the internal-only launch seam for same-process child runtimes.
// QUM-351 keeps it inside internal/supervisor; QUM-352 can refine it further.
type RuntimeStartSpec struct {
	Name       string
	Worktree   string
	SprawlRoot string
	SessionID  string
	TreePath   string
}

// RuntimeHandle is the live controller for a started in-process child runtime.
type RuntimeHandle interface {
	Interrupt(ctx context.Context) error
	Wake() error
	InterruptDelivery() error
	Stop(ctx context.Context) error
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
// consumers can bind to without depending on tmux session/window concepts.
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

// AgentRuntime is the in-memory container QUM-351 adds for future same-process
// child lifecycles. It is intentionally passive: persisted state and the
// existing tmux/agent-loop path remain authoritative until later phases.
type AgentRuntime struct {
	mu         sync.RWMutex
	sprawlRoot string
	starter    RuntimeStarter
	handle     RuntimeHandle
	snapshot   RuntimeSnapshot

	nextSubscriberID int
	subscribers      map[int]chan RuntimeEvent
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
	starter := r.starter
	spec := RuntimeStartSpec{
		Name:       r.snapshot.Name,
		Worktree:   r.snapshot.Worktree,
		SprawlRoot: r.sprawlRoot,
		SessionID:  r.snapshot.SessionID,
		TreePath:   r.snapshot.TreePath,
	}
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

// InterruptDelivery notifies a runtime that newly-persisted work should
// preempt any in-flight turn and be delivered promptly on the next loop.
func (r *AgentRuntime) InterruptDelivery() error {
	r.mu.RLock()
	handle := r.handle
	r.mu.RUnlock()

	if handle == nil {
		return fmt.Errorf("runtime session not started")
	}
	if err := handle.InterruptDelivery(); err != nil {
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
	r.mu.RLock()
	handle := r.handle
	r.mu.RUnlock()

	if handle == nil {
		return nil
	}
	if err := handle.Stop(ctx); err != nil {
		return err
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
func (r *AgentRuntime) RecordQueuedTask(_ *state.Task) {
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

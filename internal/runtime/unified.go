// UnifiedRuntime wraps the per-agent runtime building blocks (MessageQueue,
// EventBus, TurnLoop) behind a single supervised lifecycle. See
// docs/designs/unified-runtime.md sections 3.1, 3.6, and 4.

package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// RuntimeState is the externally-visible lifecycle state of a UnifiedRuntime.
type RuntimeState int

const (
	// StateIdle: runtime is alive but no turn is in flight.
	StateIdle RuntimeState = iota
	// StateTurnActive: the turn loop is currently executing a turn.
	StateTurnActive
	// StateInterrupting: an interrupt has been requested for the active turn
	// and we are waiting for it to drain.
	StateInterrupting
	// StateStopped: Stop() has completed; the runtime is no longer usable.
	StateStopped
)

// String returns a short diagnostic name for the state.
func (s RuntimeState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateTurnActive:
		return "turn-active"
	case StateInterrupting:
		return "interrupting"
	case StateStopped:
		return "stopped"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// RuntimeConfig is the immutable construction-time configuration.
type RuntimeConfig struct {
	Name          string
	SprawlRoot    string
	Session       SessionHandle
	IsRoot        bool
	InitialPrompt string
	// Capabilities is the backend-reported feature set surfaced to callers
	// via UnifiedRuntime.Capabilities(). The supervisor uses this to forward
	// caps to its RuntimeHandle. See QUM-398.
	Capabilities backend.Capabilities
	// OnQueueItemDelivered is forwarded to TurnLoopConfig. See TurnLoopConfig.
	OnQueueItemDelivered func(item QueueItem)
}

// sessionIDProvider is an optional interface a Session may satisfy to expose a
// stable session identifier.
type sessionIDProvider interface {
	SessionID() string
}

// UnifiedRuntime owns the per-agent queue, event bus, and turn loop and
// coordinates their lifecycle.
//
// State updates are driven synchronously from a wrapper around the
// SessionHandle: when StartTurn is invoked from the loop goroutine, the
// wrapper marks the runtime as turn-active before returning the event
// channel; when the channel closes, the wrapper transitions back to idle.
// This means rt.State() observed from any goroutine is consistent with the
// observable progress of the loop, without depending on a buffered
// EventBus subscription draining in time.
type UnifiedRuntime struct {
	cfg      RuntimeConfig
	queue    *MessageQueue
	eventBus *EventBus

	mu          sync.RWMutex
	state       RuntimeState
	started     bool
	stopped     bool
	turnRunning bool
	// pendingInterrupt arms an interrupt for the *next* turn the loop
	// starts. It is set when Interrupt() is called while no turn is
	// currently running (state Idle but the loop may be about to consume
	// queued work). The wrapper consumes the flag on the next StartTurn
	// and routes it through TurnLoop.Interrupt so the terminal event is
	// classified as EventInterrupted.
	pendingInterrupt bool

	cancel   context.CancelFunc
	loopWG   sync.WaitGroup
	turnLoop *TurnLoop

	done          chan struct{}
	closeDoneOnce sync.Once
}

// New constructs a UnifiedRuntime in StateIdle with a fresh queue and event
// bus. No goroutines are started until Start is called.
func New(cfg RuntimeConfig) *UnifiedRuntime {
	return &UnifiedRuntime{
		cfg:      cfg,
		queue:    NewMessageQueue(),
		eventBus: NewEventBus(),
		state:    StateIdle,
		done:     make(chan struct{}),
	}
}

// stateTrackingSession wraps a SessionHandle so the runtime can update its
// internal RuntimeState synchronously from inside the TurnLoop goroutine.
type stateTrackingSession struct {
	inner SessionHandle
	rt    *UnifiedRuntime
}

func (s *stateTrackingSession) StartTurn(ctx context.Context, prompt string, spec ...backend.TurnSpec) (<-chan *protocol.Message, error) {
	// Transition to TurnActive synchronously before delegating, so any
	// goroutine that polls State() after the StartTurn call returns sees
	// the updated state.
	s.rt.mu.Lock()
	if s.rt.state == StateIdle {
		s.rt.state = StateTurnActive
	}
	s.rt.turnRunning = true
	pending := s.rt.pendingInterrupt
	s.rt.pendingInterrupt = false
	s.rt.mu.Unlock()

	ch, err := s.inner.StartTurn(ctx, prompt, spec...)
	if err != nil {
		// Turn failed to start; revert state.
		s.rt.mu.Lock()
		s.rt.turnRunning = false
		if s.rt.state == StateTurnActive || s.rt.state == StateInterrupting {
			s.rt.state = StateIdle
		}
		s.rt.mu.Unlock()
		return nil, err
	}

	// If an interrupt was requested before the turn actually started, route
	// it through the TurnLoop so it sets `interrupted=true` and ultimately
	// publishes EventInterrupted (not EventTurnCompleted) on the bus. The
	// loop's interruptCh is buffered and is installed before StartTurn is
	// invoked, so this send is guaranteed to land. Falling through to a
	// direct Session.Interrupt call would bypass the loop's bookkeeping
	// and cause the terminal event to be misclassified.
	if pending {
		s.rt.mu.RLock()
		loop := s.rt.turnLoop
		s.rt.mu.RUnlock()
		if loop != nil {
			_ = loop.Interrupt(context.Background())
		} else {
			_ = s.inner.Interrupt(context.Background())
		}
	}

	// Forward channel and watch for close to flip state back.
	out := make(chan *protocol.Message)
	go func() {
		defer close(out)
		for msg := range ch {
			select {
			case out <- msg:
			case <-ctx.Done():
				// Context cancelled; drop remaining messages so the
				// inner channel can drain. The TurnLoop will return
				// promptly on its own select on ctx.Done.
				return
			}
		}
		// Channel closed: turn ended (success, failure, or interrupt).
		s.rt.mu.Lock()
		s.rt.turnRunning = false
		if s.rt.state != StateStopped {
			s.rt.state = StateIdle
		}
		s.rt.mu.Unlock()
	}()
	return out, nil
}

func (s *stateTrackingSession) Interrupt(ctx context.Context) error {
	return s.inner.Interrupt(ctx)
}

// SessionID is exposed if the inner session implements sessionIDProvider.
func (s *stateTrackingSession) SessionID() string {
	if p, ok := s.inner.(sessionIDProvider); ok {
		return p.SessionID()
	}
	return ""
}

// Start spins up the turn loop. Returns an error if the runtime has already
// been started or has been stopped.
func (rt *UnifiedRuntime) Start(_ context.Context) error {
	rt.mu.Lock()
	if rt.stopped {
		rt.mu.Unlock()
		return errors.New("runtime: Start called on stopped runtime")
	}
	if rt.started {
		rt.mu.Unlock()
		return errors.New("runtime: Start called twice")
	}
	rt.started = true

	// Independent context: the turn loop must outlive the Start caller's ctx.
	runCtx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel

	tracker := &stateTrackingSession{inner: rt.cfg.Session, rt: rt}

	rt.turnLoop = NewTurnLoop(TurnLoopConfig{
		Session:              tracker,
		Queue:                rt.queue,
		EventBus:             rt.eventBus,
		InitialPrompt:        rt.cfg.InitialPrompt,
		OnQueueItemDelivered: rt.cfg.OnQueueItemDelivered,
	})

	rt.mu.Unlock()

	rt.loopWG.Add(1)
	go func() {
		defer rt.loopWG.Done()
		_ = rt.turnLoop.Run(runCtx)
	}()

	go func() {
		rt.loopWG.Wait()
		rt.closeDoneOnce.Do(func() { close(rt.done) })
	}()

	return nil
}

// Stop cancels the turn loop and waits for it to drain. Idempotent and a
// no-op if Start was never called. Bounded by ctx.
//
// Stop semantics during an active turn (QUM-414):
//   - Session.Interrupt is forwarded to the backend before ctx is cancelled,
//     giving the backend a clean shutdown signal independent of the
//     ctx-cancel path. Backends are contracted to be idempotent and to
//     no-op when no turn is in flight, so this is safe in all states.
//   - The lifecycle event published is EventStopped (from the TurnLoop's
//     outer Run loop). Stop does NOT publish EventInterrupted —
//     EventInterrupted is reserved for user-initiated Interrupt drains.
//   - Mid-turn protocol messages are not guaranteed to be delivered to
//     EventBus subscribers: the wrapper forwarder returns on ctx.Done.
func (rt *UnifiedRuntime) Stop(ctx context.Context) error {
	rt.mu.Lock()
	if !rt.started {
		rt.stopped = true
		rt.state = StateStopped
		rt.closeDoneOnce.Do(func() { close(rt.done) })
		rt.mu.Unlock()
		return nil
	}
	if rt.stopped {
		rt.mu.Unlock()
		return nil
	}
	rt.stopped = true
	cancel := rt.cancel
	sess := rt.cfg.Session
	rt.mu.Unlock()

	// Best-effort: signal the backend to wind down its in-flight turn cleanly.
	// Called before cancel() so ctx is still alive for the interrupt control
	// request itself. Per SessionHandle contract, Interrupt is a no-op when
	// no turn is in flight.
	if sess != nil {
		_ = sess.Interrupt(ctx)
	}

	if cancel != nil {
		cancel()
	}

	loopDone := make(chan struct{})
	go func() {
		rt.loopWG.Wait()
		close(loopDone)
	}()

	select {
	case <-loopDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	rt.mu.Lock()
	rt.state = StateStopped
	rt.mu.Unlock()
	return nil
}

// State returns the stored runtime state. Transitions are driven from the
// wrapped Session's StartTurn / channel-close path. Callers that need to
// observe a turn starting after Enqueue should subscribe to the EventBus
// (EventTurnStarted) rather than poll State().
func (rt *UnifiedRuntime) State() RuntimeState {
	rt.mu.RLock()
	s := rt.state
	rt.mu.RUnlock()
	return s
}

// Interrupt always forwards to the underlying Session.Interrupt (Backends
// must be idempotent). When a turn is in flight it additionally drives
// runtime-state bookkeeping (TurnActive → Interrupting) and routes through
// TurnLoop.Interrupt. No-op when stopped.
func (rt *UnifiedRuntime) Interrupt(ctx context.Context) error {
	rt.mu.Lock()
	if rt.state == StateStopped {
		rt.mu.Unlock()
		return nil
	}
	sess := rt.cfg.Session
	loop := rt.turnLoop
	turnRunning := rt.turnRunning
	state := rt.state
	if state == StateTurnActive {
		rt.state = StateInterrupting
	} else if !turnRunning {
		// Queue items may be pending but the wrapper hasn't entered StartTurn yet.
		// Arm a pending-interrupt flag so the next StartTurn classifies its
		// terminal event as EventInterrupted (not EventTurnCompleted).
		rt.pendingInterrupt = true
	}
	rt.mu.Unlock()

	// Always forward to the backend session so the supervisor wrapper can collapse
	// to a single delegated call (QUM-435). Backends are required to be idempotent
	// and to no-op when no turn is in flight.
	_ = sess.Interrupt(ctx)

	if loop != nil && turnRunning {
		return loop.Interrupt(ctx)
	}
	return nil
}

// InterruptDelivery wakes the turn loop's queue signal so a blocked loop can
// re-check state. Safe to call before Start.
func (rt *UnifiedRuntime) InterruptDelivery(ctx context.Context) error {
	rt.mu.RLock()
	started := rt.started
	stopped := rt.stopped
	rt.mu.RUnlock()

	if stopped {
		return nil
	}
	if started {
		_ = rt.Interrupt(ctx)
	}
	rt.queue.Wake()
	return nil
}

// Queue returns the runtime's MessageQueue. Stable for the lifetime of the
// UnifiedRuntime.
func (rt *UnifiedRuntime) Queue() *MessageQueue {
	return rt.queue
}

// EventBus returns the runtime's EventBus. Stable for the lifetime of the
// UnifiedRuntime.
func (rt *UnifiedRuntime) EventBus() *EventBus {
	return rt.eventBus
}

// Name returns the configured agent name.
func (rt *UnifiedRuntime) Name() string {
	return rt.cfg.Name
}

// Done returns a channel that is closed after the turn loop goroutine has
// exited (whether via Stop, ctx cancellation, or natural completion). If
// Stop is called without Start ever having been called, the channel is
// also closed. Safe to call before Start.
func (rt *UnifiedRuntime) Done() <-chan struct{} { return rt.done }

// Capabilities returns the configured backend capabilities.
func (rt *UnifiedRuntime) Capabilities() backend.Capabilities {
	return rt.cfg.Capabilities
}

// SessionID returns the underlying Session's ID if it implements
// SessionID(); otherwise the empty string.
func (rt *UnifiedRuntime) SessionID() string {
	if p, ok := rt.cfg.Session.(sessionIDProvider); ok {
		return p.SessionID()
	}
	return ""
}

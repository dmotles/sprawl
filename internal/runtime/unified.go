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
	// starts. It is set when Interrupt() is called while the runtime is
	// effectively-active (state Idle but queue.Len()>0) but the wrapper
	// has not yet entered StartTurn. The wrapper consumes the flag on the
	// next StartTurn and fires Session.Interrupt against that turn —
	// which may include items enqueued after the original Interrupt call.
	pendingInterrupt bool

	cancel   context.CancelFunc
	loopWG   sync.WaitGroup
	turnLoop *TurnLoop
}

// New constructs a UnifiedRuntime in StateIdle with a fresh queue and event
// bus. No goroutines are started until Start is called.
func New(cfg RuntimeConfig) *UnifiedRuntime {
	return &UnifiedRuntime{
		cfg:      cfg,
		queue:    NewMessageQueue(),
		eventBus: NewEventBus(),
		state:    StateIdle,
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
		Session:       tracker,
		Queue:         rt.queue,
		EventBus:      rt.eventBus,
		InitialPrompt: rt.cfg.InitialPrompt,
	})

	rt.mu.Unlock()

	rt.loopWG.Add(1)
	go func() {
		defer rt.loopWG.Done()
		_ = rt.turnLoop.Run(runCtx)
	}()

	return nil
}

// Stop cancels the turn loop and waits for it to drain. Idempotent and a
// no-op if Start was never called. Bounded by ctx.
func (rt *UnifiedRuntime) Stop(ctx context.Context) error {
	rt.mu.Lock()
	if !rt.started {
		rt.stopped = true
		rt.state = StateStopped
		rt.mu.Unlock()
		return nil
	}
	if rt.stopped {
		rt.mu.Unlock()
		return nil
	}
	rt.stopped = true
	cancel := rt.cancel
	rt.mu.Unlock()

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

// State returns the current runtime state. The state reflects both stored
// transitions (driven by the wrapped Session's StartTurn / channel-close
// path) and pending work in the queue: if the stored state is Idle but the
// MessageQueue has unprocessed items, the runtime is reported as
// TurnActive — the loop is about to pick those items up. This avoids a
// race window between Enqueue (publisher) and the loop's first iteration
// where State() would briefly read Idle even though work is pending.
func (rt *UnifiedRuntime) State() RuntimeState {
	rt.mu.RLock()
	s := rt.state
	stopped := rt.stopped
	rt.mu.RUnlock()
	if s == StateIdle && !stopped && rt.queue.Len() > 0 {
		return StateTurnActive
	}
	return s
}

// Interrupt requests that the active turn be interrupted. No-op when not in
// StateTurnActive.
func (rt *UnifiedRuntime) Interrupt(ctx context.Context) error {
	rt.mu.Lock()
	// Effective state: same logic as State() but inside the lock.
	effective := rt.state
	if effective == StateIdle && !rt.stopped && rt.queue.Len() > 0 {
		effective = StateTurnActive
	}
	if effective != StateTurnActive {
		rt.mu.Unlock()
		return nil
	}
	rt.state = StateInterrupting
	loop := rt.turnLoop
	turnRunning := rt.turnRunning
	if !turnRunning {
		// Queue items pending but the wrapper hasn't yet entered
		// StartTurn for this work. Arm a pending-interrupt flag so
		// the session wrapper fires Session.Interrupt the moment the
		// turn begins.
		rt.pendingInterrupt = true
	}
	rt.mu.Unlock()

	if loop == nil {
		return nil
	}
	if !turnRunning {
		// turnLoop.Interrupt would no-op (interruptCh nil); the
		// pending flag handles delivery.
		return nil
	}
	return loop.Interrupt(ctx)
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

// SessionID returns the underlying Session's ID if it implements
// SessionID(); otherwise the empty string.
func (rt *UnifiedRuntime) SessionID() string {
	if p, ok := rt.cfg.Session.(sessionIDProvider); ok {
		return p.SessionID()
	}
	return ""
}

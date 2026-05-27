// UnifiedRuntime wraps the per-agent runtime building blocks (MessageQueue,
// EventBus, TurnLoop) behind a single supervised lifecycle. See
// docs/designs/unified-runtime.md sections 3.1, 3.6, and 4.

package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	livenesspkg "github.com/dmotles/sprawl/internal/supervisor/liveness"
)

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
	// PostTurnSweep is forwarded to TurnLoopConfig. See TurnLoopConfig.
	PostTurnSweep func()
	// TurnTimeout is forwarded to TurnLoopConfig.TurnTimeout (0 = no deadline).
	// See QUM-581.
	TurnTimeout time.Duration
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
	liveness    livenesspkg.State
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

// New constructs a UnifiedRuntime in the idle liveness state (Running,
// non-autonomous) with a fresh queue and event bus. No goroutines are started
// until Start is called.
func New(cfg RuntimeConfig) *UnifiedRuntime {
	rt := &UnifiedRuntime{
		cfg:      cfg,
		queue:    NewMessageQueue(),
		eventBus: NewEventBus(),
		liveness: livenesspkg.State{Liveness: livenesspkg.Running},
		done:     make(chan struct{}),
	}
	// QUM-602: install the backend-fault handler on the session. We use a
	// type assertion (rather than extending SessionHandle) so the public
	// interface stays minimal — the concrete backend.*session implements
	// SetTerminalErrorHandler; tests' fake sessions implement it ad-hoc.
	if cfg.Session != nil {
		if setter, ok := cfg.Session.(interface {
			SetTerminalErrorHandler(func(error))
		}); ok {
			setter.SetTerminalErrorHandler(func(err error) {
				class, hint := ClassifyBackendFault(err)
				rt.eventBus.Publish(RuntimeEvent{
					Type:            EventBackendFaulted,
					Error:           err,
					FaultClass:      class,
					FaultNextAction: hint,
				})
				// QUM-606 R2: cancel the turn-loop runCtx so the loop
				// exits, loopWG unblocks, and rt.done closes. Without
				// this, AgentRuntime.watchHandleExit is structurally
				// blind to backend-session death (Done() only fired on
				// Stop before this change). On cancel, the supervisor
				// transitions Lifecycle → Stopped and emits
				// RuntimeEventStopped so the TUI fault banner re-fires.
				rt.mu.RLock()
				cancel := rt.cancel
				rt.mu.RUnlock()
				if cancel != nil {
					cancel()
				}
			})
		}
		// QUM-631: install the autonomous-frame handler so harness-initiated
		// (SDK self-reprompt) turns surface on the EventBus. Same type-assertion
		// pattern as the terminal-error handler above.
		if setter, ok := cfg.Session.(interface {
			SetAutonomousFrameHandler(func(*protocol.Message))
		}); ok {
			setter.SetAutonomousFrameHandler(func(msg *protocol.Message) {
				// QUM-631: surface harness-initiated (autonomous) turns to the
				// EventBus so the TUI viewport + activity stream render them.
				rt.eventBus.Publish(RuntimeEvent{Type: EventProtocolMessage, Message: msg})
				if msg.Type == "result" {
					var r protocol.ResultMessage
					_ = protocol.ParseAs(msg, &r)
					rt.eventBus.Publish(RuntimeEvent{Type: EventTurnCompleted, Result: &r})
				}
			})
		}
	}
	return rt
}

// ClassifyBackendFault maps a backend session terminal error to a
// UX-visible class label and an operator-facing next-action hint. Known
// sentinels (ErrHangTimeout / ErrSubscriberWedged) get tailored hints;
// unknown errors fall through to a generic "Unknown" + respawn hint.
// QUM-602.
func ClassifyBackendFault(err error) (class, nextAction string) {
	switch {
	case errors.Is(err, backend.ErrHangTimeout):
		return "HangTimeout", "backend reader stalled; run mcp__sprawl__recover to restart in place"
	case errors.Is(err, backend.ErrSubscriberWedged):
		return "SubscriberWedged", "backend subscriber send wedged; run mcp__sprawl__recover to restart in place"
	default:
		return "Unknown", "run mcp__sprawl__recover to restart in place"
	}
}

// stateTrackingSession wraps a SessionHandle so the runtime can update its
// internal liveness state synchronously from inside the TurnLoop goroutine.
type stateTrackingSession struct {
	inner SessionHandle
	rt    *UnifiedRuntime
}

func (s *stateTrackingSession) StartTurn(ctx context.Context, prompt string, spec ...backend.TurnSpec) (<-chan *protocol.Message, error) {
	// Transition to TurnActive synchronously before delegating, so any
	// goroutine that polls State() after the StartTurn call returns sees
	// the updated state.
	s.rt.mu.Lock()
	if s.rt.liveness.Liveness == livenesspkg.Running && !s.rt.liveness.InAutonomousTurn {
		s.rt.liveness.InAutonomousTurn = true
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
		if (s.rt.liveness.Liveness == livenesspkg.Running && s.rt.liveness.InAutonomousTurn) ||
			s.rt.liveness.Liveness == livenesspkg.Stopping {
			s.rt.liveness = livenesspkg.State{Liveness: livenesspkg.Running}
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
		// QUM-618: reset turnRunning/state in a defer so BOTH exit paths —
		// the normal "channel closed" path and the early ctx.Done() return
		// (per-turn deadline / parent cancel) — flip the runtime back to Idle.
		// Previously the ctx.Done() early-return skipped the reset, leaking
		// turnRunning=true and wedging the queue drain. Defers run LIFO, so
		// this reset runs before close(out) — that ordering is fine.
		defer func() {
			s.rt.mu.Lock()
			s.rt.turnRunning = false
			if s.rt.liveness.Liveness != livenesspkg.Stopped {
				s.rt.liveness = livenesspkg.State{Liveness: livenesspkg.Running}
			}
			s.rt.mu.Unlock()
		}()
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
		PostTurnSweep:        rt.cfg.PostTurnSweep,
		TurnTimeout:          rt.cfg.TurnTimeout,
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

// StopOptions tunes UnifiedRuntime.StopWithOptions. The zero value matches
// the legacy Stop semantics (polite Session.Interrupt issued before the
// turn loop ctx is cancelled). See QUM-600.
type StopOptions struct {
	// SkipPoliteInterrupt suppresses the polite Session.Interrupt that
	// Stop normally issues before cancelling the loop. The abandon-retire
	// path (Real.Retire(abandon=true) → StopAbandon) sets this to true so
	// a wedged backend Interrupt cannot stall teardown; the caller is
	// committed to Close+Kill regardless. (QUM-600)
	SkipPoliteInterrupt bool
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
//
// Stop delegates to StopWithOptions with the zero-value StopOptions, so the
// legacy contract is preserved.
func (rt *UnifiedRuntime) Stop(ctx context.Context) error {
	return rt.StopWithOptions(ctx, StopOptions{})
}

// StopWithOptions is the configurable variant of Stop. When
// opts.SkipPoliteInterrupt is true, the polite Session.Interrupt that Stop
// normally issues before cancelling the loop is skipped — used by the
// abandon-retire path (QUM-600) so a wedged backend Interrupt cannot stall
// teardown. All other semantics match Stop.
func (rt *UnifiedRuntime) StopWithOptions(ctx context.Context, opts StopOptions) error {
	rt.mu.Lock()
	if !rt.started {
		rt.stopped = true
		rt.liveness = livenesspkg.State{Liveness: livenesspkg.Stopped}
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
	// no turn is in flight. Skipped when opts.SkipPoliteInterrupt is true
	// (QUM-600 abandon path).
	if sess != nil && !opts.SkipPoliteInterrupt {
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
	rt.liveness = livenesspkg.State{Liveness: livenesspkg.Stopped}
	rt.mu.Unlock()
	return nil
}

// State returns the stored runtime liveness state. Transitions are driven from
// the wrapped Session's StartTurn / channel-close path. Callers that need to
// observe a turn starting after Enqueue should subscribe to the EventBus
// (EventTurnStarted) rather than poll State().
func (rt *UnifiedRuntime) State() livenesspkg.State {
	rt.mu.RLock()
	s := rt.liveness
	rt.mu.RUnlock()
	return s
}

// Interrupt always forwards to the underlying Session.Interrupt (Backends
// must be idempotent). When a turn is in flight it additionally drives
// runtime-state bookkeeping (Running·autonomous-turn → Stopping) and routes
// through TurnLoop.Interrupt. No-op when stopped.
func (rt *UnifiedRuntime) Interrupt(ctx context.Context) error {
	rt.mu.Lock()
	if rt.liveness.Liveness == livenesspkg.Stopped {
		rt.mu.Unlock()
		return nil
	}
	sess := rt.cfg.Session
	loop := rt.turnLoop
	turnRunning := rt.turnRunning
	if rt.liveness.Liveness == livenesspkg.Running && rt.liveness.InAutonomousTurn {
		rt.liveness = livenesspkg.State{Liveness: livenesspkg.Stopping}
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

// WakeForDelivery is the cooperative-wake path used by send_message(
// interrupt=false). It NEVER calls Session.Interrupt or loop.Interrupt —
// it only pokes the queue signal so the runtime observes newly-enqueued
// items at the next turn boundary. This closes the QUM-549 lie where
// send_async would stomp on the recipient's mid-turn work.
func (rt *UnifiedRuntime) WakeForDelivery(_ context.Context) error {
	rt.mu.Lock()
	if rt.stopped {
		rt.mu.Unlock()
		return nil
	}
	rt.mu.Unlock()
	rt.queue.Wake()
	return nil
}

// ForceInterruptForDelivery is the preempt path used by
// send_message(interrupt=true). It snapshots `turnRunning` under `rt.mu`
// and only calls Session.Interrupt / loop.Interrupt when a turn is
// genuinely in flight (QUM-294 mid-turn preempt). When the recipient is
// idle, the ClassInterrupt queue item already enqueued by
// drainPendingToQueue plus the cooperative queue.Wake below is sufficient
// to deliver the interrupt-class prompt at the next turn boundary —
// issuing Session.Interrupt would cancel the very turn that exists to
// deliver this message (QUM-619).
//
// The QUM-462 / QUM-510 failure mode ("interrupt is silently lost") does
// NOT recur under this gate: the cooperative wake path is preserved, so
// even when the snapshot races with a turn that starts immediately
// after, the brand-new turn proceeds normally and processes the
// ClassInterrupt prompt — which is the desired outcome. See
// docs/research/qum-619-idle-interrupt-race-2026-05-21.md.
func (rt *UnifiedRuntime) ForceInterruptForDelivery(ctx context.Context) error {
	rt.mu.Lock()
	if rt.stopped {
		rt.mu.Unlock()
		return nil
	}
	sess := rt.cfg.Session
	loop := rt.turnLoop
	turnRunning := rt.turnRunning
	if rt.liveness.Liveness == livenesspkg.Running && rt.liveness.InAutonomousTurn {
		rt.liveness = livenesspkg.State{Liveness: livenesspkg.Stopping}
	}
	rt.mu.Unlock()

	if turnRunning {
		if sess != nil {
			_ = sess.Interrupt(ctx)
		}
		if loop != nil {
			_ = loop.Interrupt(ctx)
		}
	}
	rt.queue.Wake()
	return nil
}

// QUM-462 / QUM-510 (removed in QUM-550 slice 4): the legacy
// `InterruptDelivery` / `interruptForDelivery` pair conditionally called
// `Session.Interrupt` based on a turn-running snapshot. That gate produced
// "interrupt is silently lost" bugs when the snapshot raced with the
// runtime entering a new turn AND the snapshot result was used to skip the
// cooperative wake path. The successors `WakeForDelivery` (never calls
// Session.Interrupt) and `ForceInterruptForDelivery` (preempt only when
// turnRunning, but ALWAYS pokes queue.Wake) avoid that failure mode:
// regardless of the snapshot's outcome, the wake-and-deliver path always
// runs, so a ClassInterrupt queue item is observed at the next turn
// boundary. QUM-619 added the `if turnRunning` gate around
// `Session.Interrupt` to stop interrupts from canceling their own delivery
// turn against an idle recipient.

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

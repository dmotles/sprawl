// TurnLoop drives the per-agent execution cycle: it pulls work off the
// MessageQueue, hands a prompt to the backend Session, drains the resulting
// protocol stream onto the EventBus, and surfaces lifecycle events
// (TurnStarted/TurnCompleted/TurnFailed/Interrupted/QueueDrained/Stopped) to
// subscribers. See docs/designs/unified-runtime.md section 3.4.

package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// SessionHandle is the subset of the backend Session API that the TurnLoop
// needs. *backend.Session satisfies it structurally; tests may substitute a
// mock. Keeping this an interface here (rather than depending on the concrete
// type) lets the runtime package be reused across backends.
//
// Contract for implementers:
//   - StartTurn must tie the returned channel's lifetime to the supplied ctx:
//     when ctx is cancelled, the channel must be closed promptly without
//     blocking any background sender goroutine. The TurnLoop relies on this to
//     avoid leaking the backend's reader goroutine on ctx cancellation — when
//     ctx fires mid-turn the loop returns immediately and stops reading from
//     `events`, so any pending send on a non-buffered or full channel must be
//     unblocked by ctx-aware logic on the producer side.
//   - Interrupt must be safe to call concurrently with an active StartTurn and
//     when no turn is in flight (no-op).
type SessionHandle interface {
	StartTurn(ctx context.Context, prompt string, spec ...backend.TurnSpec) (<-chan *protocol.Message, error)
	Interrupt(ctx context.Context) error
}

// TurnLoopConfig is the immutable configuration used to construct a TurnLoop.
type TurnLoopConfig struct {
	Session       SessionHandle
	Queue         *MessageQueue
	EventBus      *EventBus
	InitialPrompt string
}

// TurnLoop owns the single-goroutine drive loop for an agent runtime.
type TurnLoop struct {
	cfg TurnLoopConfig

	mu          sync.Mutex
	interruptCh chan struct{} // non-nil iff a turn is currently in flight
}

// NewTurnLoop returns a TurnLoop ready to Run.
func NewTurnLoop(cfg TurnLoopConfig) *TurnLoop {
	return &TurnLoop{cfg: cfg}
}

// Run drives the loop until ctx is cancelled. It executes the optional
// InitialPrompt verbatim (for child agents whose first turn is the spawn
// prompt), then alternates between draining the MessageQueue and blocking on
// its Signal channel. Returns ctx.Err() on shutdown after publishing
// EventStopped.
func (l *TurnLoop) Run(ctx context.Context) error {
	if l.cfg.InitialPrompt != "" {
		l.executeTurn(ctx, l.cfg.InitialPrompt)
	}

	for {
		// Prefer ctx cancellation over draining: don't start another turn if
		// shutdown is already requested.
		select {
		case <-ctx.Done():
			l.cfg.EventBus.Publish(RuntimeEvent{Type: EventStopped})
			return ctx.Err()
		default:
		}

		items := l.cfg.Queue.DrainAll()
		if len(items) > 0 {
			prompt := buildCompositePrompt(items)
			l.executeTurn(ctx, prompt)
			// Published regardless of turn outcome (success, failure, or
			// interrupt): the items were consumed from the queue and won't be
			// re-delivered, so subscribers tracking queue state need the
			// signal even on failure paths.
			l.cfg.EventBus.Publish(RuntimeEvent{Type: EventQueueDrained})
			continue
		}

		// Queue empty: block until a wakeup or shutdown.
		select {
		case <-ctx.Done():
			l.cfg.EventBus.Publish(RuntimeEvent{Type: EventStopped})
			return ctx.Err()
		case <-l.cfg.Queue.Signal():
		}
	}
}

// Interrupt requests interruption of the in-flight turn, if any. It is a
// no-op when no turn is currently running. The actual Session.Interrupt call
// is dispatched from the Run goroutine on the next select tick to avoid
// racing with a turn that is already completing.
func (l *TurnLoop) Interrupt(_ context.Context) error {
	l.mu.Lock()
	ch := l.interruptCh
	l.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case ch <- struct{}{}:
	default:
		// A pending interrupt is already queued; coalesce.
	}
	return nil
}

// executeTurn runs one turn end-to-end: install per-turn interrupt channel,
// publish TurnStarted, call StartTurn, drain events into the bus, and
// finalize with TurnCompleted/TurnFailed/Interrupted as appropriate.
func (l *TurnLoop) executeTurn(ctx context.Context, prompt string) {
	thisTurn := make(chan struct{}, 1)

	l.mu.Lock()
	l.interruptCh = thisTurn
	l.mu.Unlock()

	defer func() {
		l.mu.Lock()
		l.interruptCh = nil
		l.mu.Unlock()
	}()

	l.cfg.EventBus.Publish(RuntimeEvent{Type: EventTurnStarted, Prompt: prompt})

	events, err := l.cfg.Session.StartTurn(ctx, prompt)
	if err != nil {
		l.cfg.EventBus.Publish(RuntimeEvent{Type: EventTurnFailed, Error: err})
		return
	}

	interrupted := false
	for {
		select {
		case <-ctx.Done():
			// The backend's readTurn is also wired to ctx and will close
			// `events`; let the goroutine exit here without further bookkeeping.
			return
		case <-thisTurn:
			// Best-effort: errors from the backend's interrupt control
			// request are observable via the backend's own logging/observer.
			// The drain below still runs so we observe the terminal result.
			_ = l.cfg.Session.Interrupt(context.Background())
			interrupted = true
			// Continue draining until events closes so we observe the
			// terminal result message.
		case msg, ok := <-events:
			if !ok {
				return
			}
			l.cfg.EventBus.Publish(RuntimeEvent{Type: EventProtocolMessage, Message: msg})
			if msg.Type == "result" {
				var r protocol.ResultMessage
				_ = protocol.ParseAs(msg, &r)
				if interrupted {
					l.cfg.EventBus.Publish(RuntimeEvent{Type: EventInterrupted, Result: &r})
				} else {
					l.cfg.EventBus.Publish(RuntimeEvent{Type: EventTurnCompleted, Result: &r})
				}
			}
		}
	}
}

// buildCompositePrompt formats a queue drain into a single prompt string.
// One item is sent verbatim; multiple items are wrapped in an [inbox] header
// with priority-ordered, classed lines and a trailing instruction.
func buildCompositePrompt(items []QueueItem) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0].Prompt
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "[inbox] You have %d pending item(s):\n\n", len(items))
	for i, item := range items {
		fmt.Fprintf(&sb, "%d. [%s] %s\n", i+1, item.Class, item.Prompt)
	}
	sb.WriteString("\nContinue your current work unless a message tells you otherwise.\n")
	return sb.String()
}

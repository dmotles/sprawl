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
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// continuationPrompt is the synthetic, machine-originated nudge enqueued at
// turn-end when a run_in_background task completed mid-turn (QUM-640). The
// completed task's result is already present in the SDK's context as a
// tool_result; this prompt only grants weave a turn to review it and continue.
// It is terse and neutral to avoid steering the agent.
const continuationPrompt = "[auto-continue] A background task you started has completed. Review its output above and continue your work."

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
	// OnQueueItemDelivered, if non-nil, is invoked from the turn-loop goroutine
	// once per QueueItem successfully sent to the backend, and only for items
	// where len(EntryIDs) > 0. Used by the supervisor to call
	// agentloop.MarkDelivered so the on-disk pending→delivered rename
	// happens promptly. Must not block.
	//
	// Timing (QUM-579): the callback fires on the first protocol frame
	// received from the backend whose (Type, Subtype) is NOT
	// ("system", "init"). This proves the backend has actually begun
	// processing the prompt (rather than merely accepting the StartTurn
	// call). If the events channel closes without ever emitting such a
	// frame, the callback does NOT fire — the turn never made forward
	// progress and the queue items should remain in pending/ for the next
	// attempt. This supersedes QUM-544's "fire immediately after StartTurn
	// returns nil" timing, which marked items as delivered before any
	// evidence of backend processing.
	OnQueueItemDelivered func(item QueueItem)
	// PostTurnSweep, if non-nil, is invoked from the loop goroutine once per
	// turn boundary — after a successful turn, after a StartTurn error, after
	// an interrupted turn, AND after the InitialPrompt seed turn. It is
	// invoked AFTER EventQueueDrained is published so the bus event remains
	// the final lifecycle signal for the turn. Must not block. See QUM-580
	// (defense-in-depth post-turn pending-envelope sweep).
	PostTurnSweep func()
	// TurnTimeout, if > 0, bounds the wall-clock duration of a single turn
	// (StartTurn + drain loop). On deadline expiry the loop publishes
	// EventTurnFailed with an error wrapping context.DeadlineExceeded.
	// Zero means no deadline (legacy behaviour). See QUM-581.
	TurnTimeout time.Duration
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
		// InitialPrompt has no associated queue items (it's the spawn
		// prompt), so there's nothing for OnQueueItemDelivered to fire on.
		l.executeTurn(ctx, l.cfg.InitialPrompt, nil)
		if l.cfg.PostTurnSweep != nil {
			l.cfg.PostTurnSweep()
		}
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
			l.executeTurn(ctx, prompt, items)
			// Published regardless of turn outcome (success, failure, or
			// interrupt): the items were consumed from the queue and won't be
			// re-delivered, so subscribers tracking queue state need the
			// signal even on failure paths.
			l.cfg.EventBus.Publish(RuntimeEvent{Type: EventQueueDrained})
			if l.cfg.PostTurnSweep != nil {
				l.cfg.PostTurnSweep()
			}
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
// publish TurnStarted, call StartTurn, drain events into the bus, fire
// OnQueueItemDelivered on the first non-(system,init) frame, and finalize
// with TurnCompleted/TurnFailed/Interrupted as appropriate.
//
// QUM-579: OnQueueItemDelivered fires on the first protocol frame whose
// (Type, Subtype) is NOT ("system", "init"). If the events channel closes
// without ever yielding such a frame, the callback does not fire — the
// queue items stay in pending/ so they can be retried. This supersedes
// QUM-544's "fire immediately after StartTurn returns nil" timing, which
// marked items delivered before any evidence of backend processing.
//
// items may be nil for prompts that have no associated queue entries (e.g.
// the InitialPrompt).
func (l *TurnLoop) executeTurn(ctx context.Context, prompt string, items []QueueItem) {
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

	// QUM-581: bound the per-turn duration when configured. The wrapped
	// turnCtx is passed to StartTurn and to the drain loop's select so a
	// wedged-open stream (SDK opens system:init and never closes) cannot
	// freeze the agent. Outer-ctx cancellation (parent shutdown) is still
	// distinguished from a per-turn deadline below so that silent-shutdown
	// semantics are preserved for the former.
	turnCtx := ctx
	if l.cfg.TurnTimeout > 0 {
		var cancel context.CancelFunc
		turnCtx, cancel = context.WithTimeout(ctx, l.cfg.TurnTimeout)
		defer cancel()
	}

	events, err := l.cfg.Session.StartTurn(turnCtx, prompt)
	if err != nil {
		l.cfg.EventBus.Publish(RuntimeEvent{Type: EventTurnFailed, Error: err})
		return
	}

	delivered := false
	interrupted := false
	sawTaskNotification := false
	terminalPublished := false
	for {
		select {
		case <-turnCtx.Done():
			// Distinguish per-turn deadline expiry from outer-ctx cancellation.
			// Outer cancel = parent shutdown: stay silent (no TurnFailed).
			// Per-turn deadline = wedged-open stream: surface as TurnFailed
			// wrapping context.DeadlineExceeded so operators can see it and
			// downstream callers can detect it via errors.Is. See QUM-581.
			if ctx.Err() == nil && turnCtx.Err() == context.DeadlineExceeded {
				l.cfg.EventBus.Publish(RuntimeEvent{Type: EventTurnFailed, Error: fmt.Errorf("turn deadline exceeded after %s: %w", l.cfg.TurnTimeout, turnCtx.Err())})
				// QUM-618: best-effort bounded Interrupt to wind the SDK
				// down politely after a per-turn deadline. Session.Interrupt
				// is already internally bounded; use a bounded ctx so a
				// wedged wire send can't block this teardown path.
				ictx, icancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = l.cfg.Session.Interrupt(ictx)
				icancel()
			}
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
				// QUM-640: clean end-of-stream. If a run_in_background task
				// completed mid-turn (its task_notification surfaced in this
				// turn) and the turn was not interrupted, enqueue exactly one
				// synthetic continuation item so the existing
				// queue→turnloop→StartTurn drive fires a continuation turn,
				// which folds the stranded background result into context.
				// Detection is per-turn (local boolean), so a continuation turn
				// that sees no notification won't loop forever, and a
				// notification arriving during a continuation turn schedules a
				// further continuation. The !interrupted gate relies on the
				// <-thisTurn arm having set interrupted on an earlier iteration:
				// the backend delivers the terminal `result` frame before it
				// closes `events`, so the loop keeps iterating (and drains the
				// pending interrupt token) rather than reaching this close in
				// the same tick the interrupt arrives.
				if sawTaskNotification && !interrupted {
					l.cfg.Queue.Enqueue(QueueItem{Class: ClassContinuation, Prompt: continuationPrompt})
				}
				// QUM-647: channel-close safety net. If the events channel
				// closed without ever yielding a terminal `result` frame
				// (observed in the captured local_bash-interrupt transcript),
				// the loop must still publish a terminal event so downstream
				// finalize paths (TUI finalizeTurn, runtime InterruptCount
				// snapshot bookkeeping) can unblock. Coexists with the
				// QUM-640 continuation enqueue above: in the
				// (sawTaskNotification && !interrupted) branch, a TurnFailed
				// is still surfaced because we never saw a terminal frame,
				// and the continuation will drive a fresh turn afterwards.
				if !terminalPublished {
					if interrupted {
						l.cfg.EventBus.Publish(RuntimeEvent{Type: EventInterrupted})
					} else {
						l.cfg.EventBus.Publish(RuntimeEvent{Type: EventTurnFailed, Error: fmt.Errorf("stream closed without terminal result")})
					}
				}
				return
			}
			if msg.Type == "system" && msg.Subtype == "task_notification" {
				sawTaskNotification = true
			}
			// QUM-579: fire OnQueueItemDelivered on the first frame that
			// is NOT system:init. system:init is emitted by the backend
			// before it has actually started processing the prompt, so
			// firing on it would regress to QUM-544 semantics. Any other
			// frame (assistant, tool_use, result, etc.) proves the
			// backend is processing.
			if !delivered && (msg.Type != "system" || msg.Subtype != "init") {
				if l.cfg.OnQueueItemDelivered != nil {
					for _, it := range items {
						if len(it.EntryIDs) > 0 {
							l.cfg.OnQueueItemDelivered(it)
						}
					}
				}
				delivered = true
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
				terminalPublished = true
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

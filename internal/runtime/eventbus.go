// Package runtime provides building blocks for the unified agent runtime
// (see docs/designs/unified-runtime.md). The EventBus is the real-time
// streaming foundation: it fans out RuntimeEvents from a Claude subprocess
// to multiple subscribers (TUI viewport, activity ring, log writers).
//
// Backpressure: subscribers receive events on buffered channels via a
// near-non-blocking send — the publisher waits at most publishDeadline for
// a barely-keeping-up consumer to drain before dropping the event for that
// subscriber only. Drops are observable via DroppedCounts() and the
// structured DropTelemetry() snapshot (which is surfaced to the user via the
// TUI status bar; see internal/tui/statusbar.go).
//
// Warn emission is rate+burst limited per subscriber (QUM-681): a warn fires
// on the first drop, again whenever dropWarnInterval has elapsed since the
// previous warn, and again whenever dropWarnBurstThreshold drops have
// accumulated since the previous warn. This keeps a runaway slow subscriber
// from spamming the slog stream while still surfacing pathological bursts
// promptly.
package runtime

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// publishDeadline bounds how long Publish will wait on a single slow
// subscriber before dropping the event. It is short enough that a many-
// subscriber fanout with one stuck consumer still completes promptly, and
// long enough that a barely-keeping-up consumer (e.g., buffer=4 in tight
// burst) does not see spurious drops.
const publishDeadline = 1 * time.Millisecond

// QUM-681 drop-warn rate/burst gates and telemetry-clear interval. These
// constants are referenced by the TUI status bar (via a mirrored constant
// — see internal/tui/app.go's eventDropClearInterval) so the ⚠ segment
// auto-clears after a quiet period.
const (
	dropWarnInterval       = 5 * time.Second
	dropWarnBurstThreshold = uint64(10)
	dropClearInterval      = 30 * time.Second
)

// DropTelemetry is a per-subscriber snapshot of drop accounting surfaced to
// the TUI status bar (QUM-681). Cumulative is monotonic (matches DroppedCounts);
// LastDropAt is the time of the most recent drop, used by the status bar to
// decide when to clear the ⚠ segment once drops have been quiescent for
// dropClearInterval.
type DropTelemetry struct {
	Cumulative uint64
	LastDropAt time.Time
}

// RuntimeEventType discriminates entries published on the EventBus.
type RuntimeEventType int

const (
	// EventProtocolMessage carries a raw protocol.Message streamed from the backend.
	EventProtocolMessage RuntimeEventType = iota
	// EventTurnStarted is published when the runtime begins executing a turn.
	EventTurnStarted
	// EventTurnCompleted is published when a turn finishes with a result message.
	EventTurnCompleted
	// EventTurnFailed is published when a turn aborts due to an error.
	EventTurnFailed
	// EventInterrupted is published when an in-flight turn has been interrupted.
	EventInterrupted
	// EventQueueDrained is published when the between-turns queue has been drained.
	EventQueueDrained
	// EventStopped is published when the runtime has fully stopped.
	EventStopped
	// EventBackendFaulted is published when the underlying backend session
	// fires a sticky terminal error (e.g. ErrHangTimeout / ErrSubscriberWedged).
	// FaultClass and FaultNextAction are populated; Error carries the sentinel.
	// QUM-602.
	EventBackendFaulted
)

// RuntimeEvent is the unit of fan-out on the EventBus. The set of populated
// fields depends on Type; see the constants above.
type RuntimeEvent struct {
	Type    RuntimeEventType
	Message *protocol.Message
	Prompt  string
	Result  *protocol.ResultMessage
	Error   error
	// FaultClass and FaultNextAction are populated only for
	// EventBackendFaulted. FaultClass is a UX-visible classification of
	// the underlying terminal error (e.g. "HangTimeout"); FaultNextAction
	// is an operator-facing next-step hint. QUM-602.
	FaultClass      string
	FaultNextAction string
	// Seq is the publisher-stamped monotonic 1-indexed sequence number.
	// Populated by EventBus.Publish before fan-out (QUM-669). Subscribers
	// use gaps in Seq to detect dropped events.
	Seq uint64
}

// CurrentSeq returns the last seq value the bus has stamped. Returns 0
// before any Publish. (QUM-669.)
func (b *EventBus) CurrentSeq() uint64 {
	return b.seq.Load()
}

// SubscriberCount returns the number of currently-registered subscribers.
// QUM-727: surfaced through mcp__sprawl__status as the eventbus_subscribed
// boolean so stopped-but-leaking runtimes are visible.
func (b *EventBus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// subscriber tracks a single fan-out target. Each subscriber has its own
// buffered channel and drop accounting. Warn emission is rate+burst limited
// via lastWarnAt / lastWarnCount (QUM-681); lastDropAt anchors the status-bar
// clear interval.
type subscriber struct {
	name    string
	ch      chan RuntimeEvent
	dropped atomic.Uint64

	telMu         sync.Mutex
	lastWarnAt    time.Time
	lastWarnCount uint64
	lastDropAt    time.Time
}

// EventBus fans out RuntimeEvents to multiple subscribers without blocking
// publishers on slow consumers. Subscribers receive events on buffered
// channels; if a subscriber's buffer is full the event is dropped for that
// subscriber only.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[int]*subscriber
	nextID      int
	// publishMu serializes Publish (and PublishWithSeq) so "stamp Seq" and
	// "fan out to subscribers" happen atomically per event. Without this,
	// two concurrent Publish calls could stamp Seq=N and Seq=N+1 but deliver
	// them in reverse order to a subscriber — breaking the QUM-669
	// gap-detection invariant that subscribers see Seq values in strictly
	// ascending order. publishMu is held for the duration of the fan-out
	// (including the bounded per-subscriber publishDeadline wait), so a slow
	// subscriber cannot stall the publisher beyond the existing per-sub bound.
	publishMu sync.Mutex
	// now returns the wall clock used for drop-warn rate limiting and
	// telemetry timestamps. Defaults to time.Now; tests override via setNow.
	now func() time.Time
	// seq is the monotonic publisher-side sequence counter stamped onto each
	// RuntimeEvent before fan-out (QUM-669). 1-indexed (first Publish stamps 1).
	seq atomic.Uint64
}

// NewEventBus returns an empty EventBus ready for use.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[int]*subscriber),
		now:         time.Now,
	}
}

// PublishWithSeq fans out ev to subscribers with ev.Seq forcibly set to seq.
// Test-only. Lets tests deterministically produce a specific Seq sequence
// (including gaps) on the wire path without needing a slow-consumer race to
// provoke drops. Production code path is Publish(). QUM-669.
func (b *EventBus) PublishWithSeq(ev RuntimeEvent, seq uint64) {
	// Share publishMu with Publish so test injection cannot interleave with
	// real production Publish stamping. QUM-669.
	b.publishMu.Lock()
	defer b.publishMu.Unlock()
	ev.Seq = seq
	// Advance the bus's internal counter to max(current, seq) so subsequent
	// production Publish calls do not replay lower seq values.
	if cur := b.seq.Load(); seq > cur {
		b.seq.Store(seq)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for id, sub := range b.subscribers {
		if trySendWithYield(sub.ch, ev) {
			continue
		}
		sub.dropped.Add(1)
		now := b.now()
		sub.telMu.Lock()
		sub.lastDropAt = now
		total := sub.dropped.Load()
		delta := total - sub.lastWarnCount
		shouldWarn := sub.lastWarnAt.IsZero() ||
			now.Sub(sub.lastWarnAt) >= dropWarnInterval ||
			delta >= dropWarnBurstThreshold
		if shouldWarn {
			sub.lastWarnAt = now
			sub.lastWarnCount = total
		}
		sub.telMu.Unlock()
		if shouldWarn {
			slog.Default().Warn(
				"eventbus: dropping event for slow subscriber",
				slog.String("name", subscriberKey(sub.name, id)),
				slog.Int("buffer", cap(sub.ch)),
				slog.Uint64("cumulative", total),
				slog.Uint64("delta", delta),
			)
		}
	}
}

// setNow overrides the wall clock used for drop-warn rate limiting and
// telemetry timestamps. Test-only.
func (b *EventBus) setNow(fn func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if fn == nil {
		fn = time.Now
	}
	b.now = fn
}

// Subscribe registers a new anonymous subscriber and returns a receive-only
// channel for events plus an unsubscribe function. The returned channel has
// the requested buffer size; events that arrive while the buffer is full are
// dropped for this subscriber. The unsubscribe function removes the
// subscriber and closes the channel; it is safe to call multiple times.
func (b *EventBus) Subscribe(buffer int) (<-chan RuntimeEvent, func()) {
	return b.SubscribeNamed("", buffer)
}

// SubscribeNamed registers a new subscriber tagged with name and returns a
// receive-only channel for events plus an unsubscribe function. The name is
// used as the key in DroppedCounts() and in the first-drop warn log; pass
// an empty string for an anonymous subscriber (a synthetic "#<id>" key is
// used instead).
func (b *EventBus) SubscribeNamed(name string, buffer int) (<-chan RuntimeEvent, func()) {
	if buffer < 0 {
		buffer = 0
	}
	sub := &subscriber{
		name: name,
		ch:   make(chan RuntimeEvent, buffer),
	}

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subscribers[id] = sub
	b.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, id)
			b.mu.Unlock()
			// Closing after removal from the map guarantees Publish (which
			// only sends to channels still in the map under at least an
			// RLock) cannot send on a closed channel.
			close(sub.ch)
		})
	}
	return sub.ch, unsub
}

// Publish fans the event out to every current subscriber using a
// near-non-blocking send (bounded per subscriber by publishDeadline).
// With zero subscribers Publish is a no-op.
func (b *EventBus) Publish(event RuntimeEvent) {
	// QUM-669: serialize stamp+fanout so subscribers observe Seq in strictly
	// ascending order. See publishMu's doc comment for rationale.
	b.publishMu.Lock()
	defer b.publishMu.Unlock()
	event.Seq = b.seq.Add(1)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for id, sub := range b.subscribers {
		if trySendWithYield(sub.ch, event) {
			continue
		}
		// Slow subscriber: drop this event for them rather than block
		// other subscribers (or the publisher) on backpressure.
		sub.dropped.Add(1)
		now := b.now()
		sub.telMu.Lock()
		sub.lastDropAt = now
		total := sub.dropped.Load()
		delta := total - sub.lastWarnCount
		shouldWarn := sub.lastWarnAt.IsZero() ||
			now.Sub(sub.lastWarnAt) >= dropWarnInterval ||
			delta >= dropWarnBurstThreshold
		if shouldWarn {
			sub.lastWarnAt = now
			sub.lastWarnCount = total
		}
		sub.telMu.Unlock()
		if shouldWarn {
			slog.Default().Warn(
				"eventbus: dropping event for slow subscriber",
				slog.String("name", subscriberKey(sub.name, id)),
				slog.Int("buffer", cap(sub.ch)),
				slog.Uint64("cumulative", total),
				slog.Uint64("delta", delta),
			)
		}
	}
}

// DroppedCounts returns a snapshot of cumulative drop counts keyed by
// subscriber name (or a synthetic "#<id>" for anonymous subscribers).
// Unsubscribed subscribers are not present in the snapshot; callers that
// need the final count for a subscriber must read DroppedCounts before
// calling its unsubscribe function.
func (b *EventBus) DroppedCounts() map[string]uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]uint64, len(b.subscribers))
	for id, sub := range b.subscribers {
		out[subscriberKey(sub.name, id)] = sub.dropped.Load()
	}
	return out
}

// DropTelemetry returns a structured per-subscriber snapshot of cumulative
// drop count + last-drop timestamp keyed by subscriber name (or "#<id>" for
// anonymous subscribers). Pushed to the TUI status bar (QUM-681) on each
// mcpOpTickMsg so a slow subscriber surfaces a ⚠ segment promptly and the
// segment auto-clears once drops have been quiescent for dropClearInterval.
func (b *EventBus) DropTelemetry() map[string]DropTelemetry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]DropTelemetry, len(b.subscribers))
	for id, sub := range b.subscribers {
		sub.telMu.Lock()
		out[subscriberKey(sub.name, id)] = DropTelemetry{
			Cumulative: sub.dropped.Load(),
			LastDropAt: sub.lastDropAt,
		}
		sub.telMu.Unlock()
	}
	return out
}

// trySendWithYield attempts to deliver an event with bounded patience. A
// fast non-blocking send is tried first; if the buffer is full, the
// publisher waits up to publishDeadline for a barely-keeping-up consumer
// to make room. This preserves the "publisher cannot be stalled
// indefinitely by a slow subscriber" invariant while avoiding spurious
// drops when the consumer would have drained moments later.
func trySendWithYield(ch chan RuntimeEvent, event RuntimeEvent) bool {
	select {
	case ch <- event:
		return true
	default:
	}
	timer := time.NewTimer(publishDeadline)
	defer timer.Stop()
	select {
	case ch <- event:
		return true
	case <-timer.C:
		return false
	}
}

func subscriberKey(name string, id int) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf("#%d", id)
}

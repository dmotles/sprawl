// Package runtime provides building blocks for the unified agent runtime
// (see docs/designs/unified-runtime.md). The EventBus is the real-time
// streaming foundation: it fans out RuntimeEvents from a Claude subprocess
// to multiple subscribers (TUI viewport, activity ring, log writers).
//
// Backpressure: subscribers receive events on buffered channels via a
// near-non-blocking send — the publisher waits at most publishDeadline for
// a barely-keeping-up consumer to drain before dropping the event for that
// subscriber only. Drops are observable via DroppedCounts(); a one-shot
// warn is logged per subscriber on first drop.
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
)

// RuntimeEvent is the unit of fan-out on the EventBus. The set of populated
// fields depends on Type; see the constants above.
type RuntimeEvent struct {
	Type    RuntimeEventType
	Message *protocol.Message
	Prompt  string
	Result  *protocol.ResultMessage
	Error   error
}

// subscriber tracks a single fan-out target. Each subscriber has its own
// buffered channel, drop counter, and one-shot warn flag.
type subscriber struct {
	name    string
	ch      chan RuntimeEvent
	dropped atomic.Uint64
	warned  atomic.Bool
}

// EventBus fans out RuntimeEvents to multiple subscribers without blocking
// publishers on slow consumers. Subscribers receive events on buffered
// channels; if a subscriber's buffer is full the event is dropped for that
// subscriber only.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[int]*subscriber
	nextID      int
}

// NewEventBus returns an empty EventBus ready for use.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[int]*subscriber),
	}
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
	b.mu.RLock()
	defer b.mu.RUnlock()
	for id, sub := range b.subscribers {
		if trySendWithYield(sub.ch, event) {
			continue
		}
		// Slow subscriber: drop this event for them rather than block
		// other subscribers (or the publisher) on backpressure.
		sub.dropped.Add(1)
		if sub.warned.CompareAndSwap(false, true) {
			slog.Default().Warn(
				"eventbus: dropping event for slow subscriber",
				slog.String("name", subscriberKey(sub.name, id)),
				slog.Int("buffer", cap(sub.ch)),
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

// Package runtime provides building blocks for the unified agent runtime
// (see docs/designs/unified-runtime.md). The EventBus is the real-time
// streaming foundation: it fans out RuntimeEvents from a Claude subprocess
// to multiple subscribers (TUI viewport, activity ring, log writers).
package runtime

import (
	"sync"

	"github.com/dmotles/sprawl/internal/protocol"
)

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

// EventBus fans out RuntimeEvents to multiple subscribers without blocking
// publishers on slow consumers. Subscribers receive events on buffered
// channels; if a subscriber's buffer is full the event is dropped for that
// subscriber only.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[int]chan RuntimeEvent
	nextID      int
}

// NewEventBus returns an empty EventBus ready for use.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[int]chan RuntimeEvent),
	}
}

// Subscribe registers a new subscriber and returns a receive-only channel
// for events plus an unsubscribe function. The returned channel has the
// requested buffer size; events that arrive while the buffer is full are
// dropped for this subscriber. The unsubscribe function removes the
// subscriber and closes the channel; it is safe to call multiple times.
func (b *EventBus) Subscribe(buffer int) (<-chan RuntimeEvent, func()) {
	if buffer < 0 {
		buffer = 0
	}
	ch := make(chan RuntimeEvent, buffer)

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subscribers[id] = ch
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
			close(ch)
		})
	}
	return ch, unsub
}

// Publish fans the event out to every current subscriber using a
// non-blocking send. With zero subscribers Publish is a no-op.
func (b *EventBus) Publish(event RuntimeEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
			// Slow subscriber: drop this event for them rather than block
			// other subscribers (or the publisher) on backpressure.
		}
	}
}

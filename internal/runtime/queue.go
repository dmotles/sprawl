// Package runtime provides the unified agent runtime building blocks.
//
// The MessageQueue is the between-turns work queue: callers enqueue items
// representing user input, async messages from peers, inbox notifications,
// or interrupts, and the turn loop drains the queue to compose the next
// prompt. See docs/designs/unified-runtime.md section 3.3.
package runtime

import (
	"sort"
	"sync"
)

// Class names recognized by the priority ordering. Items with any other
// class are accepted but sort after all known classes.
const (
	ClassInterrupt = "interrupt"
	ClassTask      = "task"
	ClassUser      = "user"
	ClassAsync     = "async"
	ClassInbox     = "inbox"
)

// classPriority returns the sort key for a class. Lower = higher priority.
// Unknown classes get a key larger than any known class so they sort last
// without panicking.
func classPriority(class string) int {
	switch class {
	case ClassInterrupt:
		return 0
	case ClassTask:
		return 1
	case ClassUser:
		return 2
	case ClassAsync:
		return 3
	case ClassInbox:
		return 4
	default:
		return 5
	}
}

// QueueItem is a single piece of between-turns work. EntryIDs records the
// underlying persistent-storage IDs (e.g. async message IDs) so the caller
// can clean up after the item has been delivered to Claude.
type QueueItem struct {
	Class    string
	Prompt   string
	EntryIDs []string
}

// MessageQueue is a thread-safe FIFO-within-class priority queue that
// signals waiters when work arrives.
//
// The signal channel has buffer size 1: many enqueues between drains
// produce a single wakeup. DrainAll resets the signal so the next enqueue
// will fire it again.
type MessageQueue struct {
	mu     sync.Mutex
	items  []QueueItem
	signal chan struct{}
}

// NewMessageQueue returns an empty queue ready for use.
func NewMessageQueue() *MessageQueue {
	return &MessageQueue{
		signal: make(chan struct{}, 1),
	}
}

// Enqueue appends item and pokes the signal channel (non-blocking).
func (q *MessageQueue) Enqueue(item QueueItem) {
	q.mu.Lock()
	q.items = append(q.items, item)
	q.mu.Unlock()

	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// DrainAll returns all queued items sorted by priority (interrupt > task >
// user > async > inbox), preserving FIFO order within each class. The
// queue is left empty and the signal is reset so the next enqueue fires a
// fresh wakeup.
func (q *MessageQueue) DrainAll() []QueueItem {
	q.mu.Lock()
	items := q.items
	q.items = nil
	q.mu.Unlock()

	// Reset signal: drop any pending wakeup so the turn loop will block
	// until the next Enqueue.
	select {
	case <-q.signal:
	default:
	}

	if len(items) == 0 {
		return nil
	}

	sort.SliceStable(items, func(i, j int) bool {
		return classPriority(items[i].Class) < classPriority(items[j].Class)
	})
	return items
}

// Signal returns a channel that fires (non-blocking, buffered(1)) when
// work is enqueued. Callers should re-select on it after each DrainAll.
func (q *MessageQueue) Signal() <-chan struct{} {
	return q.signal
}

// Len returns the current queue depth.
func (q *MessageQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

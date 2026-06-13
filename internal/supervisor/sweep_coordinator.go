package supervisor

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/protocol"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
)

// sweepMessagesReadToolName is the MCP tool name the delivery-confirmation
// subscriber watches for. Observing this tool_use block confirms the agent
// drained its inbox during the current turn, which suppresses the
// defense-in-depth wake from postTurnSweep. See QUM-580.
const sweepMessagesReadToolName = "mcp__sprawl__messages_read"

// sweepCoordinator owns the QUM-580 post-turn pending-envelope sweep state and
// the callbacks invoked by the turn loop (OnQueueItemDelivered, PostTurnSweep)
// and by the delivery-confirmation EventBus subscriber.
//
// QUM-584: extracted from unifiedHandle so the closures stored in
// runtimepkg.RuntimeConfig capture only *sweepCoordinator, which is fully
// constructed BEFORE runtimepkg.New(...) is called. This makes closure-capture
// races on partially-built unifiedHandle fields impossible by construction:
// the runtime callbacks no longer reach into the handle to find the runtime
// pointer, sweep counters, or wake function.
//
// The single ordering constraint that remains is the post-construction Bind
// call which installs the wake function. Bind must happen before rt.Start so
// the first PostTurnSweep firing has a non-nil wake. If Bind is forgotten or
// reordered, sweep silently no-ops (degrades gracefully — does not panic).
type sweepCoordinator struct {
	sprawlRoot string
	name       string

	mu              sync.Mutex
	deliveredItems  int
	sawMessagesRead bool
	// wake is installed via Bind after the runtime + handle are fully
	// constructed. Read under mu. Nil before Bind; sweep no-ops if nil.
	wake func() error
}

// newSweepCoordinator constructs a coordinator with no wake function bound.
// Call Bind(...) after the unifiedHandle is fully assembled but before
// rt.Start so the first PostTurnSweep firing has a non-nil wake.
func newSweepCoordinator(sprawlRoot, name string) *sweepCoordinator {
	return &sweepCoordinator{sprawlRoot: sprawlRoot, name: name}
}

// Bind installs the wake function invoked when postTurnSweep decides a wake
// is needed. Must be called once, after the runtime is constructed and the
// containing handle is fully populated, but before rt.Start.
func (c *sweepCoordinator) Bind(wake func() error) {
	c.mu.Lock()
	c.wake = wake
	c.mu.Unlock()
}

// OnDelivered is forwarded to runtimepkg.RuntimeConfig.OnDelivered (QUM-817).
// It fires when a written stdin user message is confirmed consumed by its
// isReplay echo, carrying the message's entryIDs. It increments the per-turn
// delivered counter (once per delivered message) and flips each delivered
// entry's on-disk state: task: prefixes mark the task as done; other entry IDs
// are passed to agentloop.MarkDelivered.
func (c *sweepCoordinator) OnDelivered(entryIDs []string) {
	if len(entryIDs) > 0 {
		c.mu.Lock()
		c.deliveredItems++
		c.mu.Unlock()
	}
	for _, id := range entryIDs {
		if strings.HasPrefix(id, "task:") {
			taskID := strings.TrimPrefix(id, "task:")
			found, err := state.GetTask(c.sprawlRoot, c.name, taskID)
			if err != nil {
				slog.Default().Warn(
					"unified-runtime: get task on delivery failed",
					slog.String("agent", c.name),
					slog.String("task_id", taskID),
					slog.Any("err", err),
				)
				continue
			}
			found.Status = "done"
			found.DoneAt = time.Now().UTC().Format(time.RFC3339)
			if err := state.UpdateTask(c.sprawlRoot, c.name, found); err != nil {
				slog.Default().Warn(
					"unified-runtime: mark task done failed",
					slog.String("agent", c.name),
					slog.String("task_id", taskID),
					slog.Any("err", err),
				)
			}
			continue
		}
		if err := agentloop.MarkDelivered(c.sprawlRoot, c.name, id); err != nil {
			slog.Default().Warn(
				"unified-runtime: mark delivered failed",
				slog.String("agent", c.name),
				slog.String("entry_id", id),
				slog.Any("err", err),
			)
		}
	}
}

// PostTurnSweep is the defense-in-depth check invoked from the turn loop after
// every turn boundary (QUM-580). It decides whether to fire a cooperative wake
// based on two conditions:
//
//  1. deliveredItems > 0 && !sawMessagesRead — items were handed to the model
//     this turn, but the model never invoked messages_read to drain them. The
//     model may have ignored the inbox; nudge it on the next boundary so a
//     follow-up turn happens.
//  2. len(pending/) > 0 — the on-disk pending queue is non-empty, regardless
//     of in-memory counters. A pending file may have been written by a peer
//     mid-turn and never drained into the runtime queue; this is the canonical
//     defense-in-depth path.
//
// Counters are reset under mu before any blocking I/O so the next turn starts
// clean. wake is invoked without mu held.
func (c *sweepCoordinator) PostTurnSweep() {
	c.mu.Lock()
	delivered := c.deliveredItems
	sawRead := c.sawMessagesRead
	c.deliveredItems = 0
	c.sawMessagesRead = false
	wake := c.wake
	c.mu.Unlock()

	needWake := delivered > 0 && !sawRead
	if !needWake {
		pending, err := agentloop.ListPending(c.sprawlRoot, c.name)
		if err != nil {
			slog.Default().Debug(
				"unified-runtime: postTurnSweep ListPending failed",
				slog.String("agent", c.name),
				slog.Any("err", err),
			)
		}
		if len(pending) > 0 {
			needWake = true
		}
	}
	if !needWake {
		return
	}
	if wake != nil {
		_ = wake()
	}
}

// OnTurnStarted resets both sweep counters. Invoked by
// runDeliveryConfirmationSubscriber on each EventTurnStarted so every turn
// begins with a clean slate.
func (c *sweepCoordinator) OnTurnStarted() {
	c.mu.Lock()
	c.deliveredItems = 0
	c.sawMessagesRead = false
	c.mu.Unlock()
}

// markSawMessagesRead flips sawMessagesRead=true. Called by the
// delivery-confirmation subscriber on observing a tool_use block invoking
// mcp__sprawl__messages_read.
func (c *sweepCoordinator) markSawMessagesRead() {
	c.mu.Lock()
	c.sawMessagesRead = true
	c.mu.Unlock()
}

// runDeliveryConfirmationSubscriber subscribes to bus and watches the agent's
// protocol-message stream for two signals:
//
//   - EventTurnStarted: resets both sweep counters on the coordinator so each
//     turn starts with a clean slate.
//   - EventProtocolMessage carrying an assistant tool_use block with
//     name == sweepMessagesReadToolName: marks the coordinator's
//     sawMessagesRead=true, confirming the model drained its inbox this turn.
//
// The returned function unsubscribes and waits for the goroutine to drain.
// Parsing follows the same assistant tool_use shape used by
// internal/agentloop/activity.go. QUM-583 considered factoring this into a
// shared pre-decoded ParsedEvent fanned out by the EventBus, but rejected it:
// the two consumers extract different fields (activity needs text + tool
// input; this subscriber needs only the tool name) and sharing would require
// plumbing a new event type through the EventBus and both subscribers for a
// per-event saving of one small JSON unmarshal — not worth the coupling.
func runDeliveryConfirmationSubscriber(bus *runtimepkg.EventBus, c *sweepCoordinator, name string) func() {
	ch, unsub := bus.SubscribeNamed(name, 64)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for ev := range ch {
			switch ev.Type {
			case runtimepkg.EventTurnStarted:
				c.OnTurnStarted()
			case runtimepkg.EventProtocolMessage:
				if ev.Message == nil || ev.Message.Type != "assistant" {
					continue
				}
				if isMessagesReadToolUse(ev.Message) {
					c.markSawMessagesRead()
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			unsub()
			<-doneCh
		})
	}
}

// isMessagesReadToolUse reports whether the assistant protocol message
// contains a tool_use block invoking mcp__sprawl__messages_read.
func isMessagesReadToolUse(msg *protocol.Message) bool {
	var outer struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name,omitempty"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(msg.Raw, &outer); err != nil {
		return false
	}
	for _, block := range outer.Message.Content {
		if block.Type == "tool_use" && block.Name == sweepMessagesReadToolName {
			return true
		}
	}
	return false
}

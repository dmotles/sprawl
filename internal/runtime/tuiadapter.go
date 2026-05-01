// TUIAdapter wraps a UnifiedRuntime so a Bubble Tea model can drive its
// lifecycle and consume its event stream as tea.Msg values. See QUM-397
// (docs/designs/unified-runtime.md section 5).
//
// The adapter is intentionally a thin translation layer: it owns one
// EventBus subscription and converts each RuntimeEvent it receives into the
// existing tui.* tea.Msg types so the AppModel can stay unchanged.

package runtime

import (
	"context"
	"io"
	"sync"

	tea "charm.land/bubbletea/v2"

	tui "github.com/dmotles/sprawl/internal/tui"
)

// adapterEventBufferSize is the per-subscription buffer used by the adapter.
// Sized generously so a TUI render hiccup doesn't drop content blocks.
const adapterEventBufferSize = 64

// TUIAdapter exposes a UnifiedRuntime as bubbletea-friendly tea.Cmd values.
type TUIAdapter struct {
	mu          sync.Mutex
	runtime     *UnifiedRuntime
	events      <-chan RuntimeEvent
	unsubscribe func()
	cancelled   bool
}

// NewTUIAdapter subscribes to the runtime's EventBus and returns an adapter
// ready for use by a Bubble Tea program.
func NewTUIAdapter(rt *UnifiedRuntime) *TUIAdapter {
	a := &TUIAdapter{runtime: rt}
	a.subscribe(rt)
	return a
}

// subscribe registers a fresh subscription against rt. Caller must hold a.mu
// or otherwise serialize access.
func (a *TUIAdapter) subscribe(rt *UnifiedRuntime) {
	ch, unsub := rt.EventBus().Subscribe(adapterEventBufferSize)
	a.events = ch
	a.unsubscribe = unsub
}

// Initialize returns a tea.Cmd that starts the underlying runtime. On
// success the command yields tui.SessionInitializedMsg; on error,
// tui.SessionErrorMsg.
func (a *TUIAdapter) Initialize() tea.Cmd {
	return func() tea.Msg {
		a.mu.Lock()
		rt := a.runtime
		a.mu.Unlock()
		if err := rt.Start(context.Background()); err != nil {
			return tui.SessionErrorMsg{Err: err}
		}
		return tui.SessionInitializedMsg{}
	}
}

// WaitForEvent returns a tea.Cmd that blocks on the next runtime event and
// converts it to a tea.Msg. Lifecycle events that have no TUI analogue
// (turn-started, queue-drained, stopped) are skipped — the command loops and
// reads the next event so the model only ever observes user-visible msgs.
func (a *TUIAdapter) WaitForEvent() tea.Cmd {
	return func() tea.Msg {
		for {
			a.mu.Lock()
			ch := a.events
			cancelled := a.cancelled
			a.mu.Unlock()

			if cancelled || ch == nil {
				return tui.SessionErrorMsg{Err: io.EOF}
			}

			ev, ok := <-ch
			if !ok {
				return tui.SessionErrorMsg{Err: io.EOF}
			}

			switch ev.Type {
			case EventProtocolMessage:
				if ev.Message == nil {
					continue
				}
				msg := tui.MapProtocolMessage(ev.Message)
				if msg == nil {
					continue
				}
				return msg
			case EventTurnCompleted:
				if ev.Result == nil {
					return tui.SessionResultMsg{}
				}
				return tui.SessionResultMsg{
					Result:       ev.Result.Result,
					IsError:      ev.Result.IsError,
					DurationMs:   ev.Result.DurationMs,
					NumTurns:     ev.Result.NumTurns,
					TotalCostUsd: ev.Result.TotalCostUsd,
				}
			case EventTurnFailed:
				var errStr string
				if ev.Error != nil {
					errStr = ev.Error.Error()
				}
				return tui.SessionResultMsg{
					IsError: true,
					Result:  errStr,
				}
			case EventInterrupted:
				return tui.InterruptResultMsg{Err: nil}
			case EventTurnStarted, EventQueueDrained, EventStopped:
				// Skip lifecycle-only events — read the next one.
				continue
			default:
				continue
			}
		}
	}
}

// SendMessage enqueues a user-class queue item and returns a
// tui.UserMessageSentMsg. The actual prompt delivery happens later when the
// turn loop pulls from the queue.
func (a *TUIAdapter) SendMessage(text string) tea.Cmd {
	return func() tea.Msg {
		a.mu.Lock()
		rt := a.runtime
		a.mu.Unlock()
		rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: text})
		return tui.UserMessageSentMsg{}
	}
}

// Interrupt requests an interrupt of the current turn. The result is wrapped
// in tui.InterruptResultMsg so the model can decide how to render it.
func (a *TUIAdapter) Interrupt() tea.Cmd {
	return func() tea.Msg {
		a.mu.Lock()
		rt := a.runtime
		a.mu.Unlock()
		err := rt.Interrupt(context.Background())
		return tui.InterruptResultMsg{Err: err}
	}
}

// Cancel removes the adapter's subscription from the runtime EventBus.
// Idempotent.
func (a *TUIAdapter) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancelled {
		return
	}
	a.cancelled = true
	if a.unsubscribe != nil {
		a.unsubscribe()
		a.unsubscribe = nil
	}
	a.events = nil
}

// SessionID delegates to the underlying runtime.
func (a *TUIAdapter) SessionID() string {
	a.mu.Lock()
	rt := a.runtime
	a.mu.Unlock()
	if rt == nil {
		return ""
	}
	return rt.SessionID()
}

// Observe swaps the adapter's observed runtime. The previous subscription is
// torn down and a fresh one is registered against rt. Used when a session
// restart yields a new UnifiedRuntime instance and the AppModel wants the
// existing adapter to follow it.
func (a *TUIAdapter) Observe(rt *UnifiedRuntime) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unsubscribe != nil {
		a.unsubscribe()
		a.unsubscribe = nil
	}
	a.runtime = rt
	a.cancelled = false
	if rt != nil {
		ch, unsub := rt.EventBus().Subscribe(adapterEventBufferSize)
		a.events = ch
		a.unsubscribe = unsub
	} else {
		a.events = nil
	}
}

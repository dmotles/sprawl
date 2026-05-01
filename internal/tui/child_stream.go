// Per-child viewport streaming adapter for QUM-439.
//
// The TUI subscribes to a child agent's UnifiedRuntime EventBus and
// translates RuntimeEvents into tea.Msg values routed to the per-child
// AgentBuffer. The adapter is intentionally a small in-package type (not
// a reuse of internal/tuiruntime.TUIAdapter) to avoid an import cycle:
// tuiruntime already imports this package for tea.Msg types.

package tui

import (
	"io"
	"sync"

	tea "charm.land/bubbletea/v2"

	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
)

// childStreamAdapterBufferSize is the per-subscription buffer used by the
// child viewport adapter. Sized generously so a TUI render hiccup does not
// drop assistant content blocks.
const childStreamAdapterBufferSize = 64

// ChildStreamAdapter (the in-package child-viewport variant) wraps a UnifiedRuntime's
// EventBus subscription so the AppModel can stream child-agent activity into
// the per-agent buffer. Cancel() is idempotent; Observe(rt) replaces the
// current subscription with a fresh one (tearing the prior down).
type ChildStreamAdapter struct {
	mu          sync.Mutex
	rt          *sprawlrt.UnifiedRuntime
	events      <-chan sprawlrt.RuntimeEvent
	unsubscribe func()
	cancelled   bool
	epoch       uint64
}

// NewChildStreamAdapter creates an adapter bound to rt and registers an EventBus
// subscription.
func NewChildStreamAdapter(rt *sprawlrt.UnifiedRuntime) *ChildStreamAdapter {
	a := &ChildStreamAdapter{rt: rt}
	if rt != nil {
		a.subscribe(rt)
	}
	return a
}

// subscribe registers a fresh subscription against rt. Caller must hold a.mu.
func (a *ChildStreamAdapter) subscribe(rt *sprawlrt.UnifiedRuntime) {
	ch, unsub := rt.EventBus().Subscribe(childStreamAdapterBufferSize)
	a.events = ch
	a.unsubscribe = unsub
	a.epoch++
}

// Observe swaps the adapter to a new runtime, tearing down the previous
// subscription.
func (a *ChildStreamAdapter) Observe(rt *sprawlrt.UnifiedRuntime) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unsubscribe != nil {
		a.unsubscribe()
		a.unsubscribe = nil
	}
	a.rt = rt
	a.cancelled = false
	if rt != nil {
		a.subscribe(rt)
	} else {
		a.events = nil
	}
}

// Cancel removes the adapter's subscription. Idempotent.
func (a *ChildStreamAdapter) Cancel() {
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

// WaitForEvent returns a tea.Cmd that blocks on the next runtime event and
// returns it as a tea.Msg suitable for routing to a child viewport. Returns
// SessionErrorMsg{io.EOF} once the subscription is cancelled and the channel
// drained.
func (a *ChildStreamAdapter) WaitForEvent() tea.Cmd {
	return func() tea.Msg {
		for {
			a.mu.Lock()
			ch := a.events
			cancelled := a.cancelled
			epochAtRead := a.epoch
			a.mu.Unlock()

			if cancelled || ch == nil {
				return SessionErrorMsg{Err: io.EOF}
			}

			ev, ok := <-ch
			if !ok {
				a.mu.Lock()
				swapped := a.epoch != epochAtRead && !a.cancelled && a.events != nil
				a.mu.Unlock()
				if swapped {
					continue
				}
				return SessionErrorMsg{Err: io.EOF}
			}

			switch ev.Type {
			case sprawlrt.EventProtocolMessage:
				if ev.Message == nil {
					continue
				}
				if ev.Message.Type == "result" {
					continue
				}
				msg := MapProtocolMessage(ev.Message)
				if msg == nil {
					continue
				}
				return msg
			case sprawlrt.EventTurnCompleted:
				if ev.Result == nil {
					return SessionResultMsg{}
				}
				return SessionResultMsg{
					Result:       ev.Result.Result,
					IsError:      ev.Result.IsError,
					DurationMs:   ev.Result.DurationMs,
					NumTurns:     ev.Result.NumTurns,
					TotalCostUsd: ev.Result.TotalCostUsd,
				}
			case sprawlrt.EventTurnFailed:
				var errStr string
				if ev.Error != nil {
					errStr = ev.Error.Error()
				}
				return SessionResultMsg{IsError: true, Result: errStr}
			case sprawlrt.EventInterrupted:
				return InterruptResultMsg{Err: nil}
			case sprawlrt.EventTurnStarted, sprawlrt.EventQueueDrained, sprawlrt.EventStopped:
				continue
			default:
				continue
			}
		}
	}
}

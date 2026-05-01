// Per-agent activity-panel streaming adapter for QUM-440.
//
// The TUI subscribes to an agent's UnifiedRuntime EventBus and translates
// EventProtocolMessage events into ActivityStreamMsg envelopes carrying
// []agentloop.ActivityEntry slices, so the activity panel can update in real
// time instead of polling activity.ndjson every 2s. Mirrors ChildStreamAdapter
// (QUM-439) — same Observe/Cancel/Wait/Epoch surface — but its translator
// emits panel-level entries rather than viewport tea.Msg values.

package tui

import (
	"io"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/agentloop"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
)

// activityStreamAdapterBufferSize is the per-subscription buffer used by the
// activity-panel adapter. Sized generously so a render hiccup does not drop
// activity entries.
const activityStreamAdapterBufferSize = 64

// ActivityStreamAdapter wraps a UnifiedRuntime's EventBus subscription and
// translates EventProtocolMessage events into []agentloop.ActivityEntry slices
// suitable for the activity panel. Cancel() is idempotent; Observe(rt) replaces
// the current subscription with a fresh one (tearing down the prior).
type ActivityStreamAdapter struct {
	mu          sync.Mutex
	rt          *sprawlrt.UnifiedRuntime
	events      <-chan sprawlrt.RuntimeEvent
	unsubscribe func()
	cancelled   bool
	epoch       uint64
}

// NewActivityStreamAdapter creates an adapter bound to rt and registers an
// EventBus subscription.
func NewActivityStreamAdapter(rt *sprawlrt.UnifiedRuntime) *ActivityStreamAdapter {
	a := &ActivityStreamAdapter{rt: rt}
	if rt != nil {
		a.subscribe(rt)
	}
	return a
}

// subscribe registers a fresh subscription against rt. Caller must hold a.mu
// (or be in the constructor).
func (a *ActivityStreamAdapter) subscribe(rt *sprawlrt.UnifiedRuntime) {
	ch, unsub := rt.EventBus().Subscribe(activityStreamAdapterBufferSize)
	a.events = ch
	a.unsubscribe = unsub
	a.epoch++
}

// Observe swaps the adapter to a new runtime, tearing down the previous
// subscription.
func (a *ActivityStreamAdapter) Observe(rt *sprawlrt.UnifiedRuntime) {
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
func (a *ActivityStreamAdapter) Cancel() {
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

// Epoch returns the current adapter generation counter; bumped on every
// subscribe/Observe so stale ActivityStreamMsg deliveries can be filtered.
func (a *ActivityStreamAdapter) Epoch() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.epoch
}

// WaitForEvent returns a tea.Cmd that blocks on the next runtime event and
// returns it as an ActivityStreamMsg-bearing tea.Msg. Returns
// SessionErrorMsg{io.EOF} once the subscription is cancelled and the channel
// drained. Non-protocol events are skipped silently.
func (a *ActivityStreamAdapter) WaitForEvent() tea.Cmd {
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

			if ev.Type != sprawlrt.EventProtocolMessage || ev.Message == nil {
				continue
			}

			// Reuse agentloop.RecordMessage to derive ActivityEntry values:
			// a single private ring with no writer captures the entries the
			// canonical mapper would produce, then we Tail them back out.
			ring := agentloop.NewActivityRing(agentloop.DefaultActivityCapacity, nil)
			ring.RecordMessage(ev.Message, nil)
			entries := ring.Tail(agentloop.DefaultActivityCapacity)
			if len(entries) == 0 {
				continue
			}
			return ActivityStreamMsg{Epoch: epochAtRead, Entries: entries}
		}
	}
}

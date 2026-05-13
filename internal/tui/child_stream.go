// Per-child viewport streaming adapter for QUM-439.
//
// The TUI subscribes to a child agent's UnifiedRuntime EventBus and
// translates RuntimeEvents into tea.Msg values routed to the per-child
// AgentBuffer. The adapter is intentionally a small in-package type (not
// a reuse of internal/tuiruntime.TUIAdapter) to avoid an import cycle:
// tuiruntime already imports this package for tea.Msg types.

package tui

import (
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
	done        <-chan struct{}
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
	a.done = rt.Done()
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
		a.done = nil
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
	a.done = nil
}

// WaitForEvent returns a tea.Cmd that blocks on the next runtime event and
// returns it as a tea.Msg suitable for routing to a child viewport. Returns
// ChildStreamClosedMsg once the subscription is cancelled and the channel
// drained (QUM-479: dedicated sentinel — must NOT use SessionErrorMsg{io.EOF},
// which is reserved for the bridge adapter).
func (a *ChildStreamAdapter) WaitForEvent() tea.Cmd {
	return func() tea.Msg {
		for {
			a.mu.Lock()
			ch := a.events
			done := a.done
			cancelled := a.cancelled
			epochAtRead := a.epoch
			a.mu.Unlock()

			if cancelled || ch == nil {
				return ChildStreamClosedMsg{Epoch: epochAtRead}
			}

			var (
				ev sprawlrt.RuntimeEvent
				ok bool
			)
			select {
			case ev, ok = <-ch:
			case <-done:
				// QUM-479: runtime stopped — surface the closed sentinel
				// rather than block forever.
				return ChildStreamClosedMsg{Epoch: epochAtRead}
			}
			if !ok {
				a.mu.Lock()
				swapped := a.epoch != epochAtRead && !a.cancelled && a.events != nil
				a.mu.Unlock()
				if swapped {
					continue
				}
				return ChildStreamClosedMsg{Epoch: epochAtRead}
			}

			if msg := TranslateRuntimeEvent(ev, InterruptedAsResult); msg != nil {
				return msg
			}
		}
	}
}

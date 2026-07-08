// TUIAdapter wraps a UnifiedRuntime so a Bubble Tea model can drive its
// lifecycle and consume its event stream as tea.Msg values. See QUM-397
// (docs/designs/unified-runtime.md section 5).
//
// The adapter is intentionally a thin translation layer: it owns one
// EventBus subscription and converts each RuntimeEvent it receives into the
// existing tui.* tea.Msg types so the AppModel can stay unchanged.

package tuiruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/attach"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
	tui "github.com/dmotles/sprawl/internal/tui"
)

// attachErrorToastTTL is how long a `/attach` local-validation error toast
// stays visible before auto-dismissing (QUM-860).
const attachErrorToastTTL = 6 * time.Second

// debugGapInjectEnv is a TEST-ONLY debug seam used by the QUM-669
// `viewport-resync` e2e matrix row. If set to a positive uint64 N, the
// adapter synthesizes one EventDropDetectedMsg{Missing: N} at the second
// event of the current subscription, exercising the wedge-recovery path
// end-to-end without needing to race a real slow subscriber. The synthesized
// gap is one-shot per subscription (cleared after firing). This is NOT a
// user-facing surface — do not document it outside the design doc / matrix
// row script.
const debugGapInjectEnv = "SPRAWL_DEBUG_GAP_INJECT"

// adapterEventBufferSize is the per-subscription buffer used by the adapter.
// Sized generously so a TUI render hiccup doesn't drop content blocks.
const adapterEventBufferSize = 64

// ErrNoRuntime is returned by adapter operations invoked when the adapter has
// no observed runtime (e.g. after Observe(nil)). Callers should call
// Observe(rt) first. (QUM-436)
var ErrNoRuntime = errors.New("tuiadapter: no runtime; call Observe(rt) first")

// Compile-time assertion that *TUIAdapter satisfies tui.SessionBackend so
// cmd/enter.go can return it directly to AppModel.
var _ tui.SessionBackend = (*TUIAdapter)(nil)

// TUIAdapter exposes a UnifiedRuntime as bubbletea-friendly tea.Cmd values.
type TUIAdapter struct {
	mu          sync.Mutex
	runtime     *sprawlrt.UnifiedRuntime
	events      <-chan sprawlrt.RuntimeEvent
	unsubscribe func()
	cancelled   bool
	// epoch is bumped each time a fresh subscription is installed (initial
	// subscribe + each successful Observe swap). WaitForEvent uses it to tell
	// an Observe-driven channel close apart from a real EOF. (QUM-436 Item 2)
	epoch uint64
	// lastSeq tracks the last RuntimeEvent.Seq the adapter delivered to the
	// TUI. Zero is a sentinel meaning "no event observed yet on the current
	// subscription"; the first event after subscribe (or Observe swap) never
	// triggers a gap msg. (QUM-669 §2.2)
	lastSeq uint64
	// pendingMsg holds the translated tea.Msg for an event that detected a
	// gap. The gap notice (EventDropDetectedMsg) is returned first; the next
	// WaitForEvent call drains pendingMsg before reading from the channel.
	// (QUM-669 §2.2)
	pendingMsg tea.Msg
	// injectGap is the TEST-ONLY one-shot synthetic gap size read from
	// SPRAWL_DEBUG_GAP_INJECT at subscribe time. When non-zero, the adapter
	// fabricates an EventDropDetectedMsg with Missing=injectGap on the second
	// event of the subscription, then zeros the field. See debugGapInjectEnv.
	injectGap uint64
}

// NewTUIAdapter subscribes to the runtime's EventBus and returns an adapter
// ready for use by a Bubble Tea program.
func NewTUIAdapter(rt *sprawlrt.UnifiedRuntime) *TUIAdapter {
	a := &TUIAdapter{runtime: rt}
	a.subscribe(rt)
	return a
}

// subscribe registers a fresh subscription against rt. Caller must hold a.mu
// or otherwise serialize access.
func (a *TUIAdapter) subscribe(rt *sprawlrt.UnifiedRuntime) {
	ch, unsub := rt.EventBus().SubscribeNamed("tui-viewport", adapterEventBufferSize)
	a.events = ch
	a.unsubscribe = unsub
	a.epoch++
	// Reset gap-detection baseline so the first event on the new subscription
	// is never flagged as a drop (its Seq is unrelated to the previous bus's
	// counter). (QUM-669 §2.2)
	a.lastSeq = 0
	a.pendingMsg = nil
	// TEST-ONLY (QUM-669 viewport-resync e2e row): read the debug-gap-inject
	// env var under the same lock that guards the rest of the adapter state.
	// Ignore parse errors and zero values silently.
	a.injectGap = 0
	if raw := os.Getenv(debugGapInjectEnv); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil && n > 0 {
			a.injectGap = n
		}
	}
}

// Initialize returns a tea.Cmd that starts the underlying runtime. On
// success the command yields tui.SessionInitializedMsg; on error,
// tui.SessionErrorMsg.
func (a *TUIAdapter) Initialize() tea.Cmd {
	return func() tea.Msg {
		a.mu.Lock()
		rt := a.runtime
		a.mu.Unlock()
		if rt == nil {
			return tui.SessionErrorMsg{Err: ErrNoRuntime}
		}
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
			// Drain any pending translated msg from a prior gap-detection
			// before reading from the channel. (QUM-669 §2.2)
			if a.pendingMsg != nil {
				msg := a.pendingMsg
				a.pendingMsg = nil
				a.mu.Unlock()
				return msg
			}
			ch := a.events
			cancelled := a.cancelled
			epochAtRead := a.epoch
			a.mu.Unlock()

			if cancelled || ch == nil {
				return tui.SessionErrorMsg{Err: io.EOF}
			}

			ev, ok := <-ch
			if !ok {
				// Distinguish a real EOF from an Observe()-driven channel
				// swap: if epoch advanced and we still have a live (non-
				// cancelled) subscription, transparently re-read from the
				// new channel rather than surfacing a spurious EOF.
				// (QUM-436 Item 2)
				a.mu.Lock()
				swapped := a.epoch != epochAtRead && !a.cancelled && a.events != nil
				a.mu.Unlock()
				if swapped {
					continue
				}
				return tui.SessionErrorMsg{Err: io.EOF}
			}

			// Gap detection (QUM-669 §2.2): if we have a baseline lastSeq and
			// this event's Seq is non-contiguous, emit an EventDropDetectedMsg
			// first and stash any translated msg for the next call to drain.
			a.mu.Lock()
			// TEST-ONLY debug seam (QUM-669 viewport-resync row): synthesize a
			// one-shot gap on the second event of the subscription so the
			// resync path can be exercised end-to-end. The arriving event's
			// real Seq is preserved for normal lastSeq tracking afterward.
			if a.injectGap > 0 && a.lastSeq != 0 {
				missing := a.injectGap
				from := a.lastSeq
				to := from + missing + 1
				a.injectGap = 0
				a.lastSeq = ev.Seq
				if msg := tui.TranslateRuntimeEvent(ev, tui.InterruptedAsCompleted); msg != nil {
					a.pendingMsg = msg
				}
				a.mu.Unlock()
				return tui.EventDropDetectedMsg{From: from, To: to, Missing: missing}
			}
			// QUM-669 hardening: gap detection only fires on a FORWARD jump.
			// EventBus.Publish serializes stamp+fanout so out-of-order delivery
			// shouldn't be observable in production, but if a backwards or
			// duplicate Seq slips through we'd otherwise underflow `missing` and
			// surface a banner like "recovered 18446744073709551615 events".
			// Treat ev.Seq <= a.lastSeq as a no-op (no gap msg, do not regress
			// lastSeq) and fall through to normal translation.
			gap := a.lastSeq != 0 && ev.Seq > a.lastSeq+1
			if gap {
				from := a.lastSeq
				to := ev.Seq
				missing := to - from - 1
				a.lastSeq = ev.Seq
				// Translate the gap-arriving event so its in-band msg still
				// flows to the model on the next WaitForEvent call. If the
				// translation is nil (lifecycle-only event), nothing to stash.
				if msg := tui.TranslateRuntimeEvent(ev, tui.InterruptedAsCompleted); msg != nil {
					a.pendingMsg = msg
				}
				a.mu.Unlock()
				return tui.EventDropDetectedMsg{From: from, To: to, Missing: missing}
			}
			// Forward (contiguous) OR backward/duplicate (treat as no-op for
			// gap accounting). In the backward case we deliberately do NOT
			// rewind lastSeq — once we've crossed Seq=N, an older Seq=M<N is
			// just a stale duplicate that should still translate normally for
			// in-band UI.
			if ev.Seq > a.lastSeq {
				a.lastSeq = ev.Seq
			}
			a.mu.Unlock()

			if msg := tui.TranslateRuntimeEvent(ev, tui.InterruptedAsCompleted); msg != nil {
				return msg
			}
		}
	}
}

// SendMessage writes a human-typed user prompt to the CLI stdin (QUM-817,
// priority next, kind user) and returns a tui.UserMessageSentMsg. The CLI owns
// queuing/coalescing; the prompt renders "sent" when its isReplay echo arrives.
func (a *TUIAdapter) SendMessage(text string) tea.Cmd {
	return func() tea.Msg {
		a.mu.Lock()
		rt := a.runtime
		a.mu.Unlock()
		if rt == nil {
			return tui.SessionErrorMsg{Err: ErrNoRuntime}
		}
		uuid, err := rt.WriteUserPrompt(context.Background(), text, "next")
		if err != nil {
			return tui.SessionErrorMsg{Err: err}
		}
		return tui.UserMessageSentMsg{UUID: uuid, Text: text}
	}
}

// SendAttachment validates local image files, assembles an image-before-text
// multimodal turn (priority next, kind user), and writes it to the CLI stdin
// (QUM-860). A local validation failure (missing/unreadable/unsupported/too
// large) returns an AttachRejectedMsg (ToastError) and writes NO turn; success
// returns a tui.UserMessageSentMsg carrying the prompt text and per-file chip
// metadata so the pending bubble renders and settles on its isReplay echo.
func (a *TUIAdapter) SendAttachment(paths []string, prompt string) tea.Cmd {
	return func() tea.Msg {
		a.mu.Lock()
		rt := a.runtime
		a.mu.Unlock()
		if rt == nil {
			return tui.SessionErrorMsg{Err: ErrNoRuntime}
		}
		blocks, chips, err := attach.Build(paths, prompt)
		if err != nil {
			// QUM-860: AttachRejectedMsg (not a bare ToastSpawnMsg) so the reducer
			// can unwind AttachMsg's optimistic Idle→Thinking flip — no turn is
			// written, so nothing else clears the phantom spinner.
			return tui.AttachRejectedMsg{Toast: tui.Toast{
				Text:      err.Error(),
				Style:     tui.ToastError,
				DismissOn: tui.TimerDismiss(attachErrorToastTTL),
			}}
		}
		uuid, err := rt.WriteUserBlocks(context.Background(), prompt, blocks, "next")
		if err != nil {
			return tui.SessionErrorMsg{Err: err}
		}
		tchips := make([]tui.AttachmentChip, len(chips))
		for i, c := range chips {
			tchips[i] = tui.AttachmentChip{Name: c.Name, MediaType: c.MediaType, Size: c.HumanSize}
		}
		return tui.UserMessageSentMsg{UUID: uuid, Text: prompt, Attachments: tchips}
	}
}

// Interrupt requests an interrupt of the current turn. The result is wrapped
// in tui.InterruptResultMsg so the model can decide how to render it.
func (a *TUIAdapter) Interrupt() tea.Cmd {
	return func() tea.Msg {
		a.mu.Lock()
		rt := a.runtime
		a.mu.Unlock()
		if rt == nil {
			return tui.InterruptResultMsg{Err: ErrNoRuntime}
		}
		err := rt.Interrupt(context.Background())
		return tui.InterruptResultMsg{Err: err}
	}
}

// Recall cancels every still-pending human-typed prompt and returns their text
// (newline-joined, submit order) as a tui.PromptsRecalledMsg for the input to
// rehydrate (QUM-824 — weave-only recall UX). cancelled:false prompts are left
// consumed and excluded. A partial-cancel error is surfaced on the message
// alongside whatever text did recall.
func (a *TUIAdapter) Recall() tea.Cmd {
	return func() tea.Msg {
		a.mu.Lock()
		rt := a.runtime
		a.mu.Unlock()
		if rt == nil {
			return tui.SessionErrorMsg{Err: ErrNoRuntime}
		}
		text, err := rt.Recall(context.Background())
		return tui.PromptsRecalledMsg{Text: text, Err: err}
	}
}

// SendAllNow cancels every still-pending human-typed prompt and resubmits them
// as one priority:now message that supersedes the queued ones (QUM-824 —
// weave-only send-all-now UX). Returns a tui.SendAllNowResultMsg.
func (a *TUIAdapter) SendAllNow() tea.Cmd {
	return func() tea.Msg {
		a.mu.Lock()
		rt := a.runtime
		a.mu.Unlock()
		if rt == nil {
			// QUM-830: return a SendAllNowResultMsg (not SessionErrorMsg) so the
			// TUI's Ctrl+G debounce latch — which clears ONLY on
			// SendAllNowResultMsg — does not wedge during the nil-runtime window
			// of a session restart. Mirrors Interrupt's nil-runtime precedent.
			return tui.SendAllNowResultMsg{Err: ErrNoRuntime}
		}
		err := rt.SendAllNow(context.Background())
		return tui.SendAllNowResultMsg{Err: err}
	}
}

// Close cancels the adapter's EventBus subscription. Part of the
// tui.SessionBackend contract. Always returns nil; idempotent.
func (a *TUIAdapter) Close() error {
	a.Cancel()
	return nil
}

// IsContinuous reports whether the adapter's event stream is continuous (i.e.
// produces autonomous events even when no user turn is in flight). Always
// true for TUIAdapter — the underlying UnifiedRuntime emits events
// independent of WaitForEvent's per-turn lifecycle (e.g. interrupt-delivery
// drains).
func (a *TUIAdapter) IsContinuous() bool { return true }

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

// DropTelemetry exposes the observed runtime's EventBus drop telemetry to
// the TUI status bar (QUM-681). Returns nil when no runtime is observed.
// The runtime-side runtime.DropTelemetry is mirrored into the tui-side
// tui.EventDropSnapshot so the tui package doesn't need to import
// internal/runtime.
func (a *TUIAdapter) DropTelemetry() map[string]tui.EventDropSnapshot {
	a.mu.Lock()
	rt := a.runtime
	a.mu.Unlock()
	if rt == nil {
		return nil
	}
	src := rt.EventBus().DropTelemetry()
	out := make(map[string]tui.EventDropSnapshot, len(src))
	for k, v := range src {
		out[k] = tui.EventDropSnapshot{
			Cumulative: v.Cumulative,
			LastDropAt: v.LastDropAt,
		}
	}
	return out
}

// Observe swaps the adapter's observed runtime. The previous subscription is
// torn down and a fresh one is registered against rt. Used when a session
// restart yields a new UnifiedRuntime instance and the AppModel wants the
// existing adapter to follow it.
func (a *TUIAdapter) Observe(rt *sprawlrt.UnifiedRuntime) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unsubscribe != nil {
		a.unsubscribe()
		a.unsubscribe = nil
	}
	a.runtime = rt
	a.cancelled = false
	if rt != nil {
		// Delegate to subscribe() so the (channel, unsubscribe, epoch++)
		// setup lives in exactly one place. The epoch bump lets a parked
		// WaitForEvent goroutine distinguish an Observe swap from a real
		// channel close. (QUM-436 Item 2)
		a.subscribe(rt)
	} else {
		a.events = nil
	}
}

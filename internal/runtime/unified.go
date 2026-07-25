// UnifiedRuntime wraps the per-agent EventBus and stdin-write input path
// behind a single supervised lifecycle (QUM-817: the Go MessageQueue and
// TurnLoop were deleted; every turn is now router-driven from the stdout
// stream). See docs/designs/unified-runtime.md sections 3.1, 3.6, and 4.

package runtime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	livenesspkg "github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// RuntimeConfig is the immutable construction-time configuration.
type RuntimeConfig struct {
	Name          string
	SprawlRoot    string
	Session       SessionHandle
	IsRoot        bool
	InitialPrompt string
	// Capabilities is the backend-reported feature set surfaced to callers
	// via UnifiedRuntime.Capabilities(). The supervisor uses this to forward
	// caps to its RuntimeHandle. See QUM-398.
	Capabilities backend.Capabilities
	// OnDelivered, if non-nil, is invoked when a written stdin user message is
	// confirmed consumed by its isReplay echo (QUM-817), carrying the message's
	// entryIDs (maildir ids / "task:<id>"). Replaces the QUM-579
	// OnQueueItemDelivered queue-drain signal. Must not block.
	OnDelivered func(entryIDs []string)
	// PostTurnSweep, if non-nil, is invoked once per turn boundary (on
	// EventTurnCompleted/Failed/Interrupted). The QUM-580 defense-in-depth
	// re-drain of undelivered pending maildir entries. Must not block.
	PostTurnSweep func()
}

// sessionIDProvider is an optional interface a Session may satisfy to expose a
// stable session identifier.
type sessionIDProvider interface {
	SessionID() string
}

// SessionHandle is the subset of the backend Session API the UnifiedRuntime
// drives (QUM-817). The runtime no longer opens turns via StartTurn; it writes
// user messages straight to the CLI stdin and observes the resulting frames via
// the installed frame router. The concrete *backend.session satisfies this
// structurally; tests substitute a fake.
type SessionHandle interface {
	WriteUserMessage(ctx context.Context, msg protocol.UserMessage) error
	Interrupt(ctx context.Context) error
	// CancelAsyncMessage cancels a still-pending stdin user message by uuid and
	// returns the CLI's {cancelled} ack (QUM-824). cancelled==false ⇒ already
	// dequeued for execution (gone). Used by Recall / SendAllNow.
	CancelAsyncMessage(ctx context.Context, messageUUID string) (bool, error)
}

type UnifiedRuntime struct {
	cfg      RuntimeConfig
	eventBus *EventBus

	mu       sync.RWMutex
	liveness livenesspkg.State
	started  bool
	stopped  bool
	// phase is the QUM-903 3-state in_turn machine (idle / submitted / running)
	// and the sole in_turn authority: State().InTurn == (phase != phaseIdle).
	// Driven by an optimistic submit-from-idle set (human prompts only) and the
	// CLI's authoritative session_state_changed wire signal, with terminal /
	// teardown / timeout guards. Guarded by mu.
	phase turnPhase
	// submittedGen is bumped each time the runtime (re)enters phaseSubmitted, so a
	// stale submitted-timeout guard goroutine is a no-op once its synthetic state
	// has been superseded by a wire event or a newer submit. Guarded by mu.
	submittedGen uint64
	// interruptPending is set by Interrupt when a user Esc-abort lands mid-turn
	// (QUM-827). routeFrame's EndOfTurn branches read-and-clear it to publish a
	// clean EventInterrupted instead of EventTurnCompleted/EventTurnFailed —
	// otherwise the interrupted turn's is_error `result` frame surfaces as a
	// spurious "Session Error". Guarded by mu. Three paths retire it: consumed by
	// its own turn's terminal; an idle→non-idle phase transition while NO frame
	// turn is open; or the next system/init (QUM-927). Retirement is fail-safe by
	// DIRECTION rather than by an ordering guarantee — see the system/init
	// arm-retire in routeFrame for the exact invariant and its known race
	// (QUM-935). A stale flag is not known to leak into a later turn in any
	// observed shape, but that is a consequence of the retire sites, not an
	// absolute the code may assume.
	interruptPending bool
	// frameTurnOpen mirrors the frame router's autoTurn.open under mu (QUM-927).
	// autoTurn itself is reader-goroutine-only, but Interrupt (TUI goroutine) must
	// know whether a terminal `result` may still be inbound: at a turn boundary the
	// CLI reports session_state_changed:idle while async Agent sidechains resolve,
	// so phase reads idle even though the frame-level turn is still open and its
	// is_error terminal is still coming. Guarded by mu.
	frameTurnOpen bool

	cancel        context.CancelFunc
	doneWG        sync.WaitGroup
	done          chan struct{}
	closeDoneOnce sync.Once

	// autoTurn holds the frame router's per-turn observation state. Touched ONLY
	// by routeFrame, which runs solely on the backend reader goroutine — so no
	// lock is needed for its fields (inTurn, the cross-goroutine read surface,
	// is guarded separately by mu).
	autoTurn autonomousTurnState

	// outstanding is the ONLY client-side message state (QUM-817): a map of
	// every stdin user message we've written, keyed by uuid, flipped to consumed
	// when its isReplay echo is observed. outMu is a leaf lock — never held
	// while calling the session, publishing, or acquiring mu.
	outMu       sync.Mutex
	outstanding map[string]*OutstandingEntry
	// outSeq is a monotonic counter stamped onto each OutstandingEntry.seq in
	// writeMessage, giving recall / send-all-now a stable submit order (the
	// outstanding map's iteration order is random). Guarded by outMu.
	outSeq uint64
}

// turnPhase is the QUM-903 3-state in_turn machine.
type turnPhase int

const (
	// phaseIdle: no turn in flight. State().InTurn == false.
	phaseIdle turnPhase = iota
	// phaseSubmitted: a human prompt was optimistically submitted from idle and
	// the authoritative wire `running` ack has not yet arrived. Synthetic /
	// speculative — the only phase sprawl asserts on its own. State().InTurn == true.
	phaseSubmitted
	// phaseRunning: the CLI's session_state_changed:running has confirmed a live
	// turn. State().InTurn == true.
	phaseRunning
)

// submittedPhaseTimeout bounds how long the synthetic phaseSubmitted state may
// persist without a wire `running` ack before it defensively clears to idle
// (backend died / hung after a successful write). Set well above the audit
// corpus's max submit→running latency (291ms). Package var so tests can shorten
// it; the terminal / teardown guards clear far sooner in the common case.
var submittedPhaseTimeout = 2 * time.Second

// outstandingKind classifies a written user message (QUM-817).
type outstandingKind int

const (
	// kindUser is a human-typed prompt (recallable in the weave TUI — Slice 4).
	kindUser outstandingKind = iota
	// kindSystem is a sprawl-originated message (report_status, inbox, task,
	// liveness) — NOT user-recallable.
	kindSystem
)

// outstandingState tracks a written message's lifecycle (QUM-817).
type outstandingState int

const (
	statePending outstandingState = iota
	stateConsumed
	stateCancelled
)

// OutstandingEntry is one tracked stdin user message.
type OutstandingEntry struct {
	kind     outstandingKind
	state    outstandingState
	text     string   // retained for recall (Slice 4); harmless in Slice 2
	entryIDs []string // maildir entry ids / "task:<id>" for delivery tracking
	seq      uint64   // submit order, stamped in writeMessage (QUM-824)
}

// AutoContinuePrefix is the machine sentinel that opened the historical
// auto-continue continuation prompt. QUM-929 deleted the injection — sprawl no
// longer PRODUCES such frames — but the constant is retained because six weeks of
// on-disk wire logs still contain them and it is the SINGLE shared discriminator
// the TUI replay classifier (internal/tui/replay.go) uses to reconstruct the
// "↻ auto-continued" marker when reloading those sessions. The frame carried no
// wrapper or synthetic flag on the wire, so the prefix is the only stable signal.
// Exported so the literal is defined once and the two packages cannot drift
// (QUM-924).
const AutoContinuePrefix = "[auto-continue]"

// autonomousTurnState is the frame router's per-turn bookkeeping for an
// in-flight turn (QUM-815/QUM-817). Reader-goroutine-only.
type autonomousTurnState struct {
	open bool
}

func (a *autonomousTurnState) reset() {
	a.open = false
}

// New constructs a UnifiedRuntime in the idle liveness state (Running,
// non-autonomous) with a fresh queue and event bus. No goroutines are started
// until Start is called.
func New(cfg RuntimeConfig) *UnifiedRuntime {
	rt := &UnifiedRuntime{
		cfg:         cfg,
		eventBus:    NewEventBus(),
		liveness:    livenesspkg.State{Liveness: livenesspkg.Running},
		done:        make(chan struct{}),
		outstanding: make(map[string]*OutstandingEntry),
	}
	// QUM-602: install the backend-fault handler on the session. We use a
	// type assertion (rather than extending SessionHandle) so the public
	// interface stays minimal — the concrete backend.*session implements
	// SetTerminalErrorHandler; tests' fake sessions implement it ad-hoc.
	if cfg.Session != nil {
		if setter, ok := cfg.Session.(interface {
			SetTerminalErrorHandler(func(error))
		}); ok {
			setter.SetTerminalErrorHandler(func(err error) {
				class, hint := ClassifyBackendFault(err)
				rt.eventBus.Publish(RuntimeEvent{
					Type:            EventBackendFaulted,
					Error:           err,
					FaultClass:      class,
					FaultNextAction: hint,
				})
				// QUM-635: if a turn is in flight when the backend faults,
				// the turn-loop's drain exits silently (parent-ctx cancel
				// path below, not a per-turn DeadlineExceeded) and never
				// publishes a terminal turn event. Without one, the TUI stays
				// in TurnStreaming forever — input gated, Esc a no-op — the
				// exact wedge seen when the D1 watchdog cancelled a turn
				// blocked on an ask_user_question. Emit EventTurnFailed so the
				// existing terminal path (bridge → SessionResultMsg →
				// finalizeTurn) clears streaming state and ungates input. Gated
				// on turnRunning so a fault between turns can't spuriously
				// finalize an idle TUI.
				rt.mu.RLock()
				//
				// QUM-927: "a turn is in flight" must include the FRAME-level
				// turn, not just the phase machine. At a turn boundary the CLI
				// has already reported session_state_changed:idle after end_turn
				// (so phase reads idle) while the turn's terminal `result` is
				// still inbound and frameTurnOpen is still true. With the narrow
				// phase-only gate, a genuine transport EOF / subprocess death in
				// that window published EventBackendFaulted but NO EventTurnFailed
				// — and since EventBackendFaulted has no root consumer
				// (runFaultSubscriber is children-only), the orphan teardown in
				// routeFrame then consumed the pending interrupt arm and published
				// EventInterrupted instead. Net user-visible result: "Interrupted"
				// for a DEAD subprocess — no Session Error, no restart modal, no
				// fault surface at all. Widening restores the pre-QUM-927 surface
				// and makes the boundary path behave identically to the mid-turn
				// path (which already publishes both this EventTurnFailed and the
				// orphan branch's).
				turnRunning := rt.inTurnLocked() || rt.frameTurnOpen
				rt.mu.RUnlock()
				if turnRunning {
					rt.eventBus.Publish(RuntimeEvent{Type: EventTurnFailed, Error: err})
				}
				// QUM-606 R2: cancel the turn-loop runCtx so the loop
				// exits, loopWG unblocks, and rt.done closes. Without
				// this, AgentRuntime.watchHandleExit is structurally
				// blind to backend-session death (Done() only fired on
				// Stop before this change). On cancel, the supervisor
				// transitions Lifecycle → Stopped and emits
				// RuntimeEventStopped so the TUI fault banner re-fires.
				rt.mu.RLock()
				cancel := rt.cancel
				rt.mu.RUnlock()
				if cancel != nil {
					cancel()
				}
			})
		}
		// QUM-815: install the single frame router so the backend reader routes
		// every turn frame (sprawl or autonomous) through one path. Same
		// type-assertion pattern as the terminal-error handler above.
		if setter, ok := cfg.Session.(interface {
			SetFrameRouter(func(*protocol.Message, backend.TurnInfo))
		}); ok {
			setter.SetFrameRouter(rt.routeFrame)
		}
	}
	return rt
}

// errStreamClosedNoResult is the terminal error published when an autonomous
// turn is torn down (session close/fault) without ever seeing a `result`
// frame. Mirrors the TurnLoop's QUM-647 channel-close safety net so any
// turn-boundary waiter unblocks. (QUM-815)
var errStreamClosedNoResult = errors.New("autonomous turn stream closed without terminal result")

// routeFrame is the single observe-and-route callback the backend reader
// invokes for every turn frame (QUM-815). For sprawl-initiated turns it
// returns immediately — the TurnLoop owns their lifecycle, and emitting here
// too would double-publish. For autonomous (CLI self-reprompt) turns it
// derives the full lifecycle: a balanced EventTurnStarted/EventTurnCompleted and
// an InTurn flip. Every frame is also published as EventProtocolMessage for the
// TUI / telemetry — including a background task's task_notification, which is
// OBSERVED only: QUM-929 deleted the synthetic [auto-continue] stdin nudge the
// router used to write at turn-end, because the CLI self-resumes on background-task
// completion in every timing case (so the nudge was redundant and structurally one
// turn late). Runs synchronously on the reader goroutine and must not block
// (bounded EventBus.Publish only).
func (rt *UnifiedRuntime) routeFrame(msg *protocol.Message, turn backend.TurnInfo) {
	// QUM-817: an isReplay user echo is the consumption ack for a previously
	// written stdin user message. Render it and flip the outstanding entry to
	// consumed; it is NOT a turn-lifecycle frame (the turn was already opened by
	// the preceding init).
	if turn.Replay {
		if msg != nil {
			rt.eventBus.Publish(RuntimeEvent{Type: EventProtocolMessage, Message: msg})
			var uf protocol.UserFrame
			if protocol.ParseAs(msg, &uf) == nil && uf.UUID != "" {
				rt.markConsumed(uf.UUID)
			}
		}
		return
	}

	// QUM-903: a session_state_changed frame is the CLI's authoritative in_turn
	// signal. It drives ONLY the phase machine — never a frame-lifecycle event
	// or a render publish (the TUI ignores this subtype; activity logging reads
	// it off the independent Observer path). `running` confirms; `idle` clears;
	// `requires_action` is tolerated (keep the current phase — best-effort, never
	// depended on). Early-return so it can never open a turn.
	if turn.StateChange != "" {
		switch turn.StateChange {
		case protocol.SessionStateRunning:
			rt.setPhase(phaseRunning)
		case protocol.SessionStateIdle:
			rt.setPhase(phaseIdle)
		}
		return
	}

	st := &rt.autoTurn

	// QUM-903: a system/init marks a resume/turn boundary. If a speculative
	// submitted state is still outstanding across it, re-arm its guard for a
	// fresh window (a pre-boundary timer must not fire against the post-boundary
	// turn); otherwise init is a no-op for the phase machine (phase is left to
	// the wire / teardown authorities, and autoTurn.open must survive init so an
	// already-open frame turn is not silently reopened).
	if msg != nil && msg.Type == "system" && msg.Subtype == "init" {
		rt.mu.Lock()
		if rt.phase == phaseSubmitted {
			rt.setPhaseLocked(phaseSubmitted)
		}
		// QUM-927: init is a fresh turn/resume boundary, so retire any pending
		// interrupt arm that its own turn's terminal never consumed. The clear-on-open
		// below is gated on !st.open and so does not fire when init lands while the
		// frame turn is already open — without this a boundary arm could survive and
		// swallow a later turn's genuine is_error.
		//
		// The invariant this rests on is DIRECTIONAL, not an ordering guarantee.
		// Retiring an arm can only ever cause a real terminal to classify as
		// EventTurnFailed / EventTurnCompleted{IsError} — i.e. degrade to
		// pre-QUM-827 behavior — and can NEVER cause a turn that was not
		// interrupted to be reported as EventInterrupted. That direction is what
		// AC4 pins: a stale arm cannot swallow a LATER turn's genuine error. The
		// retire is fail-safe in the direction that matters, which is why it is
		// unconditional here.
		//
		// Do NOT read this as "an armed turn's terminal `result` always precedes
		// the NEXT turn's init". That stronger premise is FALSE and the code does
		// not rely on it: an arm can precede its OWN turn's init, because
		// writeMessage arms on inTurnLocked(), which is true in the purely
		// synthetic phaseSubmitted state (set optimistically, with no CLI turn
		// behind it) — and at a turn boundary the arm rides frameTurnOpen while the
		// CLI may already be opening the replacement turn. In those races this
		// clear retires an arm whose is_error terminal was legitimately inbound,
		// and that terminal then surfaces as a spurious "Session Error". Tracked as
		// QUM-935 and deliberately NOT fixed here: the obvious alternative
		// (carrying the arm across init) trades a rare spurious error for a rare
		// false "Interrupted", which is strictly worse — it hides real errors and
		// can unblock StopAfterTurn (QUM-866) and the pause waiter mid-turn.
		rt.interruptPending = false
		rt.mu.Unlock()
	}

	// Orphan/abort teardown: an autonomous turn ended without a `result`
	// (session close/fault). Revert InTurn and publish a terminal turn event so
	// any turn-boundary waiter (e.g. supervisor Pause) unblocks. Mirrors the
	// TurnLoop's "stream closed without terminal result" semantics.
	if turn.EndOfTurn && msg == nil {
		if st.open {
			rt.setPhase(phaseIdle)
			// QUM-827: a user interrupt that closed the stream with no terminal
			// result is a clean abort, not a fault. A genuine backend crash that
			// races an Esc is still surfaced independently via the
			// SetTerminalErrorHandler path (fatalErr→terminalErr→
			// EventBackendFaulted), so re-labelling the turn event here does not
			// suppress the session-fault surface.
			if rt.consumeInterruptPending() {
				rt.eventBus.Publish(RuntimeEvent{Type: EventInterrupted})
			} else {
				rt.eventBus.Publish(RuntimeEvent{Type: EventTurnFailed, Error: errStreamClosedNoResult})
			}
			rt.closeFrameTurn()
			st.reset()
		}
		return
	}

	// Render + observe EVERY autonomous frame, including a pre-init trigger. A
	// background task's task_notification rides this same publish — that is the
	// sole source of the TUI's "↻ auto-continued" marker and of task telemetry, and
	// is all sprawl does with it since QUM-929 (no stdin nudge).
	if msg != nil {
		rt.eventBus.Publish(RuntimeEvent{Type: EventProtocolMessage, Message: msg})
	}

	// Open turn lifecycle (InTurn flip + EventTurnStarted) only on a real turn
	// frame — NEVER on a pre-init trigger, which isn't guaranteed to be followed
	// by an init (a racing StartTurn can absorb it). Otherwise InTurn would leak
	// true and EventTurnStarted would have no matching completion (QUM-815). A
	// stranded trigger is publish-only — it needs no further bookkeeping now that
	// nothing is folded into a turn-end continuation (QUM-929).
	if turn.PreInit {
		return
	}
	if !st.open {
		st.open = true
		// QUM-903: turn-open no longer sets the in_turn authority (that is now
		// wire/submit-driven, so a bare autonomous init can't leak a false
		// "thinking" state). It still clears any stale pending-interrupt flag
		// (QUM-827 clear-on-open) and fires the frame-lifecycle EventTurnStarted.
		rt.openFrameTurn()
		rt.eventBus.Publish(RuntimeEvent{Type: EventTurnStarted})
	}

	if turn.EndOfTurn {
		var r protocol.ResultMessage
		if msg != nil {
			_ = protocol.ParseAs(msg, &r)
		}
		// QUM-827: a user Esc-abort that landed mid-turn re-classifies this
		// terminal frame as a clean interrupt (EventInterrupted carries the
		// result so the TUI shows "Interrupted (Nms)") rather than
		// EventTurnCompleted — whose is_error interrupted result would surface
		// as a spurious "Session Error" dialog.
		if rt.consumeInterruptPending() {
			rt.eventBus.Publish(RuntimeEvent{Type: EventInterrupted, Result: &r})
		} else {
			rt.eventBus.Publish(RuntimeEvent{Type: EventTurnCompleted, Result: &r})
		}
		// QUM-903 running-side teardown guard: a terminal `result` clears in_turn
		// even when no wire `idle` follows (the 36 no-idle teardown cases).
		rt.setPhase(phaseIdle)
		rt.closeFrameTurn()
		st.reset()
		// Fire the post-turn sweep (QUM-580) AFTER clearing per-turn state — it
		// goes to stdin / disk, never under a lock.
		if rt.cfg.PostTurnSweep != nil {
			rt.cfg.PostTurnSweep()
		}
	}
}

// setPhase transitions the QUM-903 in_turn phase machine under mu. It is the
// sole phase mutator outside setPhaseLocked's own callers.
func (rt *UnifiedRuntime) setPhase(p turnPhase) {
	rt.mu.Lock()
	rt.setPhaseLocked(p)
	rt.mu.Unlock()
}

// setPhaseLocked transitions the phase with mu held. Entering any non-idle phase
// from idle clears a stale pending-interrupt flag (QUM-827 clear-on-open) —
// unless a frame turn is open, in which case the arm belongs to that still-open
// turn and is retired by its terminal or the next init instead (QUM-927).
// Entering phaseSubmitted arms a generation-tagged timeout guard so a synthetic
// "thinking" state cannot leak if the wire `running` ack never arrives.
func (rt *UnifiedRuntime) setPhaseLocked(p turnPhase) {
	// QUM-927: only clear when no frame-level turn is open. At a turn boundary the
	// arm belongs to the still-open turn whose terminal has not arrived yet, and a
	// following wire `running` (or a user prompt's optimistic submit) is an
	// idle→non-idle transition that would otherwise clobber it. With a frame turn
	// open the arm is instead retired by that turn's terminal (consume), its
	// teardown, or the next init.
	if rt.phase == phaseIdle && p != phaseIdle && !rt.frameTurnOpen {
		rt.interruptPending = false
	}
	rt.phase = p
	if p == phaseSubmitted {
		rt.submittedGen++
		gen := rt.submittedGen
		timeout := submittedPhaseTimeout
		go rt.guardSubmitted(gen, timeout)
	}
}

// inTurnLocked reports whether a turn is in flight (submitted or running). The
// caller must hold mu (read or write).
func (rt *UnifiedRuntime) inTurnLocked() bool { return rt.phase != phaseIdle }

// guardSubmitted is the QUM-903 submitted-side defensive clear: if the synthetic
// phaseSubmitted is still current (same generation, not superseded by a wire
// event or a newer submit) after the timeout, clear it to idle so a dead / hung
// backend can never leak a false "thinking" state. A stale timer (its generation
// bumped, or the phase already moved) is a no-op.
func (rt *UnifiedRuntime) guardSubmitted(gen uint64, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-rt.done:
		return
	}
	rt.mu.Lock()
	if !rt.stopped && rt.phase == phaseSubmitted && rt.submittedGen == gen {
		rt.phase = phaseIdle
	}
	rt.mu.Unlock()
}

// openFrameTurn records that the frame router opened a turn (QUM-927 mirror) and
// clears any stale pending-interrupt flag (QUM-827 clear-on-open).
func (rt *UnifiedRuntime) openFrameTurn() {
	rt.mu.Lock()
	rt.frameTurnOpen = true
	rt.interruptPending = false
	rt.mu.Unlock()
}

// closeFrameTurn records that the frame router's turn ended — a terminal
// `result` or an orphan teardown. Paired with autoTurn.reset().
func (rt *UnifiedRuntime) closeFrameTurn() {
	rt.mu.Lock()
	rt.frameTurnOpen = false
	rt.mu.Unlock()
}

// consumeInterruptPending read-and-clears the QUM-827 pending-interrupt flag
// under mu. Returns true iff a user interrupt was armed for the turn that is
// now ending.
func (rt *UnifiedRuntime) consumeInterruptPending() bool {
	rt.mu.Lock()
	ip := rt.interruptPending
	rt.interruptPending = false
	rt.mu.Unlock()
	return ip
}

// WriteUserPrompt writes a human-typed prompt (kind:user, recallable) to the
// CLI stdin (QUM-817). Used by the TUI input path.
func (rt *UnifiedRuntime) WriteUserPrompt(ctx context.Context, text, priority string) (string, error) {
	return rt.writeMessage(ctx, text, priority, kindUser, nil, nil)
}

// WriteUserBlocks writes a multimodal human turn (kind:user, recallable) whose
// MessageParam carries an array of content blocks (image + text) instead of a
// bare string (QUM-860). text is the prompt body, retained in the outstanding
// map for recall and for the render-on-consume bubble; blocks is the assembled
// image-before-text payload actually sent on the wire. All uuid-mint,
// outstanding-tracking, and priority semantics match WriteUserPrompt so the
// isReplay echo settles the pending bubble identically.
func (rt *UnifiedRuntime) WriteUserBlocks(ctx context.Context, text string, blocks []protocol.ContentBlock, priority string) (string, error) {
	return rt.writeMessage(ctx, text, priority, kindUser, nil, blocks)
}

// WriteSystemMessage writes a sprawl-originated message (kind:system, not
// recallable) to the CLI stdin (QUM-817). Used by the supervisor delivery path
// for inbox/status/task/liveness notifications.
// entryIDs link the message to durable maildir/task records for delivery
// tracking via the isReplay consumption ack.
func (rt *UnifiedRuntime) WriteSystemMessage(ctx context.Context, text, priority string, entryIDs []string) (string, error) {
	return rt.writeMessage(ctx, text, priority, kindSystem, entryIDs, nil)
}

// writeMessage writes one user message to the CLI stdin with the given priority
// + a freshly minted uuid, records it in the outstanding map as pending, and
// returns the uuid (QUM-817). The isReplay echo later flips the entry to
// consumed (see markConsumed). The map entry is recorded BEFORE the stdin write
// so the echo (observed on the reader goroutine) always finds it.
func (rt *UnifiedRuntime) writeMessage(ctx context.Context, text, priority string, kind outstandingKind, entryIDs []string, blocks []protocol.ContentBlock) (string, error) {
	uuid := newUUID()
	rt.outMu.Lock()
	rt.outSeq++
	rt.outstanding[uuid] = &OutstandingEntry{kind: kind, state: statePending, text: text, entryIDs: entryIDs, seq: rt.outSeq}
	rt.outMu.Unlock()

	sid := ""
	if p, ok := rt.cfg.Session.(sessionIDProvider); ok {
		sid = p.SessionID()
	}
	// A multimodal turn sets Blocks (MessageParam.MarshalJSON emits the array
	// form); a plain turn sets Content (bare-string fast-path). Never both.
	mp := protocol.MessageParam{Role: "user"}
	if len(blocks) > 0 {
		mp.Blocks = blocks
	} else {
		mp.Content = text
	}
	err := rt.cfg.Session.WriteUserMessage(ctx, protocol.UserMessage{
		Type:      "user",
		Message:   mp,
		Priority:  priority,
		UUID:      uuid,
		SessionID: sid,
	})
	if err != nil {
		rt.outMu.Lock()
		delete(rt.outstanding, uuid)
		rt.outMu.Unlock()
		return "", err
	}

	rt.mu.Lock()
	// QUM-830: a priority:"now" write (cancel-and-replace, e.g. send-all-now)
	// preempts the in-flight model turn. The preempted turn emits an is_error
	// `result` terminal frame — same shape as an Esc-abort — so arm the
	// pending-interrupt flag exactly as Interrupt does (QUM-827), letting
	// routeFrame re-classify that terminal as a clean EventInterrupted instead
	// of EventTurnCompleted{IsError} → the empty "Session Error" overlay. Gated
	// on in-turn (no in-flight turn ⇒ nothing to preempt); the next turn open
	// clears any stale flag, so this cannot leak forward.
	// QUM-927: gated on the frame turn too, for the turn-boundary case (same
	// reasoning as Interrupt's arm).
	if priority == "now" && (rt.inTurnLocked() || rt.frameTurnOpen) {
		rt.interruptPending = true
	}
	// QUM-903: optimistically enter the synthetic submitted state on a
	// human-typed prompt (kind:user — the watched weave input path) submitted
	// FROM idle, hiding the ~2–10ms submit→running wire latency. Only from idle:
	// a submit while already submitted/running just queues (no new synthetic).
	// kind:system deliveries (spawn prompt, inbox, task) never synthesize —
	// passively-observed agents are driven purely by their wire.
	if kind == kindUser && rt.phase == phaseIdle {
		rt.setPhaseLocked(phaseSubmitted)
	}
	rt.mu.Unlock()
	return uuid, nil
}

// Outstanding returns a snapshot copy of the outstanding map (QUM-817). Used by
// the TUI to render queued→sent and (Slice 4) recall.
func (rt *UnifiedRuntime) Outstanding() map[string]OutstandingEntry {
	rt.outMu.Lock()
	defer rt.outMu.Unlock()
	out := make(map[string]OutstandingEntry, len(rt.outstanding))
	for k, v := range rt.outstanding {
		out[k] = *v
	}
	return out
}

// newUUID mints a random UUID v4 string (QUM-817). Mirrors state.GenerateUUID
// without importing the state package.
func newUUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failure is fatal-ish; fall back to a time-free sentinel
		// that is still unique enough via the counter. In practice rand.Read
		// does not fail on supported platforms.
		return fmt.Sprintf("uuid-fallback-%x", buf)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

// markConsumed flips an outstanding entry to consumed on its isReplay echo,
// fires the delivery callback (QUM-580/579 replacement, keyed on the protocol
// consumption ack), and publishes EventUserMessageConsumed (QUM-817).
func (rt *UnifiedRuntime) markConsumed(uuid string) {
	rt.outMu.Lock()
	e := rt.outstanding[uuid]
	var entryIDs []string
	if e != nil {
		if e.state == statePending {
			e.state = stateConsumed
		}
		entryIDs = e.entryIDs
	}
	rt.outMu.Unlock()
	if e != nil && len(entryIDs) > 0 && rt.cfg.OnDelivered != nil {
		rt.cfg.OnDelivered(entryIDs)
	}
	rt.eventBus.Publish(RuntimeEvent{Type: EventUserMessageConsumed, UUID: uuid})
}

// ConfirmDeliveredWithoutReplay marks an outstanding stdin write consumed
// WITHOUT an isReplay echo (QUM-821). now-priority (cancel-and-replace) messages
// are injected directly and are never re-emitted via --replay-user-messages, so
// the consumption ack that normally drives markConsumed never arrives. The
// supervisor calls this on a confirmed successful now-priority write to keep the
// in-memory outstanding map and the durable maildir in sync (flip → consumed +
// OnDelivered) and to publish EventUserMessageConsumed. No-op for an unknown
// uuid. Use ONLY for priority="now" writes; next-class writes confirm via the
// isReplay echo.
func (rt *UnifiedRuntime) ConfirmDeliveredWithoutReplay(uuid string) {
	rt.markConsumed(uuid)
}

// pendingUserSnapshot is one still-pending human-typed message captured for
// recall / send-all-now, ordered by submit seq.
type pendingUserSnapshot struct {
	uuid string
	text string
	seq  uint64
}

// snapshotPendingUser returns the still-pending kind:user entries sorted by
// submit order (QUM-824). Takes outMu briefly; the result is used to drive
// session cancel calls with NO lock held (outMu is a leaf lock).
func (rt *UnifiedRuntime) snapshotPendingUser() []pendingUserSnapshot {
	rt.outMu.Lock()
	snap := make([]pendingUserSnapshot, 0, len(rt.outstanding))
	for uuid, e := range rt.outstanding {
		if e.kind == kindUser && e.state == statePending {
			snap = append(snap, pendingUserSnapshot{uuid: uuid, text: e.text, seq: e.seq})
		}
	}
	rt.outMu.Unlock()
	sort.Slice(snap, func(i, j int) bool { return snap[i].seq < snap[j].seq })
	return snap
}

// cancelPendingUser cancels every still-pending kind:user message and returns
// the text of the ones that ACTUALLY cancelled (cancelled:true), in submit
// order, plus the first error encountered (QUM-824). For each uuid:
//   - cancelled:true  → flip pending→cancelled, publish EventUserMessageCancelled,
//     include its text.
//   - cancelled:false → already dequeued for execution (gone); flip
//     pending→consumed, publish EventUserMessageConsumed; exclude its text.
//
// State flips are guarded so a concurrent isReplay (markConsumed) that already
// flipped the entry is never clobbered — only a still-pending entry is mutated.
// outMu is never held across the session CancelAsyncMessage call.
func (rt *UnifiedRuntime) cancelPendingUser(ctx context.Context) ([]string, error) {
	snap := rt.snapshotPendingUser()
	texts := make([]string, 0, len(snap))
	var firstErr error
	for _, p := range snap {
		cancelled, err := rt.cfg.Session.CancelAsyncMessage(ctx, p.uuid)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue // leave the entry pending; best-effort
		}
		if cancelled {
			if rt.flipPending(p.uuid, stateCancelled) {
				texts = append(texts, p.text)
				rt.eventBus.Publish(RuntimeEvent{Type: EventUserMessageCancelled, UUID: p.uuid})
			}
		} else {
			// cancelled:false ⇒ already executing/consumed; treat as gone.
			rt.markConsumed(p.uuid)
		}
	}
	return texts, firstErr
}

// flipPending transitions an outstanding entry from statePending to the target
// state, returning true if it actually transitioned. A no-op (returns false) if
// the entry is missing or already left statePending (e.g. a racing isReplay
// consumed it first) — this prevents clobbering a consumed/cancelled entry.
func (rt *UnifiedRuntime) flipPending(uuid string, target outstandingState) bool {
	rt.outMu.Lock()
	defer rt.outMu.Unlock()
	e := rt.outstanding[uuid]
	if e == nil || e.state != statePending {
		return false
	}
	e.state = target
	return true
}

// Recall cancels every still-pending human-typed (kind:user) stdin message and
// returns their text newline-joined in submit order, for the weave TUI to
// rehydrate into the input (QUM-824). Messages that did not actually cancel
// (cancelled:false ⇒ already dequeued for execution) are flipped to consumed and
// NOT returned — already-consumed prompts entered the conversation and cannot be
// pulled back (honest UX). Correct against both ack models: only still-pending
// entries are candidates, and `next` (isReplay) + `now`
// (ConfirmDeliveredWithoutReplay) both converge to stateConsumed, which is
// excluded by snapshotPendingUser. On a partial cancel failure the successfully
// recalled text is returned alongside the first error.
func (rt *UnifiedRuntime) Recall(ctx context.Context) (string, error) {
	texts, err := rt.cancelPendingUser(ctx)
	return strings.Join(texts, "\n"), err
}

// SendAllNow cancels every still-pending kind:user message and resubmits the
// ones that actually cancelled as ONE priority:"now" message (fresh uuid,
// cancel-and-replace), then confirms that now-write delivered-without-replay
// (QUM-821 ack asymmetry: now-writes get no isReplay echo) (QUM-824). A no-op
// returning nil if nothing was pending / nothing cancelled.
func (rt *UnifiedRuntime) SendAllNow(ctx context.Context) error {
	texts, err := rt.cancelPendingUser(ctx)
	if err != nil {
		return err
	}
	if len(texts) == 0 {
		return nil
	}
	joined := strings.Join(texts, "\n")
	uuid, err := rt.writeMessage(ctx, joined, "now", kindUser, nil, nil)
	if err != nil {
		return err
	}
	// QUM-838: a now-write gets NO isReplay echo (QUM-821), so the TUI pending
	// zone never learns this fresh uuid on its own. Publish EventUserMessageSent
	// (carrying the coalesced text) BEFORE ConfirmDeliveredWithoutReplay so the
	// zone-add lands before the consume settle — otherwise ZoneSettle is a no-op
	// against an untracked uuid and the Ctrl+G message vanishes from the
	// transcript. Both publishes happen synchronously on this goroutine, so the
	// EventBus seq-orders sent before consumed.
	rt.eventBus.Publish(RuntimeEvent{Type: EventUserMessageSent, UUID: uuid, Prompt: joined})
	rt.ConfirmDeliveredWithoutReplay(uuid)
	return nil
}

// ClassifyBackendFault maps a backend session terminal error to a
// UX-visible class label and an operator-facing next-action hint. Known
// sentinels (ErrHangTimeout / ErrSubscriberWedged) get tailored hints;
// unknown errors fall through to a generic "Unknown" + respawn hint.
// QUM-602.
func ClassifyBackendFault(err error) (class, nextAction string) {
	switch {
	case errors.Is(err, backend.ErrHangTimeout):
		return "HangTimeout", "backend reader stalled; run mcp__sprawl__wake to bring the agent back online"
	case errors.Is(err, backend.ErrSubscriberWedged):
		return "SubscriberWedged", "backend subscriber send wedged; run mcp__sprawl__wake to bring the agent back online"
	default:
		return "Unknown", "run mcp__sprawl__wake to bring the agent back online"
	}
}

// Start spins up the runtime lifecycle goroutine. Returns an error if the
// runtime has already been started or has been stopped.
func (rt *UnifiedRuntime) Start(_ context.Context) error {
	rt.mu.Lock()
	if rt.stopped {
		rt.mu.Unlock()
		return errors.New("runtime: Start called on stopped runtime")
	}
	if rt.started {
		rt.mu.Unlock()
		return errors.New("runtime: Start called twice")
	}
	rt.started = true

	// Independent context: the runtime lifecycle must outlive the Start caller's
	// ctx. Cancelled by Stop or by the backend-fault handler.
	runCtx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	initialPrompt := rt.cfg.InitialPrompt
	rt.mu.Unlock()

	// QUM-817: there is no turn loop. The backend reader (started via the
	// host-side Initialize, or by the first WriteUserMessage) observes frames
	// and the installed frame router derives lifecycle. This goroutine just
	// holds the runtime "running" until runCtx is cancelled, then publishes
	// EventStopped and closes done so watchHandleExit unblocks.
	rt.doneWG.Add(1)
	go func() {
		defer rt.doneWG.Done()
		<-runCtx.Done()
	}()
	go func() {
		rt.doneWG.Wait()
		rt.eventBus.Publish(RuntimeEvent{Type: EventStopped})
		rt.closeDoneOnce.Do(func() { close(rt.done) })
	}()

	// Seed the spawn prompt (child agents' first turn) as a stdin write. It is
	// kind:system (machine-originated, not user-recallable).
	if initialPrompt != "" {
		if _, err := rt.writeMessage(runCtx, initialPrompt, "next", kindSystem, nil, nil); err != nil {
			return err
		}
	}

	return nil
}

// StopOptions tunes UnifiedRuntime.StopWithOptions. The zero value matches
// the legacy Stop semantics (polite Session.Interrupt issued before the
// turn loop ctx is cancelled). See QUM-600.
type StopOptions struct {
	// SkipPoliteInterrupt suppresses the polite Session.Interrupt that
	// Stop normally issues before cancelling the loop. The abandon-retire
	// path (Real.Retire(abandon=true) → StopAbandon) sets this to true so
	// a wedged backend Interrupt cannot stall teardown; the caller is
	// committed to Close+Kill regardless. (QUM-600)
	SkipPoliteInterrupt bool
}

// Stop cancels the turn loop and waits for it to drain. Idempotent and a
// no-op if Start was never called. Bounded by ctx.
//
// Stop semantics during an active turn (QUM-414):
//   - Session.Interrupt is forwarded to the backend before ctx is cancelled,
//     giving the backend a clean shutdown signal independent of the
//     ctx-cancel path. Backends are contracted to be idempotent and to
//     no-op when no turn is in flight, so this is safe in all states.
//   - The lifecycle event published is EventStopped (from the TurnLoop's
//     outer Run loop). Stop does NOT publish EventInterrupted —
//     EventInterrupted is reserved for user-initiated Interrupt drains.
//   - Mid-turn protocol messages are not guaranteed to be delivered to
//     EventBus subscribers: the wrapper forwarder returns on ctx.Done.
//
// Stop delegates to StopWithOptions with the zero-value StopOptions, so the
// legacy contract is preserved.
func (rt *UnifiedRuntime) Stop(ctx context.Context) error {
	return rt.StopWithOptions(ctx, StopOptions{})
}

// StopWithOptions is the configurable variant of Stop. When
// opts.SkipPoliteInterrupt is true, the polite Session.Interrupt that Stop
// normally issues before cancelling the loop is skipped — used by the
// abandon-retire path (QUM-600) so a wedged backend Interrupt cannot stall
// teardown. All other semantics match Stop.
func (rt *UnifiedRuntime) StopWithOptions(ctx context.Context, opts StopOptions) error {
	rt.mu.Lock()
	if !rt.started {
		rt.stopped = true
		rt.liveness = livenesspkg.State{Liveness: livenesspkg.Stopped}
		rt.closeDoneOnce.Do(func() { close(rt.done) })
		rt.mu.Unlock()
		return nil
	}
	if rt.stopped {
		rt.mu.Unlock()
		return nil
	}
	rt.stopped = true
	cancel := rt.cancel
	sess := rt.cfg.Session
	rt.mu.Unlock()

	// Best-effort: signal the backend to wind down its in-flight turn cleanly.
	// Called before cancel() so ctx is still alive for the interrupt control
	// request itself. Per SessionHandle contract, Interrupt is a no-op when
	// no turn is in flight. Skipped when opts.SkipPoliteInterrupt is true
	// (QUM-600 abandon path).
	if sess != nil && !opts.SkipPoliteInterrupt {
		_ = sess.Interrupt(ctx)
	}

	if cancel != nil {
		cancel()
	}

	loopDone := make(chan struct{})
	go func() {
		rt.doneWG.Wait()
		close(loopDone)
	}()

	select {
	case <-loopDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	rt.mu.Lock()
	rt.liveness = livenesspkg.State{Liveness: livenesspkg.Stopped}
	rt.mu.Unlock()
	return nil
}

// State returns the stored runtime liveness state. Transitions are driven from
// the wrapped Session's StartTurn / channel-close path. Callers that need to
// observe a turn starting after Enqueue should subscribe to the EventBus
// (EventTurnStarted) rather than poll State().
func (rt *UnifiedRuntime) State() livenesspkg.State {
	rt.mu.RLock()
	s := rt.liveness
	inTurn := rt.inTurnLocked()
	rt.mu.RUnlock()
	// QUM-903: InTurn is the 3-state phase machine — true for submitted (synthetic
	// optimistic) or running (wire-confirmed), false for idle.
	if inTurn {
		s.InTurn = true
	}
	return s
}

// Interrupt always forwards to the underlying Session.Interrupt (Backends
// must be idempotent). When a turn is in flight it additionally drives
// runtime-state bookkeeping (Running·autonomous-turn → Stopping) and routes
// through TurnLoop.Interrupt. No-op when stopped.
func (rt *UnifiedRuntime) Interrupt(ctx context.Context) error {
	rt.mu.Lock()
	if rt.liveness.Liveness == livenesspkg.Stopped {
		rt.mu.Unlock()
		return nil
	}
	sess := rt.cfg.Session
	inTurn := rt.inTurnLocked()
	if rt.liveness.Liveness == livenesspkg.Running && rt.liveness.InTurn {
		rt.liveness = livenesspkg.State{Liveness: livenesspkg.Stopping}
	}
	// QUM-827: arm the pending-interrupt flag for an in-turn abort so the
	// turn's terminal frame is re-classified as a clean interrupt by
	// routeFrame, not surfaced as a turn error.
	//
	// QUM-927: also arm when the frame-level turn is still open even though the
	// phase machine reads idle — the turn-boundary case, where the CLI already
	// reported session_state_changed:idle after end_turn while async Agent
	// sidechains resolve. Its is_error `result` terminal is still inbound, and
	// without the arm it classifies as EventTurnCompleted{IsError} → the spurious
	// fatal "Session Error" quit/restart modal.
	armed := inTurn || rt.frameTurnOpen
	if armed {
		rt.interruptPending = true
	}
	rt.mu.Unlock()

	// Bare contentless abort (Esc). Backends are idempotent and no-op when no
	// turn is in flight.
	err := sess.Interrupt(ctx)

	// QUM-775 item 4: when an interrupt is issued against a genuinely idle runtime,
	// emit a synthetic EventInterrupted so a TUI turnState reducer wedged in
	// TurnStreaming after a dropped terminal event can finalize.
	//
	// QUM-927: gated on `armed`, not `inTurn`. When the arm was set, a real terminal
	// frame is still inbound and will publish the authoritative EventInterrupted
	// (carrying the result, for "Interrupted (Nms)"), so the synthetic would be a
	// duplicate — and a duplicate is NOT harmless: EventInterrupted is a
	// turn-boundary signal that StopAfterTurn (QUM-866) and the pause waiter select
	// on, so a synthetic emitted while the frame turn is still open can unblock
	// teardown mid-turn. The QUM-775 wedge case is unaffected: a dropped terminal
	// EVENT still means routeFrame processed the terminal FRAME and closed the frame
	// turn, so frameTurnOpen is false there and the synthetic still fires.
	if !armed && rt.eventBus != nil {
		rt.eventBus.Publish(RuntimeEvent{Type: EventInterrupted})
	}
	return err
}

// WakeForDelivery is retained for the RuntimeHandle contract (QUM-817). Message
// delivery is now a direct stdin write (the supervisor handle calls
// WriteUserMessage), and a stdin write inherently wakes the CLI's command
// queue, so there is nothing extra to poke here. No-op.
func (rt *UnifiedRuntime) WakeForDelivery(_ context.Context) error { return nil }

// EventBus returns the runtime's EventBus. Stable for the lifetime of the
// UnifiedRuntime.
func (rt *UnifiedRuntime) EventBus() *EventBus {
	return rt.eventBus
}

// Name returns the configured agent name.
func (rt *UnifiedRuntime) Name() string {
	return rt.cfg.Name
}

// Done returns a channel that is closed after the turn loop goroutine has
// exited (whether via Stop, ctx cancellation, or natural completion). If
// Stop is called without Start ever having been called, the channel is
// also closed. Safe to call before Start.
func (rt *UnifiedRuntime) Done() <-chan struct{} { return rt.done }

// Capabilities returns the configured backend capabilities.
func (rt *UnifiedRuntime) Capabilities() backend.Capabilities {
	return rt.cfg.Capabilities
}

// SessionID returns the underlying Session's ID if it implements
// SessionID(); otherwise the empty string.
func (rt *UnifiedRuntime) SessionID() string {
	if p, ok := rt.cfg.Session.(sessionIDProvider); ok {
		return p.SessionID()
	}
	return ""
}

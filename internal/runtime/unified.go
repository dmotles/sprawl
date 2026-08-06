// UnifiedRuntime wraps the per-agent EventBus and stdin-write input path
// behind a single supervised lifecycle (QUM-817: the Go MessageQueue and
// TurnLoop were deleted; every turn is now router-driven from the stdout
// stream). See docs/archive/designs/unified-runtime.md sections 3.1, 3.6, and 4.
//
// WHY THE kind:system CHANNEL IS BOUNDED — the durable-queue category error.
//
// This file implements the supervisor-originated notification channel
// (WriteSystemMessage → boundSystemFrame → maxSystemFrameBytes). The reasoning
// below is why that channel has a structural bound at all, and it is recorded
// here because the feature that produced the incident — the QUM-730 supervisor
// liveness check — was deleted by QUM-1071. The analysis outlived its subject
// and is what constrains whatever gets added to this channel next.
//
// A liveness check is a content-free EDGE signal — it asks "are you alive NOW".
// It was once written as a durable maildir envelope, which is a category error
// rather than an implementation bug: a durable queue promises at-least-once
// delivery of everything ever written, so a MISSING CONSUMER accumulates a
// backlog by design rather than by defect. Weave had exactly that missing
// consumer. ~2 months of envelopes piled up unseen (the type is filtered out of
// messages_list, so nothing surfaced them), and the first drain after QUM-925
// finally wired a consumer up delivered all 123 of them in a single 38,673-byte
// stdin frame, destroying the root session's context.
//
// The fix at the time was to demote the signal to an in-memory boolean, which
// cannot accumulate: N arms between drains are one arm, staleness is impossible
// because nothing outlives the process, and there is no TTL to tune. Its
// deliberate consequence, stated then so it would not be rediscovered as a bug,
// was that a signal armed moments before a restart is LOST rather than
// redelivered — correct for this class, since "are you alive NOW" cannot be
// answered by the next process, so redelivery is worse than loss.
//
// The general lesson outlives its subject: match the storage to the signal.
// Durable, replayable transport is for signals whose value survives delay. An
// edge signal about the present moment must be ephemeral. And a channel with no
// consumer is not idle, it is filling — so any new notification channel must
// name its consumer at the moment it is added. The frame bound and dedup are the
// structural defence that holds even when it doesn't, which is why they live at
// the single WriteSystemMessage choke point rather than in each producer.

package runtime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
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
	// postStartHook runs at the end of Start (QUM-925). Guarded by mu.
	postStartHook func()
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
	// --- frame-turn identity + interrupt classification (QUM-931) -------------
	//
	// These three replace the QUM-827 `interruptPending bool`, its QUM-927
	// mu-guarded mirror `frameTurnOpen bool`, and the reader-goroutine-only
	// `autoTurn.open` — net zero fields, one source of truth.
	//
	// turnSeq mints a fresh id on every frame-turn OPEN. openTurnID is the id of
	// the frame turn open right now, or 0 when none is (so "is a frame turn open"
	// is DERIVED, not mirrored — no hand-maintained lockstep). interruptedTurnID
	// is the id of the frame turn a user abort is aborting, or 0.
	//
	// A terminal frame re-classifies as EventInterrupted IFF its own turn's id
	// equals interruptedTurnID. That is the entire invariant.
	//
	// WHY THREE IDS BEAT ONE FLAG. A bare boolean cannot express *which* turn an
	// arm belongs to, and all four fake-crash bugs in this series (QUM-827,
	// QUM-830, QUM-927, QUM-935) were one defect wearing four hats: the arm
	// outlived its turn, or was cleared before its turn's terminal arrived. The
	// old predicate `inTurn || frameTurnOpen` was ALREADY the three-way
	// discriminator this mechanism needs (turn open / in flight but not yet open /
	// neither) — collapsed onto one bit. That collapse was the bug class. See
	// armInterruptLocked for the resulting three-way rule.
	//
	// Because a mismatched id is inherently a no-op, the three pre-QUM-931 clear
	// paths are DELETED, not ported: openFrameTurn's clear-on-open, the
	// conditional clear in setPhaseLocked, and the system/init retire. Each was a
	// hand-maintained guess at "this arm is stale" and each was wrong at least
	// once (QUM-927, QUM-935). The single remaining retire is narrow, sound and
	// id-based: an unresolved NEXT-turn arm is dropped on return to idle (see
	// setPhaseLocked) so it cannot claim an unrelated later turn.
	//
	// WRITER DISCIPLINE — the one rule a future change must not break: turnSeq and
	// openTurnID are written ONLY by openFrameTurn / closeFrameTurn, which are
	// called ONLY from routeFrame, i.e. only on the backend reader goroutine, and
	// always under mu (the lock is for readers on OTHER goroutines). Because there
	// is exactly one writer, a single routeFrame call may snapshot openTurnID once
	// at the top and trust that snapshot for the rest of the call.
	//
	// That single-writer premise is NOT local to this package: it holds because the
	// frame router is invoked synchronously on the reader goroutine — see
	// SetFrameRouter's contract in internal/backend/session.go (the reader loop plus
	// its own orphan-teardown defer, same goroutine). If that contract changes, the
	// snapshot below is the first thing that breaks.
	//
	// interruptedTurnID has THREE writer sites, all under mu: the arm sites
	// (armInterruptLocked, on the TUI goroutine), the consume site
	// (consumeInterrupt, reader goroutine), and the retire
	// (retireUnclaimedNextArmLocked — reached both from writeMessage on the TUI
	// goroutine AND from guardSubmitted's timer goroutine). All access goes through
	// the methods below; nothing else touches these fields.
	//
	// Do NOT reinstate a separate reader-goroutine-only "open" flag to avoid taking
	// mu per frame. The cost was measured on the wire logs under
	// .sprawl/logs/sessions/ (4,367 frames / 147 turns and 2,869 / 7 in two real
	// sessions; peak 25 frames/sec over any 1-second bucket). currentFrameTurn adds
	// one uncontended RLock per FRAME (~25ns), i.e. ~0.6µs per second of wall clock
	// at that peak, against the 2-4 mu acquisitions per turn routeFrame already
	// makes. Worse, under this design a split is a CORRECTNESS bug, not just
	// duplication: the two arm branches select DIFFERENT targets, so store-drift
	// between the two "open" values turns a previously harmless race into a silent
	// misclassification.
	turnSeq           uint64
	openTurnID        uint64
	interruptedTurnID uint64
	// ackedTurnID is the id of the frame turn in which an isReplay consumption ack
	// was last observed, or 0 (QUM-1000). settleNeverAcked's gate: a turn that
	// acked a submission executed that submission, so nothing else may be settled
	// on its behalf. Same id-matching discipline as interruptedTurnID — a stale id
	// is inherently a no-op, so there is no clear path to get wrong. Guarded by mu.
	ackedTurnID uint64

	cancel        context.CancelFunc
	doneWG        sync.WaitGroup
	done          chan struct{}
	closeDoneOnce sync.Once

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
	// lastRunningMark is the outSeq watermark captured at the most recent
	// →phaseRunning TRANSITION, i.e. the submit order at the instant the CLI
	// confirmed a new live turn (QUM-1000). settleNeverAcked settles only entries
	// at or below it; that comparison is the identity signal that makes a
	// turn-terminal sweep safe. Guarded by outMu — the same lock as outSeq, so the
	// watermark and the counter can never be read torn.
	lastRunningMark uint64
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
				turnRunning := rt.inTurnLocked() || rt.frameTurnOpenLocked()
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

// routeFrame is the single observe-and-route callback the backend reader invokes
// for every turn frame (QUM-815). It derives the full lifecycle for EVERY turn: a
// balanced EventTurnStarted/EventTurnCompleted and the frame-turn id bookkeeping.
// (It does not branch on TurnInfo.Autonomous — an older version of this comment
// claimed sprawl-initiated turns returned early because "the TurnLoop owns their
// lifecycle"; QUM-817 deleted the TurnLoop and made this the only turn driver, so
// sprawl-initiated turns mint and close frame-turn ids here like any other.) Every frame is also published as EventProtocolMessage for the
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
				// QUM-1000: record that THIS frame turn acked a submission, so its
				// terminal does not sweep (see settleNeverAcked's gate).
				rt.noteTurnAcked(rt.currentFrameTurn())
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
			// QUM-1000: fix the submit-order watermark on a genuine running
			// TRANSITION only — never on a `running` that lands INSIDE an already
			// open frame turn, which would drag the watermark past a prompt queued
			// mid-turn at priority `next`. Two shapes reach that second case: the
			// QUM-903 resume-boundary running→init→running doublet (caught by the
			// phase check) and a mid-turn wire `idle` followed by another `running`
			// while the frame turn stays open (caught by the openTurnID check — the
			// phase check alone would read "fresh" there). At a genuine turn's first
			// `running` no frame turn is open yet: the wire order is
			// running → command_lifecycle → init.
			//
			// Read-and-set under ONE lock, as the rest of this file does for phase.
			// noteRunningTransition then takes outMu with mu released, so outMu stays
			// an obvious leaf.
			rt.mu.Lock()
			fresh := rt.phase != phaseRunning && rt.openTurnID == 0
			rt.setPhaseLocked(phaseRunning)
			rt.mu.Unlock()
			if fresh {
				rt.noteRunningTransition()
			}
		case protocol.SessionStateIdle:
			rt.setPhase(phaseIdle)
		}
		return
	}

	// Snapshot the open frame turn once (QUM-931 writer discipline: routeFrame is
	// the only writer of openTurnID, so this cannot go stale under us).
	turnID := rt.currentFrameTurn()

	// QUM-903: a system/init marks a resume/turn boundary. If a speculative
	// submitted state is still outstanding across it, re-arm its guard for a
	// fresh window (a pre-boundary timer must not fire against the post-boundary
	// turn); otherwise init is a no-op for the phase machine (phase is left to
	// the wire / teardown authorities, and an already-open frame turn must survive
	// init so it is not silently reopened — see the turnID == 0 gate below).
	if msg != nil && msg.Type == "system" && msg.Subtype == "init" {
		rt.mu.Lock()
		if rt.phase == phaseSubmitted {
			rt.setPhaseLocked(phaseSubmitted)
		}
		// QUM-931: init deliberately does NOT retire a pending arm. It used to
		// (QUM-927), on the premise that an armed turn's terminal always precedes the
		// NEXT turn's init. That premise is false — an arm can precede its OWN turn's
		// init (the optimistic phaseSubmitted case), which IS QUM-935 — and the wire
		// cannot disambiguate the two readings of init-while-armed. Turn identity can:
		// the arm names a turn id, so an init that opens no new turn cannot affect it,
		// and one that does open a turn mints an id a stale arm will never match.
		rt.mu.Unlock()
	}

	// Orphan/abort teardown: an autonomous turn ended without a `result`
	// (session close/fault). Revert InTurn and publish a terminal turn event so
	// any turn-boundary waiter (e.g. supervisor Pause) unblocks. Mirrors the
	// TurnLoop's "stream closed without terminal result" semantics.
	if turn.EndOfTurn && msg == nil {
		if turnID != 0 {
			rt.setPhase(phaseIdle)
			// QUM-827: a user interrupt that closed the stream with no terminal
			// result is a clean abort, not a fault. A genuine backend crash that
			// races an Esc is still surfaced independently via the
			// SetTerminalErrorHandler path (fatalErr→terminalErr→
			// EventBackendFaulted), so re-labelling the turn event here does not
			// suppress the session-fault surface.
			if rt.consumeInterrupt(turnID) {
				rt.eventBus.Publish(RuntimeEvent{Type: EventInterrupted})
			} else {
				rt.eventBus.Publish(RuntimeEvent{Type: EventTurnFailed, Error: errStreamClosedNoResult})
			}
			rt.closeFrameTurn()
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
	if turnID == 0 {
		// QUM-903: turn-open does not set the in_turn authority (that is
		// wire/submit-driven, so a bare autonomous init can't leak a false
		// "thinking" state). QUM-931: it mints this turn's id and, unlike the old
		// QUM-827 clear-on-open, does NOT touch any pending arm — a next-turn arm
		// is waiting for exactly this id.
		turnID = rt.openFrameTurn()
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
		if rt.consumeInterrupt(turnID) {
			// QUM-1000 deliberately does NOT sweep here. A user Esc / QUM-830
			// now-write preempt leaves genuinely UNEXECUTED prompts pending, and
			// Ctrl+U recall (QUM-824) can only rehydrate a still-statePending entry
			// (snapshotPendingUser) — settling them would silently make them
			// unrecallable. The discriminator is "did a user abort claim this turn",
			// not "was the result an error": an unarmed is_error terminal DOES sweep,
			// because the CLI ran that turn to completion without acking the entry.
			rt.eventBus.Publish(RuntimeEvent{Type: EventInterrupted, Result: &r})
		} else {
			// QUM-1000: settle never-acked prompts BEFORE the terminal event.
			// ORDERING IS LOAD-BEARING, and the constraint is the TUI's, not ours:
			// internal/tui/app.go's UserMessageConsumedMsg reducer flips
			// TurnIdle→TurnThinking (QUM-831, "a consume means a turn is starting"),
			// and NOTHING in the TUI clears that spuriously — there is no turn
			// watchdog; the only routes back to TurnIdle are finalizeTurn
			// (SessionResultMsg / InterruptCompletedMsg / SessionErrorMsg), a session
			// restart, or the QUM-669 gap/resync path. Publishing consumed first
			// guarantees the next pump event the TUI sees is this terminal →
			// finalizeTurn → TurnIdle, so the spinner cannot be left lit. EventBus
			// serializes Publish and each subscriber has one FIFO channel, so this is
			// the order the TUI observes.
			//
			// Gated on this turn having acked NOTHING: an ack proves the turn executed
			// a submission, and everything still pending is queued for a later turn —
			// settling one of those would remove it from Ctrl+U recall. See
			// settleNeverAcked's discriminator (2).
			if !rt.turnAcked(turnID) {
				rt.settleNeverAcked()
			}
			rt.eventBus.Publish(RuntimeEvent{Type: EventTurnCompleted, Result: &r})
		}
		// QUM-903 running-side teardown guard: a terminal `result` clears in_turn
		// even when no wire `idle` follows (the 36 no-idle teardown cases).
		rt.setPhase(phaseIdle)
		rt.closeFrameTurn()
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

// setPhaseLocked transitions the phase with mu held. Entering phaseSubmitted arms
// a generation-tagged timeout guard so a synthetic "thinking" state cannot leak if
// the wire `running` ack never arrives.
//
// QUM-931: it does NOT touch the interrupt arm on any transition. Both the QUM-827
// clear-on-open and the QUM-927 conditional clear it used to carry are deleted, and
// the remaining retire deliberately does not live here — this function is also
// called to RE-ARM the guard for a still-outstanding submit at a system/init
// (routeFrame), which is the same turn the arm belongs to, not a new one.
func (rt *UnifiedRuntime) setPhaseLocked(p turnPhase) {
	rt.phase = p
	if p == phaseSubmitted {
		rt.submittedGen++
		gen := rt.submittedGen
		timeout := submittedPhaseTimeout
		go rt.guardSubmitted(gen, timeout)
	}
}

// retireUnclaimedNextArmLocked drops a NEXT-turn arm whose turn never opened
// (interruptedTurnID > turnSeq — a current-turn arm is always <= turnSeq). Caller
// holds mu for writing.
//
// This is the ONE remaining arm retire, and the trigger is what makes it sound:
// it is ordered against the ARM, not against the wire. It fires only on the two
// events that are statements about the submit the arm belongs to:
//
//  1. a SUPERSEDING SUBMIT — a new user prompt from idle (writeMessage) means the
//     aborted prompt's turn is never opening, so its arm must not claim the new
//     one. NOT "entry to phaseSubmitted": the QUM-903 guard re-arm at system/init
//     re-enters that phase for the arm's OWN turn;
//  2. that submit's own SUBMITTED-TIMEOUT expiring (guardSubmitted) — the backend
//     never acked it, so no turn is coming.
//
// It must NOT be triggered by the phase merely returning to idle. An earlier
// draft did exactly that and it was WRONG: a trailing session_state_changed:idle
// from the PREVIOUS turn routinely lands after a new prompt is submitted (the
// normal `result` -> `idle` order), and an Esc burst lives precisely at that
// boundary — so the phase-triggered retire killed a live arm and resurrected the
// QUM-935 empty "Session Error". `p == phaseIdle` is a phase signal, not an
// identity signal: it cannot distinguish "abandoned before opening" from "not
// opened yet". Reaching for it was the same premise class that produced QUM-927
// and QUM-935. Pinned by TestInterrupt_NextTurnArm_SurvivesResidualWireIdle.
//
// Accepted residual: between an abandoned submit and the next submit/timeout, an
// AUTONOMOUS turn could open and claim the arm, mislabelling its terminal
// "Interrupted". Narrow, and the cost is one soft error mislabelled rather than a
// spurious fatal "Session Error". A genuine crash still surfaces independently
// via EventBackendFaulted plus the EventTurnFailed fault-surface gate.
// Strictly `>`, not `>=`: `== turnSeq` means the arm's turn HAS opened. That is
// currently unreachable-with-a-live-arm, but for two DIFFERENT reasons at the two
// consume sites — the result branch has already consumed the arm by then, while
// the orphan branch still has the frame turn open — so the equivalence is an
// accident of ordering, not a property. `>` keeps this correct if anyone reorders
// closeFrameTurn ahead of setPhase(phaseIdle).
func (rt *UnifiedRuntime) retireUnclaimedNextArmLocked() {
	if rt.interruptedTurnID > rt.turnSeq {
		rt.interruptedTurnID = 0
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
		// QUM-931: this submit was never acked, so no turn is coming for it —
		// retire its unclaimed arm here. This is trigger (2) in
		// retireUnclaimedNextArmLocked, and it is arm-ordered: the gen check above
		// means we only ever retire the arm of the submit that just timed out.
		// Deliberately NOT routed through setPhaseLocked — a phaseIdle transition
		// must never retire an arm (see the residual-idle note on the helper).
		rt.retireUnclaimedNextArmLocked()
		rt.phase = phaseIdle
	}
	rt.mu.Unlock()
}

// frameTurnOpenLocked reports whether a frame-level turn is open. The caller
// holds mu (read or write). Successor to the QUM-927 frameTurnOpen field.
func (rt *UnifiedRuntime) frameTurnOpenLocked() bool { return rt.openTurnID != 0 }

// currentFrameTurn returns the open frame turn's id, or 0 when none is open.
// routeFrame's once-per-call snapshot (see the writer-discipline note on the
// fields: routeFrame is the only writer, so its snapshot cannot go stale).
func (rt *UnifiedRuntime) currentFrameTurn() uint64 {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.openTurnID
}

// openFrameTurn opens a frame-level turn with a fresh id and returns it. Reader
// goroutine only. Note it deliberately does NOT touch interruptedTurnID: the
// QUM-827 clear-on-open is exactly the "cleared too early" failure mode QUM-935
// is made of.
func (rt *UnifiedRuntime) openFrameTurn() uint64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.turnSeq++
	rt.openTurnID = rt.turnSeq
	return rt.openTurnID
}

// closeFrameTurn records that the frame router's turn ended — a terminal
// `result` or an orphan teardown. Reader goroutine only.
func (rt *UnifiedRuntime) closeFrameTurn() {
	rt.mu.Lock()
	rt.openTurnID = 0
	rt.mu.Unlock()
}

// armInterruptLocked marks the frame turn that a user abort (Esc, or a
// priority:"now" cancel-and-replace preempt) is aborting, so that turn's
// is_error `result` terminal classifies as EventInterrupted instead of
// EventTurnCompleted{IsError} — which renders the empty, fatal-looking "Session
// Error" quit/restart modal on a live session. Reports whether it armed. The
// caller holds mu for writing.
//
// WHICH turn is armed IS the design (QUM-931). The abort's own turn may not exist
// on the wire yet, so "the current turn" is not always the answer:
//
//   - a frame turn is open -> THAT turn (openTurnID).
//     QUM-827 mid-turn Esc; QUM-830 mid-turn now-write preempt; QUM-927
//     turn-boundary Esc (phase reads idle, frame turn still open).
//   - no frame turn open, but the phase machine says a turn is in flight (the
//     optimistic synthetic phaseSubmitted, or wire-running before its init) ->
//     the NEXT turn to open, turnSeq+1. The CLI answers an interrupt issued in
//     this window with system/init FIRST and the is_error `result` only after, so
//     the terminal to re-classify belongs to a turn that opens AFTER the arm
//     (QUM-935). Robust even if no init arrives: a bare terminal frame opens a
//     turn itself, and that turn is still turnSeq+1.
//   - neither -> DO NOT ARM. No terminal is inbound, so there is nothing to
//     re-classify, and Interrupt emits the QUM-775 synthetic EventInterrupted
//     instead (its !armed branch).
//
// Arming "current" unconditionally breaks QUM-935; arming "next"
// unconditionally breaks QUM-827/830/927 AND swallows the following turn's
// genuine error. This three-way rule is the only correct one.
// The switch ORDER is load-bearing. Both interleavings against the reader
// goroutine are safe, for DIFFERENT reasons: arm-then-open records turnSeq+1 and
// openFrameTurn then mints exactly that id; open-then-arm finds
// frameTurnOpenLocked() already true, so the first case arms the now-open id
// directly. Reversing the cases would break the second interleaving.
func (rt *UnifiedRuntime) armInterruptLocked() bool {
	switch {
	case rt.frameTurnOpenLocked():
		rt.interruptedTurnID = rt.openTurnID
	case rt.inTurnLocked():
		rt.interruptedTurnID = rt.turnSeq + 1
	default:
		return false
	}
	return true
}

// consumeInterrupt reports whether turnID is the turn a user abort aborted,
// clearing the arm. The ONLY reader of interruptedTurnID.
//
// The only site that MATCHES on interruptedTurnID (retireUnclaimedNextArmLocked
// also reads it, to decide whether an unclaimed arm is stale; the arm sites read it
// implicitly by overwriting it).
//
// A mismatch is a no-op — that is the property that lets the old clear paths go.
// Note that with the current frame router a mismatch is UNREACHABLE in practice
// (a turn cannot open while one is open; both consume sites run before
// closeFrameTurn zeroes openTurnID; a next-arm of turnSeq+1 is by construction
// the next turn to open), so the id check is defence in depth against a future
// router change rather than a live discriminator. It is pinned by a white-box
// test that stuffs a stale id, because "unreachable by construction" is exactly
// the kind of reasoning that produced the last four bugs when left untested.
//
// The zeroing below is deliberately REDUNDANT, and honestly so: mutation-testing
// shows that removing it breaks no test, because turnSeq is monotonic so a
// consumed id can never recur and a non-zeroed arm can never match again. It is
// kept as defence in depth against a future change to id allocation, and so
// interruptedTurnID reads cleanly when inspected — not because anything currently
// depends on it. (Conversely, id uniqueness and this zeroing used to MASK each
// other's absence: either mutation alone passed the suite while both together
// were a forward leak. TestFrameTurn_IdsAreUniquePerTurn breaks that masking.)
func (rt *UnifiedRuntime) consumeInterrupt(turnID uint64) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.interruptedTurnID != 0 && rt.interruptedTurnID == turnID {
		rt.interruptedTurnID = 0
		return true
	}
	return false
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

// maxSystemFrameBytes caps one kind:system stdin frame. Applies to the
// supervisor-originated notification channel ONLY — never to a kind:user prompt,
// which is the human's own words and is never truncated or deduped.
//
// WHY A CAP EXISTS AT ALL. Every layer above WriteSystemMessage concatenates
// whatever its drain happened to find, so before this the frame size was a
// function of how long a consumer had been broken. That is not a theoretical
// bound: a restart of the root weave wrote ONE 38,673-byte frame carrying 123
// identical liveness-check lines and destroyed the session's context. 64 KiB (the
// stdin pipe buffer) is not a backstop either — the frame fit under it comfortably
// and would have at twice the size.
//
// The value is chosen to be far above any legitimate batch (a busy fleet drain is
// a few hundred bytes) and far below a context-destroying one. It is not tuned;
// if a real batch ever approaches it, the batch is the defect.
//
// For why the incident happened at all — the durable-queue category error — see
// the file header at the top of unified.go.
const maxSystemFrameBytes = 8192

// systemFrameTruncationMarker is appended when a frame is cut at
// maxSystemFrameBytes. Truncating silently would make a partial batch
// indistinguishable from a complete one at the point the recipient reads it.
const systemFrameTruncationMarker = "<system-notification type=\"truncated\">Some notifications were dropped because the batch exceeded the size limit. Call mcp__sprawl__messages_list to see anything you may have missed.</system-notification>\n"

// WriteSystemMessage writes a sprawl-originated message (kind:system, not
// recallable) to the CLI stdin (QUM-817). Used by the supervisor delivery path
// for inbox/status/task/liveness notifications.
// entryIDs link the message to durable maildir/task records for delivery
// tracking via the isReplay consumption ack.
func (rt *UnifiedRuntime) WriteSystemMessage(ctx context.Context, text, priority string, entryIDs []string) (string, error) {
	return rt.writeMessage(ctx, boundSystemFrame(text), priority, kindSystem, entryIDs, nil)
}

// boundSystemFrame collapses duplicate lines and caps the total size of a
// kind:system frame. Applied at this single choke point on purpose: both drain
// paths (WeaveRuntimeHandle and unifiedHandle) and every future notification
// channel funnel through WriteSystemMessage, so the bound cannot be bypassed by
// adding a channel that forgets to bound itself — which is exactly how the
// QUM-730 flood happened.
//
// Two mechanisms, in order, because they defend different failure shapes:
//
//  1. DEDUP is lossless. A repeated identical notification line carries no
//     information the first copy did not, so collapsing is free. This is the
//     mechanism that would have reduced the incident's 123 copies to 1.
//  2. TRUNCATION is lossy and therefore second. Distinct lines (the
//     `status_change` shape carries agent + state + summary) cannot be
//     collapsed, so a genuinely large batch is still bounded — but the frame
//     says so, and the dropped bodies are WARN-logged by the callers that
//     perform destructive drains.
//
// Both are deliberately absent from WriteUserPrompt / WriteUserBlocks: a human
// typing the same prompt twice means it twice, and a truncated user prompt is a
// corrupted instruction rather than a shortened notice.
//
// INTERACTION WITH entryIDs, stated rather than discovered later. The caller
// passes maildir entry IDs alongside the text, and those entries are marked
// delivered on the consumption ack — for the WHOLE batch, with no per-line
// correspondence. So a TRUNCATED batch can mark an entry delivered whose citation
// line was cut. Two things bound that: dedup runs first and is lossless, so the
// only way to reach truncation is a genuinely large batch of DISTINCT lines; and
// the truncation marker tells the recipient to call messages_list, where the
// message is still readable (MarkDelivered moves the queue entry, it does not
// delete the mail). It is a "you must go look" degradation, not message loss.
// Dedup cannot hit this at all: mail citations embed a unique per-entry id, so
// two entries can never render the same line.
func boundSystemFrame(text string) string {
	if text == "" {
		return text
	}

	// SplitAfter keeps the trailing newline on each element, so re-joining is
	// exact and a frame with no duplicates round-trips byte-identically.
	parts := strings.SplitAfter(text, "\n")
	seen := make(map[string]struct{}, len(parts))
	var b strings.Builder
	b.Grow(len(text))
	dropped := 0
	for _, ln := range parts {
		if ln == "" {
			// SplitAfter's empty tail after a final newline.
			continue
		}
		// Blank/whitespace-only separators are structural, not content — keep
		// every one rather than collapsing a blank line out of a batch.
		if strings.TrimSpace(ln) != "" {
			if _, dup := seen[ln]; dup {
				dropped++
				continue
			}
			seen[ln] = struct{}{}
		}
		b.WriteString(ln)
	}
	out := b.String()
	if dropped > 0 {
		// The multiplicity is no longer recoverable from the frame, and the
		// liveness type is filtered out of messages_list, so this log line is the
		// only place a pathological backlog becomes observable. It is what SHOULD
		// have made a 123-deep pile visible for two months.
		slog.Default().Info(
			"runtime: collapsed duplicate lines in system frame",
			slog.Int("dropped", dropped),
			slog.Int("bytes_after", len(out)),
		)
	}
	if len(out) <= maxSystemFrameBytes {
		return out
	}

	budget := maxSystemFrameBytes - len(systemFrameTruncationMarker)
	if budget < 0 {
		budget = 0
	}
	head := out[:budget]
	// Cut on a line boundary so the recipient never sees half a notification tag.
	if i := strings.LastIndexByte(head, '\n'); i >= 0 {
		head = head[:i+1]
	}
	slog.Default().Warn(
		"runtime: truncated oversized system frame — notifications were DROPPED and are not redelivered",
		slog.Int("bytes_before", len(out)),
		slog.Int("bytes_after", len(head)+len(systemFrameTruncationMarker)),
		slog.String("dropped_bodies", out[len(head):]),
	)
	return head + systemFrameTruncationMarker
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
	// ORDERING: this MUST stay BEFORE the optimistic setPhaseLocked(phaseSubmitted)
	// below. Move it after and an idle now-write sees inTurn==true, arms the NEXT
	// turn, and hides that turn's genuine error (pinned by
	// TestSendAllNow_NowWriteWhileIdle_DoesNotArm).
	if priority == "now" {
		rt.armInterruptLocked()
	}
	// QUM-903: optimistically enter the synthetic submitted state on a
	// human-typed prompt (kind:user — the watched weave input path) submitted
	// FROM idle, hiding the ~2–10ms submit→running wire latency. Only from idle:
	// a submit while already submitted/running just queues (no new synthetic).
	// kind:system deliveries (spawn prompt, inbox, task) never synthesize —
	// passively-observed agents are driven purely by their wire. This holds for the
	// ROOT too; pinned by TestPhase_SystemMessageFromIdleDoesNotSetInTurn.
	//
	// QUM-925 CONSIDERED AND REJECTED widening this to
	// `kindUser || (kindSystem && cfg.IsRoot)` so an idle weave would enter the
	// synthetic submitted phase on an injected notification. It is not needed and it
	// is not safe:
	//
	//   - Not needed. The synthetic phase does not make the CLI take a turn. The CLI
	//     turns on any queued stdin user message when idle — that is already how a
	//     child's spawn prompt (a kindSystem `next` write in Start) opens its first
	//     turn. QUM-925's "the frame triggers a turn" is satisfied by the stdin
	//     write itself. And nothing reads the result: the only production reader of
	//     UnifiedRuntime.State() is runtime_launcher.go's Liveness check, and the
	//     in_turn the TUI renders for weave comes from WeaveRuntimeHandle.InTurn()
	//     => session.InTurn() — the backend session, not this phase.
	//   - Not safe. This branch calls retireUnclaimedNextArmLocked, whose contract
	//     (see its doc) is a SUPERSEDING USER SUBMIT — an identity signal about the
	//     arm's own submit. A background child ping is not that, and retiring a live
	//     next-turn arm on one resurrects the empty "Session Error" of QUM-927 /
	//     QUM-935. It would also flip inTurnLocked() for weave, changing
	//     armInterruptLocked's answer for an Esc pressed while idle-but-just-poked,
	//     and spawn a guardSubmitted goroutine per notification.
	if kind == kindUser && rt.phase == phaseIdle {
		// Trigger (1): a genuinely NEW user turn (from idle) supersedes an aborted
		// prompt's unclaimed arm. Deliberately keyed on a real new submit rather than
		// on entry to phaseSubmitted, because the QUM-903 guard re-arm at system/init
		// also enters phaseSubmitted for the SAME turn the arm belongs to.
		rt.retireUnclaimedNextArmLocked()
		rt.setPhaseLocked(phaseSubmitted)
	}
	rt.mu.Unlock()

	// QUM-925: a kind:system frame must reach the TUI pending zone, and its only
	// channel is EventUserMessageSent — the zone is uuid-keyed, so without this
	// publish the later isReplay consume ack settles nothing (untracked uuid) and
	// the notification never renders. kind:user writes are published by their
	// caller instead (the TUI's own SendMessage path, and SendAllNow for the
	// coalesced now-write), so publishing here would double them.
	//
	// Published AFTER a successful write, mirroring SendAllNow: publishing first
	// would leave a phantom pending entry on a write error, and ZoneDrop refuses to
	// remove system entries, so that phantom would be permanent. The theoretical
	// inverse — the CLI's isReplay echo racing ahead of this publish — needs a full
	// round-trip to beat ~3 instructions on this goroutine.
	if kind == kindSystem {
		rt.eventBus.Publish(RuntimeEvent{Type: EventUserMessageSent, UUID: uuid, Prompt: text})
	}
	return uuid, nil
}

// InFlightSystemEntryIDs returns the set of durable entry IDs carried by
// kind:system messages this runtime has written and not yet finished delivering
// (QUM-925). The supervisor delivery path uses it to skip re-injecting an entry
// that is already in flight: a maildir entry stays in pending/ until its isReplay
// echo drives OnDelivered → MarkDelivered, so a poke arriving inside that window
// would otherwise re-drain and re-write it — the unbounded stdin write storm
// measured on the child path (see runtime_launcher.go's
// ConfirmDeliveredWithoutReplay comment).
//
// "In flight" deliberately spans statePending AND stateConsumed, not just
// statePending. markConsumed flips the state under outMu, RELEASES it, and only
// then calls OnDelivered → agentloop.MarkDelivered — a rename on a shared
// filesystem. A statePending-only filter goes blind exactly inside that window
// while ListPending still returns the entry, so a concurrent poke writes the same
// notification to stdin twice. Pinned by
// TestWeaveRuntimeHandle_WakeForDelivery_ConsumedButNotYetDelivered_NoDuplicateWrite.
//
// Over-suppression is not a risk FOR A NORMALLY-CONSUMED ENTRY: maildir entry IDs
// are unique and never reused, so an ID that reached stateConsumed via the isReplay
// echo (markConsumed → OnDelivered → MarkDelivered) has been delivered exactly once
// and must never be written again.
//
// SCOPE — read this before relying on the sentence above. stateConsumed reached
// WITHOUT OnDelivered is NOT evidence of delivery, and QUM-1000's settleNeverAcked
// manufactures exactly that: it flips a never-acked entry to stateConsumed
// deliberately without calling OnDelivered, so as not to durably mark an inbox
// message delivered that the CLI never consumed. Such an entry is stateConsumed
// having never been delivered at all, so this filter keeps excluding it and it is
// SUPPRESSED rather than redelivered for the life of the process.
//
// That is correct-by-design here, not a limitation to work around: this filter
// cannot distinguish a genuine echo from a sweep's synthetic flip, and on the
// normal path stateConsumed means the content is already in the conversation, so
// suppressing is the safe answer. And nothing is lost — the outstanding map is
// IN-MEMORY, so a restart clears the marker, WeaveRuntimeHandle's post-start drain
// re-emits the entry, and MarkDelivered never ran, so it is still in pending/.
// "Permanently undeliverable" would be wrong.
//
// Pinned by TestWeaveRuntimeHandle_..._ConsumedStateStaysSuppressed and
// ..._StrandedSystemEntry_SuppressedThenRedeliveredOnRestart. See QUM-1000 and
// QUM-1028. Do not "fix" this by narrowing the predicate back to statePending —
// that reintroduces the duplicate-write window above.
func (rt *UnifiedRuntime) InFlightSystemEntryIDs() map[string]struct{} {
	rt.outMu.Lock()
	defer rt.outMu.Unlock()
	out := make(map[string]struct{})
	for _, e := range rt.outstanding {
		if e.kind != kindSystem || e.state == stateCancelled {
			continue
		}
		for _, id := range e.entryIDs {
			out[id] = struct{}{}
		}
	}
	return out
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
//
// IDEMPOTENT (QUM-1068): both side effects are gated on the entry having actually
// transitioned statePending → stateConsumed, captured inside the outMu critical
// section. A uuid that already left statePending — or that this runtime never
// wrote — fires neither. Exactly one OnDelivered and one publish per uuid, for the
// life of the entry, whichever settle signal arrives first.
//
// This is load-bearing, not hygiene. ConfirmDeliveredWithoutReplay is a bare call
// to this function, and a priority:"now" write IS usually replay-echoed (see that
// function's doc for the measurement), so before the gate every echoed now-write
// called OnDelivered twice — a second agentloop.MarkDelivered on an entry no longer
// in pending/, which fails and logs a WARN on the happy path — and published a
// second EventUserMessageConsumed, which the TUI turns into a second
// TurnIdle → TurnThinking flip (QUM-831) with no turn in flight and no route back
// to idle short of a session restart or the QUM-669 resync.
//
// Deliberately NOT gated: routeFrame calls noteTurnAcked BEFORE this function and
// outside it, so the QUM-1000 ack bookkeeping still counts a late echo for an
// already-swept entry as its turn's ack, and the no-cascade property holds
// independently of whether this publish fires.
func (rt *UnifiedRuntime) markConsumed(uuid string) {
	rt.outMu.Lock()
	e := rt.outstanding[uuid]
	var entryIDs []string
	transitioned := false
	if e != nil && e.state == statePending {
		e.state = stateConsumed
		entryIDs = e.entryIDs
		transitioned = true
	}
	rt.outMu.Unlock()
	if !transitioned {
		return
	}
	if len(entryIDs) > 0 && rt.cfg.OnDelivered != nil {
		rt.cfg.OnDelivered(entryIDs)
	}
	rt.eventBus.Publish(RuntimeEvent{Type: EventUserMessageConsumed, UUID: uuid})
}

// noteRunningTransition fixes the QUM-1000 submit-order watermark at the current
// outSeq. Called from routeFrame on a genuine →phaseRunning transition only.
func (rt *UnifiedRuntime) noteRunningTransition() {
	rt.outMu.Lock()
	rt.lastRunningMark = rt.outSeq
	rt.outMu.Unlock()
}

// noteTurnAcked records that frame turn turnID acked a submission (QUM-1000).
func (rt *UnifiedRuntime) noteTurnAcked(turnID uint64) {
	if turnID == 0 {
		return
	}
	rt.mu.Lock()
	rt.ackedTurnID = turnID
	rt.mu.Unlock()
}

// turnAcked reports whether frame turn turnID acked a submission (QUM-1000).
func (rt *UnifiedRuntime) turnAcked(turnID uint64) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return turnID != 0 && rt.ackedTurnID == turnID
}

// settleNeverAcked settles the OLDEST still-pending kind:user stdin message
// submitted at or before the last running-transition watermark, publishing
// EventUserMessageConsumed for it (QUM-1000). Called from routeFrame's terminal
// leg ONLY when the completing turn acked nothing.
//
// WHY. A slash command the CLI REFUSES — an unknown `/qum1000-nope`, or a real
// builtin the sdk-cli entrypoint declines ("/status isn't available in this
// environment.") — is answered with an ordinary `assistant` refusal text and NO
// isReplay echo, inside an otherwise normal running…result…idle envelope. That
// echo is the ONLY consumption ack (QUM-817), so markConsumed never fires, the
// TUI pending zone never gets its ZoneSettle (QUM-833), and a ghost `› /status`
// row sits in the prompt area indefinitely. Nothing can predict this at submit
// time: `system`/`local_command` is a transcript-only artifact that never reaches
// sprawl, and no predicate over the text separates refused from accepted
// (`/model` and `/context` DO echo; `/etc/hosts is broken…` is prose that
// echoes). So detect after the fact.
//
// THE THREE DISCRIMINATORS, and why each is load-bearing. Settling an entry the
// CLI still holds is not a cosmetic error: snapshotPendingUser only sees
// statePending, so an early settle silently removes the prompt from Ctrl+U recall
// and Ctrl+G send-all-now. Each condition below closes one way to do that.
//
//  1. `seq <= lastRunningMark`. A bare "settle everything pending at turn-end" is
//     the QUM-927/QUM-935 premise class — see the scar on
//     retireUnclaimedNextArmLocked: a turn-end / phase signal is not an identity
//     signal. A prompt submitted at a turn boundary is legitimately still pending
//     when THIS turn's terminal lands.
//  2. THIS TURN ACKED NOTHING (the caller's turnAcked gate). The watermark alone
//     is not enough, because writeMessage stamps seq at SUBMIT time: two prompts
//     typed back-to-back before the wire `running` arrives BOTH land at or below
//     the mark, while the CLI executes them across two turns. Without this gate
//     the first turn's terminal settled the second prompt early and made it
//     unrecallable. An ack proves the turn executed a submission, so nothing else
//     may be settled on its behalf.
//  3. OLDEST ONLY. A turn consumes one queued submission, so at most one entry can
//     have been silently dropped by it. Settling the rest would be the same
//     unrecallability bug for every prompt still queued behind the refused one
//     (e.g. `/status` typed, then a real prompt, both before `running`).
//
// WHY NOT markConsumed. markConsumed fires cfg.OnDelivered(entryIDs), which
// durably marks maildir entries delivered. kind:system entries (inbox drains)
// carry entryIDs, so sweeping one would record an inbox message as delivered that
// the CLI never consumed. This sweep is kind:user-only, and since QUM-925 slice A
// moved inbox drains onto kind:system WITH entryIDs, kind:user entries are
// entryIDs-free BY CONSTRUCTION rather than by coincidence: no OnDelivered call and
// no maildir-delivery path is reachable from this sweep at all. Flipping state and
// publishing EventUserMessageConsumed is all the TUI settle needs.
//
// The cost of that scoping, named so it is not rediscovered as a surprise: a
// never-acked kind:system entry is OUT OF SCOPE here and can strand as a permanent
// dim pending row (QUM-1028 — deliberately parked, and note its obvious fix,
// flipping such an entry to stateConsumed, CEMENTS the wedge because
// InFlightSystemEntryIDs treats stateConsumed as in-flight; see that comment).
//
// Accepted residuals, stated rather than hidden. They are NOT all in the same
// direction, and that difference is the whole risk — do not read the safe class as
// covering the list.
//
// DELAYED settle. The ghost survives until some later turn that acks nothing
// sweeps it. This is the safe direction: nothing the CLI still holds is settled, so
// no prompt loses recallability.
//
//   - a turn that ends with only a wire `idle` and no routed `result` does not
//     sweep;
//   - a turn that acked something else (e.g. an inbox drain delivered in the same
//     turn as a refused command) does not sweep.
//
// EARLY settle. The sweep settles an entry the CLI still holds, which silently
// removes it from Ctrl+U recall and Ctrl+G send-all-now, since snapshotPendingUser
// only sees statePending. This is NOT the safe direction. Both cases are bounded to
// one entry, the oldest, by (3):
//
//   - an isReplay echo observed with no frame turn open records no ack, so such a
//     turn may still sweep;
//   - QUM-1033, the one with real recall harm. outSeq is kind-blind, so a kind:user
//     prompt queued BEFORE a kind:system inbox write lands UNDER the watermark as
//     soon as the injected turn goes running. What keeps it safe is not the
//     watermark but discriminator (2), via the system entry's OWN isReplay echo:
//     routeFrame calls noteTurnAcked for any UserFrame uuid regardless of kind, so
//     the turn "acked something" and the sweep is skipped. If a system turn ends
//     WITHOUT consuming its notification there is no ack, the sweep runs, and the
//     only entry under the watermark is the innocent user prompt. Observed, not
//     reasoned about. Reachable independently of QUM-925 slice A, which raises the
//     exposure (drains are the common kind:system turn) without creating the
//     mechanism. Closing it needs per-turn uuid attribution — WHICH uuid the turn
//     acked, not merely THAT it acked — a change to the ack bookkeeping, not a
//     tightening of a predicate. Do NOT "fix" it by widening snapshotPendingUser to
//     return stateConsumed entries: that converts a recall gap into real message
//     loss, because Ctrl+U would then issue cancel_async_message for a message the
//     CLI is about to execute (QUM-1033's mutation M5, observed). The harm as it
//     stands is DELIVERED-but-unrecallable, not lost.
//
// One delivery caveat on the ordering contract: EventUserMessageConsumed is not in
// isTerminalEvent, so unlike the terminal it is droppable under subscriber
// backpressure. The ORDER relative to the terminal is guaranteed; delivery is not,
// and a dropped consume leaves the ghost until a QUM-669 resync.
//
// Lock discipline: the state flip is under outMu (a leaf lock); the Publish
// happens after it is released.
func (rt *UnifiedRuntime) settleNeverAcked() {
	rt.outMu.Lock()
	oldest := pendingUserSnapshot{}
	for uuid, e := range rt.outstanding {
		if e.kind != kindUser || e.state != statePending || e.seq > rt.lastRunningMark {
			continue
		}
		// The outstanding map iterates randomly, so pick by seq rather than by
		// iteration order.
		if oldest.uuid == "" || e.seq < oldest.seq {
			oldest = pendingUserSnapshot{uuid: uuid, seq: e.seq}
		}
	}
	if oldest.uuid != "" {
		rt.outstanding[oldest.uuid].state = stateConsumed
	}
	rt.outMu.Unlock()
	if oldest.uuid == "" {
		return
	}
	rt.eventBus.Publish(RuntimeEvent{Type: EventUserMessageConsumed, UUID: oldest.uuid})
}

// ConfirmDeliveredWithoutReplay marks an outstanding stdin write consumed without
// WAITING for an isReplay echo (QUM-821). The supervisor calls it on a confirmed
// successful now-priority write to keep the in-memory outstanding map and the
// durable maildir in sync (flip → consumed + OnDelivered) and to publish
// EventUserMessageConsumed. Use ONLY for priority="now" writes; next-class writes
// confirm via the echo.
//
// "WITHOUT" MEANS NOT-RELIED-UPON, NOT NEVER-ARRIVES (QUM-1068). This doc used to
// assert that now-priority messages "are injected directly and are never
// re-emitted via --replay-user-messages". That is false and was load-bearing in
// three places. A chunk-aware census of the wire logs (766 logs / 46 agents,
// 2026-08-04) found 51 of 54 now-writes WERE echoed — 94%, spread across log-start
// dates 2026-06-12 → 2026-07-30, all of them <system-notification> bodies, i.e.
// exactly this call's own path. Reproduced live: a now-frame injected 6s into a
// running turn truncated that turn mid-output at 6003ms AND came back with
// isReplay:true, so it preempts and is echoed.
//
// The remedy still stands on the remaining 3 of 54: the echo cannot be RELIED on,
// so delivery is confirmed on the write. Without that, an un-acked entry stays in
// pending/ and PostTurnSweep re-drains it every turn — the ~30 writes/s storm
// QUM-821 measured against real claude 2.1.173.
//
// When the echo does arrive it drives markConsumed a second time, which is a
// no-op: markConsumed is idempotent (gated on the statePending → stateConsumed
// transition), so exactly one OnDelivered and one publish happen per uuid
// regardless of which signal lands first, or whether both do. "No-op for an
// unknown uuid" is likewise true — including for the post-restart replay echoes of
// uuids written before the in-memory outstanding map was rebuilt.
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
// cancel-and-replace), then confirms that now-write delivered without waiting
// for a replay echo (QUM-824; see ConfirmDeliveredWithoutReplay for why the echo
// cannot be waited on — and, per QUM-1068, why it usually arrives anyway). A
// no-op returning nil if nothing was pending / nothing cancelled.
//
// INVARIANT (QUM-1112): no path may leave text that was flipped out of
// statePending without either re-writing it or handing it back to the caller.
// Cancelling is irreversible from the runtime's side — a cancelled entry is
// filtered out of snapshotPendingUser (so Ctrl+U can never see it again) and its
// EventUserMessageCancelled has already told the TUI to drop the bubble — so an
// early return that discards those texts DESTROYS the user's typed input.
//
// The invariant is "at least one", but the reachable behaviour is stronger and
// deliberate: handback and wire are DISJOINT — no text is ever both returned to
// the caller and written, which would have the user restore and re-send a prompt
// the model already received. See the write-error leg for the one shape that
// could violate that and why it is unreachable.
//
// The returned string is that handback: non-empty ONLY alongside a non-nil
// error, carrying the newline-joined text of the entries that actually
// cancelled, in submit order, for the caller to restore to the input. On success
// it is "" — the text is on the wire, and returning it too would have the caller
// restore a prompt that was already submitted. Entries whose cancel FAILED are
// untouched (still statePending, still queued at the CLI) and are deliberately
// absent from the handback; they remain reachable via Ctrl+U.
func (rt *UnifiedRuntime) SendAllNow(ctx context.Context) (string, error) {
	texts, err := rt.cancelPendingUser(ctx)
	joined := strings.Join(texts, "\n")
	if err != nil {
		// QUM-1112: a partial cancel. `texts` are already flipped out of
		// statePending and their EventUserMessageCancelled already published, so
		// they are gone from Ctrl+U and their TUI bubbles are already dropped.
		// Hand them back rather than aborting with them on the floor. The flush
		// itself aborts (nothing is written): writing these at `now` while the
		// failed-cancel entry is still queued at `next` would silently reorder
		// the user's prompts. Entries whose cancel FAILED are untouched and stay
		// statePending, so they are excluded from `texts` and remain recallable.
		return joined, err
	}
	if len(texts) == 0 {
		return "", nil
	}
	uuid, err := rt.writeMessage(ctx, joined, "now", kindUser, nil, nil)
	if err != nil {
		// QUM-1112: same invariant at the second loss site. writeMessage deletes
		// its own outstanding entry on a write error, so after this point the
		// handback is the only surviving copy of the text.
		//
		// The handback cannot double-submit here. A write error means the frame
		// did not land as a message: protocol.Writer.writeJSON emits ONE NDJSON
		// line per frame, so an EPIPE partway through a >PIPE_BUF write leaves a
		// truncated line with no terminating newline, which the CLI never parses
		// as a message. The one shape that WOULD put a complete frame on the wire
		// alongside an error is transport.Send's ctx-cancel race (it returns
		// ctx.Err() while its writer goroutine may still complete the write) —
		// unreachable today because the sole caller passes context.Background().
		// Should a caller ever thread a cancellable ctx here, the handback stays
		// the right trade anyway: a visible duplicate the user can see and delete
		// beats silently destroying text they cannot recover.
		return joined, err
	}
	// QUM-838: publish EventUserMessageSent (carrying the coalesced text) BEFORE
	// ConfirmDeliveredWithoutReplay so the zone-add lands before the consume
	// settle — otherwise ZoneSettle is a no-op against an untracked uuid and the
	// Ctrl+G message vanishes from the transcript. (QUM-1068: this used to say
	// "a now-write gets NO isReplay echo, so the zone never learns the uuid on
	// its own". False — see ConfirmDeliveredWithoutReplay. It changes nothing
	// here: an echo would arrive as the SETTLE, which is precisely the thing that
	// must not precede the add.) Both publishes happen synchronously on this
	// goroutine, so the
	// EventBus seq-orders sent before consumed.
	rt.eventBus.Publish(RuntimeEvent{Type: EventUserMessageSent, UUID: uuid, Prompt: joined})
	rt.ConfirmDeliveredWithoutReplay(uuid)
	return "", nil
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

	// QUM-925: weave's handle registers an inbox drain here. Start (not handle
	// construction) is the correct anchor: the TUI's Initialize cmd calls Start,
	// which is strictly after NewTUIAdapter subscribed to the EventBus, so the
	// drained frame's EventUserMessageSent is observed and renders. Draining at
	// construction would inject it into the model with nothing watching.
	rt.mu.Lock()
	hook := rt.postStartHook
	rt.mu.Unlock()
	if hook != nil {
		hook()
	}

	return nil
}

// SetPostStartHook registers a callback invoked at the end of a SUCCESSFUL Start,
// after the initial-prompt seed. Must be called before Start. Used by
// WeaveRuntimeHandle to drain an inbox that was already non-empty at startup —
// no producer poke will ever fire for those entries (QUM-925).
//
// Not invoked if the initial-prompt write fails and Start returns early; that
// session's stdin is already broken, so a drain would fail too (and would destroy
// status lines — see drainPendingToStdin). It runs SYNCHRONOUSLY on the Start
// goroutine, which for weave is TUIAdapter.Initialize's tea.Cmd, so a large
// startup inbox delays SessionInitializedMsg by at most one bounded stdin write.
func (rt *UnifiedRuntime) SetPostStartHook(fn func()) {
	rt.mu.Lock()
	rt.postStartHook = fn
	rt.mu.Unlock()
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
	if rt.liveness.Liveness == livenesspkg.Running && rt.liveness.InTurn {
		rt.liveness = livenesspkg.State{Liveness: livenesspkg.Stopping}
	}
	// Arm the interrupt for the turn this abort is aborting, so that turn's
	// terminal frame is re-classified as a clean interrupt by routeFrame instead of
	// surfacing as a turn error. armInterruptLocked owns the three-way
	// current/next/none rule (QUM-931) — including the QUM-927 turn-boundary case
	// (phase idle, frame turn still open) and the QUM-935 submit case (no frame turn
	// open yet, so the terminal belongs to a turn that opens after this arm).
	armed := rt.armInterruptLocked()
	rt.mu.Unlock()

	// Bare contentless abort (Esc). Backends are idempotent and no-op when no
	// turn is in flight.
	err := sess.Interrupt(ctx)

	// QUM-775 item 4: when an interrupt is issued against a genuinely idle runtime,
	// emit a synthetic EventInterrupted so a TUI turnState reducer wedged in
	// TurnStreaming after a dropped terminal event can finalize.
	//
	// Gated on `armed`, not on in-turn. When an arm was recorded a real terminal is
	// inbound and will publish the authoritative EventInterrupted (carrying the
	// result, for "Interrupted (Nms)"), so the synthetic would be a duplicate — and
	// a duplicate is NOT harmless. This gate is the load-bearing part of the
	// StopAfterTurn (QUM-866) / pause-waiter contract: both wait on the SET
	// {TurnCompleted, Interrupted, TurnFailed, BackendFaulted}, so re-classifying
	// among those four is invisible to them, but an EXTRA event can unblock
	// teardown mid-turn. The QUM-775 wedge case is unaffected: a dropped terminal
	// EVENT still means routeFrame processed the terminal FRAME and closed the
	// frame turn, so nothing is armed there and the synthetic still fires.
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

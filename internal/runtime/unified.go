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
	// inTurn is true while the frame router (QUM-817) is observing an in-flight
	// turn (every turn is now router-driven — there is no separate sprawl-turn
	// path). Guarded by mu and OR-ed into State().InTurn.
	inTurn bool
	// interruptPending is set by Interrupt when a user Esc-abort lands mid-turn
	// (QUM-827). routeFrame's EndOfTurn branches read-and-clear it to publish a
	// clean EventInterrupted instead of EventTurnCompleted/EventTurnFailed —
	// otherwise the interrupted turn's is_error `result` frame surfaces as a
	// spurious "Session Error". Guarded by mu. Cleared on turn open (setInTurn
	// true) so a stale flag can never leak into a later turn.
	interruptPending bool

	cancel        context.CancelFunc
	doneWG        sync.WaitGroup
	done          chan struct{}
	closeDoneOnce sync.Once

	// serviced is the QUM-807 dedup set used by the frame router for the QUM-640
	// continuation. Created in New so it is live even before Start.
	serviced *servicedTaskSet
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

// outstandingKind classifies a written user message (QUM-817).
type outstandingKind int

const (
	// kindUser is a human-typed prompt (recallable in the weave TUI — Slice 4).
	kindUser outstandingKind = iota
	// kindSystem is a sprawl-originated message (report_status, inbox, task,
	// continuation, liveness) — NOT user-recallable.
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

// continuationPrompt is the synthetic, machine-originated nudge written to
// stdin at turn-end when a run_in_background task completed (QUM-640). The
// completed task's result is already in the CLI's context as a tool_result;
// this prompt only grants the agent a turn to review it and continue. Terse and
// neutral to avoid steering the agent.
const continuationPrompt = "[auto-continue] A background task you started has completed. Review its output above and continue your work."

// servicedTaskSet is a concurrency-safe set of background-task IDs that have
// already driven an auto-continuation (QUM-807/QUM-817). The frame router
// (reader goroutine) services autonomous-turn task_ids; the dedup prevents a
// continuation turn that re-observes the same task_notification from re-firing
// (infinite loop).
type servicedTaskSet struct {
	mu sync.Mutex
	m  map[string]struct{}
}

func newServicedTaskSet() *servicedTaskSet {
	return &servicedTaskSet{m: map[string]struct{}{}}
}

func (s *servicedTaskSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[id]
	return ok
}

func (s *servicedTaskSet) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = struct{}{}
}

// autonomousTurnState is the frame router's per-turn bookkeeping for an
// in-flight turn (QUM-815/QUM-817). Reader-goroutine-only.
type autonomousTurnState struct {
	open           bool
	sawEmptyTaskID bool
	taskIDs        map[string]struct{}
}

func (a *autonomousTurnState) reset() {
	a.open = false
	a.sawEmptyTaskID = false
	a.taskIDs = nil
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
		serviced:    newServicedTaskSet(),
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
				turnRunning := rt.inTurn
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
// derives the full lifecycle: a balanced EventTurnStarted/EventTurnCompleted,
// an InTurn flip, and the QUM-640 auto-continuation when a background task
// completed (the QUM-812 fix). Runs synchronously on the reader goroutine and
// must not block (bounded EventBus.Publish / Queue.Enqueue only).
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

	st := &rt.autoTurn
	if st.taskIDs == nil {
		st.taskIDs = map[string]struct{}{}
	}

	// Orphan/abort teardown: an autonomous turn ended without a `result`
	// (session close/fault). Revert InTurn and publish a terminal turn event so
	// any turn-boundary waiter (e.g. supervisor Pause) unblocks. Mirrors the
	// TurnLoop's "stream closed without terminal result" semantics.
	if turn.EndOfTurn && msg == nil {
		if st.open {
			rt.setInTurn(false)
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
			st.reset()
		}
		return
	}

	// Render + observe EVERY autonomous frame, including a pre-init trigger.
	if msg != nil {
		rt.eventBus.Publish(RuntimeEvent{Type: EventProtocolMessage, Message: msg})
		if msg.Type == "system" && msg.Subtype == "task_notification" {
			var tn protocol.TaskNotification
			if err := protocol.ParseAs(msg, &tn); err != nil || tn.TaskID == "" {
				st.sawEmptyTaskID = true
			} else if !rt.serviced.has(tn.TaskID) {
				st.taskIDs[tn.TaskID] = struct{}{}
			}
		}
	}

	// Open turn lifecycle (InTurn flip + EventTurnStarted) only on a real turn
	// frame — NEVER on a pre-init trigger, which isn't guaranteed to be followed
	// by an init (a racing StartTurn can absorb it). Otherwise InTurn would leak
	// true and EventTurnStarted would have no matching completion (QUM-815). Any
	// task_id observed from a stranded trigger stays buffered in st.taskIDs and
	// is folded into the next autonomous turn's continuation (deduped).
	if turn.PreInit {
		return
	}
	if !st.open {
		st.open = true
		rt.setInTurn(true)
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
		// QUM-640 continuation (the QUM-812 fix): a background-task completion
		// observed this turn writes exactly one synthetic continuation user
		// message to stdin, which the CLI processes as the next turn. Deduped
		// via the shared serviced set so a continuation turn re-observing the
		// same task_notification does not re-fire (would infinite-loop).
		// QUM-817: this is a direct stdin write now, not a queue enqueue.
		continue640 := st.sawEmptyTaskID || len(st.taskIDs) > 0
		ids := make([]string, 0, len(st.taskIDs))
		for id := range st.taskIDs {
			ids = append(ids, id)
		}
		rt.setInTurn(false)
		st.reset()
		// Fire the post-turn sweep (QUM-580) and write the continuation AFTER
		// clearing per-turn state. Both go to stdin / disk, never under a lock.
		if rt.cfg.PostTurnSweep != nil {
			rt.cfg.PostTurnSweep()
		}
		if continue640 {
			for _, id := range ids {
				rt.serviced.add(id)
			}
			_, _ = rt.writeMessage(context.Background(), continuationPrompt, "next", kindSystem, nil)
		}
	}
}

// setInTurn updates the cross-goroutine InTurn read surface under mu.
func (rt *UnifiedRuntime) setInTurn(v bool) {
	rt.mu.Lock()
	rt.inTurn = v
	// QUM-827: clear any stale pending-interrupt flag on turn open so an
	// interrupt that armed but never produced a terminal frame cannot
	// mis-classify a later turn's completion.
	if v {
		rt.interruptPending = false
	}
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
	return rt.writeMessage(ctx, text, priority, kindUser, nil)
}

// WriteSystemMessage writes a sprawl-originated message (kind:system, not
// recallable) to the CLI stdin (QUM-817). Used by the supervisor delivery path
// for inbox/status/task/liveness notifications and the QUM-640 continuation.
// entryIDs link the message to durable maildir/task records for delivery
// tracking via the isReplay consumption ack.
func (rt *UnifiedRuntime) WriteSystemMessage(ctx context.Context, text, priority string, entryIDs []string) (string, error) {
	return rt.writeMessage(ctx, text, priority, kindSystem, entryIDs)
}

// writeMessage writes one user message to the CLI stdin with the given priority
// + a freshly minted uuid, records it in the outstanding map as pending, and
// returns the uuid (QUM-817). The isReplay echo later flips the entry to
// consumed (see markConsumed). The map entry is recorded BEFORE the stdin write
// so the echo (observed on the reader goroutine) always finds it.
func (rt *UnifiedRuntime) writeMessage(ctx context.Context, text, priority string, kind outstandingKind, entryIDs []string) (string, error) {
	uuid := newUUID()
	rt.outMu.Lock()
	rt.outSeq++
	rt.outstanding[uuid] = &OutstandingEntry{kind: kind, state: statePending, text: text, entryIDs: entryIDs, seq: rt.outSeq}
	rt.outMu.Unlock()

	sid := ""
	if p, ok := rt.cfg.Session.(sessionIDProvider); ok {
		sid = p.SessionID()
	}
	err := rt.cfg.Session.WriteUserMessage(ctx, protocol.UserMessage{
		Type:      "user",
		Message:   protocol.MessageParam{Role: "user", Content: text},
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
	uuid, err := rt.writeMessage(ctx, strings.Join(texts, "\n"), "now", kindUser, nil)
	if err != nil {
		return err
	}
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
		if _, err := rt.writeMessage(runCtx, initialPrompt, "next", kindSystem, nil); err != nil {
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
	inTurn := rt.inTurn
	rt.mu.RUnlock()
	// QUM-817: InTurn is derived entirely from the frame router observing the
	// stdout stream (every turn is router-driven now).
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
	inTurn := rt.inTurn
	if rt.liveness.Liveness == livenesspkg.Running && rt.liveness.InTurn {
		rt.liveness = livenesspkg.State{Liveness: livenesspkg.Stopping}
	}
	// QUM-827: arm the pending-interrupt flag for an in-turn abort so the
	// turn's terminal frame is re-classified as a clean interrupt by
	// routeFrame, not surfaced as a turn error.
	if inTurn {
		rt.interruptPending = true
	}
	rt.mu.Unlock()

	// Bare contentless abort (Esc). Backends are idempotent and no-op when no
	// turn is in flight.
	err := sess.Interrupt(ctx)

	// QUM-775 item 4: when an interrupt is issued against an idle runtime, emit
	// a synthetic EventInterrupted so a TUI turnState reducer wedged in
	// TurnStreaming after a dropped terminal event can finalize. finalizeTurn is
	// idempotent, so a duplicate is harmless.
	if !inTurn && rt.eventBus != nil {
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

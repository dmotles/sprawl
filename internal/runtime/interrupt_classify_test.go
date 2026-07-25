package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-827: a user-initiated Esc-abort that lands MID-TURN must surface as a
// clean interrupt (EventInterrupted → InterruptCompletedMsg "Interrupted"),
// NOT as the interrupted turn's is_error `result` frame (which routeFrame would
// otherwise publish as EventTurnCompleted{IsError} → SessionResultMsg{IsError}
// → the empty "Session Error" γ-overlay). UnifiedRuntime.Interrupt only emitted
// the synthetic EventInterrupted on the !inTurn branch, so an in-turn interrupt
// fell through to the error path.

func resultFrame(t *testing.T, isError bool, durationMs int) *protocol.Message {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type":           "result",
		"subtype":        "success",
		"is_error":       isError,
		"duration_ms":    durationMs,
		"num_turns":      1,
		"total_cost_usd": 0.0,
		"result":         "",
	})
	if err != nil {
		t.Fatalf("marshal result frame: %v", err)
	}
	return &protocol.Message{Type: "result", Subtype: "success", Raw: raw}
}

func openTurn(t *testing.T, rt *UnifiedRuntime) {
	t.Helper()
	// QUM-903: a bare init opens the frame lifecycle but no longer sets in_turn;
	// the authoritative wire `running` signal does. Drive both so the turn is
	// genuinely in flight (InTurn=true) before the test arms its interrupt.
	rt.routeFrame(&protocol.Message{Type: "system", Subtype: "init"}, backend.TurnInfo{Autonomous: true})
	rt.routeFrame(&protocol.Message{Type: "system", Subtype: "session_state_changed"}, backend.TurnInfo{Autonomous: true, StateChange: "running"})
	deadline := time.Now().Add(2 * time.Second)
	for !rt.State().InTurn {
		if time.Now().After(deadline) {
			t.Fatal("turn never entered InTurn")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// openTurnBoundary drives the QUM-927 wire shape: a frame-level turn is open
// (init routed) but the CLI has already reported session_state_changed:idle
// after the model's end_turn while async Agent sidechains are still resolving.
// So State().InTurn is FALSE even though a terminal `result` for that turn is
// still inbound.
func openTurnBoundary(t *testing.T, rt *UnifiedRuntime) {
	t.Helper()
	openTurn(t, rt)
	rt.routeFrame(stateFrame(protocol.SessionStateIdle))
	if rt.State().InTurn {
		t.Fatal("turn-boundary setup: State().InTurn is still true after wire idle; the test would exercise the mid-turn path instead")
	}
	// The whole point of the shape: the FRAME turn is still open, so its terminal
	// `result` is still inbound even though the phase machine reads idle.
	if !frameTurnOpenFlag(rt) {
		t.Fatal("turn-boundary setup: frame turn is not open; there would be no inbound terminal to re-classify")
	}
}

// armedTurnID reads the interrupt arm's target turn id under mu (0 = nothing
// armed). Successor to the QUM-927 interruptPendingFlag.
func armedTurnID(rt *UnifiedRuntime) uint64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.interruptedTurnID
}

// assertPhaseSubmitted pins the precondition to the exact phase, not merely to
// InTurn. `InTurn && !frameTurnOpen` under-specifies the QUM-935 shape: a future
// change that made a submit land as phaseRunning would keep a test green while
// quietly exercising a different shape.
func assertPhaseSubmitted(t *testing.T, rt *UnifiedRuntime) {
	t.Helper()
	rt.mu.RLock()
	p := rt.phase
	rt.mu.RUnlock()
	if p != phaseSubmitted {
		t.Fatalf("setup: phase = %v, want phaseSubmitted (the optimistic submit-from-idle state)", p)
	}
}

// frameTurnOpenFlag reports whether a frame turn is open, under mu. QUM-931: now
// derived from the open turn's id rather than a hand-maintained mirror.
func frameTurnOpenFlag(rt *UnifiedRuntime) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.openTurnID != 0
}

// assertNoTerminalYet drains a window after Interrupt and before the terminal
// frame is routed, asserting NOTHING terminal has been published. Two things
// ride on this: the QUM-775 synthetic must be suppressed while a real terminal is
// inbound (QUM-927 — a spurious turn-boundary signal can unblock StopAfterTurn
// mid-turn), and it keeps the post-terminal window's `interrupted == 1` strictly
// about the re-classification.
func assertNoTerminalYet(t *testing.T, ch <-chan RuntimeEvent) {
	t.Helper()
	interrupted, completed, failed := tallyTerminalEvents(ch, 150*time.Millisecond)
	if interrupted != 0 || completed != 0 || failed != 0 {
		t.Fatalf("pre-terminal window: interrupted=%d completed=%d failed=%d, want 0/0/0 (a real terminal is inbound; no event may be published before it)", interrupted, completed, failed)
	}
}

// waitForBackendFaulted drains ch for `window`, reporting whether an
// EventBackendFaulted was published.
//
// HAZARD — this is destructive-until-match: it discards EVERY event it sees
// before the fault, including EventTurnFailed / EventInterrupted / TurnCompleted.
// A tally* call on the SAME subscription afterwards is therefore dead — it can
// only ever return zero. The two fault tests below survive only because each
// opens a SECOND, independent subscription for its terminal-event tally. If you
// reuse this helper, do the same.
func waitForBackendFaulted(ch <-chan RuntimeEvent, window time.Duration) bool {
	deadline := time.After(window)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return false
			}
			if ev.Type == EventBackendFaulted {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// QUM-927: Esc pressed at a TURN BOUNDARY — after the model's end_turn (wire
// state already `idle`) but while async Agent sidechains are still resolving and
// the frame-level turn is still open. The backend still emits an is_error
// `result` terminal for the interrupt; without arming the pending-interrupt flag
// on this path routeFrame publishes EventTurnCompleted{IsError} → the spurious
// "Session Error" quit/restart modal.
func TestInterrupt_AtTurnBoundary_TrailingIsErrorSurfacesInterrupt(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-esc", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("boundary-esc-test", 32)
	defer unsub()

	openTurnBoundary(t, rt)

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)

	// seq 1163 in the QUM-927 wire capture.
	rt.routeFrame(resultFrame(t, true, 42), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed, lastInterrupt := tallyTerminalEventsWithResult(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (the terminal frame must be re-classified as a clean interrupt)", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0 (interrupted turn must not surface as a completed/error turn → Session Error)", completed)
	}
	if failed != 0 {
		t.Errorf("EventTurnFailed count = %d, want 0", failed)
	}
	if lastInterrupt == nil {
		t.Error("EventInterrupted carried a nil Result; want the interrupted turn's result (for the Interrupted-duration UX)")
	}
}

// QUM-927: same boundary shape, but the interrupt closes the stream with no
// terminal `result` (EndOfTurn && msg==nil) — must still be a clean interrupt,
// not EventTurnFailed{stream-closed}.
func TestInterrupt_AtTurnBoundary_StreamCloseSurfacesInterrupt(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-streamclose", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("boundary-streamclose-test", 32)
	defer unsub()

	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)
	rt.routeFrame(nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (the stream-close terminal must be re-classified as a clean interrupt)", interrupted)
	}
	if completed != 0 || failed != 0 {
		t.Errorf("completed=%d failed=%d, want 0/0", completed, failed)
	}
}

// QUM-927: the arm must survive an intervening wire `running` state change. At a
// turn boundary the CLI commonly reports running again as it processes the
// interrupt / next sidechain frame; setPhaseLocked's idle→non-idle clear would
// otherwise clobber the arm before the terminal is routed, re-opening the bug.
func TestInterrupt_AtTurnBoundary_WireRunningDoesNotClearArm(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-running", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("boundary-running-test", 32)
	defer unsub()

	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)
	rt.routeFrame(stateFrame(protocol.SessionStateRunning))
	rt.routeFrame(resultFrame(t, true, 42), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed, lastInterrupt := tallyTerminalEventsWithResult(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (an intervening wire `running` must not clear the boundary arm)", interrupted)
	}
	if lastInterrupt == nil {
		t.Error("EventInterrupted carried a nil Result; want the re-classified terminal's result")
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0", completed)
	}
	if failed != 0 {
		t.Errorf("EventTurnFailed count = %d, want 0", failed)
	}
}

// QUM-927: with NO frame-level turn open there is no terminal inbound to
// re-classify, so Interrupt must not arm the flag at all — otherwise it sits
// armed and could mis-classify an unrelated later error.
func TestInterrupt_TrulyIdle_NoFrameTurn_DoesNotArm(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-truly-idle", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if got := armedTurnID(rt); got != 0 {
		t.Errorf("interrupt armed turn %d with no frame turn open and nothing in flight; want 0 (no terminal inbound to re-classify)", got)
	}
}

// QUM-927 must not regress QUM-775 item 4: with no frame turn open, no real
// terminal is inbound, so Interrupt still emits the synthetic EventInterrupted
// that unwedges a TUI turnState reducer stuck in TurnStreaming after a dropped
// terminal event. Conversely, at a boundary (a real terminal IS inbound) the
// synthetic is suppressed — a duplicate turn-boundary signal can unblock
// StopAfterTurn (QUM-866) / the pause waiter while the frame turn is still open.
// Both halves are asserted here so the pair of semantics is explicit.
func TestInterrupt_SyntheticFiresOnlyWhenNoTerminalInbound(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-synthetic-gate", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("synthetic-gate-test", 32)
	defer unsub()

	// (a) Genuinely idle, no frame turn: the QUM-775 synthetic still fires.
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	interrupted, _, _, lastInterrupt := tallyTerminalEventsWithResult(ch, 250*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("idle interrupt: EventInterrupted count = %d, want 1 (QUM-775 synthetic)", interrupted)
	}
	if lastInterrupt != nil {
		t.Error("idle interrupt: synthetic EventInterrupted carried a Result; want nil (there is no terminal)")
	}

	// (b) At a boundary a real terminal is inbound, so the synthetic is suppressed
	// and only the re-classified terminal publishes.
	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if interrupted, _, _ := tallyTerminalEvents(ch, 250*time.Millisecond); interrupted != 0 {
		t.Errorf("boundary interrupt: EventInterrupted count = %d before the terminal, want 0 (synthetic must be suppressed)", interrupted)
	}
}

// QUM-927: once the boundary-armed flag is consumed by its own turn's terminal,
// a SUBSEQUENT turn's genuine is_error result must still surface as
// EventTurnCompleted{IsError} — the arm must not leak forward and swallow it.
func TestInterrupt_AtTurnBoundary_ArmDoesNotLeakToNextTurn(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-noleak", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("boundary-noleak-test", 32)
	defer unsub()

	// Turn 1: interrupted at the boundary.
	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)
	rt.routeFrame(resultFrame(t, true, 5), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 1 || completed != 0 {
		t.Fatalf("turn 1: interrupted=%d completed=%d, want 1/0 (the arm must have been armed AND consumed here)", interrupted, completed)
	}

	// Turn 2: a genuine error result, no interrupt. Must NOT be re-classified.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, true, 7), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	interrupted, completed, _ = tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("turn 2: EventInterrupted count = %d, want 0 (boundary arm leaked and swallowed a real error)", interrupted)
	}
	if completed != 1 {
		t.Errorf("turn 2: EventTurnCompleted count = %d, want 1", completed)
	}
}

// QUM-931 (T3) — SUPERSEDES TestInterrupt_AtTurnBoundary_ArmDoesNotSurviveInit.
//
// The no-forward-leak property is real and must be kept, but the OLD test keyed
// it on the wrong event: it asserted that a `system/init` arriving while the
// armed frame turn is still open retires the arm, i.e. `completed == 1` for the
// wire order `interrupt → init → result{is_error}`. That is precisely the
// QUM-935 signature — a user who pressed Esc gets the empty, fatal-looking
// "Session Error" modal on a live session. The old assertion encoded a defect as
// desired behavior (I wrote it during QUM-927, reasoning from mechanism —
// "clear-on-open is gated on !st.open" — rather than from what the user sees).
//
// `init`-while-a-frame-turn-is-open is genuinely AMBIGUOUS on the wire: it can
// mean "the armed turn was abandoned, a new turn's terminal is coming" or "the
// interrupt's own turn is re-initializing and its is_error terminal is still
// coming" (QUM-935). Nothing on the wire disambiguates, so the tiebreak is a
// POLICY choice, and the directions are not symmetric:
//   - retire (old): spurious fatal Session Error on a healthy session. QUM-935
//     reproduced this 2/2. Not rare.
//   - carry (now):  at worst a FOLLOWING turn's genuine soft error reads
//     "Interrupted" instead of "Session Error", and only if the user pressed Esc
//     moments earlier. A genuine crash still surfaces independently via
//     EventBackendFaulted + the EventTurnFailed fault-surface gate.
//
// So forward-leak protection is now keyed on the armed turn CLOSING (its
// terminal, or its orphan teardown) rather than on an ambiguous init. That is
// stronger on turn identity (unambiguous where the init is not) and adds the
// orphan-teardown close case the old test lacked — but on its own it is WEAKER
// on wire-shape coverage, because nothing else in the suite routes an `init`
// while an armed frame turn is open. Deleting that wire shape outright would
// leave "re-add the retire, gated on frameTurnOpen" (i.e. exactly today's
// semantics, which is QUM-935) passing the whole suite. So the deleted test is
// INVERTED, not dropped: see
// TestInterrupt_AtTurnBoundary_InitWhileArmed_TerminalStillSurfacesInterrupt.
func TestInterrupt_AtTurnBoundary_ArmDoesNotSurviveItsTurnClosing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close func(t *testing.T, rt *UnifiedRuntime)
	}{
		{
			name: "armed turn closes via its terminal result",
			close: func(t *testing.T, rt *UnifiedRuntime) {
				rt.routeFrame(resultFrame(t, true, 42), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
			},
		},
		{
			name: "armed turn closes via orphan teardown",
			close: func(t *testing.T, rt *UnifiedRuntime) {
				rt.routeFrame(nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockFaultableSession{}
			rt := New(RuntimeConfig{Name: "weave-arm-turn-close", Session: mock})
			if err := rt.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = rt.Stop(stopCtx)
			}()

			ch, unsub := rt.EventBus().SubscribeNamed("arm-turn-close-test", 32)
			defer unsub()

			openTurnBoundary(t, rt)
			if err := rt.Interrupt(context.Background()); err != nil {
				t.Fatalf("Interrupt: %v", err)
			}
			assertNoTerminalYet(t, ch)

			// The armed turn ends: the arm is consumed exactly here.
			tc.close(t, rt)
			interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
			if interrupted != 1 {
				t.Errorf("armed turn closing: EventInterrupted count = %d, want 1 (the user's abort must classify as a clean interrupt)", interrupted)
			}
			if completed != 0 {
				t.Errorf("armed turn closing: EventTurnCompleted count = %d, want 0", completed)
			}

			// A genuinely NEW turn's genuine error must surface as a real error —
			// the consumed arm cannot leak forward.
			openTurn(t, rt)
			rt.routeFrame(resultFrame(t, true, 9), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
			interrupted, completed, _ = tallyTerminalEvents(ch, 400*time.Millisecond)
			if interrupted != 0 {
				t.Errorf("next turn: EventInterrupted count = %d, want 0 (a consumed arm leaked forward and swallowed a real error)", interrupted)
			}
			if completed != 1 {
				t.Errorf("next turn: EventTurnCompleted count = %d, want 1", completed)
			}
		})
	}
}

// QUM-931/QUM-935 — the INVERSION of the deleted
// TestInterrupt_AtTurnBoundary_ArmDoesNotSurviveInit. Same wire shape the old
// test drove; opposite expectation.
//
//	openTurnBoundary → Interrupt (arms the open turn) → system/init → result{is_error}
//
// The old test asserted `completed == 1` here, which is the QUM-935 empty
// "Session Error" on a live session. The user pressed Esc and the frame turn was
// never closed, so its terminal belongs to the armed turn and must read
// "Interrupted".
//
// This test exists specifically because its wire shape is the ONLY thing that
// can catch a retire re-added under a `frameTurnOpen` gate — which is today's
// shipping semantics. Without it, that mutation passes the entire suite.
func TestInterrupt_AtTurnBoundary_InitWhileArmed_TerminalStillSurfacesInterrupt(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-init-while-armed", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("init-while-armed-test", 32)
	defer unsub()

	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)

	// An init lands while the armed frame turn is still open. It does NOT open a
	// new turn (routeFrame's open is gated on no turn being open), so the terminal
	// below is still the armed turn's.
	feedInit(rt)
	if !frameTurnOpenFlag(rt) {
		t.Fatal("setup: the frame turn closed across the init; this test no longer covers init-while-armed")
	}
	rt.routeFrame(resultFrame(t, true, 9), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (the armed turn's own terminal, after an intervening init)", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0 — an init must not retire an arm belonging to the still-open turn (QUM-935)", completed)
	}
}

// QUM-931/QUM-935 (T1) — the Esc-burst-from-submit ordering, which is the whole
// reason a bare boolean cannot work.
//
// Wire (from the QUM-935 repro, reproduced 2/2 and also on the pre-927 control):
//
//	26  user                              ← submit (optimistic phaseSubmitted)
//	27  control_request subtype:interrupt  ← Esc
//	28  system/init                        ← the arm's OWN turn opens HERE
//	32  result is_error=true               ← the armed turn's terminal
//
// The arm precedes its own turn's init, so an arm that records "the currently
// open turn" records nothing (none is open) and an `armed == current` check
// cannot fire. The correct rule is current-if-open-ELSE-NEXT: with no frame turn
// open but a turn in flight per the phase machine, the terminal to re-classify
// belongs to the turn that opens AFTER the arm.
func TestInterrupt_AfterOptimisticSubmit_InitThenIsErrorSurfacesInterrupt(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-submit-esc-init", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("submit-esc-init-test", 32)
	defer unsub()

	// Submit from idle: the QUM-903 optimistic synthetic makes InTurn true while
	// NO frame turn exists on the wire yet.
	if _, err := rt.WriteUserPrompt(context.Background(), "hi", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	// Assert the precondition explicitly, or this test can silently drift into the
	// QUM-927 boundary shape (frame turn open) and stop covering QUM-935 at all.
	if !rt.State().InTurn {
		t.Fatal("setup: State().InTurn is false after an optimistic submit; the phase machine did not enter phaseSubmitted")
	}
	assertPhaseSubmitted(t, rt)
	if frameTurnOpenFlag(rt) {
		t.Fatal("setup: a frame turn is already open; this test would exercise the QUM-927 boundary path instead of the QUM-935 submit path")
	}

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	// Nothing terminal yet — and in particular the QUM-775 synthetic must be
	// suppressed, because a real terminal IS inbound for the armed turn.
	assertNoTerminalYet(t, ch)

	// The arm's OWN turn opens here, then terminates with the interrupt's is_error.
	feedInit(rt)
	rt.routeFrame(resultFrame(t, true, 5), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (an Esc burst from submit must read \"Interrupted\")", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0 — this is the QUM-935 empty \"Session Error\" modal on a session whose backend never died", completed)
	}
}

// QUM-931/QUM-935 (T1b) — the same shape with NO init frame at all. A bare
// `result` opens a frame turn itself (routeFrame's open-on-any-turn-frame path),
// so the fix must not depend on observing the init.
func TestInterrupt_AfterOptimisticSubmit_BareIsErrorSurfacesInterrupt(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-submit-esc-bare", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("submit-esc-bare-test", 32)
	defer unsub()

	if _, err := rt.WriteUserPrompt(context.Background(), "hi", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	if !rt.State().InTurn {
		t.Fatal("setup: State().InTurn is false after an optimistic submit")
	}
	assertPhaseSubmitted(t, rt)
	if frameTurnOpenFlag(rt) {
		t.Fatal("setup: a frame turn is already open; wrong shape for this test")
	}
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)

	rt.routeFrame(resultFrame(t, true, 5), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (the fix must not depend on seeing the init frame)", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0", completed)
	}
}

// QUM-931 (T2) — the fix-CONSTRAINING half of T1, and the reason T1 alone is not
// enough. "Arm the next turn unconditionally" (i.e. drop the do-not-arm branch)
// satisfies T1 while silently swallowing the genuine error of a turn the user
// never interrupted. A truly idle Esc must arm NOTHING.
func TestInterrupt_TrulyIdle_ThenUnrelatedTurnErrors_StillSurfacesError(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-idle-esc-then-error", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("idle-esc-then-error-test", 32)
	defer unsub()

	// Genuinely idle: no submit, no frame turn.
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	// Nothing was armed, so the QUM-775 synthetic fires (it is what unwedges a TUI
	// stuck after a dropped terminal event). One drain per phase, with a routeFrame
	// between: a single drain covering both phases would consume this synthetic and
	// leave the second assertion structurally unable to fail.
	interrupted, completed, _ := tallyTerminalEvents(ch, 300*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("idle Esc: EventInterrupted count = %d, want 1 (the QUM-775 synthetic must fire when nothing is armed)", interrupted)
	}
	if completed != 0 {
		t.Errorf("idle Esc: EventTurnCompleted count = %d, want 0", completed)
	}

	// A LATER, unrelated turn errors. The user never interrupted it.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, true, 11), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, _ = tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("unrelated turn: EventInterrupted count = %d, want 0 (an idle Esc must not arm a future turn and hide its real error)", interrupted)
	}
	if completed != 1 {
		t.Errorf("unrelated turn: EventTurnCompleted count = %d, want 1", completed)
	}
}

// QUM-931 (T4) — pins the semantics of a next-turn arm: it is ONE-SHOT and it
// DOES claim the next turn to open, whatever that turn's terminal looks like:
//   - classification does not depend on is_error. The existing pin for that is
//     TestInterrupt_AtTurnBoundary_CleanTerminalStillInterrupt (the QUM-927
//     BOUNDARY path — not the QUM-827 mid-turn path, which never routes
//     is_error=false). Kept uniform here so one rule covers all arm targets.
//   - the arm is spent, so a SECOND turn's genuine error surfaces normally.
//
// A next-arm IS bounded — see
// TestInterrupt_NextTurnArm_NeverOpened_RetiredOnReturnToIdle. An earlier draft
// of this comment claimed a clock-free bound would re-break QUM-935; that was
// wrong, and it was disproved by implementing it. The bound cannot fire in the
// QUM-935 shape because there the armed turn HAS opened by then
// (turnSeq >= interruptedTurnID) and the arm is already consumed.
func TestInterrupt_NextTurnArmResolvesExactlyOnce(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-next-arm-once", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("next-arm-once-test", 32)
	defer unsub()

	if _, err := rt.WriteUserPrompt(context.Background(), "hi", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)

	// The claimed turn terminates CLEANLY — still an interrupt.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, false, 21), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("claimed turn: EventInterrupted count = %d, want 1 (classification does not depend on is_error)", interrupted)
	}
	if completed != 0 {
		t.Errorf("claimed turn: EventTurnCompleted count = %d, want 0", completed)
	}

	// The arm is spent: a second turn's genuine error surfaces.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, true, 22), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	interrupted, completed, _ = tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("second turn: EventInterrupted count = %d, want 0 (a next-turn arm must be one-shot)", interrupted)
	}
	if completed != 1 {
		t.Errorf("second turn: EventTurnCompleted count = %d, want 1", completed)
	}
}

// QUM-931 — an unclaimed next-arm is retired when its own submit's
// SUBMITTED-TIMEOUT expires: the backend never acked that submit, so no turn is
// coming for it. Trigger (2) of retireUnclaimedNextArmLocked.
//
// SUPERSEDES an earlier TestInterrupt_NextTurnArm_NeverOpened_RetiredOnReturnToIdle,
// which asserted the retire fired on any return to idle. That test PASSED and was
// still wrong — it pinned a phase-triggered mechanism that drops a live arm on the
// previous turn's residual idle (QUM-935 resurrected; see
// TestInterrupt_NextTurnArm_SurvivesResidualWireIdle). Third time in this series a
// test of mine ratified the mechanism instead of the outcome, so: assert the
// property (an arm whose submit is provably dead is retired) via a signal that is
// about the arm, not a wire event that merely correlates with it.
func TestInterrupt_NextTurnArm_RetiredBySubmittedTimeout(t *testing.T) {
	withShortSubmittedTimeout(t, 40*time.Millisecond)

	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-arm-submitted-timeout", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("arm-submitted-timeout-test", 32)
	defer unsub()

	if _, err := rt.WriteUserPrompt(context.Background(), "hi", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	assertPhaseSubmitted(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if armedTurnID(rt) == 0 {
		t.Fatal("setup: the Esc did not arm anything")
	}

	// The backend never opens a turn; the submitted-timeout guard fires for THIS
	// submit. Wait for it rather than sleeping blind.
	deadline := time.Now().Add(2 * time.Second)
	for rt.State().InTurn {
		if time.Now().After(deadline) {
			t.Fatal("submitted-timeout guard never returned the phase to idle")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := armedTurnID(rt); got != 0 {
		t.Errorf("armed turn id = %d after this submit's timeout expired; want 0 (no turn is coming for it)", got)
	}

	// A LATER, unrelated turn errors genuinely.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, true, 17), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("EventInterrupted count = %d, want 0 (a stale next-arm swallowed a real error)", interrupted)
	}
	if completed != 1 {
		t.Errorf("EventTurnCompleted count = %d, want 1", completed)
	}
}

// QUM-931 — a RESIDUAL wire `idle` must NOT retire a live next-arm.
//
// This is the counter-test to the retire, and it is why the retire cannot be
// triggered by the phase returning to idle. A trailing
// session_state_changed:idle from the PREVIOUS turn routinely lands after a new
// prompt has already been submitted — the normal `result` → `idle` wire order
// (see TestInterrupt_AfterTurnCompleted_TreatedAsIdle) means an Esc burst lives
// exactly at that boundary. A phase-triggered retire kills the arm there and the
// turn's own is_error terminal then surfaces as the QUM-935 empty "Session
// Error".
//
// `p == phaseIdle` is a PHASE signal, not an identity signal: it cannot
// distinguish "the CLI abandoned the turn before opening it" from "the turn has
// not opened YET". Reaching for it was the same premise class that produced
// QUM-927 and QUM-935 — a hand-maintained wire-ordering guess. The retire is
// therefore ordered against the ARM instead (a superseding submit), see
// TestInterrupt_NextTurnArm_RetiredByASupersedingSubmit.
func TestInterrupt_NextTurnArm_SurvivesResidualWireIdle(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-residual-idle", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("residual-idle-test", 32)
	defer unsub()

	// A previous turn ends. Its trailing wire `idle` has not arrived yet.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, false, 1), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	tallyTerminalEvents(ch, 200*time.Millisecond)

	// The user submits again and immediately bursts Esc.
	if _, err := rt.WriteUserPrompt(context.Background(), "hi", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	assertPhaseSubmitted(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	armed := armedTurnID(rt)
	if armed == 0 {
		t.Fatal("setup: the Esc did not arm anything")
	}

	// NOW the previous turn's residual idle lands. It says nothing about the arm.
	rt.routeFrame(stateFrame(protocol.SessionStateIdle))
	if got := armedTurnID(rt); got != armed {
		t.Errorf("armed turn id = %d after a residual wire idle, want %d retained — a previous turn's trailing idle must not retire a live arm", got, armed)
	}

	// The armed turn then opens and terminates with the interrupt's is_error.
	feedInit(rt)
	rt.routeFrame(resultFrame(t, true, 7), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0 — the residual idle dropped the arm and resurrected the QUM-935 empty \"Session Error\"", completed)
	}
}

// QUM-931 — an unclaimed next-arm IS retired, by a SUPERSEDING SUBMIT: a new
// user turn means the aborted prompt's turn is never opening, so its arm must not
// sit waiting to claim the new one.
//
// This is the arm-ordered replacement for the phase-triggered bound (see
// TestInterrupt_NextTurnArm_SurvivesResidualWireIdle for why the phase cannot be
// the trigger). It fires on a signal that is genuinely ABOUT the arm — a fresh
// submit — rather than on a wire event that merely correlates with one.
//
// Residual risk, accepted and documented rather than papered over: between an
// abandoned submit and the next submit, an AUTONOMOUS turn could open and claim
// the arm, mislabelling its terminal "Interrupted". That window is narrow and the
// cost is one soft error mislabelled, whereas the phase-triggered alternative
// costs a spurious fatal "Session Error" — the exact bug class this whole series
// is about. A genuine crash still surfaces either way, independently, via
// EventBackendFaulted plus the EventTurnFailed fault-surface gate.
func TestInterrupt_NextTurnArm_RetiredByASupersedingSubmit(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-superseding-submit", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("superseding-submit-test", 32)
	defer unsub()

	// Submit + Esc, and the CLI never opens a turn for it.
	if _, err := rt.WriteUserPrompt(context.Background(), "first", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if armedTurnID(rt) == 0 {
		t.Fatal("setup: the Esc did not arm anything")
	}
	// Return to idle (the abort landed) — this alone must NOT retire the arm.
	rt.routeFrame(stateFrame(protocol.SessionStateIdle))

	// A genuinely NEW user turn supersedes the aborted one.
	if _, err := rt.WriteUserPrompt(context.Background(), "second", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	if got := armedTurnID(rt); got != 0 {
		t.Errorf("armed turn id = %d after a superseding submit; want 0 (the aborted prompt's arm must not claim the new turn)", got)
	}

	// The new turn genuinely errors. The user never interrupted it.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, true, 17), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("EventInterrupted count = %d, want 0 (a stale next-arm swallowed the new turn's real error)", interrupted)
	}
	if completed != 1 {
		t.Errorf("EventTurnCompleted count = %d, want 1", completed)
	}
}

// QUM-931 — WHITE-BOX: a terminal must consume ONLY an arm bearing its own turn
// id. This is the property the whole refactor rests on ("a mismatched id is
// inherently a no-op, so nothing needs to clear"), and with the current frame
// router it is UNREACHABLE through the wire: a turn cannot open while one is
// open, both consume sites run before closeFrameTurn zeroes the id, and a
// next-arm of turnSeq+1 is by construction the next turn to open.
//
// So it is asserted by stuffing a stale id directly. That is deliberate: an
// id-IGNORING consume passes every black-box test in this package, which means
// without this test the id would be decoration and the "nothing needs to clear"
// rationale would be load-bearing reasoning that no test confirms. That exact
// shape — an invariant held only in the author's head — produced all four bugs in
// this series.
func TestConsumeInterrupt_StaleTurnID_DoesNotConsume(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-stale-id", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("stale-id-test", 32)
	defer unsub()

	openTurn(t, rt)
	open := openFrameTurnID(t, rt)

	// An arm for some OTHER turn (as a future router change, or a bug, might leave
	// behind). The open turn's terminal must ignore it.
	stuffArmedTurnID(rt, open+7)

	rt.routeFrame(resultFrame(t, true, 3), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("EventInterrupted count = %d, want 0 — a terminal consumed an arm belonging to a DIFFERENT turn; turn identity is not being checked", interrupted)
	}
	if completed != 1 {
		t.Errorf("EventTurnCompleted count = %d, want 1", completed)
	}
	if got := armedTurnID(rt); got != open+7 {
		t.Errorf("armed turn id = %d, want %d left intact (a non-matching consume must not clear another turn's arm)", got, open+7)
	}
}

// QUM-931 — WHITE-BOX: every frame turn must get a DISTINCT id, and a closed turn
// must leave no turn open. Each was found individually UNTESTED by mutation:
//
//   - "openFrameTurn does not bump turnSeq" (every turn reusing one id) survived
//     the whole suite, because consumeInterrupt's zeroing masked the resulting
//     forward leak;
//   - "consumeInterrupt does not zero the arm" also survived, because distinct ids
//     masked it.
//
// Each mechanism hid the other's failure, so the suite tolerated either mutation
// alone while BOTH together are a forward leak — a stale arm swallowing a later
// turn's genuine error, which is bug #1 of this series. Asserting id uniqueness
// directly is what breaks the mutual masking.
func TestFrameTurn_IdsAreUniquePerTurn(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-turn-ids", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	seen := map[uint64]bool{}
	for i := 0; i < 3; i++ {
		openTurn(t, rt)
		id := openFrameTurnID(t, rt)
		if seen[id] {
			t.Fatalf("turn %d reused frame-turn id %d; ids must be unique or a stale arm can match a later turn", i+1, id)
		}
		seen[id] = true

		rt.routeFrame(resultFrame(t, false, 1), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
		if frameTurnOpenFlag(rt) {
			t.Fatalf("turn %d: a frame turn is still open after its terminal `result`", i+1)
		}
	}
}

// openFrameTurnID returns the currently-open frame turn's id, failing if none is
// open.
func openFrameTurnID(t *testing.T, rt *UnifiedRuntime) uint64 {
	t.Helper()
	rt.mu.RLock()
	id := rt.openTurnID
	rt.mu.RUnlock()
	if id == 0 {
		t.Fatal("no frame turn is open")
	}
	return id
}

// stuffArmedTurnID plants an arm for an arbitrary turn id. Test-only: the wire
// cannot produce this state (see TestConsumeInterrupt_StaleTurnID_DoesNotConsume).
func stuffArmedTurnID(rt *UnifiedRuntime, id uint64) {
	rt.mu.Lock()
	rt.interruptedTurnID = id
	rt.mu.Unlock()
}

// QUM-931 (T6) — closes a mutation with NO coverage today: the now-write arm in
// writeMessage must be evaluated BEFORE the optimistic setPhaseLocked(submitted)
// in the same critical section. Move it after and an idle now-write starts
// arming the next turn, hiding that turn's genuine error — and the entire
// existing suite stays green. A now-write while genuinely idle preempts nothing,
// so it must arm nothing.
func TestSendAllNow_NowWriteWhileIdle_DoesNotArm(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave-nowwrite-idle", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("nowwrite-idle-test", 32)
	defer unsub()

	// Genuinely idle: phase idle, no frame turn open.
	if rt.State().InTurn {
		t.Fatal("setup: InTurn is true before any submit")
	}
	if _, err := rt.WriteUserPrompt(context.Background(), "urgent", "now"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}

	// A turn then runs and genuinely errors. Nothing preempted it.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, true, 13), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("EventInterrupted count = %d, want 0 (an idle now-write preempts nothing, so it must not arm)", interrupted)
	}
	if completed != 1 {
		t.Errorf("EventTurnCompleted count = %d, want 1", completed)
	}
}

// QUM-927: a boundary interrupt whose turn terminates CLEANLY (is_error=false)
// is still classified as an interrupt — the user did press Esc, and the
// classification deliberately does not depend on is_error (same contract as the
// QUM-827 mid-turn path). Pinned so the semantic is explicit, not accidental.
func TestInterrupt_AtTurnBoundary_CleanTerminalStillInterrupt(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-clean", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("boundary-clean-test", 32)
	defer unsub()

	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)
	rt.routeFrame(resultFrame(t, false, 12), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed, lastInterrupt := tallyTerminalEventsWithResult(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (classification does not depend on is_error)", interrupted)
	}
	if lastInterrupt == nil {
		t.Error("EventInterrupted carried a nil Result; want the re-classified terminal's result")
	}
	if completed != 0 || failed != 0 {
		t.Errorf("completed=%d failed=%d, want 0/0", completed, failed)
	}
}

// QUM-927 AC: a genuine backend fault at a turn boundary with NO preceding
// interrupt still surfaces EventBackendFaulted AND an EventTurnFailed terminal.
func TestGenuineFault_NoPrecedingInterrupt_StillFaultsAtBoundary(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-fault", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	faultCh, unsubFault := rt.EventBus().SubscribeNamed("boundary-fault-fault", 32)
	defer unsubFault()
	turnCh, unsubTurn := rt.EventBus().SubscribeNamed("boundary-fault-turn", 32)
	defer unsubTurn()

	openTurnBoundary(t, rt)
	mock.fireTerminalErr(backend.ErrSubprocessExited)
	rt.routeFrame(nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	if !waitForBackendFaulted(faultCh, time.Second) {
		t.Error("EventBackendFaulted was not published for a genuine subprocess-exit fault")
	}
	// TWO EventTurnFailed publishes are expected, from two distinct publishers:
	// the SetTerminalErrorHandler closure (carrying the REAL fault) and the
	// orphan-teardown branch in routeFrame (carrying the generic
	// errStreamClosedNoResult, because the stream closed with no terminal
	// `result`). This is the same shape the mid-turn path has always had; the
	// QUM-927 rework widened the handler's gate to include frameTurnOpen so the
	// boundary path matches it.
	//
	// The double publish is NOT harmless, which is why QUM-967 tracks it: both
	// translate to SessionResultMsg{IsError} and the second one CLOBBERS the
	// first's text (app.go rebuilds errorDialog from msg.Result), so on the no-arm
	// fault path the user reads the generic "autonomous turn stream closed without
	// terminal result" instead of the real "claude subprocess exited unexpectedly".
	// The assertion below is what keeps the REAL fault present at all.
	//
	// ONE drain feeds every assertion below. Splitting it would silently kill the
	// misclassification guard: a second drain over the same subscription reads an
	// already-empty channel, so `interrupted` could only ever be 0 and the "a real
	// fault must not surface as a clean interrupt" check — the exact property
	// QUM-927 regressed — would pass unconditionally.
	interrupted, _, failures := tallyTerminalEventsWithErrors(turnCh, 400*time.Millisecond)
	if len(failures) != 2 {
		t.Errorf("EventTurnFailed count = %d, want 2 (the terminal-error handler AND the orphan teardown); errs=%v", len(failures), failures)
	}
	// The load-bearing assertion: one of them must name the REAL fault. Without
	// this, the widened gate could regress and the count could still be met by
	// the orphan branch alone plus any other generic failure.
	var sawRealFault bool
	for _, err := range failures {
		if errors.Is(err, backend.ErrSubprocessExited) {
			sawRealFault = true
		}
	}
	if !sawRealFault {
		t.Errorf("no EventTurnFailed carried the real fault (%v); the fault-surface gate is suppressing it at the turn boundary, so the TUI cannot name the failure; errs=%v",
			backend.ErrSubprocessExited, failures)
	}
	if interrupted != 0 {
		t.Errorf("EventInterrupted count = %d, want 0 (a genuine fault with no preceding interrupt must never be misclassified as a clean interrupt)", interrupted)
	}
}

// QUM-927 AC: even when a boundary interrupt DID precede a genuine fault, the
// session-fault surface is independent — EventBackendFaulted must still fire,
// AND a real EventTurnFailed naming the fault must still be published.
//
// The rework corrected what this test asserts. It previously demanded failed==0
// on the theory that the arm's re-label of the TEARDOWN event was the whole
// story. It isn't: EventBackendFaulted has no root-session consumer
// (runFaultSubscriber is children-only) and TranslateRuntimeEvent maps it to
// nil, so "only the turn event is re-labelled" meant the TUI surfaced NOTHING
// for a dead subprocess. Asserting failed==0 here actively codified that gap —
// a bus-level test cannot see an event the TUI never consumes. The reducer-level
// counterpart lives in internal/tui/app_boundary_fault_test.go.
func TestGenuineFault_AfterBoundaryInterrupt_StillPublishesBackendFaulted(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-fault-esc", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	// Two subscriptions so draining for the fault event cannot consume the turn
	// event (their relative order is not guaranteed).
	faultCh, unsubFault := rt.EventBus().SubscribeNamed("boundary-fault-esc-fault", 32)
	defer unsubFault()
	ch, unsub := rt.EventBus().SubscribeNamed("boundary-fault-esc-turn", 32)
	defer unsub()

	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)
	mock.fireTerminalErr(backend.ErrSubprocessExited)
	rt.routeFrame(nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	if !waitForBackendFaulted(faultCh, time.Second) {
		t.Error("EventBackendFaulted was suppressed by the interrupt re-classification; real faults must not be swallowed")
	}
	// The arm re-labels the TEARDOWN event as a clean interrupt, but the fault
	// must ALSO surface as a real EventTurnFailed naming it — that is the event
	// the root TUI actually consumes (→ SessionResultMsg{IsError} → the "Session
	// Error" dialog with [r] restart).
	interrupted, completed, failed, _ := tallyTerminalEventsWithResult(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (the interrupted turn is re-labelled, exactly once)", interrupted)
	}
	if failed != 1 {
		t.Errorf("EventTurnFailed count = %d, want 1 (the genuine fault must still fail the turn so the TUI can surface it); "+
			"0 here means a dead subprocess renders as a clean \"Interrupted\"", failed)
	}
	if completed != 0 {
		t.Errorf("completed=%d, want 0", completed)
	}
}

// QUM-927 / QUM-830: a Ctrl+G send-all-now priority:"now" write at a turn
// boundary preempts the still-open turn the same way, producing the identical
// is_error terminal — it must classify as a clean interrupt too.
func TestSendAllNow_NowWriteAtTurnBoundary_SurfacesInterrupt(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave-sendnow-boundary", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("sendnow-boundary-test", 32)
	defer unsub()

	writePendingUser(t, rt, mock, "send me now", "next")
	openTurnBoundary(t, rt)
	if err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}
	rt.routeFrame(resultFrame(t, true, 42), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed := tallyTerminalEvents(ch, 750*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (boundary now-write preempt must surface as a clean interrupt)", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0", completed)
	}
	if failed != 0 {
		t.Errorf("EventTurnFailed count = %d, want 0", failed)
	}
}

// tallyTerminalEvents drains ch for `window` and counts the terminal turn
// events. Returns (interrupted, completed, failed).
func tallyTerminalEvents(ch <-chan RuntimeEvent, window time.Duration) (int, int, int) {
	interrupted, completed, failed, _ := tallyTerminalEventsWithResult(ch, window)
	return interrupted, completed, failed
}

// tallyTerminalEventsWithErrors is tallyTerminalEvents plus the actual
// EventTurnFailed errors, so a test can assert WHICH fault surfaced and not
// merely that one did — "some turn-failed event fired" is satisfied by the
// orphan branch's generic errStreamClosedNoResult, which would hide a
// regression of the real fault surface.
//
// It returns everything from ONE drain on purpose. A channel drain is
// destructive, so two sequential drains over the same subscription cannot both
// see the same events: the first consumes the window and any assertion built on
// the second is dead — it can only ever observe zero. (QUM-927 rework)
func tallyTerminalEventsWithErrors(ch <-chan RuntimeEvent, window time.Duration) (interrupted, completed int, failures []error) {
	deadline := time.After(window)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return interrupted, completed, failures
			}
			switch ev.Type {
			case EventInterrupted:
				interrupted++
			case EventTurnCompleted:
				completed++
			case EventTurnFailed:
				failures = append(failures, ev.Error)
			}
		case <-deadline:
			return interrupted, completed, failures
		}
	}
}

// QUM-830: send-all-now (Ctrl+G) writes one priority:"now" message that
// PREEMPTS the in-flight model turn (cancel-and-replace). The preempted turn
// emits an is_error `result` terminal frame — the SAME shape an Esc-abort
// produces. Without arming the pending-interrupt flag on the now-write,
// routeFrame publishes EventTurnCompleted{IsError} → SessionResultMsg{IsError}
// → the empty "Session Error" overlay → session restart. QUM-827 fixed this for
// the bare-Esc path only; the now-write preempt is a separate entry that must
// be classified as a clean interrupt too.
func TestSendAllNow_NowWritePreemptMidTurn_SurfacesInterruptNotError(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave-sendnow-preempt", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("sendnow-preempt-test", 32)
	defer unsub()

	// A human-typed prompt queued behind the in-flight turn.
	writePendingUser(t, rt, mock, "send me now", "next")

	// Turn in flight.
	openTurn(t, rt)

	// Ctrl+G send-all-now: cancels the pending prompt and writes one now-priority
	// message that preempts the in-flight turn.
	if err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}

	// The preempted turn's terminal is_error result.
	rt.routeFrame(resultFrame(t, true, 42), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed, lastInterrupt := tallyTerminalEventsWithResult(ch, 750*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (now-write preempt must surface as a clean interrupt)", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0 (preempted turn must not surface as a completed/error turn → Session Error)", completed)
	}
	if failed != 0 {
		t.Errorf("EventTurnFailed count = %d, want 0", failed)
	}
	// The interrupt carries the terminal result so the TUI can render
	// "Interrupted (Nms)" rather than an empty/error overlay.
	if lastInterrupt == nil {
		t.Error("EventInterrupted carried a nil Result; want the preempted turn's result (for the Interrupted-duration UX)")
	}
}

// tallyTerminalEventsWithResult is tallyTerminalEvents plus the Result of the
// last EventInterrupted seen (nil if none) — used to assert the preempt path
// carries the terminal result for the "Interrupted (Nms)" UX.
func tallyTerminalEventsWithResult(ch <-chan RuntimeEvent, window time.Duration) (int, int, int, *protocol.ResultMessage) {
	var interrupted, completed, failed int
	var lastInterrupt *protocol.ResultMessage
	deadline := time.After(window)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return interrupted, completed, failed, lastInterrupt
			}
			switch ev.Type {
			case EventInterrupted:
				interrupted++
				lastInterrupt = ev.Result
			case EventTurnCompleted:
				completed++
			case EventTurnFailed:
				failed++
			}
		case <-deadline:
			return interrupted, completed, failed, lastInterrupt
		}
	}
}

// TestSendAllNow_NowWriteArm_DoesNotLeakToNextTurn guards the QUM-827 stale-flag
// invariant for the new arm site: once the preempted turn consumes the pending-
// interrupt flag, a SUBSEQUENT clean turn completion must publish
// EventTurnCompleted, not EventInterrupted.
func TestSendAllNow_NowWriteArm_DoesNotLeakToNextTurn(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave-sendnow-noleak", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("sendnow-noleak-test", 32)
	defer unsub()

	// Turn 1: preempted by send-all-now.
	writePendingUser(t, rt, mock, "send me now", "next")
	openTurn(t, rt)
	if err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}
	rt.routeFrame(resultFrame(t, true, 5), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	if interrupted, _, _ := tallyTerminalEvents(ch, 400*time.Millisecond); interrupted != 1 {
		t.Fatalf("turn 1: EventInterrupted count = %d, want 1", interrupted)
	}

	// Turn 2: clean completion — must NOT inherit the consumed interrupt flag.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, false, 7), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("turn 2: EventInterrupted count = %d, want 0 (now-write arm leaked)", interrupted)
	}
	if completed != 1 {
		t.Errorf("turn 2: EventTurnCompleted count = %d, want 1", completed)
	}
}

func TestUnifiedRuntime_InTurnInterruptEmitsEventInterrupted(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "agent-esc-interrupt", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("esc-interrupt-test", 32)
	defer unsub()

	openTurn(t, rt)

	// User Esc mid-turn.
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// claude's interrupted-turn terminal result (is_error, empty text).
	rt.routeFrame(resultFrame(t, true, 42), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed := tallyTerminalEvents(ch, 750*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (in-turn interrupt must surface as a clean interrupt)", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0 (interrupted turn must not surface as a completed/error turn)", completed)
	}
	if failed != 0 {
		t.Errorf("EventTurnFailed count = %d, want 0", failed)
	}
}

// TestUnifiedRuntime_InTurnInterrupt_StreamClose covers the alternate path
// where the interrupt closes the stream with no terminal `result` frame
// (EndOfTurn && msg==nil): it too must surface as EventInterrupted, not the
// EventTurnFailed{stream-closed} error.
func TestUnifiedRuntime_InTurnInterrupt_StreamClose(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "agent-esc-streamclose", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("esc-streamclose-test", 32)
	defer unsub()

	openTurn(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	rt.routeFrame(nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed := tallyTerminalEvents(ch, 750*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (stream-close after interrupt is still a clean interrupt)", interrupted)
	}
	if completed != 0 || failed != 0 {
		t.Errorf("completed=%d failed=%d, want 0/0", completed, failed)
	}
}

// TestUnifiedRuntime_InterruptIsQueueNonDestructive pins the locked QUM-827 /
// QUM-828 contract: Esc is a pure halt — UnifiedRuntime.Interrupt must NOT
// touch the outstanding-map queue. A queued (kind:user, state:pending) entry
// must survive the abort unchanged so the CLI consumes it on its next
// iteration.
func TestUnifiedRuntime_InterruptIsQueueNonDestructive(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "agent-esc-queue", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	// Queue a pending user message, then open a turn.
	uuid, err := rt.WriteUserPrompt(context.Background(), "queued while busy", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	openTurn(t, rt)

	// User Esc mid-turn.
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// The queued entry must still be present and still pending.
	out := rt.Outstanding()
	e, ok := out[uuid]
	if !ok {
		t.Fatalf("queued message %s was dropped by Interrupt; the abort must be queue-non-destructive", uuid)
	}
	if e.kind != kindUser || e.state != statePending {
		t.Errorf("queued entry kind/state = %v/%v after Interrupt, want kindUser/statePending (untouched)", e.kind, e.state)
	}
}

// TestUnifiedRuntime_InterruptFlagDoesNotLeakToNextTurn guards the stale-flag
// race: after an interrupt is consumed by one turn-end, a SUBSEQUENT normal
// turn completion must publish EventTurnCompleted, not EventInterrupted.
func TestUnifiedRuntime_InterruptFlagDoesNotLeakToNextTurn(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "agent-esc-noleak", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("esc-noleak-test", 32)
	defer unsub()

	// Turn 1: interrupted.
	openTurn(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	rt.routeFrame(resultFrame(t, true, 5), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	if interrupted, _, _ := tallyTerminalEvents(ch, 400*time.Millisecond); interrupted != 1 {
		t.Fatalf("turn 1: EventInterrupted count = %d, want 1", interrupted)
	}

	// Turn 2: clean completion, NO interrupt. Must publish EventTurnCompleted.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, false, 7), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("turn 2: EventInterrupted count = %d, want 0 (stale interrupt flag leaked)", interrupted)
	}
	if completed != 1 {
		t.Errorf("turn 2: EventTurnCompleted count = %d, want 1", completed)
	}
}

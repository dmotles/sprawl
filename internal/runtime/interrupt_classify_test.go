package runtime

import (
	"context"
	"encoding/json"
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
	rt.routeFrame(&protocol.Message{Type: "system", Subtype: "init"}, backend.TurnInfo{Autonomous: true})
	deadline := time.Now().Add(2 * time.Second)
	for !rt.State().InTurn {
		if time.Now().After(deadline) {
			t.Fatal("turn never entered InTurn")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// tallyTerminalEvents drains ch for `window` and counts the terminal turn
// events. Returns (interrupted, completed, failed).
func tallyTerminalEvents(ch <-chan RuntimeEvent, window time.Duration) (int, int, int) {
	var interrupted, completed, failed int
	deadline := time.After(window)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return interrupted, completed, failed
			}
			switch ev.Type {
			case EventInterrupted:
				interrupted++
			case EventTurnCompleted:
				completed++
			case EventTurnFailed:
				failed++
			}
		case <-deadline:
			return interrupted, completed, failed
		}
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

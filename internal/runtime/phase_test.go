package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-903: in_turn is an explicit 3-state machine — idle / submitted (synthetic,
// speculative) / running (wire-confirmed) — driven by the CLI's authoritative
// session_state_changed wire signal plus an optimistic submit-from-idle set.
// State().InTurn is true for submitted OR running, false for idle.
//
// The synthetic submitted state is entered ONLY for a human-typed prompt
// (kind:user, the weave-window watched input path) submitted from idle;
// sprawl-originated (kind:system) deliveries — spawn prompt, inbox, task, the
// QUM-640 continuation — never synthesize it, so passively-observed children
// expose only idle/running off their wire.
//
// These tests direct-drive rt.routeFrame (the reader-goroutine frame router) and
// the WriteUser*/WriteSystemMessage stdin-write paths, mirroring the existing
// interrupt_classify / backend_fault test style.
//
// NOTE: these tests mutate the package global submittedPhaseTimeout — they must
// NOT be marked t.Parallel().

// stateFrame builds a session_state_changed wire frame + its TurnInfo tag for
// direct routeFrame drive.
func stateFrame(state string) (*protocol.Message, backend.TurnInfo) {
	return &protocol.Message{Type: "system", Subtype: "session_state_changed"},
		backend.TurnInfo{Autonomous: true, StateChange: state}
}

// feedInit drives a bare autonomous system/init frame.
func feedInit(rt *UnifiedRuntime) {
	rt.routeFrame(&protocol.Message{Type: "system", Subtype: "init"}, backend.TurnInfo{Autonomous: true})
}

func newPhaseRuntime() *UnifiedRuntime {
	return New(RuntimeConfig{Name: "phase-agent", Session: &mockUnifiedSession{}})
}

// withShortSubmittedTimeout overrides the submitted-side guard timeout for the
// duration of a test (restored via t.Cleanup) so submitting tests neither leak a
// 2s timer goroutine nor rely on the production duration.
func withShortSubmittedTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	restore := submittedPhaseTimeout
	submittedPhaseTimeout = d
	t.Cleanup(func() { submittedPhaseTimeout = restore })
}

// (a) idle -> submit -> submitted -> wire:running -> running -> wire:idle -> idle.
func TestPhase_IdleSubmitRunningIdle(t *testing.T) {
	withShortSubmittedTimeout(t, 40*time.Millisecond)
	rt := newPhaseRuntime()

	if rt.State().InTurn {
		t.Fatal("fresh runtime should be idle (InTurn=false)")
	}

	// Optimistic submit-from-idle enters the synthetic submitted state.
	if _, err := rt.WriteUserPrompt(context.Background(), "hello", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	if !rt.State().InTurn {
		t.Fatal("submit-from-idle must optimistically set InTurn=true (submitted)")
	}

	// Wire running confirms (and supersedes the synthetic before its timeout).
	rt.routeFrame(stateFrame("running"))
	if !rt.State().InTurn {
		t.Fatal("wire:running must keep InTurn=true (running)")
	}

	// Wire idle clears (authoritative turn-end).
	rt.routeFrame(stateFrame("idle"))
	if rt.State().InTurn {
		t.Fatal("wire:idle must clear InTurn=false")
	}
}

// (AC#4 / leak fix) a bare autonomous system/init must NOT set InTurn — only a
// wire:running or a human submit does. EventTurnStarted still fires (lifecycle
// event authority is unchanged). This is the false-"thinking" repro.
func TestPhase_InitAloneDoesNotSetInTurn(t *testing.T) {
	rt := newPhaseRuntime()
	ch, unsub := rt.EventBus().SubscribeNamed("phase-init", 8)
	defer unsub()

	feedInit(rt)

	// EventTurnStarted must still fire (frame-driven lifecycle unchanged).
	sawStarted := false
	deadline := time.After(time.Second)
	for !sawStarted {
		select {
		case ev := <-ch:
			if ev.Type == EventTurnStarted {
				sawStarted = true
			}
		case <-deadline:
			t.Fatal("EventTurnStarted did not fire on autonomous init")
		}
	}

	// But InTurn must be false — no running wire, no submit. routeFrame is
	// synchronous so State() reflects the final phase immediately.
	if rt.State().InTurn {
		t.Fatal("autonomous init alone leaked InTurn=true (false-\"thinking\" bug)")
	}
}

// a sprawl-originated (kind:system) delivery from idle must NOT synthesize the
// submitted state — only the wire drives a passively-delivered turn. This is the
// scope boundary that keeps children off the synthetic path and prevents the
// QUM-640 continuation / inbox writes from re-leaking false "thinking".
func TestPhase_SystemMessageFromIdleDoesNotSetInTurn(t *testing.T) {
	withShortSubmittedTimeout(t, 40*time.Millisecond)
	rt := newPhaseRuntime()

	if _, err := rt.WriteSystemMessage(context.Background(), "inbox item", "next", nil); err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}
	if rt.State().InTurn {
		t.Fatal("kind:system delivery from idle must NOT set InTurn (no synthetic submitted)")
	}

	// The wire still drives it: a running event flips InTurn true.
	rt.routeFrame(stateFrame("running"))
	if !rt.State().InTurn {
		t.Fatal("wire:running after a system delivery must set InTurn=true")
	}
}

// (b) a terminal result with NO following idle wire must still clear InTurn
// (running-side teardown guard: idle is not guaranteed — the 36 no-idle cases).
func TestPhase_ResultNoIdleClearsInTurn(t *testing.T) {
	rt := newPhaseRuntime()

	rt.routeFrame(stateFrame("running"))
	if !rt.State().InTurn {
		t.Fatal("wire:running should set InTurn=true")
	}

	// Terminal result, NO idle wire follows.
	rt.routeFrame(resultFrame(t, false, 10), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	if rt.State().InTurn {
		t.Fatal("terminal result with no idle wire must clear InTurn=false")
	}

	// A following init must NOT resurrect InTurn until a new running/submit.
	feedInit(rt)
	if rt.State().InTurn {
		t.Fatal("post-teardown init resurrected InTurn without a running/submit")
	}
}

// (c) a resume-boundary running -> init -> running doublet must not strand: the
// intervening init must not clear InTurn, and a final idle returns to idle.
func TestPhase_RunningInitDoubletStaysRunning(t *testing.T) {
	rt := newPhaseRuntime()

	rt.routeFrame(stateFrame("running")) // turn 1 running
	if !rt.State().InTurn {
		t.Fatal("wire:running (turn1) should set InTurn=true")
	}

	feedInit(rt) // resume boundary
	if !rt.State().InTurn {
		t.Fatal("intervening init stranded/cleared InTurn across the doublet")
	}

	rt.routeFrame(stateFrame("running")) // turn 2 running (idempotent)
	if !rt.State().InTurn {
		t.Fatal("second wire:running should keep InTurn=true")
	}

	rt.routeFrame(stateFrame("idle"))
	if rt.State().InTurn {
		t.Fatal("final wire:idle must clear InTurn=false")
	}
}

// (d) a submit while running is idempotent: it does NOT enter the synthetic
// submitted state (no new speculative timer), so InTurn stays true even past a
// short submitted-timeout — AND the write is still delivered.
func TestPhase_SubmitWhileRunningIsIdempotent(t *testing.T) {
	withShortSubmittedTimeout(t, 40*time.Millisecond)

	rt := newPhaseRuntime()
	mock := rt.cfg.Session.(*mockUnifiedSession)

	rt.routeFrame(stateFrame("running"))
	if !rt.State().InTurn {
		t.Fatal("wire:running should set InTurn=true")
	}

	if _, err := rt.WriteUserPrompt(context.Background(), "more", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	if mock.writeCount() != 1 {
		t.Fatalf("submit-while-running writeCount = %d, want 1 (write must still be delivered)", mock.writeCount())
	}

	// If the submit wrongly entered submitted, the short timeout would clear it.
	time.Sleep(200 * time.Millisecond)
	if !rt.State().InTurn {
		t.Fatal("submit-while-running entered synthetic submitted and got cleared (should be idempotent)")
	}
}

// (e) a requires_action wire event must not clear InTurn (best-effort tolerate;
// keep "thinking"); a subsequent idle still clears — the strong-red half.
func TestPhase_RequiresActionKeepsInTurnUntilIdle(t *testing.T) {
	rt := newPhaseRuntime()

	rt.routeFrame(stateFrame("running"))
	rt.routeFrame(stateFrame("requires_action"))
	if !rt.State().InTurn {
		t.Fatal("requires_action must keep InTurn=true")
	}

	rt.routeFrame(stateFrame("idle"))
	if rt.State().InTurn {
		t.Fatal("wire:idle after requires_action must clear InTurn=false")
	}
}

// the submitted-side defensive guard: a synthetic submitted with no running ack
// (backend died / hung after a successful write) clears to idle on a short
// timeout, so it never leaks false "thinking".
func TestPhase_SubmittedTimeoutClearsInTurn(t *testing.T) {
	withShortSubmittedTimeout(t, 30*time.Millisecond)

	rt := newPhaseRuntime()
	if _, err := rt.WriteUserPrompt(context.Background(), "hello", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	if !rt.State().InTurn {
		t.Fatal("submit-from-idle must set InTurn=true (submitted)")
	}

	deadline := time.Now().Add(time.Second)
	for rt.State().InTurn {
		if time.Now().After(deadline) {
			t.Fatal("submitted-side timeout did not clear InTurn after running never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// a stale submitted-timer must be a no-op once a wire:running has superseded the
// synthetic state (generation + phase double-check).
func TestPhase_SubmittedThenRunningNotClearedByStaleTimer(t *testing.T) {
	withShortSubmittedTimeout(t, 40*time.Millisecond)

	rt := newPhaseRuntime()
	if _, err := rt.WriteUserPrompt(context.Background(), "hello", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	// Confirm running BEFORE the timeout fires.
	rt.routeFrame(stateFrame("running"))

	// Let the stale submitted timer fire well past its deadline.
	time.Sleep(200 * time.Millisecond)
	if !rt.State().InTurn {
		t.Fatal("stale submitted timer cleared a wire-confirmed running phase")
	}
}

// pure-wire path: running -> idle clears InTurn (no result frame involved).
func TestPhase_WireIdleClearsInTurn(t *testing.T) {
	rt := newPhaseRuntime()
	rt.routeFrame(stateFrame("running"))
	if !rt.State().InTurn {
		t.Fatal("wire:running should set InTurn=true")
	}
	rt.routeFrame(stateFrame("idle"))
	if rt.State().InTurn {
		t.Fatal("wire:idle should clear InTurn=false")
	}
}

// scope guard: session_state_changed frames drive ONLY the in_turn phase — they
// must NOT publish frame-lifecycle or protocol events (the issue keeps the
// lifecycle event authority frame-driven; only the in_turn boolean moves).
func TestPhase_StateChangeFramesEmitNoLifecycleEvents(t *testing.T) {
	rt := newPhaseRuntime()
	ch, unsub := rt.EventBus().SubscribeNamed("phase-lifecycle", 16)
	defer unsub()

	rt.routeFrame(stateFrame("running"))
	rt.routeFrame(stateFrame("requires_action"))
	rt.routeFrame(stateFrame("idle"))

	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case ev := <-ch:
			switch ev.Type {
			case EventTurnStarted, EventTurnCompleted, EventInterrupted, EventTurnFailed, EventProtocolMessage:
				t.Fatalf("session_state_changed frame published %v; state-change frames must be phase-only", ev.Type)
			}
		case <-deadline:
			return
		}
	}
}

// QUM-925 Slice A: kind:system stdin writes must be observable by the TUI pending
// zone — they publish EventUserMessageSent so the uuid-keyed zone can track and
// settle them.
//
// They must NOT synthesize the optimistic submitted phase, for the ROOT any more
// than for a child. QUM-925 considered widening the writeMessage fast path to
// `kindUser || (kindSystem && cfg.IsRoot)` and rejected it; see the comment at that
// branch, and TestPhase_RootSystemMessageFromIdle_DoesNotSetInTurn below, which
// pins the rejection as a deliberate non-change.

package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
)

const testNotifFrame = `<system-notification type="status_change">child reported complete</system-notification>`

// TestWriteSystemMessage_PublishesUserMessageSent is the QUM-925 core: the
// supervisor-originated system frame is written to stdin with a fresh uuid, and
// the TUI pending zone can only track→settle that frame if the uuid reaches it.
// The only channel is EventUserMessageSent. Without this publish the frame is
// invisible until the CLI's isReplay echo, at which point ZoneSettle is a no-op
// against an untracked uuid and the notification never renders.
func TestWriteSystemMessage_PublishesUserMessageSent(t *testing.T) {
	// Once the idle->submitted fast path widens for root system writes, this test
	// arms guardSubmitted; the production 2s value would leak a timer goroutine.
	withShortSubmittedTimeout(t, 40*time.Millisecond)
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", IsRoot: true, Session: mock})

	ch, unsub := rt.EventBus().SubscribeNamed("syspublish-test", 32)
	defer unsub()

	uuid, err := rt.WriteSystemMessage(context.Background(), testNotifFrame, "next", []string{"e1"})
	if err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}

	// The CLI later echoes the frame back (isReplay) — the consumption ack.
	rt.markConsumed(uuid)

	events := drainEvents(ch)

	sentIdx, consumedIdx, sentCount := -1, -1, 0
	var sentUUID, sentText string
	for i, ev := range events {
		switch ev.Type {
		case EventUserMessageSent:
			sentCount++
			if sentIdx == -1 {
				sentIdx = i
			}
			sentUUID = ev.UUID
			sentText = ev.Prompt
		case EventUserMessageConsumed:
			if consumedIdx == -1 {
				consumedIdx = i
			}
		}
	}

	if sentCount > 1 {
		t.Errorf("EventUserMessageSent published %d times, want exactly 1", sentCount)
	}
	if sentIdx == -1 {
		t.Fatalf("WriteSystemMessage published no EventUserMessageSent (QUM-925: the pending zone never learns the uuid, so the notification never renders)")
	}
	if sentUUID != uuid {
		t.Errorf("EventUserMessageSent.UUID = %q, want the write's uuid %q", sentUUID, uuid)
	}
	if sentText != testNotifFrame {
		t.Errorf("EventUserMessageSent.Prompt = %q, want the frame text %q", sentText, testNotifFrame)
	}
	if consumedIdx == -1 {
		t.Fatalf("no EventUserMessageConsumed published for the system frame")
	}
	if sentIdx > consumedIdx {
		t.Errorf("EventUserMessageSent (idx %d) must precede EventUserMessageConsumed (idx %d) — otherwise ZoneSettle runs against an untracked uuid", sentIdx, consumedIdx)
	}
}

// TestWriteSystemMessage_FailedWrite_PublishesNoSent is the absence half of the
// pair above: a write that errors must publish nothing, or the zone grows a
// phantom pending entry that can never settle (ZoneDrop refuses system entries,
// so it would be permanent). TestWriteSystemMessage_PublishesUserMessageSent is
// this test's positive control — it proves the event CAN appear on this path.
func TestWriteSystemMessage_FailedWrite_PublishesNoSent(t *testing.T) {
	withShortSubmittedTimeout(t, 40*time.Millisecond)
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}, writeErr: errors.New("stdin closed")}
	rt := New(RuntimeConfig{Name: "weave", IsRoot: true, Session: mock})

	ch, unsub := rt.EventBus().SubscribeNamed("syspublish-fail-test", 32)
	defer unsub()

	if _, err := rt.WriteSystemMessage(context.Background(), testNotifFrame, "next", []string{"e1"}); err == nil {
		t.Fatal("WriteSystemMessage returned nil error, want the injected write failure")
	}

	for _, ev := range drainEvents(ch) {
		if ev.Type == EventUserMessageSent {
			t.Fatalf("failed write published EventUserMessageSent (uuid %q) — phantom pending-zone entry", ev.UUID)
		}
	}
	if n := len(rt.Outstanding()); n != 0 {
		t.Errorf("Outstanding() = %d entries, want 0 after a failed write", n)
	}
}

// TestPhase_RootSystemMessageFromIdle_DoesNotSetInTurn pins a deliberate
// NON-CHANGE. QUM-925's brief asked for the writeMessage idle->phaseSubmitted fast
// path to widen so an idle weave "still triggers a turn" on a system frame. Code
// review established that widening is both unnecessary and unsafe, so it was NOT
// made, and this test exists so nobody re-adds it without reading why:
//
//   - Unnecessary: the CLI takes a turn because a user message is queued on its
//     stdin, not because sprawl flipped a phase. A child's spawn prompt is a
//     kindSystem `next` write and opens its turn today with this branch NOT taken.
//     And no production code reads the result for weave — the only reader of
//     UnifiedRuntime.State() is runtime_launcher.go's Liveness check, and the
//     in_turn the TUI renders comes from WeaveRuntimeHandle.InTurn() =>
//     session.InTurn().
//   - Unsafe: the branch calls retireUnclaimedNextArmLocked, whose contract is a
//     SUPERSEDING USER SUBMIT. A background child ping is not one, and retiring a
//     live next-turn arm on one is the QUM-927 / QUM-935 empty-"Session Error"
//     class. It would also change armInterruptLocked's answer for an Esc pressed
//     while idle-but-just-poked, and spawn a guardSubmitted goroutine per
//     notification.
//
// This is the ROOT-side twin of TestPhase_SystemMessageFromIdleDoesNotSetInTurn
// (phase_test.go), which covers children. Both must be red for the widened form.
func TestPhase_RootSystemMessageFromIdle_DoesNotSetInTurn(t *testing.T) {
	withShortSubmittedTimeout(t, 40*time.Millisecond)
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "weave", IsRoot: true, Session: mock})

	if rt.State().InTurn {
		t.Fatal("setup: fresh runtime should be idle")
	}
	if _, err := rt.WriteSystemMessage(context.Background(), testNotifFrame, "next", nil); err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}
	if rt.State().InTurn {
		t.Error("a root kind:system delivery from idle synthesized the submitted phase — see this test's doc for why that is unsafe")
	}
	// The frame is still queued: suppressing the synthetic phase does not suppress
	// delivery. Without this the test would also pass against a runtime that simply
	// dropped the write.
	if got := mock.writeCount(); got != 1 {
		t.Errorf("stdin writes = %d, want 1 (the notification must still be written)", got)
	}
	// And the wire still drives it, exactly as it does for a child.
	rt.routeFrame(stateFrame("running"))
	if !rt.State().InTurn {
		t.Error("wire:running after a root system delivery must set InTurn")
	}
}

// TestWriteSystemMessage_RootIdle_DoesNotArmInterrupt: a root system frame is
// written at priority `next`, so it must arm NOTHING — a genuine error in the
// following turn stays EventTurnCompleted rather than being laundered into a
// clean EventInterrupted (the empty "Session Error" overlay bug in reverse).
// This is the pin that breaks if anyone changes weave's system frames to `now`.
//
// SCOPE, recorded honestly: this test does NOT pin the writeMessage ORDERING
// constraint (the `priority == "now"` arm staying above
// setPhaseLocked(phaseSubmitted)). Moving the arm below setPhaseLocked was
// mutation-tested and left THIS test green — a `next` write never reaches the arm
// at any position. TestSendAllNow_NowWriteWhileIdle_DoesNotArm went red and
// remains the sole pin on that ordering.
func TestWriteSystemMessage_RootIdle_DoesNotArmInterrupt(t *testing.T) {
	withShortSubmittedTimeout(t, 40*time.Millisecond)
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave-sysidle-arm", IsRoot: true, Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("sysidle-arm-test", 32)
	defer unsub()

	if _, err := rt.WriteSystemMessage(context.Background(), testNotifFrame, "next", nil); err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}

	// A turn then runs and genuinely errors. Nothing preempted it.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, true, 13), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("EventInterrupted count = %d, want 0 (an idle next-priority system write preempts nothing, so it must not arm)", interrupted)
	}
	if completed != 1 {
		t.Errorf("EventTurnCompleted count = %d, want 1", completed)
	}
}

// TestPhase_RootSystemMessageWhileInTurn_DoesNotReenterSubmitted pins the OTHER
// half of the widened fast-path gate: `rt.phase == phaseIdle`. A root system
// frame arriving while a turn is already in flight must NOT re-enter
// phaseSubmitted, because that path also calls retireUnclaimedNextArmLocked —
// which would drop a live Esc/preempt arm (the QUM-827/927 flag) and let that
// turn's abort terminal surface as an empty "Session Error".
//
// This is the busy-weave companion to TestPhase_RootSystemMessageFromIdle_SetsInTurn:
// the frame is still written and still queued at `next`; only the SYNTHETIC phase
// transition is suppressed.
func TestPhase_RootSystemMessageWhileInTurn_DoesNotReenterSubmitted(t *testing.T) {
	withShortSubmittedTimeout(t, 40*time.Millisecond)
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", IsRoot: true, Session: mock})

	// Wire-confirmed running: the authoritative in-turn state.
	rt.routeFrame(stateFrame("running"))
	if !rt.State().InTurn {
		t.Fatal("setup: wire:running must set InTurn")
	}

	if _, err := rt.WriteSystemMessage(context.Background(), testNotifFrame, "next", nil); err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}

	// Still running (not downgraded to the synthetic submitted phase). Observable
	// via the guard: phaseSubmitted expires after submittedPhaseTimeout and would
	// clear InTurn, whereas phaseRunning persists until the wire says otherwise.
	time.Sleep(120 * time.Millisecond)
	if !rt.State().InTurn {
		t.Error("a root system write during an in-flight turn re-entered phaseSubmitted — its guard then expired and cleared InTurn mid-turn")
	}
	// And the frame was still written.
	if got := mock.writeCount(); got != 1 {
		t.Errorf("stdin writes = %d, want 1 (the frame must still be queued)", got)
	}
}

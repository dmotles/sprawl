package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-815/QUM-817: every turn is router-driven. The frame router installed by
// New() derives a balanced EventTurnStarted + EventTurnCompleted from the frame
// stream. QUM-929: it writes NOTHING to stdin in response to a background task's
// task_notification — the CLI self-resumes on its own, so the QUM-640
// [auto-continue] injection is deleted; the router only OBSERVES the
// notification (publishing it as EventProtocolMessage for TUI/telemetry).
// QUM-903: in_turn is no longer flipped by the opening init frame — it is driven
// by the session_state_changed wire signal (+ submit-from-idle), with
// terminal/teardown guards.

const (
	autoInitFrame     = `{"type":"system","subtype":"init","session_id":"sess-auto"}`
	autoRunningFrame  = `{"type":"system","subtype":"session_state_changed","state":"running","session_id":"sess-auto"}`
	autoAssistFrame   = `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"auto-reply"}]}}`
	autoResultFrame   = `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`
	autoTaskNotif     = `{"type":"system","subtype":"task_notification","task_id":"task-X","status":"completed","summary":"bg done"}`
	autoTaskNotifNoID = `{"type":"system","subtype":"task_notification","status":"completed","summary":"bg done"}`
	autoReplayUser    = `{"type":"user","uuid":"u-replay-1","session_id":"sess-auto","isReplay":true,"message":{"role":"user","content":"queued prompt"}}`
)

// newAutonomousRuntime wires a UnifiedRuntime over a scripted transport-backed
// real session and starts the session reader. rt.Start is NOT called — the
// turn flows through the reader/router, and any stdin write would land on
// transport.sendCh.
func newAutonomousRuntime(t *testing.T) (*UnifiedRuntime, *scriptedTransport) {
	t.Helper()
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-auto"})
	t.Cleanup(func() { _ = session.Close() })
	rt := New(RuntimeConfig{Name: "agent-auto", Session: session})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := session.Start(ctx); err != nil {
		t.Fatalf("session.Start: %v", err)
	}
	return rt, transport
}

// assertNoUserWrite fails if ANY user message is written to stdin within d (a
// deliberate negative-assertion wait). QUM-929: matching on "any user message"
// rather than on the deleted continuation's exact wording is what stops a
// re-added, re-worded nudge from sneaking past this gate. Safe because
// newAutonomousRuntime does not call rt.Start and session.Start's initialize is
// a control_request, not a protocol.UserMessage.
func assertNoUserWrite(t *testing.T, transport *scriptedTransport, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case sent := <-transport.sendCh:
			// Match both value and pointer forms so the gate cannot go dead if a
			// future write path sends *protocol.UserMessage.
			switch um := sent.(type) {
			case protocol.UserMessage:
				t.Fatalf("unexpected stdin user-message write: content=%q priority=%q", um.Message.Content, um.Priority)
			case *protocol.UserMessage:
				t.Fatalf("unexpected stdin user-message write: content=%q priority=%q", um.Message.Content, um.Priority)
			}
		case <-deadline:
			return
		}
	}
}

// TestScriptedTransport_CapturesUserWrites is the POSITIVE CONTROL for every
// assertNoUserWrite negative in this file. Those negatives are now the whole
// unit-level defense against the [auto-continue] injection coming back (QUM-929),
// and they would go permanently and silently vacuous if a refactor moved
// writeMessage off the session-transport path. This test fails first in that case.
func TestScriptedTransport_CapturesUserWrites(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)

	if _, err := rt.WriteUserPrompt(context.Background(), "probe", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case sent := <-transport.sendCh:
			if um, ok := sent.(protocol.UserMessage); ok && um.Message.Content == "probe" {
				return
			}
		case <-deadline:
			t.Fatal("a stdin user write did NOT reach transport.sendCh — assertNoUserWrite is watching the wrong channel, so every zero-write assertion in this file is vacuous")
		}
	}
}

// waitTurnCompleted blocks until a terminal turn event arrives, so a following
// assertNoUserWrite is a real negative and not a race against a router that never
// consumed the terminal frame (the deleted write lived in routeFrame's EndOfTurn
// arm — without this sync the zero-write assertions would pass vacuously).
func waitTurnCompleted(t *testing.T, ch <-chan RuntimeEvent, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev := <-ch:
			switch ev.Type {
			case EventTurnCompleted, EventInterrupted, EventTurnFailed:
				return
			}
		case <-deadline:
			t.Fatalf("no terminal turn event within %v — the router never consumed the turn", d)
		}
	}
}

func TestUnifiedRuntime_AutonomousTurn_EmitsBalancedStartAndComplete(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)
	ch, unsub := rt.EventBus().SubscribeNamed("auto", 32)
	defer unsub()

	transport.feed(t, autoInitFrame)
	transport.feed(t, autoAssistFrame)
	transport.feed(t, autoResultFrame)

	var started, completed int
	deadline := time.After(3 * time.Second)
	for completed == 0 {
		select {
		case ev := <-ch:
			switch ev.Type {
			case EventTurnStarted:
				started++
			case EventTurnCompleted:
				completed++
			}
		case <-deadline:
			t.Fatalf("timeout: started=%d completed=%d (want 1 each)", started, completed)
		}
	}
	if started != 1 {
		t.Errorf("EventTurnStarted count = %d, want 1", started)
	}
	if completed != 1 {
		t.Errorf("EventTurnCompleted count = %d, want 1", completed)
	}
}

func TestUnifiedRuntime_AutonomousTurn_FlipsInTurn(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)

	// QUM-903: init opens the frame lifecycle but no longer sets in_turn; the
	// wire `running` signal is the authority. The terminal result clears it
	// (running-side teardown guard, no idle wire required).
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoRunningFrame)
	waitInTurn(t, rt, true, 2*time.Second)

	transport.feed(t, autoResultFrame)
	waitInTurn(t, rt, false, 2*time.Second)
}

// waitInTurn polls rt.State().InTurn until it equals want or the deadline.
func waitInTurn(t *testing.T, rt *UnifiedRuntime, want bool, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		if rt.State().InTurn == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("InTurn never reached %v within %v (current=%v)", want, d, rt.State().InTurn)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestUnifiedRuntime_IdleTaskNotification_WritesNothing is the QUM-929 headline:
// an idle bg-task completion (autonomous turn carrying a task_notification) must
// produce NO stdin write. The CLI self-resumes on background-task completion in
// every timing case, so the QUM-640 [auto-continue] nudge was pure redundancy
// that structurally landed one turn late (the spurious-continuation class).
func TestUnifiedRuntime_IdleTaskNotification_WritesNothing(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)
	ch, unsub := rt.EventBus().SubscribeNamed("auto", 32)
	defer unsub()

	transport.feed(t, autoTaskNotif)
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoAssistFrame)
	transport.feed(t, autoResultFrame)

	waitTurnCompleted(t, ch, 3*time.Second)
	assertNoUserWrite(t, transport, 200*time.Millisecond)
}

// TestUnifiedRuntime_MidTurnTaskNotification_WritesNothing covers the OTHER
// arrival shape (QUM-929): a task_notification observed AFTER init, i.e. during a
// live turn. The deleted gate folded any observed task_id — pre-init or mid-turn —
// into a turn-end write, so this path needs its own negative.
func TestUnifiedRuntime_MidTurnTaskNotification_WritesNothing(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)
	ch, unsub := rt.EventBus().SubscribeNamed("auto", 32)
	defer unsub()

	transport.feed(t, autoInitFrame)
	transport.feed(t, autoRunningFrame)
	transport.feed(t, autoTaskNotif)
	transport.feed(t, autoAssistFrame)
	transport.feed(t, autoResultFrame)

	waitTurnCompleted(t, ch, 3*time.Second)
	assertNoUserWrite(t, transport, 200*time.Millisecond)
}

// TestUnifiedRuntime_EmptyTaskIDNotification_WritesNothing covers the deleted
// `sawEmptyTaskID` arm: a task_notification whose task_id is absent/empty used to
// force-fire a continuation independently of the dedup set. It must now write
// nothing either.
func TestUnifiedRuntime_EmptyTaskIDNotification_WritesNothing(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)
	ch, unsub := rt.EventBus().SubscribeNamed("auto", 32)
	defer unsub()

	transport.feed(t, autoTaskNotifNoID)
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoResultFrame)

	waitTurnCompleted(t, ch, 3*time.Second)
	assertNoUserWrite(t, transport, 200*time.Millisecond)
}

// TestUnifiedRuntime_IdleTaskNotification_StillPublishesProtocolMessage locks the
// QUM-929 KEEP side: deleting the injection must NOT delete the OBSERVATION. The
// task_notification's EventProtocolMessage publish is the sole source of the live
// "↻ auto-continued" marker (internal/tui/protocol_mapping.go maps
// system/task_notification → AutoContinueMsg) and of task telemetry.
func TestUnifiedRuntime_IdleTaskNotification_StillPublishesProtocolMessage(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)
	ch, unsub := rt.EventBus().SubscribeNamed("auto", 32)
	defer unsub()

	transport.feed(t, autoTaskNotif)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == EventProtocolMessage && ev.Message != nil &&
				ev.Message.Type == "system" && ev.Message.Subtype == "task_notification" {
				return
			}
			if ev.Type == EventTurnStarted {
				t.Fatal("lone task_notification opened a turn")
			}
		case <-deadline:
			t.Fatal("task_notification was not published as EventProtocolMessage (↻ marker + telemetry source deleted)")
		}
	}
}

// TestAutoContinuePrefix_StableLiteral pins the exact sentinel. QUM-929 stopped
// PRODUCING [auto-continue] frames, but six weeks of historical wire logs still
// contain them and internal/tui/replay.go classifies on this literal to rehydrate
// them as the "↻ auto-continued" marker (QUM-924). Changing the string silently
// breaks replay of every existing session log.
func TestAutoContinuePrefix_StableLiteral(t *testing.T) {
	if AutoContinuePrefix != "[auto-continue]" {
		t.Errorf("AutoContinuePrefix = %q, want %q (historical wire-log replay classifies on this exact literal)", AutoContinuePrefix, "[auto-continue]")
	}
}

// TestUnifiedRuntime_LoneTrigger_DoesNotOpenTurn (QUM-815 review HIGH): a
// pre-init task_notification trigger not followed by an init must NOT flip
// InTurn or emit EventTurnStarted — it is publish-only.
func TestUnifiedRuntime_LoneTrigger_DoesNotOpenTurn(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)
	ch, unsub := rt.EventBus().SubscribeNamed("auto", 32)
	defer unsub()

	transport.feed(t, autoTaskNotif) // pre-init trigger, no init follows

	sawProto := false
	deadline := time.After(2 * time.Second)
	for !sawProto {
		select {
		case ev := <-ch:
			switch ev.Type {
			case EventProtocolMessage:
				sawProto = true
			case EventTurnStarted:
				t.Fatal("lone pre-init trigger emitted EventTurnStarted (turn opened spuriously)")
			}
		case <-deadline:
			t.Fatal("trigger was not rendered as EventProtocolMessage")
		}
	}
	time.Sleep(200 * time.Millisecond)
	if rt.State().InTurn {
		t.Fatal("InTurn leaked true after a lone pre-init trigger")
	}

	// A following real autonomous turn runs its lifecycle and reverts InTurn —
	// and (QUM-929) still writes nothing to stdin for the earlier trigger.
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoResultFrame)
	waitTurnCompleted(t, ch, 3*time.Second)
	assertNoUserWrite(t, transport, 200*time.Millisecond)
	if rt.State().InTurn {
		t.Error("InTurn still true after the real autonomous turn completed")
	}
}

// TestUnifiedRuntime_PreInitCompactStatus_PublishedNoTurn (QUM-867): the
// compaction status frames (status:"compacting" / compact_result:"failed")
// arrive BEFORE system/init on the manual /compact path. They must be published
// as EventProtocolMessage (so the TUI's MapProtocolMessage can surface the
// "compacting…" label / "compaction failed" toast) but must NOT open a turn —
// they route via the PreInit publish-only path, exactly like a lone
// task_notification trigger.
func TestUnifiedRuntime_PreInitCompactStatus_PublishedNoTurn(t *testing.T) {
	const (
		compacting    = `{"type":"system","subtype":"status","status":"compacting","session_id":"sess-auto"}`
		compactFailed = `{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"Not enough messages to compact.","session_id":"sess-auto"}`
	)
	rt, transport := newAutonomousRuntime(t)
	ch, unsub := rt.EventBus().SubscribeNamed("auto", 32)
	defer unsub()

	transport.feed(t, compacting)
	transport.feed(t, compactFailed)

	var statusFrames int
	deadline := time.After(2 * time.Second)
	for statusFrames < 2 {
		select {
		case ev := <-ch:
			switch ev.Type {
			case EventProtocolMessage:
				if ev.Message != nil && ev.Message.Type == "system" && ev.Message.Subtype == "status" {
					statusFrames++
				}
			case EventTurnStarted:
				t.Fatal("pre-init compaction status frame emitted EventTurnStarted (turn opened spuriously)")
			}
		case <-deadline:
			t.Fatalf("compaction status frames were not published as EventProtocolMessage (saw %d/2)", statusFrames)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if rt.State().InTurn {
		t.Fatal("InTurn leaked true after pre-init compaction status frames")
	}
}

// TestUnifiedRuntime_RepeatedTaskNotifications_NeverWrite replaces the QUM-807
// serviced-set dedup test: with the injection gone there is nothing to dedup, so
// the invariant it protected (a turn re-observing the same task_notification must
// not drive another turn — the old infinite-loop risk) now holds by construction.
// Pinned so a future re-introduction can't restore the loop either.
func TestUnifiedRuntime_RepeatedTaskNotifications_NeverWrite(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)
	ch, unsub := rt.EventBus().SubscribeNamed("auto", 32)
	defer unsub()

	for range 2 {
		transport.feed(t, autoTaskNotif)
		transport.feed(t, autoInitFrame)
		transport.feed(t, autoResultFrame)
		waitTurnCompleted(t, ch, 3*time.Second)
		assertNoUserWrite(t, transport, 200*time.Millisecond)
	}
}

// TestUnifiedRuntime_AutonomousTurn_WithReplayFrame_StillBalanced: a turn whose
// stream includes a user+isReplay frame (QUM-814) still emits a balanced
// start/complete pair, and the replay echo flips the outstanding entry.
func TestUnifiedRuntime_AutonomousTurn_WithReplayFrame_StillBalanced(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)
	ch, unsub := rt.EventBus().SubscribeNamed("auto", 32)
	defer unsub()

	transport.feed(t, autoInitFrame)
	transport.feed(t, autoReplayUser)
	transport.feed(t, autoAssistFrame)
	transport.feed(t, autoResultFrame)

	var started, completed int
	deadline := time.After(3 * time.Second)
	for completed == 0 {
		select {
		case ev := <-ch:
			switch ev.Type {
			case EventTurnStarted:
				started++
			case EventTurnCompleted:
				completed++
			}
		case <-deadline:
			t.Fatalf("timeout: started=%d completed=%d", started, completed)
		}
	}
	if started != 1 || completed != 1 {
		t.Errorf("started=%d completed=%d, want 1 each (isReplay frame broke lifecycle)", started, completed)
	}
}

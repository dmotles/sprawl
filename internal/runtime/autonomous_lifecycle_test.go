package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-815/QUM-817: every turn is router-driven. The frame router installed by
// New() derives a balanced EventTurnStarted + EventTurnCompleted, flips InTurn,
// and (the QUM-812 fix) WRITES the QUM-640 auto-continuation to stdin when a
// background task completed — there is no Go queue anymore.

const (
	autoInitFrame   = `{"type":"system","subtype":"init","session_id":"sess-auto"}`
	autoAssistFrame = `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"auto-reply"}]}}`
	autoResultFrame = `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`
	autoTaskNotif   = `{"type":"system","subtype":"task_notification","task_id":"task-X","status":"completed","summary":"bg done"}`
	autoReplayUser  = `{"type":"user","uuid":"u-replay-1","session_id":"sess-auto","isReplay":true,"message":{"role":"user","content":"queued prompt"}}`
)

// newAutonomousRuntime wires a UnifiedRuntime over a scripted transport-backed
// real session and starts the session reader. rt.Start is NOT called — the
// turn flows through the reader/router, and stdin writes (e.g. the
// continuation) land on transport.sendCh.
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

// waitUserWrite scans transport.sendCh for a user message whose content matches,
// returning it or failing after d.
func waitUserWrite(t *testing.T, transport *scriptedTransport, d time.Duration, content string) protocol.UserMessage {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case sent := <-transport.sendCh:
			if um, ok := sent.(protocol.UserMessage); ok && um.Message.Content == content {
				return um
			}
		case <-deadline:
			t.Fatalf("no user-message write with content %q within %v", content, d)
			return protocol.UserMessage{}
		}
	}
}

// assertNoUserWrite fails if any user message with the given content is written
// within d (a deliberate negative-assertion wait).
func assertNoUserWrite(t *testing.T, transport *scriptedTransport, d time.Duration, content string) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case sent := <-transport.sendCh:
			if um, ok := sent.(protocol.UserMessage); ok && um.Message.Content == content {
				t.Fatalf("unexpected user-message write with content %q", content)
			}
		case <-deadline:
			return
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

	transport.feed(t, autoInitFrame)
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

// TestUnifiedRuntime_IdleTaskNotification_WritesContinuation is the QUM-812 fix
// under QUM-817: an idle bg-task completion (autonomous turn carrying a
// task_notification) makes the router WRITE the continuation prompt to stdin
// (no Go queue), which the CLI then processes as the next turn.
func TestUnifiedRuntime_IdleTaskNotification_WritesContinuation(t *testing.T) {
	_, transport := newAutonomousRuntime(t)

	transport.feed(t, autoTaskNotif)
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoAssistFrame)
	transport.feed(t, autoResultFrame)

	um := waitUserWrite(t, transport, 3*time.Second, continuationPrompt)
	if um.Priority != "next" {
		t.Errorf("continuation priority = %q, want next", um.Priority)
	}
	if um.UUID == "" {
		t.Error("continuation write has no uuid")
	}
}

// TestUnifiedRuntime_LoneTrigger_DoesNotOpenTurn (QUM-815 review HIGH): a
// pre-init task_notification trigger not followed by an init must NOT flip
// InTurn or emit EventTurnStarted; its task_id is buffered for the next turn.
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

	// The buffered task_id folds into the NEXT real autonomous turn's
	// continuation (written to stdin); InTurn reverts after completion.
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoResultFrame)
	waitUserWrite(t, transport, 3*time.Second, continuationPrompt)
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

// TestUnifiedRuntime_TaskNotification_ServicedDedup: a second turn re-observing
// the SAME task_id must NOT write another continuation (QUM-807 dedup, now
// wholly within the router since the cross-boundary turnloop is gone).
func TestUnifiedRuntime_TaskNotification_ServicedDedup(t *testing.T) {
	_, transport := newAutonomousRuntime(t)

	transport.feed(t, autoTaskNotif)
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoResultFrame)
	waitUserWrite(t, transport, 3*time.Second, continuationPrompt) // first continuation

	// Second turn re-observing task-X (as the continuation turn would) — no
	// new continuation.
	transport.feed(t, autoTaskNotif)
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoResultFrame)
	assertNoUserWrite(t, transport, 300*time.Millisecond, continuationPrompt)
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

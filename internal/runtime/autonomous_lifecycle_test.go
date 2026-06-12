package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
)

// QUM-815 (Slice 1): autonomous (CLI self-reprompt) turns must drive the SAME
// lifecycle as sprawl-initiated turns — a balanced EventTurnStarted +
// EventTurnCompleted, a flipped InTurn, and (the QUM-812 fix) the QUM-640
// auto-continuation when a background task completed. These flow through the
// single runtime frame router installed by New(), NOT the deleted
// autonomousFrameHandler.

const (
	autoInitFrame   = `{"type":"system","subtype":"init","session_id":"sess-auto"}`
	autoAssistFrame = `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"auto-reply"}]}}`
	autoResultFrame = `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`
	autoTaskNotif   = `{"type":"system","subtype":"task_notification","task_id":"task-X","status":"completed","summary":"bg done"}`
	autoReplayUser  = `{"type":"user","uuid":"u-replay-1","session_id":"sess-auto","isReplay":true,"message":{"role":"user","content":"queued prompt"}}`
)

// newAutonomousRuntime wires a UnifiedRuntime over a scripted transport-backed
// real session and starts the session reader. The turn loop is NOT started —
// the autonomous path flows through the reader/router, not StartTurn.
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

	// Poll the SETTLED observable state rather than single-reading after an
	// event, so the assertion is robust to the router's write/publish ordering
	// (test-critic #1): InTurn must become true once the autonomous turn opens
	// and revert to false once it completes.
	transport.feed(t, autoInitFrame)
	waitInTurn(t, rt, true, 2*time.Second)

	transport.feed(t, autoResultFrame)
	waitInTurn(t, rt, false, 2*time.Second)
}

// waitInTurn polls rt.State().InTurn until it equals want or the deadline
// elapses.
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

func TestUnifiedRuntime_IdleTaskNotification_EnqueuesContinuation(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)

	// Autonomous turn carrying a completed background-task notification.
	transport.feed(t, autoTaskNotif)
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoAssistFrame)
	transport.feed(t, autoResultFrame)

	// The router must enqueue exactly one continuation item.
	deadline := time.After(3 * time.Second)
	for rt.Queue().Len() < 1 {
		select {
		case <-deadline:
			t.Fatal("no continuation enqueued after idle task_notification (QUM-812 not fixed)")
		case <-time.After(5 * time.Millisecond):
		}
	}
	items := rt.Queue().DrainAll()
	if len(items) != 1 {
		t.Fatalf("queue had %d items, want exactly 1 continuation", len(items))
	}
	if items[0].Class != ClassContinuation {
		t.Errorf("item class = %v, want ClassContinuation", items[0].Class)
	}
	if items[0].Prompt != continuationPrompt {
		t.Errorf("item prompt = %q, want continuationPrompt", items[0].Prompt)
	}
}

// TestUnifiedRuntime_LoneTrigger_DoesNotOpenTurn (review HIGH): a pre-init
// task_notification trigger that is NOT followed by a system:init (e.g. a
// racing StartTurn absorbed the init) must NOT flip InTurn or emit
// EventTurnStarted — otherwise InTurn leaks true and the lifecycle is
// unbalanced. The trigger is still rendered (EventProtocolMessage) and its
// task_id buffered for the next autonomous turn.
func TestUnifiedRuntime_LoneTrigger_DoesNotOpenTurn(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)
	ch, unsub := rt.EventBus().SubscribeNamed("auto", 32)
	defer unsub()

	transport.feed(t, autoTaskNotif) // pre-init trigger, no init follows

	// Render must happen, but no turn lifecycle.
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
	// InTurn must remain false (no leak) after the trigger settles.
	time.Sleep(200 * time.Millisecond)
	if rt.State().InTurn {
		t.Fatal("InTurn leaked true after a lone pre-init trigger")
	}

	// The buffered task_id must fold into the NEXT real autonomous turn's
	// continuation (no work lost, balanced lifecycle on the real turn).
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoResultFrame)
	waitQueueLen(t, rt, 1, 3*time.Second)
	if rt.State().InTurn {
		t.Error("InTurn still true after the real autonomous turn completed")
	}
}

// TestUnifiedRuntime_AutonomousTaskNotification_ServicedDedup relies on the
// servicedTaskIDs dedup set being owned by the UnifiedRuntime (created in New)
// and shared into the TurnLoop — NOT owned by the TurnLoop — so it is live even
// though this test never calls rt.Start() (test-critic #2).
func TestUnifiedRuntime_AutonomousTaskNotification_ServicedDedup(t *testing.T) {
	rt, transport := newAutonomousRuntime(t)

	// First autonomous turn services task-X → one continuation.
	transport.feed(t, autoTaskNotif)
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoResultFrame)

	waitQueueLen(t, rt, 1, 3*time.Second)
	_ = rt.Queue().DrainAll()

	// A second autonomous turn re-observing the SAME task_id must NOT enqueue
	// another continuation (QUM-807 dedup, shared with the turnloop).
	transport.feed(t, autoTaskNotif)
	transport.feed(t, autoInitFrame)
	transport.feed(t, autoResultFrame)

	// Give the reader time; assert no new item appears.
	time.Sleep(300 * time.Millisecond)
	if n := rt.Queue().Len(); n != 0 {
		t.Fatalf("re-observed serviced task_id enqueued %d items, want 0", n)
	}
}

func waitQueueLen(t *testing.T, rt *UnifiedRuntime, n int, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		if rt.Queue().Len() >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("queue len = %d, want >= %d", rt.Queue().Len(), n)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestUnifiedRuntime_AutonomousTurn_WithReplayFrame_StillBalanced: an
// autonomous turn whose stream includes a user+isReplay frame (QUM-814) must
// not choke the router — the turn still emits a balanced start/complete pair
// (Slice-1 minimal: no outstanding-map yet, just don't break routing).
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

// TestSharedServicedSet_AutonomousServiceThenSprawlReObserve_NoDoubleFire is
// tower guardrail #1: the QUM-807 idempotency must hold ACROSS the
// autonomous→sprawl boundary. When the frame router services task-X (autonomous
// turn) it adds it to the shared set; the continuation it spawns is a sprawl
// turn whose stream re-observes task-X's task_notification — the TurnLoop must
// see it as serviced and NOT enqueue another continuation (else infinite loop).
func TestSharedServicedSet_AutonomousServiceThenSprawlReObserve_NoDoubleFire(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-shared"})
	t.Cleanup(func() { _ = session.Close() })

	// Shared set, as the runtime wires it into both the router and the loop.
	shared := newServicedTaskSet()
	// Simulate the autonomous turn having already serviced task-X.
	shared.add("task-X")

	q := NewMessageQueue()
	bus := NewEventBus()
	loop := NewTurnLoop(TurnLoopConfig{Session: session, Queue: q, EventBus: bus, Serviced: shared})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = loop.Run(ctx) }()

	// Drive a sprawl continuation turn whose stream re-observes task-X.
	q.Enqueue(QueueItem{Class: ClassContinuation, Prompt: continuationPrompt})
	go func() {
		select {
		case <-transport.sendCh:
		case <-time.After(2 * time.Second):
			return
		}
		transport.feed(t, autoInitFrame)
		transport.feed(t, `{"type":"system","subtype":"task_notification","task_id":"task-X","status":"completed","summary":"bg done"}`)
		transport.feed(t, autoResultFrame)
	}()

	// Give the turn time to complete and (incorrectly) re-enqueue if buggy.
	time.Sleep(500 * time.Millisecond)
	if n := q.Len(); n != 0 {
		t.Fatalf("re-observed serviced task-X enqueued %d continuation(s) on the sprawl turn, want 0 (QUM-807 cross-boundary dedup)", n)
	}
}

// TestUnifiedRuntime_SprawlTurn_RouterDoesNotDoubleEmit drives a real
// sprawl-initiated turn through the turn loop and asserts the bus sees EXACTLY
// one EventTurnStarted and one EventTurnCompleted — i.e. the frame router did
// not also emit lifecycle events for the sprawl turn (no double-emit).
func TestUnifiedRuntime_SprawlTurn_RouterDoesNotDoubleEmit(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-auto"})
	t.Cleanup(func() { _ = session.Close() })
	rt := New(RuntimeConfig{Name: "agent-sprawl", Session: session})

	ch, unsub := rt.EventBus().SubscribeNamed("sprawl", 64)
	defer unsub()

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })

	// Feed the sprawl turn once the loop issues StartTurn (which Sends the
	// user prompt frame on the transport).
	go func() {
		select {
		case <-transport.sendCh: // user-prompt send
		case <-time.After(2 * time.Second):
			return
		}
		transport.feed(t, autoInitFrame)
		transport.feed(t, autoAssistFrame)
		transport.feed(t, autoResultFrame)
	}()

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "do work", EntryIDs: []string{"seq-1"}})

	var started, completed int
	deadline := time.After(4 * time.Second)
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
	// Drain a tail to catch a stray duplicate (deliberate negative-assertion
	// wait; 500ms one-shot to tolerate a loaded box — test-critic #5).
	drainTail := time.After(500 * time.Millisecond)
	for {
		select {
		case ev := <-ch:
			switch ev.Type {
			case EventTurnStarted:
				started++
			case EventTurnCompleted:
				completed++
			}
		case <-drainTail:
			if started != 1 {
				t.Errorf("EventTurnStarted count = %d, want exactly 1 (double-emit?)", started)
			}
			if completed != 1 {
				t.Errorf("EventTurnCompleted count = %d, want exactly 1 (double-emit?)", completed)
			}
			return
		}
	}
}

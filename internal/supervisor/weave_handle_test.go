// Tests for *WeaveRuntimeHandle (QUM-399 Phase 3). Mirrors the
// runtime_launcher_unified_test.go coverage for *unifiedHandle, but the
// handle is constructed externally via NewWeaveRuntimeHandle (no starter).

package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// resultEmittingSession wraps a fakeBackendSession so its StartTurn emits a
// minimal terminal "result" protocol message. The bare fakeBackendSession
// closes the events channel without a result, which means executeTurn never
// publishes EventTurnCompleted/EventInterrupted — defeating any test that
// asserts the terminal event class.
type resultEmittingSession struct {
	*fakeBackendSession
}

func (r *resultEmittingSession) StartTurn(ctx context.Context, prompt string, spec ...backendpkg.TurnSpec) (<-chan *protocol.Message, error) {
	// Bump the underlying counters via the wrapped session, but discard its
	// already-closed channel and substitute one that delivers a result.
	_, _ = r.fakeBackendSession.StartTurn(ctx, prompt, spec...)
	out := make(chan *protocol.Message, 1)
	out <- &protocol.Message{Type: "result", Subtype: "success"}
	close(out)
	return out, nil
}

// TestWeaveRuntimeHandle_WakeForDelivery_DoesNotEnqueue_LeavesPendingForPeekAndDrain
// pins QUM-471: under the unified runtime, weave's WeaveRuntimeHandle.InterruptDelivery
// must NOT enqueue ClassInbox/ClassInterrupt QueueItems against the runtime's queue.
// The TUI's peekAndDrainCmd (a 2s disk-poll backstop fired on AgentTreeMsg while
// idle) is the sole drain pipeline for weave's inbox: it reads pending entries
// from disk, calls AppendSystemMessage(prompt), and routes through bridge.SendMessage
// so the prompt body is rendered in the viewport.
//
// If InterruptDelivery enqueues directly, the runtime's TurnLoop pulls the
// QueueItem and emits EventTurnStarted{Prompt} — but TUIAdapter.WaitForEvent
// skips EventTurnStarted, so the prompt body never reaches the viewport. The
// 2s peek-and-drain backstop loses the race, leaving the inbox prompt body
// invisible (only the "[inbox]" banner shows up — see audit doc).
//
// Contract: pending entries on disk MUST remain in pending/ after
// InterruptDelivery returns; the runtime queue MUST be empty.
func TestWeaveRuntimeHandle_WakeForDelivery_DoesNotEnqueue_LeavesPendingForPeekAndDrain(t *testing.T) {
	sprawlRoot := t.TempDir()
	const name = "weave"

	// Seed an async pending entry. Under the old (buggy) behavior, this would
	// be drained into a ClassInbox QueueItem by InterruptDelivery.
	if _, err := agentloop.Enqueue(sprawlRoot, name, agentloop.Entry{
		ID:      "id-async-1",
		ShortID: "sa1",
		Class:   agentloop.ClassAsync,
		From:    "child",
		Subject: "status",
		Body:    "all green",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	mock := newFakeBackendSession("sess-weave", backendpkg.Capabilities{})
	rt := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name:       name,
		SprawlRoot: sprawlRoot,
		Session:    mock,
		IsRoot:     true,
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	h, err := NewWeaveRuntimeHandle(rt, mock, sprawlRoot, name)
	if err != nil {
		t.Fatalf("NewWeaveRuntimeHandle: %v", err)
	}

	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	// Note on wake-signal preservation (test-critic feedback on commit 8d1c496):
	// This test only asserts the negative contract (no enqueue, pending stays
	// on disk). The post-fix handle still needs to call rt.InterruptDelivery(ctx)
	// — the runtime's contractual wake entry-point — so an idle TurnLoop wakes
	// when peekAndDrainCmd later enqueues a ClassUser item via bridge.SendMessage.
	//
	// The wake call is exercised indirectly by the QUM-462 sibling test
	// _TerminalEventIsCompleted_NotInterrupted below: it manually enqueues a
	// ClassUser item and then calls h.InterruptDelivery, observing
	// EventTurnCompleted within 2s. That observation depends on the wake firing
	// (or, more precisely, on rt.InterruptDelivery's idempotent path against an
	// idle runtime, which still nudges the TurnLoop). If a future refactor
	// dropped the rt.InterruptDelivery(ctx) call entirely from the handle, the
	// sibling test would still pass because runtime.MessageQueue.Enqueue itself
	// calls Wake() (see queue.go) — so neither test pins the wake call as a
	// load-bearing line in isolation. Pinning it would require either a spy on
	// rt.InterruptDelivery or a runtime fixture with Enqueue's internal Wake
	// disabled, both higher-cost than the bug class warrants. Acceptable per
	// critic feedback: the contract entry-point is documented here and the
	// sibling test catches the gross "handle-doesn't-wake-runtime" regression
	// via its 2s timeout.
	//
	// Allow any (incorrect) enqueue + dispatch to occur. If the bug is
	// present, the runtime would dequeue & dispatch the item, dropping
	// queue.Len() back to 0 — so we sample multiple times to catch the
	// transient enqueue too.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if rt.Queue().Len() != 0 {
			t.Fatalf("rt.Queue().Len() = %d, want 0 (QUM-471: WeaveRuntimeHandle.InterruptDelivery must NOT enqueue — pending entries must remain on disk for peekAndDrainCmd to render via AppendSystemMessage)", rt.Queue().Len())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Pending entries on disk must remain (NOT marked delivered) — they are
	// the peek-and-drain pipeline's input.
	pending, err := agentloop.ListPending(sprawlRoot, name)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("ListPending = %d entries, want 1 (QUM-471: handle must not consume pending entries — peekAndDrainCmd owns that)", len(pending))
	}
	if pending[0].ID != "id-async-1" {
		t.Errorf("pending[0].ID = %q, want %q", pending[0].ID, "id-async-1")
	}
}

// TestWeaveRuntimeHandle_WakeForDelivery_TerminalEventIsCompleted_NotInterrupted
// pins QUM-462 (preserved post-QUM-471): when WeaveRuntimeHandle.InterruptDelivery
// is invoked against an idle runtime (the canonical inbox-arrival wake), it must
// not arm UnifiedRuntime.pendingInterrupt — so a subsequent turn (driven by a
// queued ClassUser item, which simulates what peekAndDrainCmd → bridge.SendMessage
// does under unified mode) terminates as EventTurnCompleted, not EventInterrupted.
//
// Restructure note (QUM-471): under the new contract, InterruptDelivery no longer
// enqueues anything itself. To exercise the wake-vs-pendingInterrupt invariant,
// we manually enqueue a ClassUser item BEFORE calling InterruptDelivery. The
// invariant being pinned is: rt.InterruptDelivery(ctx) is the exclusive wake
// call from the handle, and it must NOT arm pendingInterrupt against an idle
// runtime — even when a user item is then dispatched.
func TestWeaveRuntimeHandle_WakeForDelivery_TerminalEventIsCompleted_NotInterrupted(t *testing.T) {
	sprawlRoot := t.TempDir()
	const name = "weave"

	inner := newFakeBackendSession("sess-weave", backendpkg.Capabilities{})
	mock := &resultEmittingSession{fakeBackendSession: inner}
	rt := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name:       name,
		SprawlRoot: sprawlRoot,
		Session:    mock,
		IsRoot:     true,
	})

	bus := rt.EventBus()
	sub, unsub := bus.Subscribe(64)
	defer unsub()

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	h, err := NewWeaveRuntimeHandle(rt, inner, sprawlRoot, name)
	if err != nil {
		t.Fatalf("NewWeaveRuntimeHandle: %v", err)
	}

	// Wait until the runtime is observably idle so the bug condition (idle
	// runtime + InterruptDelivery) is exercised.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		// Strict 1:1 with the pre-M5 StateIdle wait: idle == Running with no
		// autonomous turn in flight (not mid-turn Running·AutonomousTurn).
		if rt.State() == (liveness.State{Liveness: liveness.Running}) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// QUM-471: post-fix WeaveRuntimeHandle.InterruptDelivery does NOT enqueue
	// anything itself. To drive a turn — so we can observe its terminal event
	// — manually enqueue a ClassUser item, simulating what peekAndDrainCmd
	// does in production via bridge.SendMessage under unified mode.
	rt.Queue().Enqueue(runtimepkg.QueueItem{
		Class:  runtimepkg.ClassUser,
		Prompt: "simulated peekAndDrain user prompt",
	})

	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	// Observe the terminal event for the resulting turn. Must be
	// EventTurnCompleted; EventInterrupted is the regression signature.
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				t.Fatalf("event bus subscription closed before terminal event")
			}
			switch ev.Type {
			case runtimepkg.EventTurnCompleted:
				return // success
			case runtimepkg.EventInterrupted:
				t.Fatalf("terminal event = EventInterrupted, want EventTurnCompleted (QUM-462: WeaveRuntimeHandle.InterruptDelivery must not arm pendingInterrupt against an idle runtime)")
			}
		case <-timeout:
			t.Fatalf("did not observe terminal event within 2s")
		}
	}
}

// ---------------------------------------------------------------------------
// QUM-547: bounded-teardown regression guards for WeaveRuntimeHandle.Stop
// ---------------------------------------------------------------------------
//
// stopActivity (the join on the activity-subscriber goroutine) and
// activityFile.Close() are both potentially-unbounded blocking calls on Stop.
// If either wedges (e.g. observer parked in OnMessage writing to a stuck NFS
// activityFile, or close() hanging on a stuck FD), Stop must bound the wait,
// log, and proceed — not hang forever (which would deadlock weave.lock during
// the QUM-329 handoff cycle).

func TestWeaveRuntimeHandle_Stop_BoundsWedgedStopActivity(t *testing.T) {
	h, _ := buildStartedWeaveRuntimeHandleForTest(t)

	block := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	h.stopActivity = func() {
		<-block
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- h.Stop(context.Background()) }()

	bound := 3 * stopActivityTimeout
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop returned err = %v, want nil", err)
		}
		if elapsed := time.Since(start); elapsed > bound {
			t.Errorf("Stop returned in %v, want <= %v (stopActivity wedge must be bounded)", elapsed, bound)
		}
	case <-time.After(bound):
		t.Fatalf("Stop wedged > %v on wedged stopActivity (QUM-547: join is unbounded)", bound)
	}
}

func TestWeaveRuntimeHandle_Stop_BoundsWedgedActivityClose(t *testing.T) {
	h, _ := buildStartedWeaveRuntimeHandleForTest(t)

	block := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	h.activityClose = func() error {
		<-block
		return nil
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- h.Stop(context.Background()) }()

	bound := 3 * activityCloseTimeout
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop returned err = %v, want nil", err)
		}
		if elapsed := time.Since(start); elapsed > bound {
			t.Errorf("Stop returned in %v, want <= %v (activityClose wedge must be bounded)", elapsed, bound)
		}
	case <-time.After(bound):
		t.Fatalf("Stop wedged > %v on wedged activityClose (QUM-547: close is unbounded)", bound)
	}
}

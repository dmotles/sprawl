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
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
)

// TestWeaveRuntimeHandle_WakeForDelivery_DoesNotEnqueue_LeavesPendingForPeekAndDrain
// pins QUM-471/817: weave's WeaveRuntimeHandle.WakeForDelivery must NOT write
// pending entries to the CLI stdin. The TUI's peekAndDrainCmd (a 2s disk-poll
// backstop fired on AgentTreeMsg while idle) is the sole drain pipeline for
// weave's inbox: it reads pending entries from disk, calls
// AppendSystemMessage(prompt), and routes through bridge.SendMessage so the
// prompt body is rendered in the viewport.
//
// If the handle wrote pending entries to stdin itself, they'd bypass the
// viewport-render path. Contract: pending entries on disk MUST remain in
// pending/ after WakeForDelivery returns; the handle MUST NOT write to stdin.
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

	// QUM-817: the handle must not write pending entries to the CLI stdin.
	// Sample multiple times to catch any transient write.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n := len(mock.writesSnapshot()); n != 0 {
			t.Fatalf("stdin writes = %d, want 0 (QUM-471/817: WeaveRuntimeHandle.WakeForDelivery must NOT write — pending entries stay on disk for peekAndDrainCmd to render)", n)
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

// NOTE (QUM-817): the former
// TestWeaveRuntimeHandle_WakeForDelivery_TerminalEventIsCompleted_NotInterrupted
// (QUM-462) was deleted here. It pinned that WakeForDelivery did not arm
// UnifiedRuntime.pendingInterrupt against an idle runtime. The Go MessageQueue +
// TurnLoop + pendingInterrupt machinery no longer exist (Slice 2): turns are
// driven by stdin writes and observed via the frame router, and WakeForDelivery
// is a no-op for weave. The invariant it guarded has no surface to regress.

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

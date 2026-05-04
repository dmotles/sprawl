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

// TestWeaveRuntimeHandle_isUnifiedHandle_Marker confirms the handle satisfies
// the unifiedRuntimeHandle marker so messages.RecipientResolver classifies
// weave's recipient kind as Unified (skipping the legacy .wake sentinel).
func TestWeaveRuntimeHandle_isUnifiedHandle_Marker(t *testing.T) {
	var h interface{} = &WeaveRuntimeHandle{}
	if _, ok := h.(unifiedRuntimeHandle); !ok {
		t.Errorf("*WeaveRuntimeHandle does not satisfy unifiedRuntimeHandle (isUnifiedHandle marker missing)")
	}
}

// TestWeaveRuntimeHandle_InterruptDelivery_EnqueuesPendingEntries seeds
// pending agentloop entries for weave on disk, then calls InterruptDelivery
// and asserts the runtime delivers the resulting QueueItems via the
// OnQueueItemDelivered callback. Mirrors the unifiedHandle equivalent
// (TestUnifiedHandle_InterruptDelivery_EnqueuesRealAsyncPrompt).
func TestWeaveRuntimeHandle_InterruptDelivery_EnqueuesPendingEntries(t *testing.T) {
	sprawlRoot := t.TempDir()
	const name = "weave"

	// Seed an async pending entry so SplitByClass yields a single inbox item.
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

	captured := newDeliveredItemsCapture()
	mock := newFakeBackendSession("sess-weave", backendpkg.Capabilities{})
	rt := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name:       name,
		SprawlRoot: sprawlRoot,
		Session:    mock,
		IsRoot:     true,
		OnQueueItemDelivered: func(it runtimepkg.QueueItem) {
			captured.record(it)
		},
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

	if err := h.InterruptDelivery(); err != nil {
		t.Fatalf("InterruptDelivery: %v", err)
	}

	items := captured.waitFor(1, 2*time.Second)
	if len(items) != 1 {
		t.Fatalf("delivered items = %d, want 1", len(items))
	}
	if items[0].Class != runtimepkg.ClassInbox {
		t.Errorf("Class = %q, want %q", items[0].Class, runtimepkg.ClassInbox)
	}
	if len(items[0].EntryIDs) != 1 || items[0].EntryIDs[0] != "id-async-1" {
		t.Errorf("EntryIDs = %v, want [id-async-1]", items[0].EntryIDs)
	}
}

// TestWeaveRuntimeHandle_InterruptDelivery_TerminalEventIsCompleted_NotInterrupted
// pins QUM-462: when WeaveRuntimeHandle.InterruptDelivery is invoked against an
// idle runtime (the canonical inbox-arrival wake), the resulting turn must
// terminate as EventTurnCompleted, not EventInterrupted. The earlier
// regression armed UnifiedRuntime.pendingInterrupt against an idle runtime
// inside InterruptDelivery, which caused the wrapper's next StartTurn to
// immediately interrupt itself — banner appeared but Claude never actually
// processed the inbox.
func TestWeaveRuntimeHandle_InterruptDelivery_TerminalEventIsCompleted_NotInterrupted(t *testing.T) {
	sprawlRoot := t.TempDir()
	const name = "weave"

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
		if rt.State() == runtimepkg.StateIdle {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := h.InterruptDelivery(); err != nil {
		t.Fatalf("InterruptDelivery: %v", err)
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

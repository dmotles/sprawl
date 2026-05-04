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

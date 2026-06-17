package runtime

import (
	"context"
	"sort"
	"testing"
	"time"
)

// writePendingUser writes a kind:user prompt and configures the mock to return
// cancelled=true for it (a genuinely-pending message the CLI still holds).
func writePendingUser(t *testing.T, rt *UnifiedRuntime, mock *mockUnifiedSession, text, priority string) string {
	t.Helper()
	uuid, err := rt.WriteUserPrompt(context.Background(), text, priority)
	if err != nil {
		t.Fatalf("WriteUserPrompt(%q): %v", text, err)
	}
	mock.mu.Lock()
	if mock.cancelResults == nil {
		mock.cancelResults = map[string]bool{}
	}
	mock.cancelResults[uuid] = true
	mock.mu.Unlock()
	return uuid
}

// TestRecall_OnlyPendingUserRehydrates_TwoAckModels is the correctness crux:
// recall must rehydrate ONLY genuinely-pending user prompts and must leave
// already-consumed ones alone, correct against BOTH ack models — `next`
// (consumed via the isReplay echo / markConsumed) AND `now` (consumed on write
// via ConfirmDeliveredWithoutReplay).
func TestRecall_OnlyPendingUserRehydrates_TwoAckModels(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	// next-consumed: written, then acked via the isReplay path.
	nextUUID, err := rt.WriteUserPrompt(context.Background(), "next-consumed", "next")
	if err != nil {
		t.Fatalf("write next: %v", err)
	}
	rt.markConsumed(nextUUID)

	// now-consumed: written at priority now, acked on write (no isReplay).
	nowUUID, err := rt.WriteUserPrompt(context.Background(), "now-consumed", "now")
	if err != nil {
		t.Fatalf("write now: %v", err)
	}
	rt.ConfirmDeliveredWithoutReplay(nowUUID)

	// genuinely pending.
	pendingUUID := writePendingUser(t, rt, mock, "still-pending", "next")

	text, err := rt.Recall(context.Background())
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if text != "still-pending" {
		t.Errorf("rehydrated text = %q, want %q", text, "still-pending")
	}

	// Only the pending uuid may be cancelled at the session layer — the consumed
	// ones must be filtered out BEFORE any cancel call.
	got := mock.cancelledUUIDs()
	if len(got) != 1 || got[0] != pendingUUID {
		t.Errorf("cancel calls = %v, want only [%s]", got, pendingUUID)
	}

	out := rt.Outstanding()
	if out[pendingUUID].state != stateCancelled {
		t.Errorf("pending entry state = %v, want stateCancelled", out[pendingUUID].state)
	}
	if out[nextUUID].state != stateConsumed || out[nowUUID].state != stateConsumed {
		t.Errorf("consumed entries changed: next=%v now=%v", out[nextUUID].state, out[nowUUID].state)
	}
}

func TestRecall_CancelledFalse_NotRehydrated_FlippedConsumed(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	uuid, err := rt.WriteUserPrompt(context.Background(), "gone", "next")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	mock.cancelResults[uuid] = false // already dequeued for execution

	text, err := rt.Recall(context.Background())
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if text != "" {
		t.Errorf("rehydrated text = %q, want empty (cancelled:false ⇒ gone)", text)
	}
	if got := rt.Outstanding()[uuid].state; got != stateConsumed {
		t.Errorf("entry state = %v, want stateConsumed", got)
	}
}

func TestRecall_OrderBySeq(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	writePendingUser(t, rt, mock, "A", "next")
	writePendingUser(t, rt, mock, "B", "next")
	writePendingUser(t, rt, mock, "C", "next")

	text, err := rt.Recall(context.Background())
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if text != "A\nB\nC" {
		t.Errorf("rehydrated text = %q, want %q", text, "A\nB\nC")
	}
}

func TestRecall_IgnoresSystemKind(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	// A pending system message must never be cancelled/rehydrated.
	sysUUID, err := rt.WriteSystemMessage(context.Background(), "<system-notification>x</system-notification>", "next", nil)
	if err != nil {
		t.Fatalf("write system: %v", err)
	}
	mock.cancelResults[sysUUID] = true

	text, err := rt.Recall(context.Background())
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if text != "" {
		t.Errorf("rehydrated text = %q, want empty (system kind not recallable)", text)
	}
	if got := mock.cancelledUUIDs(); len(got) != 0 {
		t.Errorf("cancel calls = %v, want none", got)
	}
	if got := rt.Outstanding()[sysUUID].state; got != statePending {
		t.Errorf("system entry state = %v, want statePending (untouched)", got)
	}
}

// TestRecall_DoesNotHoldOutMuAcrossSessionCall proves the lock dance: the mock's
// CancelAsyncMessage calls rt.Outstanding() (which locks outMu). If Recall held
// outMu across the session call this would deadlock; a clean return proves it
// does not.
func TestRecall_DoesNotHoldOutMuAcrossSessionCall(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})
	mock.cancelHook = func(string) { _ = rt.Outstanding() }

	writePendingUser(t, rt, mock, "P", "next")

	done := make(chan struct{})
	go func() {
		_, _ = rt.Recall(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Recall deadlocked — outMu held across CancelAsyncMessage")
	}
}

func TestSendAllNow_SingleNowWrite_SupersedesPending(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	a := writePendingUser(t, rt, mock, "A", "next")
	b := writePendingUser(t, rt, mock, "B", "next")
	c := writePendingUser(t, rt, mock, "C", "next")

	if err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}

	out := rt.Outstanding()
	for _, u := range []string{a, b, c} {
		if out[u].state != stateCancelled {
			t.Errorf("original %s state = %v, want stateCancelled", u, out[u].state)
		}
	}

	// Exactly one now-write, carrying the concatenated text, flipped consumed.
	nowWrites := 0
	var nowText string
	var nowUUID string
	for u, e := range out {
		if e.text == "A\nB\nC" {
			nowText = e.text
			nowUUID = u
		}
	}
	for _, w := range mock.writes {
		if w.Priority == "now" {
			nowWrites++
		}
	}
	if nowWrites != 1 {
		t.Errorf("now-priority writes = %d, want 1", nowWrites)
	}
	if nowText != "A\nB\nC" {
		t.Errorf("now message text = %q, want %q", nowText, "A\nB\nC")
	}
	if nowUUID == "" || out[nowUUID].state != stateConsumed {
		t.Errorf("now message entry not consumed: uuid=%q state=%v", nowUUID, out[nowUUID].state)
	}
}

func TestSendAllNow_NothingPending_NoOp(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	before := mock.writeCount()
	if err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}
	if mock.writeCount() != before {
		t.Errorf("writes happened on empty SendAllNow: before=%d after=%d", before, mock.writeCount())
	}
	if got := mock.cancelledUUIDs(); len(got) != 0 {
		t.Errorf("cancel calls = %v, want none", got)
	}
}

func TestSendAllNow_OnlyCancelledTextConcatenated(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	a := writePendingUser(t, rt, mock, "A", "next")
	b, err := rt.WriteUserPrompt(context.Background(), "B", "next")
	if err != nil {
		t.Fatalf("write B: %v", err)
	}
	mock.cancelResults[b] = false // already executing — excluded from resubmit
	c := writePendingUser(t, rt, mock, "C", "next")

	if err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}

	// All three were attempted.
	got := mock.cancelledUUIDs()
	sort.Strings(got)
	want := []string{a, b, c}
	sort.Strings(want)
	if len(got) != 3 {
		t.Errorf("cancel calls = %v, want all three", got)
	}

	var nowText string
	for _, w := range mock.writes {
		if w.Priority == "now" {
			nowText = w.Message.Content
		}
	}
	if nowText != "A\nC" {
		t.Errorf("now message text = %q, want %q (B was cancelled:false)", nowText, "A\nC")
	}
	if got := rt.Outstanding()[b].state; got != stateConsumed {
		t.Errorf("B state = %v, want stateConsumed", got)
	}
}

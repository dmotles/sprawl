package runtime

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
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

	// now-consumed: written at priority now, acked on write. This fixture
	// synthesizes no isReplay echo for it — not a claim that none would arrive
	// (QUM-1068: one usually does, and is a no-op).
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

	if _, err := rt.SendAllNow(context.Background()); err != nil {
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

// drainEvents collects every RuntimeEvent currently buffered on ch, returning
// once the channel goes quiet for the idle window. SendAllNow publishes
// synchronously, so all its events are buffered by the time it returns.
func drainEvents(ch <-chan RuntimeEvent) []RuntimeEvent {
	var events []RuntimeEvent
	for {
		select {
		case ev := <-ch:
			events = append(events, ev)
		case <-time.After(200 * time.Millisecond):
			return events
		}
	}
}

// TestSendAllNow_PublishesUserMessageSentForNowWrite is the QUM-838 regression:
// the coalesced now-write MUST publish EventUserMessageSent (carrying its fresh
// uuid + text) BEFORE its EventUserMessageConsumed, so the TUI pending zone can
// track the uuid and settle it into the committed transcript. Without the sent
// event the consume settle is a no-op (untracked uuid) and the Ctrl+G message
// vanishes from the transcript.
func TestSendAllNow_PublishesUserMessageSentForNowWrite(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	ch, unsub := rt.EventBus().SubscribeNamed("sendnow-test", 32)
	defer unsub()

	a := writePendingUser(t, rt, mock, "AAA", "next")
	b := writePendingUser(t, rt, mock, "BBB", "next")

	if _, err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}

	events := drainEvents(ch)

	sentIdx, consumedIdx := -1, -1
	var sentUUID, sentText string
	for i, ev := range events {
		switch ev.Type {
		case EventUserMessageSent:
			if sentIdx != -1 {
				t.Fatalf("EventUserMessageSent published more than once (at idx %d and %d), want exactly 1", sentIdx, i)
			}
			sentIdx = i
			sentUUID = ev.UUID
			sentText = ev.Prompt
		case EventUserMessageConsumed:
			consumedIdx = i
		}
	}

	if sentIdx == -1 {
		t.Fatalf("SendAllNow did not publish EventUserMessageSent for the now-write (QUM-838: Ctrl+G bubble vanishes)")
	}
	if sentText != "AAA\nBBB" {
		t.Errorf("EventUserMessageSent.Prompt = %q, want %q", sentText, "AAA\nBBB")
	}
	if sentUUID == "" || sentUUID == a || sentUUID == b {
		t.Errorf("EventUserMessageSent.UUID = %q, want a fresh now-write uuid (not %q/%q)", sentUUID, a, b)
	}
	if consumedIdx == -1 {
		t.Fatalf("SendAllNow published no EventUserMessageConsumed for the now-write")
	}
	if sentIdx > consumedIdx {
		t.Errorf("EventUserMessageSent (idx %d) must precede EventUserMessageConsumed (idx %d) so the zone is populated before settle", sentIdx, consumedIdx)
	}
	if events[consumedIdx].UUID != sentUUID {
		t.Errorf("consumed uuid = %q, want the now-write uuid %q", events[consumedIdx].UUID, sentUUID)
	}
}

// TestSendAllNow_NothingPending_NoSentEvent guards against publishing an empty
// phantom bubble: a no-op send-all-now must publish no EventUserMessageSent.
func TestSendAllNow_NothingPending_NoSentEvent(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	ch, unsub := rt.EventBus().SubscribeNamed("sendnow-noop-test", 16)
	defer unsub()

	if _, err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}

	for _, ev := range drainEvents(ch) {
		if ev.Type == EventUserMessageSent {
			t.Errorf("empty SendAllNow published EventUserMessageSent (phantom bubble), want none")
		}
		if ev.Type == EventUserMessageConsumed {
			t.Errorf("empty SendAllNow published EventUserMessageConsumed (phantom settle), want none")
		}
	}
}

func TestSendAllNow_NothingPending_NoOp(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	before := mock.writeCount()
	if _, err := rt.SendAllNow(context.Background()); err != nil {
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

	if _, err := rt.SendAllNow(context.Background()); err != nil {
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

// TestSendAllNow_IgnoresSystemKind is the QUM-925 twin of
// TestRecall_IgnoresSystemKind: Ctrl+G must promote ONLY user prompts to
// priority `now`. A pending system frame must be neither cancelled nor
// re-written — the user cannot displace or promote a notification.
func TestSendAllNow_IgnoresSystemKind(t *testing.T) {
	// The root system write arms guardSubmitted once the fast path widens.
	withShortSubmittedTimeout(t, 40*time.Millisecond)
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", IsRoot: true, Session: mock})

	sysUUID, err := rt.WriteSystemMessage(context.Background(), "<system-notification>x</system-notification>", "next", nil)
	if err != nil {
		t.Fatalf("write system: %v", err)
	}
	mock.mu.Lock()
	mock.cancelResults[sysUUID] = true // would cancel IF it were ever attempted
	mock.mu.Unlock()
	userUUID := writePendingUser(t, rt, mock, "typed", "next")

	if _, err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}

	got := mock.cancelledUUIDs()
	if len(got) != 1 || got[0] != userUUID {
		t.Errorf("cancel calls = %v, want only the user uuid [%s] (system frames are not promotable)", got, userUUID)
	}
	if st := rt.Outstanding()[sysUUID].state; st != statePending {
		t.Errorf("system entry state = %v, want statePending (untouched by send-all-now)", st)
	}

	var nowText string
	mock.mu.Lock()
	for _, w := range mock.writes {
		if w.Priority == "now" {
			nowText = w.Message.Content
		}
	}
	mock.mu.Unlock()
	if nowText != "typed" {
		t.Errorf("coalesced now-write text = %q, want %q (must not include the system frame)", nowText, "typed")
	}
}

// TestSendAllNow_SystemFrame_StaysNextPriority asserts the literal AC ("system
// frames stay at `next`") rather than a proxy: after Ctrl+G, the system frame's
// original write is still the only write carrying its text, and it is still at
// priority `next`.
func TestSendAllNow_SystemFrame_StaysNextPriority(t *testing.T) {
	// The root system write arms guardSubmitted once the fast path widens.
	withShortSubmittedTimeout(t, 40*time.Millisecond)
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", IsRoot: true, Session: mock})

	const sysText = "<system-notification>keep-me</system-notification>"
	sysUUID, err := rt.WriteSystemMessage(context.Background(), sysText, "next", nil)
	if err != nil {
		t.Fatalf("write system: %v", err)
	}
	// The mock would report this frame as successfully cancelled IF send-all-now
	// ever attempted it — without this the test would pass vacuously even against
	// a runtime that promotes system frames (cancelled:false ⇒ text dropped).
	mock.mu.Lock()
	mock.cancelResults[sysUUID] = true
	mock.mu.Unlock()
	writePendingUser(t, rt, mock, "typed", "next")

	if _, err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}

	mock.mu.Lock()
	writes := append([]protocol.UserMessage(nil), mock.writes...)
	mock.mu.Unlock()

	seen := 0
	for _, w := range writes {
		if !strings.Contains(w.Message.Content, sysText) {
			continue
		}
		seen++
		if w.Priority != "next" {
			t.Errorf("write carrying the system frame has Priority = %q, want %q", w.Priority, "next")
		}
	}
	if seen != 1 {
		t.Errorf("writes carrying the system frame = %d, want exactly 1 (never re-written at `now`)", seen)
	}
}

// --- QUM-1112: SendAllNow must never destroy already-cancelled text ---

// textSet is a set of prompt texts, used to answer the QUM-1112 reachability
// question structurally rather than by string-containment.
type textSet map[string]bool

func (s textSet) has(text string) bool { return s[text] }

func (s textSet) keys() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// splitJoined explodes a newline-joined blob back into the individual prompt
// texts SendAllNow coalesced. "" means nothing was handed back (NOT one empty
// prompt). This is why every fixture prompt below must be single-line — see the
// guard in reachability.
func splitJoined(joined string) []string {
	if joined == "" {
		return nil
	}
	return strings.Split(joined, "\n")
}

// reachability partitions the typed prompt texts into the three places one may
// legitimately live after a SendAllNow: handed back to the caller (→ restored
// to the TUI input), still pending in the outstanding set (→ reachable via
// Ctrl+U), or written to the CLI stdin (→ on the wire). This is the QUM-1112
// invariant made checkable: a text in NONE of the three has been destroyed.
// Each set is built structurally (returned value / entry state / write trace),
// never by grepping a struct that happens to still hold it.
//
// The sets are sets of LINES, so a multi-line fixture prompt would read as two
// texts and quietly invalidate the instrument; `fixtures` is the full set of
// typed texts and is guarded for that.
func reachability(t *testing.T, rt *UnifiedRuntime, mock *mockUnifiedSession, recovered string, wireFrom int, fixtures []string) (handedBack, stillPending, onWire textSet) {
	t.Helper()
	for _, f := range fixtures {
		if strings.Contains(f, "\n") {
			t.Fatalf("fixture prompt %q contains a newline; reachability partitions by line and cannot attribute it", f)
		}
	}
	handedBack, stillPending, onWire = textSet{}, textSet{}, textSet{}
	for _, s := range splitJoined(recovered) {
		handedBack[s] = true
	}
	for _, e := range rt.Outstanding() {
		if e.kind == kindUser && e.state == statePending {
			stillPending[e.text] = true
		}
	}
	trace := mock.writeTrace()
	if wireFrom > len(trace) {
		t.Fatalf("wireFrom=%d exceeds write trace length %d", wireFrom, len(trace))
	}
	for _, w := range trace[wireFrom:] {
		for _, s := range splitJoined(w.Message.Content) {
			onWire[s] = true
		}
	}
	return handedBack, stillPending, onWire
}

// assertHandbackWireDisjoint pins the stronger half of the QUM-1112 contract:
// the invariant says text must be reachable in AT LEAST ONE of the three
// places, but text in BOTH the handback and the wire is a distinct defect —
// the caller restores a prompt the model already received, and the user
// re-sends it. Cheaper to see than the loss it replaces, and quieter.
func assertHandbackWireDisjoint(t *testing.T, handedBack, onWire textSet) {
	t.Helper()
	for text := range handedBack {
		if onWire.has(text) {
			t.Errorf("text %q is BOTH handed back (%v) and on the wire (%v) — restoring it to the input duplicates a prompt the model already received (QUM-1112)", text, handedBack.keys(), onWire.keys())
		}
	}
}

// countEvents tallies published RuntimeEvents of one type, by UUID.
func countEvents(events []RuntimeEvent, typ RuntimeEventType) map[string]int {
	out := map[string]int{}
	for _, ev := range events {
		if ev.Type == typ {
			out[ev.UUID]++
		}
	}
	return out
}

// TestSendAllNow_PartialCancelFailure_PreservesCancelledText is the QUM-1112
// crux. With two prompts pending and one prompt's cancel failing on the wire,
// the OTHER has already been flipped out of statePending (invisible to Ctrl+U)
// and its bubble already ZoneDropped by the TUI. If SendAllNow then returns
// early and drops the collected texts, that text exists nowhere: not in the
// input, not in the outstanding set, not on the wire. The user's typed prompt is
// destroyed with only a generic error toast.
//
// The assertion therefore measures REACHABILITY, not the returned error: every
// typed text must be found in at least one of {handed back, still pending, on
// the wire}. An assertion that only checked `err != nil` would pass against the
// defect.
//
// Both failure positions are exercised. cancelPendingUser is best-effort today
// (it records firstErr and continues), so the two readings "everything that
// actually cancelled" and "everything collected before the first error" coincide
// at this commit; failing the FIRST uuid guards the case where a future refactor
// makes that helper bail early, which would destroy the later prompt's text
// while a last-uuid-only fixture stayed green.
func TestSendAllNow_PartialCancelFailure_PreservesCancelledText(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failIdx     int
		wantBack    string // exact recovered text
		wantPending string // the failed-cancel prompt, left recallable
	}{
		{name: "second cancel fails", failIdx: 1, wantBack: "A", wantPending: "B"},
		{name: "first cancel fails", failIdx: 0, wantBack: "B", wantPending: "A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockUnifiedSession{cancelResults: map[string]bool{}, cancelErrs: map[string]error{}}
			rt := New(RuntimeConfig{Name: "weave", Session: mock})
			ch, unsub := rt.EventBus().SubscribeNamed("qum1112-partial-cancel", 32)
			defer unsub()

			fixtures := []string{"A", "B"}
			uuids := []string{
				writePendingUser(t, rt, mock, "A", "next"),
				writePendingUser(t, rt, mock, "B", "next"),
			}
			injected := errors.New("cancel wire failure")
			mock.mu.Lock()
			mock.cancelErrs[uuids[tc.failIdx]] = injected
			mock.mu.Unlock()

			// uuid of the prompt that DID cancel — the one whose text is at risk.
			okUUID := uuids[1-tc.failIdx]
			failUUID := uuids[tc.failIdx]

			wireFrom := len(mock.writeTrace())
			recovered, err := rt.SendAllNow(context.Background())
			if !errors.Is(err, injected) {
				t.Fatalf("SendAllNow err = %v, want the injected cancel failure (a swallowed error shows the user the wrong cause)", err)
			}

			handedBack, stillPending, onWire := reachability(t, rt, mock, recovered, wireFrom, fixtures)

			for _, text := range fixtures {
				if !handedBack.has(text) && !stillPending.has(text) && !onWire.has(text) {
					t.Errorf("typed text %q is UNREACHABLE after a partial-cancel failure: "+
						"not handed back to the caller (%v), not still pending in the outstanding set (%v), "+
						"not on the wire (%v) — the user's text was destroyed (QUM-1112)",
						text, handedBack.keys(), stillPending.keys(), onWire.keys())
				}
			}

			// AC 2 "state which": the successfully-cancelled prompt is handed back
			// to the caller (→ the TUI input); the failed-cancel prompt is
			// untouched and stays in the outstanding set (→ Ctrl+U). The handback
			// choice is not arbitrary: its cancel already succeeded on the wire, so
			// leaving it statePending would be a lie — a later Ctrl+U would cancel
			// it again, get cancelled:false, markConsumed it, and drop the text
			// silently. Pinning both choices also rejects a fix that preserves the
			// text somewhere ELSE (writing it at `now` while the failed one is
			// still queued, reordering the conversation).
			if recovered != tc.wantBack {
				t.Errorf("recovered text = %q, want exactly %q", recovered, tc.wantBack)
			}
			if stillPending.has(tc.wantBack) {
				t.Errorf("still-pending set = %v, must NOT contain %q — it was flipped to stateCancelled", stillPending.keys(), tc.wantBack)
			}
			if !stillPending.has(tc.wantPending) {
				t.Errorf("still-pending set = %v, want the failed-cancel %q left pending and recallable", stillPending.keys(), tc.wantPending)
			}
			if handedBack.has(tc.wantPending) {
				t.Errorf("handed-back set = %v, must NOT contain %q — it is still queued at the CLI; handing it back too would duplicate it", handedBack.keys(), tc.wantPending)
			}
			assertHandbackWireDisjoint(t, handedBack, onWire)
			if len(onWire) != 0 {
				// Abort-over-partial-send: the flush is all-or-nothing. Writing the
				// cancelled text at `now` while the failed one is still queued at
				// `next` would silently reorder the user's prompts, and the caller
				// has no way to learn that only part of the flush landed.
				t.Errorf("wire writes after the failed flush = %v, want none (the flush aborts)", onWire.keys())
			}

			// The handback is only SAFE because the TUI has already dropped the
			// bubble for the cancelled prompt (EventUserMessageCancelled →
			// ZoneDrop). If that publish ever stops happening on this path, the
			// bubble survives, the handback lands in the input too, and the user
			// sees the prompt twice.
			events := drainEvents(ch)
			cancelled := countEvents(events, EventUserMessageCancelled)
			if cancelled[okUUID] != 1 {
				t.Errorf("EventUserMessageCancelled for the cancelled prompt = %d, want exactly 1 (its bubble must be dropped, or the handback duplicates it)", cancelled[okUUID])
			}
			if cancelled[failUUID] != 0 {
				t.Errorf("EventUserMessageCancelled published %d time(s) for the FAILED-cancel prompt, want 0 (its bubble must survive — it is still queued)", cancelled[failUUID])
			}
			if sent := countEvents(events, EventUserMessageSent); len(sent) != 0 {
				t.Errorf("EventUserMessageSent published %v on an aborted flush, want none (a phantom bubble plus the handback duplicates the prompt)", sent)
			}
		})
	}
}

// TestSendAllNow_WriteFailureAfterCancel_PreservesCancelledText covers the
// SECOND loss site of the same invariant: every entry cancels cleanly, then the
// coalesced now-write fails. writeMessage deletes its own outstanding entry on a
// write error, so after that point the joined text exists nowhere in the process
// unless SendAllNow hands it back. A fix that only patches the cancel-error
// early return still fails this.
//
// NOTE: the `onWire` leg of the reachability disjunction is structurally zero
// here — the mock returns writeErr BEFORE recording the write, which is the
// point. TestSendAllNow_Success_ReturnsNoRecoveredText is the proof that leg can
// be non-empty at all.
func TestSendAllNow_WriteFailureAfterCancel_PreservesCancelledText(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}, cancelErrs: map[string]error{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})
	ch, unsub := rt.EventBus().SubscribeNamed("qum1112-write-fail", 32)
	defer unsub()

	fixtures := []string{"A", "B"}
	a := writePendingUser(t, rt, mock, "A", "next")
	b := writePendingUser(t, rt, mock, "B", "next")

	wireFrom := len(mock.writeTrace())
	injected := errors.New("stdin closed")
	mock.mu.Lock()
	mock.writeErr = injected
	mock.mu.Unlock()

	recovered, err := rt.SendAllNow(context.Background())
	if !errors.Is(err, injected) {
		t.Fatalf("SendAllNow err = %v, want the injected write failure", err)
	}

	handedBack, stillPending, onWire := reachability(t, rt, mock, recovered, wireFrom, fixtures)

	for _, text := range fixtures {
		if !handedBack.has(text) && !stillPending.has(text) && !onWire.has(text) {
			t.Errorf("typed text %q is UNREACHABLE after a post-cancel write failure: "+
				"handedBack=%v stillPending=%v onWire=%v (QUM-1112)",
				text, handedBack.keys(), stillPending.keys(), onWire.keys())
		}
	}
	assertHandbackWireDisjoint(t, handedBack, onWire)
	if recovered != "A\nB" {
		t.Errorf("recovered text = %q, want %q — the handback is the ONLY surviving copy", recovered, "A\nB")
	}
	if len(stillPending) != 0 {
		t.Errorf("still-pending set = %v, want empty (both entries were cancelled)", stillPending.keys())
	}

	events := drainEvents(ch)
	cancelled := countEvents(events, EventUserMessageCancelled)
	for _, u := range []string{a, b} {
		if cancelled[u] != 1 {
			t.Errorf("EventUserMessageCancelled for %s = %d, want exactly 1 (bubbles dropped, so the handback cannot duplicate)", u, cancelled[u])
		}
	}
	if sent := countEvents(events, EventUserMessageSent); len(sent) != 0 {
		t.Errorf("EventUserMessageSent published %v despite the write failing, want none (phantom bubble)", sent)
	}
}

// TestSendAllNow_Success_ReturnsNoRecoveredText is the negative control against
// over-fixing: on the happy path the text is on the WIRE, so handing it back too
// would make the TUI restore text it already sent (duplicate submission). It is
// also the only positive observation of a non-empty `onWire` set, which makes it
// the control for that leg of the reachability instrument.
//
// Mutation-verified: `return "", nil` → `return joined, nil` at SendAllNow's
// tail fails this with `recovered text = "A\nB", want ""`.
func TestSendAllNow_Success_ReturnsNoRecoveredText(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	fixtures := []string{"A", "B"}
	writePendingUser(t, rt, mock, "A", "next")
	writePendingUser(t, rt, mock, "B", "next")

	wireFrom := len(mock.writeTrace())
	recovered, err := rt.SendAllNow(context.Background())
	if err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}

	handedBack, stillPending, onWire := reachability(t, rt, mock, recovered, wireFrom, fixtures)
	assertHandbackWireDisjoint(t, handedBack, onWire)
	if recovered != "" {
		t.Errorf("recovered text = %q, want \"\" on the success path (handedBack=%v) — the text is on the wire and must not also be restored to the input", recovered, handedBack.keys())
	}
	if !onWire.has("A") || !onWire.has("B") {
		t.Errorf("wire set = %v, want both %q and %q", onWire.keys(), "A", "B")
	}
	if len(stillPending) != 0 {
		t.Errorf("still-pending set = %v, want empty", stillPending.keys())
	}
}

// TestRecall_PartialCancelFailure_Unchanged is AC 4: Recall already returns the
// partial text alongside the error and must keep doing so.
//
// Mutation-verified: making Recall early-return `"", err` on a partial cancel
// failure fails this with `Recall recovered text = "", want "A"`.
func TestRecall_PartialCancelFailure_Unchanged(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}, cancelErrs: map[string]error{}}
	rt := New(RuntimeConfig{Name: "weave", Session: mock})

	writePendingUser(t, rt, mock, "A", "next")
	b := writePendingUser(t, rt, mock, "B", "next")
	injected := errors.New("cancel wire failure")
	mock.mu.Lock()
	mock.cancelErrs[b] = injected
	mock.mu.Unlock()

	recovered, err := rt.Recall(context.Background())
	if !errors.Is(err, injected) {
		t.Fatalf("Recall err = %v, want the injected cancel failure", err)
	}
	if recovered != "A" {
		t.Errorf("Recall recovered text = %q, want %q alongside the error", recovered, "A")
	}
	pending := textSet{}
	for _, e := range rt.Outstanding() {
		if e.kind == kindUser && e.state == statePending {
			pending[e.text] = true
		}
	}
	if !pending.has("B") {
		t.Errorf("still-pending set = %v, want the failed-cancel %q left pending", pending.keys(), "B")
	}
}

package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// QUM-833: the live + restart double-render regression. System-notification
// frames must render system-styled, EXACTLY ONCE, via the uuid-keyed pending
// zone — never as a raw user bubble (the blind AppendUser(queuedText) bug) and
// never twice.

// idleTrackingApp returns a ready, sized, TurnIdle AppModel whose fake bridge
// mints a distinct uuid per SendMessage so the full sent→consume flow is
// exercisable.
func idleTrackingApp(t *testing.T) (AppModel, *fakeSessionBackend) {
	t.Helper()
	fake := newFakeSessionBackend()
	fake.trackSends = true
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return resized.(AppModel), fake
}

func rootChat(app AppModel) *ChatList { return app.viewportFor(app.rootAgent).ChatList() }

func deliver(t *testing.T, app AppModel, msg tea.Msg) AppModel {
	t.Helper()
	updated, _ := app.Update(msg)
	return updated.(AppModel)
}

func countSystemItems(cl *ChatList) int {
	n := 0
	for _, it := range cl.Items() {
		if _, ok := it.(*SystemNotificationItem); ok {
			n++
		}
	}
	return n
}

func countUserItems(cl *ChatList) int {
	n := 0
	for _, it := range cl.Items() {
		if _, ok := it.(*UserItem); ok {
			n++
		}
	}
	return n
}

// committedTypeOrder returns a compact type string per committed item, e.g.
// "user,user,notification", for asserting consume ordering.
func committedTypeOrder(cl *ChatList) []string {
	var out []string
	for _, it := range cl.Items() {
		out = append(out, itemTypeKey(it))
	}
	return out
}

// userTextsInOrder returns the bodies of committed user items in order.
func userTextsInOrder(cl *ChatList) []string {
	var out []string
	for _, it := range cl.Items() {
		if u, ok := it.(*UserItem); ok {
			out = append(out, u.Text())
		}
	}
	return out
}

// CORE REGRESSION — a live system-notification frame renders system-styled,
// exactly once, NOT as a raw user bubble.
func TestUserMessageConsumed_SystemNotification_SystemStyledOnce_NotRawBubble(t *testing.T) {
	app, _ := idleTrackingApp(t)

	app = deliver(t, app, UserMessageSentMsg{UUID: "n1", Text: notifFrameA})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "n1"})

	cl := rootChat(app)
	if countUserItems(cl) != 0 {
		t.Errorf("a system-notification rendered as %d user bubble(s); want 0 (must be system-styled)", countUserItems(cl))
	}
	if countSystemItems(cl) != 1 {
		t.Fatalf("system items = %d, want 1", countSystemItems(cl))
	}
	out := stripAnsi(cl.Render(100))
	if strings.Contains(out, "<system-notification") {
		t.Errorf("raw tag leaked to chat:\n%s", out)
	}
	if c := strings.Count(out, "alpha → working"); c != 1 {
		t.Errorf("notification body rendered %d times, want exactly 1:\n%s", c, out)
	}
}

// Stacked notifications in one frame each render as distinct system entries.
func TestUserMessageConsumed_StackedNotifications_DistinctEntries(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "n1", Text: notifFrameA + notifFrameB})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "n1"})

	cl := rootChat(app)
	if countSystemItems(cl) != 2 {
		t.Fatalf("system items = %d, want 2 (stacked envelopes split distinctly)", countSystemItems(cl))
	}
	if countUserItems(cl) != 0 {
		t.Errorf("user bubbles = %d, want 0", countUserItems(cl))
	}
}

// T4 — InboxDrainMsg no longer eager-appends; render happens on consume.
func TestInboxDrainMsg_NoEagerSystemAppend_RendersOnConsume(t *testing.T) {
	app, fake := idleTrackingApp(t)

	app = deliver(t, app, InboxDrainMsg{Prompt: notifFrameA, EntryIDs: []string{"e1"}, Class: "async"})
	cl := rootChat(app)
	if cl.Len() != 0 {
		t.Fatalf("committed Len = %d immediately after InboxDrainMsg, want 0 (no eager AppendSystemNotification)", cl.Len())
	}
	if fake.sendCalls != 1 {
		t.Fatalf("SendMessage calls = %d, want 1 (drain still writes to stdin)", fake.sendCalls)
	}

	// Drive the sent ack: the drain wrote with the bridge-minted uuid (u1); the
	// fake mints "u<sendCalls>".
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: notifFrameA})
	if rootChat(app).Len() != 0 {
		t.Fatalf("committed Len = %d after sent, want 0 (still pending in zone)", rootChat(app).Len())
	}
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u1"})

	cl = rootChat(app)
	if countSystemItems(cl) != 1 {
		t.Fatalf("system items = %d after consume, want 1", countSystemItems(cl))
	}
	out := stripAnsi(cl.Render(100))
	if c := strings.Count(out, "alpha → working"); c != 1 {
		t.Errorf("notification body rendered %d times, want 1 (no eager + consume double):\n%s", c, out)
	}
}

// T5 — out-of-order consume commits in consume order, not submit order.
func TestUserMessage_OutOfOrderConsume_CommittedInConsumeOrder(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "a", Text: "AAA"})
	app = deliver(t, app, UserMessageSentMsg{UUID: "b", Text: "BBB"})

	// B consumes first (now-priority / send-all-now), then A.
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "b"})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "a"})

	got := userTextsInOrder(rootChat(app))
	want := []string{"BBB", "AAA"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("committed user order = %v, want %v (consume order, not submit order)", got, want)
	}
}

// T6 (C4) — a notification arriving between a prompt's submit and its echo must
// still commit in consume order: prompt consumes first → [user, notification].
func TestUserMessage_NotifBetweenSubmitAndEcho_OrderUserThenNotif(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u", Text: "my prompt"})
	app = deliver(t, app, UserMessageSentMsg{UUID: "n", Text: notifFrameA})

	// FIFO: prompt processed before the notification.
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u"})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "n"})

	order := committedTypeOrder(rootChat(app))
	want := []string{"user", "notification"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("committed order = %v, want %v (zone+relocate, not eager-into-committed)", order, want)
	}
}

// T7 — recall (Ctrl+U / supersede) drops a pending user entry with no phantom
// bubble.
func TestUserMessageCancelled_DropsUserEntry_NoPhantom(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "a", Text: "AAA"})
	app = deliver(t, app, UserMessageSentMsg{UUID: "b", Text: "BBB"})

	app = deliver(t, app, UserMessageCancelledMsg{UUID: "a"})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "b"})

	cl := rootChat(app)
	if got := userTextsInOrder(cl); strings.Join(got, ",") != "BBB" {
		t.Errorf("committed user texts = %v, want [BBB] (recalled A must not render)", got)
	}
}

// T8 (C7) — send-all-now coalescing: two pending user prompts are cancelled
// (per-uuid) and replaced by one coalesced now-frame with a NEW uuid. The
// result is a single committed bubble in consume order, no phantom of the
// cancelled originals.
func TestUserMessage_SendAllNowCoalesce_SingleBubble(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "a", Text: "AAA"})
	app = deliver(t, app, UserMessageSentMsg{UUID: "b", Text: "BBB"})

	// Ctrl+G coalesces: the CLI cancels a and b, writes one now-frame (uuid g).
	app = deliver(t, app, UserMessageCancelledMsg{UUID: "a"})
	app = deliver(t, app, UserMessageCancelledMsg{UUID: "b"})
	if rootChat(app).zone.len() != 0 {
		t.Fatalf("zone len = %d after coalesce-cancel, want 0", rootChat(app).zone.len())
	}
	app = deliver(t, app, UserMessageSentMsg{UUID: "g", Text: "AAA\nBBB"})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "g"})

	cl := rootChat(app)
	if got := userTextsInOrder(cl); strings.Join(got, "|") != "AAA\nBBB" {
		t.Errorf("committed user texts = %v, want one coalesced bubble [AAA\\nBBB]", got)
	}
	if countUserItems(cl) != 1 {
		t.Errorf("user bubbles = %d, want 1 (cancelled originals must not render)", countUserItems(cl))
	}
}

// C6 — composite: recall + out-of-order consume + notification. Zone starts
// [A·user, B·user, N·sys]; B consumes (now-priority), A is recalled, N consumes.
// Committed must be [B, N] — consume order, recalled A gone.
func TestUserMessage_RecallReorderNotif_Composite(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "a", Text: "AAA"})
	app = deliver(t, app, UserMessageSentMsg{UUID: "b", Text: "BBB"})
	app = deliver(t, app, UserMessageSentMsg{UUID: "n", Text: notifFrameA})

	app = deliver(t, app, UserMessageConsumedMsg{UUID: "b"})
	app = deliver(t, app, UserMessageCancelledMsg{UUID: "a"})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "n"})

	order := committedTypeOrder(rootChat(app))
	want := []string{"user", "notification"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("composite committed order = %v, want %v", order, want)
	}
	if got := userTextsInOrder(rootChat(app)); strings.Join(got, ",") != "BBB" {
		t.Errorf("composite committed user texts = %v, want [BBB] (A recalled)", got)
	}
}

// G-3 — a system entry settling BETWEEN two user entries lands in consume order:
// consume U1, consume N, consume U2 → [user, notification, user].
func TestUserMessage_SystemSettlesBetweenUsers_Order(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: "first"})
	app = deliver(t, app, UserMessageSentMsg{UUID: "n", Text: notifFrameA})
	app = deliver(t, app, UserMessageSentMsg{UUID: "u2", Text: "second"})

	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u1"})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "n"})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u2"})

	order := committedTypeOrder(rootChat(app))
	want := []string{"user", "notification", "user"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("committed order = %v, want %v (relocate at consume-ordered tail)", order, want)
	}
}

// T9 — the legacy empty-uuid bridge path (no consume echo) still classifies:
// a system frame renders system-styled immediately, not as a raw user bubble.
func TestUserMessageSent_EmptyUUID_ClassifiesSystemVsUser(t *testing.T) {
	app, _ := idleTrackingApp(t)

	app = deliver(t, app, UserMessageSentMsg{UUID: "", Text: notifFrameA})
	cl := rootChat(app)
	if countSystemItems(cl) != 1 || countUserItems(cl) != 0 {
		t.Fatalf("empty-uuid system frame: system=%d user=%d, want system=1 user=0",
			countSystemItems(cl), countUserItems(cl))
	}
	if cl.zone.len() != 0 {
		t.Errorf("empty-uuid path must commit directly, not populate the zone; zone len=%d", cl.zone.len())
	}

	app = deliver(t, app, UserMessageSentMsg{UUID: "", Text: "plain typed"})
	cl = rootChat(app)
	if countUserItems(cl) != 1 {
		t.Errorf("empty-uuid plain frame: user items = %d, want 1", countUserItems(cl))
	}
}

// T10 (C9) — a consume for a uuid the TUI never tracked is a no-op: nothing
// committed, no raw append. The formal restart-orphan guard.
func TestUserMessageConsumed_UntrackedUUID_NoOp(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "never-sent"})
	if rootChat(app).Len() != 0 {
		t.Errorf("committed Len = %d, want 0 (untracked consume must not blind-append)", rootChat(app).Len())
	}
}

// L7 (unit) — restart/replay parity: a transcript-replayed notification renders
// exactly once; a late orphan consume for an untracked uuid does not double it.
func TestRestart_TranscriptReplay_NoZoneDouble(t *testing.T) {
	app, _ := idleTrackingApp(t)
	cl := rootChat(app)
	cl.Reset([]MessageEntry{{
		Type:             MessageSystemNotification,
		Content:          "alpha → working",
		NotificationType: NotificationKindStatusChange,
		Complete:         true,
	}})
	if countSystemItems(cl) != 1 {
		t.Fatalf("replayed system items = %d, want 1", countSystemItems(cl))
	}

	// A stale isReplay echo for a uuid this fresh process never sent (an old
	// incarnation's frame) must NOT re-render the replayed notification.
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "old-incarnation-uuid"})

	// AND a genuinely new live notification arriving post-restart must settle
	// through the zone alongside the replayed one — both render exactly once,
	// no collision between the replay-committed item and the zone-relocated one.
	app = deliver(t, app, UserMessageSentMsg{UUID: "live1", Text: notifFrameB})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "live1"})

	cl = rootChat(app)
	if countSystemItems(cl) != 2 {
		t.Errorf("system items = %d, want 2 (1 replayed + 1 live, no double, no orphan)", countSystemItems(cl))
	}
	if countUserItems(cl) != 0 {
		t.Errorf("user bubbles = %d, want 0 (no raw re-append on restart)", countUserItems(cl))
	}
	out := stripAnsi(cl.Render(100))
	if strings.Contains(out, "<system-notification") {
		t.Errorf("raw tag leaked after restart:\n%s", out)
	}
	if c := strings.Count(out, "alpha → working"); c != 1 {
		t.Errorf("replayed notification body rendered %d times, want 1:\n%s", c, out)
	}
	if c := strings.Count(out, "beta heads up"); c != 1 {
		t.Errorf("live post-restart notification body rendered %d times, want 1:\n%s", c, out)
	}
}

// SessionRestartingMsg clears the pending zone (queue projection) and surfaces
// the dropped-queue banner.
func TestSessionRestarting_ClearsZone(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "a", Text: "will drop"})
	app = deliver(t, app, UserMessageSentMsg{UUID: "b", Text: "also drop"})
	if rootChat(app).zone.userCount() != 2 {
		t.Fatalf("setup: zone user count = %d, want 2", rootChat(app).zone.userCount())
	}

	app = deliver(t, app, SessionRestartingMsg{Reason: "x"})

	if rootChat(app).zone.len() != 0 {
		t.Errorf("zone len = %d, want 0 after session restart", rootChat(app).zone.len())
	}
	if !statusBarContains(app, "queued message") {
		t.Errorf("status bar should carry the dropped-queue banner after restart")
	}
}

package tui

import (
	"strings"
	"testing"
)

// QUM-833: pending-zone + uuid-keyed instant render. These tests pin the zone
// data structure, the single classifier, and the ChatList render/relocate
// integration. The behavioral contract: an inbound system-notification frame
// renders system-styled, EXACTLY ONCE, and never leaks its raw tag.

const (
	notifFrameA = `<system-notification type="status_change">alpha → working</system-notification>`
	notifFrameB = `<system-notification type="message" interrupt="true">beta heads up</system-notification>`
)

// T1 — the single classifier.
func TestClassifyInboundFrame(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"system prefix", notifFrameA, true},
		{"system prefix with leading whitespace", "\n  " + notifFrameA, true},
		{"stacked envelopes", notifFrameA + notifFrameB, true},
		{"plain text", "hello world", false},
		{"empty", "", false},
		{"mentions tag mid-body", "I typed <system-notification> by hand", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyInboundFrame(tc.text); got != tc.want {
				t.Errorf("classifyInboundFrame(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// T2 — UserMessageSentMsg of a system frame creates N zone entries, 0 committed,
// no raw tag anywhere.
func TestChatList_ZoneAddSystem_PeelsIntoOneEntryNItems_NoCommitted(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddSystem("u1", notifFrameA+notifFrameB)

	if cl.Len() != 0 {
		t.Fatalf("committed Len = %d, want 0 (system frame must stay in the zone until consume)", cl.Len())
	}
	if cl.zone.len() != 1 {
		t.Fatalf("zone entries = %d, want 1 (one frame = one uuid entry)", cl.zone.len())
	}
	entry := cl.zone.byUUID["u1"]
	if entry == nil {
		t.Fatal("zone has no entry for uuid u1")
	}
	if entry.kind != pendingSystem {
		t.Errorf("entry kind = %v, want pendingSystem", entry.kind)
	}
	if len(entry.items) != 2 {
		t.Fatalf("peeled items = %d, want 2 (stacked envelopes split into distinct entries)", len(entry.items))
	}
	for i, env := range entry.items {
		if _, ok := env.item.(*SystemNotificationItem); !ok {
			t.Errorf("zone item %d is %T, want *SystemNotificationItem", i, env.item)
		}
	}
	out := stripAnsi(cl.Render(80))
	if strings.Contains(out, "<system-notification") {
		t.Errorf("raw tag leaked to render:\n%s", out)
	}
	if c := strings.Count(out, "alpha → working"); c != 1 {
		t.Errorf("first envelope body rendered %d times, want 1:\n%s", c, out)
	}
	if c := strings.Count(out, "beta heads up"); c != 1 {
		t.Errorf("second envelope body rendered %d times, want 1:\n%s", c, out)
	}
}

// T3 — consume relocates the zone entry's items into the committed transcript
// (peel order), zone empties, and each body still renders EXACTLY ONCE. This is
// the regression guard for the live drain double-render.
func TestChatList_ZoneSettle_RelocatesSystemItems_RenderedOnce(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddSystem("u1", notifFrameA+notifFrameB)

	if ok := cl.ZoneSettle("u1"); !ok {
		t.Fatal("ZoneSettle(u1) = false, want true (entry exists)")
	}
	if cl.zone.len() != 0 {
		t.Errorf("zone len = %d, want 0 after settle", cl.zone.len())
	}
	if cl.Len() != 2 {
		t.Fatalf("committed Len = %d, want 2 (both system items relocated)", cl.Len())
	}
	committed := cl.Items()
	first, ok := committed[0].(*SystemNotificationItem)
	if !ok {
		t.Fatalf("committed item 0 is %T, want *SystemNotificationItem", committed[0])
	}
	if first.notificationType != NotificationKindStatusChange {
		t.Errorf("committed item 0 type = %q, want status_change (peel order preserved)", first.notificationType)
	}
	second, ok := committed[1].(*SystemNotificationItem)
	if !ok {
		t.Fatalf("committed item 1 is %T, want *SystemNotificationItem", committed[1])
	}
	if second.notificationType != NotificationKindMessage {
		t.Errorf("committed item 1 type = %q, want message (peel order preserved)", second.notificationType)
	}
	if !second.interrupt {
		t.Errorf("committed item 1 should be flagged interrupt (peel preserves attrs)")
	}
	out := stripAnsi(cl.Render(80))
	if strings.Contains(out, "<system-notification") {
		t.Errorf("raw tag leaked after settle:\n%s", out)
	}
	if c := strings.Count(out, "alpha → working"); c != 1 {
		t.Errorf("body rendered %d times after settle, want 1:\n%s", c, out)
	}
}

// T3-cache — a zone mutation while the list is Idle must invalidate the render
// cache, or the zone change is invisible until an unrelated mutation. The single
// most likely implementation bug per the oracle plan.
func TestChatList_ZoneMutationWhileIdle_InvalidatesRenderCache(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("committed prompt")
	first := stripAnsi(cl.Render(80)) // populate cache; list is Idle
	if strings.Contains(first, "alpha → working") {
		t.Fatal("setup: zone body must not be present before ZoneAddSystem")
	}
	cl.ZoneAddSystem("u1", notifFrameA)
	second := stripAnsi(cl.Render(80))
	if !strings.Contains(second, "alpha → working") {
		t.Errorf("zone mutation while Idle did not invalidate the render cache; render:\n%s", second)
	}
}

// G-4 — a settle while Idle must also invalidate the render cache: after a
// system entry relocates from the zone into the committed transcript, a re-render
// must reflect the move (it would have rendered in the zone before, committed
// after — same body, but the cache must not serve a pre-settle string).
func TestChatList_ZoneSettleWhileIdle_InvalidatesRenderCache(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddUser("u1", "pending prompt")
	cl.Render(80) // populate cache with the zone-only render
	cl.ZoneSettle("u1")
	out := stripAnsi(cl.Render(80))
	if cl.Len() != 1 {
		t.Fatalf("committed Len = %d after settle, want 1", cl.Len())
	}
	if !strings.Contains(out, "pending prompt") {
		t.Errorf("settle while Idle did not invalidate cache; render:\n%s", out)
	}
}

// F1 — a frame classified as system by the prefix check but with no peelable
// envelope (malformed `<system-notification`-prefixed text) renders verbatim as
// a USER entry, matching replay's peelNotificationEntries — single-classifier
// convergence over the malformed boundary, no silent drop.
func TestChatList_ZoneAddSystem_Unpeelable_FallsBackToUser(t *testing.T) {
	const malformed = "<system-notificationX>not a real envelope"
	if !classifyInboundFrame(malformed) {
		t.Fatalf("setup: classifyInboundFrame(%q) = false, want true (prefix matches)", malformed)
	}

	// Live (zone) path.
	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddSystem("u1", malformed)
	entry := cl.zone.byUUID["u1"]
	if entry == nil || entry.kind != pendingUser {
		t.Fatalf("zone entry = %+v, want a pendingUser entry (fallback)", entry)
	}

	// Replay path: peelNotificationEntries emits the unpeelable body as a user
	// entry (ok=true, no system entry) — the two paths converge.
	got, ok := peelNotificationEntries(malformed)
	if !ok || len(got) != 1 || got[0].Type != MessageUser {
		t.Fatalf("peelNotificationEntries(%q) = (%+v, %v), want one MessageUser entry", malformed, got, ok)
	}
}

// T10 (zone level) — settle of an unknown uuid is a no-op (the restart-orphan
// guard, ghost's C9). Nothing committed, no panic.
func TestChatList_ZoneSettle_UnknownUUID_NoOp(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	if ok := cl.ZoneSettle("never-sent"); ok {
		t.Errorf("ZoneSettle(unknown) = true, want false")
	}
	if cl.Len() != 0 {
		t.Errorf("committed Len = %d, want 0 (untracked consume must not blind-append)", cl.Len())
	}
}

// LOCKED invariant 5 — a system entry is NEVER recall-droppable; only
// user-submitted uuids are. ZoneDrop must refuse to remove a system entry.
func TestChatList_ZoneDrop_SystemEntry_Refused(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddSystem("sys1", notifFrameA)
	if ok := cl.ZoneDrop("sys1"); ok {
		t.Errorf("ZoneDrop(system uuid) = true, want false (system notifications are not recallable)")
	}
	if cl.zone.len() != 1 {
		t.Errorf("zone len = %d, want 1 (system entry must survive a cancel)", cl.zone.len())
	}
}

// ZoneDrop of a user entry removes it and reports true; ZoneUserCount tracks
// only user entries.
func TestChatList_ZoneDrop_UserEntry_RemovesIt(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddUser("u1", "alpha")
	cl.ZoneAddSystem("sys1", notifFrameA)
	if cl.ZoneUserCount() != 1 {
		t.Fatalf("ZoneUserCount = %d, want 1 (system entries are not user-queued)", cl.ZoneUserCount())
	}
	if ok := cl.ZoneDrop("u1"); !ok {
		t.Errorf("ZoneDrop(user uuid) = false, want true")
	}
	if cl.ZoneUserCount() != 0 {
		t.Errorf("ZoneUserCount = %d, want 0 after dropping the user entry", cl.ZoneUserCount())
	}
}

// Reset must clear the zone, or a stale pending entry renders under a freshly
// replayed transcript on restart/resync (a new double-render surface).
func TestChatList_Reset_ClearsZone(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddSystem("u1", notifFrameA)
	cl.ZoneAddUser("u2", "pending prompt")
	cl.Reset(nil)
	if cl.zone.len() != 0 {
		t.Errorf("zone len = %d, want 0 after Reset (stale pending entries must not survive a backfill)", cl.zone.len())
	}
}

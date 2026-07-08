package tui

import (
	"strings"
	"testing"
)

// findCompactBanner returns the single CompactBannerItem in the root viewport,
// failing if there is not exactly one.
func findCompactBanner(t *testing.T, app AppModel) *CompactBannerItem {
	t.Helper()
	var found *CompactBannerItem
	n := 0
	for _, it := range app.rootVP().ChatList().Items() {
		if b, ok := it.(*CompactBannerItem); ok {
			found = b
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 CompactBannerItem, got %d", n)
	}
	return found
}

// TestUpdate_CompactBoundaryMsg_RendersManualBanner proves a manual compaction
// boundary renders a first-party banner with the pre→post token counts and the
// trigger (QUM-865).
func TestUpdate_CompactBoundaryMsg_RendersManualBanner(t *testing.T) {
	bridge := newFakeSessionBackend()
	bridge.SetContinuous(true)
	app := readyRoutingApp(t, bridge)

	updated, cmd := app.Update(CompactBoundaryMsg{Trigger: "manual", PreTokens: 236255, PostTokens: 8955})
	app = updated.(AppModel)

	banner := findCompactBanner(t, app)
	out := banner.Render(80)
	for _, want := range []string{"context compacted", "236k", "9k", "manual"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner render %q missing %q", out, want)
		}
	}
	// QUM-826: CompactBoundaryMsg is pump-delivered — its reducer must re-arm
	// WaitForEvent exactly once or the event pump parks. readyRoutingApp does
	// not pre-arm the pump, so waitCalls starts at 0.
	if cmd == nil {
		t.Fatal("CompactBoundaryMsg returned nil cmd; expected a WaitForEvent re-arm")
	}
	if bridge.waitCalls != 1 {
		t.Errorf("CompactBoundaryMsg waitCalls = %d, want 1 (re-arm pump)", bridge.waitCalls)
	}
}

// TestUpdate_CompactBoundaryMsg_RendersAutoBanner proves auto-compaction (no
// preceding user submission) also renders the banner (QUM-865).
func TestUpdate_CompactBoundaryMsg_RendersAutoBanner(t *testing.T) {
	app := readyRoutingApp(t, newFakeSessionBackend())
	updated, _ := app.Update(CompactBoundaryMsg{Trigger: "auto", PreTokens: 180000, PostTokens: 12000})
	app = updated.(AppModel)

	banner := findCompactBanner(t, app)
	out := banner.Render(80)
	for _, want := range []string{"context compacted", "180k", "12k", "auto"} {
		if !strings.Contains(out, want) {
			t.Errorf("auto banner render %q missing %q", out, want)
		}
	}
}

// countUserBubbles returns how many committed UserItem entries are in the root
// viewport.
func countUserBubbles(app AppModel) int {
	n := 0
	for _, it := range app.rootVP().ChatList().Items() {
		if _, ok := it.(*UserItem); ok {
			n++
		}
	}
	return n
}

// TestUpdate_CompactBoundaryMsg_KeepsQueuedFollowups proves a compact boundary
// does NOT drop the rest of the queue: a settled (committed) follow-up bubble
// survives AND a still-pending follow-up keeps its normal echo-settle lifecycle
// across the boundary (QUM-865). The boundary appends only the banner.
func TestUpdate_CompactBoundaryMsg_KeepsQueuedFollowups(t *testing.T) {
	bridge := newFakeSessionBackend()
	bridge.SetContinuous(true)
	app := readyRoutingApp(t, bridge)

	// One committed follow-up (sent + consumed) and one still-pending follow-up.
	updated, _ := app.Update(UserMessageSentMsg{UUID: "u-committed", Text: "committed follow-up"})
	app = updated.(AppModel)
	updated, _ = app.Update(UserMessageConsumedMsg{UUID: "u-committed"})
	app = updated.(AppModel)
	updated, _ = app.Update(UserMessageSentMsg{UUID: "u-pending", Text: "pending follow-up"})
	app = updated.(AppModel)
	if got := app.rootBuf().ZoneUserCount(); got != 1 {
		t.Fatalf("precondition: ZoneUserCount = %d, want 1 (pending follow-up)", got)
	}
	if got := countUserBubbles(app); got != 1 {
		t.Fatalf("precondition: committed user bubbles = %d, want 1", got)
	}

	updated, _ = app.Update(CompactBoundaryMsg{Trigger: "manual", PreTokens: 200000, PostTokens: 9000})
	app = updated.(AppModel)

	if got := app.rootBuf().ZoneUserCount(); got != 1 {
		t.Errorf("pending follow-up dropped by compact boundary: ZoneUserCount = %d, want 1", got)
	}
	if got := countUserBubbles(app); got != 1 {
		t.Errorf("committed follow-up bubble dropped by compact boundary: user bubbles = %d, want 1", got)
	}
}

// TestUpdate_PassthroughMsg_ForwardsVerbatimNoPending proves a PassthroughMsg
// forwards the full line to the backend via SendPassthrough and creates NO
// pending-zone entry (the phantom-queued fix, QUM-865).
func TestUpdate_PassthroughMsg_ForwardsVerbatimNoPending(t *testing.T) {
	bridge := newFakeSessionBackend()
	bridge.SetContinuous(true)
	app := readyRoutingApp(t, bridge)

	updated, _ := app.Update(PassthroughMsg{Text: "/compact focus on the code changes"})
	app = updated.(AppModel)

	if bridge.passthroughCalls != 1 {
		t.Errorf("passthroughCalls = %d, want 1", bridge.passthroughCalls)
	}
	if bridge.lastPassthrough != "/compact focus on the code changes" {
		t.Errorf("lastPassthrough = %q, want verbatim line", bridge.lastPassthrough)
	}
	if bridge.sendCalls != 0 {
		t.Errorf("passthrough must not use SendMessage; sendCalls = %d", bridge.sendCalls)
	}
	if got := app.rootBuf().ZoneUserCount(); got != 0 {
		t.Errorf("passthrough created a pending-zone entry (phantom); ZoneUserCount = %d, want 0", got)
	}
	if got := len(app.rootVP().ChatList().Items()); got != 0 {
		t.Errorf("passthrough committed a bubble; items = %d, want 0", got)
	}
}

// TestUpdate_UserMessageSentMsg_PassthroughNoPendingEntry proves the
// UserMessageSentMsg reducer skips the pending-zone entry when Passthrough is
// set — so a passthrough command that never echoes cannot stick as a phantom
// queued bubble (QUM-865).
func TestUpdate_UserMessageSentMsg_PassthroughNoPendingEntry(t *testing.T) {
	bridge := newFakeSessionBackend()
	bridge.SetContinuous(true)
	app := readyRoutingApp(t, bridge)

	updated, _ := app.Update(UserMessageSentMsg{UUID: "u-compact", Text: "/compact", Passthrough: true})
	app = updated.(AppModel)

	if got := app.rootBuf().ZoneUserCount(); got != 0 {
		t.Errorf("passthrough UserMessageSentMsg created pending entry; ZoneUserCount = %d, want 0", got)
	}
	if got := len(app.rootVP().ChatList().Items()); got != 0 {
		t.Errorf("passthrough UserMessageSentMsg committed a bubble; items = %d, want 0", got)
	}
}

package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/messages"
)

// QUM-465 — failing tests guarding the inbox-notifier double-fire bug.
//
// Symptom: every send_async to weave produces TWO `[inbox]` banners — one
// from InboxArrivalMsg (in-process notifier) and
// one from AgentTreeMsg (2s tickAgentsCmd rise-detector reading the same
// maildir entry). Race ordering decides which lands first; both fire banners.
//
// The fix will reconcile InboxArrivalMsg against disk-truth so both code
// paths converge to exactly-once. These tests assert that:
//   * an InboxArrivalMsg arriving AFTER an AgentTreeMsg already accounted for
//     the same maildir entry must NOT re-fire the banner;
//   * an AgentTreeMsg arriving AFTER an InboxArrivalMsg already accounted for
//     the same maildir entry must NOT re-fire the banner;
//   * a stray InboxArrivalMsg with no matching disk rise must be a no-op.
//
// Tests are red on current code; the implementer will green them.

// newTestAppModelWithSprawlRoot builds an AppModel rooted at a real on-disk
// sprawlRoot so the InboxArrivalMsg handler (post-fix) can reconcile its
// counter against `messages.List(sprawlRoot, "weave", "unread")`.
func newTestAppModelWithSprawlRoot(t *testing.T, sprawlRoot string) AppModel {
	t.Helper()
	sup := &mockSupervisor{}
	app := NewAppModel("colour212", "testrepo", "v0.1.0", "v0.1.0", nil, sup, sprawlRoot, nil)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return resized.(AppModel)
}

// seedUnreadForWeave drops `count` unread maildir entries into weave's queue
// at sprawlRoot. Uses the public messages.Send API with a no-op notifier so
// the seed has zero side effects beyond the maildir write.
func seedUnreadForWeave(t *testing.T, sprawlRoot string, count int) {
	t.Helper()
	noop := func(_, _, _, _ string) {}
	for i := 0; i < count; i++ {
		if _, err := messages.Send(
			sprawlRoot, "trace", "weave",
			"qum-465 test", "body",
			messages.WithNotify(noop),
		); err != nil {
			t.Fatalf("seed messages.Send: %v", err)
		}
	}
}

// countInboxBanners counts how many `inbox:` status lines are visible in the
// weave viewport. Uses the same string match strategy as the existing
// QUM-311 tests (see TestAppModel_AgentTreeMsg_NoBannerWhenUnreadUnchanged).
func countInboxBanners(app AppModel) int {
	view := stripAnsi(app.viewportFor("weave").View())
	return strings.Count(view, "inbox:")
}

// TestInboxArrivalMsg_DedupedAfterAgentTreeMsg — the AgentTreeMsg tick has
// already fired its banner and bumped rootUnread to 1 by reading disk. A
// subsequent InboxArrivalMsg for the SAME maildir entry must be a no-op:
// no new banner, rootUnread unchanged.
func TestInboxArrivalMsg_DedupedAfterAgentTreeMsg(t *testing.T) {
	sprawlRoot := t.TempDir()
	seedUnreadForWeave(t, sprawlRoot, 1)

	app := newTestAppModelWithSprawlRoot(t, sprawlRoot)

	// AgentTreeMsg fires first (the 2s tick reading disk).
	updated, _ := app.Update(AgentTreeMsg{RootUnread: 1})
	app = updated.(AppModel)
	if got := countInboxBanners(app); got != 1 {
		t.Fatalf("after AgentTreeMsg: banner count = %d, want 1", got)
	}
	if app.rootUnread != 1 {
		t.Fatalf("after AgentTreeMsg: rootUnread = %d, want 1", app.rootUnread)
	}

	// Now the in-process InboxArrivalMsg for the SAME maildir entry arrives.
	// The fix: reconcile against disk; disk says 1 unread, model already at
	// 1, so this is a no-op.
	updated, _ = app.Update(InboxArrivalMsg{From: "trace", Subject: "qum-465 test"})
	app = updated.(AppModel)

	if got := countInboxBanners(app); got != 1 {
		t.Errorf("after InboxArrivalMsg: banner count = %d, want 1 (double-fire bug)", got)
	}
	if app.rootUnread != 1 {
		t.Errorf("after InboxArrivalMsg: rootUnread = %d, want 1 (counter double-bumped)", app.rootUnread)
	}
}

// TestInboxArrivalMsg_FiresWhenTickHasNotRunYet — happy path. The maildir has
// 1 unread, the tick has NOT yet observed it (model still at 0). The
// InboxArrivalMsg should fire exactly one banner and bump rootUnread to 1.
func TestInboxArrivalMsg_FiresWhenTickHasNotRunYet(t *testing.T) {
	sprawlRoot := t.TempDir()
	seedUnreadForWeave(t, sprawlRoot, 1)

	app := newTestAppModelWithSprawlRoot(t, sprawlRoot)

	updated, _ := app.Update(InboxArrivalMsg{From: "trace", Subject: "qum-465 test"})
	app = updated.(AppModel)

	if got := countInboxBanners(app); got != 1 {
		t.Errorf("banner count = %d, want 1", got)
	}
	if app.rootUnread != 1 {
		t.Errorf("rootUnread = %d, want 1", app.rootUnread)
	}
}

// TestAgentTreeMsg_AfterInboxArrival_NoSecondBanner — reverse race ordering.
// The in-process InboxArrivalMsg lands first, fires its banner, bumps to 1.
// Then the 2s tickAgentsCmd lands with RootUnread=1 (disk-truth). The
// AgentTreeMsg handler already correctly suppresses (msg.RootUnread > rootUnread
// is false), but we lock that in for the QUM-465 regression suite.
func TestAgentTreeMsg_AfterInboxArrival_NoSecondBanner(t *testing.T) {
	sprawlRoot := t.TempDir()
	seedUnreadForWeave(t, sprawlRoot, 1)

	app := newTestAppModelWithSprawlRoot(t, sprawlRoot)

	// In-process notifier fires first.
	updated, _ := app.Update(InboxArrivalMsg{From: "trace", Subject: "qum-465 test"})
	app = updated.(AppModel)
	if got := countInboxBanners(app); got != 1 {
		t.Fatalf("after InboxArrivalMsg: banner count = %d, want 1", got)
	}
	if app.rootUnread != 1 {
		t.Fatalf("after InboxArrivalMsg: rootUnread = %d, want 1", app.rootUnread)
	}

	// Then the 2s tick lands with the same disk-truth count.
	updated, _ = app.Update(AgentTreeMsg{RootUnread: 1})
	app = updated.(AppModel)

	if got := countInboxBanners(app); got != 1 {
		t.Errorf("after AgentTreeMsg: banner count = %d, want 1 (double-fire)", got)
	}
	if app.rootUnread != 1 {
		t.Errorf("after AgentTreeMsg: rootUnread = %d, want 1", app.rootUnread)
	}
}

// TestInboxArrivalMsg_StaleNoOp_WhenDiskUnreadDropped — defensive. If a stray
// InboxArrivalMsg arrives but the maildir has no unread entries (e.g. the
// message was already drained/marked-read), the handler must NOT fire a
// banner or bump the counter. This is the disk-truth reconciliation guard.
func TestInboxArrivalMsg_StaleNoOp_WhenDiskUnreadDropped(t *testing.T) {
	sprawlRoot := t.TempDir()
	// Maildir is empty; no unread messages on disk.

	app := newTestAppModelWithSprawlRoot(t, sprawlRoot)

	updated, _ := app.Update(InboxArrivalMsg{From: "trace", Subject: "stale"})
	app = updated.(AppModel)

	if got := countInboxBanners(app); got != 0 {
		t.Errorf("stale InboxArrivalMsg fired banner: count = %d, want 0", got)
	}
	if app.rootUnread != 0 {
		t.Errorf("stale InboxArrivalMsg bumped counter: rootUnread = %d, want 0", app.rootUnread)
	}
}

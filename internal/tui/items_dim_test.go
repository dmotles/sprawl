package tui

import "testing"

// QUM-832 — a pending (in-zone) user prompt renders DIM to signal "queued / not
// yet echoed"; it brightens to normal styling when it settles into the committed
// transcript on its consume ack. These tests pin the styling state flip at the
// UserItem layer (pending flag) and the ChatList zone→settle integration
// (including the render-cache staleness guard the oracle flagged).

// A UserItem defaults to bright (committed) styling; SetPending(true) renders it
// dim. The two differ only in styling, never in text/layout.
func TestUserItem_PendingRendersDimNotBright(t *testing.T) {
	ctx := newTestCtx()

	bright := NewUserItem(ctx, "hello world")
	if bright.pending {
		t.Fatal("NewUserItem must default to pending=false (committed bubbles render bright)")
	}
	brightOut := bright.Render(80)

	dim := NewUserItem(ctx, "hello world")
	dim.SetPending(true)
	dimOut := dim.Render(80)

	if dimOut == brightOut {
		t.Errorf("pending render must differ from bright render (dim styling); both were:\n%q", brightOut)
	}
	if stripAnsi(dimOut) != stripAnsi(brightOut) {
		t.Errorf("pending vs bright must differ ONLY in styling, not text:\ndim=%q\nbright=%q",
			stripAnsi(dimOut), stripAnsi(brightOut))
	}
}

// A zone (pending) user bubble renders dim, distinct from a committed bright
// bubble; after ZoneSettle the relocated bubble brightens to the exact bright
// rendering — proving (a) the styling flips and (b) the per-envelope render
// cache does not serve a stale dim string after settle.
func TestChatList_ZoneUserBubble_DimThenBrightOnSettle(t *testing.T) {
	// Bright reference: a committed AppendUser bubble at the same width.
	ref := newTestChatList()
	ref.SetSize(80)
	ref.AppendUser("pending prompt")
	bright := ref.Render(80)

	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddUser("u1", "pending prompt")
	dim := cl.Render(80)

	if dim == bright {
		t.Errorf("zone (pending) user bubble must render DIM, distinct from a committed bright bubble")
	}
	if stripAnsi(dim) != stripAnsi(bright) {
		t.Errorf("dim vs bright must differ ONLY in styling, not text:\ndim=%q\nbright=%q",
			stripAnsi(dim), stripAnsi(bright))
	}

	cl.ZoneSettle("u1")
	settled := cl.Render(80)
	if settled != bright {
		t.Errorf("settled bubble must brighten to normal styling (no stale dim cache):\nsettled=%q\nbright=%q",
			settled, bright)
	}
}

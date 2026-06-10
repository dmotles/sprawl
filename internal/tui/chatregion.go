package tui

// QUM-676 — ChatRegion wraps a bubbles viewport.Model around a ChatList so
// scroll machinery (PgUp/PgDn, auto-scroll, "↓ New content below" indicator,
// QUM-602 SoftWrap/xOffset guard) lives in one composable place. Replaces the
// scroll-side responsibilities of the legacy ViewportModel after S6 deletes
// its rendering surface.
//
// Contract notes:
//   - SoftWrap is forced on at construction (QUM-602). XOffset is clamped to
//     zero by the bubbles viewport when SoftWrap is true; ChatRegion does not
//     expose any path that bypasses that guard.
//   - SetSize is the only mutator of size. Width is propagated to the inner
//     ChatList so per-item Render width matches the visible column count.
//   - View() re-paints the inner bubbles viewport from cl.Render(width) on
//     every call. Cheap because ChatList caches finished items per
//     (width, expanded). When auto-scroll is on, View() also snaps to bottom;
//     when off, View() flips hasNewContent on if cl grew since the last paint.

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
)

// ChatRegion bundles a ChatList with the scroll machinery needed to display
// it inside an Outer panel. AgentBuffer owns one per agent.
type ChatRegion struct {
	vp            viewport.Model
	cl            *ChatList
	autoScroll    bool
	hasNewContent bool
	// lastClLen / lastWidth let View() detect "content appeared while
	// scrolled up" so the new-content indicator flips on. Width is tracked
	// because resizing also forces a repaint and a re-evaluation of the
	// at-bottom invariant.
	lastClLen int
	lastWidth int

	// QUM-769 View() output cache. Hits when the inner ChatList revision,
	// viewport geometry, scroll offset, and indicator flags are unchanged
	// since the last paint — the steady-state typing case. disableViewCache
	// is a same-package test toggle for the byte-equivalence oracle.
	viewCache        *chatRegionViewCache
	viewBuilds       int
	disableViewCache bool
}

// chatRegionViewCache stores the post-vp.View() string keyed on every input
// that can change the rendered bytes. revision is the ChatList's monotonic
// observable-state counter (QUM-769); yOffset captures user scroll position;
// autoScroll + hasNewContent capture the indicator-splice flags.
type chatRegionViewCache struct {
	revision      uint64
	width, height int
	yOffset       int
	autoScroll    bool
	hasNewContent bool
	out           string
}

// NewChatRegion constructs a fresh ChatRegion bound to the given theme. The
// inner ChatList starts width-0 (a no-op until SetSize) and SoftWrap is
// forced on per QUM-602.
func NewChatRegion(theme *Theme) *ChatRegion {
	vp := viewport.New()
	vp.SoftWrap = true
	return &ChatRegion{
		vp:         vp,
		cl:         NewChatList(theme),
		autoScroll: true,
	}
}

// ChatList returns the inner ChatList so callers can drive Append* / Reset /
// MarkToolResult / SetToolInputsExpanded without ChatRegion-level forwarders.
func (r *ChatRegion) ChatList() *ChatList { return r.cl }

// SetSize updates both the inner viewport dimensions and the ChatList content
// width. h is in rows; w in cells.
func (r *ChatRegion) SetSize(w, h int) {
	r.vp.SetWidth(w)
	r.vp.SetHeight(h)
	r.cl.SetSize(w)
	r.lastWidth = w
}

// SoftWrap reports whether the inner viewport is in soft-wrap mode (QUM-602).
// Exposed so tests can pin the invariant.
func (r *ChatRegion) SoftWrap() bool { return r.vp.SoftWrap }

// XOffset returns the inner viewport's horizontal offset. With SoftWrap on it
// is always zero — exposed for the QUM-602 regression test.
func (r *ChatRegion) XOffset() int { return r.vp.XOffset() }

// SetXOffset attempts to bump the inner viewport's horizontal offset. With
// SoftWrap on the bubbles viewport clamps the value to zero; this method is
// retained so the QUM-602 regression test can exercise the clamp.
func (r *ChatRegion) SetXOffset(n int) { r.vp.SetXOffset(n) }

// AtBottom reports whether the inner viewport is scrolled to the bottom.
func (r *ChatRegion) AtBottom() bool { return r.vp.AtBottom() }

// IsAutoScroll reports whether new content snaps to bottom on append.
func (r *ChatRegion) IsAutoScroll() bool { return r.autoScroll }

// HasNewContent reports whether new content arrived while the user was
// scrolled up. View() draws the "↓ New content below ↓" indicator on the
// last visible row when this is true.
func (r *ChatRegion) HasNewContent() bool { return r.hasNewContent }

// GotoBottom snaps the inner viewport to the bottom and re-engages
// auto-scroll. Clears HasNewContent.
func (r *ChatRegion) GotoBottom() {
	r.vp.GotoBottom()
	r.autoScroll = true
	r.hasNewContent = false
}

// PageUp scrolls up one page and disables auto-scroll: the user has
// explicitly scrolled away from the live feed.
func (r *ChatRegion) PageUp() {
	r.vp.PageUp()
	r.autoScroll = false
}

// PageDown scrolls down one page. If the page lands at the bottom, auto-scroll
// re-engages and the new-content indicator clears.
func (r *ChatRegion) PageDown() {
	r.vp.PageDown()
	if r.vp.AtBottom() {
		r.autoScroll = true
		r.hasNewContent = false
	}
}

// View repaints the inner bubbles viewport from cl.Render(width) and returns
// the result. Width-0 short-circuits to an empty string (matches the legacy
// ViewportModel.View() guard). When auto-scroll is engaged, the view snaps to
// the bottom so the latest content remains visible.
func (r *ChatRegion) View() string {
	w := r.cl.Width()
	if w <= 0 {
		return ""
	}
	// QUM-769 fast-path: when nothing observable to View() output has changed
	// since the last paint, return the cached string. Skips ChatList.Render
	// (already cached) + vp.SetContent (re-soft-wraps the full content) +
	// vp.View() (emits the visible window) — the ~220ms per-keystroke cost.
	rev := r.cl.Revision()
	h := r.vp.Height()
	if !r.disableViewCache && r.viewCache != nil &&
		r.viewCache.revision == rev &&
		r.viewCache.width == w &&
		r.viewCache.height == h &&
		r.viewCache.yOffset == r.vp.YOffset() &&
		r.viewCache.autoScroll == r.autoScroll &&
		r.viewCache.hasNewContent == r.hasNewContent {
		return r.viewCache.out
	}

	r.viewBuilds++
	// Re-render the ChatList content into the inner viewport. ChatList caches
	// finished items per (width, expanded) so the per-call cost is bounded by
	// the count of in-flight (streaming / pending-tool) items.
	rendered := r.cl.Render(w)
	r.vp.SetContent(rendered)

	// Flip hasNewContent if content grew while not at-bottom (i.e. the user
	// scrolled up and new items appeared since the prior View() call).
	curLen := r.cl.Len()
	if curLen > r.lastClLen && !r.autoScroll {
		r.hasNewContent = true
	}
	r.lastClLen = curLen

	if r.autoScroll {
		r.vp.GotoBottom()
	}

	view := r.vp.View()
	if r.hasNewContent && !r.autoScroll {
		indicator := r.cl.ctx.theme.AccentText.Render("  " + NewContentIndicator + "  ")
		lines := strings.Split(view, "\n")
		if len(lines) > 0 {
			lines[len(lines)-1] = indicator
		}
		view = strings.Join(lines, "\n")
	}
	if !r.disableViewCache {
		r.viewCache = &chatRegionViewCache{
			revision:      rev,
			width:         w,
			height:        h,
			yOffset:       r.vp.YOffset(),
			autoScroll:    r.autoScroll,
			hasNewContent: r.hasNewContent,
			out:           view,
		}
	}
	return view
}

package tui

// QUM-676 S6 — RED-phase tests for the ChatRegion scroll wrapper.
//
// Design choice (test-writer): the new wrapper is named `ChatRegion`. It
// hoists the bubbles viewport.Model (SetSize, View, PageUp/PageDown, AtBottom,
// auto-scroll, "↓ New content below" indicator, SoftWrap/xOffset guard) out
// of ViewportModel into a thin reusable wrapper around a ChatList. If the
// implementer prefers to fold the scroll machinery into AgentBuffer directly,
// rename ChatRegion -> AgentBuffer (and adjust accessors); the assertions
// below are about behavior, not name.
//
// Expected surface:
//
//	type ChatRegion struct { /* unexported: viewport.Model + *ChatList */ }
//
//	func NewChatRegion(theme *Theme) *ChatRegion
//	func (r *ChatRegion) ChatList() *ChatList
//	func (r *ChatRegion) SetSize(w, h int)
//	func (r *ChatRegion) View() string
//	func (r *ChatRegion) PageUp()
//	func (r *ChatRegion) PageDown()
//	func (r *ChatRegion) AtBottom() bool
//	func (r *ChatRegion) GotoBottom()
//	func (r *ChatRegion) HasNewContent() bool   // drives the indicator
//	func (r *ChatRegion) IsAutoScroll() bool

import (
	"strings"
	"testing"
)

func newTestChatRegion(t *testing.T) *ChatRegion {
	t.Helper()
	theme := NewTheme("")
	return NewChatRegion(&theme)
}

// TestChatRegion_View_EmptyBeforeSetSize asserts the width-0 guard inherited
// from ChatList — View() must produce empty output until SetSize is called.
func TestChatRegion_View_EmptyBeforeSetSize(t *testing.T) {
	r := newTestChatRegion(t)
	if got := r.View(); strings.TrimSpace(got) != "" {
		t.Errorf("View() before SetSize should be empty; got %q", got)
	}
}

// TestChatRegion_SoftWrap_XOffsetClampedToZero pins the QUM-602 guard:
// SoftWrap must be enabled on the inner bubbles viewport so a stray
// l/right keypress can never bump xOffset and mangle line rendering. This
// reproduces the assertion from viewport_test.go's
// TestViewportModel_SoftWrapPreventsHorizontalScroll against the new wrapper.
func TestChatRegion_SoftWrap_XOffsetClampedToZero(t *testing.T) {
	r := newTestChatRegion(t)
	r.SetSize(60, 20)
	if !r.SoftWrap() {
		t.Fatal("ChatRegion must enable SoftWrap on its inner viewport — see QUM-602")
	}
	r.SetXOffset(30)
	if got := r.XOffset(); got != 0 {
		t.Errorf("xOffset = %d, want 0 (SoftWrap must force xOffset to stay 0)", got)
	}
}

// TestChatRegion_AutoScroll_StaysAtBottomOnAppend asserts the autoscroll
// invariant: when the user is at the bottom and a new item appends, the view
// stays at the bottom.
func TestChatRegion_AutoScroll_StaysAtBottomOnAppend(t *testing.T) {
	r := newTestChatRegion(t)
	r.SetSize(60, 10)
	cl := r.ChatList()
	for i := 0; i < 50; i++ {
		cl.AppendUser("line filler content number to make region overflow")
	}
	r.GotoBottom()
	if !r.AtBottom() {
		t.Fatalf("precondition: GotoBottom should land at bottom")
	}
	cl.AppendUser("the very latest line")
	// Re-render once. Implementation detail: View() must trigger a re-paint.
	_ = r.View()
	if !r.AtBottom() {
		t.Errorf("AutoScroll: after appending while at-bottom, region should remain at bottom")
	}
}

// TestChatRegion_PageUp_DisablesAutoScroll asserts PgUp drops auto-scroll —
// the user explicitly scrolled away from the live feed.
func TestChatRegion_PageUp_DisablesAutoScroll(t *testing.T) {
	r := newTestChatRegion(t)
	r.SetSize(60, 10)
	cl := r.ChatList()
	for i := 0; i < 50; i++ {
		cl.AppendUser("line filler content number to make region overflow")
	}
	_ = r.View()
	r.PageUp()
	_ = r.View()
	if r.IsAutoScroll() {
		t.Errorf("PageUp should disable auto-scroll")
	}
	if r.AtBottom() {
		t.Errorf("PageUp should leave the region not-at-bottom after scrolling up")
	}
}

// TestChatRegion_HasNewContent_AfterAppendWhileScrolledUp asserts the
// "↓ New content below" indicator state: when the user is scrolled up and new
// content arrives, HasNewContent() flips to true.
func TestChatRegion_HasNewContent_AfterAppendWhileScrolledUp(t *testing.T) {
	r := newTestChatRegion(t)
	r.SetSize(60, 10)
	cl := r.ChatList()
	for i := 0; i < 50; i++ {
		cl.AppendUser("filler line that's long enough to wrap a bit on width 60")
	}
	_ = r.View()
	r.PageUp()
	_ = r.View()
	// Now append while scrolled up.
	cl.AppendUser("brand new content")
	_ = r.View()
	if !r.HasNewContent() {
		t.Errorf("HasNewContent() should be true after appending while scrolled up")
	}
	// The view must include the new-content indicator string. Strip lipgloss
	// styling before substring-matching so the assertion survives any future
	// theme tweaks to the indicator chip.
	view := stripAnsi(r.View())
	if !strings.Contains(view, NewContentIndicator) {
		t.Errorf("View() should include the new-content indicator %q when scrolled up with new content;\ngot:\n%s",
			NewContentIndicator, view)
	}
}

// TestChatRegion_PageDown_ToBottom_ReenablesAutoScroll_ClearsHasNewContent
// pins the bottom-half of the new-content indicator contract: when the user
// scrolls back down to the bottom (via PageDown), auto-scroll re-engages and
// HasNewContent() flips back to false. Most likely silent-regression site:
// PageDown could land at-bottom without clearing the indicator flag, leaving
// the chevron stuck on screen.
func TestChatRegion_PageDown_ToBottom_ReenablesAutoScroll_ClearsHasNewContent(t *testing.T) {
	r := newTestChatRegion(t)
	r.SetSize(60, 10)
	cl := r.ChatList()
	for i := 0; i < 50; i++ {
		cl.AppendUser("filler line that's long enough to wrap a bit on width 60")
	}
	_ = r.View()
	r.PageUp()
	_ = r.View()
	if r.IsAutoScroll() {
		t.Fatalf("precondition: PageUp should disable auto-scroll")
	}
	// Append while scrolled up so HasNewContent flips to true.
	cl.AppendUser("brand new content")
	_ = r.View()
	if !r.HasNewContent() {
		t.Fatalf("precondition: HasNewContent should be true after append while scrolled up")
	}
	// Now PageDown until we hit the bottom. Bounded loop so a bug doesn't
	// hang the test forever.
	for i := 0; i < 100 && !r.AtBottom(); i++ {
		r.PageDown()
		_ = r.View()
	}
	if !r.AtBottom() {
		t.Fatalf("PageDown loop did not reach bottom after 100 iterations")
	}
	if !r.IsAutoScroll() {
		t.Errorf("PageDown-to-bottom should re-enable auto-scroll")
	}
	if r.HasNewContent() {
		t.Errorf("PageDown-to-bottom should clear HasNewContent (indicator should disappear)")
	}
}

// TestChatRegion_ResetPreservesScrollWhenNotAtBottom: design decision — when
// ChatList is Reset (e.g. resync, transcript backfill), if the user was NOT
// at the bottom the scroll position is preserved (or, at minimum, the user
// is not yanked to bottom against their will). When at-bottom, Reset should
// stick to bottom so live tail continues to behave.
//
// This test codifies BOTH cases. The implementer may choose snap-to-bottom
// or preserve-offset on Reset-from-not-bottom; this test pins the
// at-bottom-stays-at-bottom direction unconditionally and lets the other be
// observed (no negative assertion on the offset).
func TestChatRegion_Reset_AtBottom_StaysAtBottom(t *testing.T) {
	r := newTestChatRegion(t)
	r.SetSize(60, 10)
	cl := r.ChatList()
	for i := 0; i < 20; i++ {
		cl.AppendUser("filler line")
	}
	r.GotoBottom()
	_ = r.View()
	if !r.AtBottom() {
		t.Fatalf("precondition: at bottom before Reset")
	}
	// Seed Reset with enough entries to overflow the 10-row viewport — a
	// 2-entry Reset would leave AtBottom trivially true regardless of scroll
	// machinery (content shorter than viewport height always reads at-bottom).
	rebuilt := make([]MessageEntry, 0, 40)
	for i := 0; i < 40; i++ {
		rebuilt = append(rebuilt, MessageEntry{
			Type:     MessageUser,
			Content:  "rebuilt content line long enough to potentially wrap on width 60",
			Complete: true,
		})
	}
	cl.Reset(rebuilt)
	_ = r.View()
	if !r.AtBottom() {
		t.Errorf("After Reset while at-bottom (with overflow content), region should remain at bottom (live-tail invariant)")
	}
}

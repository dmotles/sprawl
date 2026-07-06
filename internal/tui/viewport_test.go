package tui

import (
	"strings"
	"testing"
)

// placeholderMarker is the last line of placeholderContent — its presence in a
// rendered viewport means the empty-state placeholder is showing.
const placeholderMarker = "Waiting for agent activity"

func newTestViewport(t *testing.T) ViewportModel {
	t.Helper()
	theme := NewTheme("")
	vp := NewViewportModel(&theme)
	vp.SetSize(80, 24)
	return vp
}

// QUM-854: a truly-empty viewport (no committed items, no pending zone) still
// shows the placeholder.
func TestViewportModel_View_EmptyShowsPlaceholder(t *testing.T) {
	vp := newTestViewport(t)
	view := stripAnsi(vp.View())
	if !strings.Contains(view, placeholderMarker) {
		t.Errorf("empty viewport did not render placeholder; got %q", view)
	}
}

// QUM-854: with Len()==0 but a pending USER zone entry, the viewport must render
// the zone content, not the placeholder. This is the fresh-session bug.
func TestViewportModel_View_RendersPendingUserZone_NotPlaceholder(t *testing.T) {
	vp := newTestViewport(t)
	vp.ChatList().ZoneAddUser("u1", "pending hello")
	view := stripAnsi(vp.View())
	if strings.Contains(view, placeholderMarker) {
		t.Errorf("viewport showed placeholder with a pending user zone entry; got %q", view)
	}
	if !strings.Contains(view, "pending hello") {
		t.Errorf("viewport did not render pending user zone content; got %q", view)
	}
}

// QUM-854: a first-frame drained system notification (Len()==0, pending SYSTEM
// zone entry) must also render, not the placeholder.
func TestViewportModel_View_RendersPendingSystemZone_NotPlaceholder(t *testing.T) {
	vp := newTestViewport(t)
	vp.ChatList().ZoneAddSystem("n1", notifFrameA)
	view := stripAnsi(vp.View())
	if strings.Contains(view, placeholderMarker) {
		t.Errorf("viewport showed placeholder with a pending system zone entry; got %q", view)
	}
	if !strings.Contains(view, "alpha → working") {
		t.Errorf("viewport did not render pending system zone content; got %q", view)
	}
}

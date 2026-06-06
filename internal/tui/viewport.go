package tui

// QUM-693 — ViewportModel is now a minimal wrapper around ChatRegion/ChatList.
//
// Pre-S6 this file owned a renderMessages walk, a per-MessageEntry render
// cache (QUM-667), an in-band selection state, and the SetContentExternal
// dual-shim used during the S3 incremental rewrite. S6 deleted all of that;
// QUM-693 finished the cleanup by deleting the Append* / GetMessages /
// SetMessages / HasPending* / Len / SetSpinnerFrame / MarkToolResult back-
// compat facade. All callers (production + tests) now use vp.ChatList()
// directly. What survives here is the small set of methods that own
// scroll-side behaviour (size, auto-scroll, Update routing) and the
// placeholder render for an empty viewport.

import (
	tea "charm.land/bubbletea/v2"
)

// placeholderContent is shown on a fresh ViewportModel until the first
// real content arrives. Retained verbatim for visual parity.
const placeholderContent = `Welcome to Sprawl TUI

This is the output viewport. Agent output will appear here.

Use PgUp/PgDn to scroll through content.
Press Ctrl+C to quit.

---

Waiting for agent activity...`

// ViewportModel is the per-agent chat-region wrapper. It composes a
// ChatRegion (scroll machinery + ChatList rendering) and owns scroll
// state plus the empty-state placeholder render.
type ViewportModel struct {
	region             *ChatRegion
	theme              *Theme
	width              int
	height             int
	toolInputsExpanded bool
}

// NewViewportModel constructs a ViewportModel with an empty ChatList.
// SoftWrap is forced on inside the ChatRegion per QUM-602.
func NewViewportModel(theme *Theme) ViewportModel {
	return ViewportModel{
		region: NewChatRegion(theme),
		theme:  theme,
	}
}

// ChatList exposes the underlying ChatList for callers (AgentBuffer,
// selection-mode yank, View() composition, ToolInputsExpanded fan-out).
// Returns nil only if the wrapper has been zero-valued (never observed in
// practice).
func (m *ViewportModel) ChatList() *ChatList {
	if m.region == nil {
		return nil
	}
	return m.region.ChatList()
}

// Region exposes the inner ChatRegion for callers (AgentBuffer) that need to
// drive scroll-side knobs (PageUp/PageDown/GotoBottom/HasNewContent/IsAutoScroll).
func (m *ViewportModel) Region() *ChatRegion { return m.region }

// Update routes scroll-related events through the inner ChatRegion's
// bubbles viewport so PgUp/PgDn/mouse-wheel still work end-to-end. Mirrors
// the legacy bubbles viewport's "drop auto-scroll when the user explicitly
// scrolls away" behaviour.
func (m ViewportModel) Update(msg tea.Msg) (ViewportModel, tea.Cmd) {
	if m.region == nil {
		return m, nil
	}
	// Prime the inner bubbles viewport with the current ChatList content so
	// AtBottom() reflects actual scroll position (otherwise scroll/PgUp/PgDn
	// would operate on stale content). Cheap because ChatList caches per-Item
	// renders; the inner viewport.SetContent is the only hot path.
	if w := m.region.cl.Width(); w > 0 {
		m.region.vp.SetContent(m.region.cl.Render(w))
		if m.region.autoScroll {
			m.region.vp.GotoBottom()
		}
	}
	wasAtBottom := m.region.vp.AtBottom()
	var cmd tea.Cmd
	m.region.vp, cmd = m.region.vp.Update(msg)
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseWheelMsg:
		if m.region.vp.AtBottom() {
			m.region.autoScroll = true
			m.region.hasNewContent = false
		} else if wasAtBottom {
			m.region.autoScroll = false
		}
	}
	return m, cmd
}

// View renders the inner ChatRegion. When the ChatList is empty the
// placeholder text is returned instead.
func (m ViewportModel) View() string {
	if m.region == nil {
		return ""
	}
	if m.region.cl == nil || m.region.cl.Len() == 0 {
		return m.theme.NormalText.Render(placeholderContent)
	}
	return m.region.View()
}

// Width returns the inner cell width last applied via SetSize. Zero means
// not-yet-sized.
func (m *ViewportModel) Width() int { return m.width }

// Height returns the viewport row count last applied via SetSize.
func (m *ViewportModel) Height() int { return m.height }

// SetSize forwards to the inner ChatRegion (which sizes both the bubbles
// viewport and the inner ChatList).
func (m *ViewportModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	if m.region != nil {
		m.region.SetSize(w, h)
	}
}

// IsAutoScroll reports the inner ChatRegion's auto-scroll state.
func (m *ViewportModel) IsAutoScroll() bool {
	if m.region == nil {
		return true
	}
	return m.region.IsAutoScroll()
}

// SetAutoScroll forces auto-scroll on/off on the inner ChatRegion.
func (m *ViewportModel) SetAutoScroll(v bool) {
	if m.region == nil {
		return
	}
	if v {
		m.region.GotoBottom()
	} else {
		m.region.autoScroll = false
	}
}

// SetToolInputsExpanded propagates the global expand-tool-inputs flag to the
// inner ChatList.
func (m *ViewportModel) SetToolInputsExpanded(expanded bool) {
	m.toolInputsExpanded = expanded
	if cl := m.ChatList(); cl != nil {
		cl.SetToolInputsExpanded(expanded)
	}
}

// ToolInputsExpanded reports the current expand state.
func (m *ViewportModel) ToolInputsExpanded() bool { return m.toolInputsExpanded }

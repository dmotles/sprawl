package tui

// QUM-676 — ViewportModel is now a thin facade over ChatRegion/ChatList.
//
// Pre-S6 this file owned a renderMessages walk, a per-MessageEntry render
// cache (QUM-667), an in-band selection state, and the SetContentExternal
// dual-shim used during the S3 incremental rewrite. S6 deleted all of that:
// rendering is owned by ChatList (per-Item caches) wrapped in ChatRegion
// (scroll/SoftWrap machinery). What survives here is a compatibility shim
// that keeps the legacy `ViewportModel` type — and the assorted Append* /
// GetMessages / SetMessages helpers a large body of unit tests still
// exercises — wired to the new rendering pipeline.
//
// The MessageEntry log retained on this struct is observable-only: it's a
// running record of what the AppModel pushed into the viewport, used by
// tests to assert "did message X land". It does NOT drive any visible
// rendering — that's ChatList's job. Status / Banner / Error / System
// entries (S5 contract violators) are kept in the log so legacy tests'
// negative assertions still see an empty slot, but they no longer drive any
// UI surface. Status-bar transient text is the canonical surface for those
// signals per chatlist-invariants.md §10.
//
// Surviving helpers (MessageEntry, MessageType constants, formatSystemMessage,
// notificationGlyphAndStyle, previewResultLines, wrapToolInput, etc.) live in
// messages.go to avoid coupling them to this facade.

import (
	"strings"

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

// ViewportModel is the per-agent chat-region facade. It composes a
// ChatRegion (scroll machinery + ChatList rendering) with a small
// MessageEntry log used by tests/back-compat callers to inspect what was
// appended.
type ViewportModel struct {
	region             *ChatRegion
	theme              *Theme
	messages           []MessageEntry
	width              int
	height             int
	toolInputsExpanded bool
	// placeholderShown is true between construction and the first real
	// content append. View() short-circuits to the placeholder text while it
	// holds.
	placeholderShown bool
}

// NewViewportModel constructs a ViewportModel with the placeholder content
// installed. SoftWrap is forced on inside the ChatRegion per QUM-602.
func NewViewportModel(theme *Theme) ViewportModel {
	return ViewportModel{
		region:           NewChatRegion(theme),
		theme:            theme,
		placeholderShown: true,
	}
}

// ChatList exposes the underlying ChatList so AgentBuffer can hand it to
// callers (selection-mode yank, View() composition, ToolInputsExpanded fan-
// out). Returns nil only if the facade has been zero-valued (never observed
// in practice).
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

// View renders the inner ChatRegion. While the placeholder is showing and
// no real content has landed, the placeholder text is returned instead.
func (m ViewportModel) View() string {
	if m.placeholderShown && len(m.messages) == 0 {
		return m.theme.NormalText.Render(placeholderContent)
	}
	if m.region == nil {
		return ""
	}
	return m.region.View()
}

// Width returns the inner cell width last applied via SetSize. Zero means
// not-yet-sized.
func (m *ViewportModel) Width() int { return m.width }

// Height returns the viewport row count last applied via SetSize.
func (m *ViewportModel) Height() int { return m.height }

// Len returns the number of message entries currently in the log.
func (m *ViewportModel) Len() int { return len(m.messages) }

// SetSize forwards to the inner ChatRegion (which sizes both the bubbles
// viewport and the inner ChatList).
func (m *ViewportModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	if m.region != nil {
		m.region.SetSize(w, h)
	}
}

// GetMessages returns a copy of the current message log. Used by tests to
// assert which entries landed; not in the render hot path.
func (m *ViewportModel) GetMessages() []MessageEntry {
	out := make([]MessageEntry, len(m.messages))
	copy(out, m.messages)
	return out
}

// SetMessages replaces both the log and the ChatList contents from a
// transcript-backfill snapshot (ChildTranscriptMsg / PreloadTranscript /
// resync). ChatList.Reset force-finalizes trailing assistants and clears
// pendingTools — the wedge-exit invariant.
func (m *ViewportModel) SetMessages(msgs []MessageEntry) {
	m.messages = append(m.messages[:0:0], msgs...)
	if cl := m.ChatList(); cl != nil {
		cl.Reset(msgs)
	}
	m.placeholderShown = false
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

// AppendBanner records a session banner in the log. Post-S6 banners are
// surfaced via status-bar transient text — the log retention keeps tests'
// "what landed" assertions working without changing the visible UI.
func (m *ViewportModel) AppendBanner(text string) {
	m.messages = append(m.messages, MessageEntry{Type: MessageBanner, Content: text, Complete: true})
	m.placeholderShown = false
}

// AppendAutoTrigger appends a QUM-634 auto-continue marker to both the log
// and the inner ChatList.
func (m *ViewportModel) AppendAutoTrigger(summary string) {
	m.messages = append(m.messages, MessageEntry{Type: MessageAutoTrigger, Content: summary, Complete: true})
	if cl := m.ChatList(); cl != nil {
		cl.AppendAutoTrigger(summary)
	}
	m.placeholderShown = false
}

// AppendUserMessage appends a user turn to both the log and the ChatList.
func (m *ViewportModel) AppendUserMessage(text string) {
	m.messages = append(m.messages, MessageEntry{Type: MessageUser, Content: text, Complete: true})
	if cl := m.ChatList(); cl != nil {
		cl.AppendUser(text)
	}
	m.placeholderShown = false
}

// AppendAssistantChunk appends a streaming chunk to the in-flight assistant
// turn (or starts one if none is open) on both stores.
func (m *ViewportModel) AppendAssistantChunk(text string) {
	if n := len(m.messages); n > 0 && m.messages[n-1].Type == MessageAssistant && !m.messages[n-1].Complete {
		m.messages[n-1].Content += text
	} else {
		m.messages = append(m.messages, MessageEntry{Type: MessageAssistant, Content: text})
	}
	if cl := m.ChatList(); cl != nil {
		cl.AppendAssistantChunk(text)
	}
	m.placeholderShown = false
}

// AppendThinking records a thinking content block on the ChatList as a
// transient marker. Not mirrored into the legacy MessageEntry log — thinking
// blocks have no replay representation and the marker is intentionally
// transient (dropped on the next non-thinking append). (QUM-677 S7)
func (m *ViewportModel) AppendThinking() {
	if cl := m.ChatList(); cl != nil {
		cl.AppendThinking()
	}
	m.placeholderShown = false
}

// FinalizeAssistantMessage marks the in-flight assistant entry as complete
// on both stores.
func (m *ViewportModel) FinalizeAssistantMessage() {
	if n := len(m.messages); n > 0 && m.messages[n-1].Type == MessageAssistant && !m.messages[n-1].Complete {
		m.messages[n-1].Complete = true
	}
	if cl := m.ChatList(); cl != nil {
		cl.FinalizeAssistantMessage()
	}
}

// HasPendingAssistant returns true if the last entry is an in-flight
// assistant turn.
func (m *ViewportModel) HasPendingAssistant() bool {
	if n := len(m.messages); n > 0 {
		return m.messages[n-1].Type == MessageAssistant && !m.messages[n-1].Complete
	}
	return false
}

// AppendToolCall is the legacy two-arg-input entry point. Forwards to
// AppendToolCallWithHeader.
func (m *ViewportModel) AppendToolCall(name, toolID string, approved bool, input, fullInput string) {
	m.AppendToolCallWithHeader(name, toolID, approved, input, fullInput, "", nil, "")
}

// AppendToolCallWithHeader records a tool-call row on both stores.
func (m *ViewportModel) AppendToolCallWithHeader(name, toolID string, approved bool,
	input, fullInput, headerArg string, headerParams []KVPair, parentToolUseID string,
) {
	// Mirror the QUM-386 depth/parent inference so the log entry reflects
	// what ChatList renders.
	depth := 0
	parentID := ""
	if cl := m.ChatList(); cl != nil {
		switch {
		case parentToolUseID != "":
			parentID = parentToolUseID
			depth = 1
		case len(cl.activeAgents) > 0 && name != "Agent":
			depth = 1
			parentID = cl.lastActiveAgent
		}
	}
	m.messages = append(m.messages, MessageEntry{
		Type:          MessageToolCall,
		Content:       name,
		Complete:      true,
		Approved:      approved,
		ToolInput:     input,
		ToolInputFull: fullInput,
		ToolID:        toolID,
		Pending:       true,
		Depth:         depth,
		ParentToolID:  parentID,
		HeaderArg:     headerArg,
		HeaderParams:  headerParams,
	})
	if cl := m.ChatList(); cl != nil {
		cl.AppendToolCallWithHeader(name, toolID, approved, input, fullInput, headerArg, headerParams, parentToolUseID)
	}
	m.placeholderShown = false
}

// MarkToolResult flips the matching pending tool-call entry to its finished
// state on both stores. Returns true if a match was found in EITHER store —
// the disjunction keeps the call site (AgentBuffer.MarkToolResult) from
// stranding cl in pendingTools>0 when only the log matched, or vice-versa.
func (m *ViewportModel) MarkToolResult(toolID, content string, isError bool) bool {
	if toolID == "" {
		return false
	}
	logMatched := false
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Type != MessageToolCall {
			continue
		}
		if m.messages[i].ToolID != toolID {
			continue
		}
		m.messages[i].Pending = false
		m.messages[i].Failed = isError
		m.messages[i].Result = content
		logMatched = true
		break
	}
	clMatched := false
	if cl := m.ChatList(); cl != nil {
		clMatched = cl.MarkToolResult(toolID, content, isError)
	}
	return logMatched || clMatched
}

// HasPendingToolCall reports whether at least one logged tool-call entry is
// still in flight.
func (m *ViewportModel) HasPendingToolCall() bool {
	for _, e := range m.messages {
		if e.Type == MessageToolCall && e.Pending {
			return true
		}
	}
	return false
}

// SetSpinnerFrame is retained as a no-op. The global spinner subsystem
// (QUM-336) was removed in S4 — ToolCallItem renders its own static glyph.
func (m *ViewportModel) SetSpinnerFrame(_ string) {}

// SetToolInputsExpanded propagates the global expand-tool-inputs flag to
// both the legacy log (so future GetMessages observers see consistent
// state) and the inner ChatList.
func (m *ViewportModel) SetToolInputsExpanded(expanded bool) {
	m.toolInputsExpanded = expanded
	if cl := m.ChatList(); cl != nil {
		cl.SetToolInputsExpanded(expanded)
	}
}

// ToolInputsExpanded reports the current expand state.
func (m *ViewportModel) ToolInputsExpanded() bool { return m.toolInputsExpanded }

// AppendStatus records a status message in the log. Post-S6 the status text
// is rendered through the status-bar transient label, not the viewport — the
// log retention is purely for back-compat tests.
func (m *ViewportModel) AppendStatus(text string) {
	m.messages = append(m.messages, MessageEntry{Type: MessageStatus, Content: text, Complete: true})
	m.placeholderShown = false
}

// AppendSystemMessage records a MessageSystem entry. Post-S6 system text is
// either delivered as part of a user-role turn (the bridge already
// surfaced it) or routed to the status-bar transient label.
func (m *ViewportModel) AppendSystemMessage(text string) {
	m.messages = append(m.messages, MessageEntry{Type: MessageSystem, Content: text, Complete: true})
	m.placeholderShown = false
}

// AppendSystemNotification peels `<system-notification>` envelopes off the
// text and appends one MessageSystemNotification entry per envelope to both
// the log and the inner ChatList. When the input contains no envelope the
// raw text falls back to AppendSystemMessage so legacy plain inbox banners
// (pre-QUM-555) still leave a log entry; ChatList intentionally drops the
// raw form per chatlist-invariants L3.
func (m *ViewportModel) AppendSystemNotification(text string) {
	rest := text
	appended := false
	for {
		stripped, notifType, isInterrupt, remaining, ok := stripSystemNotificationTag(rest)
		if !ok {
			break
		}
		m.messages = append(m.messages, MessageEntry{
			Type:             MessageSystemNotification,
			Content:          stripped,
			Complete:         true,
			Interrupt:        isInterrupt,
			NotificationType: notifType,
		})
		appended = true
		rest = remaining
	}
	if !appended {
		m.AppendSystemMessage(text)
	} else if strings.TrimSpace(rest) != "" {
		m.AppendSystemMessage(rest)
	}
	if cl := m.ChatList(); cl != nil {
		cl.AppendSystemNotification(text)
	}
	m.placeholderShown = false
}

// AppendError records an error entry in the log. Post-S6 errors flow
// through the γ overlay (ErrorDialog); the log retention is purely for
// back-compat tests.
func (m *ViewportModel) AppendError(text string) {
	m.messages = append(m.messages, MessageEntry{Type: MessageError, Content: text, Complete: true})
	m.placeholderShown = false
}

package tui

import tea "charm.land/bubbletea/v2"

// SessionBackend is the interface AppModel uses to drive the underlying
// Claude session and pull events into the Bubble Tea model. It abstracts
// over both the legacy Bridge (channel-based, per-turn lifecycle) and the
// unified-runtime TUIAdapter (autonomous EventBus stream). After QUM-400
// step 3 lands the legacy implementation goes away and only the unified
// adapter remains.
type SessionBackend interface {
	// Initialize returns a tea.Cmd that performs any one-shot startup work
	// (registering MCP tools, starting the runtime loop, etc.). Emits
	// SessionInitializedMsg on success or SessionErrorMsg on failure.
	Initialize() tea.Cmd

	// SendMessage returns a tea.Cmd that delivers a user prompt to the
	// session. Emits UserMessageSentMsg on success or SessionErrorMsg on
	// failure.
	SendMessage(text string) tea.Cmd

	// WaitForEvent returns a tea.Cmd that blocks on the next session event
	// and maps it to the appropriate tea.Msg.
	WaitForEvent() tea.Cmd

	// Interrupt returns a tea.Cmd that requests interruption of the
	// in-flight turn. Emits InterruptResultMsg.
	Interrupt() tea.Cmd

	// Close shuts down the underlying session.
	Close() error

	// SessionID returns the Claude session ID for status-bar display.
	SessionID() string

	// IsContinuous reports whether the backend produces autonomous events
	// outside of a user turn. Legacy backends return false; the unified
	// adapter returns true. AppModel uses this to decide when to keep
	// WaitForEvent running across turn boundaries.
	IsContinuous() bool
}

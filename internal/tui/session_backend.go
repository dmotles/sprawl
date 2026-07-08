package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// DropTelemetrySource is an optional capability that backends may implement
// to surface EventBus drop telemetry to the status bar (QUM-681). Backends
// that don't implement it produce no status-bar segment.
type DropTelemetrySource interface {
	DropTelemetry() map[string]EventDropSnapshot
}

// CompactCapableBackend is an optional capability a backend may implement to
// advertise support for the /compact builtin (QUM-865). Backends that don't
// implement it are treated as not supporting /compact — the command is neither
// offered in the popover nor routed as passthrough.
type CompactCapableBackend interface {
	SupportsCompact() bool
}

// EventDropSnapshot mirrors runtime.DropTelemetry on the TUI side so the
// tui package doesn't need to import internal/runtime (QUM-681).
type EventDropSnapshot struct {
	Cumulative uint64
	LastDropAt time.Time
}

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

	// SendPassthrough delivers a backend-builtin passthrough command line
	// (e.g. /compact) verbatim, emitting UserMessageSentMsg{Passthrough:true}
	// on success (QUM-865). The Passthrough flag tells the reducer to skip the
	// pending-zone entry — the backend intercepts the command locally and never
	// emits an isReplay echo, so a tracked entry would never settle.
	SendPassthrough(text string) tea.Cmd

	// SendAttachment returns a tea.Cmd that validates local image files,
	// assembles an image-before-text multimodal turn, and delivers it
	// (QUM-860). Emits UserMessageSentMsg (with attachment chips) on success,
	// AttachRejectedMsg (ToastError) on a local validation failure with no turn
	// sent, or SessionErrorMsg if the backend has no runtime.
	SendAttachment(paths []string, prompt string) tea.Cmd

	// WaitForEvent returns a tea.Cmd that blocks on the next session event
	// and maps it to the appropriate tea.Msg.
	WaitForEvent() tea.Cmd

	// Interrupt returns a tea.Cmd that requests interruption of the
	// in-flight turn. Emits InterruptResultMsg.
	Interrupt() tea.Cmd

	// Recall cancels still-pending human-typed prompts and returns their text
	// for the input to rehydrate (QUM-824 — weave-only UX). Emits
	// PromptsRecalledMsg.
	Recall() tea.Cmd

	// SendAllNow cancels still-pending human-typed prompts and resubmits them
	// as one now-priority message (QUM-824 — weave-only UX). Emits
	// SendAllNowResultMsg.
	SendAllNow() tea.Cmd

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

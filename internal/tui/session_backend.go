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

// LivenessProbe is an optional capability that backends may implement to
// allow the AppModel watchdog (QUM-775 item 2) to ask the underlying
// runtime whether it is genuinely in a turn. The watchdog uses this as a
// backstop: when the TUI's turnState has been stuck in TurnStreaming/
// TurnThinking for longer than the watchdog timeout with no bus activity,
// the watchdog probes RuntimeInTurn() and, if the runtime is idle, forces
// finalizeTurn() to recover from a dropped terminal event. Backends that
// don't implement this interface are simply not watchdog-probed (the
// watchdog stays a fail-safe no-op).
type LivenessProbe interface {
	RuntimeInTurn() bool
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

	// WaitForEvent returns a tea.Cmd that blocks on the next session event
	// and maps it to the appropriate tea.Msg.
	WaitForEvent() tea.Cmd

	// Interrupt returns a tea.Cmd that requests interruption of the
	// in-flight turn. Emits InterruptResultMsg.
	Interrupt() tea.Cmd

	// InterruptAndSend preempts the in-flight turn (if any) AND delivers
	// `text` as the next prompt. Emits InterruptResultMsg.
	InterruptAndSend(text string) tea.Cmd

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

package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/agentloop"
)

// AssistantContentMsg batches all content blocks from a single assistant
// message so parallel Agent tool_use blocks are all delivered to the App.
// (QUM-386)
type AssistantContentMsg struct {
	Msgs []tea.Msg
}

// tea.Msg types for protocol events from the host session.
// These are consumed by the TUI's Update method to drive UI state.

// TurnState represents the current state of a conversation turn.
type TurnState int

const (
	TurnIdle TurnState = iota
	TurnThinking
	TurnStreaming
	TurnComplete
)

// String returns the human-readable name of the turn state.
func (s TurnState) String() string {
	switch s {
	case TurnIdle:
		return "idle"
	case TurnThinking:
		return "thinking"
	case TurnStreaming:
		return "streaming"
	case TurnComplete:
		return "complete"
	default:
		return "unknown"
	}
}

// AssistantTextMsg contains a streaming text chunk from Claude.
type AssistantTextMsg struct {
	Text string
}

// ToolCallMsg represents a tool call observed in the assistant's response.
type ToolCallMsg struct {
	ToolName string
	ToolID   string
	Approved bool
	Input    string // concise summary of tool input
	// FullInput is the un-truncated, multi-line representation of the raw
	// tool input. Bash returns the verbatim command; everything else is
	// pretty-printed JSON. Surfaced when the user toggles the global
	// tool-input expand state in AppModel (QUM-335).
	FullInput string
}

// ToolResultMsg carries the result of a previously-emitted tool call. The
// bridge produces one whenever Claude emits a `user` protocol message whose
// content array contains a `tool_result` block. ToolID matches the originating
// ToolCallMsg.ToolID so the AppModel can flip the lifecycle state on the right
// MessageEntry. Content is the raw result text (string or joined text-block
// array). IsError mirrors the `is_error` field of the tool_result block.
// (QUM-336)
type ToolResultMsg struct {
	ToolID  string
	Content string
	IsError bool
}

// TurnStateMsg signals a change in the conversation turn lifecycle.
type TurnStateMsg struct {
	State TurnState
}

// SessionErrorMsg carries an error from the host session or process death.
type SessionErrorMsg struct {
	Err error
}

// Error implements the error interface for convenience.
func (m SessionErrorMsg) Error() string {
	return m.Err.Error()
}

// SessionResultMsg signals that a turn is complete, with cost/token info.
type SessionResultMsg struct {
	Result       string
	IsError      bool
	DurationMs   int
	NumTurns     int
	TotalCostUsd float64
}

// UserMessageSentMsg confirms that user input was dispatched to the session.
type UserMessageSentMsg struct{}

// SessionInitializedMsg signals that the Claude session is ready.
type SessionInitializedMsg struct{}

// SubmitMsg is sent by InputModel when the user presses Enter with non-empty text.
type SubmitMsg struct {
	Text string
}

// AgentTreeMsg carries refreshed agent tree data from the supervisor.
// RootUnread is the unread count in the root agent's (weave's) maildir,
// polled alongside child-agent unread counts so the tree can render an
// unread badge on the weave root row (QUM-205 / QUM-311).
type AgentTreeMsg struct {
	Nodes      []TreeNode
	RootUnread int
}

// InboxArrivalMsg signals that a message has been delivered to the root
// agent's (weave's) maildir. Dispatched by the TUI-aware notifier installed
// in `cmd/enter.go` before the bubbletea program starts (QUM-311). The App
// responds by appending a short status banner and scheduling an immediate
// agent-tree refresh so the weave row's unread badge updates within ~1s
// instead of waiting for the next 2s tick.
type InboxArrivalMsg struct {
	From    string
	Subject string
}

// AgentSelectedMsg is emitted when the user presses Enter on a tree node.
type AgentSelectedMsg struct {
	Name string
}

// ConfirmResultMsg carries the user's response from the confirmation dialog.
type ConfirmResultMsg struct {
	Confirmed bool
}

// SignalMsg indicates an OS signal (SIGTERM, SIGHUP) was received.
type SignalMsg struct{}

// RestartSessionMsg signals that the user wants to restart the Claude subprocess.
type RestartSessionMsg struct{}

// HandoffRequestedMsg signals that the weave subprocess invoked the
// handoff MCP tool. The App responds by tearing down the current
// bridge and triggering the same restart path EOF takes. Supervisors fire a
// channel event that `cmd/enter.go` converts into this msg via tea.Program.Send.
type HandoffRequestedMsg struct{}

// OpenPaletteMsg requests that the command palette overlay be shown. The
// app gates this on input not being disabled and no other modal being active.
type OpenPaletteMsg struct{}

// ClosePaletteMsg requests that the command palette overlay be hidden.
type ClosePaletteMsg struct{}

// InjectPromptMsg carries a command's prompt template to be sent to Claude
// via the bridge, without rendering it as a user message in the viewport.
type InjectPromptMsg struct {
	Template string
}

// InboxDrainMsg carries a pre-rendered flush-queue prompt that the TUI should
// inject into the current bridge as a user turn. Dispatched by the AppModel's
// AgentTreeMsg handler (or cmd/enter.go's notifier) when weave's harness
// queue has pending entries AND the turn is idle. The AppModel treats this
// like InjectPromptMsg but with an inbox-origin banner; after the send
// succeeds, the entries are moved from pending/ to delivered/ so they are
// not re-injected. QUM-323.
type InboxDrainMsg struct {
	Prompt   string
	EntryIDs []string
	// Class is "async" or "interrupt" — drives the banner wording. Empty
	// defaults to "async".
	Class string
}

// ToggleHelpMsg flips the help overlay visibility (same effect as F1).
type ToggleHelpMsg struct{}

// PaletteQuitMsg requests an immediate app quit triggered by the palette's
// /exit command. The app sets `quitting=true` then returns tea.Quit — same
// post-confirm semantics as the Ctrl-C path.
type PaletteQuitMsg struct{}

// ActivityTickMsg carries a freshly-fetched tail of an agent's activity ring
// (QUM-296). Agent names the agent this tail belongs to; the App applies it
// only if Agent matches the currently-observed agent.
type ActivityTickMsg struct {
	Agent   string
	Entries []agentloop.ActivityEntry
}

// SessionRestartingMsg signals that the TUI is transitioning between Claude
// subprocess sessions (e.g. after transport EOF or /handoff). The App renders
// a status banner carrying Reason while the restart work runs.
type SessionRestartingMsg struct {
	Reason string
}

// ConsolidationProgressMsg is a periodic tick delivered while the TUI is
// waiting for the async restart work (FinalizeHandoff + Prepare + new
// session) to complete (QUM-260). Elapsed is the time since the restart
// began. The App updates the status bar's restart-elapsed indicator and
// reschedules another tick as long as the restart is still in flight so
// the user sees visible progress instead of a frozen UI.
type ConsolidationProgressMsg struct {
	Elapsed time.Duration
}

// ChildTranscriptMsg carries a freshly-loaded snapshot of a child agent's
// Claude session transcript. Emitted when the user observes a non-root agent
// (initial hydrate on AgentSelectedMsg, plus periodic re-reads while observed).
// The App applies it only if Agent matches the currently-observed agent.
//
// Empty Entries with no error → "Waiting for <agent>..." placeholder
// (covers: agent has no session_id yet, or the session log has not been
// created on disk yet). QUM-332.
type ChildTranscriptMsg struct {
	Agent     string
	SessionID string
	Entries   []MessageEntry
	Err       error
}

// InterruptResultMsg signals the outcome of a bridge.Interrupt() call triggered
// by the user pressing ESC during a streaming/thinking turn (QUM-380). Err is
// non-nil if the interrupt request failed at the transport level.
type InterruptResultMsg struct {
	Err error
}

// RestartCompleteMsg delivers the outcome of the async restart work
// (QUM-260). Bridge carries the freshly-launched Claude subprocess on
// success; Err is non-nil if restartFunc failed. The App installs the new
// bridge, clears the restarting flag, and either shows an error dialog or
// renders the New-Session banner.
type RestartCompleteMsg struct {
	Bridge *Bridge
	Err    error
}

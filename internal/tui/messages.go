package tui

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
type AgentTreeMsg struct {
	Nodes []TreeNode
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

// SessionRestartingMsg signals that the TUI is transitioning between Claude
// subprocess sessions (e.g. after transport EOF or /handoff). The App renders
// a status banner carrying Reason while the restart work runs.
type SessionRestartingMsg struct {
	Reason string
}

// Package agent (extension) — QUM-726 wake-on-traffic prompts.
//
// When a SendMessage or Delegate call targets an offline-but-recoverable
// agent and the caller asks for `wake_if_offline=true`, the supervisor wakes
// the recipient via the existing Wake plumbing and threads a wake-specific
// notice through RuntimeStartSpec.RestartInjection. The exact wording of that
// notice is pinned here so it is reviewed in one place; per-reason templates
// keep the recipient's first post-wake turn unambiguous about why it is back
// online and what (if anything) to attend to.
//
// Edit with care: each template is byte-pinned by a unit test against the
// spec literal.
package agent

import "fmt"

// Wake-prompt templates. The literal text is part of the QUM-726 contract.
//
// WakePromptBare: pure `wake` MCP verb with no payload. `%s` is the
//
//	previous-state token (e.g. "paused", "killed").
//
// WakePromptSendMessage: a SendMessage(wake_if_offline=true) targeted an
//
//	offline agent. `%s` is the message body verbatim.
//
// WakePromptDelegate: a Delegate(wake_if_offline=true) targeted an offline
//
//	agent. The em-dash in the template is a real U+2014 — do not "normalize"
//	it. `%s`/`%s` are previous-state and task body.
const (
	WakePromptBare        = "You have been resumed. Last status was %s. Check inbox and continue."
	WakePromptSendMessage = "You are coming back online. The following message was sent to you while offline; respond as appropriate:\n\n%s"
	WakePromptDelegate    = "You are coming back online from an offline state (%s). If you were in the middle of something previously, that work is abandoned — this delegate is your new task:\n\n%s"
)

// WakeReason names the reason a Wake call was issued. Used to select the
// RestartInjection template via BuildWakePrompt.
type WakeReason string

// Canonical reasons. Unknown reasons fall back to bare.
const (
	WakeReasonBare        WakeReason = "bare"
	WakeReasonSendMessage WakeReason = "send_message"
	WakeReasonDelegate    WakeReason = "delegate"
)

// BuildWakePrompt selects and formats the RestartInjection text for a wake.
//
// For WakeReasonBare the body is ignored.
// For WakeReasonSendMessage, the previousState is ignored (the send-message
//
//	template does not reference it).
//
// For WakeReasonDelegate, both previousState and body are interpolated.
// Unknown reasons fall back to the bare template (formatted with
// previousState).
func BuildWakePrompt(reason WakeReason, previousState, body string) string {
	switch reason {
	case WakeReasonSendMessage:
		return fmt.Sprintf(WakePromptSendMessage, body)
	case WakeReasonDelegate:
		return fmt.Sprintf(WakePromptDelegate, previousState, body)
	case WakeReasonBare:
		return fmt.Sprintf(WakePromptBare, previousState)
	default:
		return fmt.Sprintf(WakePromptBare, previousState)
	}
}

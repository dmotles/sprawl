// QUM-726: pin the exact byte-for-byte text of the wake-on-traffic
// RestartInjection templates. These are part of the contract — the
// supervisor builds RuntimeStartSpec.RestartInjection from them and the
// recipient's first post-wake turn sees them as the user message.
//
// A drive-by edit to wake_prompts.go shouldn't silently move the contract,
// so this file inlines the literal strings rather than const-vs-const.
package agent

import (
	"testing"
)

func TestWakePromptBare_Verbatim(t *testing.T) {
	got := BuildWakePrompt(WakeReasonBare, "paused", "")
	want := "You have been resumed. Last status was paused. Check inbox and continue."
	if got != want {
		t.Errorf("BuildWakePrompt(bare) mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestWakePromptSendMessage_Verbatim(t *testing.T) {
	got := BuildWakePrompt(WakeReasonSendMessage, "paused", "hello")
	want := "You are coming back online. The following message was sent to you while offline; respond as appropriate:\n\nhello"
	if got != want {
		t.Errorf("BuildWakePrompt(send_message) mismatch\n got: %q\nwant: %q", got, want)
	}
}

// QUM-1186: TestWakePromptDelegate_Verbatim was removed with
// WakeReasonDelegate / WakePromptDelegate. The template it byte-pinned no
// longer exists — there is no surviving behaviour to re-host, since every
// payload-carrying wake is now a send_message wake, pinned above.
//
// TestBuildWakePrompt_UnknownReasonFallsBackToBare below is now also the
// guard that a caller passing the old "delegate" reason string gets the bare
// template rather than a panic or an empty prompt.

func TestBuildWakePrompt_UnknownReasonFallsBackToBare(t *testing.T) {
	got := BuildWakePrompt(WakeReason("zzz-not-a-real-reason"), "killed", "ignored")
	want := "You have been resumed. Last status was killed. Check inbox and continue."
	if got != want {
		t.Errorf("BuildWakePrompt(unknown) fallback mismatch\n got: %q\nwant: %q", got, want)
	}
}

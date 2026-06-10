// QUM-726: pin the exact byte-for-byte text of the wake-on-traffic
// RestartInjection templates. These are part of the contract — the
// supervisor builds RuntimeStartSpec.RestartInjection from them and the
// recipient's first post-wake turn sees them as the user message.
//
// A drive-by edit to wake_prompts.go shouldn't silently move the contract,
// so this file inlines the literal strings rather than const-vs-const.
package agent

import (
	"strings"
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

func TestWakePromptDelegate_Verbatim(t *testing.T) {
	got := BuildWakePrompt(WakeReasonDelegate, "killed", "do X")
	want := "You are coming back online from an offline state (killed). If you were in the middle of something previously, that work is abandoned — this delegate is your new task:\n\ndo X"
	if got != want {
		t.Errorf("BuildWakePrompt(delegate) mismatch\n got: %q\nwant: %q", got, want)
	}
	// Em-dash check (U+2014). If somebody normalizes to "--" or " - " the
	// pin above already fails, but be explicit since this is the most
	// common drift.
	if !strings.Contains(got, "—") {
		t.Errorf("delegate template missing em-dash (U+2014); got: %q", got)
	}
}

func TestBuildWakePrompt_UnknownReasonFallsBackToBare(t *testing.T) {
	got := BuildWakePrompt(WakeReason("zzz-not-a-real-reason"), "killed", "ignored")
	want := "You have been resumed. Last status was killed. Check inbox and continue."
	if got != want {
		t.Errorf("BuildWakePrompt(unknown) fallback mismatch\n got: %q\nwant: %q", got, want)
	}
}

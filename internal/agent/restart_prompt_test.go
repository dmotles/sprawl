// QUM-723: pin the exact byte-for-byte text of the restart injection prompt.
// This is the neutral notice that RecoverAgents threads through StartResume's
// RuntimeStartSpec so a freshly-resumed child sees a benign nudge on the
// resumed turn instead of replaying whatever prompt was last in flight.
//
// The literal is intentionally inlined here (rather than const-vs-const) so a
// drive-by edit to restart_prompt.go can't silently move the contract.
package agent

import "testing"

func TestRestartInjectionPrompt_ExactText(t *testing.T) {
	want := "Sprawl was just restarted. If you were in the middle of something, continue where you left off. Otherwise, hang tight."
	if RestartInjectionPrompt != want {
		t.Errorf("RestartInjectionPrompt mismatch\n got: %q\nwant: %q", RestartInjectionPrompt, want)
	}
}

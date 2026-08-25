package agent

import (
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/card"
)

// The card-backed builders' output is pinned by prompt_card_fidelity_test.go and
// by every pre-existing golden and safety scanner in this package. What is left
// untested there is the failure path: mustRenderSeedPrompt's contract is that a
// role with no card is a loud panic, never a partial or empty system prompt.
func TestMustRenderSeedPrompt_PanicsOnAnUnknownAgentType(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("mustRenderSeedPrompt returned normally for an agent type with no card — a caller would ship an empty system prompt")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panicked with %T, want a string message: %v", r, r)
		}
		if !strings.Contains(msg, "no-such-type") {
			t.Errorf("the panic does not name the offending agent type: %q", msg)
		}
	}()
	_ = mustRenderSeedPrompt("no-such-type", card.Input{AgentName: "zone"})
}

// The negative control for the above: a known type must NOT panic, or the test
// would pass against a helper that panics unconditionally.
func TestMustRenderSeedPrompt_RendersAKnownAgentType(t *testing.T) {
	got := mustRenderSeedPrompt("engineer", cardInput("zone", "root", "sprawl/zone", "", testEnvConfig()))
	if got == "" {
		t.Fatal("mustRenderSeedPrompt returned an empty prompt for a known agent type")
	}
	if strings.Contains(got, "{{") {
		t.Errorf("the rendered prompt still carries a template token: %q", got[strings.Index(got, "{{"):min(strings.Index(got, "{{")+40, len(got))])
	}
}

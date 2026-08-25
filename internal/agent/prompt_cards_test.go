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

// TestBuildCardPrompt_SharesTheSeamWithBuildPrompt is the continuity check that
// keeps this package's whole prose/golden/scanner corpus load-bearing.
//
// The launch path (internal/supervisor.buildRoleSystemPrompt) calls
// BuildCardPrompt, not Build*Prompt. If BuildCardPrompt rendered through its own
// helper, every scanner and golden here — including
// TestPromptScanners_MutatedSeedReachesTheScanners — would measure a function
// nothing launches, and would stay green while doing it. This asserts the two
// entry points agree byte for byte on the no-card path AND that both go through
// the substitutable renderCard seam.
func TestBuildCardPrompt_SharesTheSeamWithBuildPrompt(t *testing.T) {
	prev := renderCard
	var calls int
	renderCard = func(c *card.Card, in card.Input) (string, error) {
		calls++
		return prev(c, in)
	}
	t.Cleanup(func() { renderCard = prev })

	env := DefaultEnvConfig()
	env.WorkDir = "/tmp/wt"

	for _, tc := range []struct {
		agentType string
		family    string
		direct    func() string
	}{
		{"engineer", "", func() string { return BuildEngineerPrompt("a", "p", "b", env) }},
		{"researcher", "", func() string { return BuildResearcherPrompt("a", "p", "b", env) }},
		{"qa", "", func() string { return BuildQAPrompt("a", "p", "b", env) }},
		{"manager", "fam", func() string { return BuildManagerPrompt("a", "p", "b", "fam", env) }},
	} {
		t.Run(tc.agentType, func(t *testing.T) {
			before := calls
			want := tc.direct()
			got := BuildCardPrompt(nil, tc.agentType, "a", "p", "b", tc.family, env)
			if got != want {
				t.Errorf("BuildCardPrompt disagrees with Build*Prompt for %q", tc.agentType)
			}
			// Two renders through the ONE seam. A bypass shows up here even when
			// the strings happen to match.
			if calls-before != 2 {
				t.Errorf("renderCard invoked %d times, want 2 — an entry point bypasses the seam", calls-before)
			}
			if want == "" {
				t.Errorf("rendered an EMPTY prompt for %q", tc.agentType)
			}
		})
	}
}

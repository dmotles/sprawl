// This file is package agent_test, not agent: it needs internal/rootinit, and
// internal/rootinit imports internal/agent. An external test package is the
// only place in this package's test surface that can see both.
package agent_test

import (
	"testing"

	"github.com/dmotles/sprawl/internal/card"
	"github.com/dmotles/sprawl/internal/rootinit"
)

// TestLegacyCards_MetadataMatchesTheBehaviourItReplaces is the metadata half of
// the extraction-fidelity claim. The prompt BODY is pinned byte-for-byte by the
// golden comparison above; nothing there looks at the frontmatter, so a card
// that quietly demoted the manager off its 1M-context model would pass every
// other test in this diff.
//
// It lives here rather than in internal/card because the model spawn uses today
// comes from rootinit.ModelForAgentType, and asserting against that mapping
// rather than hand-copying "opus[1m]" into a table is the whole point — a
// hand-copied expectation would only agree with itself. internal/rootinit
// cannot be imported from internal/card's tests (it imports this package, which
// imports internal/card).
func TestLegacyCards_MetadataMatchesTheBehaviourItReplaces(t *testing.T) {
	// The type list is derived from the seeds rather than written out, so this
	// half and internal/card's TestSeeds_MetadataMatchesTheTable cover the same
	// set by construction. A hardcoded list would let a fifth seed get its name,
	// version and effort pinned while its model went silently unchecked.
	seeds, err := card.Seeds()
	if err != nil {
		t.Fatalf("card.Seeds: %v", err)
	}
	if len(seeds) == 0 {
		t.Fatal("no embedded seeds — this test would assert nothing")
	}
	for _, c := range seeds {
		agentType := c.AgentType
		want := rootinit.ModelForAgentType(agentType)
		if want == "" {
			t.Errorf("%s: ModelForAgentType returned no model — this assertion would compare two empty strings", agentType)
			continue
		}
		if c.Model != want {
			t.Errorf("%s: card %s@%d declares model %q but spawn uses %q today — the extraction changed behaviour",
				agentType, c.Name, c.Version, c.Model, want)
		}
	}
}

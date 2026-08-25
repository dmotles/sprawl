package agent

import (
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/card"
)

// Fidelity of the constants-to-cards extraction (QUM-1251, M2).
//
// The prompt goldens were produced by the Go constant graph and are edited by
// nobody during this migration — CLAUDE.md's rule that a golden may not be
// regenerated in the extraction slices exists for exactly this test. So a
// byte-identical render is a mechanical proof that the extraction was lossless,
// and it stays meaningful after the constants are deleted because the goldens
// outlive them.
//
// The comparison is against the GOLDEN FILES rather than against Build*Prompt.
// Comparing a card to the builder it was extracted from would go green on a
// tree where both had drifted together.

func cardFidelityInput(agentName, parentName, branchName, family string) card.Input {
	env := testEnvConfig()
	return card.Input{
		AgentName:  agentName,
		ParentName: parentName,
		BranchName: branchName,
		Family:     family,
		Env: card.Env{
			WorkDir:  env.WorkDir,
			Platform: env.Platform,
			Shell:    env.Shell,
			TestMode: env.TestMode,
		},
	}
}

// legacyCardFidelityCases mirrors the tuples TestGenerateGoldenFiles renders.
// They must stay in step: a tuple that drifts compares a card against a golden
// built from different identity values and fails for a reason that has nothing
// to do with extraction fidelity.
func legacyCardFidelityCases() []struct {
	agentType string
	golden    string
	in        card.Input
} {
	return []struct {
		agentType string
		golden    string
		in        card.Input
	}{
		{"engineer", "engineer_tui.golden", cardFidelityInput("zone", "root", "sprawl/zone", "")},
		{"researcher", "researcher_tui.golden", cardFidelityInput("birch", "root", "sprawl/birch", "")},
		{"manager", "manager_tui.golden", cardFidelityInput("cedar", "weave", "dmotles/feature-x", "engineering")},
		{"qa", "qa_tui.golden", cardFidelityInput("inspector", "tower", "dmotles/feature-x", "")},
	}
}

func firstDiff(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) == len(b) {
		return -1
	}
	return n
}

func TestLegacyCards_RenderByteIdenticalToGoldens(t *testing.T) {
	for _, tc := range legacyCardFidelityCases() {
		t.Run(tc.agentType, func(t *testing.T) {
			c, err := card.SeedForType(tc.agentType)
			if err != nil {
				t.Fatalf("no embedded card for %q: %v", tc.agentType, err)
			}
			got, err := c.Render(tc.in)
			if err != nil {
				t.Fatalf("rendering %s@%d: %v", c.Name, c.Version, err)
			}
			want := readGolden(t, tc.golden)
			if got != want {
				i := firstDiff(got, want)
				t.Errorf("card %s@%d does not render %s byte-identically.\ngot %d bytes, want %d bytes; first diff at byte %d\n got: %q\nwant: %q",
					c.Name, c.Version, tc.golden, len(got), len(want), i,
					got[i:min(i+120, len(got))], want[i:min(i+120, len(want))])
			}
		})
	}
}

// TestLegacyCards_VariantArmsMatchTheBuilders covers the two env-conditional
// arms no golden exercises: the sub-agent banner and the test-sandbox warning.
//
// This one compares against Build*Prompt rather than a golden, which is the
// weaker claim — but the alternative is no claim at all, and it is only weak in
// the direction the test above already closes: the shared body is pinned to the
// goldens, so what is left for the builder comparison to constrain is exactly
// the conditional splicing, which the builders and Render implement separately.
func TestLegacyCards_VariantArmsMatchTheBuilders(t *testing.T) {
	envs := map[string]EnvConfig{
		"subagent":             {WorkDir: "/w", Platform: "linux", Shell: "/bin/zsh", Subagent: true, ParentName: "weave"},
		"test mode":            {WorkDir: "/w", Platform: "linux", Shell: "/bin/zsh", TestMode: true},
		"subagent+test mode":   {WorkDir: "/w", Platform: "linux", Shell: "/bin/zsh", TestMode: true, Subagent: true, ParentName: "weave"},
		"no worktree or shell": {},
	}
	for envName, env := range envs {
		in := card.Input{
			AgentName: "zone", ParentName: "root", BranchName: "sprawl/zone", Family: "engineering",
			Env: card.Env{
				WorkDir: env.WorkDir, Platform: env.Platform, Shell: env.Shell,
				TestMode: env.TestMode, Subagent: env.Subagent, ParentName: env.ParentName,
			},
		}
		builders := map[string]string{
			"engineer":   BuildEngineerPrompt("zone", "root", "sprawl/zone", env),
			"researcher": BuildResearcherPrompt("zone", "root", "sprawl/zone", env),
			"qa":         BuildQAPrompt("zone", "root", "sprawl/zone", env),
			"manager":    BuildManagerPrompt("zone", "root", "sprawl/zone", "engineering", env),
		}
		for agentType, want := range builders {
			c, err := card.SeedForType(agentType)
			if err != nil {
				t.Fatalf("no embedded card for %q: %v", agentType, err)
			}
			got, err := c.Render(in)
			if err != nil {
				t.Fatalf("%s/%s: Render: %v", envName, agentType, err)
			}
			if got != want {
				i := firstDiff(got, want)
				t.Errorf("%s/%s: card render diverges from the builder at byte %d\n got: %q\nwant: %q",
					envName, agentType, i, got[i:min(i+120, len(got))], want[i:min(i+120, len(want))])
			}
		}
	}
}

// TestLegacyCards_MutatedSeedBreaksFidelity is the positive control for the
// test above, and it is the reason that test is allowed to be trusted.
//
// A byte-comparison against a golden is exactly the shape that goes green while
// measuring nothing — if SeedForType returned a card whose body happened to be
// assembled from the same constants, or if Render silently returned the golden,
// the comparison would pass without the seed file being consulted at all. So a
// single, surgical edit to the parsed seed body MUST reach the rendered output
// and MUST break the comparison. If this subtest passes quietly, the fidelity
// test above is vacuous.
func TestLegacyCards_MutatedSeedBreaksFidelity(t *testing.T) {
	// "Sprawl" is in every legacy prompt's opening sentence, so one target
	// covers all four cards without per-type special-casing. Its presence is
	// asserted before the mutation: a Replace that matched nothing would leave
	// the body untouched and this control would then be asserting that an
	// UNMUTATED card differs from its golden — i.e. it would fail for the
	// opposite reason and mean nothing.
	const (
		before = "Sprawl"
		after  = "Sprowl"
	)
	for _, tc := range legacyCardFidelityCases() {
		t.Run(tc.agentType, func(t *testing.T) {
			c, err := card.SeedForType(tc.agentType)
			if err != nil {
				t.Fatalf("no embedded card for %q: %v", tc.agentType, err)
			}
			if !strings.Contains(c.Body, before) {
				t.Fatalf("the mutation target %q is not in the %s card body — this control mutates nothing and proves nothing", before, tc.agentType)
			}
			mutated := *c
			mutated.Body = strings.Replace(c.Body, before, after, 1)

			got, err := mutated.Render(tc.in)
			if err != nil {
				t.Fatalf("rendering the mutated card: %v", err)
			}
			want := readGolden(t, tc.golden)
			if got == want {
				t.Fatal("a mutated seed body still rendered byte-identically to the golden — the fidelity test is not reading the seed")
			}
			t.Logf("control fired: mutated %s seed diverges from %s at byte %d", tc.agentType, tc.golden, firstDiff(got, want))
		})
	}
}

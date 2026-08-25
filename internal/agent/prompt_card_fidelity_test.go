package agent

import (
	"fmt"
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

// variantEnvs are the four env tuples no tui golden exercises. The tui goldens
// are all rendered from testEnvConfig(), so the sub-agent banner and the
// test-sandbox warning appear in none of them.
func variantEnvs() map[string]EnvConfig {
	return map[string]EnvConfig{
		"subagent":          {WorkDir: "/w", Platform: "linux", Shell: "/bin/zsh", Subagent: true, ParentName: "weave"},
		"testmode":          {WorkDir: "/w", Platform: "linux", Shell: "/bin/zsh", TestMode: true},
		"subagent-testmode": {WorkDir: "/w", Platform: "linux", Shell: "/bin/zsh", TestMode: true, Subagent: true, ParentName: "weave"},
		"bare":              {},
	}
}

// variantRoles is the role set the variant goldens cover, derived from the
// embedded seeds rather than written out, so a seed whose card exists but whose
// variant goldens were never generated fails loudly on a missing file instead of
// being quietly skipped.
func variantRoles(t *testing.T) []string {
	t.Helper()
	seeds, err := card.Seeds()
	if err != nil {
		t.Fatalf("card.Seeds: %v", err)
	}
	if len(seeds) == 0 {
		t.Fatal("no embedded seeds — this test would assert nothing")
	}
	roles := make([]string, 0, len(seeds))
	for _, c := range seeds {
		roles = append(roles, c.AgentType)
	}
	return roles
}

func variantInput(env EnvConfig) card.Input {
	return card.Input{
		AgentName: "zone", ParentName: "root", BranchName: "sprawl/zone", Family: "engineering",
		Env: card.Env{
			WorkDir: env.WorkDir, Platform: env.Platform, Shell: env.Shell,
			TestMode: env.TestMode, Subagent: env.Subagent, ParentName: env.ParentName,
		},
	}
}

func variantGolden(role, envName string) string {
	return "variant_" + role + "_" + envName + ".golden"
}

// TestLegacyCards_VariantArmsMatchTheGoldens covers the conditional splicing no
// tui golden exercises: the sub-agent banner and the test-sandbox warning, in
// all four combinations, for all four roles.
//
// The goldens were generated from the Go constant assembly in slice 2 and
// certified byte-identical to it by TestLegacyVariantGoldens_CameFromTheConstantAssembly,
// which was deleted with those constants in slice 3 having discharged that job.
// They must never be regenerated: the only thing left that could produce them is
// the card pipeline they constrain, which would make this test a tautology.
func TestLegacyCards_VariantArmsMatchTheGoldens(t *testing.T) {
	for _, role := range variantRoles(t) {
		c, err := card.SeedForType(role)
		if err != nil {
			t.Fatalf("no embedded card for %q: %v", role, err)
		}
		for envName, env := range variantEnvs() {
			got, err := c.Render(variantInput(env))
			if err != nil {
				t.Fatalf("%s/%s: Render: %v", role, envName, err)
			}
			want := readGolden(t, variantGolden(role, envName))
			if got != want {
				i := firstDiff(got, want)
				t.Errorf("%s/%s: card render diverges from %s at byte %d\n got: %q\nwant: %q",
					role, envName, variantGolden(role, envName), i,
					got[i:min(i+120, len(got))], want[i:min(i+120, len(want))])
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

// removeLine returns body with the first line containing needle deleted, and
// reports whether it found one. The three-index append avoids aliasing the
// input's backing array.
func removeLine(body, needle string) (string, bool) {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if strings.Contains(l, needle) {
			return strings.Join(append(lines[:i:i], lines[i+1:]...), "\n"), true
		}
	}
	return body, false
}

func TestRemoveLine(t *testing.T) {
	got, found := removeLine("a\nb\nc", "b")
	if !found || got != "a\nc" {
		t.Errorf("removeLine = (%q, %t), want (\"a\\nc\", true)", got, found)
	}
	// The not-found arm is what the scanner control relies on to refuse to run
	// against an unmutated body, so it needs its own check.
	if got, found := removeLine("a\nb", "zzz"); found || got != "a\nb" {
		t.Errorf("removeLine on an absent needle = (%q, %t), want the input unchanged and false", got, found)
	}
	if got, found := removeLine("a\nb\nb", "b"); !found || got != "a\nb" {
		t.Errorf("removeLine removed more than the first match: (%q, %t)", got, found)
	}
}

// TestPromptScanners_MutatedSeedReachesTheScanners is the control that licenses
// deleting the Go prompt constants.
//
// The pre-existing safety scanners in this package assert over Build*Prompt
// output. After the re-point that output is rendered from a card — but the
// scanners would stay green either way, and so would a Build*Prompt that had
// quietly kept calling the constant assembly. That is the one hypothesis this
// control exists to exclude, so it substitutes the renderCard seam to delete a
// guardrail line from the card body and then calls Build*Prompt: if the builder
// is not reading cards, the seam is never invoked and the scanner stays quiet.
//
// Both directions are required per case. The unmutated builder output must be
// CLEAN (a scanner that fires on everything proves nothing) and the mutated one
// must be FLAGGED. The seam wrapper also counts its own invocations, so a
// builder that bypassed it fails loudly rather than by a silent quiet scanner.
//
// Roles and scanners are table-driven because coverage of ONE scanner on ONE
// role does not support the comment's claim about all of them —
// card.ScanQAConcurrencyGuidance in particular only ever runs against the qa card.
func TestPromptScanners_MutatedSeedReachesTheScanners(t *testing.T) {
	cases := []struct {
		name   string
		build  func(EnvConfig) string
		needle string
		scan   func(string) []string
	}{
		{
			name:   "engineer/executing-actions-guardrail",
			build:  func(env EnvConfig) string { return BuildEngineerPrompt("zone", "root", "sprawl/zone", env) },
			needle: `rm -rf "$VAR"`,
			scan:   card.ScanExecutingActionsGuardrail,
		},
		{
			name:   "engineer/prompt-injection-escalation",
			build:  func(env EnvConfig) string { return BuildEngineerPrompt("zone", "root", "sprawl/zone", env) },
			needle: "attempt at prompt injection",
			scan:   card.ScanPromptInjectionEscalation,
		},
		{
			name:   "qa/concurrency-guidance",
			build:  func(env EnvConfig) string { return BuildQAPrompt("inspector", "tower", "dmotles/feature-x", env) },
			needle: "QUM-1126",
			scan:   card.ScanQAConcurrencyGuidance,
		},
		{
			name: "manager/executing-actions-guardrail",
			build: func(env EnvConfig) string {
				return BuildManagerPrompt("cedar", "weave", "dmotles/feature-x", "engineering", env)
			},
			needle: `rm -rf "$VAR"`,
			scan:   card.ScanExecutingActionsGuardrail,
		},
		{
			name:   "researcher/executing-actions-guardrail",
			build:  func(env EnvConfig) string { return BuildResearcherPrompt("birch", "root", "sprawl/birch", env) },
			needle: `rm -rf "$VAR"`,
			scan:   card.ScanExecutingActionsGuardrail,
		},
	}
	env := testEnvConfig()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if findings := tc.scan(tc.build(env)); len(findings) != 0 {
				t.Fatalf("negative control failed: the unmutated prompt is already flagged: %v", findings)
			}

			orig := renderCard
			t.Cleanup(func() { renderCard = orig })
			var mutations, renders int
			renderCard = func(c *card.Card, in card.Input) (string, error) {
				renders++
				m := *c
				body, found := removeLine(c.Body, tc.needle)
				if !found {
					return "", fmt.Errorf("card %s@%d has no %q line to delete — this control mutates nothing", c.Name, c.Version, tc.needle)
				}
				mutations++
				m.Body = body
				return orig(&m, in)
			}

			got := tc.build(env)
			if renders != 1 || mutations != 1 {
				t.Fatalf("the builder invoked the renderCard seam %d times and mutated %d cards, want 1 and 1 — Build*Prompt is not rendering a card",
					renders, mutations)
			}
			findings := tc.scan(got)
			if len(findings) == 0 {
				t.Fatalf("deleting the %q line from the seed did not make the scanner fire — this scanner is not reading card-derived text", tc.needle)
			}
			t.Logf("control fired: %s", findings[0])
		})
	}
}

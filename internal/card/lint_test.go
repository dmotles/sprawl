package card

import (
	"strings"
	"testing"
)

// renderForLint renders a card the way Lint's callers must.
func renderForLint(t *testing.T, c *Card) string {
	t.Helper()
	out, err := c.Render(LintInput())
	if err != nil {
		t.Fatalf("rendering %s@%d: %v", c.Name, c.Version, err)
	}
	return out
}

// TestLint_AppliesAtLeastOneRuleToEverySeed is the floor. Without it, every
// "the seeds are clean" assertion below is satisfied by a rule set that ran
// nothing — zero findings out of zero rules is not evidence.
func TestLint_AppliesAtLeastOneRuleToEverySeed(t *testing.T) {
	seeds, err := Seeds()
	if err != nil {
		t.Fatalf("Seeds: %v", err)
	}
	if len(seeds) == 0 {
		t.Fatalf("no embedded seeds — this test would measure nothing")
	}
	for _, c := range seeds {
		rep := Lint(c, renderForLint(t, c))
		if len(rep.Applied) == 0 {
			t.Errorf("%s@%d (agent_type %q): NO lint rule applied, so a clean verdict would mean nothing",
				c.Name, c.Version, c.AgentType)
		}
	}
}

// TestLint_EverySeedIsClean is the negative control for the firing tests below:
// a rule set that fired on everything would prove nothing about the ones that
// are supposed to fire.
func TestLint_EverySeedIsClean(t *testing.T) {
	seeds, err := Seeds()
	if err != nil {
		t.Fatalf("Seeds: %v", err)
	}
	for _, c := range seeds {
		rep := Lint(c, renderForLint(t, c))
		for _, f := range rep.Findings {
			t.Errorf("%s@%d: rule %s: %s", c.Name, c.Version, f.Rule, f.Message)
		}
		if !rep.Clean() {
			t.Errorf("%s@%d: Clean() = false (applied=%v findings=%d)", c.Name, c.Version, rep.Applied, len(rep.Findings))
		}
	}
}

// stripLine removes the first line containing needle. Used to build a subject
// where the defect IS present out of a real seed, so a finding cannot be an
// artefact of a synthetic body that resembles nothing shipped.
func stripLine(t *testing.T, body, needle string) string {
	t.Helper()
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if strings.Contains(l, needle) {
			return strings.Join(append(append([]string{}, lines[:i]...), lines[i+1:]...), "\n")
		}
	}
	t.Fatalf("subject does not contain %q, so removing it would not make a defect", needle)
	return ""
}

// TestLint_FiresOnASeedWithTheGuardrailRemoved is the positive control that
// licenses AC5: card-lint has to REFUSE a card missing a safety section, and it
// is demonstrated against a real seed with one line deleted rather than against
// a hand-written fixture.
func TestLint_FiresOnASeedWithTheGuardrailRemoved(t *testing.T) {
	seed, err := SeedForType("engineer")
	if err != nil {
		t.Fatalf("SeedForType: %v", err)
	}
	mutated := *seed
	mutated.Body = stripLine(t, seed.Body, `rm -rf "$VAR"`)

	rep := Lint(&mutated, renderForLint(t, &mutated))
	if len(rep.Applied) == 0 {
		t.Fatalf("no rule applied, so this is not a control")
	}
	var got []string
	for _, f := range rep.Findings {
		got = append(got, f.Rule)
	}
	if !contains(got, "executing-actions-guardrail") {
		t.Errorf("removing the destructive-var guardrail produced findings %v, want one from executing-actions-guardrail", got)
	}
	if rep.Clean() {
		t.Errorf("Clean() = true for a card missing the destructive-var guardrail")
	}
}

// TestLint_QARuleIsQAOnly pins that the QA concurrency rule applies to the qa
// card and stands down for the others — asserting it against engineer would make
// the engineer seed permanently dirty.
func TestLint_QARuleIsQAOnly(t *testing.T) {
	for _, agentType := range LintableAgentTypes {
		c, err := SeedForType(agentType)
		if err != nil {
			t.Fatalf("SeedForType(%q): %v", agentType, err)
		}
		rep := Lint(c, renderForLint(t, c))
		applied := contains(rep.Applied, "qa-concurrency-guidance")
		if want := agentType == "qa"; applied != want {
			t.Errorf("agent_type %q: qa-concurrency-guidance applied = %v, want %v (skipped: %q)",
				agentType, applied, want, rep.Skipped["qa-concurrency-guidance"])
		}
	}
}

// TestLint_AnUnknownAgentTypeAppliesNoRules is the other half of the floor, and
// the reason LintableAgentTypes is a closed set: a card for a type card-lint has
// no rules for must be reported as UNCHECKED, not as clean. `def publish`
// refuses on len(Applied) == 0.
func TestLint_AnUnknownAgentTypeAppliesNoRules(t *testing.T) {
	seed, err := SeedForType("engineer")
	if err != nil {
		t.Fatalf("SeedForType: %v", err)
	}
	c := *seed
	c.AgentType = "novelist"

	rep := Lint(&c, renderForLint(t, &c))
	if len(rep.Applied) != 0 {
		t.Errorf("applied %v for agent_type %q, want none", rep.Applied, c.AgentType)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("findings %v for a card no rule examined", rep.Findings)
	}
	// The distinction the whole Report type exists for: zero findings, but NOT
	// clean, because nothing was asked.
	if rep.Clean() {
		t.Errorf("Clean() = true for a card that no rule examined")
	}
	if len(rep.Skipped) != len(SafetyRules()) {
		t.Errorf("Skipped has %d entries, want one per rule (%d) so an operator can see what was not asked",
			len(rep.Skipped), len(SafetyRules()))
	}
}

// TestLint_ADemotedFollowingHeadingDoesNotWidenTheSection covers the silent
// direction of ExtractHeadingSection's original flat-heading assumption.
//
// The loud direction — demoting the safety heading itself — reports "missing
// entirely", so it was never the risk. The risk is demoting the FOLLOWING
// heading: under the original terminator ("the next line starting with `# `")
// the section then ran to the end of the document, and a `rm -rf "$VAR"` in an
// unrelated later subsection satisfied the guardrail rule. Unreachable while
// only the four seeds were scanned; reachable the moment `def publish` lints an
// arbitrary body.
//
// The `# ` twin is the control: the SAME fixture with a top-level following
// heading already fired before this change, so a green run here has to be the
// demotion being handled rather than the fixture being dirty in some other way.
func TestLint_ADemotedFollowingHeadingDoesNotWidenTheSection(t *testing.T) {
	const decoy = "An unrelated example: rm -rf \"$VAR\" is dangerous."
	for name, following := range map[string]string{
		"following heading is top level": "# Some Later Section",
		"following heading is demoted":   "## Some Later Section",
	} {
		t.Run(name, func(t *testing.T) {
			subject := ExecutingActionsHeading + "\nBe careful out there. No guardrail text in this section.\n\n" +
				following + "\n" + decoy + "\n"
			if !strings.Contains(subject, `rm -rf "$VAR"`) {
				t.Fatal("fixture no longer carries the guardrail phrase; the demonstration is broken")
			}
			if _, ok := ExtractHeadingSection(subject, ExecutingActionsHeading); !ok {
				t.Fatal("fixture's own safety section is absent, so this would be absent-section vacuity rather than a scope test")
			}
			if f := ScanExecutingActionsGuardrail(subject); len(f) == 0 {
				t.Errorf("stayed quiet: the guardrail phrase appears only in a LATER section, so the section scope was widened past %q", following)
			}
		})
	}
}

// TestLintInput_RendersEverySeedWithoutError pins that the fixed lint identity is
// actually usable — a LintInput that failed to render would make every test above
// t.Fatal in setup, which reads as a broken harness rather than as a verdict.
func TestLintInput_RendersEverySeedWithoutError(t *testing.T) {
	seeds, err := Seeds()
	if err != nil {
		t.Fatalf("Seeds: %v", err)
	}
	for _, c := range seeds {
		out, err := c.Render(LintInput())
		if err != nil {
			t.Errorf("%s@%d: %v", c.Name, c.Version, err)
			continue
		}
		if strings.Contains(out, "{{") {
			t.Errorf("%s@%d: rendered prompt still carries an unsubstituted token", c.Name, c.Version)
		}
	}
}

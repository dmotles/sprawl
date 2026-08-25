package agent

import (
	"testing"

	"github.com/dmotles/sprawl/internal/card"
)

// QUM-1129. Researcher and QA agents received ZERO of the safety guidance
// engineer/manager agents get: the "Executing actions with care" section
// (including the destructive-var `rm -rf "$VAR"` guardrail) and the
// prompt-injection escalation sentence. QA additionally needs concurrency
// guidance so it can tell a contention-induced false-RED from a regression
// (QUM-1126).
//
// The trap named by the issue: asserting a substring is present against the
// whole assembled prompt string passes even when the prompt is one giant blob
// that happens to contain the needle somewhere irrelevant. So every scanner
// here is section- or line-scoped, and every scanner is exercised against a
// fixture where the defect IS present (must fire) and one where it is absent
// (must stay quiet) in TestPromptScanners_SafetySections_Controls, including
// an explicit demonstration that a bare strings.Contains would be fooled.

// --- Tests over the real prompts ---

// rootPromptCases are the only render cases that must NOT receive the shared
// safety sections (root has its own copy, deliberately incomplete — see the
// negative-control tests below). Kept as the allowlist, rather than listing
// child roles, so that a newly added child render case is covered by
// childSafetyRoleCases automatically: the failure mode QUM-1129 fixes is a
// new role silently born with no safety text, and an opt-in child allowlist
// reproduces exactly that risk for any case nobody remembered to add to it.
var rootPromptCases = map[string]bool{"root": true, "root-no-cli": true}

// childSafetyRoleCases are every rendered case that must receive the full
// shared safety section set — every case except the root ones.
func childSafetyRoleCases() map[string]bool {
	cases := map[string]bool{}
	for _, tc := range allPromptRenderCases() {
		if !rootPromptCases[tc.name] {
			cases[tc.name] = true
		}
	}
	return cases
}

func TestPromptRenderers_ChildRolesHaveExecutingActionsGuardrail(t *testing.T) {
	cases := childSafetyRoleCases()
	for _, tc := range allPromptRenderCases() {
		if !cases[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			for _, f := range card.ScanExecutingActionsGuardrail(tc.render()) {
				t.Errorf("%s prompt: %s", tc.name, f)
			}
		})
	}
}

// TestPromptRenderers_RootExecutingActionsSectionLacksGuardrail is the
// negative control demanded by the issue: a role deliberately excluded from
// full guardrail coverage (root, scoped OUT of QUM-1129 on purpose) must FAIL
// the "has the guardrail" assertion. It also proves the scanner distinguishes
// "section absent" from "section present but incomplete": root DOES render
// its own "Executing actions with care" section, just without the
// destructive-var guardrail.
func TestPromptRenderers_RootExecutingActionsSectionLacksGuardrail(t *testing.T) {
	for _, tc := range allPromptRenderCases() {
		if !rootPromptCases[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			prompt := tc.render()
			if _, ok := card.ExtractHeadingSection(prompt, card.ExecutingActionsHeading); !ok {
				t.Fatalf("%s prompt: expected the %q section to be present (root has its own copy) so this is a real negative control, not an absent-section vacuity", tc.name, card.ExecutingActionsHeading)
			}
			if f := card.ScanExecutingActionsGuardrail(prompt); len(f) == 0 {
				t.Errorf("%s prompt: card.ScanExecutingActionsGuardrail stayed quiet, but root is deliberately scoped OUT of the destructive-var guardrail (QUM-1129) — the assertion must fail here", tc.name)
			}
		})
	}
}

func TestPromptRenderers_ChildRolesTeachPromptInjectionEscalation(t *testing.T) {
	cases := childSafetyRoleCases()
	for _, tc := range allPromptRenderCases() {
		if !cases[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			for _, f := range card.ScanPromptInjectionEscalation(tc.render()) {
				t.Errorf("%s prompt: %s", tc.name, f)
			}
		})
	}
}

// TestPromptRenderers_RootDoesNotEscalatePromptInjectionToManagerAndWeave is
// the negative control for the prompt-injection scanner: root has its OWN
// prompt-injection sentence (flag it to the user directly), which must NOT
// satisfy a scanner keyed on the child-role escalation phrasing.
func TestPromptRenderers_RootDoesNotEscalatePromptInjectionToManagerAndWeave(t *testing.T) {
	for _, tc := range allPromptRenderCases() {
		if !rootPromptCases[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			prompt := tc.render()
			if !card.MentionsPromptInjection(prompt) {
				t.Fatalf("%s prompt: expected root to mention prompt injection at all (it has its own sentence) so this is a real negative control", tc.name)
			}
			if f := card.ScanPromptInjectionEscalation(prompt); len(f) == 0 {
				t.Errorf("%s prompt: card.ScanPromptInjectionEscalation stayed quiet, but root escalates prompt-injection suspicions to the user, not via \"manager and weave\" — the assertion must fail here", tc.name)
			}
		})
	}
}

// TestPromptRenderers_QATeachesConcurrencyGuidance pins the QA-only
// concurrency guidance. Every other child role is a negative control: this
// guidance is QA-specific (QA is the role ordered to run `make validate` and
// judge red/green under fleet load), so asserting its presence against
// engineer/researcher/manager MUST fail.
func TestPromptRenderers_QATeachesConcurrencyGuidance(t *testing.T) {
	cases := childSafetyRoleCases()
	for _, tc := range allPromptRenderCases() {
		if !cases[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			findings := card.ScanQAConcurrencyGuidance(tc.render())
			if tc.name == "qa" {
				for _, f := range findings {
					t.Errorf("qa prompt: %s", f)
				}
			} else if len(findings) == 0 {
				t.Errorf("%s prompt: card.ScanQAConcurrencyGuidance stayed quiet — this guidance is QA-specific by design at QUM-1129; if a later change deliberately gave this role the same guidance, update this control rather than reading the failure as a defect", tc.name)
			}
		})
	}
}

package card

import (
	"regexp"
	"strings"
	"testing"
)

// The slim v2 cards (QUM-1251, AC1 and the issue's slimming prescription).
//
// The issue prescribes the slimming in content terms: remove forced numeric
// decomposition minimums and thoroughness pressure, add deference to the repo's
// own CLAUDE.md and guardrails, prefer the smallest change that satisfies the
// acceptance criteria, and have plans state what will NOT be built. Those are
// claims about prose, so the probes below are regexes — and a prose regex is the
// single easiest way to write an assertion that cannot fail.
//
// So every probe here is asserted in BOTH directions, against real subjects on
// both sides:
//
//   - a REMOVAL probe must be quiet on every slim card AND must fire on the
//     named legacy card, which still contains the language. The legacy hit is
//     the positive control: it proves the regex matches the thing it describes,
//     so "quiet on slim" means the text is gone rather than that the pattern
//     never matched anything.
//   - an ADDITION probe must fire on every slim card AND be quiet on every
//     legacy card. The legacy silence is the aim control: it proves the probe is
//     keyed on the added clause rather than on boilerplate both generations
//     share.
//
// Without the second direction each of these is the classic prose-assertion
// false green: a typo'd regex is quiet on slim, and "quiet" is exactly what a
// removal probe wants to see.

// slimProbe is one prose claim about the slimming.
//
// wantFire and wantQuiet name CARDS, not agent types, because that is the axis
// the claim is about — the whole point is that two cards for the same type
// differ. A probe with an empty wantFire would assert nothing that a broken
// regex could not satisfy, so both fields are checked non-empty below.
type slimProbe struct {
	what      string
	re        *regexp.Regexp
	wantFire  []string
	wantQuiet []string
}

func allSlim() []string {
	return []string{"slim-engineer", "slim-manager", "slim-qa", "slim-researcher"}
}

func allLegacy() []string {
	return []string{"legacy-engineer", "legacy-manager", "legacy-qa", "legacy-researcher"}
}

// The deference probe below is compound — CLAUDE.md AND deference language on
// the SAME line — for a specific reason: "CLAUDE.md" alone is not an addition.
// legacy-qa.md already mentions it, when naming where the tracker is
// configured. A bare-presence probe would therefore have no clean negative
// control on that card, and would report the slimming's central clause as
// present in a card that never received it.
func slimProbes() []slimProbe {
	return []slimProbe{
		{
			// The forced decomposition minimum the issue names by number.
			what:      "a forced numeric subtask minimum",
			re:        regexp.MustCompile(`(?i)\b\d+ *[-–] *\d+ +(well-defined +)?subtasks\b`),
			wantFire:  []string{"legacy-manager"},
			wantQuiet: allSlim(),
		},
		{
			what:      "mandatory-workflow pressure",
			re:        regexp.MustCompile(`(?i)this is not optional|do not skip steps|\(MANDATORY\)`),
			wantFire:  []string{"legacy-engineer", "legacy-qa"},
			wantQuiet: allSlim(),
		},
		{
			what:      "thoroughness pressure",
			re:        regexp.MustCompile(`(?i)extremely critical|investigate deeply|do not skim|maximum correctness`),
			wantFire:  []string{"legacy-engineer", "legacy-researcher"},
			wantQuiet: allSlim(),
		},
		{
			// Compound on one line, deliberately: see the note above.
			what:      "deference to the repo's own CLAUDE.md and guardrails",
			re:        regexp.MustCompile(`(?im)^(?:.*CLAUDE\.md.*(?:defer|precedence|outrank|wins).*|.*(?:defer|precedence|outrank|wins).*CLAUDE\.md.*)$`),
			wantFire:  allSlim(),
			wantQuiet: allLegacy(),
		},
		{
			// Scoped to the two roles that actually change code. QA verifies and
			// the researcher reports; neither has a "change" to size, so
			// demanding the clause of them would be bending the cards to fit the
			// probe rather than the other way round.
			what:      "a smallest-change-for-the-acceptance-criteria clause",
			re:        regexp.MustCompile(`(?i)smallest change[^\n]*acceptance criteri`),
			wantFire:  []string{"slim-engineer", "slim-manager"},
			wantQuiet: []string{"legacy-engineer", "legacy-manager"},
		},
		{
			what:      "an instruction to state what will NOT be built",
			re:        regexp.MustCompile(`(?i)not building|deliberately not|out of scope`),
			wantFire:  []string{"slim-engineer", "slim-manager"},
			wantQuiet: []string{"legacy-engineer", "legacy-manager"},
		},
	}
}

// seedBodies returns every embedded card body keyed by card name.
func seedBodies(t *testing.T) map[string]string {
	t.Helper()
	cards, err := Seeds()
	if err != nil {
		t.Fatalf("Seeds: %v", err)
	}
	out := make(map[string]string, len(cards))
	for _, c := range cards {
		out[c.Name] = c.Body
	}
	return out
}

// TestSlimCards_ProbesAreAimedBeforeTheyAreTrusted runs before the probes are
// believed: it refuses a probe that names no subject on either side.
//
// A removal probe with an empty wantFire has no positive control, so its silence
// on the slim cards is indistinguishable from a regex that matches nothing at
// all — and that is the shape this whole file is guarding against. Checking it
// here rather than in each probe means a future probe added without a control
// fails loudly instead of joining the suite as decoration.
func TestSlimCards_ProbesAreAimedBeforeTheyAreTrusted(t *testing.T) {
	probes := slimProbes()
	if len(probes) == 0 {
		t.Fatal("no probes — this file would assert nothing")
	}
	bodies := seedBodies(t)
	for _, p := range probes {
		if len(p.wantFire) == 0 {
			t.Errorf("%s: no card is expected to match, so this probe has no positive control", p.what)
		}
		if len(p.wantQuiet) == 0 {
			t.Errorf("%s: no card is expected NOT to match, so this probe has no aim control", p.what)
		}
		for _, name := range append(append([]string{}, p.wantFire...), p.wantQuiet...) {
			if _, ok := bodies[name]; !ok {
				t.Errorf("%s: names card %q, which is not embedded — the probe would silently cover nothing", p.what, name)
			}
		}
	}
}

// TestSlimCards_TheSlimmingActuallyHappened is the substance: every probe, both
// directions.
func TestSlimCards_TheSlimmingActuallyHappened(t *testing.T) {
	bodies := seedBodies(t)
	for _, p := range slimProbes() {
		t.Run(p.what, func(t *testing.T) {
			for _, name := range p.wantFire {
				body, ok := bodies[name]
				if !ok {
					t.Errorf("%s is not embedded", name)
					continue
				}
				if !p.re.MatchString(body) {
					t.Errorf("%s: %s does not contain %s — if this is a slim card the clause was never added; if it is a legacy card this probe's regex is broken and its silence on the slim cards proves nothing",
						name, name, p.what)
				}
			}
			for _, name := range p.wantQuiet {
				body, ok := bodies[name]
				if !ok {
					t.Errorf("%s is not embedded", name)
					continue
				}
				if m := p.re.FindString(body); m != "" {
					t.Errorf("%s still carries %s: %q", name, p.what, strings.TrimSpace(m))
				}
			}
		})
	}
}

// TestSlimCards_WinTheResolutionForEveryAgentType is the one assertion that
// catches the highest-probability defect in this slice.
//
// pickHighest compares with a strict `>`, and Seeds() is sorted by name then
// version, so on a version TIE the first name wins — and "legacy-" sorts before
// "slim-". Ship the slim cards at version 1 and every visible symptom of success
// is present: they parse, they embed, syncSeedCards inserts them, `sprawl def
// list` shows eight rows, AC1 looks satisfied — and not one agent ever spawns
// with a slim prompt, with the whole suite green. Nothing else in the tree
// notices, because every other test either iterates all seeds or pins legacy by
// name.
func TestSlimCards_WinTheResolutionForEveryAgentType(t *testing.T) {
	for _, agentType := range []string{"engineer", "manager", "qa", "researcher"} {
		c, err := SeedForType(agentType)
		if err != nil {
			t.Errorf("SeedForType(%q): %v", agentType, err)
			continue
		}
		if !strings.HasPrefix(c.Name, "slim-") {
			t.Errorf("%s spawns with %s@%d — the slim card is embedded but does not win resolution, so the slimming reaches no agent",
				agentType, c.Name, c.Version)
		}
		if c.Version < 2 {
			t.Errorf("%s resolves to version %d; a slim card at version 1 loses the tie to legacy-%s@1 because pickHighest is a strict > and legacy sorts first",
				agentType, c.Version, agentType)
		}
		if c.AgentType != agentType {
			t.Errorf("%s resolved a card whose agent_type is %q", agentType, c.AgentType)
		}
	}
}

// TestSlimCards_AreShorterThanWhatTheyReplace. Slimming is the point, so a
// "slim" card that grew is a naming lie. Not a tuned ratio — just strictly
// shorter, which is the weakest form of the claim that can still fail.
func TestSlimCards_AreShorterThanWhatTheyReplace(t *testing.T) {
	bodies := seedBodies(t)
	for _, agentType := range []string{"engineer", "manager", "qa", "researcher"} {
		slim, ok := bodies["slim-"+agentType]
		if !ok {
			t.Errorf("slim-%s is not embedded", agentType)
			continue
		}
		legacy, ok := bodies["legacy-"+agentType]
		if !ok {
			t.Errorf("legacy-%s is not embedded", agentType)
			continue
		}
		if len(slim) >= len(legacy) {
			t.Errorf("slim-%s is %d bytes against legacy-%s's %d — it is not slimmer", agentType, len(slim), agentType, len(legacy))
		}
	}
}

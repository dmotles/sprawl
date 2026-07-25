package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These tests guard the QUM-953 assertion-rigor convention's two documentation
// homes. Honest labelling, per the convention itself:
//
//   - The first two are PRESENCE checks scoped to the section the text has to
//     live in. They fail if it is deleted, reworded past the mechanism it
//     names, or relocated — but they cannot verify that anyone follows the
//     convention.
//   - TestAssertionFloorDocMatchesScript is a real invariant, and only for
//     doc/script AGREEMENT: it forces whoever bumps the worked example's
//     assertion-count floor to update the doc that quotes it. It does NOT
//     protect the floor's value — lowering MIN_ASSERTIONS while updating the
//     doc passes. Do not cite it as guarding the harness's strength.

// assertionRigorSection is the SKILL.md heading that carries the long-form
// rule, and the phrase CLAUDE.md must point at. Shared so a rename cannot
// leave the two homes naming different sections.
const assertionRigorSection = "Assertion Rigor"

// wirelogUnitScript is the in-tree worked example, spelled once so a rename
// cannot half-land between the read and the citation check.
const wirelogUnitScript = "scripts/test-wirelog-helpers-unit.sh"

// mdSection returns the body of the `## <heading>` section of body, up to the
// next top-level `## ` heading. Boundaries are detected line-anchored and
// fence-aware, so a `## ` inside a fenced markdown example neither satisfies
// nor truncates a section. Section scoping is load-bearing: an unscoped search
// would also match CLAUDE.md's mandatory-test matrix table, and would keep
// passing if the text were moved somewhere nobody reads.
func mdSection(t *testing.T, name, body, heading string) string {
	t.Helper()

	lines := strings.Split(body, "\n")

	// An unbalanced fence upstream would invert parity for the rest of the
	// file and surface as "no such section" — i.e. "you didn't write the
	// docs" instead of "you have a stray fence". Diagnose it directly.
	fences := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fences++
		}
	}
	if fences%2 != 0 {
		t.Fatalf("%s: odd number of ``` fence lines (%d) — a stray fence makes section scoping unreliable", name, fences)
	}

	start, end := -1, len(lines)
	inFence := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if start < 0 {
			// Exact match on the whole heading line, so `### <heading>` and
			// `## <heading> (example)` do not satisfy it.
			if trimmed == "## "+heading {
				start = i + 1
			}
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			end = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s: no top-level %q section found", name, "## "+heading)
	}
	return strings.Join(lines[start:end], "\n")
}

// normalize lowercases and collapses all whitespace runs to single spaces, so a
// required phrase is matched on wording rather than on line wrapping.
func normalizeMD(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// phraseReq names an OBLIGATION and the pattern that evidences it.
//
// Prefer a pattern that names a MECHANISM (`parent-commit`, `assertion-count
// floor`) over a generic token, and reach for mdPat whenever a literal would
// false-pass. But note honestly what the entries below are: several ARE single
// generic tokens (`mutation`, `can fail`, `pre-existing`), because the two homes
// word the same rule differently and markdown emphasis lands mid-phrase, so a
// shared longer literal would lock wording rather than constrain meaning. Those
// entries guard deletion, not comprehension.
type phraseReq struct {
	concept string
	label   string
	re      *regexp.Regexp
}

// lit requires a literal phrase (matched whitespace- and case-insensitively).
func mdLit(concept, phrase string) phraseReq {
	return phraseReq{concept, phrase, regexp.MustCompile(regexp.QuoteMeta(normalizeMD(phrase)))}
}

// pat requires a regexp over the normalized text. Use it where a literal would
// false-pass — e.g. `0 passed` is a substring of `10 passed`, so the empty-run
// rule needs a word boundary to be evidenced at all.
func mdPat(concept, label, pattern string) phraseReq {
	return phraseReq{concept, label, regexp.MustCompile(pattern)}
}

// requirePhrases asserts each requirement against the normalized section.
//
// minRequired is this loop's own assertion-count floor. requirePhrases is an
// aggregating harness, and a harness that reports green having asserted nothing
// is the exact defect the document it guards forbids — so truncating or
// emptying a caller's list must FAIL here rather than pass quietly.
func requirePhrases(t *testing.T, where, section string, minRequired int, required []phraseReq) {
	t.Helper()

	if len(required) < minRequired {
		t.Fatalf("%s: assertion-count floor — %d requirements listed, expected at least %d; the list was truncated and this check measured less than it claims",
			where, len(required), minRequired)
	}

	norm := normalizeMD(section)
	for _, r := range required {
		if !r.re.MatchString(norm) {
			t.Errorf("%s does not state %s (no %q)", where, r.concept, r.label)
		}
	}
}

// emptyRunRule requires the `0 passed / 0 failed` rule with word boundaries, so
// a doc that only ever quotes a healthy run (`10 passed / 0 failed`) does not
// satisfy it.
func emptyRunRule() []phraseReq {
	return []phraseReq{
		mdPat("that a run asserting nothing is a failure", "0 passed", `\b0 passed\b`),
		mdPat("that a run asserting nothing is a failure", "0 failed", `\b0 failed\b`),
	}
}

func readRepoFile(t *testing.T, rel ...string) string {
	t.Helper()

	path := filepath.Join(append([]string{repoRootFromTest(t)}, rel...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func TestClaudeMDStatesAssertionConvention(t *testing.T) {
	const where = "CLAUDE.md ## Code Patterns"
	section := mdSection(t, "CLAUDE.md", readRepoFile(t, "CLAUDE.md"), "Code Patterns")

	required := []phraseReq{
		mdLit("that assertions must be shown able to fail", "can fail"),
		mdLit("the negative-control demonstration", "negative control"),
		mdLit("the mutation demonstration", "mutation"),
		mdLit("the red-first demonstration", "red-first"),
		mdLit("the assertion-count floor requirement", "assertion-count floor"),
		mdLit("the parent-commit control mechanism", "parent-commit"),
		mdLit("that a parent-commit control only establishes pre-existence", "pre-existing"),
		// Ties the two homes together: the pointer must name the section that
		// carries the long-form rule, not just the skill. Proximity-matched
		// within one sentence, so two unrelated paragraphs do not satisfy it.
		mdPat("the pointer to the long-form home", "/testing-practices … "+assertionRigorSection,
			`/testing-practices[^.]{0,120}assertion rigor|assertion rigor[^.]{0,120}/testing-practices`),
	}
	required = append(required, emptyRunRule()...)

	requirePhrases(t, where, section, 10, required)
}

func TestTestingPracticesSkillHasAssertionRigorSection(t *testing.T) {
	const where = "testing-practices SKILL.md ## " + assertionRigorSection
	body := readRepoFile(t, ".claude", "skills", "testing-practices", "SKILL.md")
	section := mdSection(t, "testing-practices SKILL.md", body, assertionRigorSection)

	required := []phraseReq{
		// The worked example must be cited by path, not paraphrased.
		mdLit("the in-tree worked example, by path", wirelogUnitScript),
		mdLit("the negative-control demonstration", "negative control"),
		mdLit("the mutation demonstration", "mutation"),
		mdLit("the red-first demonstration", "red-first"),
		mdLit("the assertion-count floor requirement", "assertion-count floor"),
		mdLit("the parent-commit control mechanism", "parent-commit"),
		mdLit("that a parent-commit control only establishes pre-existence", "pre-existing"),
		// The remaining four are QUM-953 acceptance criteria, not author
		// preference: the selection-effect reasoning, the recursive hazard,
		// the status-report extension, and the convention's stated limit.
		mdLit("the selection-effect reasoning behind the rule", "selection effect"),
		mdLit("the recursive hazard", "recursive hazard"),
		mdLit("the status-report extension", "verified by reading the code"),
		mdLit("the coordination-layer class no mechanism catches", "no mechanism catches"),
	}
	required = append(required, emptyRunRule()...)

	requirePhrases(t, where, section, 13, required)
}

var (
	// Anchored to the assignment line, so `$MIN_ASSERTIONS` uses and prose
	// mentions do not count as declarations.
	minAssertionsDecl  = regexp.MustCompile(`(?m)^MIN_ASSERTIONS=([0-9]+)$`)
	minAssertionsCited = regexp.MustCompile(`MIN_ASSERTIONS=([0-9]+)`)
)

// citationWindow is how many lines may separate the doc's mention of the worked
// example's path from its citation of that example's floor. It bounds
// attribution: a MIN_ASSERTIONS= far from the path may legitimately belong to a
// different harness or to a generic how-to snippet, and is ignored.
//
// Known limitation, and its direction: every MIN_ASSERTIONS= INSIDE the window
// is asserted against, foreign or not — so a second harness's floor quoted
// within 40 lines of this path is a false FAILURE. That direction is deliberate
// and it is the visible one; the remedy is to move the citation, not to widen
// the window.
//
// This window cannot produce a silent pass on a STALE floor: an in-window
// citation must equal the script's floor. (It can be satisfied coincidentally
// by an unrelated harness quoting the same number nearby — low probability, and
// harmless, since the value still agrees.) If it fails, WIDENING THIS CONSTANT
// IS NOT THE REMEDY — move the floor citation next to the path it describes.
const citationWindow = 40

func TestAssertionFloorDocMatchesScript(t *testing.T) {
	script := readRepoFile(t, strings.Split(wirelogUnitScript, "/")...)
	declared := minAssertionsDecl.FindAllStringSubmatch(script, -1)
	if len(declared) != 1 {
		t.Fatalf("%s: want exactly 1 `MIN_ASSERTIONS=<n>` assignment line, got %d", wirelogUnitScript, len(declared))
	}
	scriptFloor := declared[0][1]

	docLines := strings.Split(readRepoFile(t, ".claude", "skills", "testing-practices", "SKILL.md"), "\n")
	var pathLines []int
	for i, line := range docLines {
		if strings.Contains(line, wirelogUnitScript) {
			pathLines = append(pathLines, i)
		}
	}
	if len(pathLines) == 0 {
		t.Fatalf("testing-practices SKILL.md does not cite %s by path", wirelogUnitScript)
	}

	near := func(i int) bool {
		for _, p := range pathLines {
			if d := i - p; d >= -citationWindow && d <= citationWindow {
				return true
			}
		}
		return false
	}

	var found, mismatched bool
	for i, line := range docLines {
		if !near(i) {
			continue
		}
		// FindAll, not Find: a line quoting two floors must not have the
		// second one silently ignored.
		for _, m := range minAssertionsCited.FindAllStringSubmatch(line, -1) {
			if m[1] != scriptFloor {
				t.Errorf("testing-practices SKILL.md:%d cites MIN_ASSERTIONS=%s but %s enforces MIN_ASSERTIONS=%s",
					i+1, m[1], wirelogUnitScript, scriptFloor)
				mismatched = true
				continue
			}
			found = true
		}
	}
	if !found && !mismatched {
		t.Errorf("testing-practices SKILL.md cites %s but never quotes its floor as MIN_ASSERTIONS=%s within %d lines of the path",
			wirelogUnitScript, scriptFloor, citationWindow)
	}
}

package cmd

import (
	"fmt"
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
	return mdSectionLevel(t, name, body, "##", heading)
}

// mdSectionLevel is the test-facing wrapper: it fatals with extractMDSection's
// diagnosis. The extraction itself returns an error rather than calling Fatalf so
// that TestMDSectionLevelBoundaries can assert WHICH rejection fired — a check
// keyed only on "it failed" would accept the fence-parity diagnosis as proof of
// correct heading rejection.
func mdSectionLevel(t *testing.T, name, body, prefix, heading string) string {
	t.Helper()

	section, err := extractMDSection(name, body, prefix, heading)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return section
}

// extractMDSection returns the body of the `<prefix> <heading>` section, up to
// the next same-or-shallower heading.
func extractMDSection(name, body, prefix, heading string) (string, error) {
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
		return "", fmt.Errorf("%s: odd number of ``` fence lines (%d) — a stray fence makes section scoping unreliable", name, fences)
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
			// Exact match on the whole heading line, so a deeper heading and
			// `## <heading> (example)` do not satisfy it.
			if trimmed == prefix+" "+heading {
				start = i + 1
			}
			continue
		}
		// Same-or-shallower heading ends the section: for prefix "###" that is
		// "### " or "## ", for "##" it is "## " only.
		if isSameOrShallowerHeading(trimmed, prefix) {
			end = i
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf("%s: no %q section found", name, prefix+" "+heading)
	}
	return strings.Join(lines[start:end], "\n"), nil
}

// isSameOrShallowerHeading reports whether line is an ATX heading whose depth is
// at most len(prefix) — i.e. a boundary for a section opened at prefix. h1 counts
// as a boundary too: a second `# ` in the file would otherwise let `## Assertion
// Rigor` silently swallow the following chapter.
func isSameOrShallowerHeading(line, prefix string) bool {
	depth := 0
	for depth < len(line) && line[depth] == '#' {
		depth++
	}
	if depth == 0 || depth >= len(line) || line[depth] != ' ' {
		return false
	}
	return depth <= len(prefix)
}

// mdSubsection is mdSection for a `### <heading>` subsection, ending at the next
// same-or-shallower heading (`### `, `## `, or `# `). Subsection scoping is
// load-bearing for the per-change attribution guard: `mdLit("disjoint")` is
// satisfied by text under a different `###`, so a section-level scope would let a
// deleted sentence keep passing.
func mdSubsection(t *testing.T, name, body, heading string) string {
	t.Helper()
	return mdSectionLevel(t, name, body, "###", heading)
}

// normalize lowercases and collapses all whitespace runs to single spaces, so a
// required phrase is matched on wording rather than on line wrapping.
//
// Blockquote markers are dropped, because a phrase wrapped across two `> ` lines
// would otherwise normalize with a stray ">" mid-phrase and fail a literal
// requirement — which had the test dictating line breaks in the document.
// Dropping a token can only turn a currently-FAILING requirement green, never the
// reverse, so this cannot weaken an existing assertion; `go test ./cmd/ -count=1`
// is the control.
func normalizeMD(s string) string {
	fields := strings.Fields(strings.ToLower(s))
	kept := fields[:0]
	for _, f := range fields {
		if strings.Trim(f, ">") == "" {
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
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

// forbidReq names text that must NOT appear, plus a control string the pattern
// MUST match. The control is not decoration: this is a negative assertion, and
// per the document's own § Negative assertions a typo'd pattern, a renamed file,
// or a mis-scoped body would otherwise read as "clean".
//
// What the control proves, exactly: the instrument was LIVE in the run that
// reports the absence. It does NOT prove the pattern is AIMED at the phrasing
// space the doc might drift into — the control is a string written from the
// pattern, so it can only match what the author already imagined. A reworded
// "twenty-one instances" evades every pattern here while the control still
// passes. The red run at the time of writing is the only evidence of aim, and it
// is spent once the fix lands; treat these as regression guards against the
// wording that DID appear, not as a general ban.
type forbidReq struct {
	concept string
	label   string
	re      *regexp.Regexp
	control string
}

func mdForbid(concept, label, pattern, control string) forbidReq {
	return forbidReq{concept, label, regexp.MustCompile(pattern), control}
}

// forbidPatterns asserts each banned pattern is absent from text, having first
// proven the pattern matches its own control. anchor is a phrase that must be
// present in text, so an empty or mis-scoped body cannot satisfy an absence.
// minRequired is this loop's assertion-count floor, same contract as
// requirePhrases — and weaker than the shell ledger floor the document holds up as
// the model. This one catches a TRUNCATED list (a literal slice in this file); it
// cannot catch "the loop measured nothing", because the Errorf legs below are
// unconditionally reached once the list is non-empty. The ledger floor is stronger
// precisely because a case that dies early contributes no lines.
func forbidPatterns(t *testing.T, where, text, anchor string, minRequired int, banned []forbidReq) {
	t.Helper()

	if len(banned) < minRequired {
		t.Fatalf("%s: assertion-count floor — %d prohibitions listed, expected at least %d; the list was truncated and this check measured less than it claims",
			where, len(banned), minRequired)
	}

	norm := normalizeMD(text)
	if norm == "" {
		t.Fatalf("%s: text under test is empty — an absence proves nothing here", where)
	}
	if !strings.Contains(norm, normalizeMD(anchor)) {
		t.Fatalf("%s: text under test does not contain the anchor %q — the wrong body was scoped and every absence below is vacuous",
			where, anchor)
	}

	for _, r := range banned {
		if !r.re.MatchString(normalizeMD(r.control)) {
			t.Fatalf("%s: instrument dead — pattern %q does not even match its own control %q, so its absence from the doc means nothing",
				where, r.label, r.control)
		}
		if r.re.MatchString(norm) {
			t.Errorf("%s still states %s (pattern %s)%s", where, r.concept, r.label, matchedLineHint(text, r.re))
		}
	}
}

// matchedLineHint names the lines whose own normalized text the pattern matches,
// so a whole-file ban can point at its hits. Best-effort only: the VERDICT above
// is taken on the whole normalized body, because a banned phrase split across a
// line wrap matches there and matches no single line. An empty hint therefore
// means "wrapped", not "no hit".
func matchedLineHint(text string, re *regexp.Regexp) string {
	var at []string
	for i, line := range strings.Split(text, "\n") {
		if re.MatchString(normalizeMD(line)) {
			at = append(at, fmt.Sprintf("%d", i+1))
		}
	}
	if len(at) == 0 {
		return " — hit spans a line wrap; grep the normalized body"
	}
	// Numbered within the text scanned, which for a scoped section is NOT the
	// file's line numbering.
	return " at line(s) " + strings.Join(at, ", ") + " of the text scanned"
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
	// QUM-1155 moved the long-form testing prose out of `## Code Patterns` and
	// into the testing-practices skill, leaving the convention itself in a
	// section of its own. This gate stays pointed at the LIVE CLAUDE.md — not
	// at the frozen pre-cut fixture the relocation oracle uses — because its
	// subject is the text an agent actually receives on every turn. Against a
	// fixture it would be decorative.
	const where = "CLAUDE.md ## Tests and assertions"
	section := mdSection(t, "CLAUDE.md", readRepoFile(t, "CLAUDE.md"), "Tests and assertions")

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

// workedExampleSection is the `###` heading carrying the worked example's three
// demonstrations. Spelled once so a rename cannot half-land.
const workedExampleSection = "The worked example: `" + wirelogUnitScript + "`"

// TestSkillDocStatesNoBareInstanceTally enforces the document's own rule against
// itself: a session-relative count rots, and this one had already rotted inside
// the same file (three later instances were added after the tally was written).
//
// Scope notes, stated because the test is easy to over-read:
//
//   - It guards NUMERAL tallies of the instance corpus only. Spelled-out counts
//     enumerated in place ("Four times in that one session", "Three instances of
//     that one principle") are deliberately not matched — they are self-verifying
//     from the sentence that carries them, and prose is the permitted floor
//     idiom. `instance 17` is a stable ID, not a count, and every pattern
//     requires the digit BEFORE a plural "instances", which
//     TestBareTallyPatternsSpareStableIDsAndProseCounts pins.
//   - The ban is FENCE-BLIND and whole-file: it cannot tell a rule from a quoted
//     counterexample, so this document — which teaches by quoting bad forms —
//     cannot illustrate the rot by re-quoting "21 independent instances" even
//     inside a fence. That is deliberate (the stricter direction, and a loud
//     failure), but the remedy is to reword, NOT to widen the scan.
func TestSkillDocStatesNoBareInstanceTally(t *testing.T) {
	const where = "testing-practices SKILL.md"
	body := readRepoFile(t, ".claude", "skills", "testing-practices", "SKILL.md")

	// Floor of 4 = 3 detectors + 1 diagnostic namer (see bareTallyPatterns). The
	// sibling prohibition in TestSkillDocBoundsPerFixAttributionClaim floors at 1
	// for the opposite reason — there, a narrower duplicate would have added a
	// name for a shape that pattern already reports.
	forbidPatterns(t, where, body, "selection effect", 4, bareTallyPatterns())
}

// bareTallyPatterns is shared by the prohibition and its false-positive control,
// so the control cannot drift away from the patterns it vouches for.
//
// Independence, honestly: these four are NOT four independent detectors. The
// corpus-tally pattern subsumes any "<n> instances" phrasing, so a "5 of the 21
// instances" or "those 21 instances" form is caught by it alone — the narrower
// entries exist to NAME which shape was found in the failure message, which is
// the difference between a maintainer finding line 229 and grepping the file.
// The stratum patterns are the genuinely independent ones: they catch counts
// attached to a stratum name with no "instances" nearby, and there are two
// because the four strata are worded two ways ("N in committed … code",
// "N in/at ad-hoc/coordination").
func bareTallyPatterns() []forbidReq {
	return []forbidReq{
		mdForbid("a bare corpus tally", "<n> instances", `\b\d+ (independent )?instances\b`,
			"21 independent instances"),
		mdForbid("a bare share-of-corpus count (diagnostic naming; subsumed by <n> instances)",
			"<n> of the <n> instances", `\b\d+ of (the|those) \d+ instances\b`, "5 of the 21 instances"),
		mdForbid("a bare committed-code stratum count", "<n> in committed <x> code",
			`\b\d+ in committed \w+ code\b`, "6 in committed harness code"),
		mdForbid("a bare count on the ad-hoc or coordination stratum", "<n> in/at ad-hoc|coordination",
			`\b\d+ (in|at) (the )?(ad-hoc|coordination)`, "5 in ad-hoc agent tooling"),
	}
}

// TestBareTallyPatternsSpareStableIDsAndProseCounts bounds the prohibition in the
// other direction. The patterns above are the only thing standing between the doc
// and a false FAILURE on legitimate text, and the two forms most at risk are a
// stable instance ID (`instance 17`, digit AFTER a singular noun) and a
// spelled-out count enumerated in place. Without this control, tightening a
// pattern to catch one more tally could silently forbid both.
func TestBareTallyPatternsSpareStableIDsAndProseCounts(t *testing.T) {
	legitimate := []string{
		"concurrence is not evidence (instance 17) inverted",
		"Per instance 21: state the bounded claim",
		"Four times in that one session a measuring apparatus produced a zero",
		"Three instances of that one principle — derive the right one",
		"more than twenty independent instances",
		"near-evenly across four strata; none of the four is a rounding error",
	}

	// Both axes are floored. `legitimate` is the one that determines how much this
	// test asserts — flooring only the pattern list would let a truncated input
	// list report green.
	const minLegitimate, minPatterns = 6, 4
	patterns := bareTallyPatterns()
	if len(legitimate) < minLegitimate || len(patterns) < minPatterns {
		t.Fatalf("assertion-count floor — %d control strings x %d patterns, expected at least %d x %d; the list was truncated and this check measured less than it claims",
			len(legitimate), len(patterns), minLegitimate, minPatterns)
	}

	checked := 0
	for _, text := range legitimate {
		for _, p := range patterns {
			checked++
			if p.re.MatchString(normalizeMD(text)) {
				t.Errorf("pattern %s for %s false-flags legitimate text %q", p.label, p.concept, text)
			}
		}
	}
	if want := len(legitimate) * len(patterns); checked != want {
		t.Fatalf("ran %d pattern/string checks, expected %d", checked, want)
	}
}

// TestSkillDocExpressesInstanceCorpusAsFloorNotTally is the positive half of the
// rule above. The ban creates deletion pressure — the cheapest way to satisfy it
// is to delete the sentences carrying the numerals — so what the numerals were
// attached to has to be pinned independently.
//
// Honest labelling of what is red-first here: the floor form and the
// floor-not-a-tally reasoning failed before the fix. The four stratum names, the
// proportional framing, and the throwaway-tooling scope are DELETION guards that
// pass today except where the fix rewords them; they were demonstrated by
// deleting the distribution sentence and the ad-hoc-tooling sentence.
//
// The floor pattern deliberately admits only PROSE quantities. A numeric "at
// least 21 independent instances" is not offered as an option, because the ban in
// TestSkillDocStatesNoBareInstanceTally would reject it — two tests offering
// contradictory advice is worse than one narrow option.
func TestSkillDocExpressesInstanceCorpusAsFloorNotTally(t *testing.T) {
	const where = "testing-practices SKILL.md ## " + assertionRigorSection
	body := readRepoFile(t, ".claude", "skills", "testing-practices", "SKILL.md")
	section := mdSection(t, "testing-practices SKILL.md", body, assertionRigorSection)

	required := []phraseReq{
		mdPat("the corpus size as a prose floor", "more than twenty / at least twenty",
			`more than twenty|at least twenty`),
		mdPat("that the figure is a floor rather than a tally", "a floor, not a tally", `floor,? not a tally`),
		mdPat("the distribution qualitatively rather than as counts", "near-evenly across four strata",
			`near-evenly`),
		mdLit("the committed-harness stratum", "committed harness code"),
		mdLit("the committed-product stratum", "committed product code"),
		mdLit("the ad-hoc tooling stratum", "ad-hoc agent tooling"),
		mdLit("the coordination-layer stratum", "coordination/claim layer"),
		// The point the ban would otherwise let a maintainer delete along with
		// the numeral: the rule covers tooling you throw away, not just
		// committed tests.
		mdLit("that the rule covers throwaway tooling", "throwaway agent orchestration"),
	}

	requirePhrases(t, where, section, 8, required)
}

// TestSkillDocBoundsPerFixAttributionClaim pins the corrected coverage claim for
// the worked example's negative control. `82e0535` changed THREE things; the
// control's 8 failures attribute to two of them, and the third (the `fromjson? //
// empty` torn-line tolerance) has no red-first evidence — the suite's torn-frame
// assertions pass against the pre-fix helper too. The doc must state that bound
// rather than let a 7+1 attribution read as three-for-three.
//
// Red-first vs deletion guard, per requirement: the coverage prohibition, the
// two-of-three bound, the named third change, and "no red-first evidence" all
// failed before the fix. `disjoint` and "report the attribution, not the count"
// are DELETION guards — they pass today, and they are here because the
// prohibition must not be satisfiable by deleting the attribution point along
// with the over-claim.
//
// Wording-lock, admitted: "no red-first evidence" and "floor, not a tally" in the
// sibling test pin a rhetorical phrase chosen for the test, so a truthful reword
// ("was never demonstrated red") fails. They constrain wording, not meaning; the
// mechanism-named requirements (`fromjson? // empty`, `82e0535`) are the ones
// carrying meaning.
func TestSkillDocBoundsPerFixAttributionClaim(t *testing.T) {
	const where = "testing-practices SKILL.md ### " + workedExampleSection
	body := readRepoFile(t, ".claude", "skills", "testing-practices", "SKILL.md")
	section := mdSubsection(t, "testing-practices SKILL.md", body, workedExampleSection)

	// One prohibition, worded to cover fix/change phrasing in one pattern rather
	// than two — a narrower duplicate would inflate the floor without adding a
	// detector.
	forbidPatterns(t, where, section, "82e0535", 1, []forbidReq{
		mdForbid("that every change is independently covered", "each fix|change is independently covered",
			`each (fix|change) is independently covered`,
			"newest-by-mtime fix — so each fix is independently covered, rather than one"),
	})

	required := []phraseReq{
		// Proximity-matched to "change", so an unrelated "two of three" elsewhere
		// in a 60-line subsection cannot satisfy the bound.
		mdPat("that the attribution bounds at two of the three changes", "two of the three … change",
			`two of (the )?three[^.]{0,80}chang|chang[^.]{0,80}two of (the )?three`),
		mdLit("the third change, named by mechanism", "fromjson? // empty"),
		mdPat("that the third change is undemonstrated", "no red-first evidence", `no red-first evidence`),
		mdLit("that the two attributed sets are disjoint", "disjoint"),
		mdLit("the retained attribution-over-count point", "report the attribution, not the count"),
	}

	requirePhrases(t, where, section, 5, required)
}

// Subsection headings the two additions live under, spelled once so a rename
// cannot leave the guard scoped to a section that no longer exists.
const (
	sameBreathSection      = "The same breath"
	preconditionSection    = "A precondition that never holds makes the guard a no-op"
	intendedOutcomeSection = "Assert the intended outcome, not the current mechanism"
)

// TestSkillDocStatesSetupPreconditionClass pins the setup-precondition mechanism:
// a precondition that never holds converts the guard behind it into a no-op, and
// unlike a vacuous assertion it is LOUD — it presents as a flaky row, so the
// failure is misfiled rather than ignored. All of these are red-first: none of
// this text exists in the document today.
//
// Scoped to the subsection, not to `## Assertion Rigor`: at section scope,
// deleting the subsection would keep passing as long as `QUM-830` or
// `sendnow-tui` survived anywhere in ~500 lines.
func TestSkillDocStatesSetupPreconditionClass(t *testing.T) {
	const where = "testing-practices SKILL.md ### " + preconditionSection
	body := readRepoFile(t, ".claude", "skills", "testing-practices", "SKILL.md")
	section := mdSubsection(t, "testing-practices SKILL.md", body, preconditionSection)

	required := []phraseReq{
		mdLit("the class by name", "setup precondition"),
		mdPat("that setup failures must be reported separately from assertion failures",
			"separate class from assertion failures", `separate class[^.]{0,40}assertion failure`),
		mdPat("the distinction a row must draw", "the scenario never started", `never started|never ran to completion`),
		mdPat("that the correct reading is a dead guard, not an intermittent fault",
			"no-op … not flaky", `no-op[^.]{0,120}flaky|flaky[^.]{0,120}no-op`),
		// The live instance, by row name and by the fix it leaves unguarded —
		// without these the subsection is a rule with no evidence behind it.
		mdLit("the live instance by row name", "sendnow-tui"),
		mdLit("the shipped fix whose LIVE gate is a no-op", "QUM-830"),
	}

	requirePhrases(t, where, section, 6, required)
}

// TestSkillDocLeadsWithSameBreathRule pins the causal statement behind red-first —
// that a test written alongside the mechanism cannot disagree with its author, so
// it is INDEPENDENCE and not colour that carries the evidence — and pins its
// PLACEMENT ahead of the how-to-demonstrate-a-red material it explains.
//
// Every phrase leg is red-first (none of this text exists today), and each is
// scoped to the subsection that must carry it: the rule and its consequences to
// `### The same breath`, the instance and its sub-lesson to the mechanism-vs-
// outcome subsection they belong to.
//
// Wording-lock, admitted on the same terms as the sibling test: "suspect until
// something independent", "three self-pinning tests", and "looks equivalent … is
// not equivalent" pin rhetorical phrases chosen for the test, so a truthful
// reword fails them. They constrain wording; the placement leg is the only one
// here constraining structure.
func TestSkillDocLeadsWithSameBreathRule(t *testing.T) {
	body := readRepoFile(t, ".claude", "skills", "testing-practices", "SKILL.md")

	rule := mdSubsection(t, "testing-practices SKILL.md", body, sameBreathSection)
	requirePhrases(t, "testing-practices SKILL.md ### "+sameBreathSection, rule, 4, []phraseReq{
		mdLit("the same-breath rule", "same breath as the mechanism"),
		mdPat("that independence rather than colour carries the evidence",
			"independence, not colour", `independence[^.]{0,80}colou?r|colou?r[^.]{0,80}independence`),
		mdPat("the hole in TDD-as-practiced", "writing the test first does not make it independent",
			`first does not make it independent|not independent if you write it`),
		mdPat("the practical default for a same-diff green test", "suspect until something independent",
			`suspect until something independent`),
	})

	instance := mdSubsection(t, "testing-practices SKILL.md", body, intendedOutcomeSection)
	requirePhrases(t, "testing-practices SKILL.md ### "+intendedOutcomeSection, instance, 2, []phraseReq{
		mdPat("that three self-pinning tests on one flag make it a pattern, not a slip",
			"three self-pinning tests", `three self-pinning tests`),
		mdPat("the phase-vs-identity sub-lesson", "looks equivalent is not equivalent",
			`looks equivalent[^.]{0,30}is not equivalent`),
	})

	// Placement: the rule must precede the red-demonstration how-to it explains.
	// Section scope is required here — the check needs both anchors in one body.
	section := mdSection(t, "testing-practices SKILL.md", body, assertionRigorSection)
	ruleAt := strings.Index(normalizeMD(section), normalizeMD("same breath as the mechanism"))
	redAt := strings.Index(normalizeMD(section), normalizeMD("### How to demonstrate a red"))
	if ruleAt < 0 || redAt < 0 {
		t.Fatalf("placement check is vacuous: same-breath rule at %d, red-demonstration heading at %d — one of the two anchors is missing",
			ruleAt, redAt)
	}
	if ruleAt > redAt {
		t.Errorf("the same-breath rule appears AFTER \"How to demonstrate a red\" (offsets %d > %d); it explains why red-first works and has to lead it",
			ruleAt, redAt)
	}
}

// TestMDSectionLevelBoundaries controls the mdSection → mdSectionLevel refactor.
// The real document cannot reveal a bug in the `###` path: it has no `####`
// heading, and every requirement in the attribution test happens to be
// insensitive to over-scoping, so a boundary check that never fired would look
// identical to a correct one.
func TestMDSectionLevelBoundaries(t *testing.T) {
	const body = "# Doc\n\nintro\n\n## Chapter\n\n### Alpha\n\nalpha body\n\n#### Deeper\n\ndeeper body\n\n" +
		"```\n## FencedTwo\n### FencedThree\n```\n\nfence tail\n\n### Beta\n\nbeta body\n\n## Next\n\nnext body\n"

	cases := []struct {
		name            string
		prefix, heading string
		wantContains    []string
		wantNotContains []string
	}{
		{
			name: "### ends at the next ###", prefix: "###", heading: "Alpha",
			wantContains:    []string{"alpha body", "deeper body", "fence tail", "## FencedTwo"},
			wantNotContains: []string{"beta body", "next body"},
		},
		{
			name: "### ends at the next ##", prefix: "###", heading: "Beta",
			wantContains:    []string{"beta body"},
			wantNotContains: []string{"next body", "alpha body"},
		},
		{
			name: "## spans its subsections", prefix: "##", heading: "Chapter",
			wantContains:    []string{"alpha body", "beta body", "deeper body"},
			wantNotContains: []string{"next body", "intro"},
		},
		{
			// The one depth relation nothing else exercises: a section opened AT
			// `####` must end at the shallower `###` that follows.
			name: "#### ends at the next ###", prefix: "####", heading: "Deeper",
			wantContains:    []string{"deeper body", "fence tail"},
			wantNotContains: []string{"beta body", "alpha body"},
		},
	}
	if len(cases) < 4 {
		t.Fatalf("assertion-count floor — %d boundary cases, expected at least 4", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mdSectionLevel(t, "synthetic", body, tc.prefix, tc.heading)
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("section %s %q omits %q; got:\n%s", tc.prefix, tc.heading, want, got)
				}
			}
			for _, unwanted := range tc.wantNotContains {
				if strings.Contains(got, unwanted) {
					t.Errorf("section %s %q leaks %q from beyond its boundary; got:\n%s", tc.prefix, tc.heading, unwanted, got)
				}
			}
		})
	}

	// Rejections, asserted on the DIAGNOSIS and not merely on "it failed": the
	// fence-parity error would otherwise be accepted as proof of correct heading
	// rejection.
	rejections := []struct {
		name, prefix, heading string
	}{
		{"a prefix-only heading is not an exact match", "###", "Alph"},
		{"a suffixed heading is not an exact match", "###", "Alpha (example)"},
		{"a fenced heading does not open a section", "##", "FencedTwo"},
	}
	if len(rejections) < 3 {
		t.Fatalf("assertion-count floor — %d rejection cases, expected at least 3", len(rejections))
	}
	for _, tc := range rejections {
		got, err := extractMDSection("synthetic", body, tc.prefix, tc.heading)
		switch {
		case err == nil:
			t.Errorf("%s: extractMDSection accepted %s %q and returned:\n%s", tc.name, tc.prefix, tc.heading, got)
		case !strings.Contains(err.Error(), "no \""+tc.prefix+" "+tc.heading+"\" section found"):
			t.Errorf("%s: rejected for the wrong reason — %v", tc.name, err)
		}
	}

	// Fence parity is diagnosed as itself, not as a missing section — the control
	// that keeps the checks above honest.
	if _, err := extractMDSection("synthetic", "## Chapter\n\n```\nunclosed\n", "##", "Chapter"); err == nil ||
		!strings.Contains(err.Error(), "odd number of ``` fence lines") {
		t.Errorf("an unbalanced fence must be diagnosed as a stray fence, got: %v", err)
	}
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

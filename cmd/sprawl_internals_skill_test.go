package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The sprawl-internals skill (QUM-1155) holds content RELOCATED out of
// CLAUDE.md, not a rewrite of it. These tests are byte-equality oracles, and
// their oracle is itself pinned by claudeMDRelocatedDigest below: without that
// pin, paraphrasing the source in the same commit would keep every equality test
// green, which is the cheapest possible fake pass.
//
// THIS IS THE CUT SLICE the original comment anticipated. The oracle was the
// live CLAUDE.md, which was sound only while no slice edited that file; the cut
// deleted every one of these sections from it, so the tests are now pointed at
// claudeMDFixture — the pre-cut text, frozen into the tree. They were NOT
// deleted: deleting them silently degrades "relocated verbatim" to
// "paraphrased" with nothing watching, and that fake pass would have been
// available to every slice in this issue.
//
// The digest constant below is UNCHANGED by the cut, and that is the check that
// the freeze was honest: the fixture is byte-identical to the CLAUDE.md that
// produced it. Never regenerate the fixture from a post-cut CLAUDE.md — that
// would make every assertion here vacuous rather than red.

const (
	sprawlInternalsSkill = ".claude/skills/sprawl-internals/SKILL.md"
	sprawlInternalsStub  = ".agents/skills/sprawl-internals/SKILL.md"

	// claudeMDFixture is CLAUDE.md as of c7093cc, the commit the relocation was
	// performed against.
	claudeMDFixture = "cmd/testdata/claude-md-c7093cc.md"
	// claudeMDOracleName is the display name used in failure messages, so a
	// diff never implies the live file still contains this text.
	claudeMDOracleName = "CLAUDE.md@c7093cc"
)

// readClaudeMDOracle returns the frozen pre-cut CLAUDE.md. See the file comment
// above for why this is a fixture and not the live file.
func readClaudeMDOracle(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, filepath.FromSlash(claudeMDFixture))
}

// claudeMDRelocatedDigest pins the CLAUDE.md text this slice relocates. Update
// it ONLY as a deliberate act, alongside the corresponding skill edit — a
// surprise mismatch means the oracle moved under the copy.
const claudeMDRelocatedDigest = "670456633948f2ed892af0a91eabcef06ad8e8e103476918f7af4fa8a20ff0e5"

func readSprawlInternalsSkill(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, filepath.FromSlash(sprawlInternalsSkill))
}

// relocatedSourceTexts returns, in slice order, every span of CLAUDE.md this
// slice relocates. It is both the digest input and the single place the source
// spans are named.
func relocatedSourceTexts(t *testing.T) []string {
	t.Helper()

	claudeMD := readClaudeMDOracle(t)
	return []string{
		mdSection(t, claudeMDOracleName, claudeMD, "Lifecycle model (QUM-786)"),
		mdSection(t, claudeMDOracleName, claudeMD, "Build & Test"),
		mdSection(t, claudeMDOracleName, claudeMD, "Install"),
		mdSection(t, claudeMDOracleName, claudeMD, "Project Configuration"),
		mdSection(t, claudeMDOracleName, claudeMD, "Repo Layout"),
		mdSection(t, claudeMDOracleName, claudeMD, "Code Patterns"),
		mdSection(t, claudeMDOracleName, claudeMD, "Linting & Formatting"),
	}
}

func TestCLAUDEMDRelocationOracleIsPinned(t *testing.T) {
	sum := sha256.Sum256([]byte(strings.Join(relocatedSourceTexts(t), "\n\x00\n")))
	got := hex.EncodeToString(sum[:])
	if got != claudeMDRelocatedDigest {
		t.Fatalf("CLAUDE.md's relocated sections changed: digest %s, pinned %s.\n"+
			"If that change is intended, re-relocate the affected text into %s and update claudeMDRelocatedDigest in the same commit.",
			got, claudeMDRelocatedDigest, sprawlInternalsSkill)
	}
}

// Frontmatter is read with the SAME parser cmd/skills_sync_test.go uses, so the
// two tests cannot return contradictory verdicts on one file (that parser strips
// surrounding quotes; a hand-rolled one here did not).
//
// Note 3 of the 9 pre-existing .claude skills carry no frontmatter at all. This
// test imposes the convention deliberately — frontmatter is what makes the skill
// discoverable in the skill listing — rather than following house style.
func TestSprawlInternalsSkillHasFrontmatter(t *testing.T) {
	meta := readSkillMetadata(t, filepath.Join(repoRootFromTest(t), filepath.FromSlash(sprawlInternalsSkill)))

	if meta.name != "sprawl-internals" {
		t.Errorf("%s: frontmatter name = %q, want %q", sprawlInternalsSkill, meta.name, "sprawl-internals")
	}
	if meta.description == "" {
		t.Errorf("%s: frontmatter description must be non-empty", sprawlInternalsSkill)
	}
}

// TestSprawlInternalsSkillRelocatesSectionsVerbatim is the fidelity oracle: byte
// equality against CLAUDE.md's section bodies. A summary or a reflow passes any
// presence check and fails this one, which is the whole point — the QUM-1155
// governing principle is that moving content audits it and rewriting it does not.
//
// The floor below bounds THIS test, not the slice: 7 spans are relocated and only
// these 4 are whole-section copies. Note also that a section body includes the
// blank line before the next heading, so a stray extra trailing newline at the
// end of the skill fails here with a diff that looks empty.
func TestSprawlInternalsSkillRelocatesSectionsVerbatim(t *testing.T) {
	sections := []string{
		"Install",
		"Project Configuration",
		"Repo Layout",
		"Linting & Formatting",
	}
	if len(sections) < 4 {
		t.Fatalf("assertion-count floor: expected at least 4 whole-section copies, have %d", len(sections))
	}

	claudeMD := readClaudeMDOracle(t)
	skill := readSprawlInternalsSkill(t)

	for _, heading := range sections {
		want := mdSection(t, claudeMDOracleName, claudeMD, heading)
		got := mdSection(t, sprawlInternalsSkill, skill, heading)
		if got != want {
			t.Errorf("%s: section %q is not byte-identical to CLAUDE.md's\n--- CLAUDE.md ---\n%q\n--- skill ---\n%q",
				sprawlInternalsSkill, heading, want, got)
		}
	}
}

// The lifecycle section's closing cross-reference must be repointed at the
// e2e-matrix skill. Both halves are asserted: without the negative half,
// appending the new pointer while leaving the stale "under **Validating
// Changes**" wording would pass.
//
// QUM-1186 removed this test's byte-equality arm on the CONTRACT text, and the
// removal is the point rather than a concession. The arm required the skill's
// lifecycle contract to stay byte-identical to `CLAUDE.md@c7093cc` — a fixture
// frozen in 2026 — so it kept passing only while the contract never changed.
// The moment the contract DID change, the fixture stopped being an oracle and
// started being an anti-correctness gate: it demanded the shipped skill go on
// documenting `delegate(<state>, task)` gates for a tool that no longer exists,
// and it would have gone red at whoever fixed that. A test that fails BECAUSE
// you corrected the documentation is describing the past, not guarding the
// present.
//
// What replaces it has a live subject: TestSprawlInternalsSkillLifecycleNamesRealStatuses
// below checks the section against `internal/state/state.go`, which is the
// thing the contract is supposed to be true of. The relocation-fidelity
// oracles that are still meaningful — the four whole-section copies, the
// Build & Test block, the Code Patterns subset, and the digest pin on the
// fixture itself — are untouched.
func TestSprawlInternalsSkillRewritesLifecycleCrossReference(t *testing.T) {
	const heading = "Lifecycle model (QUM-786)"
	const crossRefMarker = "Touched-file matrix-row mapping"
	const staleWording = "under **Validating Changes**"

	claudeSection := mdSection(t, claudeMDOracleName, readClaudeMDOracle(t), heading)
	skillSection := mdSection(t, sprawlInternalsSkill, readSprawlInternalsSkill(t), heading)

	// Positive control for the negative assertion below: the banned wording must
	// be findable in the source, or its absence from the skill proves nothing.
	// (It survives a line wrap in CLAUDE.md at "the table\nunder **Validating".)
	if !strings.Contains(claudeSection, staleWording) {
		t.Fatalf("%s %q: control failed — %q is not present in the source, so asserting its absence is vacuous",
			claudeMDOracleName, heading, staleWording)
	}

	_, skillCrossRef, ok := strings.Cut(skillSection, crossRefMarker)
	if !ok {
		t.Fatalf("%s: %q section is missing the %q paragraph", sprawlInternalsSkill, heading, crossRefMarker)
	}
	if !strings.Contains(skillCrossRef, ".claude/skills/e2e-matrix/SKILL.md") {
		t.Errorf("%s: cross-reference must cite `.claude/skills/e2e-matrix/SKILL.md` by path, got: %q",
			sprawlInternalsSkill, skillCrossRef)
	}
	// The row name is what makes the pointer actionable; citing the skill file
	// alone sends the reader to a table with ~30 rows.
	if !strings.Contains(skillCrossRef, "complete-lifecycle") {
		t.Errorf("%s: cross-reference must still name the `complete-lifecycle` row, got: %q",
			sprawlInternalsSkill, skillCrossRef)
	}
	if strings.Contains(skillSection, staleWording) {
		t.Errorf("%s: stale cross-reference %q to CLAUDE.md's own table survives", sprawlInternalsSkill, staleWording)
	}
}

// statusIdentRe matches a backticked Go status identifier as the lifecycle
// section writes them, e.g. `StatusComplete`.
var statusIdentRe = regexp.MustCompile("`(Status[A-Za-z]+)`")

// TestSprawlInternalsSkillLifecycleNamesRealStatuses replaces the byte-equality
// arm removed above with an assertion whose subject is the code, not a frozen
// copy of a document. Every `StatusXxx` the lifecycle contract names must be a
// real constant in internal/state/state.go, and the contract must cover the
// statuses that exist — so both a stale name left behind by a deletion and a
// newly added status nobody documented are caught.
//
// This is deliberately narrower than byte equality. It cannot detect a
// paraphrase, which byte equality could; that trade is the price of a gate that
// tracks the code instead of forbidding the code to change.
func TestSprawlInternalsSkillLifecycleNamesRealStatuses(t *testing.T) {
	const heading = "Lifecycle model (QUM-786)"

	stateSrc := readRepoFile(t, filepath.FromSlash("internal/state/state.go"))
	skillSection := mdSection(t, sprawlInternalsSkill, readSprawlInternalsSkill(t), heading)

	// Control for the whole test: the oracle must actually contain status
	// declarations, or every check below passes by finding nothing.
	declared := regexp.MustCompile(`(?m)^\s*(Status[A-Za-z]+)\s*=`).FindAllStringSubmatch(stateSrc, -1)
	if len(declared) < 5 {
		t.Fatalf("control failed: internal/state/state.go declares %d Status constants, expected at least 5 — the oracle is stale or the regexp stopped matching", len(declared))
	}
	declaredSet := map[string]bool{}
	for _, m := range declared {
		declaredSet[m[1]] = true
	}

	named := statusIdentRe.FindAllStringSubmatch(skillSection, -1)
	if len(named) < 3 {
		t.Fatalf("control failed: the %q section names %d `StatusXxx` identifiers, expected at least 3 — the section was gutted or the regexp stopped matching", heading, len(named))
	}
	for _, m := range named {
		if !declaredSet[m[1]] {
			t.Errorf("%s %q: names %q, which is not declared in internal/state/state.go — the contract documents a status that does not exist",
				sprawlInternalsSkill, heading, m[1])
		}
	}

	// The statuses whose gate behaviour the contract exists to state. StatusIdle
	// (QUM-1186 D2) is the one most likely to be missed: an undocumented resting
	// state reads to an operator as "complete", which is the exact confusion the
	// separate constant was added to prevent.
	for _, must := range []string{"StatusComplete", "StatusIdle"} {
		if !declaredSet[must] {
			t.Fatalf("control failed: %q is not declared in internal/state/state.go, so requiring the skill to document it is vacuous", must)
		}
		if !strings.Contains(skillSection, "`"+must+"`") {
			t.Errorf("%s %q: does not document %q, a resting state agents and operators must be able to tell apart",
				sprawlInternalsSkill, heading, must)
		}
	}

	// The contract must not describe the gate in terms of tools QUM-1186 deleted.
	for _, banned := range []string{"delegate(", "report_status"} {
		if strings.Contains(skillSection, banned) {
			t.Errorf("%s %q: still describes the gate via the deleted tool %q; send_message is the only entry point",
				sprawlInternalsSkill, heading, banned)
		}
	}
}

// TestSprawlInternalsSkillCopiesBuildTestCommandBlock — QUM-1236 retired this
// test's WHOLE-BLOCK byte-equality arm, for the same reason and by the same
// precedent as the lifecycle arm retired above.
//
// The arm required the skill's `## Build & Test` fence to reproduce
// `CLAUDE.md@c7093cc` byte for byte. Its only oracle was a 2026 fixture; it
// never read the Makefile. So it did not guard the build — it FORBADE the skill
// from tracking the build, and it enforced three false statements:
// a `validate` prerequisite list missing five entries (`hooks-armed`,
// `test-lint-pin`, `test-e2e-lockwait-unit`, `test-always-loaded-budget-unit`,
// `always-loaded-budget`), and `make hooks # install pre-commit hook` when the
// recipe installs two hooks, the second of which CLAUDE.md leans on as the
// backstop `--no-verify` cannot skip. A test that fails BECAUSE you corrected
// the documentation is describing the past, not guarding the present.
//
// What is KEPT, deliberately:
//   - A NARROWED byte pin (`buildTestPinnedSpans`) on the parts the Makefile
//     does not govern: the two `scripts/*.sh` invocations and the race-gate
//     ordering rationale. That reasoning is not derivable from any live file,
//     so a fixture is still its best oracle.
//   - The negative half below, unchanged. It reads `claudeSection` only as a
//     positive control for an absence claim, which is still sound.
//   - `claudeMDRelocatedDigest` and `relocatedSourceTexts`, untouched. The
//     fixture was NOT regenerated.
//
// What replaces the deleted arm has a live subject:
// TestSprawlInternalsSkillBuildTargetsMatchMakefile below reads `Makefile`.

// buildTestPinnedSpans are the fragments of CLAUDE.md's Build & Test block that
// remain byte-pinned. Each is reasoning or a script path, never a claim about
// `validate`'s prerequisites — those belong to the Makefile oracle.
var buildTestPinnedSpans = []string{
	"scripts/smoke-test-memory.sh   # integration test for weave memory system",
	"scripts/sprawl-test-env.sh     # set up isolated test environment",
	"race-gate runs BEFORE test-race on purpose",
}

func TestSprawlInternalsSkillCopiesBuildTestCommandBlock(t *testing.T) {
	claudeSection := mdSection(t, claudeMDOracleName, readClaudeMDOracle(t), "Build & Test")
	skillSection := mdSection(t, sprawlInternalsSkill, readSprawlInternalsSkill(t), "Build & Test")

	if len(buildTestPinnedSpans) < 3 {
		t.Fatalf("assertion-count floor: expected at least 3 pinned spans, have %d", len(buildTestPinnedSpans))
	}
	for _, span := range buildTestPinnedSpans {
		// Control: the span must exist in the FIXTURE, or pinning the skill to
		// it asserts nothing about the relocation being faithful.
		if !strings.Contains(claudeSection, span) {
			t.Fatalf("%s ## Build & Test: control failed — pinned span %q is absent from the fixture, so requiring it in the skill is not a fidelity check", claudeMDOracleName, span)
		}
		if !strings.Contains(skillSection, span) {
			t.Errorf("%s ## Build & Test: pinned span %q is not reproduced verbatim from %s",
				sprawlInternalsSkill, span, claudeMDOracleName)
		}
	}

	for _, banned := range []string{
		"guarantees about data races",
		"atomicDuration",
	} {
		if !strings.Contains(claudeSection, banned) {
			t.Fatalf("CLAUDE.md ## Build & Test: control failed — %q absent from the source, so asserting its absence is vacuous", banned)
		}
		if strings.Contains(skillSection, banned) {
			t.Errorf("%s ## Build & Test: carries %q from the out-of-slice race-detector subsection, which belongs to the testing-practices slice",
				sprawlInternalsSkill, banned)
		}
	}
}

// Code Patterns is a PARAGRAPH SUBSET: the dependency-injection paragraph and the
// two skill pointers travel here; the testing-rigor paragraphs are routed to
// testing-practices by a sibling slice. The negative half carries the claim — a
// lazy copy-the-whole-section implementation passes the positive half alone.
func TestSprawlInternalsSkillCodePatternsIsParagraphSubset(t *testing.T) {
	const where = claudeMDOracleName + " ## Code Patterns"

	claudeSection := mdSection(t, claudeMDOracleName, readClaudeMDOracle(t), "Code Patterns")
	skillSection := mdSection(t, sprawlInternalsSkill, readSprawlInternalsSkill(t), "Code Patterns")

	// QUM-1236 dropped "**Dependency injection**" from this list. That paragraph
	// cited `internal/agentops/report.go`, deleted by QUM-1186, so the byte pin
	// was forbidding the correction of a dead citation — and it collided head-on
	// with TestSkillsCitedPathsExist, whose entire purpose is to fail on exactly
	// that. Excusing it there instead would have carved the escape hatch that let
	// the other skills rot, in the file this work exists to fix.
	//
	// Verifying it also turned up a SECOND error the byte pin was preserving: the
	// paragraph said the nil-defaulting accessors live in `internal/agentops`.
	// They do not — no Deps type there has one. Nil-defaulting is `resolveXxxDeps`
	// in `cmd/` (`cmd/merge.go`, `cmd/gc.go`, `cmd/logs.go`). The paragraph
	// misdescribed the pattern it exists to teach.
	//
	// The two skill pointers stay pinned: they cite skills, not code, so nothing
	// in the tree can rot them.
	keep := []string{
		"**Read `/go-cli-best-practices`",
		"**Read `/cli-ux-best-practices`",
	}
	if len(keep) < 2 {
		t.Fatalf("assertion-count floor: expected at least 2 verbatim-pinned paragraphs, have %d", len(keep))
	}
	for _, lead := range keep {
		// Edge whitespace is trimmed off each paragraph; the interior — which is
		// where a paraphrase would show — is compared byte for byte.
		para := paragraphStartingWith(t, where, claudeSection, lead)
		if !strings.Contains(skillSection, para) {
			t.Errorf("%s ## Code Patterns: paragraph starting %q is not reproduced verbatim\n--- want ---\n%s",
				sprawlInternalsSkill, lead, para)
		}
	}

	notThere := []string{
		"**Tests required**",
		"Every new assertion must demonstrate it CAN fail",
		"A watched failure proves the instrument works",
		"No fallback branch may silently succeed",
	}
	for _, banned := range notThere {
		// Positive control: a typo here, or a CLAUDE.md rewording, would make the
		// absence check below silently vacuous.
		if !strings.Contains(claudeSection, banned) {
			t.Fatalf("%s: control failed — %q is not present in the source, so asserting its absence is vacuous", where, banned)
		}
		if strings.Contains(skillSection, banned) {
			t.Errorf("%s ## Code Patterns: carries %q, which belongs to the testing-practices slice",
				sprawlInternalsSkill, banned)
		}
	}
}

// paragraphStartingWith returns the blank-line-delimited paragraph of body whose
// first line begins with lead. It fatals unless there is exactly one, so a
// duplicated lead cannot silently pick the wrong paragraph.
func paragraphStartingWith(t *testing.T, where, body, lead string) string {
	t.Helper()

	var found []string
	for _, para := range strings.Split(body, "\n\n") {
		if strings.HasPrefix(strings.TrimSpace(para), lead) {
			found = append(found, strings.TrimSpace(para))
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s: found %d paragraphs starting %q, want exactly 1 — the oracle is stale", where, len(found), lead)
	}
	return found[0]
}

// The stub's pointer is RESOLVED, not just string-matched: a typo duplicated
// between the test and the stub would otherwise pass.
func TestSprawlInternalsAgentsStubPointsAtClaudeSkill(t *testing.T) {
	const target = "../../../.claude/skills/sprawl-internals/SKILL.md"

	if !strings.Contains(readRepoFile(t, filepath.FromSlash(sprawlInternalsStub)), target) {
		t.Fatalf("%s: must name %q as the source of truth", sprawlInternalsStub, target)
	}

	stubDir := filepath.Join(repoRootFromTest(t), ".agents", "skills", "sprawl-internals")
	resolved := filepath.Join(stubDir, filepath.FromSlash(target))
	if _, err := os.Stat(resolved); err != nil {
		t.Errorf("%s: pointer %q does not resolve to a file (%s): %v", sprawlInternalsStub, target, resolved, err)
	}
}

// validatePrereqRE captures `validate`'s prerequisite list from the Makefile.
var validatePrereqRE = regexp.MustCompile(`(?m)^validate:[ \t]*(.*)$`)

// TestSprawlInternalsSkillBuildTargetsMatchMakefile replaces the byte-equality
// arm retired above with an assertion whose subject is the BUILD, not a frozen
// copy of a document. CLAUDE.md warns agents that "the Makefile is authoritative
// for what validate depends on; do not trust a prose list of its targets,
// including any you find in this repo's own docs" — and the list it was warning
// about was in the skill CLAUDE.md links on that same line. This makes the prose
// track the Makefile instead of needing that warning.
//
// Deliberately narrower than byte equality: it cannot detect a paraphrase. That
// is the price of a gate that tracks the build instead of forbidding the skill
// to be correct.
func TestSprawlInternalsSkillBuildTargetsMatchMakefile(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	skillSection := mdSection(t, sprawlInternalsSkill, readSprawlInternalsSkill(t), "Build & Test")

	m := validatePrereqRE.FindStringSubmatch(makefile)
	if m == nil {
		t.Fatalf("control failed: no `validate:` rule found in Makefile — the regexp stopped matching, so every check below is vacuous")
	}
	prereqs := strings.Fields(m[1])

	// Control: a truncated parse makes the loop below pass by iterating nothing.
	if len(prereqs) < 10 {
		t.Fatalf("control failed: parsed %d prerequisites from `validate:` (%q), expected at least 10 — the parse is broken", len(prereqs), m[1])
	}
	// Control on the fix itself: these five were the ones the frozen block
	// omitted. If the Makefile stops declaring them, this test must be revisited
	// rather than quietly asserting less than it was written to assert.
	for _, must := range []string{"hooks-armed", "test-lint-pin", "test-e2e-lockwait-unit", "test-always-loaded-budget-unit", "always-loaded-budget"} {
		if !slices.Contains(prereqs, must) {
			t.Fatalf("control failed: %q is no longer a `validate` prerequisite, so requiring the skill to list it is vacuous — re-derive this list from the Makefile", must)
		}
	}

	for _, p := range prereqs {
		if !strings.Contains(skillSection, p) {
			t.Errorf("%s ## Build & Test: does not name %q, a `validate` prerequisite per Makefile — an agent reading this under-counts what validate runs",
				sprawlInternalsSkill, p)
		}
	}

	// The INVERSE error, which is how W1b shipped: the skill asserted
	// `always-loaded-budget` was "parked outside validate on purpose" and
	// labelled the claim "accurate as of this tree", while Makefile listed it
	// as a prerequisite. Naming a target is not enough — naming it as EXCLUDED
	// is worse than omitting it.
	//
	// Scoped per BULLET rather than per section, so a true exclusion claim
	// about a genuinely excluded target (`test-e2e-matrix`) cannot taint a
	// bullet about an included one.
	for _, bullet := range strings.Split(stripFencedBlocks(skillSection), "\n- ") {
		if !excludedFromValidateRE.MatchString(bullet) {
			continue
		}
		for _, p := range prereqs {
			if regexp.MustCompile(`\bmake ` + regexp.QuoteMeta(p) + `\b`).MatchString(bullet) {
				t.Errorf("%s ## Build & Test: says `make %s` is outside `validate`, but Makefile lists it as a prerequisite. Bullet: %q",
					sprawlInternalsSkill, p, strings.TrimSpace(bullet))
			}
		}
	}
}

// stripFencedBlocks removes ```-fenced blocks. The inverse check below looks for
// a CLAIM that a target is excluded from validate, and a claim is prose: the
// command-listing fence merely names targets, so leaving it in made every target
// it lists inherit the exclusion sentence that follows the fence.
func stripFencedBlocks(md string) string {
	parts := strings.Split(md, "```")
	var out []string
	for i := 0; i < len(parts); i += 2 {
		out = append(out, parts[i])
	}
	return strings.Join(out, "\n")
}

// excludedFromValidateRE matches the ways this skill has phrased "this target is
// not in validate". Its control is TestExcludedFromValidateREMatchesTheShippedClaim.
var excludedFromValidateRE = regexp.MustCompile("(?i)(parked outside|not (?:in|part of)|never in|deliberately \\*?not\\*? in) `?validate`?")

// TestExcludedFromValidateREMatchesTheShippedClaim is the positive control for
// the regexp above, checked against the EXACT sentences that shipped the W1b
// defect. Without it a regexp that stopped matching would make the inverse
// check silently vacuous. These strings are historical artifacts: do NOT
// "correct" them to match the fixed skill — that deletes the control.
func TestExcludedFromValidateREMatchesTheShippedClaim(t *testing.T) {
	shipped := []string{
		"`make always-loaded-budget` is the LIVE always-loaded instruction-budget gate. It is parked outside `validate` on purpose",
		"Two notes on targets that are deliberately *not* in `validate`",
		"`make test-e2e-matrix` and its per-row targets are never in `validate`",
	}
	if len(shipped) < 3 {
		t.Fatalf("assertion-count floor: expected at least 3 shipped phrasings, have %d", len(shipped))
	}
	for _, claim := range shipped {
		if !excludedFromValidateRE.MatchString(claim) {
			t.Errorf("excludedFromValidateRE does not match the shipped claim %q — the inverse check in TestSprawlInternalsSkillBuildTargetsMatchMakefile is vacuous", claim)
		}
	}
	// Negative control: ordinary prose about validate must not trip it, or the
	// inverse check would flag correct documentation.
	for _, benign := range []string{
		"`make validate` is the gate, and the pre-commit hook runs it.",
		"run `make validate` before you commit",
	} {
		if excludedFromValidateRE.MatchString(benign) {
			t.Errorf("excludedFromValidateRE matches benign prose %q; it would flag correct documentation", benign)
		}
	}
}

// TestSprawlInternalsSkillDocumentsBothHooks pins the `make hooks` description
// against the recipe. CLAUDE.md leans on the reference-transaction hook as the
// backstop `--no-verify` cannot skip; a skill that names only the pre-commit
// hook hides the existence of the guard an agent is most likely to trip.
func TestSprawlInternalsSkillDocumentsBothHooks(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	skillSection := mdSection(t, sprawlInternalsSkill, readSprawlInternalsSkill(t), "Build & Test")

	hooks := []string{"pre-commit", "reference-transaction"}
	for _, h := range hooks {
		// Control: the recipe must actually install this hook.
		if !strings.Contains(makefile, ".git/hooks/"+h) {
			t.Fatalf("control failed: the `hooks` recipe does not install .git/hooks/%s, so requiring the skill to document it is vacuous", h)
		}
		if !strings.Contains(skillSection, h) {
			t.Errorf("%s ## Build & Test: `make hooks` description does not name the %q hook, which the recipe installs", sprawlInternalsSkill, h)
		}
	}
}

// TestSprawlInternalsSkillDependencyInjectionIsLive replaces the byte pin
// dropped from the Code Patterns `keep` list with checks whose subject is the
// code. Byte equality could catch a paraphrase; it also froze two errors into
// the document. This cannot catch a paraphrase, and that is the trade.
//
// Path citations in this paragraph are covered separately and mechanically by
// TestSkillsCitedPathsExist.
func TestSprawlInternalsSkillDependencyInjectionIsLive(t *testing.T) {
	section := mdSection(t, sprawlInternalsSkill, readSprawlInternalsSkill(t), "Code Patterns")

	// Each pair is (skill must say X, and X must be true of the tree).
	for _, tc := range []struct {
		phrase   string
		file     string
		evidence string
	}{
		{"type mergeDeps = agentops.MergeDeps", "cmd/merge.go", "type mergeDeps = agentops.MergeDeps"},
		{"resolveMergeDeps", "cmd/merge.go", "func resolveMergeDeps() *mergeDeps {"},
		{"MergeDeps", "internal/agentops/merge.go", "type MergeDeps struct {"},
	} {
		// Control: the claim must hold in the source, or requiring the skill to
		// state it is asserting a fiction.
		if src := readRepoFile(t, filepath.FromSlash(tc.file)); !strings.Contains(src, tc.evidence) {
			t.Fatalf("control failed: %s does not contain %q, so requiring the skill to document it is vacuous", tc.file, tc.evidence)
		}
		if !strings.Contains(section, tc.phrase) {
			t.Errorf("%s ## Code Patterns: does not mention %q", sprawlInternalsSkill, tc.phrase)
		}
	}

	// The specific misstatement the retired byte pin was preserving: nil-defaulting
	// lives in cmd/, not in internal/agentops.
	for _, f := range []string{"merge.go", "gc.go", "kill.go", "spawn.go", "retire.go"} {
		if strings.Contains(readRepoFile(t, filepath.FromSlash("internal/agentops/"+f)), "Deps) resolve") {
			t.Fatalf("control failed: internal/agentops/%s now has a resolve-style accessor; re-check the claim below before trusting it", f)
		}
	}
	if strings.Contains(section, "nil-defaulting accessors (`internal/agentops") {
		t.Errorf("%s ## Code Patterns: still places the nil-defaulting accessors in internal/agentops; they live in cmd/ as resolveXxxDeps", sprawlInternalsSkill)
	}
}
